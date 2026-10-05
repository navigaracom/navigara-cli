package main

import (
	"bytes"
	"encoding/json"
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
		name, source, format, summary string
		code                          int
		ids                           []string
	}{
		{"normal", "all", "json", "discovered=4 emitted=2 duplicates=0 skipped=2 failed=0 complete", 0, []string{"claude_code-root", "codex-root"}},
		{"text", "claude", "text", "discovered=2 emitted=1 duplicates=0 skipped=1 failed=0 complete", 0, []string{"claude_code-root"}},
		{"codex filter", "codex", "json", "discovered=2 emitted=1 duplicates=0 skipped=1 failed=0 complete", 0, []string{"codex-root"}},
		{"duplicate", "all", "json", "discovered=5 emitted=2 duplicates=1 skipped=2 failed=0 complete", 0, []string{"claude_code-root", "codex-root"}},
		{"three-copy conflict", "all", "json", "discovered=6 emitted=1 duplicates=0 skipped=2 failed=3 incomplete", 1, []string{"codex-root"}},
		{"same ID across sources", "all", "json", "discovered=4 emitted=2 duplicates=0 skipped=2 failed=0 complete", 0, []string{"claude_code-root", "claude_code-root"}},
		{"malformed", "all", "json", "discovered=5 emitted=2 duplicates=0 skipped=2 failed=1 incomplete", 1, []string{"claude_code-root", "codex-root"}},
		{"empty", "all", "json", "discovered=0 emitted=0 duplicates=0 skipped=0 failed=0 complete", 0, []string{}},
		{"missing root", "all", "json", "discovered=0 emitted=0 duplicates=0 skipped=0 failed=0 incomplete", 1, []string{}},
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
				cmd := exec.Command(binary, "sessions", "upload", "--dry-run", "--source", tt.source, "--format", tt.format, "--output", output, "--claude-home", claude, "--codex-home", codex)
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
				data, err := os.ReadFile(output)
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(output)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("permissions: %v %v", info, err)
				}
				var envelope session.Envelope
				if err := json.Unmarshal(data, &envelope); err != nil {
					t.Fatal(err)
				}
				if envelope.SchemaVersion != 1 || envelope.Sessions == nil || len(envelope.Sessions) != len(tt.ids) {
					t.Fatalf("envelope=%s", data)
				}
				for i, id := range tt.ids {
					s := envelope.Sessions[i]
					source := "codex"
					if id == "claude_code-root" && i == 0 {
						source = "claude_code"
					}
					if s.SessionID != id || s.Source != source {
						t.Fatalf("session=%+v", s)
					}
					version := "0.139.0"
					if s.Source == "claude_code" {
						version = "2.1.285"
					}
					if s.FirstObservedAt == nil || s.LastObservedAt == nil || s.FirstObservedAt.Format(time.RFC3339) != "2026-01-02T00:00:00Z" || s.LastObservedAt.Format(time.RFC3339) != "2026-01-02T00:02:00Z" || s.SourceVersion != version {
						t.Fatalf("missing fixture metadata: %+v", s)
					}
				}
				if bytes.Contains(data, []byte("/workspace")) || bytes.Contains(data, []byte(home)) || !bytes.HasSuffix(data, []byte("\n")) {
					t.Fatalf("unexpected payload=%s", data)
				}
				if previous != nil && !bytes.Equal(previous, data) {
					t.Fatal("unchanged input produced different bytes")
				}
				previous = data
				if tt.format == "json" {
					if !bytes.Equal(stdout.Bytes(), data) || !strings.Contains(stderr.String(), tt.summary) {
						t.Fatalf("stdout/file mismatch or summary missing: %q", stderr.String())
					}
				} else {
					if !strings.Contains(stdout.String(), tt.summary) || !strings.Contains(stdout.String(), `claude_code "claude_code-root"`) || stderr.Len() != 0 {
						t.Fatalf("text=%q stderr=%q", stdout.String(), stderr.String())
					}
				}
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
	for _, format := range []string{"json", "text"} {
		home := t.TempDir()
		output := filepath.Join(home, "request.json")
		var stderr bytes.Buffer
		if code := run([]string{"sessions", "upload", "--dry-run", "--format", format, "--output", output, "--claude-home", home, "--codex-home", home}, shortWriter{}, &stderr); code != 1 || !strings.Contains(stderr.String(), io.ErrShortWrite.Error()) {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
		if data, err := os.ReadFile(output); err != nil || !json.Valid(data) {
			t.Fatalf("complete file missing: %q %v", data, err)
		}
	}
}
