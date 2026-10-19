package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBrowserLogin(t *testing.T) {
	var challenge string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth/token" || r.Method != "POST" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if r.Form.Get("code") != "one-time-code" || r.Form.Get("client_id") != oauthClientID || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
			t.Error("PKCE mismatch")
		}
		io.WriteString(w, `{"access_token":"private-session","token_type":"Bearer","expires_in":3600,"scope":"navigara"}`)
	}))
	defer srv.Close()
	open := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		if u.Scheme+"://"+u.Host != srv.URL || u.Path != "/oauth/authorize" {
			t.Error("wrong login host")
		}
		q := u.Query()
		if q.Get("response_type") != "code" || q.Get("client_id") != oauthClientID || q.Get("scope") != oauthScope || q.Get("code_challenge_method") != "S256" {
			t.Error("incomplete OAuth authorization request")
		}
		challenge = q.Get("code_challenge")
		callback := q.Get("redirect_uri")
		bad, err := http.Get(callback + "?state=wrong&code=one-time-code")
		if err != nil {
			return err
		}
		bad.Body.Close()
		if bad.StatusCode != http.StatusBadRequest {
			t.Error("accepted wrong state")
		}
		response, err := http.Get(callback + "?" + url.Values{"state": {q.Get("state")}, "iss": {srv.URL}, "code": {"one-time-code"}}.Encode())
		if err != nil {
			return err
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("callback: %d", response.StatusCode)
		}
		return nil
	}
	var out bytes.Buffer
	got, err := newAuthClient(srv.URL).browserLogin(context.Background(), "navigara login", &out, open)
	if err != nil || got.SessionToken != "private-session" {
		t.Fatalf("login failed: %v", err)
	}
	if strings.Contains(out.String(), "private-session") || strings.Contains(out.String(), "one-time-code") {
		t.Fatal("secret leaked")
	}
}

func TestBrowserLoginCancelledWithFallback(t *testing.T) {
	var out bytes.Buffer
	_, err := newAuthClient(defaultHost).browserLogin(context.Background(), "navigara login", &out, func(string) error {
		process, findErr := os.FindProcess(os.Getpid())
		if findErr != nil {
			return findErr
		}
		return errors.Join(errors.New("no browser"), process.Signal(os.Interrupt))
	})
	if err == nil || !strings.Contains(out.String(), defaultHost+"/oauth/authorize?") {
		t.Fatal("missing fallback/cancellation")
	}
}

func TestAuthClientDoesNotFollowRedirects(t *testing.T) {
	seen := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { seen = true }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	err := newAuthClient(srv.URL).request(context.Background(), "GET", "/v1/auth/session", "private-session", nil, new(any))
	if err == nil || seen || strings.Contains(err.Error(), "private-session") {
		t.Fatal("redirect leaked credential")
	}
}

func TestAuthClientRejectsBadResponses(t *testing.T) {
	for _, body := range []string{`not-json`, strings.Repeat("x", 1024*1024+1)} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		err := newAuthClient(srv.URL).request(context.Background(), "GET", "/v1/auth/session", "", nil, new(any))
		srv.Close()
		if err == nil {
			t.Fatal("accepted invalid response")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if err := newAuthClient(defaultHost).request(ctx, "GET", "/v1/auth/session", "", nil, new(any)); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestBrowserLoginRejectedCodeGuidance(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer srv.Close()
			var out bytes.Buffer
			_, err := newAuthClient(srv.URL).browserLogin(context.Background(), "navigara login", &out, func(raw string) error {
				u, e := url.Parse(raw)
				if e != nil {
					return e
				}
				q := u.Query()
				resp, e := http.Get(q.Get("redirect_uri") + "?" + url.Values{"state": {q.Get("state")}, "iss": {srv.URL}, "code": {"expired"}}.Encode())
				if e == nil {
					resp.Body.Close()
				}
				return e
			})
			if err == nil || !strings.Contains(err.Error(), "navigara login") || strings.Contains(err.Error(), "administrator") {
				t.Fatalf("wrong sign-in recovery: %v", err)
			}
		})
	}
}
