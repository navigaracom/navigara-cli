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

func TestSetupLoginReturnsToWizard(t *testing.T) {
	withConfigHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "missing"))
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
	if code != 1 || !strings.Contains(errs.String(), "no tools selected") {
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
			code := runSetup([]string{"--resume", dir}, strings.NewReader("y\n"), &out, &errs)
			if code == 0 || posts != 0 || !strings.Contains(errs.String(), "different "+change) {
				t.Fatalf("wrong resume result: %d %s", code, &errs)
			}
		})
	}
}

func TestSetupDeclineDoesNotMintIngestionKey(t *testing.T) {
	claude, codex := fixtureHomes(t)
	home := filepath.Dir(claude)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
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
	code := runSetup(nil, strings.NewReader("y\nn\n\nskip\ny\nn\n"), &out, &errs)
	if code != 0 || posts != 0 {
		t.Fatalf("decline: %d %s", code, &errs)
	}
	if _, err = os.Stat(filepath.Join(claude, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("decline wrote tool settings")
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
	if code := runSetup([]string{"--resume", dir}, strings.NewReader("n\n"), &out, &errs); code != 0 {
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
	if code := runSetup([]string{"--resume", dir}, strings.NewReader(""), &out, &errs); code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if strings.Contains(out.String(), "Upload these saved batches again") {
		t.Fatalf("asked for consent after token input failed: %s", out.String())
	}
}

func TestSetupRejectsSettingsChangedWhileAwaitingFinalConsent(t *testing.T) {
	claude, codex := fixtureHomes(t)
	home := filepath.Dir(claude)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
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
	out := &mutateSetupWriter{match: "Apply these settings and upload the preview", mutate: func() {
		if err := os.WriteFile(settings, concurrent, 0600); err != nil {
			t.Fatal(err)
		}
	}}
	var errs bytes.Buffer
	code := runSetup(nil, strings.NewReader("y\nn\n\nskip\ny\ny\n"), out, &errs)
	if code != 1 || !strings.Contains(errs.String(), "changed during setup") {
		t.Fatalf("concurrent edit = %d: %s", code, &errs)
	}
	if got, err := os.ReadFile(settings); err != nil || !bytes.Equal(got, concurrent) {
		t.Fatalf("concurrent settings were not preserved: %q, %v", got, err)
	}
	if posts != 0 {
		t.Fatalf("minted or uploaded after concurrent edit: %d requests", posts)
	}
}
