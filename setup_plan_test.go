package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupPlansBothShellStartupModes(t *testing.T) {
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	os.Mkdir(codex, 0700)
	os.WriteFile(filepath.Join(home, ".profile"), []byte("# keep login setup\n"), 0600)
	t.Setenv("SHELL", "/bin/bash")
	files, err := setupPlans(options{source: "codex", codexHome: codex, url: "https://app.example/api", token: "secret", email: "dev@example.com"}, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || files[1].path != filepath.Join(home, ".bashrc") || files[2].path != filepath.Join(home, ".profile") {
		t.Fatalf("plans: %+v", files)
	}
	for _, f := range files {
		if err := f.apply(); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(filepath.Join(home, ".profile"))
	if !strings.Contains(string(got), "# keep login setup") || strings.Contains(string(got), "secret") {
		t.Fatal("login config lost or credential leaked")
	}
	t.Setenv("SHELL", "/bin/fish")
	if _, err = setupPlans(options{source: "codex", codexHome: codex, url: "https://app.example/api", token: "secret", email: "dev@example.com"}, home); err == nil {
		t.Fatal("unsupported shell must not be configured silently")
	}
}
