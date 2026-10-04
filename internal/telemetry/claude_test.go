package telemetry

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, rows ...map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, r := range rows {
		line, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

const sid = "11111111-1111-1111-1111-111111111111"

func user(at, promptID string, content any) map[string]any {
	return map[string]any{"type": "user", "sessionId": sid, "promptId": promptID, "uuid": "u-" + at, "timestamp": at,
		"entrypoint": "cli", "cwd": "/repo", "message": map[string]any{"role": "user", "content": content}}
}

func assistant(at, requestID, model string, output int, blocks ...map[string]any) map[string]any {
	return map[string]any{"type": "assistant", "sessionId": sid, "timestamp": at, "requestId": requestID,
		"message": map[string]any{"id": "msg-" + requestID, "model": model, "content": blocks,
			"usage": map[string]any{"input_tokens": 10, "output_tokens": output, "cache_read_input_tokens": 1000,
				"cache_creation": map[string]any{"ephemeral_5m_input_tokens": 0, "ephemeral_1h_input_tokens": 100}}}}
}

func toolUse(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func toolResult(id string) map[string]any {
	return map[string]any{"type": "tool_result", "tool_use_id": id, "content": "ok"}
}

func events(s *Session, name string) []Event {
	var out []Event
	for _, e := range s.Events {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

func sum(s *Session, metric, kind string) float64 {
	total := 0.0
	for _, p := range s.Points {
		if p.Metric == metric && p.Type == kind {
			total += p.Value
		}
	}
	return total
}

func buildFixture(t *testing.T, costState bool) *Session {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, sid+".jsonl")
	rows := []map[string]any{
		user("2026-10-01T10:00:00Z", "p1", "fix the bug"),
		// Two streaming snapshots of one request: the larger output count is the final one.
		assistant("2026-10-01T10:00:05Z", "r1", "claude-opus-5-5", 5, toolUse("t1", "Bash", map[string]any{"command": "ls"})),
		assistant("2026-10-01T10:00:06Z", "r1", "claude-opus-5-5", 40, toolUse("t2", "mcp__linear__get_issue", map[string]any{})),
		user("2026-10-01T10:00:08Z", "p1", []any{toolResult("t1"), toolResult("t2")}),
		assistant("2026-10-01T10:00:10Z", "r2", "claude-opus-5-5", 20,
			toolUse("t3", "AskUserQuestion", map[string]any{}), toolUse("t4", "Skill", map[string]any{"skill": "page-loading"})),
		// The user answers four minutes later: the decision is the user's, made at the answer.
		user("2026-10-01T10:04:00Z", "p1", []any{toolResult("t3"), toolResult("t4")}),
		assistant("2026-10-01T10:04:05Z", "r3", "claude-opus-5-5", 30, toolUse("t5", "Agent", map[string]any{"subagent_type": "Explore"})),
		{"type": "user", "sessionId": sid, "promptId": "p1", "timestamp": "2026-10-01T10:04:06Z",
			"message":       map[string]any{"role": "user", "content": []any{toolResult("t5")}},
			"toolUseResult": map[string]any{"isAsync": true, "status": "async_launched", "agentId": "a1"}},
		user("2026-10-01T10:10:00Z", "p2", "<command-name>/loop</command-name><command-args>x</command-args>"),
		{"type": "system", "subtype": "stop_hook_summary", "sessionId": sid, "timestamp": "2026-10-01T10:10:30Z",
			"hookInfos": []any{map[string]any{"durationMs": 400}}},
	}
	if costState {
		rows = append(rows, map[string]any{"type": "cost-state", "sessionId": sid, "startTime": 1790762300000,
			"modelUsage": map[string]any{
				"claude-opus-5-5[1m]":       map[string]any{"inputTokens": 30, "outputTokens": 90, "cacheReadInputTokens": 3000, "cacheCreationInputTokens": 300, "costUSD": 2.5},
				"claude-haiku-4-5-20251001": map[string]any{"inputTokens": 1000, "outputTokens": 15, "costUSD": 0.001},
			}})
	}
	writeLines(t, root, rows...)
	sub := filepath.Join(dir, sid, "subagents", "agent-a1.jsonl")
	writeLines(t, sub,
		map[string]any{"type": "user", "isSidechain": true, "sessionId": sid, "promptId": "p1", "timestamp": "2026-10-01T10:04:07Z",
			"message": map[string]any{"role": "user", "content": "explore"}},
		map[string]any{"type": "assistant", "isSidechain": true, "sessionId": sid, "timestamp": "2026-10-01T10:12:00Z", "requestId": "s1",
			"message": map[string]any{"id": "msg-s1", "model": "claude-opus-5-5",
				"content": []any{map[string]any{"type": "text", "text": strings.Repeat("x", 4000)}},
				"usage":   map[string]any{"input_tokens": 5, "output_tokens": 1, "cache_read_input_tokens": 0}}},
	)
	if err := os.WriteFile(strings.TrimSuffix(sub, ".jsonl")+".meta.json", []byte(`{"agentType":"Explore","toolUseId":"t5","requestShape":"background"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mate := filepath.Join(dir, sid, "subagents", "agent-aeng1.jsonl")
	writeLines(t, mate, map[string]any{"type": "user", "isSidechain": true, "sessionId": sid, "promptId": "p1",
		"timestamp": "2026-10-01T10:05:00Z", "message": map[string]any{"role": "user", "content": "work"}})
	if err := os.WriteFile(strings.TrimSuffix(mate, ".jsonl")+".meta.json", []byte(`{"agentType":"eng1","taskKind":"in_process_teammate"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ident := Identity{Email: "dev@example.com", UserID: "hash"}
	s, err := BuildClaude(ClaudeInput{Path: root, Subagents: ClaudeSubagents(root)}, ident, func(cwd string) string {
		if cwd == "/repo" {
			return "https://github.com/acme/repo"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBuildClaudeEvents(t *testing.T) {
	s := buildFixture(t, true)

	if s.SessionID != sid || s.Common["user.email"] != "dev@example.com" || s.Common["vcs.repository.url.full"] != "https://github.com/acme/repo" ||
		s.Common["terminal.type"] != "unknown" {
		t.Fatalf("common attributes = %v", s.Common)
	}
	prompts := events(s, "user_prompt")
	if len(prompts) != 2 || prompts[0].Attrs["prompt.id"] != "p1" || prompts[1].Attrs["prompt.id"] != "p2" {
		t.Fatalf("prompts = %v", prompts)
	}
	results := events(s, "tool_result")
	if len(results) != 5 {
		t.Fatalf("tool results = %d, want 5", len(results))
	}
	var mcp int
	for _, r := range results {
		if r.Attrs["mcp_server_scope"] != nil {
			mcp++
			if r.Attrs["tool_name"] != "mcp_tool" {
				t.Errorf("MCP tool name = %v", r.Attrs["tool_name"])
			}
		}
	}
	if mcp != 1 {
		t.Errorf("MCP results = %d, want 1", mcp)
	}
	for _, d := range events(s, "tool_decision") {
		if d.Attrs["tool_name"] == "AskUserQuestion" && !d.Time.Equal(time.Date(2026, 10, 1, 10, 4, 0, 0, time.UTC)) {
			t.Errorf("AskUserQuestion decided at %v, want the answer time", d.Time)
		}
	}
	skills := events(s, "skill_activated")
	if len(skills) != 2 || skills[0].Attrs["skill.name"] != "page-loading" || skills[1].Attrs["skill.name"] != "loop" {
		t.Fatalf("skills = %v", skills)
	}
	completed := events(s, "subagent_completed")
	if len(completed) != 1 {
		t.Fatalf("subagent completions = %d, want 1 (teammates report none)", len(completed))
	}
	if c := completed[0]; c.Attrs["agent_type"] != "Explore" || c.Attrs["is_built_in"] != true || c.Attrs["prompt.id"] != "p2" ||
		!c.Time.Equal(time.Date(2026, 10, 1, 10, 12, 0, 0, time.UTC)) {
		t.Errorf("async subagent completion = %v at %v, want Explore under the prompt active at its end", c.Attrs, c.Time)
	}
	hooks := events(s, "hook_execution_start")
	if len(hooks) != 1 || !hooks[0].Time.Equal(time.Date(2026, 10, 1, 10, 10, 29, 600e6, time.UTC)) {
		t.Errorf("stop hook start = %v", hooks)
	}
	if starts := events(s, "session_start"); len(starts) != 1 || !starts[0].Time.Equal(time.UnixMilli(1790762300000).UTC()) {
		t.Errorf("session start = %v", starts)
	}
}

func TestBuildClaudeUsageMatchesCostState(t *testing.T) {
	s := buildFixture(t, true)

	if s.Estimated {
		t.Fatal("cost marked estimated despite a cost-state row")
	}
	if got := sum(s, "cost.usage", ""); math.Abs(got-2.501) > 1e-9 {
		t.Errorf("cost = %v, want the cost-state total 2.501", got)
	}
	for _, c := range []struct {
		kind string
		want float64
	}{{"input", 1030}, {"output", 105}, {"cacheRead", 3000}, {"cacheCreation", 300}} {
		if got := sum(s, "token.usage", c.kind); got != c.want {
			t.Errorf("%s tokens = %v, want %v", c.kind, got, c.want)
		}
	}
	for _, p := range s.Points {
		if p.Model == "claude-opus-5-5" {
			t.Fatalf("metric model %q, want the cost-state name with its context variant", p.Model)
		}
	}
	seen := map[string]bool{}
	for _, p := range s.Points {
		key := p.Metric + p.Model + p.Type + p.Time.String()
		if seen[key] {
			t.Fatalf("two points share %s; the receiver keeps only one", key)
		}
		seen[key] = true
	}
}

func TestBuildClaudeEstimatesWithoutCostState(t *testing.T) {
	s := buildFixture(t, false)

	if !s.Estimated {
		t.Fatal("cost not marked estimated")
	}
	var subOutput float64
	for _, e := range events(s, "api_request") {
		switch e.Attrs["request_id"] {
		case "r1":
			if e.Attrs["output_tokens"] != int64(40) {
				t.Errorf("r1 output = %v, want the largest snapshot 40", e.Attrs["output_tokens"])
			}
		case "s1":
			subOutput = float64(e.Attrs["output_tokens"].(int64))
			if e.Attrs["query_source"] != "agent:subagent" {
				t.Errorf("subagent request source = %v", e.Attrs["query_source"])
			}
		}
	}
	if want := tokensPerTextChar * 4000; math.Abs(subOutput-want) > 1 {
		t.Errorf("subagent output = %v, want the content estimate %v", subOutput, want)
	}
	if got, want := sum(s, "cost.usage", ""), (30*4+(40+20+30)*20+3000*0.2+300*8+5*4+subOutput*20)/1e6; math.Abs(got-want) > 1e-9 {
		t.Errorf("estimated cost = %v, want %v from the price table", got, want)
	}
}

func TestBuildClaudeUntilDropsLaterRecords(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, sid+".jsonl")
	writeLines(t, root,
		user("2026-10-01T10:00:00Z", "p1", "a"),
		assistant("2026-10-01T10:00:05Z", "r1", "claude-opus-5-5", 5),
		user("2026-10-01T12:00:00Z", "p2", "b"),
		map[string]any{"type": "cost-state", "sessionId": sid, "startTime": 1790762300000,
			"modelUsage": map[string]any{"claude-opus-5-5": map[string]any{"costUSD": 9.0}}},
	)
	s, err := BuildClaude(ClaudeInput{Path: root, Until: time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)}, Identity{}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if n := len(events(s, "user_prompt")); n != 1 {
		t.Errorf("prompts = %d, want 1", n)
	}
	if !s.Estimated || sum(s, "cost.usage", "") >= 9 {
		t.Errorf("a cut session must not take the whole session's cost-state total")
	}
}
