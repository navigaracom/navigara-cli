package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// Record contains metadata extracted from a history.
// An empty SessionID with no error denotes a recognized child-only history.
type Record struct {
	Session Session
	// Path identifies local provenance and is excluded from the request payload.
	Path string
}

// Read extracts recorded root metadata from JSONL. Memory scales with retained
// observations and the largest record. Codex child headers return immediately;
// Claude sidechains are ignored until EOF. Errors discard all partial metadata
// and never include input values.
func Read(source string, r io.Reader) (Record, error) {
	return read(source, r, "")
}

// read keeps the Claude lines of session want, or of the first sessionId when no line has want.
func read(source string, r io.Reader, want string) (Record, error) {
	if source != "claude_code" && source != "codex" {
		return Record{}, fmt.Errorf("unsupported source")
	}
	result := Record{Session: Session{Source: source}}
	var context codexContext
	reader := bufio.NewReader(r)
	line, sawSidechain := 0, false
	first, fallback := "", Session{Source: source}
	fail := func(problem string) (Record, error) {
		return Record{}, fmt.Errorf("line %d: %s", line, problem)
	}
	for {
		b, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			line++
			return fail("cannot read record")
		}
		if len(b) == 0 && err == io.EOF {
			break
		}
		line++
		b = bytes.TrimSpace(b)
		if len(b) == 0 {
			continue
		}
		var id, version, timestamp, kind string
		dst := &result.Session
		if source == "claude_code" {
			var side struct {
				IsSidechain bool `json:"isSidechain"`
			}
			if json.Unmarshal(b, &side) != nil {
				return fail("invalid JSON record")
			}
			if side.IsSidechain {
				sawSidechain = true
				continue
			}
			var identity struct {
				ID string `json:"sessionId"`
			}
			if json.Unmarshal(b, &identity) != nil {
				return fail("invalid session identity")
			}
			if identity.ID == "" {
				continue
			}
			if first == "" {
				first = identity.ID
			}
			if want == "" {
				want = first
			}
			if identity.ID != want {
				// Another session wrote this line; that session's own file counts it.
				if identity.ID != first {
					continue
				}
				dst = &fallback
			}
			var root struct {
				Version   string `json:"version"`
				Timestamp string `json:"timestamp"`
			}
			if json.Unmarshal(b, &root) != nil {
				return fail("invalid root metadata")
			}
			id, version, timestamp = identity.ID, root.Version, root.Timestamp
		} else {
			// Stage known row types; unrelated event IDs and payloads are opaque.
			var header struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(b, &header) != nil {
				return fail("invalid JSON record")
			}
			kind = header.Type
			if result.Session.SessionID == "" && header.Type != "session_meta" {
				return fail("expected session_meta first")
			}
			if header.Type == "session_meta" {
				var metadata struct {
					Payload struct {
						ID             string          `json:"id"`
						SessionID      json.RawMessage `json:"session_id"`
						Version        string          `json:"cli_version"`
						Timestamp      string          `json:"timestamp"`
						ParentThreadID string          `json:"parent_thread_id"`
						Source         json.RawMessage `json:"source"`
					} `json:"payload"`
				}
				if json.Unmarshal(b, &metadata) != nil {
					return fail("invalid session metadata")
				}
				p := metadata.Payload
				child := p.ParentThreadID != ""
				if s := bytes.TrimSpace(p.Source); len(s) > 0 && s[0] == '{' {
					var object map[string]json.RawMessage
					if json.Unmarshal(s, &object) != nil {
						return fail("invalid metadata source")
					}
					_, childKey := object["subagent"]
					child = child || childKey
				}
				if child {
					if result.Session.SessionID != "" {
						return fail("child metadata in root history")
					}
					return Record{}, nil
				}
				if strings.TrimSpace(p.ID) == "" {
					return fail("missing session identity")
				}
				if len(p.SessionID) > 0 {
					var sessionID string
					if json.Unmarshal(p.SessionID, &sessionID) != nil || sessionID != p.ID {
						return fail("conflicting session identity")
					}
				}
				id, version = p.ID, p.Version
				if _, err := addTimestamp(&result.Session, p.Timestamp); err != nil {
					return fail("invalid timestamp")
				}
			}
			var event struct {
				Timestamp string `json:"timestamp"`
			}
			if json.Unmarshal(b, &event) != nil {
				return fail("invalid record timestamp")
			}
			timestamp = event.Timestamp
		}
		if id != "" {
			if strings.TrimSpace(id) == "" {
				return fail("missing session identity")
			}
			if dst.SessionID != "" && dst.SessionID != id {
				return fail("conflicting session identity")
			}
			dst.SessionID = id
		}
		if dst.SourceVersion == "" {
			dst.SourceVersion = version
		}
		observedAt, timestampErr := addTimestamp(dst, timestamp)
		if timestampErr != nil {
			return fail("invalid timestamp")
		}
		var u *UsageObservation
		if source == "codex" {
			u, err = context.observe(b, kind, observedAt, line)
		} else {
			u, err = observeClaude(b, observedAt, line)
		}
		if err != nil {
			return fail(err.Error())
		}
		if u != nil {
			dst.UsageObservations = append(dst.UsageObservations, *u)
		}
		repository, err := observeRepository(b, source, kind, observedAt, line)
		if err != nil {
			return fail(err.Error())
		}
		if repository != nil {
			observations := dst.RepositoryObservations
			if len(observations) == 0 || repository.Branch != observations[len(observations)-1].Branch || repository.CommitHash != observations[len(observations)-1].CommitHash || repository.RepositoryURL != observations[len(observations)-1].RepositoryURL {
				dst.RepositoryObservations = append(observations, *repository)
			}
		}
	}
	if result.Session.SessionID == "" && fallback.SessionID != "" {
		result.Session = fallback
	}
	if result.Session.SessionID == "" {
		if sawSidechain {
			return Record{}, nil
		}
		if line == 0 {
			line = 1
		}
		return fail("no root session identity")
	}
	return result, nil
}

func addTimestamp(s *Session, value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp")
	}
	t = t.UTC()
	if s.FirstObservedAt == nil || t.Before(*s.FirstObservedAt) {
		s.FirstObservedAt = &t
	}
	if s.LastObservedAt == nil || t.After(*s.LastObservedAt) {
		s.LastObservedAt = &t
	}
	return &t, nil
}

// ClaudeFileSessionID returns the session ID in a Claude Code history file name
// (<uuid>.jsonl), or "" when the name is not a UUID.
func ClaudeFileSessionID(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if len(name) != 36 {
		return ""
	}
	for i, c := range name {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return ""
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return ""
		}
	}
	return name
}
