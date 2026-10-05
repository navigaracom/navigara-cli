package otel

import (
	"os"
	"testing"
	"time"

	"github.com/navigaracom/navigara-cli/internal/session"
)

func mappingFixture(t *testing.T, source, file string) session.Session {
	t.Helper()
	input, err := os.Open("../session/testdata/" + file + "-usage.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	record, err := session.Read(source, input)
	if err != nil {
		t.Fatal(err)
	}
	return record.Session
}

func TestMapRecordedFixtures(t *testing.T) {
	for _, test := range []struct {
		source, file, model, id, stamp, repository string
		kinds                                      []string
		values                                     []int64
		repeated, unmapped                         int
	}{
		{"claude_code", "claude", "claude-opus-5-5", "id_001", "2026-11-08T16:10:04.225Z", "", []string{"input", "output", "cacheRead", "cacheCreation"}, []int64{2, 348, 27495, 42816}, 1, 0},
		{"codex", "codex", "gpt-6-astra", "id_007", "2026-11-08T09:10:42.921Z", "github.com/example/project", []string{"input", "output", "cached_input", "cache_write_input", "reasoning_output", "total"}, []int64{38558, 289, 28928, 0, 7, 38847}, 0, 1},
	} {
		t.Run(test.source, func(t *testing.T) {
			result := Map(mappingFixture(t, test.source, test.file))
			if len(result.Points) != len(test.values) || result.Mapped != 1 || result.Repeated != test.repeated || result.Unmapped != test.unmapped || result.Ambiguous != 0 {
				t.Fatalf("mapping: %+v", result)
			}
			for index, point := range result.Points {
				if point.Source != test.source || point.SessionID != test.id || point.Model != test.model || point.TokenType != test.kinds[index] || point.Value != test.values[index] || point.RepositoryURL != test.repository || point.Time.Format(time.RFC3339Nano) != test.stamp {
					t.Fatalf("point %d: %+v", index, point)
				}
			}
		})
	}
}

func TestClaudeLateConflictsAndCanonicalTime(t *testing.T) {
	for _, conflict := range []string{"output", "model", "request", "nil zero", "TTL"} {
		t.Run(conflict, func(t *testing.T) {
			input := mappingFixture(t, "claude_code", "claude")
			first, repeat := input.UsageObservations[0], input.UsageObservations[1]
			middle := first
			middle.MessageID, middle.RecordLine, repeat.RecordLine = "other", 3, 4
			switch conflict {
			case "output":
				repeat.MessageUsage.OutputTokens = new(int64(400))
			case "model":
				repeat.Model = "other"
			case "request":
				repeat.RequestID = "other"
			case "nil zero":
				repeat.MessageUsage.CacheCreation.Ephemeral5mInputTokens = nil
			case "TTL":
				repeat.MessageUsage.CacheCreation = nil
			}
			input.UsageObservations = []session.UsageObservation{first, middle, repeat}
			result := Map(input)
			if result.Ambiguous != 2 || result.Mapped != 1 || len(result.Points) != 4 {
				t.Fatalf("late conflict: %+v", result)
			}
		})
	}
	input := mappingFixture(t, "claude_code", "claude")
	input.UsageObservations[0].ObservedAt = nil
	result := Map(input)
	if result.Unmapped != 1 || result.Repeated != 1 || len(result.Points) != 0 {
		t.Fatalf("repeat repaired canonical: %+v", result)
	}
}

func TestMappingUnknownsValueAndTimeBounds(t *testing.T) {
	maximum := time.Unix(18446744073, 709551615).UTC()
	for _, test := range []struct {
		name   string
		mutate func(*session.UsageObservation)
		points int
	}{
		{"nil usage", func(observation *session.UsageObservation) { observation.MessageUsage = nil }, 0},
		{"missing model", func(observation *session.UsageObservation) { observation.Model = "" }, 0},
		{"missing message", func(observation *session.UsageObservation) { observation.MessageID = "" }, 0},
		{"no counters", func(observation *session.UsageObservation) { observation.MessageUsage = &session.ClaudeUsage{} }, 0},
		{"zero", func(observation *session.UsageObservation) {
			observation.MessageUsage = &session.ClaudeUsage{OutputTokens: new(int64(0))}
		}, 1},
		{"negative", func(observation *session.UsageObservation) { observation.MessageUsage.OutputTokens = new(int64(-1)) }, 0},
		{"unsafe value", func(observation *session.UsageObservation) {
			observation.MessageUsage.OutputTokens = new(int64(9007199254740992))
		}, 0},
		{"before epoch", func(observation *session.UsageObservation) { observation.ObservedAt = new(time.Unix(0, -1)) }, 0},
		{"epoch", func(observation *session.UsageObservation) { observation.ObservedAt = new(time.Unix(0, 0)) }, 0},
		{"maximum nanos", func(observation *session.UsageObservation) { observation.ObservedAt = &maximum }, 4},
		{"after maximum", func(observation *session.UsageObservation) {
			observation.ObservedAt = new(maximum.Add(time.Nanosecond))
		}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := mappingFixture(t, "claude_code", "claude")
			input.UsageObservations = input.UsageObservations[:1]
			test.mutate(&input.UsageObservations[0])
			result := Map(input)
			if len(result.Points) != test.points || (test.points == 0 && result.Unmapped != 1) {
				t.Fatalf("bounds: %+v", result)
			}
			if test.name == "zero" && (result.Points[0].Value != 0 || result.Points[0].TokenType != "output") {
				t.Fatal("zero omitted or unknown invented")
			}
		})
	}
}

func snapshot(input int64) *session.CodexUsage {
	return &session.CodexUsage{InputTokens: new(input), OutputTokens: new(int64(0)), CachedInputTokens: new(int64(0)), CacheWriteInputTokens: new(int64(0)), ReasoningOutputTokens: new(int64(0)), TotalTokens: new(input)}
}

func TestCodexBoundariesAndRepeats(t *testing.T) {
	for _, test := range []struct {
		name                        string
		mutate                      func(*session.Session)
		mapped, repeated, ambiguous int
	}{
		{"missing total", func(input *session.Session) { input.UsageObservations[1].TotalTokenUsage = nil }, 0, 0, 0},
		{"partial last", func(input *session.Session) { input.UsageObservations[1].LastTokenUsage.OutputTokens = nil }, 0, 0, 0},
		{"changed model", func(input *session.Session) { input.UsageObservations[1].Model = "other" }, 0, 0, 0},
		{"changed turn", func(input *session.Session) { input.UsageObservations[1].TurnID = "other" }, 0, 0, 0},
		{"missing previous time", func(input *session.Session) { input.UsageObservations[0].ObservedAt = nil }, 0, 0, 0},
		{"reset", func(input *session.Session) { input.UsageObservations[1].TotalTokenUsage.OutputTokens = new(int64(0)) }, 0, 0, 0},
		{"wrong last", func(input *session.Session) {
			input.UsageObservations[1].LastTokenUsage.ReasoningOutputTokens = new(int64(8))
		}, 0, 0, 1},
		{"previous last optional", func(input *session.Session) { input.UsageObservations[0].LastTokenUsage = nil }, 1, 0, 0},
		{"identical", func(input *session.Session) {
			input.UsageObservations[1].TotalTokenUsage = input.UsageObservations[0].TotalTokenUsage
			input.UsageObservations[1].LastTokenUsage = input.UsageObservations[0].LastTokenUsage
		}, 0, 1, 0},
		{"zero delta", func(input *session.Session) {
			input.UsageObservations[1].TotalTokenUsage = input.UsageObservations[0].TotalTokenUsage
			input.UsageObservations[1].LastTokenUsage = snapshot(0)
		}, 0, 1, 0},
		{"equal total changed last", func(input *session.Session) {
			input.UsageObservations[1].TotalTokenUsage = input.UsageObservations[0].TotalTokenUsage
		}, 0, 0, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := mappingFixture(t, "codex", "codex")
			test.mutate(&input)
			result := Map(input)
			if result.Mapped != test.mapped || result.Repeated != test.repeated || result.Ambiguous != test.ambiguous || result.Unmapped != 2-test.mapped-test.repeated-test.ambiguous || len(result.Points) != 6*test.mapped {
				t.Fatalf("boundary: %+v", result)
			}
		})
	}
}

func TestCodexSkippedBoundaryAndLargeTotals(t *testing.T) {
	input := mappingFixture(t, "codex", "codex")
	first := input.UsageObservations[0]
	first.TotalTokenUsage = snapshot(0)
	middle, last := first, first
	middle.TotalTokenUsage, middle.LastTokenUsage = snapshot(10), snapshot(9)
	last.TotalTokenUsage, last.LastTokenUsage = snapshot(13), snapshot(3)
	input.UsageObservations = []session.UsageObservation{first, middle, last}
	result := Map(input)
	if result.Mapped != 1 || result.Ambiguous != 1 || len(result.Points) != 6 || result.Points[0].Value != 3 {
		t.Fatalf("skipped interval leaked: %+v", result)
	}
	for _, test := range []struct {
		before, after int64
		points        int
	}{{9223372036854775800, 9223372036854775805, 6}, {0, 9007199254740991, 6}, {0, 9007199254740992, 0}} {
		input := mappingFixture(t, "codex", "codex")
		input.UsageObservations[0].TotalTokenUsage = snapshot(test.before)
		input.UsageObservations[1].TotalTokenUsage, input.UsageObservations[1].LastTokenUsage = snapshot(test.after), snapshot(test.after-test.before)
		result := Map(input)
		if len(result.Points) != test.points {
			t.Fatalf("precision: %+v", result)
		}
		if test.points != 0 && result.Points[0].Value != test.after-test.before {
			t.Fatal("integer delta lost precision")
		}
	}
}
