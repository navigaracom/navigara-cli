package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

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
	envelope := session.Envelope{SchemaVersion: session.SchemaVersion, Sessions: []session.Session{}}
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
			envelope.Sessions = append(envelope.Sessions, first)
			duplicates += end - start - 1
		}
		start = end
	}
	for _, err := range result.Errors {
		fmt.Fprintln(stderr, "navigara:", err)
	}
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err == nil {
		data = append(data, '\n')
		err = writeRequest(opts.output, data)
	}
	if err != nil {
		fmt.Fprintf(stderr, "navigara: output %q: %q\n", opts.output, err.Error())
		return 1
	}
	status := "complete"
	if len(result.Errors) != 0 {
		status = "incomplete"
	}
	summary := fmt.Sprintf("discovered=%d emitted=%d duplicates=%d skipped=%d failed=%d %s\n", result.Files, len(envelope.Sessions), duplicates, result.Skipped, failed, status)
	if opts.format == "json" {
		fmt.Fprint(stderr, summary)
	} else {
		var text bytes.Buffer
		text.WriteString(summary)
		for _, s := range envelope.Sessions {
			fmt.Fprintf(&text, "%s %q\n", s.Source, s.SessionID)
		}
		data = text.Bytes()
	}
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
