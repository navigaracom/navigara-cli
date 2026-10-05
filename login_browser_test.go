package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBrowserLogin(t *testing.T) {
	var challenge string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/cli/exchange" || r.Method != "POST" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var body struct{ Code, CodeVerifier string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		hash := sha256.Sum256([]byte(body.CodeVerifier))
		if body.Code != "one-time-code" || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
			t.Error("PKCE mismatch")
		}
		io.WriteString(w, `{"sessionToken":"private-session","expiresAt":"2099-01-01T00:00:00Z"}`)
	}))
	defer srv.Close()
	open := func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		if u.Scheme+"://"+u.Host != srv.URL || u.Path != "/cli/login" {
			t.Error("wrong login host")
		}
		q := u.Query()
		challenge = q.Get("code_challenge")
		callback := "http://127.0.0.1:" + q.Get("port") + "/callback"
		bad, err := http.Get(callback + "?state=wrong&code=one-time-code")
		if err != nil {
			return err
		}
		bad.Body.Close()
		if bad.StatusCode != http.StatusBadRequest {
			t.Error("accepted wrong state")
		}
		response, err := http.Get(callback + "?" + url.Values{"state": {q.Get("state")}, "code": {"one-time-code"}}.Encode())
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
	got, err := newAuthClient(srv.URL).browserLogin(context.Background(), &out, open)
	if err != nil || got.SessionToken != "private-session" {
		t.Fatalf("login failed: %v", err)
	}
	if strings.Contains(out.String(), "private-session") || strings.Contains(out.String(), "one-time-code") {
		t.Fatal("secret leaked")
	}
}

func TestBrowserLoginCancelledWithFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	_, err := newAuthClient(defaultHost).browserLogin(ctx, &out, func(string) error { cancel(); return errors.New("no browser") })
	if err == nil || !strings.Contains(out.String(), defaultHost+"/cli/login?") {
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
