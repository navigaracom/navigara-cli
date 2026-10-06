package main

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOAuthTokenExchangesAuthorizationCodeWithPKCE(t *testing.T) {
	started := time.Now()
	c := newAuthClient("https://navigara.example")
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.String() != "https://navigara.example/api/oauth/token" {
			t.Fatalf("request = %s %s", r.Method, r.URL)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Fatalf("Content-Type = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]string{
			"grant_type":    "authorization_code",
			"code":          "authorization-code",
			"client_id":     "navigara-cli",
			"redirect_uri":  "http://127.0.0.1:54321/callback",
			"code_verifier": "verifier",
		}
		for key, value := range want {
			if got := form.Get(key); got != value {
				t.Errorf("%s = %q, want %q", key, got, value)
			}
		}
		if form.Get("client_secret") != "" {
			t.Fatal("sent client_secret")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"access_token":"access-token","token_type":"bEaReR","expires_in":3600,"scope":"navigara"}`))}, nil
	})}

	session, err := c.oauthToken(context.Background(), "authorization-code", "http://127.0.0.1:54321/callback", "verifier", started)
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionToken != "access-token" || !session.ExpiresAt.After(started) || session.ExpiresAt.After(started.Add(time.Hour)) {
		t.Fatalf("unexpected session: %#v", session)
	}
}

func TestOAuthTokenMapsSafeOAuthErrors(t *testing.T) {
	c := newAuthClient("https://navigara.example")
	c.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"invalid_grant","error_description":"secret server detail"}`))}, nil
	})}
	_, err := c.oauthToken(context.Background(), "private-code", "http://127.0.0.1:1/callback", "private-verifier", time.Now())
	if err == nil || !strings.Contains(err.Error(), "expired") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe OAuth error: %v", err)
	}
}

func TestOAuthTokenRejectsInvalidResponses(t *testing.T) {
	for _, response := range []string{
		`{"access_token":"token","token_type":"Bearer","expires_in":0}`,
		`{"access_token":"token","token_type":"other","expires_in":60}`,
		`{"access_token":"token","token_type":"Bearer","expires_in":60,"scope":"other"}`,
	} {
		t.Run(response, func(t *testing.T) {
			c := newAuthClient("https://navigara.example")
			c.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
			})}
			if _, err := c.oauthToken(context.Background(), "code", "http://127.0.0.1:1/callback", "verifier", time.Now()); err == nil {
				t.Fatal("accepted invalid token response")
			}
		})
	}
}

func TestOAuthAuthorizationCancellationIsSafe(t *testing.T) {
	err := oauthAuthorizationError("access_denied")
	if !strings.Contains(err.Error(), "denied") || strings.Contains(err.Error(), "error_description") {
		t.Fatalf("unsafe cancellation error: %v", err)
	}
}

func TestOAuthCallbackValidation(t *testing.T) {
	state := "expected-state"
	issuer := "https://navigara.example"
	for name, query := range map[string]url.Values{
		"valid code":         {"state": {state}, "iss": {issuer}, "code": {"code"}},
		"valid denial":       {"state": {state}, "iss": {issuer}, "error": {"access_denied"}},
		"wrong state":        {"state": {"other"}, "iss": {issuer}, "code": {"code"}},
		"missing issuer":     {"state": {state}, "code": {"code"}},
		"wrong issuer":       {"state": {state}, "iss": {"https://other.example"}, "code": {"code"}},
		"duplicate issuer":   {"state": {state}, "iss": {issuer, issuer}, "code": {"code"}},
		"duplicate state":    {"state": {state, state}, "iss": {issuer}, "code": {"code"}},
		"empty code":         {"state": {state}, "iss": {issuer}, "code": {""}},
		"code and error":     {"state": {state}, "iss": {issuer}, "code": {"code"}, "error": {"access_denied"}},
		"duplicate detail":   {"state": {state}, "iss": {issuer}, "error": {"access_denied"}, "error_description": {"a", "b"}},
		"unknown extensions": {"state": {state}, "iss": {issuer}, "code": {"code"}, "extension": {"", "value"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := validOAuthCallback(query, state, issuer)
			want := name == "valid code" || name == "valid denial" || name == "duplicate detail" || name == "unknown extensions"
			if got != want {
				t.Fatalf("validOAuthCallback() = %v, want %v", got, want)
			}
		})
	}
}

func TestOAuthCallbackQueryRejectsMalformedEncoding(t *testing.T) {
	if _, ok := parseOAuthCallbackQuery("state=state&iss=https%3A%2F%2Fnavigara.example&code=%zz"); ok {
		t.Fatal("accepted malformed callback query")
	}
}
