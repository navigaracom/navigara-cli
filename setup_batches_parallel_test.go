package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type parallelRoundTripFunc func(*http.Request) (*http.Response, error)

func (f parallelRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSetupBatchesUsesFourWorkersAndReportsAcceptedProgress(t *testing.T) {
	d := writeBatchFixture(t, "http://127.0.0.1/api")
	var active, maxActive atomic.Int32
	var calls atomic.Int32
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	s := newSender("http://127.0.0.1/api", "secret")
	s.client.Transport = parallelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		now := active.Add(1)
		for {
			old := maxActive.Load()
			if now <= old || maxActive.CompareAndSwap(old, now) {
				break
			}
		}
		calls.Add(1)
		started <- struct{}{}
		<-release
		active.Add(-1)
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	if err := os.WriteFile(filepath.Join(d, "logs-3.json.gz"), mustGzip(t, `{"l":3}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "metrics-4.json.gz"), mustGzip(t, `{"m":4}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(d, "setup.json")); err != nil {
		t.Fatal(err)
	}
	if err := saveSetupBatches(d, "http://127.0.0.1/api", ""); err != nil {
		t.Fatal(err)
	}
	var progress [][2]int
	done := make(chan error, 1)
	go func() {
		done <- sendSetupBatches(d, s, func(n, total int) { progress = append(progress, [2]int{n, total}) })
	}()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("workers did not reach the transport barrier")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if maxActive.Load() > 4 || calls.Load() != 4 {
		t.Fatalf("active=%d calls=%d; want four bounded workers", maxActive.Load(), calls.Load())
	}
	if len(progress) != 5 || progress[0] != [2]int{0, 4} {
		t.Fatalf("progress=%v; want initial zero callback and four accepted updates", progress)
	}
	for i, p := range progress[1:] {
		if p[0] != i+1 || p[1] != 4 {
			t.Fatalf("progress[%d]=%v; want sequential accepted progress", i, p)
		}
	}
}

func TestSetupBatchesFatalAuthCancelsOtherRequests(t *testing.T) {
	d := writeBatchFixture(t, "http://127.0.0.1/api")
	var calls atomic.Int32
	s := newSender("http://127.0.0.1/api", "secret")
	s.client.Transport = parallelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{StatusCode: http.StatusForbidden, Body: http.NoBody, Header: make(http.Header)}, nil
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	err := sendSetupBatches(d, s, nil)
	if err == nil || !strings.Contains(err.Error(), "analytics ingestor role") {
		t.Fatalf("error=%v; want original auth rejection", err)
	}
	if calls.Load() > 4 {
		t.Fatalf("calls=%d; want at most four active requests", calls.Load())
	}
}
