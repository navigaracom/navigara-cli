package otel

import (
	"reflect"
	"strings"

	"github.com/navigaracom/navigara-cli/internal/session"
)

func mapClaude(input session.Session, result *Result) {
	firstByMessage, conflicts := make(map[string]int), make(map[string]bool)
	for index, observation := range input.UsageObservations {
		if strings.TrimSpace(observation.MessageID) == "" {
			continue
		}
		first, exists := firstByMessage[observation.MessageID]
		if !exists {
			firstByMessage[observation.MessageID] = index
			continue
		}
		canonical := input.UsageObservations[first]
		if observation.Model != canonical.Model || observation.RequestID != canonical.RequestID || !reflect.DeepEqual(observation.MessageUsage, canonical.MessageUsage) {
			conflicts[observation.MessageID] = true
		}
	}
	for index, observation := range input.UsageObservations {
		switch {
		case strings.TrimSpace(observation.MessageID) == "":
			result.Unmapped++
		case conflicts[observation.MessageID]:
			result.Ambiguous++
		case index != firstByMessage[observation.MessageID]:
			// Repeated evidence does not imply its canonical observation was mapped.
			result.Repeated++
		case observation.MessageUsage == nil:
			result.Unmapped++
		default:
			usage := observation.MessageUsage
			if addPoints(result, input, observation, tokenCounter{"input", usage.InputTokens}, tokenCounter{"output", usage.OutputTokens}, tokenCounter{"cacheRead", usage.CacheReadInputTokens}, tokenCounter{"cacheCreation", usage.CacheCreationInputTokens}) {
				result.Mapped++
			} else {
				result.Unmapped++
			}
		}
	}
}
