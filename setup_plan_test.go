package main

import (
	"bytes"
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
	if err := applySetupFiles(files, func() error { return nil }); err != nil {
		t.Fatal(err)
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

func TestRefreshSetupPlansRejectsChangedSnapshots(t *testing.T) {
	for _, existing := range []bool{true, false} {
		t.Run(map[bool]string{true: "existing", false: "created"}[existing], func(t *testing.T) {
			home := t.TempDir()
			codex := filepath.Join(home, ".codex")
			if err := os.Mkdir(codex, 0700); err != nil {
				t.Fatal(err)
			}
			config := filepath.Join(codex, "config.toml")
			if existing {
				if err := os.WriteFile(config, []byte("model = \"initial\"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("SHELL", "/bin/zsh")
			t.Setenv("ZDOTDIR", home)
			opts := options{source: "codex", codexHome: codex, url: "https://app.example/api", token: "first", email: "dev@example.com"}
			files, err := setupPlans(opts, home)
			if err != nil {
				t.Fatal(err)
			}
			originalAfter := append([]byte(nil), files[0].after...)
			if err := os.WriteFile(config, []byte("model = \"changed\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			opts.token = "second"
			if err := refreshSetupPlans(files, opts, home); err == nil {
				t.Fatal("accepted changed config snapshot")
			}
			if !bytes.Equal(files[0].after, originalAfter) {
				t.Fatal("replaced original output after rejecting changed snapshot")
			}
		})
	}
}
