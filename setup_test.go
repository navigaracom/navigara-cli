package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSetupContinueConfiguresAndStreams(t *testing.T) {
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
			var requests atomic.Int32
			srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				request := requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer test-secret" {
					t.Error("missing token")
				}
				body := mustBody(r)
				if request == 1 && string(body) != "{}" {
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
			input := strings.Join([]string{"y", "y", "dev@example.com", "all", answer, ""}, "\n")
			var out, errs bytes.Buffer
			code := runSetup(context.Background(), nil, strings.NewReader(input), &out, &errs)
			if code != 0 {
				t.Fatalf("exit %d: %s\n%s", code, errs.String(), out.String())
			}
			if strings.Contains(out.String()+errs.String(), "test-secret") {
				t.Fatal("token leaked")
			}
			if strings.Contains(out.String(), "Review your connection") || strings.Contains(out.String(), "y/n/edit") {
				t.Fatalf("wizard retained a second approval screen: %s", out.String())
			}
			data, _ := os.ReadFile(filepath.Join(claudeHome, "settings.json"))
			if !strings.Contains(string(data), "keep-me") {
				t.Fatal("unrelated setting removed")
			}
			if !apply {
				if requests.Load() != 0 || strings.Contains(string(data), "TELEMETRY") {
					t.Fatal("decline had side effects")
				}
				return
			}
			if requests.Load() < 2 || !strings.Contains(string(data), "test-secret") {
				t.Fatal("no upload or missing native auth")
			}
			if !strings.Contains(out.String(), "Connecting your tools...") {
				t.Fatal("Continue did not begin direct setup")
			}
			for _, path := range []string{filepath.Join(codexHome, "config.toml"), filepath.Join(home, ".zshrc")} {
				data, err := os.ReadFile(path)
				if err != nil || !strings.Contains(string(data), telemetryBegin) {
					t.Fatalf("missing managed settings: %s %v", path, err)
				}
			}
			if !strings.Contains(out.String(), "Imported 2 past sessions.\nNew sessions sync automatically after you restart Claude Code / Codex in a new terminal.") {
				t.Fatalf("finish screen missing: %s", out.String())
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
	srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	input := strings.Join([]string{"y", "y", "dev@example.com", "skip", "y", "y", ""}, "\n")
	var out, errs bytes.Buffer
	if runSetup(context.Background(), nil, strings.NewReader(input), &out, &errs) == 0 {
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
			var payloadsMu sync.Mutex
			failedPayloads := map[string]int{}
			retriedPayloads := map[string]int{}
			routeHash := func(path string, body []byte) string {
				sum := sha256.Sum256(body)
				return path + ":" + hex.EncodeToString(sum[:])
			}
			srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				if failure == "auth" {
					w.WriteHeader(401)
					return
				}
				if failure != "policy" {
					w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
				}
				if string(body) != "{}" {
					payloadsMu.Lock()
					failing := fail
					payloads := failedPayloads
					if !failing {
						payloads = retriedPayloads
					}
					payloads[routeHash(r.URL.Path, body)]++
					payloadsMu.Unlock()
					if failing {
						w.WriteHeader(400)
						return
					}
				}
				io.WriteString(w, `{}`)
			}))
			defer srv.Close()
			input := strings.Join([]string{"y", "n", "dev@example.com", "all", "y", "y", ""}, "\n")
			var out, errs bytes.Buffer
			if runSetup(context.Background(), nil, strings.NewReader(input), &out, &errs) == 0 {
				t.Fatal("expected setup failure")
			}
			_, err := os.Stat(filepath.Join(claudeHome, "settings.json"))
			if failure != "upload" {
				payloadsMu.Lock()
				gotPayloads := len(failedPayloads) + len(retriedPayloads)
				payloadsMu.Unlock()
				if !os.IsNotExist(err) || gotPayloads != 0 {
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
			manifest, err := loadSetupBatches(dirs[0])
			if err != nil {
				t.Fatal(err)
			}
			expectedPayloads := map[string]int{}
			for _, batch := range manifest.Batches {
				body, err := os.ReadFile(filepath.Join(dirs[0], batch.Name))
				if err != nil {
					t.Fatal(err)
				}
				kind, _, _ := strings.Cut(batch.Name, "-")
				expectedPayloads[routeHash("/api/otlp/v1/"+kind, body)]++
			}
			// Changes to source history after preview cannot affect the retry.
			os.RemoveAll(filepath.Join(claudeHome, "projects"))
			payloadsMu.Lock()
			fail = false
			payloadsMu.Unlock()
			if runSetup(context.Background(), []string{"--resume", dirs[0]}, strings.NewReader("y\n"), &out, &errs) != 0 {
				t.Fatal(errs.String())
			}
			payloadsMu.Lock()
			gotFailedPayloads := maps.Clone(failedPayloads)
			gotRetriedPayloads := maps.Clone(retriedPayloads)
			payloadsMu.Unlock()
			if len(gotFailedPayloads) == 0 {
				t.Fatal("initial upload did not send a frozen batch")
			}
			for payload, count := range gotFailedPayloads {
				if count > expectedPayloads[payload] {
					t.Fatalf("initial upload changed frozen history: failed=%v expected=%v", gotFailedPayloads, expectedPayloads)
				}
			}
			if !reflect.DeepEqual(gotRetriedPayloads, expectedPayloads) {
				t.Fatalf("retry rebuilt history: failed=%v retried=%v expected=%v", gotFailedPayloads, gotRetriedPayloads, expectedPayloads)
			}
			if strings.Contains(out.String()+errs.String(), "test-secret") {
				t.Fatal("token exposed")
			}
		})
	}
}

func TestSetupSkipsUnreadableHistory(t *testing.T) {
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
			srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if string(mustBody(r)) != "{}" {
					requests++
				}
				w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
				io.WriteString(w, `{}`)
			}))
			defer srv.Close()
			var out, errs bytes.Buffer
			selectClaude, selectCodex := "y", "n"
			if source == "codex" {
				selectClaude, selectCodex = "n", "y"
			}
			input := selectClaude + "\n" + selectCodex + "\n" + "dev@example.com\nall\ny\ny\n"
			code := runSetup(context.Background(), nil, strings.NewReader(input), &out, &errs)
			if !strings.Contains(errs.String(), "Skipped 1 session files that could not be read:\n  ") || !strings.Contains(errs.String(), broken) {
				t.Fatalf("missing skip report: %s", errs.String())
			}
			if source == "claude" {
				// The two readable fixture sessions are still imported.
				if code != 0 || requests == 0 {
					t.Fatalf("exit=%d requests=%d stderr=%s", code, requests, errs.String())
				}
				return
			}
			if code == 0 || !strings.Contains(errs.String(), "could not read session history") || strings.Contains(out.String()+errs.String(), "backfill") {
				t.Fatalf("history with no readable session accepted: exit=%d stdout=%s stderr=%s", code, out.String(), errs.String())
			}
			if requests != 0 {
				t.Fatalf("uploaded before incomplete history stopped setup: %d", requests)
			}
			if _, err := os.Stat(filepath.Join(claudeHome, "settings.json")); !os.IsNotExist(err) {
				t.Fatal("changed settings after unreadable history")
			}
			if _, err := os.Stat(filepath.Join(codexHome, "config.toml")); !os.IsNotExist(err) {
				t.Fatal("changed Codex settings after unreadable history")
			}
			if !strings.Contains(errs.String(), "Codex rollout header") {
				t.Fatalf("missing Codex diagnostic: %s", errs.String())
			}
		})
	}
}
