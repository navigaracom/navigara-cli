package session

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadRepositoryFixtures(t *testing.T) {
	for _, tc := range []struct{ source, file, want string }{
		{"codex", "codex-usage", `[{"record_line":1,"observed_at":"2026-11-08T09:10:13.87Z","branch":"main","commit_hash":"713dae51a693d38c8bf78c81eae9f0e8fca67307","repository_url":"github.com/example/project"}]`},
		{"claude_code", "claude-usage", `[{"record_line":2,"observed_at":"2026-11-08T16:10:04.225Z","branch":"main"}]`},
	} {
		got, err := Read(tc.source, strings.NewReader(fixture(t, tc.file)))
		if err != nil {
			t.Fatal(err)
		}
		assertUsageJSON(t, got.Session.RepositoryObservations, tc.want)
	}
}

// Synthetic context transitions cover recorded changes, clearing, and absent rows.
func TestReadRepositorySyntheticChanges(t *testing.T) {
	for _, source := range []string{"codex", "claude_code"} {
		var input string
		if source == "codex" {
			input = `{"type":"session_meta","payload":{"id":"root"}}` + "\n"
		}
		for _, context := range []string{`"main"`, `null`, `"main"`, `"next"`, `""`, `""`, `"main"`} {
			if source == "codex" {
				git := `{"branch":` + context + `}`
				if context == "null" {
					git = "null"
				}
				input += `{"type":"session_meta","timestamp":"2026-01-02T02:00:00+02:00","payload":{"id":"root","git":` + git + `}}` + "\n\n"
			} else {
				input += `{"sessionId":"root","timestamp":"2026-01-02T02:00:00+02:00","gitBranch":` + context + `}` + "\n\n"
			}
		}
		got, err := Read(source, strings.NewReader(input))
		if err != nil || len(got.Session.RepositoryObservations) != 4 {
			t.Fatalf("got %+v, %v", got, err)
		}
		for i, want := range []struct {
			line   int
			branch string
		}{{1, "main"}, {7, "next"}, {9, ""}, {13, "main"}} {
			if source == "codex" {
				want.line++
			}
			r := got.Session.RepositoryObservations[i]
			if r.RecordLine != want.line || r.Branch != want.branch || r.ObservedAt == nil || r.ObservedAt.Location() != time.UTC || r.ObservedAt.Format(time.RFC3339) != "2026-01-02T00:00:00Z" {
				t.Fatalf("context %d: %+v", i, r)
			}
		}
	}
	input := `{"type":"session_meta","payload":{"id":"root","git":{"branch":"main","commit_hash":"recorded"}}}` + "\n" +
		`{"type":"session_meta","payload":{"id":"root","git":{"branch":"next"}}}` + "\n" +
		`{"type":"session_meta","payload":{"id":"root","git":{}}}`
	got, err := Read("codex", strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	assertUsageJSON(t, got.Session.RepositoryObservations, `[{"record_line":1,"branch":"main","commit_hash":"recorded"},{"record_line":2,"branch":"next"},{"record_line":3}]`)
}

func TestReadRepositoryURLPrivacy(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"https://user:private@GitHub.COM:443/Owner/Repo.git/?token=private#private", "github.com/owner/repo"},
		{"https://user%40private:p%3Ass@GitHub.com/Owner/Repo.git", "github.com/owner/repo"},
		{"http://host/Org/Repo///", "host/org/repo"},
		{"ssh://git@host:22/Org/Repo.git", "host/org/repo"},
		{"git://host/Org/Repo.git", "host/org/repo"},
		{"git@GitHub.com:Owner/Repo.git?token=private#private", "github.com/owner/repo"},
		{"user%40private@host:Org/Repo.git", "host/org/repo"},
		{"host:Org/Repo.git", "host/org/repo"},
		{"user:private@host/Owner/Repo", ""},
		{"user:private%40host/Owner/Repo", ""},
		{"file:/private/repo", ""},
		{"/private/repo", ""}, {"./private/repo", ""}, {"../private/repo", ""}, {"repo", ""},
		{`C:\private\repo`, ""}, {"C:/private/repo", ""}, {`\\host\private\repo`, ""},
		{"file:///private/repo", ""}, {"ftp://user:private@host/Org/Repo", ""},
		{"https://host", ""}, {"https:///Org/Repo", ""}, {"https://host/.git", ""},
		{"https://user:private@host:bad/Org/Repo", ""}, {"private%40host:Org/Repo", ""},
		{"https://host/Org/Repo%5Cprivate", ""}, {"https://host/Org/Repo%0Aprivate", ""}, {"https://host/Org/Repo%00private", ""},
		{"https://host/Org/Repo%3Fname", "host/org/repo%3fname"}, {"https://host/Org/Repo%23name", "host/org/repo%23name"},
		{"https://host/Org%2FRepo.git", "host/org%2frepo"},
		{"https://user:12/credential-marker@github.com/org/repo", ""},
		{"https://user:12/credential-marker%40github.com/org/repo", ""},
		{"https://github.com/org/repo@credential-marker", ""},
		{"https://github.com/org/repo%40credential-marker", ""},
		{"git@github.com:org/repo@credential-marker", ""},
		{"git@github.com:org/repo%40credential-marker", ""},
		{"https://github.com/org/.git", ""},
		{"https://github.com/org/.git/", ""},
		{"git@github.com:org/.git", ""},
		{"https://user:12/se/cret?x@github.com/org/repo", ""},
		{"https://user:12/se/cret?x%40github.com/org/repo", ""},
		{"https://user:12/se/cret?x%2540github.com/org/repo", ""},
		{"https://user:12/se/cret#x@github.com/org/repo", ""},
		{"https://user:12/se/cret#x%40github.com/org/repo", ""},
		{"https://user:12/se/cret#x%2540github.com/org/repo", ""},
		{"user:se/cret?x@host:org/repo", ""},
		{"user:se/cret?x%40host:org/repo", ""},
		{"user:se/cret?x%2540host:org/repo", ""},
		{"user:se/cret#x@host:org/repo", ""},
		{"user:se/cret#x%40host:org/repo", ""},
		{"user:se/cret#x%2540host:org/repo", ""},
		{"https://host/org/repo?token=%ZZ", ""},
		{"https://host/org/repo?token=%00", ""},
		{"https://host/org/repo#token%5Cmarker", ""},
		{"https://host/org/repo%2540marker", ""},
		{"https://user:p%3F%23%40%25@host/org/repo?token=ordinary%2Fvalue#ordinary%3Fvalue", "host/org/repo"},
		{"git@host:org/repo?token=ordinary%2Fvalue#ordinary%3Fvalue", "host/org/repo"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			value, _ := json.Marshal(tc.input)
			input := `{"type":"session_meta","payload":{"id":"root","git":{"branch":"main","repository_url":` + string(value) + `}}}`
			got, err := Read("codex", strings.NewReader(input))
			if err != nil || len(got.Session.RepositoryObservations) != 1 || got.Session.RepositoryObservations[0].RepositoryURL != tc.want || got.Session.RepositoryObservations[0].Branch != "main" {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestReadRepositorySyntheticFiltersAndInvalid(t *testing.T) {
	for _, source := range []string{"codex", "claude_code"} {
		file := "codex"
		diagnostic := "line 4: invalid repository context"
		if source == "claude_code" {
			file = "claude"
			diagnostic = "line 7: invalid repository context"
		}
		for _, context := range []string{`42`, `false`, `[]`, `"private"`, `{"branch":42}`, `{"commit_hash":[]}`, `{"repository_url":false}`} {
			row := `{"type":"session_meta","payload":{"id":"codex-root","git":` + context + `}}`
			if source == "claude_code" {
				row = `{"sessionId":"claude_code-root","gitBranch":` + context + `}`
			}
			if source == "claude_code" && context == `"private"` {
				continue
			}
			got, err := Read(source, strings.NewReader(fixture(t, file+"-root")+"\n"+row))
			if err == nil || err.Error() != diagnostic || !reflect.DeepEqual(got, Record{}) {
				t.Fatalf("got %+v, %v", got, err)
			}
		}
		child := fixture(t, file+"-child")
		if source == "codex" {
			child = strings.Replace(child, `"payload":{`, `"payload":{"git":false,`, 1)
		} else {
			child = strings.Replace(child, `{`, `{"gitBranch":false,`, 1)
		}
		got, err := Read(source, strings.NewReader(child))
		if err != nil || !reflect.DeepEqual(got, Record{}) {
			t.Fatalf("child context parsed: %+v, %v", got, err)
		}
	}
	got, err := Read("claude_code", strings.NewReader(`{"gitBranch":false}`+"\n"+fixture(t, "claude-root")))
	if err != nil || len(got.Session.RepositoryObservations) != 0 {
		t.Fatalf("bookkeeping context parsed: %+v, %v", got, err)
	}
	got, err = Read("codex", strings.NewReader(fixture(t, "codex-root")+`{"type":"response_item","payload":{"git":false}}`))
	if err != nil || len(got.Session.RepositoryObservations) != 0 {
		t.Fatalf("opaque context parsed: %+v, %v", got, err)
	}
}
