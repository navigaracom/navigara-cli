package main

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path+":"+string(mustBody(r)))
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	d := writeBatchFixture(t, srv.URL+"/api")
	if err := os.WriteFile(filepath.Join(d, "after-manifest.txt"), []byte("ignored"), 0600); err != nil {
		t.Fatal(err)
	}
	s := newSender(srv.URL+"/api", "secret-token")
	if err := sendSetupBatches(d, s); err != nil {
		t.Fatal(err)
	}
	if err := sendSetupBatches(d, s); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0] != got[2] || got[1] != got[3] {
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
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++; fmt.Fprint(w, `{}`) }))
			defer srv.Close()
			d := writeBatchFixture(t, srv.URL+"/api")
			tc.change(d)
			if err := sendSetupBatches(d, newSender(srv.URL+"/api", "secret")); err == nil {
				t.Fatal("accepted invalid batch")
			}
			if requests != 0 {
				t.Fatalf("requested before validation: %d", requests)
			}
		})
	}
	t.Run("destination", func(t *testing.T) {
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
		defer srv.Close()
		d := writeBatchFixture(t, srv.URL+"/api")
		if err := sendSetupBatches(d, newSender(srv.URL+"/other", "secret")); err == nil {
			t.Fatal("accepted destination mismatch")
		}
		if requests != 0 {
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
