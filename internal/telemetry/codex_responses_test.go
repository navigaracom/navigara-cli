package telemetry

import (
	"fmt"
	"testing"
	"time"
)

func TestBuildCodexRecordedResponses(t *testing.T) {
	session, err := BuildCodex("testdata/codex-recorded-responses.jsonl", Identity{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	responses := events(session, "codex.sse_event")
	if len(responses) != 2 {
		t.Fatalf("recorded responses = %d, want 2", len(responses))
	}
	for index, expected := range []map[string]any{
		{"input_token_count": "27785", "cached_token_count": "13056", "cache_write_token_count": "0",
			"output_token_count": "218", "reasoning_token_count": "21", "tool_token_count": "28003"},
		{"input_token_count": "29485", "cached_token_count": "27392", "cache_write_token_count": "0",
			"output_token_count": "325", "reasoning_token_count": "35", "tool_token_count": "29810"},
	} {
		expected["event.kind"] = "response.completed"
		expected["response_id"] = fmt.Sprintf("response-fixture-%d", index+1)
		expected["turn_id"] = "turn-fixture-2"
		expected["model"] = "model-fixture"
		expected["model_reasoning_effort"] = "effort-fixture"
		for key, value := range expected {
			if responses[index].Attrs[key] != value {
				t.Errorf("response %d %s = %v, want %v", index, key, responses[index].Attrs[key], value)
			}
		}
	}
	if len(session.Points) != 0 {
		t.Fatal("response logs must not add histogram usage")
	}
	bounded, err := BuildCodex("testdata/codex-recorded-responses.jsonl", Identity{}, responses[0].Time)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(events(bounded, "codex.sse_event")); got != 1 {
		t.Fatalf("responses through first completion = %d, want 1", got)
	}
	late, err := BuildCodex("testdata/codex-recorded-response-late-context.jsonl", Identity{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	completed := events(late, "codex.sse_event")
	if len(completed) != 1 || completed[0].Attrs["model"] != "gpt-5.6-terra" || completed[0].Attrs["model_reasoning_effort"] != "medium" {
		t.Fatal("response must use its recorded turn context even when that row follows it")
	}
}
