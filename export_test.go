package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fixtureHomes writes two Claude Code sessions and a ~/.claude.json with the signed-in account.
func fixtureHomes(t *testing.T) (claudeHome, codexHome string) {
	t.Helper()
	root := t.TempDir()
	claudeHome = filepath.Join(root, ".claude")
	codexHome = filepath.Join(root, ".codex")
	for _, id := range []string{"aaaaaaaa-0000-0000-0000-000000000001", "aaaaaaaa-0000-0000-0000-000000000002"} {
		lines := []string{
			`{"type":"user","sessionId":"` + id + `","promptId":"p-` + id + `","timestamp":"2026-10-01T10:00:00Z","entrypoint":"cli","message":{"role":"user","content":"hi"}}`,
			`{"type":"assistant","sessionId":"` + id + `","timestamp":"2026-10-01T10:00:03Z","requestId":"r-` + id + `","message":{"id":"m","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":3,"output_tokens":7}}}`,
		}
		path := filepath.Join(claudeHome, "projects", "-repo", id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := `{"userID":"hash","oauthAccount":{"emailAddress":"Dev@Example.com","accountUuid":"acc"}}`
	if err := os.WriteFile(filepath.Join(root, ".claude.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	return claudeHome, codexHome
}

type received struct {
	mu     sync.Mutex
	paths  []string
	bodies []string
}

func TestUploadSendsPinnedSessions(t *testing.T) {
	claudeHome, codexHome := fixtureHomes(t)
	var got received
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		got.mu.Lock()
		got.paths = append(got.paths, r.URL.Path)
		got.bodies = append(got.bodies, string(body))
		got.mu.Unlock()
	}))
	defer server.Close()

	output := filepath.Join(t.TempDir(), "out")
	var stdout, stderr bytes.Buffer
	code := run([]string{"sessions", "upload", "--claude-home", claudeHome, "--codex-home", codexHome,
		"--session", "aaaaaaaa-0000-0000-0000-000000000002", "--output", output,
		"--send", "--url", server.URL, "--token", "secret"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if len(got.paths) != 2 || got.paths[0] != "/otlp/v1/logs" || got.paths[1] != "/otlp/v1/metrics" {
		t.Fatalf("posted %v, want logs then metrics", got.paths)
	}
	all := strings.Join(got.bodies, "")
	if strings.Contains(all, "000000000001") || !strings.Contains(all, "000000000002") {
		t.Errorf("unpinned session exported")
	}
	if !strings.Contains(all, `"dev@example.com"`) || !strings.Contains(all, `"navigara.backfill"`) {
		t.Errorf("bodies lack the identity or the backfill marker")
	}
	var logs struct {
		ResourceLogs []json.RawMessage `json:"resourceLogs"`
	}
	if err := json.Unmarshal([]byte(got.bodies[0]), &logs); err != nil || len(logs.ResourceLogs) != 1 {
		t.Errorf("logs body is not OTLP JSON: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(output, "*.json"))
	if len(files) != 2 {
		t.Errorf("wrote %d files, want 2", len(files))
	}
}

func TestUploadStopsOnRejectedKey(t *testing.T) {
	claudeHome, codexHome := fixtureHomes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := run([]string{"sessions", "upload", "--claude-home", claudeHome, "--codex-home", codexHome,
		"--output", filepath.Join(t.TempDir(), "out"), "--send", "--url", server.URL, "--token", "bad"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "analytics ingestor") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}

func TestUploadNeedsAnIdentity(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"sessions", "upload", "--claude-home", filepath.Join(root, ".claude"), "--codex-home", filepath.Join(root, ".codex"),
		"--output", filepath.Join(root, "out")}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "--email") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, "out")); !os.IsNotExist(err) {
		t.Fatalf("output created without an identity")
	}
}

func TestUploadRejectsIncompleteClaudeDiscovery(t *testing.T) {
	t.Run("missing explicit home", func(t *testing.T) {
		root := t.TempDir()
		output := filepath.Join(root, "out")
		var stdout, stderr bytes.Buffer
		code := run([]string{"sessions", "upload", "--source", "claude", "--email", "dev@example.com",
			"--claude-home", filepath.Join(root, "missing"), "--output", output}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "missing") {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("output created after failed discovery: %v", err)
		}
	})

	t.Run("malformed transcript", func(t *testing.T) {
		claudeHome, codexHome := fixtureHomes(t)
		bad := filepath.Join(claudeHome, "projects", "-repo", "aaaaaaaa-0000-0000-0000-000000000001.jsonl")
		if err := os.WriteFile(bad, []byte("{malformed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(t.TempDir(), "out")
		var stdout, stderr bytes.Buffer
		code := run([]string{"sessions", "upload", "--source", "claude", "--claude-home", claudeHome,
			"--codex-home", codexHome, "--output", output}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "line 1") {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("output created after failed discovery: %v", err)
		}
	})
}

func TestUploadRejectsInvalidCodexDiscovery(t *testing.T) {
	t.Run("malformed rollout header", func(t *testing.T) {
		claudeHome, codexHome := fixtureHomes(t)
		rollout := filepath.Join(codexHome, "sessions", "rollout-bad.jsonl")
		if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(rollout, []byte(`{"type":`), 0o600); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(t.TempDir(), "out")
		var stdout, stderr bytes.Buffer
		code := run([]string{"sessions", "upload", "--source", "codex", "--email", "dev@example.com",
			"--claude-home", claudeHome, "--codex-home", codexHome, "--output", output}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "invalid Codex rollout header") {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("output created after failed discovery: %v", err)
		}
	})

	t.Run("invalid root", func(t *testing.T) {
		claudeHome, codexHome := fixtureHomes(t)
		if err := os.WriteFile(filepath.Join(codexHome, "sessions"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		code := run([]string{"sessions", "upload", "--source", "codex", "--email", "dev@example.com",
			"--claude-home", claudeHome, "--codex-home", codexHome, "--output", filepath.Join(t.TempDir(), "out")}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "not a directory") {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
	})
}

func TestUploadReportsBuildFailuresAfterSendingOtherSessions(t *testing.T) {
	for _, send := range []bool{false, true} {
		t.Run(map[bool]string{false: "dry run", true: "send"}[send], func(t *testing.T) {
			claudeHome, codexHome := fixtureHomes(t)
			for name, data := range map[string]string{
				"rollout-good.jsonl": `{"timestamp":"2026-10-01T10:00:00Z","type":"session_meta","payload":{"id":"good","originator":"codex-tui"}}` + "\n",
				"rollout-bad.jsonl":  `{"timestamp":"2026-10-01T10:00:00Z","type":"session_meta","payload":{"id":"bad","originator":"codex-tui"}}` + "\n{malformed\n",
			} {
				path := filepath.Join(codexHome, "sessions", name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
			defer server.Close()
			args := []string{"sessions", "upload", "--source", "codex", "--email", "dev@example.com", "--claude-home", claudeHome,
				"--codex-home", codexHome, "--output", filepath.Join(t.TempDir(), "out")}
			if send {
				args = append(args, "--send", "--url", server.URL, "--token", "secret")
			}
			var stdout, stderr bytes.Buffer
			code := run(args, &stdout, &stderr)
			if code != 1 || (send && requests.Load() == 0) || (!send && requests.Load() != 0) || !strings.Contains(stdout.String(), "failed=1") || !strings.Contains(stderr.String(), "bad") {
				t.Fatalf("exit=%d requests=%d stdout=%q stderr=%q", code, requests.Load(), stdout.String(), stderr.String())
			}
		})
	}
}

func TestUploadSkipsASourceWithoutItsOwnEmail(t *testing.T) {
	claudeHome, codexHome := fixtureHomes(t)
	rollout := filepath.Join(codexHome, "sessions", "2026", "10", "01", "rollout-2026-10-01T10-00-00-c1.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o700); err != nil {
		t.Fatal(err)
	}
	meta := `{"timestamp":"2026-10-01T10:00:00Z","type":"session_meta","payload":{"id":"codex-1","originator":"codex-tui"}}` + "\n"
	if err := os.WriteFile(rollout, []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"sessions", "upload", "--claude-home", claudeHome, "--codex-home", codexHome,
		"--output", filepath.Join(t.TempDir(), "out")}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stderr.String(), "skipping Codex") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "codex-1") || !strings.Contains(stdout.String(), "claude_code") {
		t.Errorf("stdout = %q; want Claude Code sessions only", stdout.String())
	}
}
