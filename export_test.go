package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/navigaracom/navigara-cli/internal/telemetry"
)

// fixtureHomes writes two Claude Code sessions and a ~/.claude.json with the signed-in account
// into a new $HOME.
func fixtureHomes(t *testing.T) (claudeHome, codexHome string) {
	t.Helper()
	root := t.TempDir()
	setHome(t, root) // The default layout: ~/.claude with ~/.claude.json next to it.
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
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "legacy --send"}[legacy], func(t *testing.T) { testUploadSendsPinnedSessions(t, legacy) })
	}
}

func testUploadSendsPinnedSessions(t *testing.T, legacy bool) {
	claudeHome, codexHome := fixtureHomes(t)
	var got received
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body := mustBody(r)
		if string(body) == "{}" {
			w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
			return
		}
		if r.Header.Get("Content-Encoding") != "gzip" {
			t.Error("upload is not gzip-compressed")
		}
		got.mu.Lock()
		got.paths = append(got.paths, r.URL.Path)
		got.bodies = append(got.bodies, string(body))
		got.mu.Unlock()
	}))
	defer server.Close()

	output := filepath.Join(t.TempDir(), "out")
	args := []string{"sessions", "upload", "--claude-home", claudeHome, "--codex-home", codexHome,
		"--session", "aaaaaaaa-0000-0000-0000-000000000002", "--output", output, "--url", server.URL, "--token", "secret"}
	if legacy {
		args = append(args, "--send")
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Sessions: 1. Upload batches: 2.") || !strings.HasSuffix(stdout.String(), "Navigara received 2 upload batches.\n") {
		t.Errorf("stdout = %q", stdout.String())
	}
	if len(got.paths) != 2 || got.paths[0] == got.paths[1] {
		t.Fatalf("posted %v, want logs and metrics", got.paths)
	}
	if got.paths[0] != "/otlp/v1/logs" {
		got.bodies[0], got.bodies[1] = got.bodies[1], got.bodies[0]
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
	files, _ := filepath.Glob(filepath.Join(output, "*.json.gz"))
	if len(files) != 2 {
		t.Errorf("wrote %d files, want 2", len(files))
	}
	if _, err := loadSetupBatches(output); err != nil {
		t.Errorf("no upload manifest: %v", err)
	}
}

func TestUploadDryRunSendsNothing(t *testing.T) {
	claudeHome, codexHome := fixtureHomes(t)
	output := filepath.Join(t.TempDir(), "out")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--source", "claude", "--claude-home", claudeHome, "--codex-home", codexHome,
		"--output", output, "--url", "https://127.0.0.1:1/api", "--token", "secret"}, &stdout, &stderr)
	if code != 0 || stderr.String() != "Prepared 2/2 sessions\n" {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if want := "Dry run: nothing was sent. Request bodies are in " + output + ".\n"; !strings.HasSuffix(stdout.String(), want) {
		t.Fatalf("stdout = %q, want suffix %q", stdout.String(), want)
	}
	if files, _ := filepath.Glob(filepath.Join(output, "*.json.gz")); len(files) == 0 {
		t.Fatal("dry run wrote no request bodies")
	}
	for _, word := range []string{"points", "="} {
		if strings.Contains(stdout.String(), word) {
			t.Errorf("summary uses %q: %q", word, stdout.String())
		}
	}
}

// A dry run attributes sessions to the same email as a send: the Navigara login, not the tool's
// local account.
func TestUploadDryRunUsesLoginEmail(t *testing.T) {
	claudeHome, codexHome := fixtureHomes(t)
	config := `{"oauthAccount":{"emailAddress":"local@example.com"}}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(claudeHome), ".claude.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	setConfigHome(t, filepath.Join(filepath.Dir(claudeHome), "config"))
	t.Setenv("NAVIGARA_TOKEN", "")
	var batches atomic.Int32
	srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if string(mustBody(r)) != "{}" {
			batches.Add(1)
		}
		w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
	}))
	defer srv.Close()
	lines := map[bool]string{}
	for _, dry := range []bool{true, false} {
		args := []string{"sessions", "upload", "--claude-home", claudeHome, "--codex-home", codexHome, "--output", filepath.Join(t.TempDir(), "out")}
		if dry {
			args = append(args, "--dry-run")
		}
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 0 {
			t.Fatalf("dry=%v exit=%d stderr=%q", dry, code, stderr.String())
		}
		lines[dry], _, _ = strings.Cut(stdout.String(), "\n")
		if dry && batches.Load() != 0 {
			t.Fatal("dry run sent batches")
		}
	}
	if want := "Sessions will be attributed to dev@example.com."; lines[true] != want || lines[false] != want {
		t.Fatalf("dry run %q, send %q, want %q", lines[true], lines[false], want)
	}
}

func TestUploadResumeSendsOnlyUnsentBatches(t *testing.T) {
	claudeHome, codexHome := fixtureHomes(t)
	output := filepath.Join(t.TempDir(), "it's $HOME `x`")
	var failLogs atomic.Bool
	failLogs.Store(true)
	var mu sync.Mutex
	var posted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if string(mustBody(r)) == "{}" {
			w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
			return
		}
		if r.URL.Path == "/otlp/v1/logs" && failLogs.Load() {
			// Fail only once the metrics batch is recorded, so the first run sends exactly one batch.
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
				if data, _ := os.ReadFile(filepath.Join(output, sentLog)); strings.Contains(string(data), "metrics") {
					break
				}
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		posted = append(posted, r.URL.Path)
		mu.Unlock()
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"sessions", "upload", "--claude-home", claudeHome, "--codex-home", codexHome,
		"--output", output, "--url", server.URL, "--token", "secret"}, &stdout, &stderr)
	_, cmd, _ := strings.Cut(stderr.String(), "\n  navigara ")
	cmd, ok := strings.CutSuffix(cmd, " --token <Analytics Ingestor key>\n")
	if code != 1 || !ok {
		t.Fatalf("exit=%d stderr=%q, want a resume command", code, stderr.String())
	}
	// The shell must pass every argument through unchanged.
	words, err := exec.Command("sh", "-c", `printf '%s\n' `+cmd).Output()
	if want := strings.Join([]string{"sessions", "upload", "--resume", output, "--url", server.URL}, "\n") + "\n"; err != nil || string(words) != want {
		t.Fatalf("shell read %q (%v), want %q", words, err, want)
	}
	if !reflect.DeepEqual(posted, []string{"/otlp/v1/metrics"}) {
		t.Fatalf("first run posted %v", posted)
	}

	failLogs.Store(false)
	posted = nil
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"sessions", "upload", "--resume", output, "--url", server.URL, "--token", "secret"}, &stdout, &stderr)
	if code != 0 || stdout.String() != "Navigara received 1 upload batches.\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !reflect.DeepEqual(posted, []string{"/otlp/v1/logs"}) {
		t.Fatalf("resume posted %v, want only the unsent logs batch", posted)
	}

	posted = nil
	stdout.Reset()
	if code = run(context.Background(), []string{"sessions", "upload", "--resume", output, "--url", server.URL, "--token", "secret"}, &stdout, &stderr); code != 0 || len(posted) != 0 {
		t.Fatalf("exit=%d posted=%v after every batch was sent", code, posted)
	}
}

func TestUploadProgressLinesWithoutTerminal(t *testing.T) {
	var out bytes.Buffer
	u := &uploadProgress{out: &out}
	u.prepared(1, 4)
	u.prepared(2, 4) // Within a few seconds of the last line.
	u.last = time.Time{}
	u.sent(3, 9)
	u.prepared(4, 4) // The final count prints within the few seconds too.
	u.sent(0, 0)
	u.retry("Upload failed (HTTP 503), retrying")
	if out.String() != "Prepared 1/4 sessions\nSent 3/9 batches\nPrepared 4/4 sessions\nUpload failed (HTTP 503), retrying\n" {
		t.Fatalf("progress = %q", out.String())
	}
}

// A resume checks the frozen destination and organization before any request carries a key.
func TestUploadResumeKeepsTheKeyAwayFromAnotherDestination(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, tc := range []struct{ name, frozenURL, org, want string }{
		{"other destination", "https://frozen.example/api", "", "belong to https://frozen.example/api"},
		{"login organization with a key", server.URL, "org-1", "prepared with navigara login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			body, err := gzipBody([]byte(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			if err := writeRequest(filepath.Join(dir, "logs-000001.json.gz"), body); err != nil {
				t.Fatal(err)
			}
			if err := saveSetupBatches(dir, tc.frozenURL, tc.org); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"sessions", "upload", "--resume", dir, "--url", server.URL, "--token", "secret"}, &stdout, &stderr)
			if code != 1 || !strings.Contains(stderr.String(), tc.want) || requests.Load() != 0 {
				t.Fatalf("exit=%d requests=%d stderr=%q", code, requests.Load(), stderr.String())
			}
		})
	}
}

// Sessions outside the window do not count: when every session in it fails, the run fails and
// removes the empty output directory it made.
func TestUploadFailsWhenEverySessionInTheWindowFails(t *testing.T) {
	claudeHome, codexHome := fixtureHomes(t)
	bad := filepath.Join(claudeHome, "projects", "-repo", "bbbbbbbb-0000-0000-0000-000000000001.jsonl")
	if err := os.WriteFile(bad, []byte("{malformed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "out")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--source", "claude", "--claude-home", claudeHome,
		"--codex-home", codexHome, "--since", "2026-10-02T00:00:00Z", "--output", output}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "Skipped 1 session files") || !strings.Contains(stdout.String(), "Sessions: 0.") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("failed run left its empty output directory: %v", err)
	}
}

func TestExportProgressCountsFilteredSessions(t *testing.T) {
	claudeHome, codexHome := fixtureHomes(t)
	opts := options{
		source:     "claude",
		claudeHome: claudeHome,
		codexHome:  codexHome,
		output:     filepath.Join(t.TempDir(), "out"),
		// Fresh files keep the stale-file skip off, so the window filters each session by its records.
		since: time.Now(),
	}
	var discovered []int
	var processed [][2]int
	var stdout, stderr bytes.Buffer
	code := exportSessionsWithProgress(context.Background(), opts, &stdout, &stderr, func(done, total int) {
		if done == 0 {
			discovered = append(discovered, total)
		}
		processed = append(processed, [2]int{done, total})
	})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !reflect.DeepEqual(discovered, []int{2}) {
		t.Fatalf("discovered = %#v", discovered)
	}
	if !reflect.DeepEqual(processed, [][2]int{{0, 2}, {1, 2}, {2, 2}}) {
		t.Fatalf("processed = %#v", processed)
	}
	if got := stdout.String(); !strings.Contains(got, "Sessions: 0.") {
		t.Fatalf("filtered history changed command output: %q", got)
	}
}

func TestUploadPinnedCodexForkNeedsNoParentRollout(t *testing.T) {
	claudeHome, codexHome := fixtureHomes(t)
	child := filepath.Join(codexHome, "sessions", "rollout-child.jsonl")
	parentHeader := map[string]any{"timestamp": "2026-10-03T09:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "parent"}}
	body := map[string]any{"timestamp": "2026-10-03T09:00:01Z", "type": "turn_context", "ordinal": 1, "payload": map[string]any{"model": "parent-model"}}
	writeRollout := func(path string, rows ...map[string]any) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		var lines []string
		for _, row := range rows {
			encoded, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, string(encoded))
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRollout(child,
		map[string]any{"timestamp": "2026-10-03T10:00:00Z", "type": "session_meta", "payload": map[string]any{"id": "child", "forked_from_id": "parent", "parent_thread_id": "parent", "subagent_history_start_ordinal": 8}},
		parentHeader, body,
	)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--source", "codex", "--email", "dev@example.com",
		"--claude-home", claudeHome, "--codex-home", codexHome, "--session", "child", "--output", filepath.Join(t.TempDir(), "out")}, &stdout, &stderr)
	if code != 0 || noProgress(stderr.String()) != "" {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Sessions: 1. Upload batches: 1. Activity events: 1. Usage measurements: 0.") || strings.Contains(stdout.String(), "parent") {
		t.Fatalf("unexpected export summary: %q", stdout.String())
	}
}

func TestUploadStopsOnRejectedKey(t *testing.T) {
	claudeHome, codexHome := fixtureHomes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"sessions", "upload", "--claude-home", claudeHome, "--codex-home", codexHome,
		"--output", filepath.Join(t.TempDir(), "out"), "--send", "--url", server.URL, "--token", "bad"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "Analytics Ingestor role") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
}

func TestUploadNeedsAnIdentity(t *testing.T) {
	withConfigHome(t)
	root := t.TempDir()
	for _, home := range []string{".claude", ".codex"} {
		if err := os.MkdirAll(filepath.Join(root, home), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--claude-home", filepath.Join(root, ".claude"), "--codex-home", filepath.Join(root, ".codex"),
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
		code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--source", "claude", "--email", "dev@example.com",
			"--claude-home", filepath.Join(root, "missing"), "--codex-home", filepath.Join(root, "codex"), "--output", output}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "missing") {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("output created after failed discovery: %v", err)
		}
	})

	t.Run("only unreadable transcripts", func(t *testing.T) {
		claudeHome, codexHome := fixtureHomes(t)
		var bad []string
		for _, id := range []string{"1", "2"} {
			path := filepath.Join(claudeHome, "projects", "-repo", "aaaaaaaa-0000-0000-0000-00000000000"+id+".jsonl")
			if err := os.WriteFile(path, []byte("{malformed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			bad = append(bad, path)
		}
		if err := os.Chmod(bad[1], 0); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(t.TempDir(), "out")
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--source", "claude", "--claude-home", claudeHome,
			"--codex-home", codexHome, "--output", output}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "Skipped 2 session files that could not be read:") ||
			!strings.Contains(stderr.String(), bad[0]) || !strings.Contains(stderr.String(), bad[1]) {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("output created after failed discovery: %v", err)
		}
	})
}

func TestUploadSkipsUnreadableTranscripts(t *testing.T) {
	claudeHome, codexHome := fixtureHomes(t)
	claudeHome, _ = filepath.EvalSymlinks(claudeHome)
	malformed := filepath.Join(claudeHome, "projects", "-repo", "aaaaaaaa-0000-0000-0000-000000000001.jsonl")
	if err := os.WriteFile(malformed, []byte("{malformed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadable := filepath.Join(claudeHome, "projects", "-repo", "aaaaaaaa-0000-0000-0000-000000000003.jsonl")
	if err := os.WriteFile(unreadable, nil, 0); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--source", "claude", "--claude-home", claudeHome,
		"--codex-home", codexHome, "--output", filepath.Join(t.TempDir(), "out")}, &stdout, &stderr)
	report := fmt.Sprintf("Skipped 2 session files that could not be read:\n  %q: \"line 1: invalid JSON record\"\n  %q: \"permission denied\"\n", malformed, unreadable)
	if code != 0 || noProgress(stderr.String()) != report || !strings.Contains(stdout.String(), "Sessions: 1.") {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want report %q", code, stdout.String(), stderr.String(), report)
	}
}

func TestUploadRejectsInvalidCodexDiscovery(t *testing.T) {
	t.Run("requested before identity eligibility", func(t *testing.T) {
		claudeHome, codexHome := fixtureHomes(t)
		if err := os.RemoveAll(codexHome); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(t.TempDir(), "out")
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--source", "all", "--claude-home", claudeHome,
			"--codex-home", codexHome, "--output", output}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "Codex home") {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("output created after failed discovery: %v", err)
		}
	})

	t.Run("missing explicit home", func(t *testing.T) {
		root := t.TempDir()
		missing := filepath.Join(root, "missing")
		output := filepath.Join(root, "out")
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--source", "codex", "--email", "dev@example.com",
			"--claude-home", filepath.Join(root, "claude"), "--codex-home", missing, "--output", output}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "missing") {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
		if _, err := os.Stat(output); !os.IsNotExist(err) {
			t.Fatalf("output created after failed discovery: %v", err)
		}
	})

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
		code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--source", "codex", "--email", "dev@example.com",
			"--claude-home", claudeHome, "--codex-home", codexHome, "--output", output}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "invalid Codex rollout header") || !strings.Contains(stderr.String(), rollout) {
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
		code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--source", "codex", "--email", "dev@example.com",
			"--claude-home", claudeHome, "--codex-home", codexHome, "--output", filepath.Join(t.TempDir(), "out")}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "not a directory") {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
	})
}

func TestUploadSkipsBuildFailuresAndSendsOtherSessions(t *testing.T) {
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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
			}))
			defer server.Close()
			args := []string{"sessions", "upload", "--source", "codex", "--email", "dev@example.com", "--claude-home", claudeHome,
				"--codex-home", codexHome, "--output", filepath.Join(t.TempDir(), "out"), "--url", server.URL, "--token", "secret"}
			if !send {
				args = append(args, "--dry-run")
			}
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), args, &stdout, &stderr)
			codexHome, _ = filepath.EvalSymlinks(codexHome)
			bad := filepath.Join(codexHome, "sessions", "rollout-bad.jsonl")
			if code != 0 || (send && requests.Load() == 0) || (!send && requests.Load() != 0) || !strings.Contains(stdout.String(), "Sessions: 1.") ||
				!strings.Contains(stderr.String(), "Skipped 1 session files that could not be read:\n  \""+bad+"\": line 2") {
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
	code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--claude-home", claudeHome, "--codex-home", codexHome,
		"--output", filepath.Join(t.TempDir(), "out")}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stderr.String(), "skipping Codex") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "codex-1") || !strings.Contains(stdout.String(), "claude_code") {
		t.Errorf("stdout = %q; want Claude Code sessions only", stdout.String())
	}
}

func TestCommitIndexMatchesAuthorEmailInAnyCase(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_EMAIL=Dev.Name+x@Example.com", "GIT_COMMITTER_EMAIL=dev@example.com",
			"GIT_AUTHOR_NAME=Dev", "GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_DATE=2026-10-01T10:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "one")
	idx := &commitIndex{email: "dev.name+x@example.com", byRepo: map[string][]commit{}}
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	if got := idx.between(repo, at.Add(-time.Second), at.Add(time.Second)); len(got) != 1 {
		t.Fatalf("commits = %v, want the one commit whose author email differs only in case", got)
	}
	for _, email := range []string{"devxname+x@example.com", "name+x@example.com"} {
		other := &commitIndex{email: email, byRepo: map[string][]commit{}}
		if got := other.between(repo, at.Add(-time.Second), at.Add(time.Second)); len(got) != 0 {
			t.Fatalf("%s matched %v; only the whole author email counts", email, got)
		}
	}
}

func TestCommitIndexKeepsTheAmendedCommit(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_EMAIL=dev@example.com", "GIT_COMMITTER_EMAIL=dev@example.com",
			"GIT_AUTHOR_NAME=Dev", "GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_DATE=2026-10-01T10:00:00Z")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "fix")
	git("commit", "-q", "--allow-empty", "--amend", "--no-edit", "--date=2026-10-01T10:00:00Z")
	head := git("rev-parse", "HEAD")
	idx := &commitIndex{email: "dev@example.com", byRepo: map[string][]commit{}}
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	got := idx.between(repo, at.Add(-time.Second), at.Add(time.Second))
	reachable := 0
	for _, c := range got {
		if c.Reachable {
			reachable++
			if c.SHA != head {
				t.Errorf("reachable commit %s, want the amended head %s", c.SHA, head)
			}
		}
	}
	if len(got) != 2 || reachable != 1 {
		t.Fatalf("commits = %+v; want the replaced commit and the amended head, only the head reachable", got)
	}
}

func TestCommitIndexKeepsADeletedBranchCommit(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_EMAIL=dev@example.com", "GIT_COMMITTER_EMAIL=dev@example.com",
			"GIT_AUTHOR_NAME=Dev", "GIT_COMMITTER_NAME=Dev", "GIT_COMMITTER_DATE=2026-10-01T10:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "base", "--date=2026-09-01T10:00:00Z")
	git("checkout", "-q", "-b", "feature")
	git("commit", "-q", "--allow-empty", "-m", "work")
	git("checkout", "-q", "main")
	git("branch", "-q", "-D", "feature")
	idx := &commitIndex{email: "dev@example.com", byRepo: map[string][]commit{}}
	at := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	got := idx.between(repo, at.Add(-time.Second), at.Add(time.Second))
	var work *telemetry.Commit
	for i := range got {
		if got[i].Subject == "work" {
			work = &got[i]
		}
	}
	if work == nil || work.Reachable {
		t.Fatalf("commits = %+v, want the deleted branch's commit, unreachable", got)
	}
}

func TestUploadIgnoresUnselectedExplicitHome(t *testing.T) {
	claudeHome, _ := fixtureHomes(t)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--source", "claude", "--claude-home", claudeHome,
		"--codex-home", filepath.Join(t.TempDir(), "missing"), "--output", filepath.Join(t.TempDir(), "out")}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "claude_code") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestUploadUntilExcludesFutureSessions(t *testing.T) {
	for _, source := range []string{"claude", "codex"} {
		t.Run(source, func(t *testing.T) {
			claudeHome, codexHome := fixtureHomes(t)
			wantIncluded := 2
			if source == "codex" {
				wantIncluded = 1
				for id, day := range map[string]string{"included": "01", "future": "03"} {
					path := filepath.Join(codexHome, "sessions", "rollout-"+id+".jsonl")
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					data := fmt.Sprintf(`{"timestamp":"2026-10-%sT10:00:00Z","type":"session_meta","payload":{"id":"%s"}}`+"\n", day, id)
					if err := os.WriteFile(path, []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				path := filepath.Join(claudeHome, "projects", "-repo", "aaaaaaaa-0000-0000-0000-000000000003.jsonl")
				data := `{"type":"user","sessionId":"aaaaaaaa-0000-0000-0000-000000000003","timestamp":"2026-10-03T10:00:00Z","promptId":"future","message":{"role":"user","content":"future"}}` + "\n"
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, window := range []struct {
				until string
				want  int
			}{{"2026-09-01T00:00:00Z", 0}, {"2026-10-02T00:00:00Z", wantIncluded}} {
				t.Run(window.until, func(t *testing.T) {
					out := filepath.Join(t.TempDir(), "out")
					var stdout, stderr bytes.Buffer
					code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--source", source, "--email", "dev@example.com", "--claude-home", claudeHome, "--codex-home", codexHome, "--until", window.until, "--output", out}, &stdout, &stderr)
					if code != 0 || !strings.Contains(stdout.String(), fmt.Sprintf("Sessions: %d.", window.want)) || noProgress(stderr.String()) != "" {
						t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
					}
					files, err := filepath.Glob(filepath.Join(out, "*.json.gz"))
					if err != nil || (len(files) == 0) != (window.want == 0) {
						t.Fatalf("files=%v err=%v", files, err)
					}
					for _, path := range files {
						zr, err := gzip.NewReader(bytes.NewReader(mustRead(t, path)))
						if err != nil {
							t.Fatal(err)
						}
						body, err := io.ReadAll(zr)
						if err != nil {
							t.Fatal(err)
						}
						if bytes.Contains(body, []byte("future")) || bytes.Contains(body, []byte("000000000003")) {
							t.Fatalf("future session exported in %s", path)
						}
					}
				})
			}
		})
	}
}

func TestUploadSkipsHistoryThatEndsBeforeSince(t *testing.T) {
	root := t.TempDir()
	claudeHome, codexHome := filepath.Join(root, ".claude"), filepath.Join(root, ".codex")
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	write := func(path string, at time.Time, lines ...string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	claudeRow := func(id, at string) string {
		return `{"type":"user","sessionId":"` + id + `","promptId":"p-` + id + `","timestamp":"` + at + `","message":{"role":"user","content":"hi"}}`
	}
	codexMeta := func(id, at string) string {
		return `{"type":"session_meta","timestamp":"` + at + `","payload":{"id":"` + id + `","timestamp":"` + at + `","cli_version":"0.139.0","source":"exec"}}`
	}
	upload := func(since string) (int, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--email", "dev@example.com", "--claude-home", claudeHome, "--codex-home", codexHome,
			"--since", since, "--output", filepath.Join(t.TempDir(), "out")}, &stdout, &stderr)
		return code, stdout.String() + stderr.String()
	}

	// Old histories hold an invalid row: reading them would report them as skipped.
	dir := filepath.Join(claudeHome, "projects", "-repo")
	write(filepath.Join(dir, "c-old.jsonl"), old, claudeRow("c-old", "2025-01-01T00:00:00Z"), `not json`, claudeRow("c-old", "2025-01-01T00:00:01Z"))
	write(filepath.Join(codexHome, "sessions", "rollout-x-old.jsonl"), old, codexMeta("x-old", "2025-01-01T00:00:00Z"), `not json`,
		`{"type":"event_msg","timestamp":"2025-01-01T00:00:01Z","payload":{}}`)
	// A file system clock behind the tool's: the write time is old, the records are not.
	write(filepath.Join(dir, "c-skew.jsonl"), old, claudeRow("c-skew", "2026-10-01T10:00:00Z"))
	write(filepath.Join(codexHome, "sessions", "rollout-x-skew.jsonl"), old, codexMeta("x-skew", "2026-10-01T10:00:00Z"))
	// A resumed process whose only new row is its cost-state.
	write(filepath.Join(dir, "c-cost.jsonl"), old, claudeRow("c-cost", "2025-01-01T00:00:00Z"),
		`{"type":"cost-state","totalCostUSD":1,"startTime":1790000000000,"modelUsage":{"claude-opus-5-5":{"costUSD":1}}}`)
	// An old root whose subagent is still writing.
	write(filepath.Join(dir, "c-sub.jsonl"), old, claudeRow("c-sub", "2025-01-01T00:00:00Z"))
	write(filepath.Join(dir, "c-sub", "subagents", "agent-a.jsonl"), time.Now(),
		`{"type":"user","sessionId":"c-sub","isSidechain":true,"timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":"x"}}`)

	code, got := upload("2026-09-01T00:00:00Z")
	if code != 0 || !strings.Contains(got, "Sessions: 4.") {
		t.Fatalf("exit=%d output=%q; want the skewed, cost-state and subagent sessions only", code, got)
	}
	for _, id := range []string{"claude_code c-cost", "claude_code c-skew", "claude_code c-sub", "codex x-skew"} {
		if !strings.Contains(got, id) {
			t.Errorf("missing %s in %q", id, got)
		}
	}
	if _, got := upload("2024-01-01T00:00:00Z"); !strings.Contains(got, "Skipped 2 session files") {
		t.Fatalf("read nothing old: %q", got)
	}
}

func TestStaleKeepsFilesWrittenJustBeforeSince(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		written time.Time
		want    bool
	}{{since.Add(-time.Second), false}, {since.Add(-staleMargin + time.Millisecond), false}, {since.Add(-2 * staleMargin), true}} {
		row := `{"timestamp":"` + tc.written.Format(time.RFC3339Nano) + `"}` + "\n"
		if err := os.WriteFile(path, []byte(row), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, tc.written, tc.written); err != nil {
			t.Fatal(err)
		}
		if got := stale(since, path); got != tc.want {
			t.Errorf("written %v before since: stale=%v, want %v", since.Sub(tc.written), got, tc.want)
		}
	}
}

func TestExportReportsInterruptAfterLastSession(t *testing.T) {
	claudeHome, codexHome := fixtureHomes(t)
	opts := options{source: "all", claudeHome: claudeHome, codexHome: codexHome, email: "dev@example.com",
		output: filepath.Join(t.TempDir(), "out")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr bytes.Buffer
	code := exportSessionsWithProgress(ctx, opts, &stdout, &stderr, func(done, total int) {
		if total > 0 && done == total {
			cancel() // Interrupt while the final batches are still pending.
		}
	})
	if code != exitInterrupted || !strings.Contains(stderr.String(), "interrupted") {
		t.Fatalf("exit=%d stderr=%q, want an interrupted export", code, stderr.String())
	}
}

func TestUploadDryRunNeedsNoUsableLogin(t *testing.T) {
	offline := httptest.NewServer(http.NotFoundHandler())
	offline.Close()
	for _, mode := range []string{"env token", "expired session", "auth server offline"} {
		t.Run(mode, func(t *testing.T) {
			claudeHome, codexHome := fixtureHomes(t)
			setConfigHome(t, filepath.Join(filepath.Dir(claudeHome), "config"))
			t.Setenv("NAVIGARA_TOKEN", "env-secret")
			if mode != "env token" {
				expires := time.Now().Add(time.Hour)
				if mode == "expired session" {
					expires = time.Now().Add(-time.Hour)
				}
				if err := saveAccount(accountConfig{Host: offline.URL, SessionToken: "session-secret", SessionExpiresAt: expires,
					UserID: "user-1", Email: "login@example.com", OrganizationID: "org-1"}); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"sessions", "upload", "--dry-run", "--source", "claude", "--claude-home", claudeHome,
				"--codex-home", codexHome, "--output", filepath.Join(t.TempDir(), "out")}, &stdout, &stderr)
			if code != 0 || !strings.HasPrefix(stdout.String(), "Sessions will be attributed to dev@example.com.\n") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if note := strings.Contains(stderr.String(), "Saved login could not be checked"); note != (mode != "env token") {
				t.Fatalf("stderr=%q", stderr.String())
			}
		})
	}
}

var progressLine = regexp.MustCompile(`(?m)^(Prepared \d+/\d+ sessions|Sent \d+/\d+ batches)\n`)

// noProgress drops the progress lines that an upload without a terminal prints.
func noProgress(s string) string { return progressLine.ReplaceAllString(s, "") }
