package telemetry

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func copilotUsage(at, callID, model string, input, output, cacheRead, cacheWrite int, duration float64) map[string]any {
	return map[string]any{"type": "session.usage_record", "timestamp": at, "data": map[string]any{"usage": map[string]any{
		"model": model, "apiCallId": callID, "initiator": "user", "inputTokens": input, "outputTokens": output,
		"cacheReadTokens": cacheRead, "cacheWriteTokens": cacheWrite, "duration": duration, "cost": 0.33}}}
}

func TestBuildCopilot(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "session-state", "s-1", "events.jsonl")
	writeLines(t, path,
		map[string]any{"type": "session.start", "timestamp": "2026-11-02T12:48:14Z", "data": map[string]any{"copilotVersion": "1.0.105"}},
		map[string]any{"type": "user.message", "timestamp": "2026-11-02T12:48:16Z", "data": map[string]any{
			"content": "secret prompt", "interactionId": "i-1", "turnId": "0"}},
		copilotUsage("2026-11-02T12:48:18Z", "msg_2", "gpt-6", 500, 20, 400, 0, 500),
		copilotUsage("2026-11-02T12:48:17Z", "msg_1", "claude-haiku-4.5", 18010, 42, 0, 18000, 1161),
		copilotUsage("2026-11-02T12:48:19Z", "", "gpt-6", 1, 1, 0, 0, 1),
		map[string]any{"type": "session.usage_record", "timestamp": "not a time"},
		// Totals at shutdown are not counted missing when per-call records exist.
		map[string]any{"type": "session.shutdown", "timestamp": "2026-11-02T12:49:00Z", "data": map[string]any{
			"modelMetrics": map[string]any{"gpt-6": map[string]any{"requests": map[string]any{"count": 3}}}}},
	)
	writeLines(t, filepath.Join(home, "session-state", "old", "events.jsonl"),
		map[string]any{"type": "user.message", "timestamp": "2026-06-01T10:00:00Z", "data": map[string]any{"interactionId": "i-old"}},
		map[string]any{"type": "session.shutdown", "timestamp": "2026-06-01T10:01:00Z", "data": map[string]any{
			"modelMetrics": map[string]any{"claude-haiku-4.5": map[string]any{"requests": map[string]any{"count": 2}}}}},
	)
	writeLines(t, filepath.Join(home, "session-state", "no-history", "workspace.yaml"))

	paths, err := CopilotSessions(home)
	if err != nil || len(paths) != 2 || CopilotSessionID(paths[1]) != "s-1" {
		t.Fatalf("sessions = %v, %v; want old and s-1", paths, err)
	}
	s, err := BuildCopilot(path, Identity{CopilotEmail: "dev@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Spans) != 2 || len(s.Events) != 1 || s.Missing["usage records without a call id"] != 1 || s.Missing["unreadable records"] != 1 || len(s.Missing) != 2 {
		t.Fatalf("spans %d, events %d, missing %v", len(s.Spans), len(s.Events), s.Missing)
	}
	call := s.Spans[0]
	if call.Name != "chat claude-haiku-4.5" || call.Attrs["gen_ai.response.id"] != "msg_1" ||
		!call.End.Equal(time.Date(2026, 11, 2, 12, 48, 17, 0, time.UTC)) || call.End.Sub(call.Start) != 1161*time.Millisecond {
		t.Fatalf("first span = %+v; want msg_1 sorted first, ending at its record", call)
	}
	if _, ok := call.Attrs["gen_ai.usage.cache_read.input_tokens"]; ok || call.Attrs["gen_ai.usage.cache_write.input_tokens"] != int64(18000) {
		t.Fatalf("cache attributes = %v; zero is left out", call.Attrs)
	}
	if s.Spans[1].TraceID != call.TraceID || s.Spans[1].SpanID == call.SpanID || len(call.TraceID) != 32 || len(call.SpanID) != 16 {
		t.Fatalf("ids = %s/%s and %s/%s; one trace per session, one span per call", call.TraceID, call.SpanID, s.Spans[1].TraceID, s.Spans[1].SpanID)
	}
	prompt := s.Events[0]
	if prompt.Name != "github.copilot.user.message" || prompt.Attrs["github.copilot.user.message.interaction_id"] != "i-1" || len(prompt.Attrs) != 3 {
		t.Fatalf("prompt = %+v; want its interaction id and no text", prompt)
	}

	old, err := BuildCopilot(paths[0], Identity{})
	if err != nil || len(old.Spans) != 0 || len(old.Events) != 1 || old.Missing[MissingCopilotTotals] != 2 {
		t.Fatalf("old session = %+v, %v; totals are missing, the prompt is kept", old, err)
	}

	var bodies []Body
	if err := Encode(s, func(b Body) error { bodies = append(bodies, b); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0].Path != "/v1/logs" || bodies[1].Path != "/v1/traces" {
		t.Fatalf("bodies = %v; want logs then traces", bodies)
	}
	var traces struct {
		ResourceSpans []struct {
			Resource struct {
				Attributes []keyValue `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					Name       string     `json:"name"`
					Kind       int        `json:"kind"`
					Attributes []keyValue `json:"attributes"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(bodies[1].Data, &traces); err != nil {
		t.Fatal(err)
	}
	rs := traces.ResourceSpans[0]
	span := rs.ScopeSpans[0].Spans[0]
	attrs := map[string]string{}
	for _, kv := range span.Attributes {
		if kv.Value.String != nil {
			attrs[kv.Key] = *kv.Value.String
		}
	}
	if *rs.Resource.Attributes[1].Value.String != CopilotService || span.Kind != spanKindClient ||
		attrs["enduser.id"] != "dev@example.com" || attrs["gen_ai.conversation.id"] != "s-1" {
		t.Fatalf("resource %v, span %+v; want the live Copilot shape", rs.Resource.Attributes, span)
	}
	var logs map[string]any
	if err := json.Unmarshal(bodies[0].Data, &logs); err != nil {
		t.Fatal(err)
	}
	record := logs["resourceLogs"].([]any)[0].(map[string]any)["scopeLogs"].([]any)[0].(map[string]any)["logRecords"].([]any)[0].(map[string]any)
	if record["eventName"] != "github.copilot.user.message" {
		t.Fatalf("event name = %v; Copilot names carry no claude_code. prefix", record["eventName"])
	}

	b := NewBatcher(func(Body) error { return nil })
	if err := b.Add(bodies[1]); err != nil {
		t.Fatalf("batcher rejects traces: %v", err)
	}
}
