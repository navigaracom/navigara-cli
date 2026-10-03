package otel

import (
	"strings"
	"time"

	"github.com/navigaracom/navigara-cli/internal/session"
)

const maxJSONInteger = int64(1<<53 - 1)

type Point struct {
	Source, SessionID, Model, TokenType, RepositoryURL string
	Time                                               time.Time
	Value                                              int64
}

// Result counts usage observations separately from emitted token points.
type Result struct {
	Points                                []Point
	Mapped, Repeated, Ambiguous, Unmapped int
}

// Map preserves only supported recorded token facts, without pricing or identity inference.
func Map(input session.Session) Result {
	result := Result{Points: []Point{}}
	if strings.TrimSpace(input.SessionID) == "" {
		result.Unmapped = len(input.UsageObservations)
		return result
	}
	switch input.Source {
	case "claude_code":
		mapClaude(input, &result)
	case "codex":
		mapCodex(input, &result)
	default:
		result.Unmapped = len(input.UsageObservations)
	}
	return result
}

type tokenCounter struct {
	kind  string
	value *int64
}

func addPoints(result *Result, input session.Session, observation session.UsageObservation, counters ...tokenCounter) bool {
	if strings.TrimSpace(observation.Model) == "" || observation.ObservedAt == nil {
		return false
	}
	if _, valid := timestampNanos(*observation.ObservedAt); !valid {
		return false
	}
	count := 0
	for _, counter := range counters {
		if counter.value != nil {
			if *counter.value < 0 || *counter.value > maxJSONInteger {
				return false
			}
			count++
		}
	}
	if count == 0 {
		return false
	}
	var repositoryURL string
	for _, repository := range input.RepositoryObservations {
		if repository.RecordLine > observation.RecordLine {
			break
		}
		repositoryURL = repository.RepositoryURL
	}
	for _, counter := range counters {
		if counter.value != nil {
			result.Points = append(result.Points, Point{Source: input.Source, SessionID: input.SessionID,
				Model: observation.Model, TokenType: counter.kind, RepositoryURL: repositoryURL,
				Time: observation.ObservedAt.UTC(), Value: *counter.value})
		}
	}
	return true
}

// timestampNanos avoids signed UnixNano overflow and the receiver's zero-timestamp drop.
func timestampNanos(stamp time.Time) (uint64, bool) {
	seconds := stamp.Unix()
	if seconds < 0 || uint64(seconds) > ^uint64(0)/1_000_000_000 {
		return 0, false
	}
	whole, fraction := uint64(seconds)*1_000_000_000, uint64(stamp.Nanosecond())
	if fraction > ^uint64(0)-whole {
		return 0, false
	}
	nanos := whole + fraction
	return nanos, nanos != 0
}
