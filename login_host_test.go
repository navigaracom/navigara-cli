package main

import (
	"bufio"
	"bytes"
	"context"
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

// oauthServer signs in user-1 into org-1 and returns an opener that completes the browser step.
func oauthServer(t *testing.T) (*httptest.Server, func(string) error) {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			io.WriteString(w, `{"access_token":"session-secret","token_type":"Bearer","expires_in":3600,"scope":"navigara"}`)
		case "/api/v1/auth/session":
			if r.Header.Get("Authorization") != "Bearer session-secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"userId":"user-1","email":"dev@example.com"}`)
		case "/api/v1/auth/organizations":
			io.WriteString(w, `{"organizations":[{"id":"org-1","name":"One","status":"active"}]}`)
		case "/api/otlp/v1/metrics":
			w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
			io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	open := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		resp, err := http.Get(u.Query().Get("redirect_uri") + "?" + url.Values{"state": {u.Query().Get("state")}, "iss": {srv.URL}, "code": {"code"}}.Encode())
		if err != nil {
			return err
		}
		return resp.Body.Close()
	}
	return srv, open
}

func TestBrowserLoginGuidance(t *testing.T) {
	t.Setenv("SSH_CONNECTION", "10.0.0.1 51000 192.0.2.7 22")
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	var port string
	_, err := newAuthClient(defaultHost).browserLogin(ctx, "navigara wizard", &out, func(raw string) error {
		u, _ := url.Parse(raw)
		redirect, _ := url.Parse(u.Query().Get("redirect_uri"))
		port = redirect.Port()
		cancel()
		return nil
	})
	for _, want := range []string{
		"If the browser did not open, copy this link:\n" + defaultHost + "/oauth/authorize?",
		"You are on SSH. Open the link in a browser on this machine, or forward the port: ssh -L " + port + ":127.0.0.1:" + port + " 192.0.2.7\n",
		"Waiting for sign-in in your browser (up to 5 min). Press Ctrl-C to cancel.\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	if err == nil || err.Error() != "login cancelled or timed out; run navigara wizard again" {
		t.Fatalf("err = %v", err)
	}
	if got := sshServer("10.0.0.1 51000 192.0.2.7 2222"); got != "-p 2222 192.0.2.7" {
		t.Errorf("custom SSH port: %q", got)
	}
	t.Setenv("SSH_CONNECTION", "")
	out.Reset()
	ctx, cancel = context.WithCancel(context.Background())
	newAuthClient(defaultHost).browserLogin(ctx, "navigara login", &out, func(string) error { cancel(); return nil })
	if strings.Contains(out.String(), "SSH") {
		t.Fatalf("SSH hint without SSH: %s", out.String())
	}
}

func TestHostMismatchNamesSignedInHost(t *testing.T) {
	withConfigHome(t)
	if err := saveAccount(accountConfig{Host: defaultHost, SessionToken: "s", SessionExpiresAt: time.Now().Add(time.Hour), UserID: "u", OrganizationID: "o"}); err != nil {
		t.Fatal(err)
	}
	_, err := verifiedAccount(context.Background(), "https://company.example")
	want := "No saved login for https://company.example (signed in to https://app.navigara.com). Run navigara login --host https://company.example."
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v", err)
	}
	for _, host := range []string{"app.navigara.com", "http://navigara.example.com", "https://navigara.example.com/path"} {
		if _, err := loadAccount(host); err != errHostFormat {
			t.Errorf("%s: err = %v", host, err)
		}
	}
}

func TestLoginToOtherHostNamesReplacedLogin(t *testing.T) {
	withConfigHome(t)
	srv, open := oauthServer(t)
	if err := saveAccount(accountConfig{Host: "https://old.example", SessionToken: "old", SessionExpiresAt: time.Now().Add(time.Hour), UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	p := &setupInput{r: bufio.NewReader(strings.NewReader("")), out: &out}
	if _, err := ensureAccount(context.Background(), srv.URL, p, true, open); err != nil || !strings.Contains(out.String(), "Replaced the saved login for https://old.example.\n") {
		t.Fatalf("err=%v out=%s", err, &out)
	}
	out.Reset()
	if _, err := ensureAccount(context.Background(), srv.URL, p, true, open); err != nil || strings.Contains(out.String(), "Replaced") {
		t.Fatalf("same-host login: err=%v out=%s", err, &out)
	}
}

func TestEnvironmentTokenErrorsNameTheSource(t *testing.T) {
	withConfigHome(t)
	opts := options{send: true, token: "env-secret"}
	err := prepareUploadAccount(context.Background(), &opts, io.Discard)
	if err == nil || err.Error() != "NAVIGARA_TOKEN is set. Unset it to use your saved login, or add --url." {
		t.Fatalf("err = %v", err)
	}
}

func TestConfigPatchEnvironmentTokenUsesSavedHost(t *testing.T) {
	withConfigHome(t)
	t.Setenv("NAVIGARA_TOKEN", "env-secret")
	claude := filepath.Join(t.TempDir(), "claude")
	if err := os.MkdirAll(claude, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveAccount(accountConfig{Host: "https://company.example", UserID: "u"}); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if code := runConfigPatch(context.Background(), []string{"--email", "dev@example.com", "--source", "claude", "--claude-home", claude}, &out, &errs); code != 0 {
		t.Fatalf("code=%d err=%s", code, &errs)
	}
	if !strings.Contains(out.String(), "Using the host of your saved login: https://company.example\n") || !strings.Contains(out.String(), "Host: https://company.example\n") {
		t.Fatalf("out=%s", &out)
	}
}

func TestUploadAndConfigPatchLogInOnTerminal(t *testing.T) {
	for _, command := range []string{"upload", "config patch"} {
		t.Run(command, func(t *testing.T) {
			withConfigHome(t)
			srv, open := oauthServer(t)
			if err := saveAccount(accountConfig{Host: srv.URL, SessionToken: "old", SessionExpiresAt: time.Now().Add(-time.Hour), UserID: "user-1", OrganizationID: "org-1", IngestToken: "test-secret", IngestExpiresAt: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			opened := false
			terminal, browser := stdinIsTerminal, loginBrowser
			t.Cleanup(func() { stdinIsTerminal, loginBrowser = terminal, browser })
			stdinIsTerminal = func() bool { return true }
			loginBrowser = func(raw string) error { opened = true; return open(raw) }
			var err error
			if command == "upload" {
				opts := options{send: true}
				if err = prepareUploadAccount(context.Background(), &opts, io.Discard); err == nil && opts.email != "dev@example.com" {
					t.Fatal("upload did not use the new login")
				}
			} else {
				claude := filepath.Join(t.TempDir(), "claude")
				if err := os.MkdirAll(claude, 0700); err != nil {
					t.Fatal(err)
				}
				var out, errs bytes.Buffer
				if code := runConfigPatch(context.Background(), []string{"--source", "claude", "--claude-home", claude}, &out, &errs); code != 0 {
					t.Fatalf("code=%d err=%s", code, &errs)
				}
			}
			if err != nil || !opened {
				t.Fatalf("opened=%v err=%v", opened, err)
			}
			stdinIsTerminal = func() bool { return false }
			if err := saveAccount(accountConfig{Host: srv.URL, SessionToken: "old", SessionExpiresAt: time.Now().Add(-time.Hour), UserID: "user-1", OrganizationID: "org-1"}); err != nil {
				t.Fatal(err)
			}
			if _, err := signedInAccount(context.Background(), "", "navigara sessions upload", io.Discard); err != errLoginNeeded {
				t.Fatalf("non-terminal err = %v", err)
			}
		})
	}
}
