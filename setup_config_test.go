package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
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
	first, err := codexSettings(input, testEndpoint, "a\"b")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(first, []byte("model = \"o3\"\n\n# comment\n")) || !bytes.Contains(first, []byte(`endpoint = "https://app.navigara.com/api/otlp/v1/metrics"`)) || !bytes.Contains(first, []byte(`endpoint = "https://app.navigara.com/api/otlp/v1/traces"`)) || !bytes.Contains(first, []byte(`Authorization = "Bearer a\"b"`)) {
		t.Fatalf("missing expected content:\n%s", first)
	}
	second, err := codexSettings(first, testEndpoint, "new")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(second, []byte("# >>> navigara telemetry >>>")) != 1 || bytes.Contains(second, []byte(`Bearer a\"b`)) || !bytes.Contains(second, []byte(`Bearer new`)) {
		t.Fatalf("managed block was not replaced:\n%s", second)
	}
	suffix := []byte("\n# keep this separation\n[projects.keep]\ntrust_level = \"trusted\"\n")
	withSuffix := append(append([]byte(nil), second...), suffix...)
	updated, err := codexSettings(withSuffix, testEndpoint, "next")
	if err != nil || !bytes.HasSuffix(updated, suffix) {
		t.Fatalf("managed update changed its suffix: %q, %v", updated, err)
	}
	crlfPrefix := []byte("model = \"o3\"\r\n\r\n# comment\r\n")
	crlfSuffix := []byte("\r\n# keep this CRLF separation\r\n[projects.keep]\r\ntrust_level = \"trusted\"\r\n")
	crlfManaged := bytes.ReplaceAll(first, []byte("\n"), []byte("\r\n"))
	crlfInput := append(append([]byte(nil), crlfManaged...), crlfSuffix...)
	crlfUpdated, err := codexSettings(crlfInput, testEndpoint, "crlf-token")
	if err != nil || !bytes.HasPrefix(crlfUpdated, crlfPrefix) || !bytes.HasSuffix(crlfUpdated, crlfSuffix) || bytes.Count(crlfUpdated, []byte(telemetryBegin)) != 1 || !bytes.Contains(crlfUpdated, []byte("Bearer crlf-token")) {
		t.Fatalf("CRLF managed update changed surrounding content: %q, %v", crlfUpdated, err)
	}
	withRootSuffix := append(append([]byte(nil), first...), []byte("model = \"other\"\n")...)
	if _, err := codexSettings(withRootSuffix, testEndpoint, "new"); err == nil {
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

	first, err := codexSettings(input, testEndpoint, "token")
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

	second, err := codexSettings(first, testEndpoint, "token")
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
		got, err := codexSettings(input, testEndpoint, "token")
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
		got, err := codexSettings(input, testEndpoint, "token")
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
		if _, err := codexSettings(input, testEndpoint, "token"); err == nil {
			t.Errorf("codexSettings(%q) succeeded", input)
		}
	}
}

func TestCodexSettingsPreservesMarkerTextInValues(t *testing.T) {
	input := []byte("note = \"# >>> navigara telemetry >>>\"\n")
	got, err := codexSettings(input, testEndpoint, "token")
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
			conflict := codexTelemetryConflict(input)
			if conflict == nil || !strings.Contains(conflict.Error(), "Codex already sends telemetry to https://old.example/v1/") || !strings.Contains(conflict.Error(), "--replace") {
				t.Fatalf("expected a replaceable conflict, got %v", conflict)
			}
			got, err := codexSettings(input, testEndpoint, "new")
			if err != nil {
				t.Fatal(err)
			}
			changes := strings.Join(settingsChanges(input, got, toml.Unmarshal), "\n")
			if !strings.Contains(changes, "otel.metrics_exporter.otlp-http.endpoint: https://old.example/v1/metrics -> "+testEndpoint+"/v1/metrics") ||
				!strings.Contains(changes, "otel.metrics_exporter.otlp-http.headers.Authorization: *** -> ***") || strings.Contains(changes, "fixture-old") || strings.Contains(changes, "model") {
				t.Fatalf("changes = %s", changes)
			}
			if !bytes.HasPrefix(got, prefix) || !bytes.HasSuffix(got, suffix) || bytes.Contains(got, []byte("fixture-old")) || bytes.Count(got, []byte(telemetryBegin)) != 1 {
				t.Fatal("replacement changed unrelated settings or retained the old exporters")
			}
			if _, err := codexSettings(append(got, test.telemetry...), testEndpoint, "next"); err == nil {
				t.Fatal("mixed managed and unmanaged telemetry was accepted")
			}
		})
	}
	noncontiguous := append(append(append(append([]byte(nil), prefix...), fixture...), suffix...), trace...)
	if got, err := codexSettings(noncontiguous, testEndpoint, "new"); err == nil || got != nil {
		t.Fatalf("noncontiguous telemetry was accepted: %q, %v", got, err)
	}
	foreignRoot := append([]byte("otel.environment = \"dev\"\n"), fixture...)
	if got, err := codexSettings(foreignRoot, testEndpoint, "new"); err == nil || got != nil {
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

func TestClaudeSettingsChangesListEveryKeyAndMaskSecrets(t *testing.T) {
	before := []byte(`{"otelHeadersHelper":"echo Authorization=Bearer helper-secret","env":{"OTEL_EXPORTER_OTLP_HEADERS":"a=one-secret,b=two-secret","OTEL_RESOURCE_ATTRIBUTES":"enduser.id=old@example.com"}}`)
	after, err := claudeSettings(before, testEndpoint, "new-secret", "dev@example.com")
	if err != nil {
		t.Fatal(err)
	}
	changes := strings.Join(settingsChanges(before, after, json.Unmarshal), "\n")
	for _, want := range []string{
		"otelHeadersHelper: *** -> (removed)",
		"env.OTEL_EXPORTER_OTLP_HEADERS: a=***,b=*** -> Authorization=***",
		"env.OTEL_RESOURCE_ATTRIBUTES: enduser.id=old@example.com -> enduser.id=dev@example.com",
		"env.OTEL_EXPORTER_OTLP_ENDPOINT: (none) -> " + testEndpoint,
		"env.CLAUDE_CODE_ENABLE_TELEMETRY: (none) -> 1",
	} {
		if !strings.Contains(changes, want) {
			t.Errorf("changes lack %q:\n%s", want, changes)
		}
	}
	if strings.Contains(changes, "secret") {
		t.Fatalf("changes leak a secret:\n%s", changes)
	}
	if claudeTelemetryConflict(before, testEndpoint, "new-secret") == nil {
		t.Fatal("foreign headers are not a conflict")
	}
	if got := settingsChanges(after, after, json.Unmarshal); len(got) != 0 {
		t.Fatalf("same file lists changes: %q", got)
	}
}

func TestRemoveClaudeSettingsKeepsOtherCollectors(t *testing.T) {
	foreign := []byte(`{"env":{"OTEL_EXPORTER_OTLP_ENDPOINT":"https://collector.example/api/otlp","OTEL_EXPORTER_OTLP_HEADERS":"x-api-key=corp","CLAUDE_CODE_ENABLE_TELEMETRY":"1"}}`)
	if got, err := removeClaudeSettings(foreign, nil); err != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("removed a collector that Navigara did not write: %s, %v", got, err)
	}
	ours, err := claudeSettings([]byte(`{}`), "https://app.example/api/otlp", "token", "dev@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := removeClaudeSettings(ours, nil); err != nil || string(got) != "{}" {
		t.Fatalf("remove = %s, %v", got, err)
	}
}

// The 831b08e writer wrote nine keys, none of the later prompt, trace,
// session and beta keys, and no marker.
func TestRemoveClaudeSettingsHandlesOlderLayout(t *testing.T) {
	older := []byte(`{"theme":"dark","env":{"KEEP":"yes",
		"CLAUDE_CODE_ENABLE_TELEMETRY":"1","OTEL_METRICS_EXPORTER":"otlp","OTEL_LOGS_EXPORTER":"otlp",
		"OTEL_METRICS_INCLUDE_REPOSITORY":"true","OTEL_LOG_TOOL_DETAILS":"1","OTEL_EXPORTER_OTLP_PROTOCOL":"http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT":"https://app.navigara.com/api/otlp","OTEL_EXPORTER_OTLP_HEADERS":"Authorization=Bearer old",
		"OTEL_RESOURCE_ATTRIBUTES":"team=a,enduser.id=dev@example.com"}}`)
	if got, err := removeClaudeSettings(older, []string{"https://other.example"}); err != nil || !bytes.Equal(got, older) {
		t.Fatalf("removed an older layout at an unknown origin: %s, %v", got, err)
	}
	got, err := removeClaudeSettings(older, []string{defaultHost})
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(got, &settings); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"theme": "dark", "env": map[string]any{"KEEP": "yes", "OTEL_RESOURCE_ATTRIBUTES": "team=a"}}
	if !reflect.DeepEqual(settings, want) {
		t.Fatalf("remove = %s", got)
	}
}

func TestClaudeSettingsSecondHostKeepsNoNavigaraIdentity(t *testing.T) {
	first, err := claudeSettings([]byte(`{"env":{"KEEP":"yes"}}`), "https://a.example/api/otlp", "a", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	second, err := claudeSettings(first, "https://b.example/api/otlp", "b", "b@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(second, []byte(previousEnduserID)) {
		t.Fatalf("recorded a Navigara identity: %s", second)
	}
	got, err := removeClaudeSettings(second, nil)
	if err != nil || string(got) != "{\n  \"env\": {\n    \"KEEP\": \"yes\"\n  }\n}" {
		t.Fatalf("remove = %s, %v", got, err)
	}

	user, err := claudeSettings([]byte(`{"env":{"OTEL_RESOURCE_ATTRIBUTES":"enduser.id=me@corp.example"}}`), "https://a.example/api/otlp", "a", "a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	moved, err := claudeSettings(user, "https://b.example/api/otlp", "b", "b@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := removeClaudeSettings(moved, nil); err != nil || !bytes.Contains(got, []byte(`"OTEL_RESOURCE_ATTRIBUTES": "enduser.id=me@corp.example"`)) || bytes.Contains(got, []byte(previousEnduserID)) {
		t.Fatalf("remove after a host change = %s, %v", got, err)
	}
}

func TestRemoveClaudeSettingsKeepsKeysChangedByUser(t *testing.T) {
	ours, err := claudeSettings([]byte(`{"env":{"KEEP":"yes"}}`), "https://app.example/api/otlp", "token", "dev@example.com")
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(ours, []byte(`"OTEL_LOGS_EXPORTER": "otlp"`), []byte(`"OTEL_LOGS_EXPORTER": "none"`), 1)
	got, err := removeClaudeSettings(changed, nil)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(got, &settings); err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"env": map[string]any{"KEEP": "yes", "OTEL_LOGS_EXPORTER": "none"}}; !reflect.DeepEqual(settings, want) {
		t.Fatalf("remove = %s", got)
	}
	if notes := claudeKeptKeys(changed, got); !reflect.DeepEqual(notes, []string{"Kept (changed by you): OTEL_LOGS_EXPORTER"}) {
		t.Fatalf("notes = %q", notes)
	}
}

func TestClaudeTelemetryConflictComparesNavigaraSetup(t *testing.T) {
	ours, err := claudeSettings([]byte(`{}`), testEndpoint, "old-token", "dev@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := claudeTelemetryConflict(ours, testEndpoint, "new-token"); err != nil {
		t.Fatalf("token rotation is a conflict: %v", err)
	}
	for _, edit := range [][2]string{
		{`"OTEL_LOGS_EXPORTER": "otlp"`, `"OTEL_LOGS_EXPORTER": "none"`},
		{`"Authorization=Bearer old-token"`, `"Authorization=Bearer old-token,X-Corp=custom"`},
	} {
		edited := bytes.Replace(ours, []byte(edit[0]), []byte(edit[1]), 1)
		if err := claudeTelemetryConflict(edited, testEndpoint, "new-token"); err == nil || !strings.Contains(err.Error(), "--replace") {
			t.Errorf("%s: user change is not a conflict", edit[1])
		}
	}
}

func TestRemoveClaudeSettingsKeepsCustomHeader(t *testing.T) {
	ours, err := claudeSettings([]byte(`{}`), "https://app.example/api/otlp", "token-secret", "dev@example.com")
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(ours, []byte(`"Authorization=Bearer token-secret"`), []byte(`"Authorization=Bearer token-secret,X-Corp=custom"`), 1)
	got, err := removeClaudeSettings(edited, nil)
	if err != nil {
		t.Fatal(err)
	}
	notes := claudeKeptKeys(edited, got)
	if !bytes.Contains(got, []byte("X-Corp=custom")) || !reflect.DeepEqual(notes, []string{"Kept (changed by you): OTEL_EXPORTER_OTLP_HEADERS"}) {
		t.Fatalf("remove = %s, notes = %q", got, notes)
	}
}

func TestRemoveClaudeSettingsNeedsProofOfNavigara(t *testing.T) {
	foreign := []byte(`{"env":{"OTEL_EXPORTER_OTLP_ENDPOINT":"https://collector.example/api/otlp","OTEL_EXPORTER_OTLP_HEADERS":"Authorization=Bearer corp","OTEL_LOGS_EXPORTER":"otlp"}}`)
	known := []string{defaultHost}
	if got, err := removeClaudeSettings(foreign, known); err != nil || !bytes.Equal(got, foreign) {
		t.Fatalf("removed a foreign Bearer collector: %s, %v", got, err)
	}
	if origin := claudeUnknownOrigin(foreign, known); origin != "https://collector.example" {
		t.Fatalf("unknown origin = %q", origin)
	}
	if got, err := removeClaudeSettings(foreign, []string{"https://collector.example"}); err != nil || bytes.Contains(got, []byte("collector.example")) {
		t.Fatalf("--host origin did not prove Navigara: %s, %v", got, err)
	}
	marked, err := claudeSettings(nil, "https://self-hosted.example/api/otlp", "token", "dev@example.com")
	if err != nil || !bytes.Contains(marked, []byte(`"NAVIGARA_MANAGED_ENDPOINT": "https://self-hosted.example/api/otlp"`)) || !bytes.Contains(marked, []byte(`"NAVIGARA_MANAGED_EMAIL": "dev@example.com"`)) {
		t.Fatalf("patch wrote no marker: %s, %v", marked, err)
	}
	if got, err := removeClaudeSettings(marked, known); err != nil || string(got) != "{}" {
		t.Fatalf("marked settings were not removed: %s, %v", got, err)
	}
}

func TestRemoveClaudeSettingsKeepsEditedEndpoint(t *testing.T) {
	ours, err := claudeSettings([]byte(`{"env":{"KEEP":"yes"}}`), "https://app.example/api/otlp", "token", "dev@example.com")
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(ours, []byte(`"OTEL_EXPORTER_OTLP_ENDPOINT": "https://app.example/api/otlp"`), []byte(`"OTEL_EXPORTER_OTLP_ENDPOINT": "https://collector.example/api/otlp"`), 1)
	got, err := removeClaudeSettings(edited, []string{"https://app.example"})
	if err != nil {
		t.Fatal(err)
	}
	var before, after struct{ Env map[string]string }
	if json.Unmarshal(edited, &before) != nil || json.Unmarshal(got, &after) != nil {
		t.Fatal("invalid JSON")
	}
	delete(before.Env, managedEndpoint)
	delete(before.Env, managedEmail)
	if !reflect.DeepEqual(before.Env, after.Env) {
		t.Fatalf("remove deleted more than the markers: %s", got)
	}
	notes := claudeKeptKeys(edited, got)
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, "\n"), "Kept (changed by you): OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Fatalf("notes = %q", notes)
	}
}

func TestRemoveClaudeSettingsKeepsEditedEnduserID(t *testing.T) {
	ours, err := claudeSettings([]byte(`{"env":{"OTEL_RESOURCE_ATTRIBUTES":"team=a,enduser.id=me@corp.example"}}`), "https://app.example/api/otlp", "token", "dev@example.com")
	if err != nil {
		t.Fatal(err)
	}
	edited := bytes.Replace(ours, []byte("enduser.id=dev@example.com"), []byte("enduser.id=other@example.com"), 1)
	if err := claudeTelemetryConflict(edited, "https://app.example/api/otlp", "token"); err == nil {
		t.Fatal("patch would overwrite an edited enduser.id")
	}
	got, err := removeClaudeSettings(edited, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{\n  \"env\": {\n    \"OTEL_RESOURCE_ATTRIBUTES\": \"team=a,enduser.id=other@example.com\"\n  }\n}" {
		t.Fatalf("remove = %s", got)
	}
	if notes := claudeKeptKeys(edited, got); !reflect.DeepEqual(notes, []string{"Kept (changed by you): enduser.id"}) {
		t.Fatalf("notes = %q", notes)
	}
}
