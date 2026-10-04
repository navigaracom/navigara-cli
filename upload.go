package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/navigaracom/navigara-cli/internal/otel"
	"github.com/navigaracom/navigara-cli/internal/session"
)

func upload(opts options, stdout, stderr io.Writer) int {
	return writeUpload(opts, session.Scan(opts.source, opts.claudeHome, opts.codexHome), stdout, stderr)
}

func writeUpload(opts options, result session.Result, stdout, stderr io.Writer) int {
	sort.Slice(result.Records, func(i, j int) bool {
		a, b := result.Records[i].Session, result.Records[j].Session
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.SessionID < b.SessionID
	})
	sessions := []session.Session{}
	duplicates, failed := 0, result.Files-len(result.Records)-result.Skipped
	for start := 0; start < len(result.Records); {
		first := result.Records[start].Session
		firstPayload, err := json.Marshal(first)
		if err != nil {
			fmt.Fprintf(stderr, "navigara: output %q: %q\n", opts.output, err.Error())
			return 1
		}
		end, conflict := start+1, false
		for end < len(result.Records) {
			next := result.Records[end].Session
			if next.Source != first.Source || next.SessionID != first.SessionID {
				break
			}
			nextPayload, err := json.Marshal(next)
			if err != nil {
				fmt.Fprintf(stderr, "navigara: output %q: %q\n", opts.output, err.Error())
				return 1
			}
			if !bytes.Equal(nextPayload, firstPayload) {
				conflict = true
			}
			end++
		}
		if conflict {
			failed += end - start
			result.Errors = append(result.Errors, fmt.Errorf("conflicting metadata for %q %q", first.Source, first.SessionID))
		} else {
			sessions = append(sessions, first)
			duplicates += end - start - 1
		}
		start = end
	}
	for _, err := range result.Errors {
		fmt.Fprintln(stderr, "navigara:", err)
	}
	if err := os.Mkdir(opts.output, 0700); err != nil {
		fmt.Fprintf(stderr, "navigara: output %q: %q\n", opts.output, err.Error())
		return 1
	}
	mapped := otel.Result{}
	points, filtered, batches := 0, 0, 0
	encoder := otel.NewEncoder(func(data []byte) error {
		path := filepath.Join(opts.output, fmt.Sprintf("metrics-%06d.json", batches+1))
		if err := writeRequest(path, data); err != nil {
			return err
		}
		batches++
		return nil
	})
	var err error
	for _, s := range sessions {
		r := otel.Map(s)
		mapped.Mapped += r.Mapped
		mapped.Repeated += r.Repeated
		mapped.Ambiguous += r.Ambiguous
		mapped.Unmapped += r.Unmapped
		selected, dropped := filterPoints(r.Points, opts.since, opts.until)
		points += len(selected)
		filtered += dropped
		if err = encoder.Add(selected); err != nil {
			break
		}
	}
	if err == nil {
		err = encoder.Flush()
	}
	if err != nil {
		fmt.Fprintf(stderr, "navigara: incomplete output %q (%d completed batches): %q\n", opts.output, batches, err.Error())
		return 1
	}
	status := "complete"
	if len(result.Errors) != 0 {
		status = "incomplete"
	}
	var text bytes.Buffer
	fmt.Fprintf(&text, "discovered=%d sessions=%d duplicates=%d skipped=%d failed=%d %s\n", result.Files, len(sessions), duplicates, result.Skipped, failed, status)
	fmt.Fprintf(&text, "points=%d batches=%d mapped=%d repeated=%d ambiguous=%d unmapped=%d filtered=%d\n", points, batches, mapped.Mapped, mapped.Repeated, mapped.Ambiguous, mapped.Unmapped, filtered)
	for _, s := range sessions {
		fmt.Fprintf(&text, "%s %q\n", s.Source, s.SessionID)
	}
	data := text.Bytes()
	if err := writeBytes(stdout, data); err != nil {
		fmt.Fprintf(stderr, "navigara: stdout: %q\n", err.Error())
		return 1
	}
	if len(result.Errors) != 0 {
		return 1
	}
	return 0
}

func writeRequest(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = errors.Join(writeBytes(f, data), f.Close())
	if err != nil {
		if cleanupErr := os.Remove(path); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("remove incomplete output: %w", cleanupErr))
		}
	}
	return err
}

func writeBytes(w io.Writer, data []byte) error {
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}
