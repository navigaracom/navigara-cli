package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/navigaracom/navigara-cli/internal/session"
)

func TestUploadBinary(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "navigara")
	if data, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, data)
	}
	for _, tt := range []struct {
		name, source, summary string
		code                  int
		ids                   []string
	}{
		{"normal", "all", "discovered=4 sessions=2 duplicates=0 skipped=2 failed=0 complete", 0, []string{"claude_code-root", "codex-root"}},
		{"text", "claude", "discovered=2 sessions=1 duplicates=0 skipped=1 failed=0 complete", 0, []string{"claude_code-root"}},
		{"codex filter", "codex", "discovered=2 sessions=1 duplicates=0 skipped=1 failed=0 complete", 0, []string{"codex-root"}},
		{"duplicate", "all", "discovered=5 sessions=2 duplicates=1 skipped=2 failed=0 complete", 0, []string{"claude_code-root", "codex-root"}},
		{"three-copy conflict", "all", "discovered=6 sessions=1 duplicates=0 skipped=2 failed=3 incomplete", 1, []string{"codex-root"}},
		{"same ID across sources", "all", "discovered=4 sessions=2 duplicates=0 skipped=2 failed=0 complete", 0, []string{"claude_code-root", "claude_code-root"}},
		{"malformed", "all", "discovered=5 sessions=2 duplicates=0 skipped=2 failed=1 incomplete", 1, []string{"claude_code-root", "codex-root"}},
		{"empty", "all", "discovered=0 sessions=0 duplicates=0 skipped=0 failed=0 complete", 0, []string{}},
		{"missing root", "all", "discovered=0 sessions=0 duplicates=0 skipped=0 failed=0 incomplete", 1, []string{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			claude, codex := filepath.Join(home, "claude"), filepath.Join(home, "codex")
			if tt.name != "missing root" {
				for _, dir := range []string{claude, codex} {
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			if tt.name != "empty" && tt.name != "missing root" {
				for _, name := range []string{"claude-root", "claude-child", "codex-root", "codex-child"} {
					dir := filepath.Join(codex, "sessions")
					if strings.HasPrefix(name, "claude") {
						dir = filepath.Join(claude, "projects", "example")
					}
					data, err := os.ReadFile(filepath.Join("internal/session/testdata", name+".jsonl"))
					if err != nil {
						t.Fatal(err)
					}
					if tt.name == "same ID across sources" && name == "codex-root" {
						data = bytes.ReplaceAll(data, []byte("codex-root"), []byte("claude_code-root"))
					}
					putHistory(t, filepath.Join(dir, name+".jsonl"), data)
					if name == "claude-root" && (tt.name == "duplicate" || tt.name == "three-copy conflict") {
						putHistory(t, filepath.Join(dir, "copy.jsonl"), bytes.ReplaceAll(data, []byte("/workspace/example"), []byte("/another/cwd")))
						if tt.name == "three-copy conflict" {
							putHistory(t, filepath.Join(dir, "conflict.jsonl"), bytes.ReplaceAll(data, []byte("2.1.285"), []byte("changed")))
						}
					}
				}
				if tt.name == "malformed" {
					putHistory(t, filepath.Join(codex, "sessions", "bad.jsonl"), []byte("{bad\n"))
				}
			}
			var previous []byte
			for _, filename := range []string{"first.json", "second.json"} {
				output := filepath.Join(home, filename)
				cmd := exec.Command(binary, "sessions", "upload", "--dry-run", "--source", tt.source, "--output", output, "--claude-home", claude, "--codex-home", codex)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				err := cmd.Run()
				code := 0
				if err != nil {
					if exit, ok := err.(*exec.ExitError); ok {
						code = exit.ExitCode()
					} else {
						t.Fatal(err)
					}
				}
				if code != tt.code {
					t.Fatalf("exit=%d want=%d stderr=%q", code, tt.code, stderr.String())
				}
				info, err := os.Stat(output)
				if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
					t.Fatalf("directory permissions: %v %v", info, err)
				}
				files, err := os.ReadDir(output)
				if err != nil || len(files) != 0 {
					t.Fatalf("sessions without usage produced files: %v %v", files, err)
				}
				if !strings.Contains(stdout.String(), tt.summary) || !strings.Contains(stdout.String(), "points=0 batches=0") {
					t.Fatalf("summary=%q stderr=%q", stdout.String(), stderr.String())
				}
				for _, id := range tt.ids {
					if !strings.Contains(stdout.String(), fmt.Sprintf("%q", id)) {
						t.Fatalf("missing ID %q in %q", id, stdout.String())
					}
				}
				if previous != nil && !bytes.Equal(previous, stdout.Bytes()) {
					t.Fatal("unchanged input produced different summary")
				}
				previous = append([]byte(nil), stdout.Bytes()...)
				if tt.name == "three-copy conflict" && !strings.Contains(stderr.String(), `conflicting metadata for "claude_code" "claude_code-root"`) {
					t.Fatalf("missing conflict diagnosis: %q", stderr.String())
				}
			}
		})
	}
}

func putHistory(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestOutputRefusals(t *testing.T) {
	for _, kind := range []string{"existing", "dangling symlink", "missing parent"} {
		t.Run(kind, func(t *testing.T) {
			home := t.TempDir()
			output := filepath.Join(home, "request.json")
			if kind == "existing" {
				if err := os.WriteFile(output, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "dangling symlink" {
				if err := os.Symlink("absent", output); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "missing parent" {
				output = filepath.Join(home, "absent", "request.json")
			}
			var stdout, stderr bytes.Buffer
			code := run([]string{"sessions", "upload", "--dry-run", "--output", output, "--claude-home", home, "--codex-home", home}, &stdout, &stderr)
			if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "output") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if kind == "existing" {
				if data, err := os.ReadFile(output); err != nil || string(data) != "preserve" {
					t.Fatalf("existing file changed: %q %v", data, err)
				}
			}
			if kind == "dangling symlink" {
				if target, err := os.Readlink(output); err != nil || target != "absent" {
					t.Fatalf("symlink changed: %q %v", target, err)
				}
			}
			if kind == "missing parent" {
				if _, err := os.Stat(filepath.Dir(output)); !os.IsNotExist(err) {
					t.Fatalf("parent created: %v", err)
				}
			}
		})
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestStdoutShortWritePreservesCompleteOutput(t *testing.T) {
	home := t.TempDir()
	output := filepath.Join(home, "output")
	var stderr bytes.Buffer
	if code := run([]string{"sessions", "upload", "--output", output, "--claude-home", home, "--codex-home", home}, shortWriter{}, &stderr); code != 1 || !strings.Contains(stderr.String(), io.ErrShortWrite.Error()) {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if info, err := os.Stat(output); err != nil || !info.IsDir() {
		t.Fatalf("complete output missing: %v %v", info, err)
	}
}

func TestWriteUploadNativeUsage(t *testing.T) {
	home := t.TempDir()
	claude, codex := filepath.Join(home, "claude"), filepath.Join(home, "codex")
	for _, source := range []string{"claude", "codex"} {
		data, err := os.ReadFile("internal/session/testdata/" + source + "-usage.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(codex, "sessions")
		if source == "claude" {
			dir = filepath.Join(claude, "projects", "example")
		}
		putHistory(t, filepath.Join(dir, source+".jsonl"), data)
	}
	path := filepath.Join(t.TempDir(), "output")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"sessions", "upload", "--output", path, "--claude-home", claude, "--codex-home", codex}, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "points=10 batches=2 mapped=2 repeated=1 ambiguous=0 unmapped=1") {
		t.Fatalf("summary=%q", stdout.String())
	}
	for i, want := range [][]int64{{2, 348, 27495, 42816}, {38558, 289, 28928, 0, 7, 38847}} {
		file := filepath.Join(path, fmt.Sprintf("metrics-%06d.json", i+1))
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("permissions: %v %v", info, err)
		}
		var body struct {
			ResourceMetrics []struct {
				ScopeMetrics []struct {
					Metrics []struct {
						Name           string
						Sum, Histogram struct {
							DataPoints []struct {
								AsInt        string
								Sum          int64
								TimeUnixNano string
							}
						}
					}
				}
			}
		}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		metric := body.ResourceMetrics[0].ScopeMetrics[0].Metrics[0]
		points := metric.Sum.DataPoints
		name := "claude_code.token.usage"
		if i == 1 {
			points = metric.Histogram.DataPoints
			name = "codex.turn.token_usage"
		}
		if metric.Name != name || len(points) != len(want) {
			t.Fatalf("metric=%+v", metric)
		}
		for j, point := range points {
			if point.TimeUnixNano == "" || (i == 0 && point.AsInt != fmt.Sprint(want[j])) || (i == 1 && point.Sum != want[j]) {
				t.Fatalf("point=%+v want=%d", point, want[j])
			}
		}
		if bytes.Contains(data, []byte("schema_version")) || bytes.Contains(data, []byte("/workspace")) || bytes.Contains(data, []byte("message_id")) {
			t.Fatalf("unexpected payload=%s", data)
		}
	}
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 2 {
		t.Fatalf("files=%v err=%v", entries, err)
	}
}

func TestWriteUploadCompletePayloadDuplicates(t *testing.T) {
	zero, one := int64(0), int64(1)
	observed := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	offset := observed.In(time.FixedZone("recorded", 3600))
	for _, tt := range []struct {
		name         string
		first, other []session.UsageObservation
		conflict     bool
	}{
		{"same usage", []session.UsageObservation{{RecordLine: 2, MessageUsage: &session.ClaudeUsage{InputTokens: &zero}}}, []session.UsageObservation{{RecordLine: 2, MessageUsage: &session.ClaudeUsage{InputTokens: &zero}}}, false},
		{"omitted empty", nil, []session.UsageObservation{}, false},
		{"different usage", []session.UsageObservation{{RecordLine: 2, MessageUsage: &session.ClaudeUsage{InputTokens: &zero}}}, []session.UsageObservation{{RecordLine: 2, MessageUsage: &session.ClaudeUsage{InputTokens: &one}}}, true},
		{"unknown versus zero", []session.UsageObservation{{RecordLine: 2, MessageUsage: &session.ClaudeUsage{}}}, []session.UsageObservation{{RecordLine: 2, MessageUsage: &session.ClaudeUsage{InputTokens: &zero}}}, true},
		{"serialized timestamp", []session.UsageObservation{{RecordLine: 2, ObservedAt: &observed}}, []session.UsageObservation{{RecordLine: 2, ObservedAt: &offset}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			first := session.Session{Source: "claude_code", SessionID: "same", UsageObservations: tt.first}
			other := first
			other.UsageObservations = tt.other
			result := session.Result{Files: 4, Records: []session.Record{{Session: first}, {Session: other}, {Session: first}, {Session: session.Session{Source: "codex", SessionID: "valid"}}}}
			path := filepath.Join(t.TempDir(), "request.json")
			var stdout, stderr bytes.Buffer
			code := writeUpload(options{output: path}, result, &stdout, &stderr)
			wantCode, wantEmitted, summary := 0, 2, "discovered=4 sessions=2 duplicates=2 skipped=0 failed=0 complete"
			if tt.conflict {
				wantCode, wantEmitted, summary = 1, 1, "discovered=4 sessions=1 duplicates=0 skipped=0 failed=3 incomplete"
			}
			if code != wantCode || !strings.Contains(stdout.String(), summary) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if strings.Count(stdout.String(), "\n") != wantEmitted+2 || (tt.conflict && strings.Contains(stdout.String(), `"same"`)) {
				t.Fatalf("wrong session inventory: %q", stdout.String())
			}

		})
	}
}

func TestWriteUploadSerializationFailureCreatesNoOutput(t *testing.T) {
	invalid := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprintf("duplicate=%t", duplicate), func(t *testing.T) {
			bad := session.Session{Source: "codex", SessionID: "same", UsageObservations: []session.UsageObservation{{RecordLine: 2, ObservedAt: &invalid}}}
			records := []session.Record{{Session: bad}}
			if duplicate {
				records = []session.Record{{Session: session.Session{Source: "codex", SessionID: "same"}}, {Session: bad}}
			}
			result := session.Result{Files: len(records), Records: records}
			path := filepath.Join(t.TempDir(), "request.json")
			var stdout, stderr bytes.Buffer
			if code := writeUpload(options{output: path}, result, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "year outside") {
				t.Fatalf("exit=%d stdout=%s stderr=%q", code, stdout.Bytes(), stderr.String())
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("output created: %v", err)
			}
		})
	}
}

func TestWriteUploadRepositoryContextConflicts(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		first := session.Session{Source: "codex", SessionID: "same", RepositoryObservations: []session.RepositoryObservation{{RecordLine: 1, Branch: "main", CommitHash: "recorded", RepositoryURL: "host/org/repo"}}}
		other := first
		if conflict {
			other.RepositoryObservations = []session.RepositoryObservation{{RecordLine: 1, Branch: "changed", CommitHash: "recorded", RepositoryURL: "host/org/repo"}}
		}
		result := session.Result{Files: 4, Records: []session.Record{{Session: first}, {Session: other}, {Session: first}, {Session: session.Session{Source: "codex", SessionID: "valid"}}}}
		path := filepath.Join(t.TempDir(), "request.json")
		var stdout, stderr bytes.Buffer
		code := writeUpload(options{output: path}, result, &stdout, &stderr)
		wantCode, wantCount, summary := 0, 2, "duplicates=2 skipped=0 failed=0 complete"
		if conflict {
			wantCode, wantCount, summary = 1, 1, "duplicates=0 skipped=0 failed=3 incomplete"
		}
		if code != wantCode || strings.Count(stdout.String(), "\n") != wantCount+2 || !strings.Contains(stdout.String(), summary) {
			t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout.Bytes(), stderr.String())
		}
		if conflict && strings.Contains(stdout.String(), `"same"`) {
			t.Fatalf("conflicting group emitted: %s", stdout.Bytes())
		}

	}
}

func TestEncodingFailureRetainsCompletedBatches(t *testing.T) {
	result := session.Result{Files: 3}
	for _, source := range []string{"claude_code", "codex"} {
		name := "codex"
		if source == "claude_code" {
			name = "claude"
		}
		data, err := os.ReadFile("internal/session/testdata/" + name + "-usage.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		r, err := session.Read(source, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		result.Records = append(result.Records, r)
	}
	oversized := result.Records[1].Session
	oversized.SessionID = "z-oversized"
	oversized.UsageObservations = append([]session.UsageObservation(nil), oversized.UsageObservations...)
	for i := range oversized.UsageObservations {
		oversized.UsageObservations[i].Model = strings.Repeat("x", 4<<20)
	}
	result.Records = append(result.Records, session.Record{Session: oversized})
	path := filepath.Join(t.TempDir(), "output")
	var stdout, stderr bytes.Buffer
	if code := writeUpload(options{output: path}, result, &stdout, &stderr); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "incomplete output") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	files, err := os.ReadDir(path)
	if err != nil || len(files) != 1 || files[0].Name() != "metrics-000001.json" {
		t.Fatalf("files=%v err=%v", files, err)
	}
	data, err := os.ReadFile(filepath.Join(path, files[0].Name()))
	if err != nil || !json.Valid(data) {
		t.Fatalf("completed batch lost: %v", err)
	}
}
