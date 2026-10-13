package telemetry

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func envelope(path string, resources ...string) Body {
	key := "resourceLogs"
	if path == "/v1/metrics" {
		key = "resourceMetrics"
	}
	return Body{Path: path, Data: []byte(`{"` + key + `":[` + strings.Join(resources, ",") + `]}`)}
}

func resourceNames(t *testing.T, body Body) []string {
	t.Helper()
	key := "resourceLogs"
	if body.Path == "/v1/metrics" {
		key = "resourceMetrics"
	}
	var top map[string][]json.RawMessage
	if err := json.Unmarshal(body.Data, &top); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, raw := range top[key] {
		var resource struct {
			Resource struct {
				Attributes []struct {
					Key   string `json:"key"`
					Value struct {
						String string `json:"stringValue"`
					} `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
		}
		if err := json.Unmarshal(raw, &resource); err != nil {
			t.Fatal(err)
		}
		for _, attr := range resource.Resource.Attributes {
			if attr.Key == "name" {
				names = append(names, attr.Value.String)
			}
		}
	}
	return names
}

func TestBatcherCombinesResourcesAndFlushesLogsBeforeMetrics(t *testing.T) {
	var got []Body
	b := NewBatcher(func(body Body) error {
		got = append(got, body)
		return nil
	})
	if err := b.Add(envelope("/v1/metrics", `{"resource":{"attributes":[{"key":"name","value":{"stringValue":"metric-1"}}]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(envelope("/v1/logs", `{"resource":{"attributes":[{"key":"name","value":{"stringValue":"log-1"}}]}}`, `{"resource":{"attributes":[{"key":"name","value":{"stringValue":"log-2"}}]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(envelope("/v1/metrics", `{"resource":{"attributes":[{"key":"name","value":{"stringValue":"metric-2"}}]}}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Path != "/v1/logs" || got[1].Path != "/v1/metrics" {
		t.Fatalf("batches = %#v; want logs then metrics", got)
	}
	if names := resourceNames(t, got[0]); strings.Join(names, ",") != "log-1,log-2" {
		t.Fatalf("log resources = %v", names)
	}
	if names := resourceNames(t, got[1]); strings.Join(names, ",") != "metric-1,metric-2" {
		t.Fatalf("metric resources = %v", names)
	}
}

func TestBatcherPreservesNestedResourcePayloads(t *testing.T) {
	logs := `{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"claude"}}]},"scopeLogs":[{"logRecords":[{"body":{"stringValue":"prompt"},"attributes":[{"key":"event.name","value":{"stringValue":"user_prompt"}}]}]}]}`
	metrics := `{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"codex"}}]},"scopeMetrics":[{"metrics":[{"name":"codex.cost.usage","sum":{"dataPoints":[{"asDouble":1.25}]}}]}]}`
	var got []Body
	b := NewBatcher(func(body Body) error { got = append(got, body); return nil })
	if err := b.Add(envelope("/v1/logs", logs)); err != nil {
		t.Fatal(err)
	}
	if err := b.Add(envelope("/v1/metrics", metrics)); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("batches = %d; want 2", len(got))
	}
	var logTop, metricTop map[string][]json.RawMessage
	if err := json.Unmarshal(got[0].Data, &logTop); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got[1].Data, &metricTop); err != nil {
		t.Fatal(err)
	}
	if string(logTop["resourceLogs"][0]) != logs || string(metricTop["resourceMetrics"][0]) != metrics {
		t.Fatal("nested resource payload changed while batching")
	}
}

func TestBatcherPreservesEncodedCodexSessions(t *testing.T) {
	var encoded, got []Body
	for i, id := range []string{"session-a", "session-b"} {
		path := t.TempDir() + "/rollout.jsonl"
		at := "2026-10-03T10:0" + string(rune('0'+i)) + ":00Z"
		writeLines(t, path,
			map[string]any{"timestamp": at, "type": "session_meta", "payload": map[string]any{"id": id}},
			map[string]any{"timestamp": at, "type": "turn_context", "payload": map[string]any{"model": "gpt-5.6-terra"}},
			codexEvent(at, "item_completed", map[string]any{"item": map[string]any{"type": "UserMessage", "content": []any{map[string]any{"type": "text", "text": id}}}}),
			tokenCount(at, 100+i, 20, 30, 130+i),
		)
		s, err := BuildCodex(path, Identity{CodexEmail: "dev@example.com"}, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if err := Encode(s, func(body Body) error {
			encoded = append(encoded, body)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}

	b := NewBatcher(func(body Body) error {
		got = append(got, body)
		return nil
	})
	for _, body := range encoded {
		if err := b.Add(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/logs", "/v1/metrics"} {
		var want []json.RawMessage
		for _, body := range encoded {
			if body.Path == path {
				want = append(want, rawResources(body)...)
			}
		}
		var batches []Body
		for _, body := range got {
			if body.Path == path {
				batches = append(batches, body)
			}
		}
		if len(batches) != 1 {
			t.Fatalf("%s batches = %d; want one", path, len(batches))
		}
		actual := rawResources(batches[0])
		if len(actual) != len(want) {
			t.Fatalf("%s resources = %d; want %d", path, len(actual), len(want))
		}
		for i := range want {
			if string(actual[i]) != string(want[i]) {
				t.Fatalf("%s resource %d changed", path, i)
			}
		}
	}
}

func rawResources(body Body) []json.RawMessage {
	key := "resourceLogs"
	if body.Path == "/v1/metrics" {
		key = "resourceMetrics"
	}
	var top map[string][]json.RawMessage
	if err := json.Unmarshal(body.Data, &top); err != nil {
		return nil
	}
	return top[key]
}

func TestBatcherSplitsAtExactLimitAndRejectsOversize(t *testing.T) {
	var got []Body
	b := NewBatcher(func(body Body) error {
		got = append(got, body)
		return nil
	})
	resource := `{"resource":{"attributes":[{"key":"name","value":{"stringValue":"` + strings.Repeat("x", MaxBodyBytes) + `"}}]}}`
	if err := b.Add(envelope("/v1/logs", resource)); err == nil {
		t.Fatal("oversize resource accepted")
	}
	if len(got) != 0 {
		t.Fatal("oversize resource emitted")
	}

	// A body exactly at the limit is accepted and passed through when its resource fits.
	base := envelope("/v1/logs", `{"resource":{"attributes":[{"key":"name","value":{"stringValue":""}}]}}`)
	exact := envelope("/v1/logs", `{"resource":{"attributes":[{"key":"name","value":{"stringValue":"`+strings.Repeat("y", MaxBodyBytes-len(base.Data))+`"}}]}}`)
	if len(exact.Data) != MaxBodyBytes {
		t.Fatalf("exact body size = %d", len(exact.Data))
	}
	if err := b.Add(exact); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("exact body emitted before flush: batches=%d", len(got))
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Data) != MaxBodyBytes {
		t.Fatalf("exact body was not emitted at limit: batches=%d", len(got))
	}

	// Several large resources force a bounded split, and every emitted body remains valid.
	got = nil
	b = NewBatcher(func(body Body) error { got = append(got, body); return nil })
	large := `{"resource":{"attributes":[{"key":"name","value":{"stringValue":"` + strings.Repeat("z", 900000) + `"}}]}}`
	for i := 0; i < 6; i++ {
		if err := b.Add(envelope("/v1/logs", large)); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Fatalf("batches = %d; want a split", len(got))
	}
	for _, body := range got {
		if len(body.Data) > MaxBodyBytes {
			t.Fatalf("body size %d exceeds limit", len(body.Data))
		}
	}
}

func TestBatcherStopsAfterEmitFailure(t *testing.T) {
	want := errors.New("receiver stopped")
	calls := 0
	b := NewBatcher(func(Body) error {
		calls++
		return want
	})
	if err := b.Add(envelope("/v1/logs", `{"resource":{}}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); !errors.Is(err, want) {
		t.Fatalf("flush error = %v; want %v", err, want)
	}
	if err := b.Add(envelope("/v1/logs", `{"resource":{}}`)); !errors.Is(err, want) {
		t.Fatalf("add after failure = %v; want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("callback calls = %d; want 1", calls)
	}
}
