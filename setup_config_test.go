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

func TestClaudeSettingsMergesEnvAndPreservesMetadataOnly(t *testing.T) {
	got, err := claudeSettings([]byte(`{"theme":"dark","env":{"KEEP":"yes","OTEL_RESOURCE_ATTRIBUTES":"service.name=claude,enduser.id=old"}}`), testEndpoint, "abc", "dev@example.com")
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
		"CLAUDE_CODE_ENABLE_TELEMETRY":    "1",
		"OTEL_METRICS_EXPORTER":           "otlp",
		"OTEL_LOGS_EXPORTER":              "otlp",
		"OTEL_METRICS_INCLUDE_REPOSITORY": "true",
		"OTEL_LOG_TOOL_DETAILS":           "1",
		"OTEL_EXPORTER_OTLP_ENDPOINT":     testEndpoint,
		"OTEL_EXPORTER_OTLP_HEADERS":      "Authorization=Bearer abc",
		"OTEL_EXPORTER_OTLP_PROTOCOL":     "http/protobuf",
	} {
		if settings.Env[key] != want {
			t.Errorf("%s = %q, want %q", key, settings.Env[key], want)
		}
	}
	for _, forbidden := range []string{"OTEL_LOG_USER_PROMPTS", "OTEL_LOG_TOOL_CONTENT", "OTEL_LOG_RAW_API_BODIES", "OTEL_TRACES_EXPORTER"} {
		if _, ok := settings.Env[forbidden]; ok {
			t.Errorf("must not enable %s", forbidden)
		}
	}
}

func TestClaudeSettingsRejectsInvalidJSONAndEnv(t *testing.T) {
	for _, data := range [][]byte{[]byte(`[]`), []byte(`{"env":[]`), []byte(`{"env":"bad"}`)} {
		if _, err := claudeSettings(data, testEndpoint, "token", "dev@example.com"); err == nil {
			t.Errorf("claudeSettings(%s) succeeded", data)
		}
	}
}

func TestClaudeSettingsRejectsConflictingOrContentTelemetry(t *testing.T) {
	for _, env := range []string{
		`"OTEL_EXPORTER_OTLP_ENDPOINT":"https://other.example/otlp"`,
		`"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT":"https://other.example/v1/logs"`,
		`"OTEL_LOG_USER_PROMPTS":"true"`,
		`"OTEL_TRACES_EXPORTER":"otlp"`,
	} {
		if _, err := claudeSettings([]byte(`{"env":{`+env+`}}`), testEndpoint, "token", "dev@example.com"); err == nil {
			t.Errorf("claudeSettings accepted %s", env)
		}
	}
	if _, err := claudeSettings([]byte(`{"env":{"OTEL_EXPORTER_OTLP_ENDPOINT":"`+testEndpoint+`"}}`), testEndpoint, "token", "dev@example.com"); err != nil {
		t.Fatalf("same endpoint must be reusable: %v", err)
	}
	if _, err := claudeSettings([]byte(`{"otelHeadersHelper":true}`), testEndpoint, "token", "dev@example.com"); err == nil {
		t.Fatal("top-level otelHeadersHelper succeeded")
	}
}

func TestCodexSettingsUpdatesManagedBlockAndPreservesInput(t *testing.T) {
	input := []byte("model = \"o3\"\n\n# comment\n")
	first, err := codexSettings(input, testEndpoint, "a\"b", false)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(first, []byte("model = \"o3\"\n\n# comment\n")) || !bytes.Contains(first, []byte(`endpoint = "https://app.navigara.com/api/otlp/v1/metrics"`)) || !bytes.Contains(first, []byte(`Authorization = "Bearer a\"b"`)) {
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
	withRootSuffix := append(append([]byte(nil), first...), []byte("model = \"other\"\n")...)
	if _, err := codexSettings(withRootSuffix, testEndpoint, "new", false); err == nil {
		t.Fatal("managed block followed by root keys succeeded")
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
	prefix, suffix := []byte("model = \"fixture\"\n"), []byte("[mcp_servers.example]\ncommand = \"example\"\n")
	input := append(append(append([]byte(nil), prefix...), fixture...), suffix...)
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
	if _, err := codexSettings(append(got, fixture...), testEndpoint, "next", true); err == nil {
		t.Fatal("mixed managed and unmanaged telemetry was accepted")
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
