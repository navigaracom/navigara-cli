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

type codexRow struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexTokens struct {
	Input           int64 `json:"input_tokens"`
	CachedInput     int64 `json:"cached_input_tokens"`
	CacheWriteInput int64 `json:"cache_write_input_tokens"`
	Output          int64 `json:"output_tokens"`
	ReasoningOutput int64 `json:"reasoning_output_tokens"`
	Total           int64 `json:"total_tokens"`
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
	scanner := bufio.NewScanner(&limitedReader{f: f, left: info.Size()})
	scanner.Buffer(make([]byte, 0, 1<<20), 256<<20)
	model := ""
	var last *codexTokens
	line := 0
	for scanner.Scan() {
		line++
		var r codexRow
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			return nil, fmt.Errorf("line %d: invalid JSON", line)
		}
		at, err := time.Parse(time.RFC3339Nano, r.Timestamp)
		if err != nil {
			continue
		}
		at = at.UTC()
		if !until.IsZero() && at.After(until) {
			break
		}
		switch r.Type {
		case "session_meta":
			var meta struct {
				ID            string `json:"id"`
				Originator    string `json:"originator"`
				Source        any    `json:"source"`
				ThreadSource  string `json:"thread_source"`
				ParentThread  string `json:"parent_thread_id"`
				ModelProvider string `json:"model_provider"`
				Git           *struct {
					RepositoryURL string `json:"repository_url"`
				} `json:"git"`
			}
			if json.Unmarshal(r.Payload, &meta) != nil || meta.ID == "" {
				return nil, fmt.Errorf("invalid session_meta")
			}
			s.SessionID = meta.ID
			s.Common["session.id"] = meta.ID
			if meta.Originator != "" {
				s.Common["originator"] = meta.Originator
				s.Service = strings.ReplaceAll(meta.Originator, "-", "_")
			}
			if src, ok := meta.Source.(string); ok && src != "" {
				s.Common["session_source"] = src
			}
			if meta.Git != nil {
				if url := repositoryURL(meta.Git.RepositoryURL); url != "" {
					s.Common["vcs.repository.url.full"] = url
				}
			}
			s.event("codex.conversation_starts", at, map[string]any{})
		case "turn_context":
			var tc struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(r.Payload, &tc) == nil && tc.Model != "" {
				model = tc.Model
			}
		case "event_msg":
			var head struct {
				Type string `json:"type"`
				Item json.RawMessage
			}
			if json.Unmarshal(r.Payload, &head) != nil {
				continue
			}
			switch head.Type {
			case "item_completed":
				var p struct {
					Item struct {
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
				if json.Unmarshal(r.Payload, &p) != nil {
					continue
				}
				it := p.Item
				switch it.Type {
				case "UserMessage":
					var parts []struct {
						Text string `json:"text"`
					}
					_ = json.Unmarshal(it.Content, &parts)
					length := 0
					for _, part := range parts {
						length += len(part.Text)
					}
					s.event("codex.user_prompt", at, map[string]any{"model": model, "prompt_length": fmt.Sprint(length)})
				case "McpToolCall", "CommandExecution", "FileChange", "Extension", "ImageView", "CollabAgentToolCall":
					attrs := map[string]any{"model": model, "tool_name": codexToolName(it.Type, it.Tool, it.Kind),
						"success": it.Status == "" || it.Status == "completed"}
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
				if json.Unmarshal(r.Payload, &p) != nil || p.Info == nil {
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
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if s.SessionID == "" {
		return nil, fmt.Errorf("no session_meta")
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
