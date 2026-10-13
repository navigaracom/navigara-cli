package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolHomesPreferFlagThenEnvThenDefault(t *testing.T) {
	home := t.TempDir()
	for _, tc := range []struct {
		name                  string
		claudeFlag, codexFlag string
		claudeEnv, codexEnv   string
		wantClaude, wantCodex string
	}{
		{"defaults", "", "", "", "", filepath.Join(home, ".claude"), filepath.Join(home, ".codex")},
		{"environment", "", "", "/env/claude", "/env/codex", "/env/claude", "/env/codex"},
		{"flags win", "/flag/claude", "/flag/codex", "/env/claude", "/env/codex", "/flag/claude", "/flag/codex"},
		{"mixed", "/flag/claude", "", "", "/env/codex", "/flag/claude", "/env/codex"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv("CLAUDE_CONFIG_DIR", tc.claudeEnv)
			t.Setenv("CODEX_HOME", tc.codexEnv)
			claude, codex, err := toolHomes(tc.claudeFlag, tc.codexFlag)
			if err != nil || claude != tc.wantClaude || codex != tc.wantCodex {
				t.Fatalf("got %q, %q, %v; want %q, %q", claude, codex, err, tc.wantClaude, tc.wantCodex)
			}
		})
	}
}

func TestUploadReadsClaudeConfigDir(t *testing.T) {
	claudeHome, _ := fixtureHomes(t)
	t.Setenv("HOME", t.TempDir()) // No history in the default home.
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	t.Setenv("CODEX_HOME", "")
	var out, errs bytes.Buffer
	code := run([]string{"sessions", "upload", "--source", "claude", "--email", "dev@example.com",
		"--output", filepath.Join(t.TempDir(), "out")}, &out, &errs)
	if code != 0 || !strings.HasPrefix(out.String(), "sessions=2 ") {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want both sessions from $CLAUDE_CONFIG_DIR", code, out.String(), errs.String())
	}
}

func TestUploadWarnsAboutMissingClaudeConfigDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "missing"))
	var out, errs bytes.Buffer
	code := run([]string{"sessions", "upload", "--source", "claude", "--email", "dev@example.com",
		"--output", filepath.Join(t.TempDir(), "out")}, &out, &errs)
	if code != 0 || !strings.HasPrefix(out.String(), "sessions=0 ") || !strings.Contains(errs.String(), "$CLAUDE_CONFIG_DIR") {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want an empty export with a warning", code, out.String(), errs.String())
	}
}

func TestToolHomesNeedNoHomeDirectoryForExplicitHomes(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "/env/codex")
	claude, codex, err := toolHomes("/flag/claude", "")
	if err != nil || claude != "/flag/claude" || codex != "/env/codex" {
		t.Fatalf("got %q, %q, %v", claude, codex, err)
	}
	t.Setenv("CODEX_HOME", "")
	if claude, codex, err := toolHomes("/flag/claude", ""); err == nil || claude != "/flag/claude" || codex != "" {
		t.Fatalf("got %q, %q, %v; want the flag, an empty unresolved default and an error", claude, codex, err)
	}
}

func TestUploadReportsMissingClaudeConfigDirBeforeEmail(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "missing"))
	var out, errs bytes.Buffer
	code := run([]string{"sessions", "upload", "--source", "claude", "--output", filepath.Join(t.TempDir(), "out")}, &out, &errs)
	if code != 0 || !strings.Contains(errs.String(), "$CLAUDE_CONFIG_DIR") || strings.Contains(errs.String(), "email") {
		t.Fatalf("exit=%d stderr=%q, want the missing home without an email error", code, errs.String())
	}
}

func TestUploadNeedsNoHomeDirectoryForOneExplicitHome(t *testing.T) {
	claudeHome, _ := fixtureHomes(t)
	t.Setenv("HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	var out, errs bytes.Buffer
	code := run([]string{"sessions", "upload", "--source", "claude", "--claude-home", claudeHome, "--email", "dev@example.com",
		"--output", filepath.Join(t.TempDir(), "out")}, &out, &errs)
	if code != 0 || !strings.HasPrefix(out.String(), "sessions=2 ") {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want the Claude sessions without $HOME", code, out.String(), errs.String())
	}
}
