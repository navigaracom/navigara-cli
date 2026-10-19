package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	telemetryBegin = "# >>> navigara telemetry >>>"
	telemetryEnd   = "# <<< navigara telemetry <<<"
)

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
	for key, value := range claudeTelemetryEnv(endpoint, token, attrs) {
		env[key] = value
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return nil, err
	}
	settings["env"] = raw
	return json.MarshalIndent(settings, "", "  ")
}

func claudeTelemetryEnv(endpoint, token, attrs string) map[string]string {
	return map[string]string{
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
		"OTEL_LOG_USER_PROMPTS":               "1",
		"OTEL_LOG_ASSISTANT_RESPONSES":        "0",
		"OTEL_LOG_TOOL_CONTENT":               "0",
		"OTEL_LOG_RAW_API_BODIES":             "0",
		"OTEL_TRACES_EXPORTER":                "none",
		"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA": "0",
	}
}

// claudeTelemetryChanges lists the existing settings.json telemetry values
// that claudeSettings replaces or removes. Values that Navigara wrote before
// (same endpoint) are updates, not conflicts.
func claudeTelemetryChanges(data []byte, endpoint, token string) ([]string, error) {
	var settings struct {
		Env               map[string]string `json:"env"`
		OtelHeadersHelper *string           `json:"otelHeadersHelper"`
	}
	if len(bytes.TrimSpace(data)) == 0 || json.Unmarshal(data, &settings) != nil {
		return nil, nil
	}
	var changes []string
	if settings.OtelHeadersHelper != nil {
		changes = append(changes, telemetryChange("otelHeadersHelper", *settings.OtelHeadersHelper, ""))
	}
	for _, key := range claudeFileTelemetryOverrides {
		if old, ok := settings.Env[key]; ok {
			changes = append(changes, telemetryChange(key, old, ""))
		}
	}
	current := settings.Env["OTEL_EXPORTER_OTLP_ENDPOINT"]
	if current != endpoint {
		want := claudeTelemetryEnv(endpoint, token, "")
		keys := make([]string, 0, len(want))
		for key := range want {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if old, ok := settings.Env[key]; ok && key != "OTEL_RESOURCE_ATTRIBUTES" && old != want[key] {
				changes = append(changes, telemetryChange(key, old, want[key]))
			}
		}
	}
	if len(changes) == 0 {
		return nil, nil
	}
	if current != "" && current != endpoint {
		return changes, fmt.Errorf("Claude Code already sends telemetry to %s. Re-run with --replace, or use --source codex.", current)
	}
	return changes, errors.New("Claude Code already has telemetry settings. Re-run with --replace, or use --source codex.")
}

func telemetryChange(key, old, new string) string {
	if new == "" {
		return fmt.Sprintf("%s: %s -> (removed)", key, maskTelemetry(key, old))
	}
	return fmt.Sprintf("%s: %s -> %s", key, maskTelemetry(key, old), maskTelemetry(key, new))
}

// maskTelemetry hides header values, which usually hold secrets.
func maskTelemetry(key, value string) string {
	if !strings.Contains(key, "HEADERS") {
		return value
	}
	parts := strings.Split(value, ",")
	for i, part := range parts {
		name, _, _ := strings.Cut(part, "=")
		parts[i] = strings.TrimSpace(name) + "=***"
	}
	return strings.Join(parts, ",")
}

func codexSettings(data []byte, endpoint, token string) ([]byte, error) {
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
	}
	block := []byte(codexBlock(endpoint, token))
	if start != end {
		if !managedSuffixSafe(data[end:]) {
			return nil, errors.New("Navigara managed TOML block is followed by root keys; move those keys before the block or into a table")
		}
	}
	out := spliceManaged(data, block, start, end)
	out, _, err = removeObsoleteCodexFeatures(out)
	if err != nil {
		return nil, err
	}
	if err := validateTOML(out); err != nil {
		return nil, err
	}
	return out, nil
}

// codexTelemetryChanges lists what codexSettings replaces or removes. Telemetry
// outside the Navigara block is a conflict.
func codexTelemetryChanges(data []byte) ([]string, error) {
	var changes []string
	_, removed, _ := removeObsoleteCodexFeatures(data)
	for _, key := range removed {
		changes = append(changes, "[features] "+key+": (removed)")
	}
	start, end, err := tomlManagedRange(data)
	if err != nil || start != end || !hasOTELTOML(data) {
		return changes, nil
	}
	endpoint := "another destination"
	var decoded struct {
		Otel map[string]map[string]struct {
			Endpoint string `toml:"endpoint"`
		} `toml:"otel"`
	}
	if toml.Unmarshal(data, &decoded) == nil {
		for _, name := range []string{"exporter", "metrics_exporter", "trace_exporter"} {
			if e := decoded.Otel[name]["otlp-http"].Endpoint; e != "" {
				endpoint = e
				break
			}
		}
	}
	changes = append(changes, "[otel] exporters: "+endpoint+" -> Navigara")
	return changes, fmt.Errorf("Codex already sends telemetry to %s. Re-run with --replace, or use --source claude.", endpoint)
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

func fishSettings(data []byte, email string) ([]byte, error) {
	if err := validEmail(email); err != nil {
		return nil, err
	}
	start, end, err := managedRange(data)
	if err != nil {
		return nil, err
	}
	block := []byte(telemetryBegin + `
set -l navigara_attrs (string split , -- "$OTEL_RESOURCE_ATTRIBUTES" | string match -v -r '^(enduser\.id=.*)?$')
set -gx OTEL_RESOURCE_ATTRIBUTES (string join , -- $navigara_attrs ` + fishQuote("enduser.id="+email) + `)
` + telemetryEnd + "\n")
	return spliceManaged(data, block, start, end), nil
}

func fishQuote(value string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, "'", `\'`).Replace(value) + "'"
}

// removeManagedBlock removes the Navigara block from a shell profile or, with
// tomlManagedRange, from Codex config.toml.
func removeManagedBlock(data []byte, managed func([]byte) (int, int, error)) ([]byte, error) {
	start, end, err := managed(data)
	if err != nil || start == end {
		return data, err
	}
	return append(append([]byte(nil), data[:start]...), data[end:]...), nil
}

// removeClaudeSettings removes the env keys that claudeSettings wrote. It
// changes nothing unless the endpoint is a Navigara endpoint.
func removeClaudeSettings(data []byte) ([]byte, error) {
	var settings map[string]json.RawMessage
	if len(bytes.TrimSpace(data)) == 0 {
		return data, nil
	}
	if err := json.Unmarshal(data, &settings); err != nil || settings == nil {
		return nil, errors.New("Claude settings must be a JSON object")
	}
	env := map[string]string{}
	if raw, ok := settings["env"]; ok {
		if err := json.Unmarshal(raw, &env); err != nil || env == nil {
			return nil, errors.New("Claude settings env must be a JSON object of strings")
		}
	}
	endpoint := env["OTEL_EXPORTER_OTLP_ENDPOINT"]
	if !strings.HasSuffix(endpoint, "/api/otlp") {
		return data, nil
	}
	for key, value := range claudeTelemetryEnv(endpoint, "", "") {
		switch key {
		case "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_HEADERS":
			delete(env, key)
		case "OTEL_RESOURCE_ATTRIBUTES":
			var kept []string
			for _, attr := range strings.Split(env[key], ",") {
				if name, _, _ := strings.Cut(attr, "="); attr != "" && strings.TrimSpace(name) != "enduser.id" {
					kept = append(kept, attr)
				}
			}
			if len(kept) == 0 {
				delete(env, key)
			} else {
				env[key] = strings.Join(kept, ",")
			}
		default:
			if env[key] == value {
				delete(env, key)
			}
		}
	}
	if len(env) == 0 {
		delete(settings, "env")
	} else {
		raw, err := json.Marshal(env)
		if err != nil {
			return nil, err
		}
		settings["env"] = raw
	}
	return json.MarshalIndent(settings, "", "  ")
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

[otel.trace_exporter.otlp-http]
endpoint = ` + q(strings.TrimSuffix(endpoint, "/")+"/v1/traces") + `
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
	conflict := func(key string) error {
		return fmt.Errorf("%s is set in your shell environment (%s); unset it and retry.", key, maskTelemetry(key, env[key]))
	}
	if current := env["OTEL_EXPORTER_OTLP_ENDPOINT"]; current != "" && current != endpoint {
		return conflict("OTEL_EXPORTER_OTLP_ENDPOINT")
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
			return conflict(key)
		}
	}
	if current := strings.ToLower(strings.TrimSpace(env["OTEL_LOG_USER_PROMPTS"])); activeTelemetryValue(current) && current != "1" && current != "true" {
		return conflict("OTEL_LOG_USER_PROMPTS")
	}
	if current := strings.ToLower(strings.TrimSpace(env["OTEL_TRACES_EXPORTER"])); activeTelemetryValue(current) && current != "none" {
		return conflict("OTEL_TRACES_EXPORTER")
	}
	for _, key := range []string{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
		"OTEL_HEADERS_HELPER", "CLAUDE_CODE_OTEL_HEADERS_HELPER",
		"BETA_TRACING_ENDPOINT", "ENABLE_ENHANCED_TELEMETRY_BETA", "ENABLE_BETA_TRACING_DETAILED",
		"CLAUDE_CODE_ENHANCED_TELEMETRY_BETA", "OTEL_LOG_ASSISTANT_RESPONSES", "OTEL_LOG_TOOL_CONTENT", "OTEL_LOG_RAW_API_BODIES",
	} {
		if activeTelemetryValue(env[key]) {
			return conflict(key)
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
