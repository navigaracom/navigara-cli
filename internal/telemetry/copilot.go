package telemetry

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// CopilotService is the OTLP service.name of GitHub Copilot CLI.
const CopilotService = "github-copilot"

// MissingCopilotTotals counts the model calls of a session that kept only totals at shutdown:
// Copilot CLI 1.0.60 and older write no per-call usage record.
const MissingCopilotTotals = "model calls with totals only"

// CopilotSessions lists the events.jsonl of each session under a Copilot home. A session folder
// without one has no history.
func CopilotSessions(home string) ([]string, error) {
	return filepath.Glob(filepath.Join(home, "session-state", "*", "events.jsonl"))
}

// CopilotSessionID is the session folder name, which Copilot sends live as gen_ai.conversation.id.
func CopilotSessionID(path string) string { return filepath.Base(filepath.Dir(path)) }

// BuildCopilot rebuilds the chat spans and prompt events Copilot CLI sends live: one span per
// usage record, keyed by its apiCallId, and one prompt event per user message, keyed by its
// interactionId. Prompt text is never read.
func BuildCopilot(path string, ident Identity) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Copilot session %q: %w", path, err)
	}
	defer f.Close()
	id := CopilotSessionID(path)
	s := &Session{Source: "copilot", SessionID: id, Service: CopilotService,
		Common: map[string]any{"gen_ai.conversation.id": id}}
	for k, v := range ident.copilotAttributes() {
		s.Common[k] = v
	}
	sum := sha256.Sum256([]byte(id))
	traceID := hex.EncodeToString(sum[:16])
	records, totals := 0, 0
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && (bytes.Contains(line, []byte(`"session.usage_record"`)) ||
			bytes.Contains(line, []byte(`"user.message"`)) || bytes.Contains(line, []byte(`"session.shutdown"`))) {
			var row copilotRow
			if json.Unmarshal(line, &row) != nil {
				s.miss("unreadable records")
			} else {
				switch row.Type {
				case "session.usage_record":
					records++
					s.copilotCall(traceID, row)
				case "user.message":
					s.copilotPrompt(row)
				case "session.shutdown":
					for _, m := range row.Data.ModelMetrics {
						// The count comes from the file: bound it so a corrupt value cannot overflow.
						totals += int(min(max(m.Requests.Count, 0), 1e6))
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read Copilot session %q: %w", path, err)
		}
	}
	if records == 0 && totals > 0 {
		if s.Missing == nil {
			s.Missing = map[string]int{}
		}
		s.Missing[MissingCopilotTotals] = totals
	}
	s.sortRecords()
	return s, nil
}

type copilotRow struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	Data      struct {
		InteractionID string `json:"interactionId"`
		TurnID        string `json:"turnId"`
		Usage         struct {
			Model            string  `json:"model"`
			APICallID        string  `json:"apiCallId"`
			Initiator        string  `json:"initiator"`
			InputTokens      int64   `json:"inputTokens"`
			OutputTokens     int64   `json:"outputTokens"`
			CacheReadTokens  int64   `json:"cacheReadTokens"`
			CacheWriteTokens int64   `json:"cacheWriteTokens"`
			Duration         float64 `json:"duration"`
		} `json:"usage"`
		ModelMetrics map[string]struct {
			Requests struct {
				Count float64 `json:"count"`
			} `json:"requests"`
		} `json:"modelMetrics"`
	} `json:"data"`
}

func (s *Session) copilotCall(traceID string, row copilotRow) {
	u := row.Data.Usage
	// Without the call id the span cannot meet its live copy, so it would count twice.
	if u.APICallID == "" || row.Timestamp.IsZero() {
		s.miss("usage records without a call id")
		return
	}
	attrs := map[string]any{
		"gen_ai.operation.name": "chat", "gen_ai.response.id": u.APICallID,
		"gen_ai.request.model": u.Model, "gen_ai.response.model": u.Model,
		"gen_ai.usage.input_tokens": u.InputTokens, "gen_ai.usage.output_tokens": u.OutputTokens,
	}
	if u.CacheReadTokens > 0 {
		attrs["gen_ai.usage.cache_read.input_tokens"] = u.CacheReadTokens
	}
	if u.CacheWriteTokens > 0 {
		attrs["gen_ai.usage.cache_write.input_tokens"] = u.CacheWriteTokens
	}
	if u.Initiator != "" {
		attrs["github.copilot.initiator"] = u.Initiator
	}
	sum := sha256.Sum256([]byte(u.APICallID))
	// The record is written when the call ends.
	start := row.Timestamp.Add(-time.Duration(u.Duration * float64(time.Millisecond)))
	s.Spans = append(s.Spans, Span{Name: "chat " + u.Model, Start: start, End: row.Timestamp,
		TraceID: traceID, SpanID: hex.EncodeToString(sum[:8]), Attrs: attrs})
	if row.Timestamp.After(s.End) {
		s.End = row.Timestamp
	}
}

func (s *Session) copilotPrompt(row copilotRow) {
	if row.Data.InteractionID == "" || row.Timestamp.IsZero() {
		s.miss("user messages without an interaction id")
		return
	}
	attrs := map[string]any{
		"github.copilot.user.message.interaction_id": row.Data.InteractionID,
		"github.copilot.user.message.source":         "user",
	}
	if row.Data.TurnID != "" {
		attrs["github.copilot.user.message.turn_id"] = row.Data.TurnID
	}
	s.event("github.copilot.user.message", row.Timestamp, attrs)
}
