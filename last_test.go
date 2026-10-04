package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/navigaracom/navigara-cli/internal/otel"
	"github.com/navigaracom/navigara-cli/internal/session"
)

func TestParseLast(t *testing.T) {
	for _, test := range []struct {
		value string
		want  time.Duration
		valid bool
	}{
		{"24h", 24 * time.Hour, true}, {"90m", 90 * time.Minute, true}, {"7d", 7 * 24 * time.Hour, true},
		{"", 0, false}, {"0", 0, false}, {"0d", 0, false}, {"-1h", 0, false}, {"+1d", 0, false}, {"1w", 0, false}, {"1.5d", 0, false}, {"999999999999999d", 0, false},
	} {
		got, err := parseLast(test.value)
		if (err == nil) != test.valid || got != test.want {
			t.Fatalf("parseLast(%q) = %v, %v", test.value, got, err)
		}
	}
}

func TestLastInvalidCreatesNoOutput(t *testing.T) {
	for _, value := range []string{"", "0", "-1h", "1w", "999999999999999d"} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output")
			var stdout, stderr bytes.Buffer
			if code := run([]string{"sessions", "upload", "--last=" + value, "--output", output}, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "--last") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatalf("output created: %v", err)
			}
		})
	}
}

func TestLastWiresRunToMappedFixturePoints(t *testing.T) {
	for _, test := range []struct {
		last, summary string
	}{
		{"1h", "points=4 batches=1 mapped=2 repeated=1 ambiguous=0 unmapped=1 filtered=6"},
		{"3h", "points=10 batches=2 mapped=2 repeated=1 ambiguous=0 unmapped=1 filtered=0"},
		{"1d", "points=10 batches=2 mapped=2 repeated=1 ambiguous=0 unmapped=1 filtered=0"},
	} {
		t.Run(test.last, func(t *testing.T) {
			home := t.TempDir()
			claude, codex := filepath.Join(home, "claude"), filepath.Join(home, "codex")
			writeRecentUsageFixtures(t, claude, codex, time.Now().UTC())
			output := filepath.Join(home, "output")
			var stdout, stderr bytes.Buffer
			if code := run([]string{"sessions", "upload", "--last", test.last, "--output", output, "--claude-home", claude, "--codex-home", codex}, &stdout, &stderr); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), test.summary) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestLastFiltersMappedFixturePoints(t *testing.T) {
	var records []session.Record
	for _, source := range []string{"claude", "codex"} {
		data, err := os.ReadFile(filepath.Join("internal", "session", "testdata", source+"-usage.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		record, err := session.Read(map[string]string{"claude": "claude_code", "codex": "codex"}[source], bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	cutoff := time.Date(2026, 11, 8, 9, 10, 42, 921000000, time.UTC)
	output := filepath.Join(t.TempDir(), "output")
	var stdout, stderr bytes.Buffer
	if code := writeUpload(options{output: output, since: cutoff, until: cutoff}, session.Result{Files: 2, Records: records}, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "points=6 batches=1 mapped=2 repeated=1 ambiguous=0 unmapped=1 filtered=4") {
		t.Fatalf("summary=%q", stdout.String())
	}
	data, err := os.ReadFile(filepath.Join(output, "metrics-000001.json"))
	if err != nil || bytes.Contains(data, []byte("claude_code.token.usage")) || !bytes.Contains(data, []byte("codex.turn.token_usage")) {
		t.Fatalf("body=%q err=%v", data, err)
	}
}

func TestFilterPointsIsInclusive(t *testing.T) {
	start := time.Date(2026, 10, 3, 8, 0, 0, 0, time.UTC)
	points := []otel.Point{{Time: start.Add(-time.Nanosecond)}, {Time: start}, {Time: start.Add(time.Hour)}, {Time: start.Add(2 * time.Hour)}, {Time: start.Add(2*time.Hour + time.Nanosecond)}}
	kept, filtered := filterPoints(points, start, start.Add(2*time.Hour))
	if filtered != 2 || len(kept) != 3 || !kept[0].Time.Equal(start) || !kept[2].Time.Equal(start.Add(2*time.Hour)) {
		t.Fatalf("kept=%+v filtered=%d", kept, filtered)
	}
}

func writeRecentUsageFixtures(t *testing.T, claudeHome, codexHome string, now time.Time) {
	t.Helper()
	claude, err := os.ReadFile(filepath.Join("internal", "session", "testdata", "claude-usage.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	codex, err := os.ReadFile(filepath.Join("internal", "session", "testdata", "codex-usage.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rewrite := func(data []byte, old string, stamp time.Time) []byte {
		return bytes.ReplaceAll(data, []byte(old), []byte(stamp.Format(time.RFC3339Nano)))
	}
	claude = rewrite(claude, "2026-11-08T16:10:04.225Z", now.Add(-time.Minute))
	claude = rewrite(claude, "2026-11-08T16:10:05.707Z", now.Add(-30*time.Second))
	codex = rewrite(codex, "2026-11-08T09:06:40.678Z", now.Add(-2*time.Hour-7*time.Minute))
	codex = rewrite(codex, "2026-11-08T09:10:13.870Z", now.Add(-2*time.Hour-3*time.Minute))
	codex = rewrite(codex, "2026-11-08T09:10:14.143Z", now.Add(-2*time.Hour-2*time.Minute))
	codex = rewrite(codex, "2026-11-08T09:10:29.108Z", now.Add(-2*time.Hour-time.Minute))
	codex = rewrite(codex, "2026-11-08T09:10:42.921Z", now.Add(-2*time.Hour))
	putHistory(t, filepath.Join(claudeHome, "projects", "example", "claude.jsonl"), claude)
	putHistory(t, filepath.Join(codexHome, "sessions", "codex.jsonl"), codex)
}
