package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/navigaracom/navigara-cli/internal/telemetry"
)

func TestUploadRequiresPolicyBeforeCreatingOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "out")
	var out, errs bytes.Buffer
	code := run([]string{"sessions", "upload", "--send", "--url", srv.URL, "--token", "secret", "--output", dir}, &out, &errs)
	if code != 1 || !strings.Contains(errs.String(), "deduplication") {
		t.Fatalf("exit=%d: %s", code, errs.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("output created before policy check")
	}
}

func TestSenderRejectsRedirectsAndPartialAcceptance(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	for _, body := range []string{"redirect", `{"partialSuccess":{"rejectedDataPoints":"2"}}`, `not json`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if body == "redirect" {
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
					return
				}
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			err := newSender(srv.URL, "secret").post(telemetry.Body{Path: "/v1/metrics", Data: []byte(`{}`)})
			if err == nil {
				t.Fatal("must reject redirect/partial/invalid response")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("token in error")
			}
		})
	}
	if leaked {
		t.Fatal("followed redirect")
	}
}

func TestSenderRequiresSecureDestination(t *testing.T) {
	for _, base := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com?token=secret", "https://example.com#fragment"} {
		s := newSender(base, "secret")
		// No connection is needed: reject unsafe URL before making any request.
		if err := s.validate(); err == nil {
			t.Errorf("accepted %q", base)
		}
	}
}

func TestSetupProbeRequiresSessionPolicy(t *testing.T) {
	for _, policy := range []string{"", "session-live-wins-v1"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/otlp/v1/metrics" || r.Header.Get("Authorization") != "Bearer secret" {
				t.Errorf("wrong request")
			}
			w.Header().Set("X-Navigara-Backfill-Policy", policy)
			fmt.Fprint(w, `{}`)
		}))
		s := newSender(srv.URL+"/api", "secret")
		err := s.checkSetup()
		srv.Close()
		if (err == nil) != (policy != "") {
			t.Fatalf("policy %q: %v", policy, err)
		}
		if s.sent != 0 {
			t.Fatal("probe counted as uploaded usage")
		}
	}
}
