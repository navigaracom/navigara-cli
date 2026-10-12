package main

import "testing"

func TestWizardPathValueEscapesOnlyTerminalControls(t *testing.T) {
	for _, test := range []struct {
		name string
		in   string
		want string
	}{
		{"printable quote and backslash", `my"dir\\file`, `my"dir\\file`},
		{"graphic unicode", "žluťoučký\u00a0📁", "žluťoučký\u00a0📁"},
		{"controls", "line\n\t\x1b", `line\n\t\x1b`},
		{"invalid utf8", string([]byte{'a', 0xff}), `a\xFF`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := wizardPathValue(test.in); got != test.want {
				t.Fatalf("wizardPathValue(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}
