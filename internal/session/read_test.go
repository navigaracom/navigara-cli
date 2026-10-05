package session

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReadFixtures(t *testing.T) {
	for _, tc := range []struct{ source, file, id, version, last string }{
		{"claude_code", "claude-root", "claude_code-root", "2.1.285", "2026-01-02T00:02:00Z"},
		{"codex", "codex-root", "codex-root", "0.139.0", "2026-01-02T00:02:00Z"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			got, err := Read(tc.source, strings.NewReader(fixture(t, tc.file)))
			first, _ := time.Parse(time.RFC3339, "2026-01-02T00:00:00Z")
			last, _ := time.Parse(time.RFC3339, tc.last)
			want := Record{Session: Session{Source: tc.source, SessionID: tc.id, SourceVersion: tc.version, FirstObservedAt: &first, LastObservedAt: &last}}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v, %v; want %+v", got, err, want)
			}
		})
	}
}

func TestReadMutations(t *testing.T) {
	claude, codex := fixture(t, "claude-root"), fixture(t, "codex-root")
	child := fixture(t, "claude-child")
	for _, tc := range []struct{ name, source, input, id, diagnostic string }{
		{"claude child", "claude_code", child, "", ""},
		{"claude child malformed tail", "claude_code", child + "{private", "", "line 2"},
		{"claude first optional values", "claude_code", claude + `{"sessionId":"claude_code-root","version":"later","cwd":"later"}`, "claude_code-root", ""},
		{"codex first optional values", "codex", codex + strings.ReplaceAll(strings.ReplaceAll(codex, "0.139.0", "later"), "/workspace/example", "later"), "codex-root", ""},
		{"codex child before identity equality", "codex", fixture(t, "codex-child") + "broken tail", "", ""},
		{"null subagent", "codex", strings.Replace(codex, `"source":"exec"`, `"source":{"subagent":null}`, 1), "", ""},
		{"parent marker", "codex", strings.Replace(codex, `"source":"exec"`, `"parent_thread_id":"parent"`, 1), "", ""},
		{"sidechain metadata types ignored", "claude_code", strings.Replace(child, `"sessionId":"claude_code-child"`, `"sessionId":42`, 1) + claude, "claude_code-root", ""},
		{"mixed sidechain", "claude_code", strings.Replace(child, "2026-01-02T00:00:00Z", "private-invalid", 1) + claude, "claude_code-root", ""},
		{"bookkeeping", "claude_code", `{"timestamp":"private-invalid","version":"wrong","cwd":"wrong"}` + "\n" + claude, "claude_code-root", ""},
		{"opaque event", "codex", codex + `{"type":"response_item","payload":{"id":42,"session_id":false,"cwd":{},"timestamp":"private-invalid"}}`, "codex-root", ""},
		{"malformed", "claude_code", claude + "{private", "", "line 6"},
		{"truncated", "codex", codex + `{"type":`, "", "line 3"},
		{"trailing JSON", "codex", codex + `{} {}`, "", "line 3"},
		{"claude conflicting identity", "claude_code", claude + `{"sessionId":"other"}`, "", "line 6"},
		{"codex null session identity", "codex", strings.Replace(codex, `"session_id":"codex-root"`, `"session_id":null`, 1), "", "line 1"},
		{"late null session identity", "codex", codex + strings.Replace(codex, `"session_id":"codex-root"`, `"session_id":null`, 1), "", "line 3"},
		{"codex absent session identity", "codex", strings.Replace(codex, `"session_id":"codex-root",`, ``, 1), "codex-root", ""},
		{"codex child null session identity", "codex", strings.Replace(fixture(t, "codex-child"), `"session_id":"codex-parent"`, `"session_id":null`, 1), "", ""},
		{"codex conflicting identity", "codex", strings.Replace(codex, `"session_id":"codex-root"`, `"session_id":"other"`, 1), "", "line 1"},
		{"late different metadata", "codex", codex + strings.ReplaceAll(codex, "codex-root", "other"), "", "line 3"},
		{"late child metadata", "codex", codex + fixture(t, "codex-child"), "", "line 3"},
		{"bad timestamp", "claude_code", strings.Replace(claude, "2026-01-02T00:01:00Z", "private-invalid", 1), "", "line 3"},
		{"bad metadata timestamp", "codex", strings.Replace(codex, "2026-01-02T00:00:00Z", "private-invalid", 1), "", "line 1"},
		{"bad event timestamp", "codex", codex + `{"timestamp":"private-invalid"}`, "", "line 3"},
		{"wrong first row", "codex", "\n{}\n" + codex, "", "line 2"},
		{"missing id", "codex", `{"type":"session_meta","payload":{}}`, "", "line 1"},
		{"whitespace id", "codex", strings.ReplaceAll(codex, "codex-root", "   "), "", "line 1"},
		{"claude whitespace id", "claude_code", `{"sessionId":"  "}`, "", "line 1"},
		{"bookkeeping only", "claude_code", "{}", "", "line 1"},
		{"empty", "codex", "", "", "line 1"},
		{"unknown source", "unknown", codex, "", "unsupported source"},
		{"large opaque event", "codex", codex + `{"type":"response_item","payload":{"content":"` + strings.Repeat("x", 16*1024*1024) + `"}}`, "codex-root", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Read(tc.source, strings.NewReader(tc.input))
			if tc.diagnostic != "" {
				if err == nil || !strings.Contains(err.Error(), tc.diagnostic) || strings.Contains(err.Error(), "private") || !reflect.DeepEqual(got, Record{}) {
					t.Fatalf("got %+v, %v", got, err)
				}
			} else if err != nil || got.Session.SessionID != tc.id {
				t.Fatalf("got %+v, %v", got, err)
			}
			if tc.diagnostic == "" {
				if tc.id == "" && !reflect.DeepEqual(got, Record{}) {
					t.Fatalf("child yielded metadata: %+v", got)
				}
				if tc.id != "" {
					version := "2.1.285"
					if tc.source == "codex" {
						version = "0.139.0"
					}
					if got.Session.SourceVersion != version || got.Session.FirstObservedAt.Format(time.RFC3339) != "2026-01-02T00:00:00Z" || got.Session.LastObservedAt.Format(time.RFC3339) != "2026-01-02T00:02:00Z" {
						t.Fatalf("ignored row polluted root metadata: %+v", got)
					}
				}
			}
		})
	}
}

func TestReadOptionalMetadataAndUTC(t *testing.T) {
	for _, source := range []string{"claude_code", "codex"} {
		file := "claude-root"
		if source == "codex" {
			file = "codex-root"
		}
		var input strings.Builder
		for _, row := range strings.Split(strings.TrimSpace(fixture(t, file)), "\n") {
			var fields map[string]any
			if err := json.Unmarshal([]byte(row), &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, "timestamp")
			delete(fields, "version")
			fields["cwd"] = 42
			if _, ok := fields["sessionId"]; ok {
				fields["sessionId"] = "opaque id"
			}
			if payload, ok := fields["payload"].(map[string]any); ok {
				delete(payload, "timestamp")
				delete(payload, "cli_version")
				payload["cwd"] = 42
				payload["id"], payload["session_id"] = "opaque id", "opaque id"
			}
			b, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			input.Write(b)
			input.WriteByte('\n')
		}
		got, err := Read(source, strings.NewReader(input.String()))
		want := Record{Session: Session{Source: source, SessionID: "opaque id"}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, %v; want %+v", got, err, want)
		}
	}
	got, err := Read("codex", strings.NewReader(`{"type":"session_meta","timestamp":"2026-01-02T02:00:00+02:00","payload":{"id":"id"}}`+"\n"+`{"timestamp":"2026-01-02T00:00:00Z"}`))
	if err != nil || got.Session.FirstObservedAt.Location() != time.UTC || got.Session.LastObservedAt.Location() != time.UTC || !got.Session.FirstObservedAt.Equal(*got.Session.LastObservedAt) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("private reader detail") }
func TestReadFailureDiscardsPartialResult(t *testing.T) {
	got, err := Read("codex", io.MultiReader(strings.NewReader(fixture(t, "codex-root")+"\n"), failingReader{}))
	if err == nil || !strings.Contains(err.Error(), "line 4") || strings.Contains(err.Error(), "private") || !reflect.DeepEqual(got, Record{}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}
