package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupPreviewConsentAndNativeStreaming(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(map[bool]string{false: "decline", true: "apply"}[apply], func(t *testing.T) {
			claudeHome, codexHome := fixtureHomes(t)
			home := filepath.Dir(claudeHome)
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
			t.Setenv("CODEX_HOME", codexHome)
			t.Setenv("SHELL", "/bin/zsh")
			t.Setenv("ZDOTDIR", home)
			os.WriteFile(filepath.Join(claudeHome, "settings.json"), []byte(`{"model":"keep-me"}`), 0600)
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Error("missing token")
				}
				body, _ := io.ReadAll(r.Body)
				if requests == 1 && string(body) != "{}" {
					t.Error("first request must only probe")
				}
				w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
				io.WriteString(w, `{}`)
			}))
			defer srv.Close()
			answer := "n"
			if apply {
				answer = "y"
			}
			input := strings.Join([]string{"y", "y", srv.URL, "test-secret", "dev@example.com", "all", "y", answer, ""}, "\n")
			var out, errs bytes.Buffer
			code := runSetup(nil, strings.NewReader(input), &out, &errs)
			if code != 0 {
				t.Fatalf("exit %d: %s\n%s", code, errs.String(), out.String())
			}
			if strings.Contains(out.String()+errs.String(), "test-secret") {
				t.Fatal("token leaked")
			}
			data, _ := os.ReadFile(filepath.Join(claudeHome, "settings.json"))
			if !strings.Contains(string(data), "keep-me") {
				t.Fatal("unrelated setting removed")
			}
			if !apply {
				if requests != 0 || strings.Contains(string(data), "TELEMETRY") {
					t.Fatal("decline had side effects")
				}
				return
			}
			if requests < 2 || !strings.Contains(string(data), "test-secret") {
				t.Fatal("no upload or missing native auth")
			}
			for _, path := range []string{filepath.Join(codexHome, "config.toml"), filepath.Join(home, ".zshrc")} {
				data, err := os.ReadFile(path)
				if err != nil || !strings.Contains(string(data), telemetryBegin) {
					t.Fatalf("missing managed settings: %s %v", path, err)
				}
			}
			if !strings.Contains(out.String(), "not yet verified") {
				t.Fatal("must distinguish configured from observed streaming")
			}
		})
	}
}

func TestSetupRollsBackFirstFileWhenLaterTargetChanges(t *testing.T) {
	claudeHome, codexHome := fixtureHomes(t)
	home := filepath.Dir(claudeHome)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZDOTDIR", home)
	claudeSettings := filepath.Join(claudeHome, "settings.json")
	codexSettings := filepath.Join(codexHome, "config.toml")
	if err := os.WriteFile(claudeSettings, []byte(`{"model":"keep"}`), 0600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, _ := io.ReadAll(r.Body); string(body) != "{}" {
			t.Errorf("unexpected upload during failed setup: %s", body)
		}
		if err := os.WriteFile(codexSettings, []byte("concurrent = true\n"), 0600); err != nil {
			t.Error(err)
		}
		w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	input := strings.Join([]string{"y", "y", srv.URL, "test-secret", "dev@example.com", "skip", "y", "y", ""}, "\n")
	var out, errs bytes.Buffer
	if runSetup(nil, strings.NewReader(input), &out, &errs) == 0 {
		t.Fatal("later target change accepted")
	}
	if got, _ := os.ReadFile(claudeSettings); !bytes.Equal(got, []byte(`{"model":"keep"}`)) {
		t.Fatalf("first target was not rolled back: %q", got)
	}
	if got, _ := os.ReadFile(codexSettings); !bytes.Equal(got, []byte("concurrent = true\n")) {
		t.Fatalf("concurrent target changed: %q", got)
	}
}

func TestSetupRejectedPreflightAndResumableUpload(t *testing.T) {
	for _, failure := range []string{"policy", "auth", "upload"} {
		t.Run(failure, func(t *testing.T) {
			claudeHome, codexHome := fixtureHomes(t)
			home := filepath.Dir(claudeHome)
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
			t.Setenv("CODEX_HOME", codexHome)
			t.Setenv("SHELL", "/bin/zsh")
			t.Setenv("ZDOTDIR", home)
			fail := true
			var payloads []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if failure == "auth" {
					w.WriteHeader(401)
					return
				}
				if failure != "policy" {
					w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
				}
				if string(body) != "{}" {
					payloads = append(payloads, string(body))
					if fail {
						w.WriteHeader(400)
						return
					}
				}
				io.WriteString(w, `{}`)
			}))
			defer srv.Close()
			input := strings.Join([]string{"y", "n", srv.URL, "test-secret", "dev@example.com", "all", "y", "y", ""}, "\n")
			var out, errs bytes.Buffer
			if runSetup(nil, strings.NewReader(input), &out, &errs) == 0 {
				t.Fatal("expected setup failure")
			}
			_, err := os.Stat(filepath.Join(claudeHome, "settings.json"))
			if failure != "upload" {
				if !os.IsNotExist(err) || len(payloads) != 0 {
					t.Fatal("failed preflight changed config/uploaded data")
				}
				return
			}
			if err != nil {
				t.Fatal("upload failure lost streaming configuration")
			}
			root, _ := os.UserConfigDir()
			dirs, _ := filepath.Glob(filepath.Join(root, "navigara", "backfills", "setup-*", "payload"))
			if len(dirs) != 1 {
				t.Fatal("missing frozen payload")
			}
			// Changes to source history after preview cannot affect the retry.
			os.RemoveAll(filepath.Join(claudeHome, "projects"))
			fail = false
			if runSetup([]string{"--resume", dirs[0]}, strings.NewReader("test-secret\ny\n"), &out, &errs) != 0 {
				t.Fatal(errs.String())
			}
			if len(payloads) < 2 || payloads[0] != payloads[1] {
				t.Fatal("retry rebuilt history")
			}
			if strings.Contains(out.String()+errs.String(), "test-secret") {
				t.Fatal("token exposed")
			}
		})
	}
}

func TestSetupStopsOnIncompleteHistory(t *testing.T) {
	for _, source := range []string{"claude", "codex"} {
		t.Run(source, func(t *testing.T) {
			claudeHome, codexHome := fixtureHomes(t)
			home := filepath.Dir(claudeHome)
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
			t.Setenv("CODEX_HOME", codexHome)
			t.Setenv("SHELL", "/bin/zsh")
			t.Setenv("ZDOTDIR", home)
			var broken string
			if source == "claude" {
				broken = filepath.Join(claudeHome, "projects", "broken", "broken.jsonl")
			} else {
				broken = filepath.Join(codexHome, "sessions", "rollout-bad.jsonl")
			}
			if err := os.MkdirAll(filepath.Dir(broken), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(broken, []byte(`{"type":`), 0600); err != nil {
				t.Fatal(err)
			}
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
			defer srv.Close()
			var out, errs bytes.Buffer
			selectClaude, selectCodex := "y", "n"
			if source == "codex" {
				selectClaude, selectCodex = "n", "y"
			}
			input := selectClaude + "\n" + selectCodex + "\n" + srv.URL + "\nsecret\ndev@example.com\nall\ny\ny\n"
			if runSetup(nil, strings.NewReader(input), &out, &errs) == 0 {
				t.Fatal("incomplete history accepted")
			}
			if requests != 0 {
				t.Fatalf("requested before incomplete history stopped setup: %d", requests)
			}
			if _, err := os.Stat(filepath.Join(claudeHome, "settings.json")); !os.IsNotExist(err) {
				t.Fatal("changed settings after incomplete preview")
			}
			if _, err := os.Stat(filepath.Join(codexHome, "config.toml")); !os.IsNotExist(err) {
				t.Fatal("changed Codex settings after incomplete preview")
			}
			if source == "codex" && !strings.Contains(errs.String(), "Codex rollout header") {
				t.Fatalf("missing Codex diagnostic: %s", errs.String())
			}
		})
	}
}
