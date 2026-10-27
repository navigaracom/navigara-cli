package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolatePlatform moves platform state that tests change away from the real
// user. It returns the cleanup.
var isolatePlatform = func(string) func() { return func() {} }

// TestMain keeps setup backups of tests that do not set XDG_CONFIG_HOME out of
// the real home.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "navigara-test-config-*")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	cleanup := isolatePlatform(dir)
	code := m.Run()
	cleanup()
	os.RemoveAll(dir)
	os.Exit(code)
}

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
		{"upload help", []string{"sessions", "upload", "--help"}, 0, "-dry-run", ""},
		{"upload short help", []string{"sessions", "upload", "-h"}, 0, "-send", ""},
		{"upload help positional", []string{"sessions", "upload", "--help", "extra"}, 2, "", "unexpected argument"},
		{"upload help unknown flag", []string{"sessions", "upload", "--help", "--unknown"}, 2, "", "unknown"},
		{"upload help bad source", []string{"sessions", "upload", "--source=other", "--help"}, 2, "", "--source"},
		{"removed format", []string{"sessions", "upload", "-h", "--format=other"}, 2, "", "-format"},
		{"upload help empty output", []string{"sessions", "upload", "--help", "--output="}, 2, "", "--output"},
		{"upload help empty home", []string{"sessions", "upload", "--help", "--codex-home="}, 2, "", "--codex-home"},
		{"unknown command", []string{"other"}, 2, "", "unknown command"},
		{"unknown subcommand", []string{"sessions", "other"}, 2, "", "unknown command"},
		{"unknown root flag", []string{"--unknown"}, 2, "", "unknown command"},
		{"extra root argument", []string{"--version", "extra"}, 2, "", "unexpected argument"},
		{"extra group argument", []string{"sessions", "--help", "extra"}, 2, "", "unexpected argument"},
		{"unknown upload flag", []string{"sessions", "upload", "--unknown"}, 2, "", "unknown"},
		{"bad source", []string{"sessions", "upload", "--source=other"}, 2, "", "--source"},
		{"removed format", []string{"sessions", "upload", "--format=other"}, 2, "", "-format"},
		{"missing output", []string{"sessions", "upload", "--output"}, 2, "", "output"},
		{"empty output", []string{"sessions", "upload", "--output="}, 2, "", "--output"},
		{"empty claude home", []string{"sessions", "upload", "--claude-home="}, 2, "", "--claude-home"},
		{"empty codex home", []string{"sessions", "upload", "--codex-home="}, 2, "", "--codex-home"},
		{"positional argument", []string{"sessions", "upload", "extra"}, 2, "", "unexpected argument"},
		{"bad bool", []string{"sessions", "upload", "--send=maybe"}, 2, "", "send"},
		{"dry run and send", []string{"sessions", "upload", "--dry-run", "--send"}, 2, "", "--dry-run"},
		{"resume with export flag", []string{"sessions", "upload", "--resume", "dir", "--source", "claude"}, 2, "", "--source"},
		{"resume dry run", []string{"sessions", "upload", "--resume", "dir", "--dry-run"}, 2, "", "--dry-run"},
		{"empty resume", []string{"sessions", "upload", "--resume="}, 2, "", "--resume"},
		{"empty session", []string{"sessions", "upload", "--session="}, 2, "", "--session"},
		{"bad until", []string{"sessions", "upload", "--until=yesterday"}, 2, "", "--until"},
		{"bad since", []string{"sessions", "upload", "--since=2026-13-01"}, 2, "", "--since"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(context.Background(), tt.args, &stdout, &stderr); got != tt.code {
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

func TestWizardAndSetupHelpUseSameFlow(t *testing.T) {
	var wizardOut, wizardErr, setupOut, setupErr bytes.Buffer
	if code := run(context.Background(), []string{"wizard", "--help"}, &wizardOut, &wizardErr); code != 0 {
		t.Fatalf("wizard help exit = %d; stderr=%q", code, wizardErr.String())
	}
	if code := run(context.Background(), []string{"setup", "--help"}, &setupOut, &setupErr); code != 0 {
		t.Fatalf("setup help exit = %d; stderr=%q", code, setupErr.String())
	}
	if wizardOut.String() != setupOut.String() || wizardErr.String() != setupErr.String() {
		t.Fatalf("wizard and setup help differ: wizard stdout=%q stderr=%q; setup stdout=%q stderr=%q", wizardOut.String(), wizardErr.String(), setupOut.String(), setupErr.String())
	}
	if !strings.Contains(wizardOut.String(), "Usage: navigara wizard") {
		t.Errorf("wizard help = %q", wizardOut.String())
	}
}

func TestInvalidUploadDoesNotCreateOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "sessions.json")
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"sessions", "upload", "--source=other", "--output", output}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit=%d", code)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("invalid invocation created output: %v", err)
	}
}
