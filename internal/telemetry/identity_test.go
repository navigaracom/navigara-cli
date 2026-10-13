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
