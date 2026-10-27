package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
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
}

func TestSetupPlansShellSelection(t *testing.T) {
	for _, test := range []struct {
		name, shell, flag, profile string
	}{
		{name: "fish", shell: "/usr/local/bin/fish", profile: ".config/fish/config.fish"},
		{name: "flag overrides SHELL", shell: "/bin/zsh", flag: "fish", profile: ".config/fish/config.fish"},
		{name: "flag without SHELL", flag: "bash", profile: ".bashrc"},
		{name: "unset SHELL"},
		{name: "fish without config directory", shell: "/usr/local/bin/fish", flag: "fish", profile: "missing"},
		{name: "unknown SHELL", shell: "/bin/tcsh"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			codex := filepath.Join(home, ".codex")
			if test.profile != "missing" {
				if err := os.MkdirAll(filepath.Join(home, ".config", "fish"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(codex, 0700); err != nil {
				t.Fatal(err)
			}
			setConfigHome(t, filepath.Join(home, ".config"))
			t.Setenv("ZDOTDIR", home)
			t.Setenv("SHELL", test.shell)
			files, err := setupPlans(options{source: "codex", codexHome: codex, shell: test.flag, url: "https://app.example/api", token: "secret", email: "dev@example.com"}, home)
			if err != nil {
				t.Fatal(err)
			}
			if test.profile == "missing" {
				if len(files) != 1 || !strings.Contains(files[0].warning, "set -gx OTEL_RESOURCE_ATTRIBUTES 'enduser.id=dev@example.com'") {
					t.Fatalf("want config.toml only and a manual fish line, got %d files, warning %q", len(files), files[0].warning)
				}
				return
			}
			if test.profile == "" {
				if len(files) != 1 || !strings.Contains(files[0].warning, "export OTEL_RESOURCE_ATTRIBUTES=enduser.id='dev@example.com'") {
					t.Fatalf("want config.toml only and a manual line, got %d files, warning %q", len(files), files[0].warning)
				}
				return
			}
			if len(files) < 2 || files[1].path != filepath.Join(home, test.profile) || files[0].warning != "" {
				t.Fatalf("plans: %+v", files)
			}
			if test.profile == ".config/fish/config.fish" && !strings.Contains(string(files[1].after), "set -gx OTEL_RESOURCE_ATTRIBUTES (string join , -- $navigara_attrs 'enduser.id=dev@example.com')") {
				t.Fatalf("fish block = %s", files[1].after)
			}
		})
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

func TestRefreshSetupPlansUpdatesChangeLines(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude")
	if err := os.Mkdir(claude, 0700); err != nil {
		t.Fatal(err)
	}
	opts := options{source: "claude", claudeHome: claude, url: "https://app.example/api", token: "old", email: "dev@example.com"}
	patched, err := claudeSettings(nil, otlpBase(opts.url), opts.token, opts.email)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, "settings.json"), patched, 0600); err != nil {
		t.Fatal(err)
	}
	files, err := setupPlans(opts, home)
	if err != nil || len(files[0].notes) != 0 {
		t.Fatalf("notes = %q, %v", files[0].notes, err)
	}
	opts.token = "rotated"
	if err := refreshSetupPlans(files, opts, home); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(files[0].notes, []string{"env.OTEL_EXPORTER_OTLP_HEADERS: Authorization=*** -> Authorization=***"}) {
		t.Fatalf("notes after rotation = %q", files[0].notes)
	}
}
