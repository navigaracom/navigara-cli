package main

import "testing"

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
