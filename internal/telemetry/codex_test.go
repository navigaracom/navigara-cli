package telemetry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func codexEvent(at, kind string, payload map[string]any) map[string]any {
	payload["type"] = kind
	return map[string]any{"timestamp": at, "type": "event_msg", "payload": payload}
}

func tokenCount(at string, input, cached, output, total int) map[string]any {
	return codexEvent(at, "token_count", map[string]any{"info": map[string]any{"total_token_usage": map[string]any{
		"input_tokens": input, "cached_input_tokens": cached, "output_tokens": output, "total_tokens": total}}})
}

func TestBuildCodex(t *testing.T) {
	home := t.TempDir()
	child := filepath.Join(home, "sessions", "2026", "10", "03", "rollout-2026-10-03T10-00-00-child.jsonl")
	writeLines(t, child,
		map[string]any{"timestamp": "2026-10-03T10:00:00Z", "type": "session_meta", "payload": map[string]any{
			"id": "child-thread", "parent_thread_id": "parent", "originator": "codex-tui", "thread_source": "subagent",
			"git": map[string]any{"repository_url": "git@github.com:acme/repo.git"}}},
		// Fork pattern reported in review: a copied parent header follows the child header.
		map[string]any{"timestamp": "2026-10-03T09:59:59Z", "type": "session_meta", "payload": map[string]any{
			"id": "parent", "originator": "codex-tui"}},
		map[string]any{"timestamp": "2026-10-03T10:00:01Z", "type": "turn_context", "payload": map[string]any{"model": "gpt-6-astra"}},
		codexEvent("2026-10-03T10:00:02Z", "item_completed", map[string]any{"item": map[string]any{
			"type": "UserMessage", "content": []any{map[string]any{"type": "text", "text": "review this"}}}}),
		codexEvent("2026-10-03T10:00:05Z", "item_completed", map[string]any{"item": map[string]any{
			"type": "McpToolCall", "server": "linear", "tool": "get_issue", "status": "completed",
			"duration": map[string]any{"secs": 1, "nanos": 500000000}}}),
		codexEvent("2026-10-03T10:00:06Z", "item_completed", map[string]any{"item": map[string]any{
			"type": "CommandExecution", "status": "failed"}}),
		tokenCount("2026-10-03T10:00:07Z", 100, 40, 10, 110),
		// A repeated count is not a new response.
		tokenCount("2026-10-03T10:00:08Z", 100, 40, 10, 110),
		tokenCount("2026-10-03T10:00:09Z", 250, 140, 30, 280),
	)

	paths, err := CodexRollouts(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("rollouts = %v; want one child thread", paths)
	}
	id, err := CodexSessionID(paths[0])
	if err != nil || id != "child-thread" {
		t.Fatalf("rollouts = %v; a child thread is its own session", paths)
	}
	s, err := BuildCodex(paths[0], Identity{CodexEmail: "dev@example.com"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionID != "child-thread" || s.Common["session.id"] != "child-thread" || s.Service != "codex_tui" || s.Common["user.email"] != "dev@example.com" ||
		s.Common["vcs.repository.url.full"] != "https://github.com/acme/repo" {
		t.Fatalf("session = %+v", s)
	}
	starts := events(s, "codex.conversation_starts")
	if len(starts) != 1 || !starts[0].Time.Equal(time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("conversation starts = %v", starts)
	}
	prompts := events(s, "codex.user_prompt")
	if len(prompts) != 1 || prompts[0].Attrs["prompt_length"] != "11" || prompts[0].Attrs["model"] != "gpt-6-astra" {
		t.Errorf("prompts = %v", prompts)
	}
	results := events(s, "codex.tool_result")
	if len(results) != 2 || results[0].Attrs["mcp_server"] != "linear" || results[0].Attrs["duration_ms"] != int64(1500) ||
		results[1].Attrs["success"] != false {
		t.Errorf("tool results = %v", results)
	}
	for _, c := range []struct {
		kind string
		want float64
	}{{"input", 250}, {"cached_input", 140}, {"output", 30}, {"total", 280}} {
		if got := sum(s, "token.usage", c.kind); got != c.want {
			t.Errorf("%s = %v, want %v", c.kind, got, c.want)
		}
	}
}

func TestBuildCodexKeepsFutureSessionIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-future.jsonl")
	writeLines(t, path, map[string]any{"timestamp": "2026-10-03T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "future"}})
	s, err := BuildCodex(path, Identity{}, time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	if err != nil || s.SessionID != "future" || len(s.Events) != 0 || len(s.Points) != 0 {
		t.Fatalf("session=%+v err=%v", s, err)
	}

	writeLines(t, path, map[string]any{"timestamp": "2026-10-03T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "future", "originator": 123}})
	if _, err := BuildCodex(path, Identity{}, time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("malformed future metadata accepted")
	}
}

func TestBuildCodexKeepsCanonicalServiceForFriendlyOriginators(t *testing.T) {
	for _, originator := range []string{"Claude Code", "Codex Desktop"} {
		t.Run(originator, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout.jsonl")
			writeLines(t, path,
				map[string]any{"timestamp": "2026-10-03T10:00:00Z", "type": "session_meta", "payload": map[string]any{
					"id": "friendly-originator", "originator": originator}},
				map[string]any{"timestamp": "2026-10-03T10:00:01Z", "type": "turn_context", "payload": map[string]any{"model": "gpt-6-astra"}},
				tokenCount("2026-10-03T10:00:02Z", 100, 40, 10, 110),
			)

			s, err := BuildCodex(path, Identity{}, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			if s.Source != "codex" || s.Service != "codex_tui" || s.Common["originator"] != originator {
				t.Fatalf("source=%q service=%q originator=%q", s.Source, s.Service, s.Common["originator"])
			}
			if len(s.Points) != 4 || s.Points[0].Metric != "token.usage" || s.Points[0].Model != "gpt-6-astra" || s.Points[0].Type != "input" {
				t.Fatalf("token dimensions = %+v", s.Points)
			}
		})
	}
}

func TestCodexDiscoveryErrors(t *testing.T) {
	home := t.TempDir()
	if paths, err := CodexRollouts(home); err != nil || len(paths) != 0 {
		t.Fatalf("missing optional roots = %v, %v", paths, err)
	}

	rollout := filepath.Join(home, "sessions", "rollout-bad.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollout, []byte(`{"type":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CodexSessionID(rollout); err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("malformed header error = %v", err)
	}

	blocked := filepath.Join(home, "blocked")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CodexRollouts(blocked); err == nil {
		t.Fatal("non-directory Codex home accepted")
	}
}

func TestEncodeBoundsBodies(t *testing.T) {
	s := &Session{Source: "claude_code", SessionID: sid, Service: "claude-code", Common: map[string]any{"session.id": sid}}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range 20000 {
		s.Events = append(s.Events, Event{Name: "tool_result", Time: start.Add(time.Duration(i) * time.Second),
			Attrs: map[string]any{"tool_name": "Bash", "duration_ms": int64(i), "success": "true", "filler": string(make([]byte, 200))}})
	}
	s.Points = []Point{{Metric: "cost.usage", Time: start, Model: "m", Value: 1.5}}
	bodies, err := Encode([]*Session{s})
	if err != nil {
		t.Fatal(err)
	}
	logs := 0
	for _, b := range bodies {
		if len(b.Data) > MaxBodyBytes {
			t.Fatalf("body of %d bytes exceeds %d", len(b.Data), MaxBodyBytes)
		}
		if b.Path == "/v1/logs" {
			logs++
		}
	}
	if logs < 2 || bodies[len(bodies)-1].Path != "/v1/metrics" {
		t.Fatalf("bodies = %d logs of %d; want logs split and metrics last", logs, len(bodies))
	}
}
