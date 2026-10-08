package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSetupHistoryProgressPlainPhasesAreANSIless(t *testing.T) {
	var out bytes.Buffer
	p := &setupInput{out: &out}
	progress := newSetupHistoryProgress(p)
	progress.discovering()
	progress.processed(0, 2)
	progress.processed(1, 2)
	progress.processed(2, 2)
	progress.finalizing()
	progress.stop()

	got := out.String()
	for _, want := range []string{
		"Finding session history",
		"Preparing history | 0/2 sessions checked",
		"Preparing history | 1/2 sessions checked",
		"Preparing history | 2/2 sessions checked",
		"Finalizing history",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("plain progress contains ANSI: %q", got)
	}
}
