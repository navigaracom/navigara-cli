package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/navigaracom/navigara-cli/internal/telemetry"
)

func TestUploadRequiresPolicyBeforeCreatingOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) }))
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "out")
	var out, errs bytes.Buffer
	code := run(context.Background(), []string{"sessions", "upload", "--send", "--url", srv.URL, "--token", "secret", "--output", dir}, &out, &errs)
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
			err := newSender(srv.URL, "secret").post(context.Background(), telemetry.Body{Path: "/v1/metrics", Data: []byte(`{}`)})
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
		err := s.checkSetup(context.Background())
		srv.Close()
		if (err == nil) != (policy != "") {
			t.Fatalf("policy %q: %v", policy, err)
		}
		if s.sent != 0 {
			t.Fatal("probe counted as uploaded usage")
		}
	}
}

func TestSenderStopsWhenCancelledDuringRequest(t *testing.T) {
	release := make(chan struct{})
	requests := make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		requests <- struct{}{}
		<-release
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-requests
		cancel()
	}()
	start := time.Now()
	err := newSender(srv.URL, "secret").post(ctx, telemetry.Body{Path: "/v1/metrics", Data: []byte(`{}`)})
	if !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v after %v, want prompt context.Canceled", err, time.Since(start))
	}
	if len(requests) != 0 {
		t.Fatal("retried after cancellation")
	}
}

func TestSenderStopsWhenCancelledDuringBackoff(t *testing.T) {
	attempts := make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts <- struct{}{}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-attempts
		time.Sleep(50 * time.Millisecond) // The first backoff is two seconds.
		cancel()
	}()
	start := time.Now()
	err := newSender(srv.URL, "secret").post(ctx, telemetry.Body{Path: "/v1/metrics", Data: []byte(`{}`)})
	if !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Fatalf("err=%v after %v, want context.Canceled during backoff", err, time.Since(start))
	}
	if len(attempts) != 0 {
		t.Fatal("retried after cancellation")
	}
}

func TestSenderKeepsTransportErrorWithoutToken(t *testing.T) {
	s := newSender("https://collector.example", "secret")
	s.client.Transport = &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("dial refused")
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := s.post(ctx, telemetry.Body{Path: "/v1/metrics", Data: []byte(`{}`)})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "dial refused") {
		t.Fatalf("err=%v, want the deadline and the last transport error", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("token in error")
	}
}

func TestSenderReportsCancellationWhileReadingResponse(t *testing.T) {
	headers := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(headers)
		<-release // Never finish the body.
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-headers
		time.Sleep(50 * time.Millisecond) // Let the client start reading the body.
		cancel()
	}()
	err := newSender(srv.URL, "secret").post(ctx, telemetry.Body{Path: "/v1/metrics", Data: []byte(`{}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled while reading a 200 response", err)
	}
}
