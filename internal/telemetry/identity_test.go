package telemetry

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadIdentityReadsParentConfigOnlyForDefaultHome(t *testing.T) {
	root := t.TempDir()
	writeConfig := func(path, email string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"oauthAccount":{"emailAddress":"`+email+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(filepath.Join(root, ".claude.json"), "personal@example.com")
	writeConfig(filepath.Join(root, "inside", ".claude.json"), "inside@example.com")
	for _, dir := range []string{".claude", "work-claude"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for home, want := range map[string]string{
		".claude":     "personal@example.com", // ~/.claude keeps its config at ~/.claude.json.
		"work-claude": "",                     // Never another home's parent config.
		"inside":      "inside@example.com",
	} {
		if got := ReadIdentity(filepath.Join(root, home), "").Email; got != want {
			t.Errorf("home %s: email %q, want %q", home, got, want)
		}
	}
}
