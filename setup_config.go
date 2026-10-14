package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
)

const (
	telemetryBegin = "# >>> navigara telemetry >>>"
	telemetryEnd   = "# <<< navigara telemetry <<<"
)

var errCodexTelemetry = errors.New("Codex already has telemetry configured")

func claudeSettings(data []byte, endpoint, token, email string) ([]byte, error) {
	if err := validValue("endpoint", endpoint); err != nil {
		return nil, err
	}
	if err := validValue("token", token); err != nil {
		return nil, err
	}
	if err := validEmail(email); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil || settings == nil {
		return nil, errors.New("Claude settings must be a JSON object")
	}
	env := map[string]string{}
	if raw, ok := settings["env"]; ok {
		if err := json.Unmarshal(raw, &env); err != nil || env == nil {
			return nil, errors.New("Claude settings env must be a JSON object of strings")
		}
	}
	delete(settings, "otelHeadersHelper")
	for _, key := range claudeFileTelemetryOverrides {
		delete(env, key)
	}
	attrs, err := resourceAttributes(env["OTEL_RESOURCE_ATTRIBUTES"], email)
	if err != nil {
		return nil, err
	}
	for key, value := range map[string]string{
		"CLAUDE_CODE_ENABLE_TELEMETRY":        "1",
		"OTEL_METRICS_EXPORTER":               "otlp",
		"OTEL_LOGS_EXPORTER":                  "otlp",
		"OTEL_METRICS_INCLUDE_REPOSITORY":     "true",
		"OTEL_METRICS_INCLUDE_SESSION_ID":     "true",
		"OTEL_LOG_TOOL_DETAILS":               "1",
		"OTEL_EXPORTER_OTLP_PROTOCOL":         "http/protobuf",
		"OTEL_EXPORTER_OTLP_ENDPOINT":         endpoint,
		"OTEL_EXPORTER_OTLP_HEADERS":          "Authorization=Bearer " + token,
		"OTEL_RESOURCE_ATTRIBUTES":            attrs,
		"OTEL_LOG_USER_PROMPTS":               "0",
		"OTEL_LOG_ASSISTANT_RESPONSES":        "0",
		"OTEL_LOG_TOOL_CONTENT":               "0",
		"OTEL_LOG_RAW_API_BODIES":             "0",
		"OTEL_TRACES_EXPORTER":                "none",
		"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA": "0",
	} {
		env[key] = value
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	settings["env"] = raw
	return json.MarshalIndent(settings, "", "  ")
}

func codexSettings(data []byte, endpoint, token string, replace bool) ([]byte, error) {
	if err := validValue("endpoint", endpoint); err != nil {
		return nil, err
	}
	if err := validValue("token", token); err != nil {
		return nil, err
	}
	start, end, err := tomlManagedRange(data)
	if err != nil {
		return nil, err
	}
	outside := append(append([]byte(nil), data[:start]...), data[end:]...)
	if hasOTELTOML(outside) {
		if start != end {
			return nil, errors.New("Codex has telemetry outside its Navigara block; this layout cannot be replaced automatically")
		}
		var supported bool
		start, end, supported = codexTelemetryRange(data)
		if !supported {
			return nil, errors.New("Codex settings use an unsupported or conflicting TOML layout; skip Codex or update its telemetry settings manually")
		}
		if !replace {
			return nil, errCodexTelemetry
		}
	}
	block := []byte(codexBlock(endpoint, token))
	if start != end {
		if !managedSuffixSafe(data[end:]) {
			return nil, errors.New("Navigara managed TOML block is followed by root keys; move those keys before the block or into a table")
		}
	}
	out := spliceManaged(data, block, start, end)
	out, err = removeObsoleteCodexFeatures(out)
	if err != nil {
		return nil, err
	}
	if err := validateTOML(out); err != nil {
		return nil, err
	}
	return out, nil
}

func shellSettings(data []byte, email string) ([]byte, error) {
	if err := validEmail(email); err != nil {
		return nil, err
	}
	start, end, err := managedRange(data)
	if err != nil {
		return nil, err
	}
	block := []byte(telemetryBegin + `
navigara_attrs= navigara_remaining=${OTEL_RESOURCE_ATTRIBUTES-}
while [ -n "$navigara_remaining" ]; do
  case $navigara_remaining in *,*) navigara_attr=${navigara_remaining%%,*}; navigara_remaining=${navigara_remaining#*,} ;; *) navigara_attr=$navigara_remaining; navigara_remaining= ;; esac
  case $navigara_attr in enduser.id=*|'') ;; *) navigara_attrs=${navigara_attrs:+$navigara_attrs,}$navigara_attr ;; esac
done
export OTEL_RESOURCE_ATTRIBUTES="${navigara_attrs:+$navigara_attrs,}enduser.id="` + shellQuote(email) + `
unset navigara_attrs navigara_remaining navigara_attr
` + telemetryEnd + "\n")
	return spliceManaged(data, block, start, end), nil
}

func codexBlock(endpoint, token string) string {
	q := func(value string) string { b, _ := json.Marshal(value); return string(b) }
	return telemetryBegin + `
[otel.metrics_exporter.otlp-http]
endpoint = ` + q(strings.TrimSuffix(endpoint, "/")+"/v1/metrics") + `
protocol = "binary"
headers = { Authorization = ` + q("Bearer "+token) + ` }

[otel.exporter.otlp-http]
endpoint = ` + q(strings.TrimSuffix(endpoint, "/")+"/v1/logs") + `
protocol = "binary"
headers = { Authorization = ` + q("Bearer "+token) + ` }
` + telemetryEnd + "\n"
}

func spliceManaged(data, block []byte, start, end int) []byte {
	if start != end {
		return append(append(append([]byte(nil), data[:start]...), block...), data[end:]...)
	}
	out := append([]byte(nil), data...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, block...)
}

func managedRange(data []byte) (int, int, error) {
	begin, finish := -1, -1
	for pos := 0; pos < len(data); {
		next := bytes.IndexByte(data[pos:], '\n')
		lineEnd := len(data)
		if next >= 0 {
			lineEnd = pos + next + 1
		}
		line := strings.TrimSuffix(strings.TrimSuffix(string(data[pos:lineEnd]), "\n"), "\r")
		if strings.Contains(line, "navigara telemetry") && line != telemetryBegin && line != telemetryEnd {
			return 0, 0, errors.New("malformed Navigara telemetry marker")
		}
		switch line {
		case telemetryBegin:
			if begin >= 0 || finish >= 0 {
				return 0, 0, errors.New("multiple or nested Navigara telemetry markers")
			}
			begin = pos
		case telemetryEnd:
			if begin < 0 || finish >= 0 {
				return 0, 0, errors.New("unmatched Navigara telemetry end marker")
			}
			finish = lineEnd
		}
		pos = lineEnd
	}
	if (begin >= 0) != (finish >= 0) {
		return 0, 0, errors.New("unmatched Navigara telemetry marker")
	}
	if begin < 0 {
		return len(data), len(data), nil
	}
	return begin, finish, nil
}

func managedSuffixSafe(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return strings.HasPrefix(line, "[")
	}
	return true
}

func resourceAttributes(existing, email string) (string, error) {
	var kept []string
	for _, attr := range strings.Split(existing, ",") {
		attr = strings.TrimSpace(attr)
		if attr == "" {
			continue
		}
		key, _, ok := strings.Cut(attr, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return "", errors.New("invalid OTEL_RESOURCE_ATTRIBUTES entry")
		}
		if strings.TrimSpace(key) != "enduser.id" {
			kept = append(kept, attr)
		}
	}
	return strings.Join(append(kept, "enduser.id="+email), ","), nil
}

func validValue(name, value string) error {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%s must not be empty or contain control characters", name)
	}
	return nil
}

func validEmail(email string) error {
	if err := validValue("email", email); err != nil {
		return err
	}
	if strings.ContainsAny(email, ",=") {
		return errors.New("email cannot contain comma or equals in OTEL_RESOURCE_ATTRIBUTES")
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return errors.New("email must be a plain email address")
	}
	return nil
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

var claudeFileTelemetryOverrides = []string{
	"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_HEADERS", "OTEL_EXPORTER_OTLP_METRICS_PROTOCOL",
	"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_HEADERS", "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
	"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
	"OTEL_HEADERS_HELPER", "CLAUDE_CODE_OTEL_HEADERS_HELPER",
	"BETA_TRACING_ENDPOINT", "ENABLE_ENHANCED_TELEMETRY_BETA", "ENABLE_BETA_TRACING_DETAILED",
}

func validateClaudeEnv(env map[string]string, endpoint, token string) error {
	if current := env["OTEL_EXPORTER_OTLP_ENDPOINT"]; current != "" && current != endpoint {
		return errors.New("existing conflicting Claude telemetry setting: OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	base := strings.TrimSuffix(endpoint, "/")
	want := map[string]string{
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT": base + "/v1/metrics",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT":    base + "/v1/logs",
		"OTEL_EXPORTER_OTLP_METRICS_HEADERS":  "Authorization=Bearer " + token,
		"OTEL_EXPORTER_OTLP_LOGS_HEADERS":     "Authorization=Bearer " + token,
		"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL": "http/protobuf",
		"OTEL_EXPORTER_OTLP_LOGS_PROTOCOL":    "http/protobuf",
	}
	for key, expected := range want {
		if current, ok := env[key]; ok && current != expected {
			return fmt.Errorf("existing conflicting Claude telemetry setting: %s", key)
		}
	}
	for _, key := range []string{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
		"OTEL_HEADERS_HELPER", "CLAUDE_CODE_OTEL_HEADERS_HELPER",
		"BETA_TRACING_ENDPOINT", "ENABLE_ENHANCED_TELEMETRY_BETA", "ENABLE_BETA_TRACING_DETAILED",
		"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA", "OTEL_TRACES_EXPORTER", "OTEL_LOG_USER_PROMPTS", "OTEL_LOG_ASSISTANT_RESPONSES", "OTEL_LOG_TOOL_CONTENT", "OTEL_LOG_RAW_API_BODIES",
	} {
		if activeTelemetryValue(env[key]) {
			return fmt.Errorf("existing unsupported Claude telemetry setting: %s", key)
		}
	}
	return nil
}

func activeTelemetryValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "off", "no":
		return false
	default:
		return true
	}
}
