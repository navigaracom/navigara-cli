package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/navigaracom/navigara-cli/internal/telemetry"
)

type setupBatch struct{ Name, SHA256 string }
type setupManifest struct {
	Version        int
	URL            string
	OrganizationID string `json:",omitempty"`
	Batches        []setupBatch
	// ImportedTools get ImportedUntil as their import checkpoint once every batch is accepted.
	ImportedUntil time.Time `json:",omitzero"`
	ImportedTools []string  `json:",omitempty"`
}

var batchName = regexp.MustCompile(`^(logs|metrics)-[0-9]+\.json\.gz$`)

func saveSetupBatches(dir, url, organizationID string) error {
	return saveSetupManifest(dir, setupManifest{URL: url, OrganizationID: organizationID})
}

func saveSetupManifest(dir string, m setupManifest) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	m.Version = 2
	for _, e := range entries {
		if !batchName.MatchString(e.Name()) {
			return errors.New("unexpected file in setup export")
		}
		data, err := readBatch(dir, e.Name())
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		m.Batches = append(m.Batches, setupBatch{e.Name(), hex.EncodeToString(sum[:])})
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeRequest(filepath.Join(dir, "setup.json"), data)
}

func loadSetupBatches(dir string) (setupManifest, error) {
	var m setupManifest
	data, exists, err := readSetupFile(filepath.Join(dir, "setup.json"))
	if err != nil {
		return m, err
	}
	if !exists || json.Unmarshal(data, &m) != nil {
		return m, errors.New("invalid setup manifest")
	}
	if m.Version == 1 {
		return m, errors.New("this payload was prepared by an older navigara; run navigara wizard again")
	}
	if m.Version != 2 {
		return m, errors.New("unsupported setup manifest version")
	}
	seen := map[string]bool{}
	for _, batch := range m.Batches {
		if !batchName.MatchString(batch.Name) || seen[batch.Name] {
			return m, errors.New("invalid or duplicate batch filename")
		}
		seen[batch.Name] = true
		data, err := readBatch(dir, batch.Name)
		if err != nil {
			return m, err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != batch.SHA256 {
			return m, fmt.Errorf("batch %s changed; refusing to resume", batch.Name)
		}
	}
	return m, nil
}

func readBatch(dir, name string) ([]byte, error) {
	data, exists, err := readSetupFile(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	if !exists || !validBatch(data) {
		return nil, errors.New("missing or invalid OTLP batch")
	}
	return data, nil
}

// validBatch reports whether a gzip body decodes to JSON within the receiver's limit.
func validBatch(data []byte) bool {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return false
	}
	plain, err := io.ReadAll(io.LimitReader(zr, maxOTLPBody+1))
	return err == nil && len(plain) <= maxOTLPBody && json.Valid(plain)
}

// sentLog lists the batches the receiver accepted, one name per line.
const sentLog = "sent.log"

// sendSetupBatches posts the frozen batches in dir. With track, it skips the batches listed in
// sent.log and adds each accepted batch to it.
func sendSetupBatches(ctx context.Context, dir string, s *sender, uploaded func(done, total int), track bool) error {
	m, err := loadSetupBatches(dir)
	if err != nil {
		return err
	}
	if s.base != otlpBase(m.URL) {
		return errors.New("resume destination differs from frozen upload")
	}
	if err := s.validate(); err != nil {
		return err
	}
	var log *os.File
	var logMu sync.Mutex
	if track {
		data, err := os.ReadFile(filepath.Join(dir, sentLog))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		sent := strings.Fields(string(data))
		m.Batches = slices.DeleteFunc(m.Batches, func(b setupBatch) bool { return slices.Contains(sent, b.Name) })
		if log, err = os.OpenFile(filepath.Join(dir, sentLog), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600); err != nil {
			return err
		}
		defer log.Close()
	}
	if uploaded != nil {
		uploaded(0, len(m.Batches))
	}
	if len(m.Batches) == 0 {
		return nil
	}

	jobs := make(chan setupBatch, 4)
	results := make(chan error, 4)
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var workers sync.WaitGroup
	worker := func() {
		defer workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case batch, ok := <-jobs:
				if !ok {
					return
				}
				if err := ctx.Err(); err != nil {
					return
				}
				data, err := readBatch(dir, batch.Name)
				if ctx.Err() != nil {
					return
				}
				if err == nil {
					sum := sha256.Sum256(data)
					if hex.EncodeToString(sum[:]) != batch.SHA256 {
						err = errors.New("batch changed during upload")
					}
				}
				if err == nil {
					kind, _, _ := strings.Cut(batch.Name, "-")
					err = s.post(ctx, telemetry.Body{Path: "/v1/" + kind, Data: data})
				}
				if err == nil && log != nil {
					logMu.Lock()
					_, err = fmt.Fprintln(log, batch.Name)
					logMu.Unlock()
				}
				if err != nil {
					cancel(err)
				}
				results <- err
			}
		}
	}
	workerCount := len(m.Batches)
	if workerCount > 4 {
		workerCount = 4
	}
	workers.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go worker()
	}
	go func() {
		defer close(jobs)
		for _, batch := range m.Batches {
			select {
			case jobs <- batch:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
			if uploaded != nil {
				uploaded(accepted, len(m.Batches))
			}
		}
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return nil
}
