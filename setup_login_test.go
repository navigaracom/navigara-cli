package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type mutateSetupWriter struct {
	bytes.Buffer
	match  string
	mutate func()
	done   bool
}

func (w *mutateSetupWriter) Write(p []byte) (int, error) {
	if !w.done && strings.Contains(string(p), w.match) {
		w.done = true
		w.mutate()
	}
	return w.Buffer.Write(p)
}

func setupAccountServer(t *testing.T, ingestion http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/session":
			if r.Header.Get("Authorization") != "Bearer session-secret" {
				t.Error("wrong account credential")
			}
			io.WriteString(w, `{"userId":"user-1","email":"dev@example.com"}`)
		case "/api/v1/auth/organizations":
			io.WriteString(w, `{"organizations":[{"id":"org-1","name":"One","status":"active"}]}`)
		default:
			ingestion.ServeHTTP(w, r)
		}
	}))
	err := saveAccount(accountConfig{Host: server.URL, SessionToken: "session-secret", SessionExpiresAt: time.Now().Add(time.Hour), UserID: "user-1", Email: "dev@example.com", OrganizationID: "org-1", IngestToken: "test-secret", IngestExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	return server
}

func TestSetupStartsLoginOnSelectedHost(t *testing.T) {
	for _, mode := range []string{"default", "override", "expired"} {
		t.Run(mode, func(t *testing.T) {
			withConfigHome(t)
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var out, errs bytes.Buffer
			host := ""
			want := defaultHost
			if mode == "override" {
				host = "https://onprem.example"
				want = host
			}
			if mode == "expired" {
				want = "https://saved.example"
				if err := saveAccount(accountConfig{Host: want, SessionToken: "old", SessionExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
					t.Fatal(err)
				}
			}
			opened := false
			code := runSetupWithBrowser(ctx, []string{"--host", host}, strings.NewReader(""), &out, &errs, func(raw string) error {
				opened = true
				u, err := url.Parse(raw)
				if err != nil {
					return err
				}
				if u.Scheme+"://"+u.Host != want || u.Path != "/oauth/authorize" {
					t.Error("wrong login destination")
				}
				cancel()
				return nil
			})
			if code == 0 || !opened {
				t.Fatalf("login not started/cancelled: code=%d opened=%v %s", code, opened, &errs)
			}
		})
	}
}

func TestSetupWithoutToolsStopsBeforeLogin(t *testing.T) {
	withConfigHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	var out, errs bytes.Buffer
	code := runSetupWithBrowser(context.Background(), nil, strings.NewReader(""), &out, &errs, func(string) error {
		t.Error("opened the browser without a tool")
		return nil
	})
	want := "navigara: Claude Code and Codex were not found on this computer (looked in " + filepath.FromSlash("~/.claude") + " and " + filepath.FromSlash("~/.codex") + "). Install one, run it once, then run navigara wizard again.\n"
	if code != 1 || errs.String() != want || out.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, &out, &errs)
	}
}

func TestSetupLoginReturnsToWizard(t *testing.T) {
	withConfigHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "missing"))
	var out, errs bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			json.NewEncoder(w).Encode(map[string]any{"access_token": "session-secret", "token_type": "Bearer", "expires_in": 3600, "scope": "navigara"})
		case "/api/v1/auth/session":
			io.WriteString(w, `{"userId":"user-1","email":"dev@example.com"}`)
		case "/api/v1/auth/organizations":
			io.WriteString(w, `{"organizations":[{"id":"org-1","name":"One","status":"active"}]}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	code := runSetupWithBrowser(context.Background(), []string{"--host", srv.URL}, strings.NewReader(""), &out, &errs, func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := u.Query()
		resp, err := http.Get(q.Get("redirect_uri") + "?" + url.Values{"state": {q.Get("state")}, "iss": {srv.URL}, "code": {"code"}}.Encode())
		if err == nil {
			resp.Body.Close()
		}
		return err
	})
	if code != 1 || !strings.Contains(errs.String(), "input ended") {
		t.Fatalf("didn't resume wizard after login: %d %s", code, &errs)
	}
	c, err := loadAccount("")
	if err != nil || c.OrganizationID != "org-1" {
		t.Fatal("login didn't persist chosen organization")
	}
	if strings.Contains(out.String()+errs.String(), "session-secret") {
		t.Fatal("session token printed")
	}
}

func TestSetupResumeRejectsDifferentTenantBeforeSending(t *testing.T) {
	for _, change := range []string{"host", "organization"} {
		t.Run(change, func(t *testing.T) {
			withConfigHome(t)
			posts := 0
			srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts++; t.Error("unexpected ingestion") }))
			defer srv.Close()
			dir := t.TempDir()
			host, org := srv.URL+"/api", "org-1"
			if change == "host" {
				host = "https://another.example/api"
			} else {
				org = "other-org"
			}
			if err := saveSetupBatches(dir, host, org); err != nil {
				t.Fatal(err)
			}
			var out, errs bytes.Buffer
			code := runSetup(context.Background(), []string{"--resume", dir}, strings.NewReader("y\n"), &out, &errs)
			if code == 0 || posts != 0 || !strings.Contains(errs.String(), "different "+change) {
				t.Fatalf("wrong resume result: %d %s", code, &errs)
			}
		})
	}
}

func TestSetupDeclineDoesNotMintIngestionKey(t *testing.T) {
	claude, codex := fixtureHomes(t)
	home := filepath.Dir(claude)
	setHome(t, home)
	setConfigHome(t, filepath.Join(home, "config"))
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZDOTDIR", home)
	posts := 0
	srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { posts++; t.Error("request before consent") }))
	defer srv.Close()
	c, err := loadAccount("")
	if err != nil {
		t.Fatal(err)
	}
	c.IngestToken = ""
	c.IngestExpiresAt = time.Time{}
	if err = saveAccount(c); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	code := runSetup(context.Background(), nil, strings.NewReader("y\nn\n\nskip\nn\n"), &out, &errs)
	if code != 0 || posts != 0 {
		t.Fatalf("decline: %d %s", code, &errs)
	}
	if _, err = os.Stat(filepath.Join(claude, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("decline wrote tool settings")
	}
}

func TestSetupReplacesRejectedCachedIngestionKey(t *testing.T) {
	for _, flow := range []string{"fresh", "resume"} {
		t.Run(flow, func(t *testing.T) {
			var home string
			if flow == "fresh" {
				claude, codex := fixtureHomes(t)
				home = filepath.Dir(claude)
				t.Setenv("CLAUDE_CONFIG_DIR", claude)
				t.Setenv("CODEX_HOME", codex)
			} else {
				home = t.TempDir()
			}
			setHome(t, home)
			setConfigHome(t, filepath.Join(home, "config"))
			t.Setenv("SHELL", "/bin/zsh")
			t.Setenv("ZDOTDIR", home)
			var keyRequests, probes int
			srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/organizations/org-1/api-keys":
					keyRequests++
					_ = json.NewEncoder(w).Encode(map[string]any{"plainTextKey": "replacement-secret", "apiKey": map[string]any{"organizationId": "org-1", "expiresAt": time.Now().Add(time.Hour)}})
				case "/api/otlp/v1/metrics":
					probes++
					if body, _ := io.ReadAll(r.Body); string(body) != "{}" {
						t.Error("setup sent data before the receiver probe")
					}
					switch r.Header.Get("Authorization") {
					case "Bearer test-secret":
						w.WriteHeader(http.StatusUnauthorized)
					case "Bearer replacement-secret":
						w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
						_, _ = w.Write([]byte(`{}`))
					default:
						t.Error("probe used an unexpected key")
						w.WriteHeader(http.StatusUnauthorized)
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			var out, errs bytes.Buffer
			var code int
			if flow == "fresh" {
				code = runSetup(context.Background(), nil, strings.NewReader("y\nn\ndev@example.com\nskip\ny\ny\n"), &out, &errs)
			} else {
				dir := t.TempDir()
				if err := saveSetupBatches(dir, srv.URL+"/api", "org-1"); err != nil {
					t.Fatal(err)
				}
				code = runSetup(context.Background(), []string{"--resume", dir}, strings.NewReader("y\n"), &out, &errs)
			}
			if code != 0 || keyRequests != 1 || probes != 2 {
				t.Fatalf("setup=%s code=%d keys=%d probes=%d stderr=%s", flow, code, keyRequests, probes, &errs)
			}
			saved, err := loadAccount("")
			if err != nil || saved.IngestToken != "replacement-secret" {
				t.Fatalf("replacement key was not saved: %#v err=%v", saved, err)
			}
			if len(saved.Imports) != 0 {
				t.Fatalf("saved a checkpoint without imported history: %v", saved.Imports)
			}
		})
	}
}

func TestSetupResumeDeclineExpiredKeyDoesNotMintOrUpload(t *testing.T) {
	withConfigHome(t)
	posts := 0
	srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		t.Error("request before upload consent")
	}))
	defer srv.Close()
	c, err := loadAccount("")
	if err != nil {
		t.Fatal(err)
	}
	c.IngestToken = ""
	c.IngestExpiresAt = time.Time{}
	if err := saveAccount(c); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := saveSetupBatches(dir, srv.URL+"/api", "org-1"); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if code := runSetup(context.Background(), []string{"--resume", dir}, strings.NewReader("n\n"), &out, &errs); code != 0 {
		t.Fatalf("declined resume = %d: %s", code, &errs)
	}
	if posts != 0 {
		t.Fatalf("minted or uploaded before consent: %d requests", posts)
	}
}

func TestSetupResumeLegacyTokenErrorDoesNotAskForConsent(t *testing.T) {
	dir := t.TempDir()
	if err := saveSetupBatches(dir, "https://example.test/api", ""); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if code := runSetup(context.Background(), []string{"--resume", dir}, strings.NewReader(""), &out, &errs); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if strings.Contains(out.String(), "Upload these saved batches again") {
		t.Fatalf("asked for consent after token input failed: %s", out.String())
	}
}

func TestSetupRejectsSettingsChangedWhileAwaitingFinalConsent(t *testing.T) {
	claude, codex := fixtureHomes(t)
	home := filepath.Dir(claude)
	setHome(t, home)
	setConfigHome(t, filepath.Join(home, "config"))
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZDOTDIR", home)
	settings := filepath.Join(claude, "settings.json")
	if err := os.WriteFile(settings, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	posts := 0
	srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api-keys") {
			json.NewEncoder(w).Encode(map[string]any{"plainTextKey": "new-secret", "apiKey": map[string]any{"organizationId": "org-1", "expiresAt": time.Now().Add(time.Hour)}})
			return
		}
		posts++
		t.Error("ingestion request after concurrent edit")
	}))
	defer srv.Close()
	c, err := loadAccount("")
	if err != nil {
		t.Fatal(err)
	}
	c.IngestToken = ""
	c.IngestExpiresAt = time.Time{}
	if err := saveAccount(c); err != nil {
		t.Fatal(err)
	}
	concurrent := []byte("{\"concurrent\":true}\n")
	out := &mutateSetupWriter{match: "Connecting your tools...", mutate: func() {
		if err := os.WriteFile(settings, concurrent, 0600); err != nil {
			t.Fatal(err)
		}
	}}
	var errs bytes.Buffer
	code := runSetup(context.Background(), nil, strings.NewReader("y\nn\n\nskip\ny\ny\n"), out, &errs)
	if code != 1 || !strings.Contains(errs.String(), "changed during setup") {
		t.Fatalf("concurrent edit = %d: %s", code, &errs)
	}
	if got, err := os.ReadFile(settings); err != nil || !bytes.Equal(got, concurrent) {
		t.Fatalf("concurrent settings were not preserved: %q, %v", got, err)
	}
	if posts != 0 {
		t.Fatalf("uploaded after concurrent edit: %d requests", posts)
	}
}

func TestSetupPermissionErrorComesBeforeHistoryScan(t *testing.T) {
	claude, codex := fixtureHomes(t)
	home := filepath.Dir(claude)
	setConfigHome(t, filepath.Join(home, "config"))
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZDOTDIR", home)
	srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api-keys") {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c, err := loadAccount("")
	if err != nil {
		t.Fatal(err)
	}
	c.IngestToken, c.IngestExpiresAt = "", time.Time{}
	if err = saveAccount(c); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	code := runSetup(context.Background(), nil, strings.NewReader("y\ny\n\nall\ny\n"), &out, &errs)
	want := "navigara: Your Navigara role cannot create an API key for \"One\". Ask an organization admin to grant you access, then run navigara wizard again.\n"
	if code != 1 || errs.String() != want {
		t.Fatalf("exit=%d stderr=%q", code, &errs)
	}
	if strings.Contains(out.String(), "sessions ready to import") {
		t.Fatalf("scanned history before the key check: %s", &out)
	}
}

func TestSetupRerunImportsOnlySinceLastImport(t *testing.T) {
	claude, codex := fixtureHomes(t)
	home := filepath.Dir(claude)
	setConfigHome(t, filepath.Join(home, "config"))
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZDOTDIR", home)
	srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	connected := "Already connected to " + srv.URL + " as dev@example.com.\n"
	for _, run := range []struct {
		input, want string
		imports     int
	}{
		// Skipping history keeps no checkpoint, so later runs still offer all of it.
		{"y\nn\n\nskip\ny\n", "restart Claude Code in a new terminal.", 0},
		// Codex first: the fixture's two Claude sessions stay unimported.
		{"n\ny\n\nall\ny\n", "Imported 0 past sessions.", 1},
		// Claude is new, so "since last import" still imports all of its history.
		{"y\ny\n\n\ny\n", "Imported 2 past sessions.", 2},
		// Both tools were imported: nothing older than the last import comes back.
		{"y\ny\n\n\ny\n", "Imported 0 past sessions.", 2},
	} {
		c, err := loadAccount("")
		if err != nil {
			t.Fatal(err)
		}
		before := len(c.Imports)
		var out, errs bytes.Buffer
		if code := runSetup(context.Background(), nil, strings.NewReader(run.input), &out, &errs); code != 0 {
			t.Fatalf("wizard = %d: %s", code, &errs)
		}
		if strings.Contains(out.String(), connected) != (before > 0) || !strings.Contains(out.String(), run.want) {
			t.Fatalf("want %q, connected=%v: %s", run.want, before > 0, &out)
		}
		if before > 0 && !strings.Contains(out.String(), "(since last import, 7d, 24h, all, or skip) [since]") {
			t.Fatalf("re-run did not default to the last import: %s", &out)
		}
		if c, err = loadAccount(""); err != nil || len(c.Imports) != run.imports {
			t.Fatalf("imports = %v, %v", c.Imports, err)
		}
		for _, at := range c.Imports {
			if time.Since(at) > time.Minute {
				t.Fatalf("stale import time %v", at)
			}
		}
	}
}

func TestSetupKeepsCheckpointWhenFilesWereSkipped(t *testing.T) {
	claude, codex := fixtureHomes(t)
	home := filepath.Dir(claude)
	setConfigHome(t, filepath.Join(home, "config"))
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZDOTDIR", home)
	broken := filepath.Join(claude, "projects", "broken", "broken.jsonl")
	if err := os.MkdirAll(filepath.Dir(broken), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(broken, []byte(`{"type":`), 0600); err != nil {
		t.Fatal(err)
	}
	srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	var out, errs bytes.Buffer
	if code := runSetup(context.Background(), nil, strings.NewReader("y\ny\n\nall\ny\n"), &out, &errs); code != 0 {
		t.Fatalf("wizard = %d: %s", code, &errs)
	}
	if !strings.Contains(errs.String(), "could not be read") || !strings.Contains(out.String(), "Imported 2 past sessions.") {
		t.Fatalf("setup did not import the good files and report the bad one: %s\n%s", &out, &errs)
	}
	c, err := loadAccount("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Imports["org-1/claude"]; ok {
		t.Fatalf("advanced the Claude checkpoint past a skipped file: %v", c.Imports)
	}
	if _, ok := c.Imports["org-1/codex"]; !ok {
		t.Fatalf("Codex checkpoint missing: %v", c.Imports)
	}
}

func TestSetupKeepsCheckpointForUnreadableSubagent(t *testing.T) {
	skipOnWindows(t, "Windows cannot make a file unreadable with chmod")
	claude, codex := fixtureHomes(t)
	home := filepath.Dir(claude)
	setConfigHome(t, filepath.Join(home, "config"))
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZDOTDIR", home)
	id := "aaaaaaaa-0000-0000-0000-000000000001"
	subagent := filepath.Join(claude, "projects", "-repo", id, "subagents", "agent-a.jsonl")
	if err := os.MkdirAll(filepath.Dir(subagent), 0700); err != nil {
		t.Fatal(err)
	}
	row := `{"type":"user","sessionId":"` + id + `","isSidechain":true,"timestamp":"2026-10-01T10:00:01Z","message":{"role":"user","content":"x"}}` + "\n"
	if err := os.WriteFile(subagent, []byte(row), 0); err != nil {
		t.Fatal(err)
	}
	var bodies strings.Builder
	srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodies.Write(mustBody(r))
		w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
		io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	run := func(input string) (accountConfig, string) {
		t.Helper()
		bodies.Reset()
		var out, errs bytes.Buffer
		if code := runSetup(context.Background(), nil, strings.NewReader(input), &out, &errs); code != 0 {
			t.Fatalf("wizard = %d: %s", code, &errs)
		}
		c, err := loadAccount("")
		if err != nil {
			t.Fatal(err)
		}
		return c, errs.String()
	}

	c, errs := run("y\nn\n\nall\ny\n")
	if !strings.Contains(errs, "1 unreadable subagent transcripts") || strings.Contains(bodies.String(), "subagent_completed") {
		t.Fatalf("unreadable subagent not reported: %s", errs)
	}
	if _, ok := c.Imports["org-1/claude"]; ok {
		t.Fatalf("advanced the Claude checkpoint past an unreadable subagent: %v", c.Imports)
	}

	if err := os.Chmod(subagent, 0600); err != nil {
		t.Fatal(err)
	}
	c, errs = run("y\nn\n\n\ny\n")
	if strings.Contains(errs, "unreadable") || !strings.Contains(bodies.String(), "subagent_completed") {
		t.Fatalf("repaired subagent not imported: %s", errs)
	}
	if _, ok := c.Imports["org-1/claude"]; !ok {
		t.Fatalf("Claude checkpoint missing after a complete import: %v", c.Imports)
	}
}
