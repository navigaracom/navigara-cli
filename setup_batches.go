package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/navigaracom/navigara-cli/internal/telemetry"
)

type setupBatch struct{ Name, SHA256 string }
type setupManifest struct {
	Version        int
	URL            string
	OrganizationID string `json:",omitempty"`
	Batches        []setupBatch
}

var batchName = regexp.MustCompile(`^(logs|metrics)-[0-9]+\.json$`)

func saveSetupBatches(dir, url, organizationID string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	m := setupManifest{Version: 1, URL: url, OrganizationID: organizationID}
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
	if m.Version != 1 {
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
	if !exists || !json.Valid(data) {
		return nil, errors.New("missing or invalid OTLP batch")
	}
	return data, nil
}

func sendSetupBatches(dir string, s *sender, uploaded func(done, total int)) error {
	m, err := loadSetupBatches(dir)
	if err != nil {
		return err
	}
	if s.base != otlpBase(m.URL) {
		return errors.New("resume destination differs from frozen upload")
	}
	if uploaded != nil {
		uploaded(0, len(m.Batches))
	}
	for done, b := range m.Batches {
		data, err := readBatch(dir, b.Name)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != b.SHA256 {
			return errors.New("batch changed during upload")
		}
		kind, _, _ := strings.Cut(b.Name, "-")
		if err := s.post(telemetry.Body{Path: "/v1/" + kind, Data: data}); err != nil {
			return err
		}
		if uploaded != nil {
			uploaded(done+1, len(m.Batches))
		}
	}
	return nil
}
