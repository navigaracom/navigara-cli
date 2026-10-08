package main

import (
	"bytes"
	"os"
	"testing"
)

func TestHasOTELTOML(t *testing.T) {
	for _, test := range []struct {
		name, input string
		want        bool
	}{
		{"multiline values", `
closed = """'''"""
escaped = "\"\"\"'''"
escaped_triple = """\"""ok"""
four = """x""""
five = """x"""""
literal_four = '''x''''
literal_five = '''x'''''
instructions = """
otel = 'notes'
"""
items = [
  { otel = "notes" },
  "otel",
]
inline = { otel = "notes" }
[projects."C:\\otel\\repo"]
[mcp_servers.foo]
otel = "notes"
`, false},
		{"case-sensitive root", "[OTEL]\n", false},
		{"root after nested array", `items = [
  [1, 2],
]
otel.exporter = "none"
`, true},
		{"root table", "[otel.metrics_exporter.otlp-http]\n", true},
		{"unclosed", "items = [\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := hasOTELTOML([]byte(test.input)); got != test.want {
				t.Fatalf("hasOTELTOML() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCodexTelemetryRange(t *testing.T) {
	fixture, err := os.ReadFile("testdata/codex-existing-telemetry.toml")
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "model = \"outside\"\n\n"
	const suffix = "[projects.outside]\ntrust_level = \"trusted\"\n"
	input := append(append([]byte(prefix), fixture...), suffix...)
	start, end, ok := codexTelemetryRange(input)
	if !ok {
		t.Fatal("fixture was not recognized")
	}
	if got := input[start:end]; !bytes.Equal(got, fixture) {
		t.Fatalf("recognized range = %q, want fixture", got)
	}
	if !bytes.Equal(input[:start], []byte(prefix)) || !bytes.Equal(input[end:], []byte(suffix)) {
		t.Fatal("range does not preserve surrounding bytes")
	}

	for _, input := range []string{
		"[otel.metrics_exporter.otlp-http]\nendpoint = \"x\"\n",
		"[otel.metrics_exporter.otlp-http]\nendpoint = \"x\"\n[other]\n[otel.exporter.otlp-http]\nendpoint = \"x\"\n",
		"[[otel.metrics_exporter.otlp-http]]\n[otel.exporter.otlp-http]\n",
		"otel.exporter = { endpoint = \"x\" }\n",
		"[otel.metrics_exporter.otlp-http]\nunknown = \"x\"\n[otel.exporter.otlp-http]\n",
		"[otel.metrics_exporter.otlp-http]\n[otel.exporter.otlp-http]\nunknown = \"x\"\n",
		"[otel.metrics_exporter.otlp-http]\nheaders.Authorization = \"x\"\n[otel.exporter.otlp-http]\n",
		"[otel.metrics_exporter.otlp-http]\nheaders = { Authorization = \"x\"\n[otel.exporter.otlp-http]\n",
		string(append(append([]byte{}, fixture...), []byte("[otel.exporter.otlp-http]\n")...)),
	} {
		if _, _, ok := codexTelemetryRange([]byte(input)); ok {
			t.Fatalf("unsafe input was accepted: %q", input)
		}
	}
	if _, _, ok := codexTelemetryRange(append([]byte("# [otel.exporter.otlp-http]\nnote = \"[otel.metrics_exporter.otlp-http]\"\n"), fixture...)); !ok {
		t.Fatal("comments or strings were mistaken for telemetry")
	}
}
