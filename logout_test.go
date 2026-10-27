package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func logoutAccount(host string) accountConfig {
	return accountConfig{
		Version:          1,
		Host:             host,
		SessionToken:     "session-secret",
		SessionExpiresAt: time.Now().Add(time.Hour),
		UserID:           "user-1",
		Email:            "dev@example.com",
		OrganizationID:   "org-1",
		OrganizationName: "Acme",
		IngestToken:      "ingest-secret",
		IngestExpiresAt:  time.Now().Add(time.Hour),
	}
}

func setLogoutConfigHome(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	setHome(t, root)
	setConfigHome(t, filepath.Join(root, "config"))
}

func requireLoggedOut(t *testing.T) {
	t.Helper()
	got, err := loadAccount("")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionToken != "" || !got.SessionExpiresAt.IsZero() || got.UserID != "" || got.Email != "" || got.OrganizationID != "" || got.OrganizationName != "" || got.IngestToken != "" || !got.IngestExpiresAt.IsZero() {
		t.Fatalf("account was not cleared: %#v", got)
	}
}

func TestLogoutRevokesAndClearsSavedAccount(t *testing.T) {
	setLogoutConfigHome(t)
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/auth/sign-out" || r.Header.Get("Authorization") != "Bearer session-secret" {
			t.Fatalf("unexpected request: %s %s %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "{}" {
			t.Fatalf("body = %q, want {}", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	if err := saveAccount(logoutAccount(srv.URL)); err != nil {
		t.Fatal(err)
	}

	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"logout"}, &out, &errs); code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, &out, &errs)
	}
	if !called {
		t.Fatal("logout did not revoke the saved session")
	}
	if !strings.Contains(out.String(), "navigara sessions config remove --apply") {
		t.Fatalf("logout does not name the remove command: %s", &out)
	}
	requireLoggedOut(t)
	if code := run(context.Background(), []string{"logout"}, &out, &errs); code != 0 {
		t.Fatalf("repeat logout = %d: %s", code, &errs)
	}
}

func TestLogoutWithoutSessionClearsSavedIdentityOffline(t *testing.T) {
	setLogoutConfigHome(t)
	c := logoutAccount("https://app.navigara.com")
	c.SessionToken, c.SessionExpiresAt = "", time.Time{}
	if err := saveAccount(c); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"logout"}, &out, &errs); code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, &out, &errs)
	}
	requireLoggedOut(t)
}

func TestLogoutHostDoesNotClearAnotherSavedHost(t *testing.T) {
	setLogoutConfigHome(t)
	if err := saveAccount(logoutAccount("https://saved.example")); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"logout", "--host", "https://other.example"}, &out, &errs); code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, &out, &errs)
	}
	got, err := loadAccount("")
	if err != nil || got.Host != "https://saved.example" || got.SessionToken != "session-secret" {
		t.Fatalf("saved account changed: %#v, %v", got, err)
	}
}

func TestLogoutDoesNotOverwriteConcurrentLogin(t *testing.T) {
	setLogoutConfigHome(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fresh := logoutAccount(srv.URL)
		fresh.SessionToken, fresh.UserID, fresh.Email, fresh.OrganizationID = "fresh-session", "user-2", "new@example.com", "org-2"
		if err := saveAccount(fresh); err != nil {
			t.Errorf("save fresh account: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	if err := saveAccount(logoutAccount(srv.URL)); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"logout"}, &out, &errs); code == 0 || !strings.Contains(errs.String(), "changed") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, &out, &errs)
	}
	got, err := loadAccount("")
	if err != nil || got.SessionToken != "fresh-session" || got.UserID != "user-2" {
		t.Fatalf("concurrent login was overwritten: %#v, %v", got, err)
	}
}

func TestLogoutClearsLocalAccountWhenRevocationIsOffline(t *testing.T) {
	setLogoutConfigHome(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	host := srv.URL
	srv.Close()
	if err := saveAccount(logoutAccount(host)); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if code := run(context.Background(), []string{"logout"}, &out, &errs); code == 0 || !strings.Contains(errs.String(), "could not revoke") {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, &out, &errs)
	}
	requireLoggedOut(t)
}

func TestLogoutHandlesExpiredAndDeniedRemoteSessions(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		want   int
	}{
		{name: "expired session", status: http.StatusUnauthorized, want: 0},
		{name: "denied session", status: http.StatusForbidden, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setLogoutConfigHome(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()
			if err := saveAccount(logoutAccount(srv.URL)); err != nil {
				t.Fatal(err)
			}
			var out, errs bytes.Buffer
			if code := run(context.Background(), []string{"logout"}, &out, &errs); code != tt.want {
				t.Fatalf("exit=%d, want %d; stdout=%s stderr=%s", code, tt.want, &out, &errs)
			}
			requireLoggedOut(t)
		})
	}
}
