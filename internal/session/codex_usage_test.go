package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadCodexUsageFixture(t *testing.T) {
	got, err := Read("codex", strings.NewReader(fixture(t, "codex-usage")))
	if err != nil {
		t.Fatal(err)
	}
	if got.Session.SessionID != "id_007" || got.Session.SourceVersion != "0.156.1" || len(got.Session.UsageObservations) != 2 {
		t.Fatalf("unexpected fixture metadata: %+v", got.Session)
	}
	for i, want := range []struct {
		at          string
		total, last string
	}{
		{"2026-11-08T09:10:29.108Z", `{"input_tokens":29151,"output_tokens":317,"cached_input_tokens":0,"cache_write_input_tokens":0,"reasoning_output_tokens":0,"total_tokens":29468}`, `{"input_tokens":29151,"output_tokens":317,"cached_input_tokens":0,"cache_write_input_tokens":0,"reasoning_output_tokens":0,"total_tokens":29468}`},
		{"2026-11-08T09:10:42.921Z", `{"input_tokens":67709,"output_tokens":606,"cached_input_tokens":28928,"cache_write_input_tokens":0,"reasoning_output_tokens":7,"total_tokens":68315}`, `{"input_tokens":38558,"output_tokens":289,"cached_input_tokens":28928,"cache_write_input_tokens":0,"reasoning_output_tokens":7,"total_tokens":38847}`},
	} {
		u := got.Session.UsageObservations[i]
		if u.RecordLine != i+3 || u.Model != "gpt-6-astra" || u.TurnID != "id_009" || u.ObservedAt == nil || u.ObservedAt.Format(time.RFC3339Nano) != want.at || u.ObservedAt.Location() != time.UTC || u.EventID != "" || u.MessageID != "" || u.RequestID != "" || u.MessageUsage != nil {
			t.Fatalf("unexpected observation: %+v", u)
		}
		assertUsageJSON(t, u.TotalTokenUsage, want.total)
		assertUsageJSON(t, u.LastTokenUsage, want.last)
	}
}

func assertUsageJSON(t *testing.T, value any, want string) {
	t.Helper()
	b, err := json.Marshal(value)
	var actual, expected any
	if err != nil || json.Unmarshal(b, &actual) != nil || json.Unmarshal([]byte(want), &expected) != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("got %s (%v); want %s", b, err, want)
	}
}

// Synthetic rows exercise context transitions and repeated snapshots, not observed producer behavior.
func TestReadCodexUsageSyntheticContext(t *testing.T) {
	usage := `{"type":"event_msg","timestamp":"2026-01-02T02:00:00+02:00","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":9},"last_token_usage":{"input_tokens":2}}}}` + "\n"
	input := fixture(t, "codex-root") + "\n" + usage +
		`{"type":"turn_context","payload":{"model":"first","turn_id":"turn-1"}}` + "\n" + usage + usage +
		`{"type":"turn_context","payload":{"model":"second","turn_id":"turn-2"}}` + "\n" + usage +
		`{"type":"turn_context","payload":{"model":null}}` + "\n" + usage +
		`{"type":"turn_context","payload":{"turn_id":"turn-3"}}` + "\n" + usage +
		`{"type":"turn_context","payload":{}}` + "\n" + usage
	got, err := Read("codex", strings.NewReader(input))
	if err != nil || len(got.Session.UsageObservations) != 7 {
		t.Fatalf("got %+v, %v", got, err)
	}
	for i, want := range []struct {
		line        int
		model, turn string
	}{{4, "", ""}, {6, "first", "turn-1"}, {7, "first", "turn-1"}, {9, "second", "turn-2"}, {11, "", ""}, {13, "", "turn-3"}, {15, "", ""}} {
		u := got.Session.UsageObservations[i]
		if u.RecordLine != want.line || u.Model != want.model || u.TurnID != want.turn || u.ObservedAt == nil || u.ObservedAt.Format(time.RFC3339) != "2026-01-02T00:00:00Z" || u.ObservedAt.Location() != time.UTC {
			t.Fatalf("observation %d: %+v", i, u)
		}
		assertUsageJSON(t, u.TotalTokenUsage, `{"input_tokens":9}`)
		assertUsageJSON(t, u.LastTokenUsage, `{"input_tokens":2}`)
	}
}

func TestReadCodexUsageSyntheticAbsenceAndOpaqueRows(t *testing.T) {
	input := fixture(t, "codex-root") + `{"type":"response_item","payload":{"type":"token_count","info":false}}` + "\n"
	for _, payload := range []string{`42`, `[]`, `{"type":42,"info":false}`, `{"type":"other","info":false}`, `{"type":"token_count"}`, `{"type":"token_count","info":null}`, `{"type":"token_count","info":{"total_token_usage":null}}`} {
		input += `{"type":"event_msg","payload":` + payload + "}\n"
	}
	input += `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{},"last_token_usage":{"input_tokens":null,"output_tokens":0,"cache_write_input_tokens":0}}}}`
	got, err := Read("codex", strings.NewReader(input))
	if err != nil || len(got.Session.UsageObservations) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	u := got.Session.UsageObservations[0]
	if u.ObservedAt != nil || u.Model != "" || u.TurnID != "" {
		t.Fatalf("invented context: %+v", u)
	}
	assertUsageJSON(t, u.TotalTokenUsage, `{}`)
	assertUsageJSON(t, u.LastTokenUsage, `{"output_tokens":0,"cache_write_input_tokens":0}`)
}

func TestReadCodexUsageSyntheticInvalidCountersDiscardFile(t *testing.T) {
	for _, field := range []string{"input_tokens", "output_tokens", "cached_input_tokens", "cache_write_input_tokens", "reasoning_output_tokens", "total_tokens"} {
		for _, value := range []string{`-1`, `1.5`, `"private"`, `true`, `{}`, `[]`, `9223372036854775808`} {
			for _, snapshot := range []string{"total_token_usage", "last_token_usage"} {
				t.Run(field+"/"+value+"/"+snapshot, func(t *testing.T) {
					input := fixture(t, "codex-usage") + "\n" + `{"type":"event_msg","payload":{"type":"token_count","info":{"` + snapshot + `":{"` + field + `":` + value + `}}}}`
					got, err := Read("codex", strings.NewReader(input))
					if err == nil || err.Error() != "line 6: invalid Codex usage" || !reflect.DeepEqual(got, Record{}) {
						t.Fatalf("got %+v, %v", got, err)
					}
				})
			}
		}
	}
	for _, info := range []string{`42`, `[]`, `"private"`, `{"total_token_usage":42}`, `{"last_token_usage":[]}`} {
		got, err := Read("codex", strings.NewReader(fixture(t, "codex-root")+`{"type":"event_msg","payload":{"type":"token_count","info":`+info+`}}`))
		if err == nil || err.Error() != "line 3: invalid Codex usage" || !reflect.DeepEqual(got, Record{}) {
			t.Fatalf("got %+v, %v", got, err)
		}
	}
	got, err := Read("codex", strings.NewReader(fixture(t, "codex-child")+`{"type":"event_msg","payload":{"type":"token_count","info":false}}`))
	if err != nil || !reflect.DeepEqual(got, Record{}) {
		t.Fatalf("child usage was parsed: %+v, %v", got, err)
	}
}

func TestReadCodexUsageSyntheticInt64Maximum(t *testing.T) {
	input := fixture(t, "codex-root") + `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":9223372036854775807}}}}`
	got, err := Read("codex", strings.NewReader(input))
	if err != nil || len(got.Session.UsageObservations) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	count := got.Session.UsageObservations[0].LastTokenUsage.TotalTokens
	if count == nil || *count != 9223372036854775807 {
		t.Fatalf("int64 maximum lost: %v", count)
	}
}
