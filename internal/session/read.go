package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
	if source != "claude_code" && source != "codex" {
		return Record{}, fmt.Errorf("unsupported source")
	}
	result := Record{Session: Session{Source: source}}
	var context codexContext
	reader := bufio.NewReader(r)
	line, sawSidechain := 0, false
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
				if addTimestamp(&result.Session, p.Timestamp) != nil {
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
			if result.Session.SessionID != "" && result.Session.SessionID != id {
				return fail("conflicting session identity")
			}
			result.Session.SessionID = id
		}
		if result.Session.SourceVersion == "" {
			result.Session.SourceVersion = version
		}
		if addTimestamp(&result.Session, timestamp) != nil {
			return fail("invalid timestamp")
		}
		if source == "codex" {
			u, err := context.observe(b, kind, timestamp, line)
			if err != nil {
				return fail(err.Error())
			}
			if u != nil {
				result.Session.UsageObservations = append(result.Session.UsageObservations, *u)
			}
		}
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

func addTimestamp(s *Session, value string) error {
	if value == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return fmt.Errorf("invalid timestamp")
	}
	t = t.UTC()
	if s.FirstObservedAt == nil || t.Before(*s.FirstObservedAt) {
		s.FirstObservedAt = &t
	}
	if s.LastObservedAt == nil || t.After(*s.LastObservedAt) {
		s.LastObservedAt = &t
	}
	return nil
}
