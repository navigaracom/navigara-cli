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
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	query := url.Values{"port": {port}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}}
	loginURL := c.host + "/cli/login?" + query.Encode()
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
		q := r.URL.Query()
		if r.Host != listener.Addr().String() || len(q["state"]) != 1 || q.Get("state") != state || len(q["code"]) != 1 || q.Get("code") == "" || len(q.Get("code")) > 8192 {
			http.Error(w, "Invalid login callback. Return to the terminal and try again.", http.StatusBadRequest)
			return
		}
		handled := false
		once.Do(func() {
			handled = true
			var session loginSession
			err := c.request(ctx, "POST", "/auth/cli/exchange", "", map[string]string{"code": q.Get("code"), "codeVerifier": verifier}, &session)
			if err == nil && (session.SessionToken == "" || !session.ExpiresAt.After(time.Now())) {
				err = errors.New("Navigara returned an invalid session")
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
	fmt.Fprintf(out, "Sign in to Navigara in your browser:\n%s\n", loginURL)
	if err := open(loginURL); err != nil {
		fmt.Fprintln(out, "Could not open a browser. Open the URL above on this computer.")
	}
	select {
	case result := <-done:
		return result.session, result.err
	case <-ctx.Done():
		return loginSession{}, errors.New("login cancelled or timed out; run navigara login again")
	}
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
