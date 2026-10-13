package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestSetupProgressETAWaitsForUsefulKnownTotalSample(t *testing.T) {
	started := time.Date(2026, time.October, 12, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name        string
		done, total int
		now         time.Time
		want        string
	}{
		{"missing start", 2, 4, started.Add(10 * time.Second), ""},
		{"unknown total", 2, 0, started.Add(10 * time.Second), ""},
		{"zero progress", 0, 4, started.Add(10 * time.Second), ""},
		{"short sample", 2, 4, started.Add(999 * time.Millisecond), ""},
		{"warm up", 1, 4, started.Add(10 * time.Second), ""},
		{"completed", 4, 4, started.Add(10 * time.Second), ""},
		{"estimated", 2, 4, started.Add(10 * time.Second), "~10s left"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var eta setupProgressETACache
			if test.name != "missing start" {
				eta.update(started, 0, test.total)
			}
			if got := eta.update(test.now, test.done, test.total); got != test.want {
				t.Fatalf("ETA = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSetupProgressETACacheUsesRecentCompletedWork(t *testing.T) {
	base := time.Date(2026, time.October, 12, 10, 0, 0, 0, time.UTC)
	var eta setupProgressETACache

	if got := eta.update(base, 0, 3600); got != "" {
		t.Fatalf("initial ETA = %q, want empty", got)
	}
	if got := eta.update(base.Add(22*time.Second), 60, 3600); got != "" {
		t.Fatalf("warmup ETA = %q, want empty", got)
	}
	if got := eta.update(base.Add(27*time.Second), 900, 3600); got != "~16s left" {
		t.Fatalf("recent ETA = %q, want ~16s left", got)
	}
	if got := eta.update(base.Add(28*time.Second), 950, 3600); got != "~16s left" {
		t.Fatalf("idle ETA = %q, want cached ~16s left", got)
	}
	if got := eta.update(base.Add(32*time.Second), 1900, 3600); got != "~8s left" {
		t.Fatalf("refreshed ETA = %q, want ~8s left", got)
	}
	if got := eta.update(base.Add(33*time.Second), 3600, 3600); got != "" {
		t.Fatalf("completed ETA = %q, want empty", got)
	}
}

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
