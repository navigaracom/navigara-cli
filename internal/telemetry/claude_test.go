package telemetry

import (
	"encoding/json"
	"fmt"
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
	if starts := events(s, "session_start"); len(starts) != 1 || !starts[0].Time.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("session start = %v, want the first row", starts)
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

func TestBuildClaudeSettlesEachProcessRun(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, sid+".jsonl")
	writeLines(t, root,
		user("2026-10-01T10:00:00Z", "p1", "a"),
		assistant("2026-10-01T10:00:05Z", "r1", "claude-opus-5-5", 5),
		// The first process exits here; its totals cover only r1.
		map[string]any{"type": "cost-state", "sessionId": sid, "startTime": 1790762300000,
			"modelUsage": map[string]any{"claude-opus-5-5": map[string]any{"inputTokens": 10, "outputTokens": 5, "cacheReadInputTokens": 1000, "cacheCreationInputTokens": 100, "costUSD": 3.0}}},
		// The session is resumed the next day without a new cost-state row.
		user("2026-10-02T10:00:00Z", "p2", "b"),
		assistant("2026-10-02T10:00:05Z", "r2", "claude-opus-5-5", 5),
	)
	s, err := BuildClaude(ClaudeInput{Path: root}, Identity{}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	var first, second float64
	for _, p := range s.Points {
		if p.Metric != "cost.usage" {
			continue
		}
		if p.Time.Before(time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)) {
			first += p.Value
		} else {
			second += p.Value
		}
	}
	want, _ := claudeUsage{input: 10, output: 5, cacheRead: 1000, cacheWrite1h: 100}.cost("claude-opus-5-5")
	if math.Abs(first-3.0) > 1e-9 || math.Abs(second-want) > 1e-9 {
		t.Errorf("run costs = %v, %v; want 3.0 from the first run's row and %v from the price table for the resumed run", first, second, want)
	}
	if !s.Estimated {
		t.Error("a resumed run without its own cost-state is estimated")
	}
	starts := events(s, "session_start")
	if len(starts) != 2 || !starts[1].Time.Equal(time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("session starts = %v, want the first process's start and the resumed run's first row", starts)
	}
}

func TestBuildClaudeReportsCommitsAndBranches(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, sid+".jsonl")
	withBranch := func(row map[string]any) map[string]any {
		row["gitBranch"] = "feature/x"
		return row
	}
	writeLines(t, root,
		user("2026-10-01T10:00:00Z", "p1", "commit it"),
		assistant("2026-10-01T10:00:05Z", "r1", "claude-opus-5-5", 5,
			toolUse("t1", "Bash", map[string]any{"command": "git commit -q -m fix && git commit -q --allow-empty -m 'fix two'"}),
			toolUse("t2", "Bash", map[string]any{"command": "ls"})),
		withBranch(user("2026-10-01T10:00:09Z", "p1", []any{toolResult("t1"), toolResult("t2")})),
	)
	var asked []time.Time
	commits := func(cwd string, from, to time.Time) []Commit {
		asked = append(asked, from, to)
		return []Commit{{SHA: strings.Repeat("a", 40), Subject: "fix"}, {SHA: strings.Repeat("b", 40), Subject: "fix two"}}
	}
	s, err := BuildClaude(ClaudeInput{Path: root, Commits: commits}, Identity{}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	var revisions []string
	for _, e := range events(s, "tool_result") {
		if e.Attrs["vcs.ref.head.name"] != "feature/x" {
			t.Errorf("result branch = %v", e.Attrs["vcs.ref.head.name"])
		}
		if rev, ok := e.Attrs["vcs.ref.head.revision"].(string); ok {
			revisions = append(revisions, rev)
		}
	}
	if n := len(events(s, "tool_result")); n != 2 {
		t.Fatalf("tool results = %d, want one per call", n)
	}
	if len(revisions) != 1 || revisions[0] != strings.Repeat("b", 40) || len(asked) != 4 {
		t.Fatalf("revisions = %v, lookups = %v; want the call's last commit, one lookup per git commit step and none for ls", revisions, asked)
	}
	if !asked[0].Equal(time.Date(2026, 10, 1, 10, 0, 4, 0, time.UTC)) || !asked[1].Equal(time.Date(2026, 10, 1, 10, 0, 10, 0, time.UTC)) {
		t.Errorf("commit window = %v..%v, want the tool call with a second of slack", asked[0], asked[1])
	}
}

func TestCommitCalls(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    []commitCall
	}{
		{`git commit -m "x"`, []commitCall{{"/repo", "x"}}},
		{`cd ../other && git add -A && git commit -m x`, []commitCall{{"/other", "x"}}},
		{`git add -A && cd ../other && git commit -m x`, []commitCall{{"/other", "x"}}},
		{`git -C /other/repo commit -m fix`, []commitCall{{"/other/repo", "fix"}}},
		{`git -C sub -c user.name=x commit -m x`, []commitCall{{"/repo/sub", "x"}}},
		{`git commit -m one && git commit --allow-empty -m two`, []commitCall{{"/repo", "one"}, {"/repo", "two"}}},
		{`git commit -m fix && git commit --amend --no-edit`, []commitCall{{"/repo", "fix"}}},
		{`git merge main && git rebase main`, nil},
		{`git status && echo git commit`, nil},
	} {
		got := commitCalls("/repo", tc.command)
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("commitCalls(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}

func TestBuildClaudeLooksUpCommitsWhereTheCommandCommitted(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, sid+".jsonl")
	writeLines(t, root,
		user("2026-10-01T10:00:00Z", "p1", "commit"),
		assistant("2026-10-01T10:00:05Z", "r1", "claude-opus-5-5", 5,
			toolUse("t1", "Bash", map[string]any{"command": "git -C /other/repo commit -m fix"}),
			toolUse("t2", "Bash", map[string]any{"command": "git add -A && cd ../wt && git commit -m fix"})),
		user("2026-10-01T10:00:09Z", "p1", []any{toolResult("t1"), toolResult("t2")}),
	)
	var dirs []string
	commits := func(cwd string, from, to time.Time) []Commit {
		dirs = append(dirs, cwd)
		return nil
	}
	if _, err := BuildClaude(ClaudeInput{Path: root, Commits: commits}, Identity{}, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 2 || dirs[0] != "/other/repo" || dirs[1] != "/wt" {
		t.Errorf("commit lookups in %v, want /other/repo and /wt", dirs)
	}
}

func TestBuildClaudeGivesParallelCommitsToTheirOwnCalls(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, sid+".jsonl")
	heredoc := "git commit -q -m \"$(cat <<'EOF'\nfeat: two\n\nBody.\nEOF\n)\""
	writeLines(t, root,
		user("2026-10-01T10:00:00Z", "p1", "commit both"),
		assistant("2026-10-01T10:00:05Z", "r1", "claude-opus-5-5", 5,
			toolUse("t1", "Bash", map[string]any{"command": `git commit -m "fix: one"`}),
			toolUse("t2", "Bash", map[string]any{"command": heredoc})),
		user("2026-10-01T10:00:09Z", "p1", []any{toolResult("t1"), toolResult("t2")}),
	)
	both := []Commit{{SHA: strings.Repeat("1", 40), Subject: "fix: one"}, {SHA: strings.Repeat("2", 40), Subject: "feat: two"}}
	s, err := BuildClaude(ClaudeInput{Path: root, Commits: func(string, time.Time, time.Time) []Commit { return both }},
		Identity{}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range events(s, "tool_result") {
		if rev, ok := e.Attrs["vcs.ref.head.revision"].(string); ok {
			got[e.Attrs["tool_use_id"].(string)] = rev[:1]
		}
	}
	if len(got) != 2 || got["t1"] != "1" || got["t2"] != "2" {
		t.Errorf("revisions per call = %v, want t1:1 and t2:2", got)
	}
}

func TestCommitSubject(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{`git commit -m "fix: one"`, "fix: one"},
		{`git commit -q -m 'chore: x' && git push`, "chore: x"},
		{"git commit -q -m \"$(cat <<'EOF'\nfeat: two\n\nBody.\nEOF\n)\"", "feat: two"},
		{"git commit --amend --no-edit", ""},
		{`git commit -m 'say "hi"'`, `say "hi"`},
		{`git commit -m "say \"hi\""`, `say "hi"`},
		{`git commit -m "$MSG"`, ""},
		{`git commit --message=plain && git push`, "plain"},
		{"git commit -m 'feat: x\nmore detail\n\nBody.'", "feat: x more detail"},
		{"git commit -q -F - <<'EOF' 2>&1 | tail -3\nfeat: from stdin\n\nBody.\nEOF", "feat: from stdin"},
		{"git commit -q -am 'fix: all'", "fix: all"},
		{"git -C /repos/my-module commit -m fix", "fix"},
	} {
		if got := commitSubject(tc.command); got != tc.want {
			t.Errorf("commitSubject(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

func TestBuildClaudeLeavesAmbiguousParallelCommitsUnassigned(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, sid+".jsonl")
	writeLines(t, root,
		user("2026-10-01T10:00:00Z", "p1", "commit both"),
		assistant("2026-10-01T10:00:05Z", "r1", "claude-opus-5-5", 5,
			toolUse("t1", "Bash", map[string]any{"command": `git commit -m "fix"`}),
			toolUse("t2", "Bash", map[string]any{"command": `git commit -m 'fix'`}),
			toolUse("t3", "Bash", map[string]any{"command": `git commit -m 'say "hi"'`})),
		user("2026-10-01T10:00:09Z", "p1", []any{toolResult("t1"), toolResult("t2"), toolResult("t3")}),
	)
	found := []Commit{{SHA: strings.Repeat("1", 40), Subject: "fix"}, {SHA: strings.Repeat("2", 40), Subject: "fix"},
		{SHA: strings.Repeat("3", 40), Subject: `say "hi"`}}
	s, err := BuildClaude(ClaudeInput{Path: root, Commits: func(string, time.Time, time.Time) []Commit { return found }},
		Identity{}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range events(s, "tool_result") {
		if rev, ok := e.Attrs["vcs.ref.head.revision"].(string); ok {
			got[e.Attrs["tool_use_id"].(string)] = rev[:1]
		}
	}
	if len(got) != 1 || got["t3"] != "3" {
		t.Errorf("revisions per call = %v; want only t3:3 (same-subject parallel commits are ambiguous)", got)
	}
}

func TestBuildClaudeGivesAParallelMergeNoCommits(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, sid+".jsonl")
	writeLines(t, root,
		user("2026-10-01T10:00:00Z", "p1", "merge and commit"),
		assistant("2026-10-01T10:00:05Z", "r1", "claude-opus-5-5", 5,
			toolUse("t1", "Bash", map[string]any{"command": "git merge main"}),
			toolUse("t2", "Bash", map[string]any{"command": `git commit -m "fix: one"`})),
		user("2026-10-01T10:00:09Z", "p1", []any{toolResult("t1"), toolResult("t2")}),
	)
	found := []Commit{{SHA: strings.Repeat("1", 40), Subject: "fix: one"}, {SHA: strings.Repeat("2", 40), Subject: "Merge branch 'main'"}}
	s, err := BuildClaude(ClaudeInput{Path: root, Commits: func(string, time.Time, time.Time) []Commit { return found }},
		Identity{}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range events(s, "tool_result") {
		if rev, ok := e.Attrs["vcs.ref.head.revision"].(string); ok {
			got[e.Attrs["tool_use_id"].(string)] = rev[:1]
		}
	}
	if len(got) != 1 || got["t2"] != "1" {
		t.Errorf("revisions per call = %v; want only the commit's own revision on t2", got)
	}
}

func TestBuildClaudeKeepsACommitWhenALaterStepFails(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, sid+".jsonl")
	failed := toolResult("t1")
	failed["is_error"] = true
	writeLines(t, root,
		user("2026-10-01T10:00:00Z", "p1", "commit and push"),
		assistant("2026-10-01T10:00:05Z", "r1", "claude-opus-5-5", 5,
			toolUse("t1", "Bash", map[string]any{"command": "git -C /repos/my-module commit -m fix && git push"})),
		user("2026-10-01T10:00:09Z", "p1", []any{failed}),
	)
	found := []Commit{{SHA: strings.Repeat("1", 40), Subject: "fix"}}
	var dirs []string
	s, err := BuildClaude(ClaudeInput{Path: root, Commits: func(cwd string, _, _ time.Time) []Commit {
		dirs = append(dirs, cwd)
		return found
	}}, Identity{}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	results := events(s, "tool_result")
	if len(results) != 1 || results[0].Attrs["vcs.ref.head.revision"] != found[0].SHA || len(dirs) != 1 || dirs[0] != "/repos/my-module" {
		t.Errorf("result = %v, lookups in %v; want the commit made before the push failed", results, dirs)
	}
}

func TestBuildClaudeRevisionIsTheLastStepsCommit(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, sid+".jsonl")
	writeLines(t, root,
		user("2026-10-01T10:00:00Z", "p1", "commit"),
		assistant("2026-10-01T10:00:05Z", "r1", "claude-opus-5-5", 5,
			toolUse("t1", "Bash", map[string]any{"command": `git commit -m a && git commit -m "$MSG"`}),
			toolUse("t2", "Bash", map[string]any{"command": `git commit -m fix && git commit --amend --no-edit`})),
		user("2026-10-01T10:00:09Z", "p1", []any{toolResult("t1"), toolResult("t2")}),
	)
	found := []Commit{
		{SHA: strings.Repeat("a", 40), Subject: "a", Reachable: true},
		{SHA: strings.Repeat("d", 40), Subject: "dynamic", Reachable: true},
		{SHA: strings.Repeat("0", 40), Subject: "fix"},
		{SHA: strings.Repeat("f", 40), Subject: "fix", Reachable: true},
	}
	s, err := BuildClaude(ClaudeInput{Path: root, Commits: func(string, time.Time, time.Time) []Commit { return found }},
		Identity{}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, e := range events(s, "tool_result") {
		got[e.Attrs["tool_use_id"].(string)] = e.Attrs["vcs.ref.head.revision"]
	}
	if got["t1"] != nil || got["t2"] != strings.Repeat("f", 40) {
		t.Errorf("revisions = %v; want none when the last message is unknown, and the amended commit", got)
	}
}
