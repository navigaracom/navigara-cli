package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCanonicalHost(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"https://App.Navigara.com:443/", "https://app.navigara.com"},
		{"https://app.navigara.com:00443", "https://app.navigara.com"},
		{"http://127.0.0.1:80", "http://127.0.0.1"},
		{"http://[::1]:8080/", "http://[::1]:8080"},
	}
	for _, tt := range tests {
		got, err := canonicalHost(tt.input)
		if err != nil || got != tt.want {
			t.Errorf("canonicalHost(%q) = %q, %v; want %q", tt.input, got, err, tt.want)
		}
	}
	for _, input := range []string{
		"", " https://app.navigara.com", "https://app.navigara.com/path", "https://app.navigara.com?x=1",
		"https://user:pass@app.navigara.com", "http://localhost:8080", "http://example.com", "https://app.navigara.com:bad",
		"https://app.navigara.com:0", "https://app.navigara.com:99999", "https://app.navigara.com:",
		"https://app.navigara.com?", "https://app.navigara.com#fragment", "https://app.navigara.com#",
		"https://app.navigara.com/%", "https://app.navigara.com\n",
		"https://[fe80::1%25en0]",
	} {
		if got, err := canonicalHost(input); err == nil {
			t.Errorf("canonicalHost(%q) accepted %q", input, got)
		}
	}
}

func TestAccountIPv6HostRoundTrip(t *testing.T) {
	withConfigHome(t)
	want := accountConfig{Host: "https://[::1]", SessionToken: "token"}
	if err := saveAccount(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadAccount("")
	if err != nil || got.Host != want.Host || got.SessionToken != want.SessionToken {
		t.Fatalf("roundtrip = %#v, %v", got, err)
	}
}

func TestSaveAccountRejectsConcurrentWriter(t *testing.T) {
	dir := withConfigHome(t)
	if err := ensureAccountDir(dir); err != nil {
		t.Fatal(err)
	}
	release, err := acquireAccountLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveAccount(accountConfig{Host: defaultHost}); err == nil || !strings.Contains(err.Error(), "try again") {
		t.Fatalf("concurrent save error = %v", err)
	}
	release()
	if err := saveAccount(accountConfig{Host: defaultHost}); err != nil {
		t.Fatalf("save after lock release: %v", err)
	}
}

func withConfigHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	setHome(t, dir)
	setConfigHome(t, filepath.Join(dir, "config"))
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, "navigara")
}

func TestLoadAccountInitialSameAndDifferentHost(t *testing.T) {
	withConfigHome(t)
	c, err := loadAccount("https://app.navigara.com")
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != 1 || c.Host != defaultHost || c.SessionToken != "" {
		t.Fatalf("initial account = %#v", c)
	}
	c.SessionToken, c.Email, c.OrganizationID = "secret", "a@example.com", "org-1"
	if err := saveAccount(c); err != nil {
		t.Fatal(err)
	}
	same, err := loadAccount("https://APP.NAVIGARA.COM:443/")
	if err != nil || same.SessionToken != "secret" {
		t.Fatalf("same host account = %#v, %v", same, err)
	}
	different, err := loadAccount("https://other.example")
	if err != nil {
		t.Fatal(err)
	}
	if different.Version != 1 || different.Host != "https://other.example" || different.SessionToken != "" || different.Email != "" || different.OrganizationID != "" {
		t.Fatalf("different host leaked context: %#v", different)
	}
}

func TestAccountPersistenceValidationAndAtomicity(t *testing.T) {
	dir := withConfigHome(t)
	c := accountConfig{Version: 1, Host: defaultHost, SessionToken: "token", IngestToken: "ingest", SessionExpiresAt: time.Unix(10, 0).UTC()}
	if err := saveAccount(c); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}
	got, err := loadAccount("")
	if err != nil || got.SessionToken != c.SessionToken || got.SessionExpiresAt != c.SessionExpiresAt {
		t.Fatalf("roundtrip = %#v, %v", got, err)
	}
	os.WriteFile(path, []byte(`{"version":99,"host":"https://app.navigara.com"}`), 0600)
	if _, err := loadAccount(""); err == nil {
		t.Fatal("accepted unsupported config version")
	}
	if err := os.WriteFile(path, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAccount(""); err == nil {
		t.Fatal("accepted corrupt config")
	}
}

func TestLoadAccountDoesNotEchoMalformedSecrets(t *testing.T) {
	dir := withConfigHome(t)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	secret := "do-not-echo-this-secret"
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"host":"https://app.navigara.com","sessionExpiresAt":"`+secret+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAccount(""); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("malformed config error leaked secret: %v", err)
	}
}

func TestAccountRejectsInsecureModesAndSymlinks(t *testing.T) {
	dir := withConfigHome(t)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAccount(""); err == nil {
		t.Fatal("accepted insecure config directory")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"host":"https://app.navigara.com"}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Force the insecure mode explicitly; os.WriteFile honors the umask.

	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAccount(""); err == nil {
		t.Fatal("accepted insecure config file")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAccount(""); err == nil {
		t.Fatal("accepted config symlink")
	}
}
