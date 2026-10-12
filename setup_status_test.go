package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

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

func TestSetupUploadLineFitsTerminalAndKeepsLabelBarGap(t *testing.T) {
	line := setupUploadLine(2, 4, "~10s left", 72)
	if !strings.Contains(ansi.Strip(line), "Uploading history  [") {
		t.Fatalf("upload line lacks label/bar spacing: %q", line)
	}
	for _, width := range []int{40, 12, 4} {
		line := setupUploadLine(2, 4, "~10s left", width)
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("upload line is %d columns at width %d: %q", got, width, line)
		}
	}
}

func TestSetupUploadLineReservesRoomForETAAtNormalWidth(t *testing.T) {
	line := ansi.Strip(setupUploadLine(2568, 6831, "~3m 20s left", 72))
	if !strings.Contains(line, "Uploading history  [") || !strings.Contains(line, "~3m 20s left") {
		t.Fatalf("normal-width upload line omits label, bar, or ETA: %q", line)
	}
	if got := ansi.StringWidth(line); got > 72 {
		t.Fatalf("upload line is %d columns: %q", got, line)
	}
}
