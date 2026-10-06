package main

import (
	"bufio"
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

func TestAccountOrganizationSelection(t *testing.T) {
	for _, mode := range []string{"one", "several", "saved", "revoked", "none", "cycle"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Authorization") != "Bearer session-secret" {
					t.Error("missing session")
				}
				switch r.URL.Path {
				case "/api/v1/auth/session":
					io.WriteString(w, `{"userId":"user-1","email":"dev@example.com"}`)
				case "/api/v1/auth/organizations":
					if mode == "none" {
						io.WriteString(w, `{"organizations":[]}`)
						return
					}
					if mode == "cycle" {
						io.WriteString(w, `{"organizations":[],"nextPageToken":"repeat"}`)
						return
					}
					if r.URL.Query().Get("page_token") == "page two" {
						io.WriteString(w, `{"organizations":[{"id":"org-2","name":"Two","status":"active"}]}`)
						return
					}
					response := map[string]any{"organizations": []organization{{ID: "org-1", Name: "One", Status: "active"}}}
					if mode == "several" || mode == "saved" || mode == "revoked" {
						response["nextPageToken"] = "page two"
					}
					json.NewEncoder(w).Encode(response)
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			c := accountConfig{Version: 1, Host: srv.URL, SessionToken: "session-secret", SessionExpiresAt: time.Now().Add(time.Hour), UserID: "user-1"}
			if mode == "saved" {
				c.OrganizationID = "org-2"
				c.IngestToken = "ingest-secret"
			}
			if mode == "revoked" {
				c.OrganizationID = "removed"
				c.IngestToken = "ingest-secret"
			}
			if err := saveAccount(c); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			p := &setupInput{r: bufio.NewReader(strings.NewReader("2\n")), out: &out}
			got, err := ensureAccount(context.Background(), "", p, false, func(string) error { t.Fatal("unexpected browser"); return nil })
			if mode == "none" || mode == "cycle" {
				if err == nil {
					t.Fatal("missing error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := "org-2"
			if mode == "one" {
				want = "org-1"
			}
			if got.OrganizationID != want || got.Email != "dev@example.com" {
				t.Fatalf("wrong account/org: %s", got.OrganizationID)
			}
			if mode == "saved" && got.IngestToken != "ingest-secret" {
				t.Fatal("discarded valid org key")
			}
			if mode == "revoked" && got.IngestToken != "" {
				t.Fatal("retained revoked org key")
			}
			if requests < 2 || strings.Contains(out.String(), "secret") {
				t.Fatal("bad requests/redaction")
			}
			saved, err := loadAccount("")
			if err != nil || saved.OrganizationID != want {
				t.Fatal("selection not persisted")
			}
		})
	}
}

func TestAccountChecksAuthBeforeLogin(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer srv.Close()
			if err := saveAccount(accountConfig{Host: srv.URL, SessionToken: "expired", SessionExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opened := false
			var out bytes.Buffer
			_, err := ensureAccount(ctx, "", &setupInput{r: bufio.NewReader(strings.NewReader("")), out: &out}, false, func(string) error { opened = true; cancel(); return nil })
			if err == nil || opened != (status == 401) {
				t.Fatalf("status=%d opened=%v err=%v", status, opened, err)
			}
		})
	}
}

func TestAccountKeepsNewSessionWhenOrganizationSelectionFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	exchanges := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			exchanges++
			io.WriteString(w, `{"access_token":"session-secret","token_type":"Bearer","expires_in":3600,"scope":"navigara"}`)
		case "/api/v1/auth/session":
			if r.Header.Get("Authorization") != "Bearer session-secret" {
				t.Error("missing saved session")
			}
			io.WriteString(w, `{"userId":"user-1","email":"dev@example.com"}`)
		case "/api/v1/auth/organizations":
			io.WriteString(w, `{"organizations":[]}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	open := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		response, err := http.Get(u.Query().Get("redirect_uri") + "?" + url.Values{"state": {u.Query().Get("state")}, "iss": {srv.URL}, "code": {"code"}}.Encode())
		if err != nil {
			return err
		}
		return response.Body.Close()
	}
	p := &setupInput{r: bufio.NewReader(strings.NewReader("")), out: io.Discard}
	if _, err := ensureAccount(context.Background(), srv.URL, p, true, open); err == nil {
		t.Fatal("missing organization selection succeeded")
	}
	saved, err := loadAccount("")
	if err != nil || saved.SessionToken != "session-secret" || saved.UserID != "user-1" || saved.OrganizationID != "" {
		t.Fatalf("session was not retained: %#v, %v", saved, err)
	}
	if _, err := ensureAccount(context.Background(), "", p, true, func(string) error { t.Fatal("opened browser despite saved session"); return nil }); err == nil {
		t.Fatal("missing organization selection succeeded")
	}
	if exchanges != 1 {
		t.Fatalf("browser exchange count = %d, want 1", exchanges)
	}
}

func TestLoginHelpIsOffline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var out, errs bytes.Buffer
	if code := run([]string{"login", "--help"}, &out, &errs); code != 0 || !strings.Contains(out.String(), "--host") {
		t.Fatalf("exit=%d %s %s", code, &out, &errs)
	}
	dir, _ := os.UserConfigDir()
	if _, err := os.Stat(filepath.Join(dir, "navigara")); !os.IsNotExist(err) {
		t.Fatal("help wrote configuration")
	}
}
