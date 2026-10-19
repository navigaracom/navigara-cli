package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionsStatus(t *testing.T) {
	for _, tc := range []struct {
		name             string
		reject, codex    bool
		codexLogsRemoved bool
		claude, identity string
		code             int
		want, avoid      []string
	}{
		{name: "connected", codex: true, claude: "connected", code: 0,
			want: []string{"Claude Code: last import 2026-10-01T", "Codex: last import 2026-10-02T", "Login: http://127.0.0.1", "as dev@example.com, organization Acme", "Claude Code: connected to 127.0.0.1", "Codex: connected to 127.0.0.1:", "as o'neil@example.com", "Navigara accepts the Analytics Ingestor key"}},
		{name: "rejected key", reject: true, codex: true, claude: "connected", code: 1,
			want: []string{"Navigara does not accept the Analytics Ingestor key"}},
		{name: "codex absent", claude: "connected", code: 0,
			want: []string{"Codex: not found"}},
		{name: "claude not set up", codex: true, claude: "plain", code: 1,
			want: []string{"Claude Code: not connected: telemetry is not set up"}},
		{name: "codex logs exporter removed", codex: true, codexLogsRemoved: true, code: 1,
			want: []string{"Codex: not connected: the Navigara block in config.toml is not complete"}},
		{name: "claude exporters off", claude: "exporters off", code: 1,
			want: []string{"Claude Code: not connected: telemetry is not set up"}},
		{name: "claude grpc", claude: "grpc", code: 1,
			want: []string{"Claude Code: not connected: telemetry is not set up"}},
		{name: "codex without identity", codex: true, identity: "none", code: 1,
			want: []string{"Codex: not connected: no Navigara email block in"}},
		{name: "codex fish identity", codex: true, identity: "fish", code: 0,
			want: []string{"Codex: connected to 127.0.0.1:", "as o'neil@example.com"}},
		{name: "nothing found", code: 0,
			want: []string{"Claude Code: not found", "Codex: not found", "No Claude Code or Codex home found"}, avoid: []string{"connected to"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			claudeHome, codexHome := filepath.Join(home, ".claude"), filepath.Join(home, ".codex")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
			t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
			t.Setenv("CODEX_HOME", codexHome)
			t.Setenv("SHELL", "/bin/zsh")
			t.Setenv("ZDOTDIR", home)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.reject || r.URL.Path != "/api/otlp/v1/metrics" || r.Header.Get("Authorization") != "Bearer test-secret" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("X-Navigara-Backfill-Policy", "session-live-wins-v1")
			}))
			defer srv.Close()
			if err := saveAccount(accountConfig{Version: 1, Host: srv.URL, SessionToken: "session", Email: "dev@example.com", OrganizationName: "Acme", SessionExpiresAt: time.Now().Add(time.Hour),
				OrganizationID: "org", Imports: map[string]time.Time{"org/claude": time.Date(2026, 10, 1, 12, 0, 0, 0, time.Local), "org/codex": time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local), "other/claude": time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}}); err != nil {
				t.Fatal(err)
			}
			endpoint := otlpBase(srv.URL + "/api")
			write := func(path string, data []byte, err error) {
				t.Helper()
				if err == nil {
					err = os.MkdirAll(filepath.Dir(path), 0700)
				}
				if err == nil {
					err = os.WriteFile(path, data, 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			switch tc.claude {
			case "connected":
				data, err := claudeSettings(nil, endpoint, "test-secret", "dev@example.com")
				write(filepath.Join(claudeHome, "settings.json"), data, err)
			case "exporters off":
				data, err := claudeSettings(nil, endpoint, "test-secret", "dev@example.com")
				data = bytes.ReplaceAll(data, []byte(`"otlp"`), []byte(`"none"`))
				write(filepath.Join(claudeHome, "settings.json"), data, err)
			case "grpc":
				data, err := claudeSettings(nil, endpoint, "test-secret", "dev@example.com")
				data = bytes.ReplaceAll(data, []byte(`"http/protobuf"`), []byte(`"grpc"`))
				write(filepath.Join(claudeHome, "settings.json"), data, err)
			case "plain":
				write(filepath.Join(claudeHome, "settings.json"), []byte(`{"model":"x"}`), nil)
			}
			if tc.codex {
				data, err := codexSettings(nil, endpoint, "test-secret")
				if tc.codexLogsRemoved {
					data = bytes.Replace(data, []byte("[otel.exporter.otlp-http]"), []byte("[otel.removed.otlp-http]"), 1)
				}
				write(filepath.Join(codexHome, "config.toml"), data, err)
				switch tc.identity {
				case "":
					data, err = shellSettings(nil, "o'neil@example.com")
					write(filepath.Join(home, ".zshrc"), data, err)
				case "fish":
					t.Setenv("SHELL", "/usr/bin/fish")
					data, err = fishSettings(nil, "o'neil@example.com")
					write(fishConfig(home), data, err)
				}
			}
			var out, errs bytes.Buffer
			code := run(context.Background(), []string{"sessions", "status"}, &out, &errs)
			if code != tc.code {
				t.Fatalf("exit %d, want %d: %s%s", code, tc.code, out.String(), errs.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in:\n%s", want, out.String())
				}
			}
			for _, avoid := range append(tc.avoid, "test-secret", "2020-") {
				if strings.Contains(out.String(), avoid) {
					t.Errorf("unexpected %q in:\n%s", avoid, out.String())
				}
			}
		})
	}
}
