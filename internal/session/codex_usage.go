package session

import (
	"encoding/json"
	"fmt"
	"time"
)

type codexContext struct {
	Model  string `json:"model"`
	TurnID string `json:"turn_id"`
}

func (c *codexContext) observe(b []byte, kind string, observedAt *time.Time, line int) (*UsageObservation, error) {
	if kind != "turn_context" && kind != "event_msg" {
		return nil, nil
	}
	var row struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(b, &row); err != nil {
		return nil, fmt.Errorf("invalid Codex usage")
	}
	if kind == "turn_context" {
		*c = codexContext{} // Every context replaces prior fields, even when absent/null.
		if len(row.Payload) > 0 && json.Unmarshal(row.Payload, c) != nil {
			return nil, fmt.Errorf("invalid Codex context")
		}
		return nil, nil
	}
	// Decode the discriminator before touching info; unrelated payloads stay opaque.
	var event struct {
		Type json.RawMessage `json:"type"`
		Info json.RawMessage `json:"info"`
	}
	if json.Unmarshal(row.Payload, &event) != nil {
		return nil, nil
	}
	var eventType string
	if json.Unmarshal(event.Type, &eventType) != nil || eventType != "token_count" || len(event.Info) == 0 {
		return nil, nil
	}
	var info struct {
		Total *CodexUsage `json:"total_token_usage"`
		Last  *CodexUsage `json:"last_token_usage"`
	}
	if json.Unmarshal(event.Info, &info) != nil {
		return nil, fmt.Errorf("invalid Codex usage")
	}
	for _, usage := range []*CodexUsage{info.Total, info.Last} {
		if usage == nil {
			continue
		}
		for _, counter := range []*int64{usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens, usage.CacheWriteInputTokens, usage.ReasoningOutputTokens, usage.TotalTokens} {
			if counter != nil && *counter < 0 {
				return nil, fmt.Errorf("invalid Codex usage")
			}
		}
	}
	if info.Total == nil && info.Last == nil {
		return nil, nil
	}
	u := &UsageObservation{RecordLine: line, ObservedAt: observedAt, Model: c.Model, TurnID: c.TurnID, TotalTokenUsage: info.Total, LastTokenUsage: info.Last}
	return u, nil
}
