package telemetry

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/navigaracom/navigara-cli/internal/session"
)

// CodexRollouts lists the rollout files under a Codex home, child threads included:
// Codex reports each thread as its own session. Missing roots are optional.
func CodexRollouts(codexHome string) ([]string, error) {
	var paths []string
	for _, dir := range []string{"sessions", "archived_sessions"} {
		root, err := filepath.EvalSymlinks(filepath.Join(codexHome, dir))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect Codex %s: %w", dir, err)
		}
		info, err := os.Stat(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect Codex %s: %w", dir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("inspect Codex %s: not a directory", dir)
		}
		if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return fmt.Errorf("walk Codex %s at %q: %w", dir, path, err)
			}
			if d.Type().IsRegular() && strings.HasPrefix(d.Name(), "rollout-") && strings.HasSuffix(d.Name(), ".jsonl") {
				paths = append(paths, path)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// CodexSessionID reads a rollout's thread id from its session_meta header.
func CodexSessionID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open Codex rollout %q: %w", path, err)
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 1<<20).ReadBytes('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read Codex rollout header %q: %w", path, err)
	}
	var row struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &row) != nil || row.Type != "session_meta" || row.Payload.ID == "" {
		return "", fmt.Errorf("invalid Codex rollout header %q", path)
	}
	return row.Payload.ID, nil
}

const codexInlinePayloadBytes = 64 << 10 // Bounds the additional payload copy; larger rows use the compatible lazy path.

type codexHeader struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Ordinal   *int64 `json:"ordinal"`
}

type codexRow struct {
	codexHeader
	Payload       json.RawMessage `json:"payload"`
	payloadLoaded bool
}

func (r *codexRow) payload(data []byte) json.RawMessage {
	if !r.payloadLoaded {
		r.Payload = codexPayload(data)
		r.payloadLoaded = true
	}
	return r.Payload
}

// codexPayload serves typed handlers for larger rows. Unhandled large payloads
// avoid an additional copy.
func codexPayload(data []byte) json.RawMessage {
	var row struct {
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(data, &row) != nil {
		return nil
	}
	return row.Payload
}

type codexTokens struct {
	Input           int64 `json:"input_tokens"`
	CachedInput     int64 `json:"cached_input_tokens"`
	CacheWriteInput int64 `json:"cache_write_input_tokens"`
	Output          int64 `json:"output_tokens"`
	ReasoningOutput int64 `json:"reasoning_output_tokens"`
	Total           int64 `json:"total_tokens"`
}

type codexNativeToolCall struct {
	Name  string
	Model string
}

// BuildCodex rebuilds a Codex thread's telemetry from its rollout file.
func BuildCodex(path string, ident Identity, until time.Time) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	s := &Session{Source: "codex", Service: "codex_tui", Common: ident.codexAttributes()}
	scanner := bufio.NewScanner(io.LimitReader(f, info.Size()))
	scanner.Buffer(make([]byte, 0, 1<<20), 256<<20)
	model := ""
	var last *codexTokens
	legacyToolIDs := map[string]bool{}
	foreignToolIDs := map[string]bool{}
	nativeToolCalls := map[string]codexNativeToolCall{}
	nativeToolOutputs := map[string]time.Time{}
	nativeToolOutputIDs := []string{}
	responses := map[string]codexResponse{}
	responseIDs := []string{}
	responseContexts := map[string]codexResponseContext{}
	var historyStart int64
	line := 0
	for scanner.Scan() {
		line++
		data := scanner.Bytes()
		var r codexRow
		if len(data) <= codexInlinePayloadBytes {
			if json.Unmarshal(data, &r) != nil {
				return nil, fmt.Errorf("line %d: invalid JSON", line)
			}
			r.payloadLoaded = true
		} else if json.Unmarshal(data, &r.codexHeader) != nil {
			return nil, fmt.Errorf("line %d: invalid JSON", line)
		}
		inheritedRecord := r.Type != "session_meta" && historyStart > 0 && r.Ordinal != nil && *r.Ordinal < historyStart
		if inheritedRecord && r.Type != "turn_context" && r.Type != "response_item" {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, r.Timestamp)
		if err != nil {
			if line == 1 && r.Type == "session_meta" {
				return nil, fmt.Errorf("line %d: invalid session_meta timestamp", line)
			}
			continue
		}
		at = at.UTC()
		future := !until.IsZero() && at.After(until)
		if future && !inheritedRecord && r.Type != "session_meta" {
			break
		}
		switch r.Type {
		case "session_meta":
			// Forked rollouts may copy an optional parent header after the child header.
			if s.SessionID != "" {
				break
			}
			var meta struct {
				ID            string `json:"id"`
				Originator    string `json:"originator"`
				Source        any    `json:"source"`
				ThreadSource  string `json:"thread_source"`
				ParentThread  string `json:"parent_thread_id"`
				HistoryStart  int64  `json:"subagent_history_start_ordinal"`
				ModelProvider string `json:"model_provider"`
				Git           *struct {
					RepositoryURL string `json:"repository_url"`
				} `json:"git"`
			}
			if json.Unmarshal(r.payload(data), &meta) != nil || meta.ID == "" {
				return nil, fmt.Errorf("invalid session_meta")
			}
			historyStart = meta.HistoryStart
			s.SessionID = meta.ID
			s.Common["session.id"] = meta.ID
			if meta.Originator != "" {
				s.Common["originator"] = meta.Originator
			}
			if src, ok := meta.Source.(string); ok && src != "" {
				s.Common["session_source"] = src
			}
			if meta.Git != nil {
				if url := repositoryURL(meta.Git.RepositoryURL); url != "" {
					s.Common["vcs.repository.url.full"] = url
				}
			}
			if !future {
				s.event("codex.conversation_starts", at, map[string]any{})
			}
		case "turn_context":
			var tc struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(r.payload(data), &tc) == nil && tc.Model != "" {
				model = tc.Model
			}
			if !future {
				recordCodexContext(r.payload(data), responseContexts)
			}
		case "token_usage_record":
			response, err := readCodexResponse(r.payload(data), s.SessionID, at)
			if err != nil {
				return nil, fmt.Errorf("line %d: %w", line, err)
			}
			if response == nil {
				continue
			}
			if prior, exists := responses[response.responseID]; exists {
				// Persistence retries may change the observation time, not the response fact.
				response.at = prior.at
				if prior != *response {
					return nil, fmt.Errorf("line %d: conflicting response identity", line)
				}
				continue
			}
			responses[response.responseID] = *response
			responseIDs = append(responseIDs, response.responseID)
		case "event_msg":
			payload := r.payload(data)
			var head struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(payload, &head) != nil {
				continue
			}
			switch head.Type {
			case "item_completed":
				var p struct {
					ThreadID string `json:"thread_id"`
					Item     struct {
						ID       string          `json:"id"`
						Type     string          `json:"type"`
						Content  json.RawMessage `json:"content"`
						Server   string          `json:"server"`
						Tool     string          `json:"tool"`
						Kind     string          `json:"kind"`
						Status   string          `json:"status"`
						Duration *struct {
							Secs  int64 `json:"secs"`
							Nanos int64 `json:"nanos"`
						} `json:"duration"`
					} `json:"item"`
				}
				if json.Unmarshal(payload, &p) != nil {
					continue
				}
				it := p.Item
				switch it.Type {
				case "UserMessage":
					if p.ThreadID != "" && p.ThreadID != s.SessionID {
						continue
					}
					var parts []struct {
						Text string `json:"text"`
					}
					_ = json.Unmarshal(it.Content, &parts)
					length := 0
					for _, part := range parts {
						length += utf8.RuneCountInString(part.Text)
					}
					s.event("codex.user_prompt", at, map[string]any{"model": model, "prompt_length": fmt.Sprint(length)})
				case "McpToolCall", "CommandExecution", "FileChange", "Extension", "ImageView", "CollabAgentToolCall":
					if p.ThreadID != "" && p.ThreadID != s.SessionID {
						if it.ID != "" {
							foreignToolIDs[it.ID] = true
						}
						continue
					}
					if it.ID != "" {
						if legacyToolIDs[it.ID] {
							continue
						}
						legacyToolIDs[it.ID] = true
					}
					attrs := map[string]any{"model": model, "tool_name": codexToolName(it.Type, it.Tool, it.Kind),
						"success": it.Status == "" || it.Status == "completed"}
					if it.ID != "" {
						attrs["call_id"] = it.ID
					}
					if it.Type == "McpToolCall" {
						attrs["mcp_server"] = it.Server
					}
					if it.Duration != nil {
						attrs["duration_ms"] = it.Duration.Secs*1000 + it.Duration.Nanos/1e6
					}
					s.event("codex.tool_result", at, attrs)
				}
			case "token_count":
				var p struct {
					Info *struct {
						Total codexTokens `json:"total_token_usage"`
					} `json:"info"`
				}
				if json.Unmarshal(payload, &p) != nil || p.Info == nil {
					continue
				}
				total := p.Info.Total
				// Codex repeats a token_count without a new response; only a grown total is usage.
				if last != nil && total.Total <= last.Total {
					if total.Total < last.Total {
						last = &total
					}
					continue
				}
				prev := codexTokens{}
				if last != nil {
					prev = *last
				}
				last = &total
				for _, c := range []struct {
					kind  string
					value int64
				}{{"input", total.Input - prev.Input}, {"cached_input", total.CachedInput - prev.CachedInput},
					{"cache_write_input", total.CacheWriteInput - prev.CacheWriteInput}, {"output", total.Output - prev.Output},
					{"reasoning_output", total.ReasoningOutput - prev.ReasoningOutput}, {"total", total.Total - prev.Total}} {
					if c.value > 0 {
						s.Points = append(s.Points, Point{Metric: "token.usage", Time: at, Model: model, Type: c.kind, Value: float64(c.value)})
					}
				}
			}
		case "response_item":
			var envelope struct {
				Payload struct {
					Type      string `json:"type"`
					CallID    string `json:"call_id"`
					Name      string `json:"name"`
					Execution string `json:"execution"`
				} `json:"payload"`
			}
			if json.Unmarshal(data, &envelope) != nil || envelope.Payload.CallID == "" {
				continue
			}
			item := envelope.Payload
			if inheritedRecord && (item.Type == "tool_search_output" || item.Type == "function_call_output" || item.Type == "custom_tool_call_output") {
				continue
			}
			switch item.Type {
			case "tool_search_call":
				if item.Execution == "client" {
					nativeToolCalls[item.CallID] = codexNativeToolCall{Name: "tool_search", Model: model}
				}
			case "function_call", "custom_tool_call":
				if item.Name != "" {
					nativeToolCalls[item.CallID] = codexNativeToolCall{Name: item.Name, Model: model}
				}
			case "tool_search_output", "function_call_output", "custom_tool_call_output":
				if item.Type == "tool_search_output" && item.Execution != "client" {
					continue
				}
				if _, seen := nativeToolOutputs[item.CallID]; !seen {
					nativeToolOutputs[item.CallID] = at
					nativeToolOutputIDs = append(nativeToolOutputIDs, item.CallID)
				}
			}
		}
		if future && !inheritedRecord {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if s.SessionID == "" {
		return nil, fmt.Errorf("no session_meta")
	}
	for _, responseID := range responseIDs {
		response := responses[responseID]
		context := responseContexts[response.turnID]
		if context.Model == "" || context.ambiguous {
			s.miss("codex.response_missing_turn_context")
		}
		s.event("codex.sse_event", response.at, response.attributes(context))
	}
	for _, callID := range nativeToolOutputIDs {
		if legacyToolIDs[callID] || foreignToolIDs[callID] {
			continue
		}
		at := nativeToolOutputs[callID]
		call, ok := nativeToolCalls[callID]
		if !ok {
			s.miss("codex.native_tool_output_missing_metadata")
			continue
		}
		attrs := map[string]any{"model": call.Model, "tool_name": call.Name, "call_id": callID}
		s.event("codex.tool_result", at, attrs)
	}
	s.mergePoints()
	s.sortRecords()
	return s, nil
}

func codexToolName(itemType, tool, kind string) string {
	switch {
	case tool != "":
		return tool
	case kind != "":
		return kind
	case itemType == "CommandExecution":
		return "exec_command"
	case itemType == "FileChange":
		return "apply_patch"
	}
	return itemType
}

// repositoryURL turns a recorded remote into the https form the tools report.
func repositoryURL(remote string) string {
	if key := session.RepositoryKey(remote); key != "" {
		return "https://" + key
	}
	return ""
}
