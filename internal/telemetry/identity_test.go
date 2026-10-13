package telemetry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadIdentityReadsParentConfigOnlyForDefaultHome(t *testing.T) {
	user := t.TempDir()
	other := t.TempDir()
	t.Setenv("HOME", user)
	writeConfig := func(path, email string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"oauthAccount":{"emailAddress":"`+email+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(filepath.Join(user, ".claude.json"), "personal@example.com")
	writeConfig(filepath.Join(other, ".claude.json"), "other@example.com")
	writeConfig(filepath.Join(user, "inside", ".claude.json"), "inside@example.com")
	for _, dir := range []string{filepath.Join(user, ".claude"), filepath.Join(user, "work-claude"), filepath.Join(other, ".claude")} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for home, want := range map[string]string{
		filepath.Join(user, ".claude"):     "personal@example.com", // ~/.claude keeps its config at ~/.claude.json.
		filepath.Join(user, "work-claude"): "",                     // Never another home's parent config,
		filepath.Join(other, ".claude"):    "",                     // even for a custom home named .claude.
		filepath.Join(user, "inside"):      "inside@example.com",
	} {
		if got := ReadIdentity(home, "").Email; got != want {
			t.Errorf("home %s: email %q, want %q", home, got, want)
		}
	}
}

func TestReadIdentityRecognizesDefaultHomeHoweverSpelled(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	if err := os.Mkdir(filepath.Join(user, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := `{"oauthAccount":{"emailAddress":"personal@example.com"}}`
	if err := os.WriteFile(filepath.Join(user, ".claude.json"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "claude-link")
	if err := os.Symlink(filepath.Join(user, ".claude"), link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(user)
	for _, home := range []string{
		".claude",                            // --claude-home .claude from $HOME.
		filepath.Join(user, ".claude") + "/", // CLAUDE_CONFIG_DIR with a trailing slash.
		link,                                 // A symlink to ~/.claude.
	} {
		if got := ReadIdentity(home, "").Email; got != "personal@example.com" {
			t.Errorf("home %s: email %q, want the default home's ~/.claude.json", home, got)
		}
	}
}
