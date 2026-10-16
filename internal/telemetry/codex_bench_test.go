package telemetry

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkBuildCodex(b *testing.B) {
	for _, name := range []string{
		"codex-recorded-responses.jsonl",
		"codex-fork-owned-after-boundary.jsonl",
		"codex-native-tool-items.jsonl",
	} {
		path := filepath.Join("testdata", name)
		info, err := os.Stat(path)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(info.Size())
			b.ResetTimer()
			for range b.N {
				if _, err := BuildCodex(path, Identity{}, time.Time{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
