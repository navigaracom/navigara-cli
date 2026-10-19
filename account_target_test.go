package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestVerifiedAccountRequiresSavedAuthAndDoesNotWrite(t *testing.T) {
	withConfigHome(t)
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	c := accountConfig{Version: 1, Host: server.URL, UserID: "user-1", OrganizationID: "org-1", Email: "old@example.com"}
	if err := saveAccount(c); err != nil {
		t.Fatal(err)
	}
	before, err := loadAccount("")
	if err != nil {
		t.Fatal(err)
	}
	_, err = verifiedAccount(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "navigara login") {
		t.Fatalf("expected login guidance, got %v", err)
	}
	after, err := loadAccount("")
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("verification wrote config: before=%#v after=%#v err=%v", before, after, err)
	}
}

func TestVerifiedAccountRefreshesServerValuesAndChecksMembership(t *testing.T) {
	withConfigHome(t)
	var orgRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/session":
			io.WriteString(w, `{"userId":"user-1","email":"new@example.com"}`)
		case "/api/v1/auth/organizations":
			orgRequests++
			if r.URL.Query().Get("page_token") == "next" {
				io.WriteString(w, `{"organizations":[{"id":"org-1","name":"Fresh name","status":"active"}]}`)
				return
			}
			io.WriteString(w, `{"organizations":[],"nextPageToken":"next"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := accountConfig{Version: 1, Host: server.URL, SessionToken: "session-secret", SessionExpiresAt: time.Now().Add(time.Hour), UserID: "user-1", Email: "old@example.com", OrganizationID: "org-1", OrganizationName: "Old"}
	if err := saveAccount(c); err != nil {
		t.Fatal(err)
	}
	got, err := verifiedAccount(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "new@example.com" || got.OrganizationName != "Fresh name" || orgRequests != 2 {
		t.Fatalf("unexpected refreshed account %#v requests=%d", got, orgRequests)
	}
	saved, _ := loadAccount("")
	if saved.Email != "old@example.com" || saved.OrganizationName != "Old" {
		t.Fatal("verification persisted refreshed values")
	}
}

func TestEnsureIngestionMintsScopedKeyAndReusesIt(t *testing.T) {
	withConfigHome(t)
	var keyRequests, probes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/organizations/org-1/api-keys":
			keyRequests++
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer session-secret" {
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.RequestURI())
			}
			var body struct {
				Name string `json:"name"`
				Role string `json:"role"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name != "Navigara CLI" || body.Role != "roles/analytics.ingestor" {
				t.Fatalf("bad key request %#v err=%v", body, err)
			}
			json.NewEncoder(w).Encode(map[string]any{
				"plainTextKey": "ingest-secret",
				"apiKey":       map[string]any{"organizationId": "org-1", "expiresAt": time.Now().Add(time.Hour).UTC()},
			})
		case "/api/otlp/v1/metrics":
			probes++
			if r.Header.Get("Authorization") != "Bearer ingest-secret" {
				t.Fatal("probe needs scoped ingestion auth")
			}
			w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := accountConfig{Version: 1, Host: server.URL, SessionToken: "session-secret", OrganizationID: "org-1"}
	if err := saveAccount(c); err != nil {
		t.Fatal(err)
	}
	if err := ensureIngestion(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	if c.IngestToken != "ingest-secret" || keyRequests != 1 {
		t.Fatalf("mint failed: %#v requests=%d", c, keyRequests)
	}
	if err := ensureIngestion(context.Background(), &c); err != nil || keyRequests != 1 || probes != 1 {
		t.Fatalf("valid key was not reused: err=%v key requests=%d probes=%d", err, keyRequests, probes)
	}
}

func TestEnsureIngestionReplacesOnlyRejectedCachedKey(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			withConfigHome(t)
			var keyRequests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/otlp/v1/metrics":
					if r.Header.Get("Authorization") != "Bearer revoked-secret" {
						t.Fatal("probe used an unexpected key")
					}
					w.WriteHeader(status)
				case "/api/v1/organizations/org-1/api-keys":
					keyRequests++
					_ = json.NewEncoder(w).Encode(map[string]any{"plainTextKey": "replacement-secret", "apiKey": map[string]any{"organizationId": "org-1", "expiresAt": time.Now().Add(time.Hour)}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			c := accountConfig{Version: 1, Host: server.URL, SessionToken: "session-secret", OrganizationID: "org-1", IngestToken: "revoked-secret", IngestExpiresAt: time.Now().Add(time.Hour)}
			if err := saveAccount(c); err != nil {
				t.Fatal(err)
			}
			err := ensureIngestion(context.Background(), &c)
			if status == http.StatusBadRequest {
				if err == nil || keyRequests != 0 || c.IngestToken != "revoked-secret" {
					t.Fatalf("non-auth failure minted or changed the key: err=%v key requests=%d account=%#v", err, keyRequests, c)
				}
				return
			}
			if err != nil || keyRequests != 1 || c.IngestToken != "replacement-secret" {
				t.Fatalf("rejected key was not replaced: err=%v key requests=%d account=%#v", err, keyRequests, c)
			}
			saved, err := loadAccount("")
			if err != nil || saved.IngestToken != "replacement-secret" {
				t.Fatalf("replacement key was not saved: %#v err=%v", saved, err)
			}
		})
	}
}

func TestEnsureIngestionRejectsForbiddenAndWrongOrganization(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			withConfigHome(t)
			secret := "do-not-print-ingest-secret"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == http.StatusForbidden {
					w.WriteHeader(status)
					io.WriteString(w, secret)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"plainTextKey": secret, "apiKey": map[string]any{"organizationId": "other", "expiresAt": time.Now().Add(time.Hour)}})
			}))
			defer server.Close()
			c := accountConfig{Version: 1, Host: server.URL, SessionToken: "session-secret", OrganizationID: "org-1"}
			err := ensureIngestion(context.Background(), &c)
			if err == nil || strings.Contains(err.Error(), secret) {
				t.Fatalf("bad error: %v", err)
			}
		})
	}
}

func TestEnsureIngestionPreservesConcurrentLogin(t *testing.T) {
	withConfigHome(t)
	later := accountConfig{Version: 1, Host: "https://later.example", SessionToken: "new-session", OrganizationID: "new-org"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := saveAccount(later); err != nil {
			t.Error(err)
		}
		json.NewEncoder(w).Encode(map[string]any{"plainTextKey": "old-org-key", "apiKey": map[string]any{"organizationId": "org-1", "expiresAt": time.Now().Add(time.Hour)}})
	}))
	defer srv.Close()
	c := accountConfig{Version: 1, Host: srv.URL, SessionToken: "session-secret", OrganizationID: "org-1"}
	if err := saveAccount(c); err != nil {
		t.Fatal(err)
	}
	if err := ensureIngestion(context.Background(), &c); err == nil {
		t.Fatal("overwrote a later login")
	}
	saved, err := loadAccount("")
	if err != nil || saved.Host != later.Host || saved.SessionToken != later.SessionToken || saved.IngestToken != "" {
		t.Fatal("later account changed")
	}
}
