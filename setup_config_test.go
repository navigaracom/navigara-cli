package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const testEndpoint = "https://app.navigara.com/api/otlp"

func TestClaudeSettingsCanonicalizesExistingTelemetry(t *testing.T) {
	env := map[string]string{
		"KEEP":                                "yes",
		"OTEL_RESOURCE_ATTRIBUTES":            "service.name=claude,enduser.id=old",
		"OTEL_METRICS_INCLUDE_SESSION_ID":     "false",
		"OTEL_EXPORTER_OTLP_ENDPOINT":         "https://old.example/otlp",
		"OTEL_EXPORTER_OTLP_HEADERS":          "Authorization=Bearer old",
		"OTEL_EXPORTER_OTLP_PROTOCOL":         "grpc",
		"OTEL_TRACES_EXPORTER":                "otlp",
		"OTEL_LOG_USER_PROMPTS":               "0",
		"OTEL_LOG_ASSISTANT_RESPONSES":        "true",
		"OTEL_LOG_TOOL_CONTENT":               "true",
		"OTEL_LOG_RAW_API_BODIES":             "true",
		"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA": "1",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": "https://old.example/metrics",
		"OTEL_EXPORTER_OTLP_METRICS_HEADERS":  "Authorization=Bearer old",
		"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL": "grpc",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT":    "https://old.example/logs",
		"OTEL_EXPORTER_OTLP_LOGS_HEADERS":     "Authorization=Bearer old",
		"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL":    "grpc",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":  "https://old.example/traces",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS":   "Authorization=Bearer old",
		"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL":  "grpc",
		"OTEL_HEADERS_HELPER":                 "legacy",
		"CLAUDE_CODE_OTEL_HEADERS_HELPER":     "legacy",
		"BETA_TRACING_ENDPOINT":               "https://old.example/beta",
		"ENABLE_ENHANCED_TELEMETRY_BETA":      "1",
		"ENABLE_BETA_TRACING_DETAILED":        "1",
	}
	input, err := json.Marshal(map[string]any{"theme": "dark", "otelHeadersHelper": true, "env": env})
	if err != nil {
		t.Fatal(err)
	}
	got, err := claudeSettings(input, testEndpoint, "abc", "dev@example.com")
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Theme string            `json:"theme"`
		Env   map[string]string `json:"env"`
	}
	if err := json.Unmarshal(got, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Theme != "dark" || settings.Env["KEEP"] != "yes" || settings.Env["OTEL_RESOURCE_ATTRIBUTES"] != "service.name=claude,enduser.id=dev@example.com" {
		t.Fatalf("unexpected settings: %#v", settings)
	}
	for key, want := range map[string]string{
		"CLAUDE_CODE_ENABLE_TELEMETRY":        "1",
		"OTEL_METRICS_EXPORTER":               "otlp",
		"OTEL_LOGS_EXPORTER":                  "otlp",
		"OTEL_METRICS_INCLUDE_REPOSITORY":     "true",
		"OTEL_METRICS_INCLUDE_SESSION_ID":     "true",
		"OTEL_LOG_TOOL_DETAILS":               "1",
		"OTEL_EXPORTER_OTLP_ENDPOINT":         testEndpoint,
		"OTEL_EXPORTER_OTLP_HEADERS":          "Authorization=Bearer abc",
		"OTEL_EXPORTER_OTLP_PROTOCOL":         "http/protobuf",
		"OTEL_LOG_USER_PROMPTS":               "1",
		"OTEL_LOG_ASSISTANT_RESPONSES":        "0",
		"OTEL_LOG_TOOL_CONTENT":               "0",
		"OTEL_LOG_RAW_API_BODIES":             "0",
		"OTEL_TRACES_EXPORTER":                "none",
		"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA": "0",
	} {
		if settings.Env[key] != want {
			t.Errorf("%s = %q, want %q", key, settings.Env[key], want)
		}
	}
	for _, forbidden := range []string{
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_HEADERS", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_HEADERS", "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
		"OTEL_HEADERS_HELPER", "CLAUDE_CODE_OTEL_HEADERS_HELPER",
		"BETA_TRACING_ENDPOINT", "ENABLE_ENHANCED_TELEMETRY_BETA", "ENABLE_BETA_TRACING_DETAILED",
	} {
		if _, ok := settings.Env[forbidden]; ok {
			t.Errorf("must remove %s", forbidden)
		}
	}
	if bytes.Contains(got, []byte(`"otelHeadersHelper"`)) {
		t.Fatal("top-level helper remained")
	}
	again, err := claudeSettings(got, testEndpoint, "abc", "dev@example.com")
	if err != nil || !bytes.Equal(again, got) {
		t.Fatalf("canonical transform was not idempotent: %v\n%s", err, again)
	}
}

func TestClaudeSettingsRejectsInvalidJSONAndEnv(t *testing.T) {
	for _, data := range [][]byte{[]byte(`[]`), []byte(`{"env":[]`), []byte(`{"env":"bad"}`)} {
		if _, err := claudeSettings(data, testEndpoint, "token", "dev@example.com"); err == nil {
			t.Errorf("claudeSettings(%s) succeeded", data)
		}
	}
}

func TestValidateClaudeEnvRejectsConflictingProcessTelemetry(t *testing.T) {
	for _, env := range []map[string]string{
		{"OTEL_EXPORTER_OTLP_ENDPOINT": "https://other.example/otlp"},
		{"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "https://other.example/v1/logs"},
		{"OTEL_LOG_ASSISTANT_RESPONSES": "true"},
		{"OTEL_TRACES_EXPORTER": "otlp"},
		{"BETA_TRACING_ENDPOINT": "https://other.example/traces"},
		{"OTEL_LOG_USER_PROMPTS": "yes"},
	} {
		if err := validateClaudeEnv(env, testEndpoint, "token"); err == nil {
			t.Errorf("validateClaudeEnv accepted %#v", env)
		}
	}
	for _, env := range []map[string]string{
		{"OTEL_LOG_USER_PROMPTS": "1"},
		{"OTEL_LOG_USER_PROMPTS": "true"},
		{"OTEL_TRACES_EXPORTER": "none"},
	} {
		if err := validateClaudeEnv(env, testEndpoint, "token"); err != nil {
			t.Errorf("supported process telemetry %#v rejected: %v", env, err)
		}
	}
	if err := validateClaudeEnv(map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT":         testEndpoint,
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": testEndpoint + "/v1/metrics",
	}, testEndpoint, "token"); err != nil {
		t.Fatalf("same endpoint must be reusable: %v", err)
	}
}

func TestCodexSettingsUpdatesManagedBlockAndPreservesInput(t *testing.T) {
	input := []byte("model = \"o3\"\n\n# comment\n")
	first, err := codexSettings(input, testEndpoint, "a\"b", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(first, []byte("model = \"o3\"\n\n# comment\n")) || !bytes.Contains(first, []byte(`endpoint = "https://app.navigara.com/api/otlp/v1/metrics"`)) || !bytes.Contains(first, []byte(`endpoint = "https://app.navigara.com/api/otlp/v1/traces"`)) || !bytes.Contains(first, []byte(`Authorization = "Bearer a\"b"`)) {
		t.Fatalf("missing expected content:\n%s", first)
	}
	second, err := codexSettings(first, testEndpoint, "new", false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(second, []byte("# >>> navigara telemetry >>>")) != 1 || bytes.Contains(second, []byte(`Bearer a\"b`)) || !bytes.Contains(second, []byte(`Bearer new`)) {
		t.Fatalf("managed block was not replaced:\n%s", second)
	}
	suffix := []byte("\n# keep this separation\n[projects.keep]\ntrust_level = \"trusted\"\n")
	withSuffix := append(append([]byte(nil), second...), suffix...)
	updated, err := codexSettings(withSuffix, testEndpoint, "next", false)
	if err != nil || !bytes.HasSuffix(updated, suffix) {
		t.Fatalf("managed update changed its suffix: %q, %v", updated, err)
	}
	crlfPrefix := []byte("model = \"o3\"\r\n\r\n# comment\r\n")
	crlfSuffix := []byte("\r\n# keep this CRLF separation\r\n[projects.keep]\r\ntrust_level = \"trusted\"\r\n")
	crlfManaged := bytes.ReplaceAll(first, []byte("\n"), []byte("\r\n"))
	crlfInput := append(append([]byte(nil), crlfManaged...), crlfSuffix...)
	crlfUpdated, err := codexSettings(crlfInput, testEndpoint, "crlf-token", false)
	if err != nil || !bytes.HasPrefix(crlfUpdated, crlfPrefix) || !bytes.HasSuffix(crlfUpdated, crlfSuffix) || bytes.Count(crlfUpdated, []byte(telemetryBegin)) != 1 || !bytes.Contains(crlfUpdated, []byte("Bearer crlf-token")) {
		t.Fatalf("CRLF managed update changed surrounding content: %q, %v", crlfUpdated, err)
	}
	withRootSuffix := append(append([]byte(nil), first...), []byte("model = \"other\"\n")...)
	if _, err := codexSettings(withRootSuffix, testEndpoint, "new", false); err == nil {
		t.Fatal("managed block followed by root keys succeeded")
	}
}

func TestCodexSettingsRemovesObsoleteRootFeatureFlags(t *testing.T) {
	input := []byte(`# preserved root setting
model = "gpt-5"

[features]
rmcp_client = false # obsolete
terminal_resize_reflow = true
js_repl = false

# current feature flags
goals = true # keep
prevent_idle_sleep = true # keep

[profiles.work.features]
rmcp_client = true
`)

	first, err := codexSettings(input, testEndpoint, "token", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, obsolete := range []string{"rmcp_client = false", "terminal_resize_reflow = true", "js_repl = false"} {
		if bytes.Contains(first, []byte(obsolete)) {
			t.Errorf("obsolete root feature flag remained: %q", obsolete)
		}
	}
	for _, preserved := range []string{
		"# preserved root setting\nmodel = \"gpt-5\"",
		"[features]\n",
		"# current feature flags\n",
		"goals = true # keep",
		"prevent_idle_sleep = true # keep",
		"[profiles.work.features]\nrmcp_client = true",
		`endpoint = "https://app.navigara.com/api/otlp/v1/metrics"`,
		`endpoint = "https://app.navigara.com/api/otlp/v1/logs"`,
		`headers = { Authorization = "Bearer token" }`,
	} {
		if !bytes.Contains(first, []byte(preserved)) {
			t.Errorf("expected preserved or configured content %q:\n%s", preserved, first)
		}
	}

	second, err := codexSettings(first, testEndpoint, "token", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second, first) {
		t.Fatalf("second patch was not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestCodexSettingsRemovesQuotedAndDottedObsoleteRootFeatureFlags(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`["features"]
"rmcp_client" = false
"terminal_resize_reflow" = true
"js_repl" = false
goals = true
prevent_idle_sleep = true
future_flag = true
`),
		[]byte(`features.rmcp_client = false
features.terminal_resize_reflow = true
features.js_repl = false
features.goals = true
features.prevent_idle_sleep = true
features.future_flag = true
`),
	} {
		got, err := codexSettings(input, testEndpoint, "token", false)
		if err != nil {
			t.Fatal(err)
		}
		for _, obsolete := range []string{"rmcp_client", "terminal_resize_reflow", "js_repl"} {
			if bytes.Contains(got, []byte(obsolete)) {
				t.Errorf("obsolete root feature flag remained in %q:\n%s", input, got)
			}
		}
		for _, current := range []string{"goals = true", "prevent_idle_sleep = true", "future_flag = true"} {
			if !bytes.Contains(got, []byte(current)) {
				t.Errorf("current root feature flag was removed: %q", current)
			}
		}
	}
}

func TestCodexSettingsRejectsUnsupportedObsoleteFeatureShapes(t *testing.T) {
	for _, input := range [][]byte{
		[]byte("features = { rmcp_client = false, goals = true }\n"),
		[]byte("[features]\nrmcp_client = \"false\"\n"),
	} {
		got, err := codexSettings(input, testEndpoint, "token", false)
		if !errors.Is(err, errUnsupportedCodexFeatureLayout) || got != nil {
			t.Fatalf("unsupported feature layout returned %q, %v", got, err)
		}
	}
}

func TestCodexSettingsRejectsOtherOTELAndMalformedMarkers(t *testing.T) {
	for _, input := range [][]byte{
		[]byte("[otel.metrics_exporter.otlp-http]\nendpoint = \"elsewhere\"\n"),
		[]byte("[\"otel\".metrics_exporter.otlp-http]\n"),
		[]byte("[\"o\\u0074el\".metrics_exporter.otlp-http]\n"),
		[]byte("'otel' = \"foreign\"\n"),
		[]byte("note = \"\"\"unterminated\n"),
		[]byte("model = \"x\" # '''\n[otel.metrics_exporter.otlp-http]\n"),
		[]byte("model = \"x\"\nmodel = \"duplicate\"\n"),
		[]byte("# >>> navigara telemetry >>>\n"),
		[]byte("# <<< navigara telemetry <<<\n"),
	} {
		if _, err := codexSettings(input, testEndpoint, "token", false); err == nil {
			t.Errorf("codexSettings(%q) succeeded", input)
		}
	}
}

func TestCodexSettingsPreservesMarkerTextInValues(t *testing.T) {
	input := []byte("note = \"# >>> navigara telemetry >>>\"\n")
	got, err := codexSettings(input, testEndpoint, "token", false)
	if err != nil || !bytes.HasPrefix(got, input) {
		t.Fatalf("marker text in a value was not preserved: %q, %v", got, err)
	}
}

func TestCodexSettingsRequiresConsentToReplaceExistingExporters(t *testing.T) {
	fixture, err := os.ReadFile("testdata/codex-existing-telemetry.toml")
	if err != nil {
		t.Fatal(err)
	}
	trace := []byte(`[otel.trace_exporter.otlp-http]
endpoint = "https://old.example/v1/traces"
protocol = "binary"
headers = { Authorization = "Bearer fixture-old" }
`)
	prefix, suffix := []byte("model = \"fixture\"\n"), []byte("[mcp_servers.example]\ncommand = \"example\"\n")
	for _, test := range []struct {
		name      string
		telemetry []byte
	}{
		{name: "metrics and logs", telemetry: fixture},
		{name: "metrics logs and traces", telemetry: append(append([]byte(nil), fixture...), trace...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := append(append(append([]byte(nil), prefix...), test.telemetry...), suffix...)
			if _, err := codexSettings(input, testEndpoint, "new", false); !errors.Is(err, errCodexTelemetry) {
				t.Fatalf("expected a replaceable conflict, got %v", err)
			}
			got, err := codexSettings(input, testEndpoint, "new", true)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(got, prefix) || !bytes.HasSuffix(got, suffix) || bytes.Contains(got, []byte("fixture-old")) || bytes.Count(got, []byte(telemetryBegin)) != 1 {
				t.Fatal("replacement changed unrelated settings or retained the old exporters")
			}
			if _, err := codexSettings(append(got, test.telemetry...), testEndpoint, "next", true); err == nil {
				t.Fatal("mixed managed and unmanaged telemetry was accepted")
			}
		})
	}
	noncontiguous := append(append(append(append([]byte(nil), prefix...), fixture...), suffix...), trace...)
	if got, err := codexSettings(noncontiguous, testEndpoint, "new", true); err == nil || got != nil {
		t.Fatalf("noncontiguous telemetry was accepted: %q, %v", got, err)
	}
	foreignRoot := append([]byte("otel.environment = \"dev\"\n"), fixture...)
	if got, err := codexSettings(foreignRoot, testEndpoint, "new", true); err == nil || got != nil {
		t.Fatalf("legacy exporters with root OTEL settings were accepted: %q, %v", got, err)
	}
}

func TestShellSettingsIsIdempotentAndSafelyQuotesEmail(t *testing.T) {
	first, err := shellSettings([]byte("export PATH=/bin\n"), "dev@example.com")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("sh", "-c", string(first)+"\n"+string(first)+"\nprintf %s \"$OTEL_RESOURCE_ATTRIBUTES\"")
	command.Env = append(os.Environ(), "OTEL_RESOURCE_ATTRIBUTES=service.name=codex,enduser.id=old@example.com,region=eu,enduser.id=other@example.com")
	if got, err := command.Output(); err != nil || string(got) != "service.name=codex,region=eu,enduser.id=dev@example.com" {
		t.Fatalf("sourcing block twice got %q, %v", got, err)
	}
	second, err := shellSettings(first, "new@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(second, []byte("# >>> navigara telemetry >>>")) != 1 || !bytes.Contains(second, []byte("export PATH=/bin\n")) || strings.Contains(string(second), "dev@example.com") || !bytes.Contains(second, []byte("enduser.id=")) || !bytes.Contains(second, []byte("new@example.com")) {
		t.Fatalf("unexpected shell settings:\n%s", second)
	}
}

func TestShellSettingsRejectsAttributeInjectionAndBadMarkers(t *testing.T) {
	if _, err := shellSettings(nil, "a,b@example.com"); err == nil {
		t.Fatal("comma injection succeeded")
	}
	if _, err := shellSettings(nil, "name <dev@example.com>"); err == nil {
		t.Fatal("display-name email succeeded")
	}
	if _, err := shellSettings([]byte("# >>> navigara telemetry >>> x\n"), "dev@example.com"); err == nil {
		t.Fatal("malformed marker succeeded")
	}
}
