package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrganizationNamesCannotAlterTerminalChoices(t *testing.T) {
	var out bytes.Buffer
	p := newSetupInput(strings.NewReader("2\n"), &out)
	orgs := []organization{{ID: "one", Name: "One"}, {ID: "two", Name: "Two\nForged prompt\x1b[2J"}}
	selected, err := chooseOrganization(orgs, "", true, p)
	if err != nil || selected.ID != "two" {
		t.Fatalf("organization selection = %v, %v", selected, err)
	}
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "\nForged prompt") {
		t.Fatalf("organization name altered terminal choices: %q", out.String())
	}
}

func TestWizardCanSkipConflictingToolWithoutChangingSettings(t *testing.T) {
	for _, action := range []string{"cancel", "skip", "connect"} {
		t.Run(action, func(t *testing.T) {
			skip, apply := action != "cancel", action == "connect"
			claude, codex := fixtureHomes(t)
			home := filepath.Dir(claude)
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("CLAUDE_CONFIG_DIR", claude)
			t.Setenv("CODEX_HOME", codex)
			t.Setenv("SHELL", "/bin/zsh")
			t.Setenv("ZDOTDIR", home)
			path := filepath.Join(codex, "config.toml")
			existing := []byte("[otel]\nexporter = \"console\"\n")
			if err := os.WriteFile(path, existing, 0600); err != nil {
				t.Fatal(err)
			}
			fixture, err := os.ReadFile("internal/session/testdata/codex-root.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(codex, "sessions"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(codex, "sessions", "rollout-existing.jsonl"), fixture, 0600); err != nil {
				t.Fatal(err)
			}
			posts := 0
			srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts++
				if !apply {
					t.Error("ingestion request without consent")
				}
				data, _ := io.ReadAll(r.Body)
				if bytes.Contains(data, []byte(`"codex"`)) {
					t.Error("uploaded history for skipped Codex")
				}
				w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
				io.WriteString(w, `{}`)
			}))
			defer srv.Close()
			answer := "n"
			if skip {
				answer = "y"
			}
			consent := "n"
			if apply {
				consent = "y"
			}
			var out, errs bytes.Buffer
			code := runSetup(nil, strings.NewReader("y\ny\n\nall\n"+answer+"\ny\n"+consent+"\n"), &out, &errs)
			if code != 0 {
				t.Fatalf("wizard failed rather than offering recovery: %d %s", code, &errs)
			}
			if skip && (!strings.Contains(out.String(), "2 sessions") || !strings.Contains(out.String(), "Keep new activity in sync: Claude Code")) {
				t.Fatalf("preview did not reflect the remaining tool: %s", &out)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, existing) {
				t.Fatalf("conflicting settings changed: %q %v", got, err)
			}
			data, err := os.ReadFile(filepath.Join(claude, "settings.json"))
			if apply {
				if err != nil || !bytes.Contains(data, []byte("test-secret")) || posts < 2 {
					t.Fatal("remaining Claude tool was not configured and imported")
				}
			} else if !os.IsNotExist(err) || posts != 0 {
				t.Fatal("cancel/decline changed settings or contacted ingestion")
			}
		})
	}
}
