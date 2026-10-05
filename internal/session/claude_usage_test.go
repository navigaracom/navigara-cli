package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadClaudeUsageFixture(t *testing.T) {
	got, err := Read("claude_code", strings.NewReader(fixture(t, "claude-usage")))
	if err != nil || got.Session.SessionID != "id_001" || got.Session.SourceVersion != "2.1.287" || len(got.Session.UsageObservations) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	for i, want := range []struct{ event, at string }{{"id_002", "2026-11-08T16:10:04.225Z"}, {"id_006", "2026-11-08T16:10:05.707Z"}} {
		u := got.Session.UsageObservations[i]
		if u.RecordLine != i+2 || u.EventID != want.event || u.MessageID != "id_005" || u.RequestID != "id_004" || u.Model != "claude-opus-5-5" || u.ObservedAt == nil || u.ObservedAt.Format(time.RFC3339Nano) != want.at || u.ObservedAt.Location() != time.UTC || u.TurnID != "" || u.TotalTokenUsage != nil || u.LastTokenUsage != nil {
			t.Fatalf("unexpected observation: %+v", u)
		}
		assertUsageJSON(t, u.MessageUsage, `{"input_tokens":2,"output_tokens":348,"cache_read_input_tokens":27495,"cache_creation_input_tokens":42816,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":42816}}`)
	}
}

// Growth here is a synthetic root mutation; the observed root fixture repeats identical counters.
func TestReadClaudeUsageSyntheticGrowthAndUTC(t *testing.T) {
	rows := strings.Split(strings.TrimSpace(fixture(t, "claude-usage")), "\n")
	rows[1] = strings.Replace(rows[1], "2026-11-08T16:10:04.225Z", "2026-11-08T18:10:04.225+02:00", 1)
	rows[2] = strings.Replace(rows[2], `"output_tokens":348`, `"output_tokens":400`, 1)
	got, err := Read("claude_code", strings.NewReader(rows[0]+"\n\n"+rows[1]+"\n"+rows[2]))
	if err != nil || len(got.Session.UsageObservations) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	for i, output := range []int64{348, 400} {
		u := got.Session.UsageObservations[i]
		if u.RecordLine != i+3 || u.MessageUsage.OutputTokens == nil || *u.MessageUsage.OutputTokens != output || u.MessageID != "id_005" || u.RequestID != "id_004" || u.ObservedAt.Location() != time.UTC {
			t.Fatalf("observation %d: %+v", i, u)
		}
	}
	if got.Session.UsageObservations[0].ObservedAt.Format(time.RFC3339Nano) != "2026-11-08T16:10:04.225Z" {
		t.Fatal("observation did not use UTC record timestamp")
	}
}

func TestReadClaudeUsageSyntheticAbsenceOpaqueAndContent(t *testing.T) {
	input := fixture(t, "claude-root")
	for _, row := range []string{
		`{"type":"assistant","message":{"usage":false},"timestamp":"private-invalid"}`,
		`{"isSidechain":true,"sessionId":42,"message":{"usage":false},"timestamp":"private-invalid"}`,
		`{"sessionId":"claude_code-root","type":"user","message":{"usage":false},"uuid":42}`,
		`{"sessionId":"claude_code-root","type":42,"message":{"usage":false}}`,
		`{"sessionId":"claude_code-root","type":"assistant","message":42,"uuid":42}`,
		`{"sessionId":"claude_code-root","type":"assistant","message":{"id":42,"model":false,"content":"private-content"},"uuid":42}`,
		`{"sessionId":"claude_code-root","type":"assistant","message":{"usage":null,"id":42},"requestId":false}`,
	} {
		input += row + "\n"
	}
	input += `{"sessionId":"claude_code-root","type":"assistant","cwd":"private-path","tools":["private-tool"],"message":{"content":"private-content","usage":{}}}` + "\n" +
		`{"sessionId":"claude_code-root","type":"assistant","message":{"usage":{"input_tokens":null,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":null,"ephemeral_1h_input_tokens":0}}}}`
	got, err := Read("claude_code", strings.NewReader(input))
	if err != nil || len(got.Session.UsageObservations) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	assertUsageJSON(t, got.Session.UsageObservations[0].MessageUsage, `{}`)
	assertUsageJSON(t, got.Session.UsageObservations[1].MessageUsage, `{"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":0}}`)
	for _, u := range got.Session.UsageObservations {
		if u.ObservedAt != nil || u.EventID != "" || u.MessageID != "" || u.RequestID != "" || u.Model != "" {
			t.Fatalf("invented metadata: %+v", u)
		}
	}
	b, err := json.Marshal(got.Session)
	if err != nil || strings.Contains(string(b), "private") || strings.Contains(string(b), "content") || strings.Contains(string(b), "tools") || strings.Contains(string(b), "cwd") {
		t.Fatalf("content leaked: %s, %v", b, err)
	}
}

func TestReadClaudeUsageSyntheticInvalidCountersDiscardFile(t *testing.T) {
	for _, field := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "ephemeral_5m_input_tokens", "ephemeral_1h_input_tokens"} {
		for _, value := range []string{`-1`, `1.5`, `"private"`, `true`, `{}`, `[]`, `9223372036854775808`} {
			t.Run(field+"/"+value, func(t *testing.T) {
				usage := `{"` + field + `":` + value + `}`
				if strings.HasPrefix(field, "ephemeral_") {
					usage = `{"cache_creation":` + usage + `}`
				}
				input := fixture(t, "claude-usage") + "\n" + `{"sessionId":"id_001","type":"assistant","message":{"usage":` + usage + `}}`
				got, err := Read("claude_code", strings.NewReader(input))
				if err == nil || err.Error() != "line 5: invalid Claude usage" || !reflect.DeepEqual(got, Record{}) {
					t.Fatalf("got %+v, %v", got, err)
				}
			})
		}
	}
	for _, usage := range []string{`42`, `false`, `[]`, `"private"`, `{"cache_creation":42}`, `{"cache_creation":[]}`} {
		got, err := Read("claude_code", strings.NewReader(fixture(t, "claude-usage")+`{"sessionId":"id_001","type":"assistant","message":{"usage":`+usage+`}}`))
		if err == nil || err.Error() != "line 4: invalid Claude usage" || !reflect.DeepEqual(got, Record{}) {
			t.Fatalf("got %+v, %v", got, err)
		}
	}
}

func TestReadClaudeUsageSyntheticInt64Maximum(t *testing.T) {
	input := fixture(t, "claude-root") + `{"sessionId":"claude_code-root","type":"assistant","message":{"usage":{"input_tokens":9223372036854775807,"cache_creation":{"ephemeral_1h_input_tokens":9223372036854775807}}}}`
	got, err := Read("claude_code", strings.NewReader(input))
	if err != nil || len(got.Session.UsageObservations) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	u := got.Session.UsageObservations[0].MessageUsage
	if u.InputTokens == nil || *u.InputTokens != 9223372036854775807 || u.CacheCreation == nil || u.CacheCreation.Ephemeral1hInputTokens == nil || *u.CacheCreation.Ephemeral1hInputTokens != 9223372036854775807 {
		t.Fatalf("int64 maximum lost: %+v", u)
	}
}
