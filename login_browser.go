package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

type loginSession struct {
	SessionToken string    `json:"sessionToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

func (c *authClient) browserLogin(ctx context.Context, out io.Writer, open func(string) error) (loginSession, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	issuer, err := canonicalHost(c.host)
	if err != nil {
		return loginSession{}, err
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return loginSession{}, errors.New("cannot open local login callback")
	}
	defer listener.Close()
	random := make([]byte, 64)
	if _, err = rand.Read(random); err != nil {
		return loginSession{}, err
	}
	state := base64.RawURLEncoding.EncodeToString(random[:32])
	verifier := base64.RawURLEncoding.EncodeToString(random[32:])
	hash := sha256.Sum256([]byte(verifier))
	redirectURI := "http://" + listener.Addr().String() + "/callback"
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {oauthClientID},
		"redirect_uri":          {redirectURI},
		"scope":                 {oauthScope},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(hash[:])},
		"code_challenge_method": {"S256"},
	}
	loginURL := c.host + "/oauth/authorize?" + query.Encode()
	type reply struct {
		session loginSession
		err     error
	}
	done := make(chan reply, 1)
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		q, queryOK := parseOAuthCallbackQuery(r.URL.RawQuery)
		if r.Host != listener.Addr().String() || !queryOK || !validOAuthCallback(q, state, issuer) {
			http.Error(w, "Invalid login callback. Return to the terminal and try again.", http.StatusBadRequest)
			return
		}
		handled := false
		once.Do(func() {
			handled = true
			var session loginSession
			var err error
			if q.Get("error") != "" {
				err = oauthAuthorizationError(q.Get("error"))
			} else {
				requestStart := time.Now()
				session, err = c.oauthToken(ctx, q.Get("code"), redirectURI, verifier, requestStart)
			}
			if err != nil {
				http.Error(w, "Login failed. Return to the terminal and try again.", http.StatusBadGateway)
			} else {
				fmt.Fprintln(w, "Navigara CLI is connected. You can close this tab.")
			}
			done <- reply{session, err}
		})
		if !handled {
			http.Error(w, "Login callback already received.", http.StatusConflict)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16384}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	waitCtx, stop := interruptible(ctx)
	defer stop()
	fmt.Fprintf(out, "Sign in to Navigara in your browser:\n%s\n", loginURL)
	if err := open(loginURL); err != nil {
		fmt.Fprintln(out, "Could not open a browser. Open the URL above on this computer.")
	}
	select {
	case result := <-done:
		return result.session, result.err
	case <-waitCtx.Done():
		return loginSession{}, errors.New("login cancelled or timed out; run navigara login again")
	}
}

func validOAuthCallback(q url.Values, state, issuer string) bool {
	for _, key := range []string{"state", "iss", "code", "error"} {
		if values, ok := q[key]; ok && (len(values) != 1 || values[0] == "") {
			return false
		}
	}
	if len(q["state"]) != 1 || q.Get("state") != state || len(q["iss"]) != 1 || q.Get("iss") != issuer {
		return false
	}
	hasCode, hasError := q.Get("code") != "", q.Get("error") != ""
	if hasCode == hasError || (hasCode && len(q.Get("code")) > maxOAuthToken) {
		return false
	}
	return true
}

func parseOAuthCallbackQuery(rawQuery string) (url.Values, bool) {
	query, err := url.ParseQuery(rawQuery)
	return query, err == nil
}

func oauthAuthorizationError(code string) error {
	if code == "access_denied" {
		return errors.New("sign-in was denied; run navigara login again")
	}
	return errors.New("Navigara could not finish sign-in; run navigara login again")
}

func openBrowser(raw string) error {
	program := "xdg-open"
	if runtime.GOOS == "darwin" {
		program = "open"
	}
	cmd := exec.Command(program, raw)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
