package otel

import (
	"strings"

	"github.com/navigaracom/navigara-cli/internal/session"
)

type outcome int

const (
	unmapped outcome = iota
	mapped
	repeated
	ambiguous
)

func mapCodex(input session.Session, result *Result) {
	for index, observation := range input.UsageObservations {
		var previous *session.UsageObservation
		// Every physical observation advances the boundary, including skipped ones.
		if index > 0 {
			previous = &input.UsageObservations[index-1]
		}
		delta, status := codexInterval(previous, observation)
		if status == mapped && !addPoints(result, input, observation, tokenCounter{"input", &delta[0]}, tokenCounter{"output", &delta[1]}, tokenCounter{"cached_input", &delta[2]}, tokenCounter{"cache_write_input", &delta[3]}, tokenCounter{"reasoning_output", &delta[4]}, tokenCounter{"total", &delta[5]}) {
			status = unmapped
		}
		switch status {
		case mapped:
			result.Mapped++
		case repeated:
			result.Repeated++
		case ambiguous:
			result.Ambiguous++
		default:
			result.Unmapped++
		}
	}
}

func codexInterval(previous *session.UsageObservation, current session.UsageObservation) ([6]int64, outcome) {
	var delta [6]int64
	total, complete := codexCounters(current.TotalTokenUsage)
	if !complete || previous == nil {
		return delta, unmapped
	}
	priorTotal, complete := codexCounters(previous.TotalTokenUsage)
	if !complete {
		return delta, unmapped
	}
	last, complete := codexCounters(current.LastTokenUsage)
	if !complete || strings.TrimSpace(current.Model) == "" || strings.TrimSpace(previous.Model) == "" || strings.TrimSpace(current.TurnID) == "" || strings.TrimSpace(previous.TurnID) == "" || current.Model != previous.Model || current.TurnID != previous.TurnID {
		return delta, unmapped
	}
	if current.ObservedAt == nil || previous.ObservedAt == nil || current.ObservedAt.Before(*previous.ObservedAt) {
		return delta, unmapped
	}
	if _, valid := timestampNanos(*current.ObservedAt); !valid {
		return delta, unmapped
	}
	if _, valid := timestampNanos(*previous.ObservedAt); !valid {
		return delta, unmapped
	}
	for index := range total {
		if total[index] < priorTotal[index] {
			return delta, unmapped
		}
		// Nonnegative int64 totals permit safe subtraction without float conversion.
		delta[index] = total[index] - priorTotal[index]
	}
	if delta == [6]int64{} {
		priorLast, known := codexCounters(previous.LastTokenUsage)
		if last == [6]int64{} || (known && last == priorLast) {
			return delta, repeated
		}
		return delta, ambiguous
	}
	if delta != last {
		return delta, ambiguous
	}
	return delta, mapped
}

func codexCounters(usage *session.CodexUsage) ([6]int64, bool) {
	var values [6]int64
	if usage == nil {
		return values, false
	}
	for index, counter := range []*int64{usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens, usage.CacheWriteInputTokens, usage.ReasoningOutputTokens, usage.TotalTokens} {
		if counter == nil || *counter < 0 {
			return values, false
		}
		values[index] = *counter
	}
	return values, true
}
