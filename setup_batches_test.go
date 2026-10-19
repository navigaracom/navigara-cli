package main

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func writeBatchFixture(t *testing.T, url string) string {
	t.Helper()
	d := t.TempDir()
	for name, body := range map[string]string{"metrics-2.json.gz": `{"m":2}`, "logs-1.json.gz": `{"l":1}`} {
		if err := os.WriteFile(filepath.Join(d, name), mustGzip(t, body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveSetupBatches(d, url, ""); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSetupManifestIsSortedHashedAndTokenFree(t *testing.T) {
	d := writeBatchFixture(t, "https://collector.example/api")
	raw, err := os.ReadFile(filepath.Join(d, "setup.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || strings.Contains(string(raw), "secret-token") {
		t.Fatal("invalid manifest")
	}
	var m setupManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != 2 || m.URL != "https://collector.example/api" || len(m.Batches) != 2 {
		t.Fatalf("manifest: %#v", m)
	}
	if got := []string{m.Batches[0].Name, m.Batches[1].Name}; !reflect.DeepEqual(got, []string{"logs-1.json.gz", "metrics-2.json.gz"}) {
		t.Fatal(got)
	}
	for _, b := range m.Batches {
		sum := sha256.Sum256(mustRead(t, filepath.Join(d, b.Name)))
		if b.SHA256 != hex.EncodeToString(sum[:]) || b.SHA256 == "secret-token" {
			t.Fatal("bad hash")
		}
	}
}

func TestSetupBatchesRetryExactBodiesAndNoRescan(t *testing.T) {
	var got []string
	var mu sync.Mutex
	d := writeBatchFixture(t, "http://127.0.0.1/api")
	if err := os.WriteFile(filepath.Join(d, "after-manifest.txt"), []byte("ignored"), 0600); err != nil {
		t.Fatal(err)
	}
	s := newSender("http://127.0.0.1/api", "secret-token")
	s.client.Transport = parallelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(body))
		mu.Unlock()
		return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	if err := sendSetupBatches(context.Background(), d, s, nil, false); err != nil {
		t.Fatal(err)
	}
	if err := sendSetupBatches(context.Background(), d, s, nil, false); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("retry changed files: %#v", got)
	}
	first, second := append([]string(nil), got[:2]...), append([]string(nil), got[2:]...)
	sort.Strings(first)
	sort.Strings(second)
	if !reflect.DeepEqual(first, second) || first[0] == first[1] {
		t.Fatalf("retry changed files: %#v", got)
	}
}

func TestSetupBatchesRejectInvalidBeforeRequest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(string)
	}{
		{"tampered", func(d string) { os.WriteFile(filepath.Join(d, "logs-1.json.gz"), mustGzip(t, `{"changed":1}`), 0600) }},
		{"missing", func(d string) { os.Remove(filepath.Join(d, "logs-1.json.gz")) }},
		{"symlink", func(d string) {
			os.Remove(filepath.Join(d, "logs-1.json.gz"))
			os.Symlink("metrics-2.json.gz", filepath.Join(d, "logs-1.json.gz"))
		}},
		{"older manifest", func(d string) {
			raw := strings.Replace(string(mustRead(t, filepath.Join(d, "setup.json"))), `"Version": 2`, `"Version": 1`, 1)
			os.WriteFile(filepath.Join(d, "setup.json"), []byte(raw), 0600)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			d := writeBatchFixture(t, "http://127.0.0.1/api")
			tc.change(d)
			s := newSender("http://127.0.0.1/api", "secret")
			s.client.Transport = parallelRoundTripFunc(func(*http.Request) (*http.Response, error) {
				requests.Add(1)
				return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
			})
			if err := sendSetupBatches(context.Background(), d, s, nil, false); err == nil {
				t.Fatal("accepted invalid batch")
			}
			if requests.Load() != 0 {
				t.Fatalf("requested before validation: %d", requests.Load())
			}
		})
	}
	t.Run("destination", func(t *testing.T) {
		var requests atomic.Int32
		d := writeBatchFixture(t, "http://127.0.0.1/api")
		s := newSender("http://127.0.0.1/other", "secret")
		s.client.Transport = parallelRoundTripFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Header: make(http.Header)}, nil
		})
		if err := sendSetupBatches(context.Background(), d, s, nil, false); err == nil {
			t.Fatal("accepted destination mismatch")
		}
		if requests.Load() != 0 {
			t.Fatal("requested on destination mismatch")
		}
	})
}

func TestSetupBatchesRejectUncompressedBodies(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "logs-1.json.gz"), []byte(`{"l":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveSetupBatches(d, "https://collector.example/api", ""); err == nil {
		t.Fatal("accepted an uncompressed batch")
	}
}

func mustGzip(t *testing.T, body string) []byte {
	t.Helper()
	data, err := gzipBody([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSetupBatchesReportOnlyAcceptedProgress(t *testing.T) {
	d := writeBatchFixture(t, "http://127.0.0.1/api")
	var calls atomic.Int32
	s := newSender("http://127.0.0.1/api", "secret")
	s.client.Transport = parallelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		status := http.StatusOK
		if calls.Add(1) == 2 {
			status = http.StatusBadRequest
		}
		return &http.Response{StatusCode: status, Body: http.NoBody, Header: make(http.Header)}, nil
	})
	var progress [][2]int
	err := sendSetupBatches(context.Background(), d, s, func(done, total int) {
		progress = append(progress, [2]int{done, total})
	}, false)
	if err == nil {
		t.Fatal("accepted a rejected batch")
	}
	if !reflect.DeepEqual(progress, [][2]int{{0, 2}, {1, 2}}) {
		t.Fatalf("progress = %#v", progress)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// mustBody reads a request body, decoding a gzip-compressed upload.
func mustBody(r *http.Request) []byte {
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			return nil
		}
		body = zr
	}
	b, _ := io.ReadAll(body)
	return b
}

func TestSetupBatchesResumeAfterCancellation(t *testing.T) {
	var mu sync.Mutex
	var got []string
	hold := true
	arrived := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		held := hold
		if !held {
			got = append(got, r.URL.Path)
		}
		mu.Unlock()
		if held { // Hold the upload until the client gives up.
			arrived <- struct{}{}
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	d := writeBatchFixture(t, srv.URL+"/api")
	s := newSender(srv.URL+"/api", "secret")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-arrived
		cancel()
	}()
	if err := sendSetupBatches(ctx, d, s, nil, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	mu.Lock()
	hold = false
	mu.Unlock()
	if err := sendSetupBatches(context.Background(), d, s, nil, false); err != nil {
		t.Fatalf("resume after cancellation: %v", err)
	}
	// A request from the cancelled run may still reach the server late; the receiver drops
	// repeats, so the resume only has to deliver every batch.
	mu.Lock()
	defer mu.Unlock()
	sort.Strings(got)
	if want := []string{"/api/otlp/v1/logs", "/api/otlp/v1/metrics"}; !reflect.DeepEqual(slices.Compact(got), want) {
		t.Fatalf("resumed requests = %v, want each of %v", got, want)
	}
}
