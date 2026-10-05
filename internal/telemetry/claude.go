package telemetry

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ClaudeInput is one Claude Code session on disk: the root transcript and its subagent transcripts.
type ClaudeInput struct {
	Path      string
	Subagents []string
	// Until drops records after this time; the session's own cost totals then no longer apply.
	Until time.Time
	// Commits lists the commits made in a working directory's repository in [from, to].
	Commits func(cwd string, from, to time.Time) []Commit
}

// ClaudeSubagents lists the subagent transcripts Claude Code keeps next to a root transcript.
func ClaudeSubagents(rootPath string) []string {
	dir := strings.TrimSuffix(rootPath, ".jsonl")
	paths, _ := filepath.Glob(filepath.Join(dir, "subagents", "agent-*.jsonl"))
	sort.Strings(paths)
	return paths
}

// builtInAgents are the subagent types Claude Code ships; anything else is custom.
var builtInAgents = map[string]bool{
	"general-purpose": true, "Explore": true, "Plan": true, "statusline-setup": true,
	"claude-code-guide": true, "output-style-setup": true, "fork": true,
}

// userTurnTools emit their tool_decision when the user answers, not when the agent asks.
var userTurnTools = map[string]bool{"AskUserQuestion": true}

type claudeRow struct {
	Type          string          `json:"type"`
	SessionID     string          `json:"sessionId"`
	UUID          string          `json:"uuid"`
	PromptID      string          `json:"promptId"`
	Timestamp     string          `json:"timestamp"`
	IsSidechain   bool            `json:"isSidechain"`
	Entrypoint    string          `json:"entrypoint"`
	Cwd           string          `json:"cwd"`
	GitBranch     string          `json:"gitBranch"`
	RequestID     string          `json:"requestId"`
	Message       json.RawMessage `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Subtype       string          `json:"subtype"`
	Operation     string          `json:"operation"`
	Content       json.RawMessage `json:"content"`
	HookInfos     []struct {
		DurationMs int64 `json:"durationMs"`
	} `json:"hookInfos"`
	CompactMetadata *struct {
		DurationMs int64 `json:"durationMs"`
	} `json:"compactMetadata"`
	// cost-state rows
	TotalCostUSD float64                    `json:"totalCostUSD"`
	StartTime    int64                      `json:"startTime"`
	ModelUsage   map[string]claudeModelCost `json:"modelUsage"`
}

type claudeModelCost struct {
	InputTokens              int64   `json:"inputTokens"`
	OutputTokens             int64   `json:"outputTokens"`
	CacheReadInputTokens     int64   `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64   `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
}

type claudeMessage struct {
	ID      string          `json:"id"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   *struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheCreation            *struct {
			Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
			Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation"`
	} `json:"usage"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
	Text      string          `json:"text"`
	Signature string          `json:"signature"`
}

type parsedRow struct {
	claudeRow
	at time.Time
}

func readClaudeRows(path string) ([]parsedRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	var rows []parsedRow
	scanner := bufio.NewScanner(io.LimitReader(f, info.Size()))
	scanner.Buffer(make([]byte, 0, 1<<20), 256<<20)
	line := 0
	for scanner.Scan() {
		line++
		b := scanner.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		var r claudeRow
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("line %d: invalid JSON", line)
		}
		var at time.Time
		if r.Timestamp != "" {
			if at, err = time.Parse(time.RFC3339Nano, r.Timestamp); err != nil {
				return nil, fmt.Errorf("line %d: invalid timestamp", line)
			}
			at = at.UTC()
		}
		rows = append(rows, parsedRow{claudeRow: r, at: at})
	}
	return rows, scanner.Err()
}

type pendingTool struct {
	name     string
	input    json.RawMessage
	at       time.Time
	promptID string
}

type claudeRequest struct {
	model string
	usage claudeUsage
	// Output content size, to estimate output tokens a transcript never finalized.
	textChars, toolChars, signatureChars int
	at                                   time.Time
	first                                time.Time
	promptID                             string
	source                               string
	id                                   string
}

// BuildClaude rebuilds a Claude Code session's telemetry. The repository resolver maps a
// recorded working directory to the repository URL Claude Code would have reported.
func BuildClaude(in ClaudeInput, ident Identity, repository func(cwd string) string) (*Session, error) {
	rows, err := readClaudeRows(in.Path)
	if err != nil {
		return nil, err
	}
	sessionID := ""
	for _, r := range rows {
		if r.SessionID != "" && !r.IsSidechain {
			sessionID = r.SessionID
			break
		}
	}
	if sessionID == "" {
		return nil, fmt.Errorf("no root session id")
	}
	cut := false
	if !in.Until.IsZero() {
		kept := rows[:0]
		for _, r := range rows {
			if r.at.After(in.Until) {
				cut = true
				continue
			}
			kept = append(kept, r)
		}
		rows = kept
	}
	s := &Session{Source: "claude_code", Service: "claude-code", seen: newSeen(), commits: in.Commits}
	s.SessionID = sessionID
	hasTimedRow := false
	for _, r := range rows {
		hasTimedRow = hasTimedRow || !r.at.IsZero()
	}

	var cwd, entrypoint string
	for _, r := range rows {
		if r.Cwd != "" && cwd == "" {
			cwd = r.Cwd
		}
		if r.Entrypoint != "" && entrypoint == "" {
			entrypoint = r.Entrypoint
		}
	}
	s.Common = ident.claudeAttributes()
	s.Common["session.id"] = s.SessionID
	s.Common["terminal.type"] = terminalType(entrypoint)
	if url := repository(cwd); url != "" {
		s.Common["vcs.repository.url.full"] = url
	}

	for _, r := range rows {
		if r.UUID != "" {
			s.seen.rows[r.UUID] = true
		}
	}
	prompts := s.walkClaude(rows, false, nil)
	syncAgents := map[string]bool{}
	for _, e := range s.Events {
		if e.Name == "subagent_completed" {
			if id, ok := e.Attrs["tool_use_id"].(string); ok {
				syncAgents[id] = true
			}
		}
	}
	for _, path := range in.Subagents {
		subRows, err := readClaudeRows(path)
		if err != nil {
			s.miss("unreadable subagent transcript")
			continue
		}
		if !in.Until.IsZero() {
			kept := subRows[:0]
			for _, r := range subRows {
				if !r.at.After(in.Until) {
					kept = append(kept, r)
				}
			}
			subRows = kept
		}
		s.walkClaude(subRows, true, prompts)
		s.subagentCompleted(path, subRows, prompts, syncAgents)
	}
	if cut {
		s.Estimated = true
	} else {
		s.reconcileCost(rows)
	}
	if s.Estimated && hasTimedRow {
		// A process without a cost-state row has no recorded start: its first row is the closest.
		s.event("session_start", firstAfter(rows, s.lastSettled), map[string]any{})
	}
	s.mergePoints()
	s.sortRecords()
	return s, nil
}

func terminalType(entrypoint string) string {
	switch entrypoint {
	case "sdk-cli", "sdk-ts", "sdk-py", "print":
		return "non-interactive"
	case "":
		return ""
	}
	return "unknown"
}

// promptTimeline is the root session's prompts in time order, to find the prompt active at a time.
type promptTimeline []struct {
	at time.Time
	id string
}

func (p promptTimeline) at(t time.Time) string {
	id := ""
	for _, e := range p {
		if e.at.After(t) {
			break
		}
		id = e.id
	}
	if id == "" && len(p) > 0 {
		id = p[0].id
	}
	return id
}

// walkClaude maps one transcript's rows to events and usage. Subagent transcripts report no
// prompts of their own: their records ride on the prompt that launched them.
func (s *Session) walkClaude(rows []parsedRow, subagent bool, timeline promptTimeline) promptTimeline {
	requests := map[string]*claudeRequest{}
	var order []*claudeRequest
	tools := map[string]*pendingTool{}
	var toolOrder []string
	seenPrompt := map[string]bool{}
	current := ""
	var queued []string
	querySource := "repl_main_thread"
	if subagent {
		querySource = "agent:subagent"
	}

	for line, r := range rows {
		if r.IsSidechain && !subagent {
			continue
		}
		// A fork's transcript starts with a copy of the parent conversation.
		if subagent && r.UUID != "" && s.seen.rows[r.UUID] {
			continue
		}
		if r.PromptID != "" {
			current = r.PromptID
		}
		switch r.Type {
		case "system":
			if !subagent {
				s.systemRow(r, current)
			}
		case "queue-operation":
			if subagent {
				continue
			}
			var content string
			_ = json.Unmarshal(r.Content, &content)
			switch r.Operation {
			case "enqueue":
				queued = append(queued, content)
			case "dequeue":
				if len(queued) == 0 {
					continue
				}
				head := queued[0]
				queued = queued[1:]
				// A queued slash command that becomes a model turn reports its own prompt when it
				// leaves the queue; a local command (/plugin, /reload-plugins) does not.
				if name, _, _ := strings.Cut(strings.TrimPrefix(head, "/"), " "); strings.HasPrefix(head, "/") && !localCommand(rows, line, name) {
					id := syntheticID(s.SessionID, r.Timestamp)
					s.event("user_prompt", r.at, map[string]any{"prompt.id": id, "prompt_length": fmt.Sprint(len(head))})
					timeline = append(timeline, struct {
						at time.Time
						id string
					}{r.at, id})
				}
			}
		case "user":
			var m claudeMessage
			if json.Unmarshal(r.Message, &m) != nil {
				continue
			}
			var blocks []claudeBlock
			text := ""
			if json.Unmarshal(m.Content, &blocks) != nil {
				_ = json.Unmarshal(m.Content, &text)
			}
			results := 0
			for _, b := range blocks {
				if b.Type == "tool_result" {
					results++
					if !s.seen.results[b.ToolUseID] {
						s.seen.results[b.ToolUseID] = true
						s.toolResult(r, b, tools[b.ToolUseID], current)
					}
				}
			}
			if results == 0 && !subagent && r.PromptID != "" && !seenPrompt[r.PromptID] {
				seenPrompt[r.PromptID] = true
				length := len(text)
				for _, b := range blocks {
					length += len(b.Text)
				}
				s.event("user_prompt", r.at, map[string]any{
					"prompt.id": r.PromptID, "message.uuid": r.UUID, "prompt_length": fmt.Sprint(length),
				})
				timeline = append(timeline, struct {
					at time.Time
					id string
				}{r.at, r.PromptID})
				for _, name := range slashSkills(text, blocks) {
					s.event("skill_activated", r.at, map[string]any{
						"prompt.id": r.PromptID, "skill.name": name, "invocation_trigger": "user-slash",
					})
				}
			}
		case "assistant":
			var m claudeMessage
			if json.Unmarshal(r.Message, &m) != nil {
				continue
			}
			// "<synthetic>" rows are Claude Code's own messages (errors, interruptions), not API calls.
			if m.Usage != nil && r.RequestID != "" && m.Model != "<synthetic>" && (requests[r.RequestID] != nil || !s.seen.requests[r.RequestID]) {
				s.seen.requests[r.RequestID] = true
				req := requests[r.RequestID]
				if req == nil {
					req = &claudeRequest{first: r.at, promptID: current, source: querySource, id: r.RequestID}
					requests[r.RequestID] = req
					order = append(order, req)
				}
				u := m.Usage
				req.model, req.at = m.Model, r.at
				// Rows are streaming snapshots; the largest output count is the final one.
				output := max(req.usage.output, u.OutputTokens)
				req.usage = claudeUsage{input: u.InputTokens, output: output, cacheRead: u.CacheReadInputTokens}
				if u.CacheCreation != nil {
					req.usage.cacheWrite5m, req.usage.cacheWrite1h = u.CacheCreation.Ephemeral5m, u.CacheCreation.Ephemeral1h
				} else {
					req.usage.cacheWrite5m = u.CacheCreationInputTokens
				}
			}
			var blocks []claudeBlock
			if json.Unmarshal(m.Content, &blocks) != nil {
				continue
			}
			if req := requests[r.RequestID]; req != nil {
				for _, b := range blocks {
					switch b.Type {
					case "text":
						req.textChars += len(b.Text)
					case "tool_use":
						req.toolChars += len(b.Input)
					case "thinking":
						req.signatureChars += len(b.Signature)
					}
				}
			}
			for _, b := range blocks {
				if b.Type != "tool_use" || s.seen.tools[b.ID] {
					continue
				}
				s.seen.tools[b.ID] = true
				tools[b.ID] = &pendingTool{name: b.Name, input: b.Input, at: r.at, promptID: current}
				toolOrder = append(toolOrder, b.ID)
				if b.Name == "Skill" {
					var input struct {
						Skill string `json:"skill"`
					}
					if json.Unmarshal(b.Input, &input) == nil && input.Skill != "" {
						s.event("skill_activated", r.at, map[string]any{
							"prompt.id": current, "skill.name": input.Skill, "invocation_trigger": "model",
						})
					}
				}
			}
		}
	}
	for _, req := range order {
		s.apiRequest(req)
	}
	// A call without a result (interrupted, or the session ended) was still decided.
	for _, id := range toolOrder {
		if t := tools[id]; !s.seen.results[id] && !userTurnTools[t.name] {
			s.event("tool_decision", t.at, map[string]any{"prompt.id": t.promptID, "tool_name": liveToolName(t.name),
				"tool_use_id": id, "decision": "accept", "source": "config"})
		}
	}
	return timeline
}

func liveToolName(name string) string {
	if strings.HasPrefix(name, "mcp__") {
		return "mcp_tool"
	}
	return name
}

func (s *Session) toolResult(r parsedRow, b claudeBlock, tool *pendingTool, current string) {
	if tool == nil {
		s.miss("tool result without a tool use")
		return
	}
	promptID := tool.promptID
	if promptID == "" {
		promptID = current
	}
	decision := map[string]any{"prompt.id": promptID, "tool_name": liveToolName(tool.name), "tool_use_id": b.ToolUseID,
		"decision": "accept", "source": "config"}
	if denied(b) {
		// A denied call never runs: Claude Code reports the rejection and no result.
		decision["decision"], decision["source"] = "reject", "classifier"
		s.event("tool_decision", r.at, decision)
		return
	}
	if userTurnTools[tool.name] {
		decision["source"] = "user_temporary"
		s.event("tool_decision", r.at, decision)
	} else {
		s.event("tool_decision", tool.at, decision)
	}
	attrs := map[string]any{
		"prompt.id": promptID, "tool_name": liveToolName(tool.name), "tool_use_id": b.ToolUseID,
		"success": fmt.Sprint(!b.IsError),
	}
	duration := r.at.Sub(tool.at).Milliseconds()
	var result struct {
		DurationMs      *int64 `json:"durationMs"`
		TotalDurationMs *int64 `json:"totalDurationMs"`
		IsAsync         bool   `json:"isAsync"`
		Status          string `json:"status"`
		AgentType       string `json:"agentType"`
	}
	_ = json.Unmarshal(r.ToolUseResult, &result)
	if result.DurationMs != nil {
		duration = *result.DurationMs
	}
	if duration >= 0 {
		attrs["duration_ms"] = duration
	}
	if strings.HasPrefix(tool.name, "mcp__") {
		attrs["mcp_server_scope"] = "local"
	}
	if r.GitBranch != "" && r.GitBranch != "HEAD" {
		attrs["vcs.ref.head.name"] = r.GitBranch
	}
	revision := ""
	// A later step can fail after the commit; a commit that did not happen simply matches nothing.
	if tool.name == "Bash" && s.commits != nil {
		var input struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(tool.input, &input)
		calls := commitCalls(r.Cwd, input.Command)
		for i, call := range calls {
			sha := s.claimCommit(s.commits(call.dir, tool.at.Add(-time.Second), r.at.Add(time.Second)), call)
			// The head after the command is the last step's commit: unknown when that step does not match.
			if i == len(calls)-1 && sha != "" {
				revision = sha
			}
		}
	}
	// Like live, one revision per result: the head after the command.
	if revision != "" {
		attrs["vcs.ref.head.revision"] = revision
	}
	s.event("tool_result", r.at, attrs)

	if (tool.name == "Agent" || tool.name == "Task") && !result.IsAsync && result.Status != "async_launched" && result.Status != "teammate_spawned" {
		var input struct {
			SubagentType string `json:"subagent_type"`
		}
		_ = json.Unmarshal(tool.input, &input)
		agentType := input.SubagentType
		if agentType == "" {
			agentType = "general-purpose"
		}
		total := duration
		if result.TotalDurationMs != nil {
			total = *result.TotalDurationMs
		}
		s.event("subagent_completed", r.at, subagentAttrs(promptID, agentType, false, total, b.ToolUseID))
	}
}

func subagentAttrs(promptID, agentType string, async bool, durationMs int64, toolUseID string) map[string]any {
	source := "built-in"
	if !builtInAgents[agentType] {
		source = "custom"
		if strings.Contains(agentType, ":") {
			source = "plugin"
		}
	}
	return map[string]any{
		"prompt.id": promptID, "agent_type": agentType, "is_built_in": builtInAgents[agentType],
		"agent.source": source, "is_async": async, "duration_ms": durationMs, "tool_use_id": toolUseID,
	}
}

// subagentCompleted reports an async subagent when its transcript ends, under the root prompt active then.
func (s *Session) subagentCompleted(path string, rows []parsedRow, timeline promptTimeline, sync map[string]bool) {
	var meta struct {
		AgentType    string `json:"agentType"`
		ToolUseID    string `json:"toolUseId"`
		RequestShape string `json:"requestShape"`
		TaskKind     string `json:"taskKind"`
	}
	if b, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".meta.json"); err == nil {
		_ = json.Unmarshal(b, &meta)
	}
	// Teammates run beside the session; Claude Code reports no completion for them.
	if (meta.ToolUseID != "" && sync[meta.ToolUseID]) || meta.TaskKind == "in_process_teammate" {
		return
	}
	var first, last time.Time
	for _, r := range rows {
		if r.at.IsZero() {
			continue
		}
		if first.IsZero() {
			first = r.at
		}
		last = r.at
	}
	if last.IsZero() {
		return
	}
	agentType := meta.AgentType
	if agentType == "" {
		agentType = "general-purpose"
	}
	s.event("subagent_completed", last, subagentAttrs(timeline.at(last), agentType, meta.RequestShape == "background",
		last.Sub(first).Milliseconds(), meta.ToolUseID))
}

// Output tokens per character of recorded output, fitted on finished sessions against Claude Code's
// own counts. Thinking is redacted, but its signature grows with it.
const (
	tokensPerTextChar      = 0.3994
	tokensPerToolChar      = 0.4156
	tokensPerSignatureChar = 0.2772
)

func (s *Session) apiRequest(req *claudeRequest) {
	// Subagent transcripts keep only the first streaming snapshot of the output count.
	estimate := int64(tokensPerTextChar*float64(req.textChars) + tokensPerToolChar*float64(req.toolChars) +
		tokensPerSignatureChar*float64(req.signatureChars))
	if req.usage.output*2 < estimate {
		req.usage.output = estimate
		s.miss("estimated output tokens")
	}
	cost, priced := req.usage.cost(req.model)
	if !priced {
		s.miss("unpriced model " + req.model)
	}
	attrs := map[string]any{
		"prompt.id": req.promptID, "model": req.model, "request_id": req.id, "query_source": req.source,
		"input_tokens": req.usage.input, "output_tokens": req.usage.output,
		"cache_read_tokens": req.usage.cacheRead, "cache_creation_tokens": req.usage.cacheCreation(),
		"cost_usd": cost, "duration_ms": req.at.Sub(req.first).Milliseconds(),
	}
	s.event("api_request", req.at, attrs)
	s.Points = append(s.Points,
		Point{Metric: "cost.usage", Time: req.at, Model: req.model, Value: cost},
		Point{Metric: "token.usage", Time: req.at, Model: req.model, Type: "input", Value: float64(req.usage.input)},
		Point{Metric: "token.usage", Time: req.at, Model: req.model, Type: "output", Value: float64(req.usage.output)},
		Point{Metric: "token.usage", Time: req.at, Model: req.model, Type: "cacheRead", Value: float64(req.usage.cacheRead)},
		Point{Metric: "token.usage", Time: req.at, Model: req.model, Type: "cacheCreation", Value: float64(req.usage.cacheCreation())},
	)
}

// slashSkills names the skills a typed slash command invoked.
func slashSkills(text string, blocks []claudeBlock) []string {
	for _, b := range blocks {
		text += b.Text
	}
	var out []string
	for {
		start := strings.Index(text, "<command-name>/")
		if start < 0 {
			return out
		}
		text = text[start+len("<command-name>/"):]
		end := strings.Index(text, "</command-name>")
		if end < 0 {
			return out
		}
		out = append(out, text[:end])
		text = text[end:]
	}
}

// reconcileCost rescales per-request cost to the session's own cost-state totals, which also
// cover background calls the transcript never records, and adds those calls' residue. Claude Code
// writes a cost-state row when a process exits, for that process only: each row settles the
// requests since the previous process's row. Requests after the last row keep the price table.
func (s *Session) reconcileCost(rows []parsedRow) {
	type run struct {
		start int64
		end   time.Time
		usage map[string]claudeModelCost
	}
	var runs []run
	var lastAt time.Time
	for _, r := range rows {
		if !r.at.IsZero() {
			lastAt = r.at
		}
		if r.Type != "cost-state" || r.ModelUsage == nil {
			continue
		}
		// A process rewrites its row; the last copy of a start time is the final one.
		if n := len(runs); n > 0 && runs[n-1].start == r.StartTime {
			runs[n-1] = run{start: r.StartTime, end: lastAt, usage: r.ModelUsage}
			continue
		}
		runs = append(runs, run{start: r.StartTime, end: lastAt, usage: r.ModelUsage})
	}
	if len(runs) == 0 {
		s.Estimated = true
		return
	}
	// The last process's row, with nothing after it, covers everything up to the session end.
	if last := &runs[len(runs)-1]; !last.end.Before(lastAt) {
		last.end = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	for _, r := range runs {
		if r.start > 0 {
			s.event("session_start", time.UnixMilli(r.start).UTC(), map[string]any{})
		}
	}
	// Metrics name the context variant ("claude-opus-5-5[1m]"); events name the base model.
	names := map[string]bool{}
	for _, r := range runs {
		for model := range r.usage {
			names[model] = true
		}
	}
	for i := range s.Points {
		if m := s.Points[i].Model; !names[m] && names[m+"[1m]"] {
			s.Points[i].Model = m + "[1m]"
		}
	}
	var from time.Time
	for _, r := range runs {
		s.settleRun(from, r.end, r.usage)
		from = r.end
	}
	if s.End.After(from) {
		s.Estimated = true
		s.lastSettled = from
	}
}

// settleRun reconciles the points in (from, to] to one process's cost-state totals.
func (s *Session) settleRun(from, to time.Time, totals map[string]claudeModelCost) {
	in := func(p Point) bool { return p.Time.After(from) && !p.Time.After(to) }
	type sums struct{ cost, input, output, cacheRead, cacheCreation float64 }
	seen := map[string]*sums{}
	for _, p := range s.Points {
		if !in(p) {
			continue
		}
		x := seen[p.Model]
		if x == nil {
			x = &sums{}
			seen[p.Model] = x
		}
		switch {
		case p.Metric == "cost.usage":
			x.cost += p.Value
		case p.Type == "input":
			x.input += p.Value
		case p.Type == "output":
			x.output += p.Value
		case p.Type == "cacheRead":
			x.cacheRead += p.Value
		case p.Type == "cacheCreation":
			x.cacheCreation += p.Value
		}
	}
	at := to.Add(time.Millisecond)
	if to.Year() == 9999 {
		at = s.End.Add(time.Millisecond)
	}
	for model, t := range totals {
		x := seen[model]
		if x == nil {
			x = &sums{}
		}
		if x.cost > 0 {
			scale := t.CostUSD / x.cost
			for i := range s.Points {
				if p := &s.Points[i]; p.Model == model && p.Metric == "cost.usage" && in(*p) {
					p.Value *= scale
				}
			}
		} else if t.CostUSD > 0 {
			s.Points = append(s.Points, Point{Metric: "cost.usage", Time: at, Model: model, Value: t.CostUSD})
		}
		for _, r := range []struct {
			kind string
			want int64
			have float64
		}{{"input", t.InputTokens, x.input}, {"output", t.OutputTokens, x.output},
			{"cacheRead", t.CacheReadInputTokens, x.cacheRead}, {"cacheCreation", t.CacheCreationInputTokens, x.cacheCreation}} {
			switch {
			case float64(r.want) > r.have:
				s.Points = append(s.Points, Point{Metric: "token.usage", Time: at, Model: model, Type: r.kind, Value: float64(r.want) - r.have})
			case float64(r.want) < r.have && r.have > 0:
				scale := float64(r.want) / r.have
				for i := range s.Points {
					if p := &s.Points[i]; p.Model == model && p.Metric == "token.usage" && p.Type == r.kind && in(*p) {
						p.Value *= scale
					}
				}
			}
		}
	}
}

// systemRow maps the system rows that stand for live events: Stop hooks and background API calls.
func (s *Session) systemRow(r parsedRow, promptID string) {
	switch r.Subtype {
	case "stop_hook_summary":
		var total int64
		for _, h := range r.HookInfos {
			total += h.DurationMs
		}
		attrs := func() map[string]any {
			return map[string]any{"prompt.id": promptID, "hook_event": "Stop", "hook_name": "Stop", "num_hooks": int64(len(r.HookInfos))}
		}
		s.event("hook_execution_start", r.at.Add(-time.Duration(total)*time.Millisecond), attrs())
		s.event("hook_execution_complete", r.at, attrs())
	case "away_summary":
		s.event("api_request", r.at, map[string]any{"prompt.id": promptID, "query_source": "away_summary"})
	case "compact_boundary":
		attrs := map[string]any{"prompt.id": promptID, "query_source": "compact"}
		if r.CompactMetadata != nil {
			attrs["duration_ms"] = r.CompactMetadata.DurationMs
		}
		s.event("api_request", r.at, attrs)
	}
}

// syntheticID is a stable UUID-shaped id for a prompt the transcript does not name.
func syntheticID(sessionID, at string) string {
	sum := sha256.Sum256([]byte(sessionID + "|" + at))
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// localCommand reports whether the next row that runs the named slash command is a local
// command: Claude Code records those with a local-command caveat or output in the same prompt.
func localCommand(rows []parsedRow, from int, name string) bool {
	tag := "<command-name>/" + name + "</command-name>"
	for i := from; i < len(rows); i++ {
		r := rows[i]
		if r.Type != "user" || !strings.Contains(string(r.Message), tag) {
			continue
		}
		for _, other := range rows {
			if other.PromptID == r.PromptID && other.Type == "user" &&
				(strings.Contains(string(other.Message), "<local-command-caveat>") || strings.Contains(string(other.Message), "<local-command-stdout>")) {
				return true
			}
		}
		return false
	}
	return false
}

// denied reports a tool result that is a permission refusal rather than an outcome.
func denied(b claudeBlock) bool {
	if !b.IsError {
		return false
	}
	var text string
	if json.Unmarshal(b.Content, &text) != nil {
		var parts []struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(b.Content, &parts)
		for _, p := range parts {
			text += p.Text
		}
	}
	return strings.Contains(text, "Permission for this action was denied") ||
		strings.Contains(text, "doesn't want to proceed with this tool use") ||
		strings.Contains(text, "tool use was rejected")
}

// firstAfter is the time of the first timestamped row after t.
func firstAfter(rows []parsedRow, t time.Time) time.Time {
	for _, r := range rows {
		if !r.at.IsZero() && r.at.After(t) {
			return r.at
		}
	}
	return t
}

// commitCall is one "git commit" in a shell command: where it runs and its message's subject.
type commitCall struct {
	dir, subject string
	// amend: the step replaced a commit of the same command, which the reflog still holds.
	amend bool
}

// commitCalls finds every "git commit" of a shell command. The directory is the recorded working
// directory moved by each "cd DIR" before it and by "git -C DIR". The subject is "" when the message
// is not a literal the shell would pass unchanged.
func commitCalls(cwd, command string) []commitCall {
	var calls []commitCall
	// Search the shell text only: a heredoc message may hold lines that start with "git".
	shell := maskHeredocs(command)
	for _, at := range gitInvocation.FindAllStringIndex(shell, -1) {
		dir := dirBefore(cwd, shell[:at[0]])
		fields := strings.Fields(shell[at[1]:])
		for i := 0; i < len(fields); i++ {
			f := fields[i]
			switch {
			case f == "-C" && i+1 < len(fields):
				dir = moveDir(dir, fields[i+1])
				i++
				continue
			case f == "-c" || f == "--git-dir" || f == "--work-tree":
				i++
				continue
			case strings.HasPrefix(f, "-"):
				continue
			case f == "commit":
				end := len(command)
				if next := gitInvocation.FindStringIndex(shell[at[1]:]); next != nil {
					end = at[1] + next[0]
				}
				call := commitCall{dir: dir, subject: commitSubject(command[at[1]:end]),
					amend: strings.Contains(" "+shell[at[1]:end]+" ", " --amend ")}
				// An amend without a message keeps the message of the commit it replaces.
				if n := len(calls); n > 0 && call.amend && call.subject == "" && calls[n-1].dir == dir {
					call.subject = calls[n-1].subject
					calls = calls[:n-1]
				}
				calls = append(calls, call)
			}
			break
		}
	}
	return calls
}

// gitInvocation is git at the start of a shell command, not an argument (echo git commit).
var gitInvocation = regexp.MustCompile(`(?:^|[;&|(\n])\s*git\s`)

// dirBefore is where a command stands after the "cd" steps of its prefix. A subshell's "cd" holds
// only inside its parentheses.
func dirBefore(cwd, prefix string) string {
	dir := cwd
	var saved []string
	step := func(text string) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(text), "cd "); ok {
			dir = moveDir(dir, strings.TrimSpace(rest))
		}
	}
	start := 0
	for i, r := range prefix {
		switch r {
		case '&', ';', '|', '\n', '(', ')':
			step(prefix[start:i])
			start = i + 1
			switch r {
			case '(':
				saved = append(saved, dir)
			case ')':
				if n := len(saved); n > 0 {
					dir, saved = saved[n-1], saved[:n-1]
				}
			}
		}
	}
	step(prefix[start:])
	return dir
}

func moveDir(base, to string) string {
	to = strings.Trim(to, `"'`)
	switch {
	case to == "" || to == "-":
		return base
	case strings.HasPrefix(to, "~/"):
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, to[2:])
		}
		return base
	case filepath.IsAbs(to):
		return filepath.Clean(to)
	}
	return filepath.Join(base, to)
}

// commitSubject is git's subject of the message one "git commit" passes: its first paragraph with
// the lines joined by spaces. It reads -m/--message (also a "-am" cluster) as the shell would, a
// "$(cat <<'EOF' ... EOF)" message, and "-F -" fed by a heredoc. It is "" when the message is not a
// literal the command shows.
func commitSubject(args string) string {
	spans := wordSpan.FindAllStringIndex(args, -1)
	fields := make([]string, len(spans))
	for i, sp := range spans {
		fields[i] = args[sp[0]:sp[1]]
	}
	for i, f := range fields {
		switch {
		case (f == "-F" || f == "--file") && i+1 < len(fields) && fields[i+1] == "-", f == "--file=-":
			return subjectOf(heredocBody(args))
		case f == "-m" || f == "--message" || strings.HasPrefix(f, "--message=") ||
			(len(f) > 2 && f[0] == '-' && f[1] != '-' && strings.HasSuffix(f, "m")):
			at := spans[i][1]
			rest := strings.TrimLeft(args[at:], " =")
			if strings.HasPrefix(f, "--message=") {
				rest = strings.TrimPrefix(f, "--message=") + args[at:]
			}
			if strings.HasPrefix(rest, `"$(cat <<`) {
				return subjectOf(heredocBody(rest))
			}
			word, ok := shellWord(rest)
			if !ok {
				return ""
			}
			return subjectOf(word)
		}
	}
	return ""
}

func subjectOf(msg string) string {
	paragraph, _, _ := strings.Cut(strings.TrimSpace(msg), "\n\n")
	return strings.Join(strings.Fields(paragraph), " ")
}

var wordSpan = regexp.MustCompile(`\S+`)

var heredocStart = regexp.MustCompile(`<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)

// heredocBody is the text of the first heredoc in a command, up to its terminator line.
func heredocBody(command string) string {
	m := heredocStart.FindStringSubmatchIndex(command)
	if m == nil {
		return ""
	}
	tag := command[m[2]:m[3]]
	_, body, ok := strings.Cut(command[m[1]:], "\n")
	if !ok {
		return ""
	}
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == tag {
			return strings.Join(lines, "\n")
		}
		lines = append(lines, line)
	}
	return ""
}

// claimCommit picks the one commit a "git commit" made: the only unclaimed commit with its subject
// made during the call. An unknown subject or a tie leaves the call without a revision, since a
// parallel call or another session may have made the other commit. Only an amend breaks a tie: the
// commit it replaced stays in the reflog with the same subject but is no longer reachable.
func (s *Session) claimCommit(found []Commit, call commitCall) string {
	// A standalone amend without a message is identified by being the call's only new commit.
	if call.subject == "" && !call.amend {
		return ""
	}
	var fit, reachable []string
	for _, c := range found {
		if s.seen.commits[c.SHA] || (call.subject != "" && strings.Join(strings.Fields(c.Subject), " ") != call.subject) {
			continue
		}
		fit = append(fit, c.SHA)
		if c.Reachable {
			reachable = append(reachable, c.SHA)
		}
	}
	switch {
	case len(fit) == 1 && (call.subject != "" || len(reachable) == 1):
	case call.amend && call.subject != "" && len(fit) == 2 && len(reachable) == 1:
		fit = reachable
	default:
		return ""
	}
	s.seen.commits[fit[0]] = true
	return fit[0]
}

// maskHeredocs blanks heredoc bodies, keeping every offset, so only shell text is searched.
func maskHeredocs(command string) string {
	b := []byte(command)
	from := 0
	for {
		m := heredocStart.FindStringSubmatchIndex(command[from:])
		if m == nil {
			return string(b)
		}
		tag := command[from+m[2] : from+m[3]]
		nl := strings.IndexByte(command[from+m[1]:], '\n')
		if nl < 0 {
			return string(b)
		}
		start := from + m[1] + nl + 1
		end := len(command)
		for pos := start; pos < len(command); {
			lineEnd := strings.IndexByte(command[pos:], '\n')
			if lineEnd < 0 {
				lineEnd = len(command) - pos
			}
			if strings.TrimSpace(command[pos:pos+lineEnd]) == tag {
				end = pos
				break
			}
			pos += lineEnd + 1
		}
		for k := start; k < end; k++ {
			if b[k] != '\n' {
				b[k] = ' '
			}
		}
		from = end
	}
}

// Commit is one commit of the signed-in user.
type Commit struct {
	SHA     string
	Subject string
	// Reachable is false for a commit only the reflog still holds (amended, rebased, branch deleted).
	Reachable bool
}

// shellWord reads one shell word with its quotes removed; ok is false when the word holds an
// expansion whose value only the shell knows.
func shellWord(s string) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '&' || c == '|' || c == '(' || c == ')':
			return b.String(), b.Len() > 0
		case c == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				return "", false
			}
			b.WriteString(s[i+1 : i+1+end])
			i += end + 1
		case c == '"':
			for i++; i < len(s) && s[i] != '"'; i++ {
				switch {
				case s[i] == '$' || s[i] == '`':
					return "", false
				case s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`\n", s[i+1]) >= 0:
					i++
				}
				b.WriteByte(s[i])
			}
			if i >= len(s) {
				return "", false
			}
		case c == '$' || c == '`':
			return "", false
		case c == '\\' && i+1 < len(s):
			i++
			b.WriteByte(s[i])
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), b.Len() > 0
}
