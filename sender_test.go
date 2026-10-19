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
	"sync"
	"sync/atomic"
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
	if code != 1 || !strings.Contains(errs.String(), "Your Navigara server is too old for this CLI. Ask your admin to update Navigara.") {
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

func TestSenderBackfillReceipt(t *testing.T) {
	for _, test := range []struct {
		name, ack, count, reason, response string
		fail                               bool
		summary                            string
	}{
		{"legacy ignores stray count", "", "5", "", `{}`, false, "Navigara did not report how many Codex usage records were skipped.\n"},
		{"zero", "v1", "0", "", `{}`, false, ""},
		{"omitted", "v1", "3", "overlap-unverified-v1", `{}`, false, "3 Codex usage records were skipped because they may overlap live data.\n"},
		{"negative", "v1", "-1", "", `{}`, true, ""},
		{"missing", "v1", "", "", `{}`, true, ""},
		{"signed", "v1", "+1", "overlap-unverified-v1", `{}`, true, ""},
		{"overflow", "v1", "99999999999999999999999", "", `{}`, true, ""},
		{"unknown reason", "v1", "3", "other", `{}`, true, ""},
		{"unknown version", "v2", "0", "", `{}`, true, ""},
		{"partial success", "v1", "3", "overlap-unverified-v1", `{"partialSuccess":{"rejectedDataPoints":"3"}}`, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newSender("https://collector.example", "secret")
			s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("X-Navigara-Backfill-Receipt") != "v1" {
					t.Error("receipt not requested")
				}
				header := http.Header{}
				header.Set("X-Navigara-Backfill-Receipt", test.ack)
				header.Set("X-Navigara-Backfill-Skipped-Codex-Datapoints", test.count)
				header.Set("X-Navigara-Backfill-Codex-Skip-Reason", test.reason)
				return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(test.response))}, nil
			})
			err := s.post(context.Background(), telemetry.Body{Path: "/v1/metrics", Data: []byte(`{}`)})
			if (err != nil) != test.fail {
				t.Fatalf("error=%v", err)
			}
			if test.fail && s.sent != 0 {
				t.Fatal("invalid receipt counted as accepted")
			}
			if !test.fail && s.codexSummary() != test.summary {
				t.Fatal(s.codexSummary())
			}
		})
	}
}

func TestSenderParallelReceiptsReplaceResentBatch(t *testing.T) {
	s := newSender("https://collector.example", "secret")
	s.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		header := http.Header{}
		header.Set("X-Navigara-Backfill-Receipt", "v1")
		header.Set("X-Navigara-Backfill-Skipped-Codex-Datapoints", "2")
		header.Set("X-Navigara-Backfill-Codex-Skip-Reason", "overlap-unverified-v1")
		return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			if err := s.post(context.Background(), telemetry.Body{Path: "/v1/metrics", Data: []byte(fmt.Sprintf(`{"batch":%d}`, i%4))}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	workers.Wait()
	if s.sent != 8 || !strings.Contains(s.codexSummary(), "8 Codex usage records were skipped") {
		t.Fatalf("sent=%d, %s", s.sent, s.codexSummary())
	}
	s.codexReceipts[[32]byte{}] = -1
	if !strings.Contains(s.codexSummary(), "did not report") {
		t.Fatal("missing receipt became zero")
	}
}

func TestSenderReportsEachRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	s := newSender(srv.URL, "secret")
	var lines []string
	s.retried = func(line string) { lines = append(lines, line) }
	if err := s.post(context.Background(), telemetry.Body{Path: "/v1/metrics", Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	want := []string{"Upload failed (HTTP 500), retrying in 2 s (attempt 1 of 5)…"}
	if fmt.Sprint(lines) != fmt.Sprint(want) {
		t.Fatalf("retry lines=%q, want %q", lines, want)
	}
}

func TestSenderExplainsRejectedKeys(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusUnauthorized: "Navigara rejected the key (expired or revoked). Run navigara login, or create a new Analytics Ingestor key.",
		http.StatusForbidden:    "Navigara rejected the key: it does not have the Analytics Ingestor role. Create a new Analytics Ingestor key.",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		err := newSender(srv.URL, "secret").checkSetup(context.Background())
		srv.Close()
		if err == nil || err.Error() != want || !errors.Is(err, errIngestionAuthRejected) {
			t.Fatalf("HTTP %d: err=%v, want %q", status, err, want)
		}
	}
}

func TestUploadExplainsRejectedExplicitKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer srv.Close()
	var out, errs bytes.Buffer
	code := run(context.Background(), []string{"sessions", "upload", "--send", "--url", srv.URL, "--token", "secret", "--output", filepath.Join(t.TempDir(), "out")}, &out, &errs)
	want := "navigara: Navigara rejected the key (expired or revoked). Create a new Analytics Ingestor key, or remove --url/--token/NAVIGARA_TOKEN to use your saved login.\n"
	if code != 1 || errs.String() != want {
		t.Fatalf("exit=%d stderr=%q, want %q", code, errs.String(), want)
	}
}

func TestSetupProbeFailsFastWhenConnectionIsRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host := ln.Addr().String()
	ln.Close()
	s := newSender("http://"+host, "secret")
	s.retried = func(line string) { t.Errorf("unexpected retry: %s", line) }
	start := time.Now()
	err = s.checkSetup(context.Background())
	want := "Cannot reach " + host + ". Check the address and your network."
	if err == nil || err.Error() != want || time.Since(start) > time.Second {
		t.Fatalf("err=%v after %v, want %q", err, time.Since(start), want)
	}
}

func TestSetupProgressPrintsRetryOutsidePlainOutput(t *testing.T) {
	var out, errs bytes.Buffer
	u := newSetupProgress(newSetupInput(strings.NewReader(""), &out))
	u.stderr = &errs
	u.upload(0, 2)
	u.retry("Upload failed (HTTP 500), retrying in 2 s (attempt 1 of 5)…")
	if errs.String() != "Upload failed (HTTP 500), retrying in 2 s (attempt 1 of 5)…\n" || strings.Contains(out.String(), "retrying") {
		t.Fatalf("stdout=%q stderr=%q", out.String(), errs.String())
	}
}

func TestUploadPrintsRetriesToCommandStderr(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	var out, errs bytes.Buffer
	run(context.Background(), []string{"sessions", "upload", "--send", "--url", srv.URL, "--token", "secret", "--output", filepath.Join(t.TempDir(), "out")}, &out, &errs)
	if !strings.Contains(errs.String(), "Upload failed (HTTP 503), retrying in 2 s (attempt 1 of 5)…\n") {
		t.Fatalf("stderr=%q", errs.String())
	}
}
