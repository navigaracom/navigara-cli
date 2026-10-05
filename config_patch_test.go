package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckPatchHomesDiscoversExistingTools(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	if err := os.Mkdir(claude, 0700); err != nil {
		t.Fatal(err)
	}
	opts := options{source: "all", claudeHome: claude, codexHome: filepath.Join(root, "missing")}
	if err := checkPatchHomes(&opts, false, false); err != nil {
		t.Fatal(err)
	}
	if opts.source != "claude" {
		t.Fatalf("source = %q, want claude", opts.source)
	}
}

func TestCheckPatchHomesRejectsExplicitMissingHome(t *testing.T) {
	root := t.TempDir()
	err := checkPatchHomes(&options{source: "codex", claudeHome: filepath.Join(root, "claude"), codexHome: filepath.Join(root, "codex")}, false, true)
	if err == nil || !strings.Contains(err.Error(), "Codex home") {
		t.Fatalf("error = %v, want explicit Codex home failure", err)
	}
}
