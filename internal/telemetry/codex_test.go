package telemetry

import (
	"errors"
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
			"type": "UserMessage", "content": []any{map[string]any{"type": "text", "text": "review café"}}}}),
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

func TestBuildCodexActualForkCountsOwnedTokenAfterBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-child.jsonl")
	fixture, err := os.ReadFile("testdata/codex-fork-ordinal-owned.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := BuildCodex(path, Identity{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got := sum(s, "token.usage", "total"); got != 23004 {
		t.Fatalf("owned token total = %v, want 23004", got)
	}
}

func TestBuildCodexCountsCopiedHeaderRowAfterBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-child.jsonl")
	fixture, err := os.ReadFile("testdata/codex-fork-owned-after-boundary.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := BuildCodex(path, Identity{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got := sum(s, "token.usage", "total"); got != 55192 {
		t.Fatalf("after-boundary token total = %v, want 55192", got)
	}
	if got := len(events(s, "codex.conversation_starts")); got != 1 || len(s.Points) != 9 || s.Points[0].Model != "gpt-5.6-terra" {
		t.Fatalf("after-boundary records = events=%d points=%d", got, len(s.Points))
	}
}

func TestBuildCodexActualSingleHeaderForkSkipsInheritedToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-child.jsonl")
	fixture, err := os.ReadFile("testdata/codex-fork-single-header.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := BuildCodex(path, Identity{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionID != "child-thread" || len(events(s, "codex.conversation_starts")) != 1 || len(s.Points) != 0 {
		t.Fatalf("single-header fork = %+v", s)
	}
}

func TestBuildCodexUsesInheritedNativeCallForOwnedOutput(t *testing.T) {
	child := filepath.Join(t.TempDir(), "rollout-child.jsonl")
	call := map[string]any{"timestamp": "2026-10-03T09:00:01Z", "type": "response_item", "ordinal": 1, "payload": map[string]any{"type": "function_call", "call_id": "call", "name": "exec_command"}}
	writeLines(t, child,
		map[string]any{"timestamp": "2026-10-03T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "child", "forked_from_id": "parent", "subagent_history_start_ordinal": 2}},
		call,
		map[string]any{"timestamp": "2026-10-03T10:00:02Z", "type": "response_item", "ordinal": 2, "payload": map[string]any{"type": "function_call_output", "call_id": "call"}},
	)
	s, err := BuildCodex(child, Identity{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if results := events(s, "codex.tool_result"); len(results) != 1 || results[0].Attrs["call_id"] != "call" {
		t.Fatalf("owned output result = %+v", results)
	}
}

func TestBuildCodexClientToolSearch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeLines(t, path,
		map[string]any{"timestamp": "2026-10-03T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "search-thread"}},
		map[string]any{"timestamp": "2026-10-03T10:00:01Z", "type": "turn_context", "payload": map[string]any{"model": "gpt-6-astra"}},
		// Sanitized client discovery shapes from local rollouts; no name is supplied.
		map[string]any{"timestamp": "2026-10-03T10:00:02Z", "type": "response_item", "payload": map[string]any{
			"type": "tool_search_call", "call_id": "search-call", "execution": "client", "status": "completed", "arguments": map[string]any{}}},
		map[string]any{"timestamp": "2026-10-03T10:00:03Z", "type": "response_item", "payload": map[string]any{
			"type": "tool_search_output", "call_id": "search-call", "execution": "client", "status": "completed", "tools": []any{}}},
	)
	s, err := BuildCodex(path, Identity{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	results := events(s, "codex.tool_result")
	if len(results) != 1 || results[0].Attrs["tool_name"] != "tool_search" || results[0].Attrs["call_id"] != "search-call" {
		t.Fatalf("client tool discovery result = %v", results)
	}
	for _, key := range []string{"arguments", "tools", "success", "duration_ms"} {
		if _, found := results[0].Attrs[key]; found {
			t.Fatalf("unexpected content or inferred outcome: %s", key)
		}
	}
}

func TestBuildCodexKeepsOpaqueResponsePayloadsOpaque(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-opaque.jsonl")
	writeLines(t, path,
		map[string]any{"timestamp": "2026-10-03T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "opaque"}},
		// Payload types used by unrelated response rows are intentionally not
		// interpreted, even when they cannot match a native tool shape.
		map[string]any{"timestamp": "2026-10-03T10:00:01Z", "type": "response_item", "payload": []any{map[string]any{"private": "ignored"}}},
		map[string]any{"timestamp": "2026-10-03T10:00:02Z", "type": "response_item", "payload": false},
	)
	s, err := BuildCodex(path, Identity{}, time.Time{})
	if err != nil || s.SessionID != "opaque" || len(s.Events) != 1 {
		t.Fatalf("opaque response rows changed session output: session=%+v err=%v", s, err)
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

func TestBuildCodexMapsNativeCompletedToolsWithoutDoubleCountingLegacyItems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout-native-tools.jsonl")
	legacy := func(at, id string, duplicate bool) map[string]any {
		item := map[string]any{"type": "CommandExecution", "id": id, "status": "completed", "duration": map[string]any{"secs": 2}}
		if duplicate {
			item["duration"] = map[string]any{"secs": 9}
		}
		return codexEvent(at, "item_completed", map[string]any{"item": item})
	}
	response := func(at, kind string, payload map[string]any) map[string]any {
		payload["type"] = kind
		return map[string]any{"timestamp": at, "type": "response_item", "payload": payload}
	}
	writeLines(t, path,
		map[string]any{"timestamp": "2026-10-03T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "child"}},
		map[string]any{"timestamp": "2026-10-03T09:59:59Z", "type": "session_meta", "payload": map[string]any{"id": "parent"}},
		map[string]any{"timestamp": "2026-10-03T10:00:01Z", "type": "turn_context", "payload": map[string]any{"model": "model-a"}},
		response("2026-10-03T10:00:02Z", "function_call", map[string]any{"call_id": "native", "name": "exec_command", "arguments": "{}"}),
		response("2026-10-03T10:00:03Z", "function_call_output", map[string]any{"call_id": "native", "output": "redacted"}),
		response("2026-10-03T10:00:04Z", "custom_tool_call", map[string]any{"call_id": "custom", "name": "mcp.lookup", "status": "completed", "input": "{}"}),
		response("2026-10-03T10:00:05Z", "custom_tool_call_output", map[string]any{"call_id": "custom", "output": "redacted"}),
		response("2026-10-03T10:00:06Z", "custom_tool_call_output", map[string]any{"call_id": "custom", "output": "redacted-repeat"}),
		legacy("2026-10-03T10:00:07Z", "before", false),
		response("2026-10-03T10:00:08Z", "function_call", map[string]any{"call_id": "before", "name": "exec_command", "arguments": "{}"}),
		response("2026-10-03T10:00:09Z", "function_call_output", map[string]any{"call_id": "before", "output": "redacted"}),
		response("2026-10-03T10:00:10Z", "function_call", map[string]any{"call_id": "after", "name": "exec_command", "arguments": "{}"}),
		response("2026-10-03T10:00:11Z", "function_call_output", map[string]any{"call_id": "after", "output": "redacted"}),
		legacy("2026-10-03T10:00:12Z", "after", false),
		legacy("2026-10-03T10:00:13Z", "after", true),
		codexEvent("2026-10-03T10:00:14Z", "item_completed", map[string]any{"item": map[string]any{"type": "CommandExecution", "status": "failed"}}),
		response("2026-10-03T10:00:15Z", "function_call_output", map[string]any{"call_id": "missing", "output": "redacted"}),
		response("2026-10-03T10:00:16Z", "function_call", map[string]any{"call_id": "parallel-a", "name": "parallel", "arguments": "{}"}),
		response("2026-10-03T10:00:16Z", "function_call", map[string]any{"call_id": "parallel-b", "name": "parallel", "arguments": "{}"}),
		response("2026-10-03T10:00:17Z", "function_call_output", map[string]any{"call_id": "parallel-a", "output": "redacted"}),
		response("2026-10-03T10:00:17Z", "function_call_output", map[string]any{"call_id": "parallel-b", "output": "redacted"}),
	)

	s, err := BuildCodex(path, Identity{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if s.SessionID != "child" {
		t.Fatalf("session id = %q", s.SessionID)
	}
	got := events(s, "codex.tool_result")
	if len(got) != 7 {
		t.Fatalf("tool results = %+v", got)
	}
	if got[0].Time.Format(time.RFC3339) != "2026-10-03T10:00:03Z" || got[0].Attrs["tool_name"] != "exec_command" || got[0].Attrs["model"] != "model-a" || got[0].Attrs["call_id"] != "native" || got[0].Attrs["success"] != nil || got[0].Attrs["duration_ms"] != nil {
		t.Errorf("native function result = %+v", got[0])
	}
	if got[1].Time.Format(time.RFC3339) != "2026-10-03T10:00:05Z" || got[1].Attrs["tool_name"] != "mcp.lookup" || got[1].Attrs["call_id"] != "custom" || got[1].Attrs["success"] != nil || got[1].Attrs["duration_ms"] != nil {
		t.Errorf("native custom result = %+v", got[1])
	}
	if got[2].Attrs["duration_ms"] != int64(2000) || got[2].Attrs["call_id"] != "before" || got[3].Attrs["duration_ms"] != int64(2000) || got[3].Attrs["call_id"] != "after" || got[4].Attrs["success"] != false || got[4].Attrs["call_id"] != nil {
		t.Errorf("legacy results = %+v", got[2:])
	}
	if got[5].Time != got[6].Time || got[5].Attrs["tool_name"] != "parallel" || got[6].Attrs["tool_name"] != "parallel" || got[5].Attrs["call_id"] != "parallel-a" || got[6].Attrs["call_id"] != "parallel-b" {
		t.Errorf("same-time native results = %+v", got[5:])
	}
	if s.Missing["codex.native_tool_output_missing_metadata"] != 1 {
		t.Errorf("missing = %+v", s.Missing)
	}

	cutoff, err := time.Parse(time.RFC3339, "2026-10-03T10:00:04Z")
	if err != nil {
		t.Fatal(err)
	}
	cut, err := BuildCodex(path, Identity{}, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if results := events(cut, "codex.tool_result"); len(results) != 1 || results[0].Attrs["tool_name"] != "exec_command" {
		t.Errorf("cutoff results = %+v", results)
	}

	other := filepath.Join(t.TempDir(), "rollout-second-session.jsonl")
	writeLines(t, other,
		map[string]any{"timestamp": "2026-10-03T11:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "other"}},
		map[string]any{"timestamp": "2026-10-03T11:00:01Z", "type": "turn_context", "payload": map[string]any{"model": "model-b"}},
		response("2026-10-03T11:00:02Z", "function_call", map[string]any{"call_id": "native", "name": "other_tool", "arguments": "{}"}),
		response("2026-10-03T11:00:03Z", "function_call_output", map[string]any{"call_id": "native", "output": "redacted"}),
	)
	otherSession, err := BuildCodex(other, Identity{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if results := events(otherSession, "codex.tool_result"); len(results) != 1 || results[0].Attrs["tool_name"] != "other_tool" || results[0].Attrs["model"] != "model-b" {
		t.Errorf("second session results = %+v", results)
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

	locked := filepath.Join(home, "sessions", "2026")
	if err := os.Mkdir(locked, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0700)
	archived := filepath.Join(home, "archived_sessions")
	if err := os.WriteFile(archived, nil, 0600); err != nil {
		t.Fatal(err)
	}
	paths, err := CodexRollouts(home)
	joined, _ := err.(interface{ Unwrap() []error })
	if joined == nil || len(joined.Unwrap()) != 2 || !strings.Contains(err.Error(), locked) || !strings.Contains(err.Error(), archived) ||
		len(paths) != 1 || filepath.Base(paths[0]) != "rollout-bad.jsonl" {
		t.Fatalf("unreadable directory and root: paths=%v err=%v, want the other rollouts and one error per path", paths, err)
	}
}

func TestEncodeStopsAtTheFirstEmitError(t *testing.T) {
	s := &Session{Source: "claude_code", SessionID: sid, Service: "claude-code", Common: map[string]any{"session.id": sid}}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := range 20000 {
		s.Events = append(s.Events, Event{Name: "tool_result", Time: start.Add(time.Duration(i) * time.Second),
			Attrs: map[string]any{"filler": string(make([]byte, 200))}})
	}
	stop := errors.New("stop")
	calls := 0
	if err := Encode(s, func(Body) error { calls++; return stop }); !errors.Is(err, stop) || calls != 1 {
		t.Fatalf("err=%v calls=%d; want the emit error after one body", err, calls)
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
	var bodies []Body
	err := Encode(s, func(b Body) error { bodies = append(bodies, b); return nil })
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
