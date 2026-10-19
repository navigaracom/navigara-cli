package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	if code := runConfigPatch(context.Background(), []string{"--host", srv.URL, "--source", "all", "--claude-home", claude, "--codex-home", codex}, &out, &errOut); code != 0 {
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
	if code := runConfigPatch(context.Background(), args, &out, &errOut); code == 0 || posts != 0 {
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
	if code := runConfigPatch(context.Background(), args, &out, &errOut); code != 0 {
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
	if code := runConfigPatch(context.Background(), args, &out, &errOut); code != 0 {
		t.Fatalf("replacement apply code=%d err=%s", code, errOut.String())
	}
	secondClaude, _ := os.ReadFile(claudePath)
	secondCodex, _ := os.ReadFile(codexPath)
	if !bytes.Contains(secondClaude, []byte("replacement-secret")) || !bytes.Contains(secondCodex, []byte("replacement-secret")) {
		t.Fatal("native configs did not receive the replacement key")
	}
	if code := runConfigPatch(context.Background(), args[:len(args)-1], &out, &errOut); code != 0 {
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
	if code := runConfigPatch(context.Background(), []string{"--apply", "--host", srv.URL, "--source", "claude", "--claude-home", claude}, &out, &errOut); code != 1 {
		t.Fatalf("code = %d, want 1: %s", code, errOut.String())
	}
	if keyPosts != 0 {
		t.Fatalf("minted ingestion key despite invalid settings: %d", keyPosts)
	}
}

func TestConfigPatchTokenPreviewUsesDefaultHostWithoutSavedAccount(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "state"))
	claude := filepath.Join(root, "claude")
	if err := os.MkdirAll(claude, 0700); err != nil {
		t.Fatal(err)
	}
	claudePath := filepath.Join(claude, "settings.json")
	if err := os.WriteFile(claudePath, []byte(`{"permissions":{"allow":["Bash(git status)"]},"env":{"KEEP":"yes"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	// A saved target must not become the direct-token target or be read for identity.
	if err := saveAccount(accountConfig{Version: 1, Host: "https://saved.example", SessionToken: "expired", OrganizationID: "saved-org"}); err != nil {
		t.Fatal(err)
	}
	beforeSettings, _ := os.ReadFile(claudePath)
	_, accountPath, err := accountPath()
	if err != nil {
		t.Fatal(err)
	}
	beforeAccount, err := os.ReadFile(accountPath)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	secret := "mdm-preview-secret"
	if code := runConfigPatch(context.Background(), []string{"--token", secret, "--email", "dev@example.com", "--source", "claude", "--claude-home", claude}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d err=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), defaultHost) || strings.Contains(out.String()+errOut.String(), secret) {
		t.Fatalf("unsafe or wrong direct-token preview: %s%s", out.String(), errOut.String())
	}
	afterSettings, _ := os.ReadFile(claudePath)
	afterAccount, err := os.ReadFile(accountPath)
	backups, globErr := filepath.Glob(filepath.Join(claude, ".navigara-backup-*"))
	if !bytes.Equal(beforeSettings, afterSettings) || err != nil || !bytes.Equal(beforeAccount, afterAccount) || globErr != nil || len(backups) != 0 {
		t.Fatal("direct-token preview changed settings, account, or backups")
	}
}

func TestConfigPatchTokenRejectsInvalidDirectCredentialsWithoutOAuthFallback(t *testing.T) {
	for _, test := range []struct {
		name, env, want string
		args            []string
	}{
		{name: "empty flag overrides environment", env: "env-secret", args: []string{"--token", "", "--email", "dev@example.com"}, want: "nonempty analytics ingestor token"},
		{name: "empty environment", env: "", args: []string{"--email", "dev@example.com"}, want: "nonempty analytics ingestor token"},
		{name: "missing email", args: []string{"--token", "flag-secret"}, want: "--email is required with --token."},
		{name: "missing email with environment token", env: "env-secret", want: "NAVIGARA_TOKEN is set; --email is required with a token."},
		{name: "invalid host", args: []string{"--token", "flag-secret", "--email", "dev@example.com", "--host", "https://bad host"}, want: "--host must look like https://navigara.example.com (no path, HTTPS only)."},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NAVIGARA_TOKEN", test.env)
			var out, errOut bytes.Buffer
			if code := runConfigPatch(context.Background(), test.args, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), test.want) || strings.Contains(out.String()+errOut.String(), "secret") {
				t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
			}
		})
	}
	var out, errOut bytes.Buffer
	t.Setenv("NAVIGARA_TOKEN", "help-secret")
	if code := runConfigPatch(context.Background(), []string{"--help"}, &out, &errOut); code != 0 || strings.Contains(out.String()+errOut.String(), "help-secret") {
		t.Fatalf("code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
}

func TestConfigPatchTokenApplyPreflightsBeforeWriting(t *testing.T) {
	for _, test := range []struct {
		name, token, env string
		flag             bool
		status           int
		wantOK           bool
	}{
		{name: "accepted flag", token: "mdm-apply-secret", env: "different-env-secret", flag: true, status: http.StatusOK, wantOK: true},
		{name: "accepted environment", token: "mdm-env-secret", env: "mdm-env-secret", status: http.StatusOK, wantOK: true},
		{name: "rejected", token: "mdm-apply-secret", flag: true, status: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("NAVIGARA_TOKEN", test.env)
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/api/otlp/v1/metrics" || r.Header.Get("Authorization") != "Bearer "+test.token {
					t.Errorf("unexpected direct-token request %s %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer srv.Close()

			root := t.TempDir()
			t.Setenv("HOME", root)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "state"))
			t.Setenv("SHELL", "/bin/zsh")
			t.Setenv("ZDOTDIR", root)
			claude, codex := filepath.Join(root, "claude"), filepath.Join(root, "codex")
			if err := os.MkdirAll(claude, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(codex, 0700); err != nil {
				t.Fatal(err)
			}
			claudePath, codexPath, zshrc := filepath.Join(claude, "settings.json"), filepath.Join(codex, "config.toml"), filepath.Join(root, ".zshrc")
			if err := os.WriteFile(claudePath, []byte(`{"permissions":{"allow":["Bash(git status)"]},"env":{"KEEP":"yes"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(codexPath, []byte("model = \"gpt-5.4\"\n[projects.\"/work\"]\ntrust_level = \"trusted\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(zshrc, []byte("export PATH=\"$HOME/bin:$PATH\"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			beforeClaude, _ := os.ReadFile(claudePath)
			beforeCodex, _ := os.ReadFile(codexPath)
			beforeZshrc, _ := os.ReadFile(zshrc)
			var out, errOut bytes.Buffer
			args := []string{"--apply", "--host", srv.URL, "--email", "dev@example.com", "--source", "all", "--claude-home", claude, "--codex-home", codex}
			if test.flag {
				args = append(args, "--token", test.token)
			}
			code := runConfigPatch(context.Background(), args, &out, &errOut)
			if (code == 0) != test.wantOK || requests != 1 {
				t.Fatalf("code=%d requests=%d err=%s", code, requests, errOut.String())
			}
			afterClaude, _ := os.ReadFile(claudePath)
			afterCodex, _ := os.ReadFile(codexPath)
			afterZshrc, _ := os.ReadFile(zshrc)
			if test.wantOK {
				if !bytes.Contains(afterClaude, []byte(test.token)) || !bytes.Contains(afterCodex, []byte(test.token)) || !bytes.Contains(afterClaude, []byte("Bash(git status)")) || !bytes.Contains(afterCodex, []byte("gpt-5.4")) {
					t.Fatalf("direct-token patch lost existing settings or did not apply: %s", out.String())
				}
				command := exec.Command("sh", "-c", string(afterZshrc)+"\nprintf '%s\\n%s' \"$PATH\" \"$OTEL_RESOURCE_ATTRIBUTES\"")
				command.Env = append(os.Environ(), "PATH=/bin", "OTEL_RESOURCE_ATTRIBUTES=service.name=work,enduser.id=old@example.com")
				if got, err := command.Output(); err != nil || string(got) != root+"/bin:/bin\nservice.name=work,enduser.id=dev@example.com" {
					t.Fatalf("patched shell identity = %q, %v", got, err)
				}
				return
			}
			if !bytes.Equal(beforeClaude, afterClaude) || !bytes.Equal(beforeCodex, afterCodex) || !bytes.Equal(beforeZshrc, afterZshrc) {
				t.Fatal("rejected token changed settings")
			}
			for _, dir := range []string{claude, codex, root} {
				backups, err := filepath.Glob(filepath.Join(dir, ".navigara-backup-*"))
				if err != nil || len(backups) != 0 {
					t.Fatalf("rejected token created backups in %s: %v, %v", dir, backups, err)
				}
			}
		})
	}
}

// patchFixture returns empty Claude and Codex homes, a zsh profile, and a
// receiver that accepts direct-token probes.
func patchFixture(t *testing.T) (root, claude, codex, host string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	root = t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "state"))
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZDOTDIR", root)
	t.Setenv("NAVIGARA_TOKEN", "")
	claude, codex = filepath.Join(root, "claude"), filepath.Join(root, "codex")
	for _, dir := range []string{claude, codex} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return root, claude, codex, srv.URL
}

func runPatch(source, claude, codex, host string, extra ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	args := append([]string{"--token", "mdm-secret", "--email", "dev@example.com", "--host", host, "--source", source, "--claude-home", claude, "--codex-home", codex}, extra...)
	code := runConfigPatch(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestConfigPatchExistingClaudeTelemetryNeedsReplace(t *testing.T) {
	_, claude, codex, host := patchFixture(t)
	path := filepath.Join(claude, "settings.json")
	write := func(data string) {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"env":{"KEEP":"yes"}}`)
	if code, out, errOut := runPatch("claude", claude, codex, host, "--apply"); code != 0 || strings.Contains(out, "->") {
		t.Fatalf("no existing telemetry: code=%d out=%s err=%s", code, out, errOut)
	}
	if code, out, errOut := runPatch("claude", claude, codex, host, "--apply"); code != 0 || !strings.Contains(out, "Unchanged: "+path) || !strings.Contains(out, "No changes needed.") || strings.Contains(out, "applied") {
		t.Fatalf("same values: code=%d out=%s err=%s", code, out, errOut)
	}

	corp := `{"env":{"OTEL_EXPORTER_OTLP_ENDPOINT":"https://otel.corp.example","OTEL_EXPORTER_OTLP_HEADERS":"x-api-key=corp-secret","OTEL_METRICS_EXPORTER":"otlp"}}`
	write(corp)
	code, out, errOut := runPatch("claude", claude, codex, host)
	if code != 0 || !strings.Contains(out, "OTEL_EXPORTER_OTLP_ENDPOINT: https://otel.corp.example -> "+host+"/api/otlp") ||
		!strings.Contains(out, "OTEL_EXPORTER_OTLP_HEADERS: x-api-key=*** -> Authorization=***") || strings.Contains(out, "OTEL_METRICS_EXPORTER") ||
		strings.Contains(out+errOut, "secret") || !strings.Contains(out, "--replace") {
		t.Fatalf("preview: code=%d out=%s err=%s", code, out, errOut)
	}
	code, out, errOut = runPatch("claude", claude, codex, host, "--apply")
	if got, _ := os.ReadFile(path); code != 1 || errOut != "navigara: Claude Code already sends telemetry to https://otel.corp.example. Re-run with --replace, or use --source codex.\n" || string(got) != corp {
		t.Fatalf("apply without --replace: code=%d out=%s err=%s", code, out, errOut)
	}
	code, out, errOut = runPatch("claude", claude, codex, host, "--apply", "--replace")
	if got, _ := os.ReadFile(path); code != 0 || !strings.Contains(string(got), host+"/api/otlp") || strings.Contains(string(got), "corp") {
		t.Fatalf("apply with --replace: code=%d out=%s err=%s", code, out, errOut)
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://shell.example")
	if code, _, errOut := runPatch("claude", claude, codex, host); code != 1 || !strings.Contains(errOut, "OTEL_EXPORTER_OTLP_ENDPOINT is set in your shell environment (https://shell.example); unset it and retry.") {
		t.Fatalf("shell conflict: code=%d err=%s", code, errOut)
	}
}

func TestConfigPatchExistingCodexTelemetryNeedsReplace(t *testing.T) {
	_, claude, codex, host := patchFixture(t)
	fixture, err := os.ReadFile("testdata/codex-existing-telemetry.toml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(codex, "config.toml")
	original := append([]byte("[features]\nrmcp_client = true\n\n"), fixture...)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runPatch("codex", claude, codex, host)
	if code != 0 || !strings.Contains(out, "[features] rmcp_client: (removed)") || !strings.Contains(out, "[otel] exporters: https://old.example/v1/logs -> Navigara") {
		t.Fatalf("preview: code=%d out=%s err=%s", code, out, errOut)
	}
	code, _, errOut = runPatch("codex", claude, codex, host, "--apply")
	if got, _ := os.ReadFile(path); code != 1 || errOut != "navigara: Codex already sends telemetry to https://old.example/v1/logs. Re-run with --replace, or use --source claude.\n" || !bytes.Equal(got, original) {
		t.Fatalf("apply without --replace: code=%d err=%s", code, errOut)
	}
	code, _, errOut = runPatch("codex", claude, codex, host, "--apply", "--replace")
	if got, _ := os.ReadFile(path); code != 0 || bytes.Contains(got, []byte("old.example")) || bytes.Contains(got, []byte("rmcp_client")) {
		t.Fatalf("apply with --replace: code=%d err=%s got=%s", code, errOut, got)
	}
}

func TestConfigPatchWithoutKnownShellStillPatches(t *testing.T) {
	root, claude, codex, host := patchFixture(t)
	t.Setenv("SHELL", "")
	code, out, errOut := runPatch("all", claude, codex, host, "--apply")
	if code != 0 || !strings.Contains(out, "Warning:") || !strings.Contains(out, "export OTEL_RESOURCE_ATTRIBUTES=enduser.id='dev@example.com'") {
		t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
	}
	for _, path := range []string{filepath.Join(claude, "settings.json"), filepath.Join(codex, "config.toml")} {
		if got, err := os.ReadFile(path); err != nil || !bytes.Contains(got, []byte("mdm-secret")) {
			t.Fatalf("%s was not patched: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".zshrc")); !os.IsNotExist(err) {
		t.Fatal("wrote a shell profile without a known shell")
	}
}

func TestConfigRemoveRestoresOriginalSettings(t *testing.T) {
	root, claude, codex, host := patchFixture(t)
	claudePath, codexPath, zshrc := filepath.Join(claude, "settings.json"), filepath.Join(codex, "config.toml"), filepath.Join(root, ".zshrc")
	originals := map[string]string{
		claudePath: `{"theme":"dark","env":{"KEEP":"yes","OTEL_RESOURCE_ATTRIBUTES":"team=a"}}`,
		codexPath:  "model = \"o3\"\n\n[projects.\"/work\"]\ntrust_level = \"trusted\"\n",
		zshrc:      "export PATH=/bin\n",
	}
	for path, data := range originals {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if code, out, errOut := runPatch("all", claude, codex, host, "--apply"); code != 0 {
		t.Fatalf("patch: code=%d out=%s err=%s", code, out, errOut)
	}
	backups, _ := filepath.Glob(filepath.Join(root, "state", "navigara", "backups", "*", "*"))
	if len(backups) != 3 {
		t.Fatalf("want 3 backups in one run folder, got %v", backups)
	}

	remove := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := run(context.Background(), append([]string{"sessions", "config", "remove", "--claude-home", claude, "--codex-home", codex}, args...), &out, &errOut)
		return code, out.String(), errOut.String()
	}
	patched, _ := os.ReadFile(codexPath)
	if code, out, _ := remove(); code != 0 || strings.Count(out, "Update:") != 3 || !strings.Contains(out, "Preview only") {
		t.Fatalf("remove preview: code=%d out=%s", code, out)
	}
	if got, _ := os.ReadFile(codexPath); !bytes.Equal(got, patched) {
		t.Fatal("remove preview changed a file")
	}
	if code, out, errOut := remove("--apply"); code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("remove: code=%d out=%s err=%s", code, out, errOut)
	}
	for path, want := range originals {
		got, _ := os.ReadFile(path)
		if path == claudePath {
			var a, b any
			if json.Unmarshal(got, &a) != nil || json.Unmarshal([]byte(want), &b) != nil || !reflect.DeepEqual(a, b) {
				t.Fatalf("settings.json = %s, want %s", got, want)
			}
			continue
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", path, got, want)
		}
	}
	if code, out, _ := remove("--apply"); code != 0 || strings.Count(out, "Unchanged:") != 3 || !strings.HasSuffix(out, "No changes needed.\n") {
		t.Fatalf("second remove: code=%d out=%s", code, out)
	}
}
