package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type parallelRoundTripFunc func(*http.Request) (*http.Response, error)

func (f parallelRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSetupBatchesUsesFourWorkersAndReportsAcceptedProgress(t *testing.T) {
	d := writeBatchFixture(t, "http://127.0.0.1/api")
	if err := os.Remove(filepath.Join(d, "setup.json")); err != nil {
		t.Fatal(err)
	}
	for i := 3; i <= 9; i++ {
		kind := "logs"
		if i%2 == 0 {
			kind = "metrics"
		}
		name := filepath.Join(d, kind+"-"+fmt.Sprint(i)+".json.gz")
		if err := os.WriteFile(name, mustGzip(t, fmt.Sprintf(`{"%s":%d}`, kind[:1], i)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveSetupBatches(d, "http://127.0.0.1/api", ""); err != nil {
		t.Fatal(err)
	}
	var active, maxActive atomic.Int32
	var calls atomic.Int32
	started := make(chan struct{}, 4)
	completed := make(chan int, 4)
	release := make([]chan struct{}, 4)
	for i := range release {
		release[i] = make(chan struct{})
	}
	var gotBodies [][]byte
	var bodiesMu sync.Mutex
	s := newSender("http://127.0.0.1/api", "secret")
	s.client.Transport = parallelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		now := active.Add(1)
		for {
			old := maxActive.Load()
			if now <= old || maxActive.CompareAndSwap(old, now) {
				break
			}
		}
		call := int(calls.Add(1))
		body, _ := io.ReadAll(r.Body)
		bodiesMu.Lock()
		gotBodies = append(gotBodies, body)
		bodiesMu.Unlock()
		if call <= 4 {
			started <- struct{}{}
			<-release[call-1]
			completed <- call
		}
		active.Add(-1)
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	var progress [][2]int
	done := make(chan error, 1)
	go func() {
		done <- sendSetupBatches(context.Background(), d, s, func(n, total int) { progress = append(progress, [2]int{n, total}) })
	}()
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("workers did not reach the transport barrier")
		}
	}
	// Complete the first four in reverse order and observe each return before the next release.
	for i := 3; i >= 0; i-- {
		close(release[i])
		select {
		case call := <-completed:
			if call != i+1 {
				t.Fatalf("completed call %d; want %d", call, i+1)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("call %d did not return after release", i+1)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if maxActive.Load() > 4 || calls.Load() != 9 {
		t.Fatalf("active=%d calls=%d; want four bounded workers and nine uploads", maxActive.Load(), calls.Load())
	}
	if len(progress) != 10 || progress[0] != [2]int{0, 9} {
		t.Fatalf("progress=%v; want initial zero callback and nine accepted updates", progress)
	}
	for i, p := range progress[1:] {
		if p[0] != i+1 || p[1] != 9 {
			t.Fatalf("progress[%d]=%v; want sequential accepted progress", i, p)
		}
	}
	m, err := loadSetupBatches(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, batch := range m.Batches {
		want, err := os.ReadFile(filepath.Join(d, batch.Name))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, got := range gotBodies {
			if bytes.Equal(want, got) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("request body for %s changed", batch.Name)
		}
	}
}

func TestSetupBatchesFatalAuthCancelsOtherRequests(t *testing.T) {
	d := writeBatchFixture(t, "http://127.0.0.1/api")
	if err := os.Remove(filepath.Join(d, "setup.json")); err != nil {
		t.Fatal(err)
	}
	for i := 3; i <= 9; i++ {
		kind := "logs"
		if i%2 == 0 {
			kind = "metrics"
		}
		if err := os.WriteFile(filepath.Join(d, fmt.Sprintf("%s-%d.json.gz", kind, i)), mustGzip(t, fmt.Sprintf(`{"%s":%d}`, kind[:1], i)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveSetupBatches(d, "http://127.0.0.1/api", ""); err != nil {
		t.Fatal(err)
	}
	beforeManifest, err := os.ReadFile(filepath.Join(d, "setup.json"))
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var got429, got403, canceled atomic.Bool
	peerStarted := make(chan struct{})
	var peerOnce sync.Once
	var bodiesMu sync.Mutex
	var bodies [][]byte
	s := newSender("http://127.0.0.1/api", "secret")
	s.client.Transport = parallelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		bodiesMu.Lock()
		bodies = append(bodies, body)
		bodiesMu.Unlock()
		if call == 1 {
			got429.Store(true)
			return &http.Response{StatusCode: http.StatusTooManyRequests, Body: http.NoBody, Header: make(http.Header)}, nil
		}
		if call == 2 {
			<-peerStarted
			got403.Store(true)
			return &http.Response{StatusCode: http.StatusForbidden, Body: http.NoBody, Header: make(http.Header)}, nil
		}
		peerOnce.Do(func() { close(peerStarted) })
		select {
		case <-r.Context().Done():
			canceled.Store(true)
			return nil, r.Context().Err()
		case <-time.After(3 * time.Second):
			t.Fatal("peer request was not canceled")
			return nil, nil
		}
	})
	err = sendSetupBatches(context.Background(), d, s, nil)
	if err == nil || !strings.Contains(err.Error(), "analytics ingestor role") {
		t.Fatalf("error=%v; want original auth rejection", err)
	}
	if !got429.Load() || !got403.Load() || !canceled.Load() {
		t.Fatalf("got429=%v got403=%v canceled=%v", got429.Load(), got403.Load(), canceled.Load())
	}
	if calls.Load() > 4 {
		t.Fatalf("calls=%d; want no fifth request after cancellation", calls.Load())
	}
	afterManifest, err := os.ReadFile(filepath.Join(d, "setup.json"))
	if err != nil || !bytes.Equal(beforeManifest, afterManifest) {
		t.Fatal("resume manifest changed after fatal upload")
	}
	for _, name := range []string{"logs-1.json.gz", "metrics-2.json.gz", "logs-3.json.gz", "metrics-4.json.gz"} {
		if _, err := os.Stat(filepath.Join(d, name)); err != nil {
			t.Fatalf("resume batch %s changed or disappeared: %v", name, err)
		}
	}
}
