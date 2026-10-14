package main

import (
	"bytes"
	"context"
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
		{"upload help", []string{"sessions", "upload", "--help"}, 0, "-send", ""},
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

func TestUploadTokenDoesNotExposeEnvironmentInHelp(t *testing.T) {
	t.Setenv("NAVIGARA_TOKEN", "dummy-help-token")
	for _, args := range [][]string{
		{"sessions", "upload", "--help"},
		{"sessions", "upload", "-h"},
		{"sessions", "upload", "--token=dummy-flag-token", "--help"},
		{"sessions", "upload", "--token=dummy-flag-token", "-h"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr); code != 0 {
			t.Fatalf("args=%q code=%d stderr=%q", args, code, stderr.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), "dummy-help-token") || strings.Contains(stdout.String()+stderr.String(), "dummy-flag-token") {
			t.Fatalf("args=%q exposed a token: stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
	}
}

func TestUploadEmptyTokenFlagOverridesEnvironment(t *testing.T) {
	t.Setenv("NAVIGARA_TOKEN", "dummy-environment-token")
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"sessions", "upload", "--send", "--token="}, &stdout, &stderr); code != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "explicit upload requires both --url and --token") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}
