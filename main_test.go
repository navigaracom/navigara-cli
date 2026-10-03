package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		out  string
		err  string
	}{
		{"root", nil, 0, "Usage: navigara", ""},
		{"root help", []string{"--help"}, 0, "Usage: navigara", ""},
		{"root short help", []string{"-h"}, 0, "Usage: navigara", ""},
		{"version", []string{"--version"}, 0, "navigara dev\n", ""},
		{"sessions", []string{"sessions"}, 0, "Usage: navigara sessions", ""},
		{"sessions help", []string{"sessions", "--help"}, 0, "Usage: navigara sessions", ""},
		{"sessions short help", []string{"sessions", "-h"}, 0, "Usage: navigara sessions", ""},
		{"upload help", []string{"sessions", "upload", "--help"}, 0, "--dry-run", ""},
		{"upload short help", []string{"sessions", "upload", "-h"}, 0, "--dry-run", ""},
		{"upload help positional", []string{"sessions", "upload", "--help", "extra"}, 2, "", "unexpected argument"},
		{"upload help unknown flag", []string{"sessions", "upload", "--help", "--unknown"}, 2, "", "unknown"},
		{"upload help bad source", []string{"sessions", "upload", "--source=other", "--help"}, 2, "", "--source"},
		{"upload help bad format", []string{"sessions", "upload", "-h", "--format=other"}, 2, "", "--format"},
		{"upload help empty output", []string{"sessions", "upload", "--help", "--output="}, 2, "", "--output"},
		{"upload help empty home", []string{"sessions", "upload", "--help", "--codex-home="}, 2, "", "--codex-home"},
		{"unknown command", []string{"other"}, 2, "", "unknown command"},
		{"unknown subcommand", []string{"sessions", "other"}, 2, "", "unknown command"},
		{"unknown root flag", []string{"--unknown"}, 2, "", "unknown command"},
		{"extra root argument", []string{"--version", "extra"}, 2, "", "unexpected argument"},
		{"extra group argument", []string{"sessions", "--help", "extra"}, 2, "", "unexpected argument"},
		{"missing guard", []string{"sessions", "upload"}, 2, "", "--dry-run"},
		{"false guard", []string{"sessions", "upload", "--dry-run=false"}, 2, "", "--dry-run"},
		{"bad bool", []string{"sessions", "upload", "--dry-run=maybe"}, 2, "", "dry-run"},
		{"unknown upload flag", []string{"sessions", "upload", "--unknown"}, 2, "", "unknown"},
		{"bad source", []string{"sessions", "upload", "--dry-run", "--source=other"}, 2, "", "--source"},
		{"bad format", []string{"sessions", "upload", "--dry-run", "--format=other"}, 2, "", "--format"},
		{"missing output", []string{"sessions", "upload", "--output"}, 2, "", "output"},
		{"empty output", []string{"sessions", "upload", "--dry-run", "--output="}, 2, "", "--output"},
		{"empty claude home", []string{"sessions", "upload", "--dry-run", "--claude-home="}, 2, "", "--claude-home"},
		{"empty codex home", []string{"sessions", "upload", "--dry-run", "--codex-home="}, 2, "", "--codex-home"},
		{"positional argument", []string{"sessions", "upload", "--dry-run", "extra"}, 2, "", "unexpected argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(tt.args, &stdout, &stderr); got != tt.code {
				t.Fatalf("exit = %d, want %d; stdout=%q stderr=%q", got, tt.code, stdout.String(), stderr.String())
			}
			if (tt.out == "" && stdout.Len() != 0) || !strings.Contains(stdout.String(), tt.out) {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.out)
			}
			if (tt.err == "" && stderr.Len() != 0) || !strings.Contains(stderr.String(), tt.err) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.err)
			}
		})
	}
}

func TestInvalidUploadDoesNotCreateOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "sessions.json")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"sessions", "upload", "--source=other", "--output", output}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("invalid invocation created output: %v", err)
	}
}
