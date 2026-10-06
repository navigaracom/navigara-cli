package main

import (
	"context"
	"io"
	"net/http"
	"testing"
)

func TestUploadUsesSavedAccountOnlyWithoutExplicitTransport(t *testing.T) {
	withConfigHome(t)
	var probes, dataRequests int
	srv := setupAccountServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/otlp/v1/metrics" {
			dataRequests++
			t.Error("unexpected ingestion request")
			return
		}
		if body, _ := io.ReadAll(r.Body); string(body) != "{}" || r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Error("unexpected saved-key probe")
		}
		probes++
		w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	for _, mode := range []string{"saved", "override-email", "anonymous", "explicit", "url-only", "token-only", "other-host", "offline"} {
		t.Run(mode, func(t *testing.T) {
			opts := options{send: true}
			switch mode {
			case "override-email":
				opts.email = "other@example.com"
			case "anonymous":
				opts.allowAnonymous = true
			case "explicit":
				opts.url = "https://explicit.example/api"
				opts.token = "explicit-secret"
			case "url-only":
				opts.url = "https://explicit.example/api"
			case "token-only":
				opts.token = "explicit-secret"
			case "other-host":
				opts.host = "https://other.example"
			case "offline":
				opts.send = false
			}
			err := prepareUploadAccount(context.Background(), &opts)
			bad := mode == "url-only" || mode == "token-only" || mode == "other-host"
			if (err != nil) != bad {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
			if bad || mode == "offline" {
				if opts.token == "test-secret" {
					t.Fatal("saved key leaked to alternate destination")
				}
				return
			}
			if mode == "explicit" {
				if opts.token != "explicit-secret" || opts.email != "" {
					t.Fatal("explicit transport changed")
				}
				return
			}
			if opts.url != srv.URL+"/api" || opts.token != "test-secret" {
				t.Fatal("wrong saved transport")
			}
			want := "dev@example.com"
			if mode == "override-email" {
				want = "other@example.com"
			}
			if mode == "anonymous" {
				want = ""
			}
			if opts.email != want {
				t.Errorf("email=%q want=%q", opts.email, want)
			}
		})
	}
	if probes != 3 || dataRequests != 0 {
		t.Fatalf("probes=%d data requests=%d", probes, dataRequests)
	}
}
