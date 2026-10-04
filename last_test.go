package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
