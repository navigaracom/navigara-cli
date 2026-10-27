package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
		metricsOverride  string
		codexProtocol    string
		codexOutside     bool
		codexEndpoint    string
		claudeEndpoint   string
		busyOnce         bool
		args             []string
		wantErr          string
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
			want: []string{"Codex: not connected: [otel.exporter.otlp-http] in config.toml is not what setup writes"}},
		{name: "claude exporters off", claude: "exporters off", code: 1,
			want: []string{"Claude Code: not connected: OTEL_LOGS_EXPORTER in settings.json is not what setup writes"}},
		{name: "claude grpc", claude: "grpc", code: 1,
			want: []string{"Claude Code: not connected: OTEL_EXPORTER_OTLP_PROTOCOL in settings.json is not what setup writes"}},
		{name: "codex json protocol", codex: true, codexProtocol: "json", code: 1,
			want: []string{"Codex: not connected: [otel.metrics_exporter.otlp-http] protocol in config.toml is not what setup writes"}},
		{name: "codex without identity", codex: true, identity: "none", code: 1,
			want: []string{"Codex: not connected: no Navigara email block in"}},
		{name: "codex fish identity", codex: true, identity: "fish", code: 0,
			want: []string{"Codex: connected to 127.0.0.1:", "as o'neil@example.com"}},
		{name: "claude shell override", claude: "connected", metricsOverride: "https://elsewhere.example/v1/metrics", code: 1,
			want: []string{"Claude Code: not connected: OTEL_EXPORTER_OTLP_METRICS_ENDPOINT is set in your shell environment"}},
		{name: "claude without email", claude: "no email", code: 1,
			want: []string{"Claude Code: not connected: OTEL_RESOURCE_ATTRIBUTES in settings.json has no valid enduser.id"}},
		{name: "codex bash identity", codex: true, identity: "bash", code: 0,
			want: []string{"Codex: connected to 127.0.0.1:", "as o'neil@example.com"}},
		{name: "codex bash without login profile block", codex: true, identity: "bash no login", code: 1,
			want: []string{"Codex: not connected: no Navigara email block in", ".bash_profile"}},
		{name: "codex telemetry outside block", codex: true, codexOutside: true, code: 1,
			want: []string{"Codex: not connected: config.toml has telemetry settings outside the Navigara block"}},
		{name: "claude endpoint without otlp", claude: "connected", claudeEndpoint: "/api", code: 1,
			want: []string{"Claude Code: not connected: endpoint http://127.0.0.1:", "/api does not end in /otlp"}},
		{name: "claude endpoint with trailing slash", claude: "connected", claudeEndpoint: "/api/otlp/", code: 1,
			want: []string{"/api/otlp/ does not end in /otlp"}},
		{name: "claude endpoint with otlp prefix", claude: "connected", claudeEndpoint: "/api/otlpx", code: 1,
			want: []string{"/api/otlpx does not end in /otlp"}},
		{name: "retry goes to stderr", claude: "connected", busyOnce: true, code: 0,
			want: []string{"Navigara accepts the Analytics Ingestor key"}, wantErr: "Upload failed (HTTP 503), retrying in 2 s"},
		{name: "invalid shell flag", args: []string{"--shell", "tcsh"}, code: 2, wantErr: "navigara: --shell must be bash, zsh, or fish"},
		{name: "empty shell flag", args: []string{"--shell="}, code: 2, wantErr: "navigara: --shell must be bash, zsh, or fish"},
		{name: "codex endpoint without otlp", codex: true, codexEndpoint: "/api", code: 1,
			want: []string{"Codex: not connected: endpoint http://127.0.0.1:", "/api does not end in /otlp"}},
		{name: "claude home unreadable", claude: "bad home", code: 1,
			want: []string{"Claude Code: cannot check its home:"}, avoid: []string{"Claude Code: not found"}},
		{name: "nothing found", code: 0,
			want: []string{"Claude Code: not found", "Codex: not found", "No Claude Code or Codex home found"}, avoid: []string{"connected to"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			claudeHome, codexHome := filepath.Join(home, ".claude"), filepath.Join(home, ".codex")
			setHome(t, home)
			setConfigHome(t, filepath.Join(home, "config"))
			t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
			t.Setenv("CODEX_HOME", codexHome)
			t.Setenv("SHELL", "/bin/zsh")
			if tc.metricsOverride != "" {
				t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", tc.metricsOverride)
			}
			t.Setenv("ZDOTDIR", home)
			busy := tc.busyOnce
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if busy {
					busy = false
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
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
				claudeEndpoint := endpoint
				if tc.claudeEndpoint != "" {
					claudeEndpoint = srv.URL + tc.claudeEndpoint
				}
				data, err := claudeSettings(nil, claudeEndpoint, "test-secret", "dev@example.com")
				write(filepath.Join(claudeHome, "settings.json"), data, err)
			case "exporters off":
				data, err := claudeSettings(nil, endpoint, "test-secret", "dev@example.com")
				data = bytes.ReplaceAll(data, []byte(`"otlp"`), []byte(`"none"`))
				write(filepath.Join(claudeHome, "settings.json"), data, err)
			case "grpc":
				data, err := claudeSettings(nil, endpoint, "test-secret", "dev@example.com")
				data = bytes.ReplaceAll(data, []byte(`"http/protobuf"`), []byte(`"grpc"`))
				write(filepath.Join(claudeHome, "settings.json"), data, err)
			case "no email":
				data, err := claudeSettings(nil, endpoint, "test-secret", "dev@example.com")
				data = bytes.ReplaceAll(data, []byte(`"enduser.id=dev@example.com"`), []byte(`""`))
				write(filepath.Join(claudeHome, "settings.json"), data, err)
			case "bad home":
				write(filepath.Join(home, "file"), nil, nil)
				t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "file", ".claude"))
			case "plain":
				write(filepath.Join(claudeHome, "settings.json"), []byte(`{"model":"x"}`), nil)
			}
			if tc.codex {
				codexEndpoint := endpoint
				if tc.codexEndpoint != "" {
					codexEndpoint = srv.URL + tc.codexEndpoint
				}
				data, err := codexSettings(nil, codexEndpoint, "test-secret")
				if tc.codexProtocol != "" {
					data = bytes.ReplaceAll(data, []byte(`protocol = "binary"`), []byte(`protocol = "`+tc.codexProtocol+`"`))
				}
				if tc.codexOutside {
					data = append([]byte("[otel]\nenvironment = \"dev\"\n"), data...)
				}
				if tc.codexLogsRemoved {
					data = bytes.Replace(data, []byte("[otel.exporter.otlp-http]"), []byte("[otel.removed.otlp-http]"), 1)
				}
				write(filepath.Join(codexHome, "config.toml"), data, err)
				switch tc.identity {
				case "":
					data, err = shellSettings(nil, "o'neil@example.com")
					write(filepath.Join(home, ".zshrc"), data, err)
				case "bash", "bash no login":
					t.Setenv("SHELL", "/bin/bash")
					data, err = shellSettings(nil, "o'neil@example.com")
					write(filepath.Join(home, ".bashrc"), data, err)
					if tc.identity == "bash" {
						write(filepath.Join(home, ".profile"), data, err)
					} else {
						write(filepath.Join(home, ".bash_profile"), []byte("# mine\n"), nil)
					}
				case "fish":
					t.Setenv("SHELL", "/usr/bin/fish")
					data, err = fishSettings(nil, "o'neil@example.com")
					write(fishConfig(home), data, err)
				}
			}
			var out, errs bytes.Buffer
			code := run(context.Background(), append([]string{"sessions", "status"}, tc.args...), &out, &errs)
			if code != tc.code {
				t.Fatalf("exit %d, want %d: %s%s", code, tc.code, out.String(), errs.String())
			}
			if !strings.Contains(errs.String(), tc.wantErr) {
				t.Errorf("missing %q in stderr:\n%s", tc.wantErr, errs.String())
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

func TestStatusRejectsEachChangedManagedSetting(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("ZDOTDIR", home)
	profile, err := shellSettings(nil, "dev@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), profile, 0600); err != nil {
		t.Fatal(err)
	}
	endpoint := "https://app.navigara.com/api/otlp"
	attrs := "enduser.id=dev@example.com"
	for key := range claudeTelemetryEnv(endpoint, "secret", attrs) {
		env := claudeTelemetryEnv(endpoint, "secret", attrs)
		env[key] += "x"
		data, _ := json.Marshal(map[string]any{"env": env})
		if err := os.WriteFile(filepath.Join(home, "settings.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		link, err := claudeLink(home)
		switch key {
		case "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_HEADERS", "OTEL_RESOURCE_ATTRIBUTES":
			// Status reports these values and the preflight checks them.
			if err != nil || link == (toolLink{endpoint: endpoint, token: "secret", email: "dev@example.com"}) {
				t.Errorf("changed %s: %+v %v", key, link, err)
			}
		default:
			if err == nil || !strings.HasPrefix(err.Error(), key+" ") && !strings.Contains(err.Error(), "not set up") {
				t.Errorf("changed %s: %v", key, err)
			}
		}
	}
	for _, key := range claudeFileTelemetryOverrides {
		env := claudeTelemetryEnv(endpoint, "secret", attrs)
		env[key] = "x"
		data, _ := json.Marshal(map[string]any{"env": env})
		if err := os.WriteFile(filepath.Join(home, "settings.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := claudeLink(home); err == nil || !strings.HasPrefix(err.Error(), key+" ") {
			t.Errorf("added %s: %v", key, err)
		}
	}
	block := codexBlock(endpoint, "secret")
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		if !strings.Contains(line, " = ") {
			continue
		}
		changed := slices.Clone(lines)
		changed[i] = strings.Replace(line, `"`, `"x`, 1)
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(strings.Join(changed, "\n")), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := codexLink(home, "zsh"); err == nil {
			t.Errorf("changed line %q: accepted", line)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(block), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := codexLink(home, "zsh"); err != nil {
		t.Fatalf("unchanged block: %v", err)
	}
}
