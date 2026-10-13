package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCheckPatchHomesDiscoversExistingTools(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	if err := os.Mkdir(claude, 0700); err != nil {
		t.Fatal(err)
	}
	opts := options{source: "all", claudeHome: claude, codexHome: filepath.Join(root, "missing")}
	if err := checkPatchHomes(&opts, "", ""); err != nil {
		t.Fatal(err)
	}
	if opts.source != "claude" {
		t.Fatalf("source = %q, want claude", opts.source)
	}
}

func TestCheckPatchHomesRejectsExplicitMissingHome(t *testing.T) {
	root := t.TempDir()
	codex := filepath.Join(root, "codex")
	err := checkPatchHomes(&options{source: "codex", claudeHome: filepath.Join(root, "claude"), codexHome: codex}, "", codex)
	if err == nil || !strings.Contains(err.Error(), "Codex home") {
		t.Fatalf("error = %v, want explicit Codex home failure", err)
	}
}

func TestCheckPatchHomesNamesMissingEnvironmentHome(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "codex")
	t.Setenv("CODEX_HOME", missing)
	err := checkPatchHomes(&options{source: "codex", codexHome: missing}, "", "")
	if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "$CODEX_HOME") {
		t.Fatalf("error = %v, want the missing path and $CODEX_HOME", err)
	}
}

func TestConfigPatchPreviewReadsAccountWithoutWritingOrPosting(t *testing.T) {
	var posts int
	revoked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		}
		switch r.URL.Path {
		case "/api/v1/auth/session":
			_ = json.NewEncoder(w).Encode(map[string]string{"userId": "u1", "email": "dev@example.com"})
		case "/api/v1/auth/organizations":
			if revoked {
				w.Write([]byte(`{"organizations":[]}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"organizations": []map[string]string{{"id": "org1", "name": "Acme", "status": "active"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "state"))
	t.Setenv("SHELL", "/bin/bash")
	claude, codex := filepath.Join(root, "claude"), filepath.Join(root, "codex")
	if err := os.MkdirAll(claude, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codex, 0700); err != nil {
		t.Fatal(err)
	}
	claudePath, codexPath := filepath.Join(claude, "settings.json"), filepath.Join(codex, "config.toml")
	if err := os.WriteFile(claudePath, []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexPath, []byte("model = \"o3\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := accountConfig{Version: 1, Host: srv.URL, SessionToken: "session", UserID: "u1", Email: "dev@example.com", OrganizationID: "org1", OrganizationName: "Acme", IngestToken: "saved-ingest", IngestExpiresAt: time.Now().Add(time.Hour), SessionExpiresAt: time.Now().Add(time.Hour)}
	if err := saveAccount(c); err != nil {
		t.Fatal(err)
	}
	beforeClaude, _ := os.ReadFile(claudePath)
	beforeCodex, _ := os.ReadFile(codexPath)
	var out, errOut bytes.Buffer
	if code := runConfigPatch([]string{"--host", srv.URL, "--source", "all", "--claude-home", claude, "--codex-home", codex}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	if posts != 0 {
		t.Fatalf("preview made %d POST requests", posts)
	}
	afterClaude, _ := os.ReadFile(claudePath)
	afterCodex, _ := os.ReadFile(codexPath)
	if !bytes.Equal(beforeClaude, afterClaude) || !bytes.Equal(beforeCodex, afterCodex) {
		t.Fatal("preview changed tool settings")
	}
	if !strings.Contains(out.String(), "org1") || strings.Contains(out.String(), "saved-ingest") {
		t.Fatalf("unsafe or incomplete preview output: %s", out.String())
	}
	revoked = true
	args := []string{"--apply", "--claude-home", claude, "--codex-home", codex}
	if code := runConfigPatch(args, &out, &errOut); code == 0 || posts != 0 {
		t.Fatal("revoked membership accepted or posted")
	}
	afterClaude, _ = os.ReadFile(claudePath)
	afterCodex, _ = os.ReadFile(codexPath)
	if !bytes.Equal(beforeClaude, afterClaude) || !bytes.Equal(beforeCodex, afterCodex) {
		t.Fatal("revoked membership changed settings")
	}

}

func TestConfigPatchApplyScopesAndReusesIngestionKey(t *testing.T) {
	var keyPosts, otlpPosts int
	issuedKey := "ingest-secret"
	rejectCachedKey := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/session":
			_ = json.NewEncoder(w).Encode(map[string]string{"userId": "u1", "email": "dev@example.com"})
		case "/api/v1/auth/organizations":
			_ = json.NewEncoder(w).Encode(map[string]any{"organizations": []map[string]string{{"id": "org1", "name": "Acme", "status": "active"}}})
		case "/api/v1/organizations/org1/api-keys":
			if r.Header.Get("Authorization") != "Bearer session-secret" {
				t.Error("key request needs session auth")
			}
			var body struct{ Role string }
			json.NewDecoder(r.Body).Decode(&body)
			if body.Role != "roles/analytics.ingestor" {
				t.Error("wrong key role")
			}
			keyPosts++
			if keyPosts == 2 {
				issuedKey = "replacement-secret"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"plainTextKey": issuedKey, "apiKey": map[string]any{"organizationId": "org1", "expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339)}})
		case "/api/otlp/v1/metrics":
			if rejectCachedKey && r.Header.Get("Authorization") == "Bearer ingest-secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+issuedKey {
				t.Error("probe needs scoped ingestion auth")
			}
			otlpPosts++
			w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "state"))
	t.Setenv("SHELL", "/bin/bash")
	claude, codex := filepath.Join(root, "claude"), filepath.Join(root, "codex")
	if err := os.MkdirAll(claude, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codex, 0700); err != nil {
		t.Fatal(err)
	}
	claudePath, codexPath := filepath.Join(claude, "settings.json"), filepath.Join(codex, "config.toml")
	if err := os.WriteFile(claudePath, []byte(`{"theme":"dark","env":{"KEEP":"yes"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codexPath, []byte("model = \"o3\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveAccount(accountConfig{Version: 1, Host: srv.URL, SessionToken: "session-secret", UserID: "u1", Email: "dev@example.com", OrganizationID: "org1", OrganizationName: "Acme", SessionExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	args := []string{"--host", srv.URL, "--source", "all", "--claude-home", claude, "--codex-home", codex, "--apply"}
	var out, errOut bytes.Buffer
	if code := runConfigPatch(args, &out, &errOut); code != 0 {
		t.Fatalf("first apply code=%d err=%s", code, errOut.String())
	}
	firstClaude, _ := os.ReadFile(claudePath)
	firstCodex, _ := os.ReadFile(codexPath)
	if !bytes.Contains(firstClaude, []byte("ingest-secret")) || !bytes.Contains(firstCodex, []byte("ingest-secret")) {
		t.Fatal("native configs lack ingestion key")
	}
	if bytes.Contains(firstClaude, []byte("session-secret")) || bytes.Contains(firstCodex, []byte("session-secret")) {
		t.Fatal("session token leaked into native config")
	}
	if !bytes.Contains(firstClaude, []byte(`"KEEP": "yes"`)) || !bytes.Contains(firstClaude, []byte("theme")) {
		t.Fatal("unrelated Claude settings were lost")
	}
	rejectCachedKey = true
	if code := runConfigPatch(args, &out, &errOut); code != 0 {
		t.Fatalf("replacement apply code=%d err=%s", code, errOut.String())
	}
	secondClaude, _ := os.ReadFile(claudePath)
	secondCodex, _ := os.ReadFile(codexPath)
	if !bytes.Contains(secondClaude, []byte("replacement-secret")) || !bytes.Contains(secondCodex, []byte("replacement-secret")) {
		t.Fatal("native configs did not receive the replacement key")
	}
	if code := runConfigPatch(args[:len(args)-1], &out, &errOut); code != 0 {
		t.Fatalf("preview after apply failed: %s", &errOut)
	}
	if strings.Contains(out.String()+errOut.String(), "secret") {
		t.Fatal("printed credential")
	}
	if keyPosts != 2 {
		t.Fatalf("ingestion key minted %d times", keyPosts)
	}
	if otlpPosts != 2 {
		t.Fatalf("receiver checked %d times", otlpPosts)
	}
}

func TestConfigPatchInvalidSettingsDoesNotMintIngestionKey(t *testing.T) {
	keyPosts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/session":
			_ = json.NewEncoder(w).Encode(map[string]string{"userId": "u1", "email": "dev@example.com"})
		case "/api/v1/auth/organizations":
			_ = json.NewEncoder(w).Encode(map[string]any{"organizations": []map[string]string{{"id": "org1", "name": "Acme", "status": "active"}}})
		case "/api/v1/organizations/org1/api-keys":
			keyPosts++
			http.Error(w, "must not mint", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "state"))
	claude := filepath.Join(root, "claude")
	if err := os.MkdirAll(claude, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claude, "settings.json"), []byte(`{"env":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveAccount(accountConfig{Version: 1, Host: srv.URL, SessionToken: "session", UserID: "u1", Email: "dev@example.com", OrganizationID: "org1", OrganizationName: "Acme", SessionExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runConfigPatch([]string{"--apply", "--host", srv.URL, "--source", "claude", "--claude-home", claude}, &out, &errOut); code != 1 {
		t.Fatalf("code = %d, want 1: %s", code, errOut.String())
	}
	if keyPosts != 0 {
		t.Fatalf("minted ingestion key despite invalid settings: %d", keyPosts)
	}
}
