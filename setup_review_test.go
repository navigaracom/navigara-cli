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

type reviewSetupWriter struct {
	bytes.Buffer
	reviews int
}

func (w *reviewSetupWriter) Write(p []byte) (int, error) {
	w.reviews += strings.Count(string(p), "Review your connection")
	return w.Buffer.Write(p)
}

func TestWizardReviewPlainChoices(t *testing.T) {
	for input, want := range map[string]string{
		"y\n":    wizardConnect,
		"n\n":    wizardCancel,
		"edit\n": wizardEdit,
		"back\n": wizardEdit,
	} {
		t.Run(want+input, func(t *testing.T) {
			var out bytes.Buffer
			got, err := wizardReview("all", newSetupInput(strings.NewReader(input), &out))
			if err != nil || got != want {
				t.Fatalf("wizardReview(%q) = %q, %v", input, got, err)
			}
		})
	}
}

func TestSetupEditRebuildsPreviewBeforeFinalConsent(t *testing.T) {
	for _, final := range []string{"cancel", "connect"} {
		t.Run(final, func(t *testing.T) {
			claude, codex := fixtureHomes(t)
			home := filepath.Dir(claude)
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("CLAUDE_CONFIG_DIR", claude)
			t.Setenv("CODEX_HOME", codex)
			t.Setenv("SHELL", "/bin/zsh")
			t.Setenv("ZDOTDIR", home)
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

			var out reviewSetupWriter
			var bodies []string
			srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if out.reviews != 2 {
					t.Error("ingestion or key request before the second review")
				}
				body := mustBody(r)
				bodies = append(bodies, string(body))
				w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
				_, _ = io.WriteString(w, `{}`)
			}))
			defer srv.Close()

			answer := "n"
			if final == "connect" {
				answer = "y"
			}
			input := strings.Join([]string{
				"y", "y", "qa@example.com", "all", "y", "edit",
				"", "n", "", "", "y", answer,
			}, "\n") + "\n"
			var errs bytes.Buffer
			if code := runSetup(nil, strings.NewReader(input), &out, &errs); code != 0 {
				t.Fatalf("setup = %d: %s\n%s", code, errs.String(), out.String())
			}
			if got := strings.Count(out.String(), "Review your connection"); got != 2 {
				t.Fatalf("preview count = %d: %s", got, out.String())
			}
			if got := strings.Count(out.String(), "Activity belongs to: qa@example.com"); got != 2 {
				t.Fatalf("saved email did not survive edit: %s", out.String())
			}
			lastPreview := out.String()[strings.LastIndex(out.String(), "Review your connection"):]
			if !strings.Contains(lastPreview, "Keep new activity in sync: Claude Code") || strings.Contains(lastPreview, "Claude Code, Codex") {
				t.Fatalf("second preview did not exclude Codex: %s", lastPreview)
			}
			root, err := os.UserConfigDir()
			if err != nil {
				t.Fatal(err)
			}
			payloads, err := filepath.Glob(filepath.Join(root, "navigara", "backfills", "setup-*", "payload"))
			if err != nil {
				t.Fatal(err)
			}
			if final == "cancel" {
				if len(bodies) != 0 || len(payloads) != 0 {
					t.Fatalf("cancel sent data or retained preview: requests=%d payloads=%v", len(bodies), payloads)
				}
				if _, err := os.Stat(filepath.Join(claude, "settings.json")); !os.IsNotExist(err) {
					t.Fatal("cancel changed Claude settings")
				}
				return
			}
			if len(bodies) < 2 || len(payloads) != 1 {
				t.Fatalf("connect did not retain only accepted preview: requests=%d payloads=%v", len(bodies), payloads)
			}
			allBodies := strings.Join(bodies, "\n")
			if strings.Contains(allBodies, `"codex"`) || !strings.Contains(allBodies, "qa@example.com") {
				t.Fatalf("upload did not reflect edited settings: %s", allBodies)
			}
			if _, err := os.Stat(filepath.Join(codex, "config.toml")); !os.IsNotExist(err) {
				t.Fatal("excluded Codex settings were created")
			}
			if data, err := os.ReadFile(filepath.Join(claude, "settings.json")); err != nil || !bytes.Contains(data, []byte("test-secret")) {
				t.Fatalf("Claude settings were not applied: %q, %v", data, err)
			}
		})
	}
}
