package session

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

func observeClaude(b []byte, timestamp string, line int) (*UsageObservation, error) {
	var row struct {
		Type    json.RawMessage `json:"type"`
		Message json.RawMessage `json:"message"`
	}
	if json.Unmarshal(b, &row) != nil {
		return nil, fmt.Errorf("invalid Claude usage")
	}
	var kind string
	if json.Unmarshal(row.Type, &kind) != nil || kind != "assistant" {
		return nil, nil
	}
	// Locate usage before decoding IDs/model; assistant rows without usage stay opaque.
	var message struct {
		Usage json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(row.Message, &message) != nil || len(message.Usage) == 0 || bytes.Equal(bytes.TrimSpace(message.Usage), []byte("null")) {
		return nil, nil
	}
	var usage ClaudeUsage
	if json.Unmarshal(message.Usage, &usage) != nil {
		return nil, fmt.Errorf("invalid Claude usage")
	}
	counters := []*int64{usage.InputTokens, usage.OutputTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens}
	if usage.CacheCreation != nil {
		counters = append(counters, usage.CacheCreation.Ephemeral5mInputTokens, usage.CacheCreation.Ephemeral1hInputTokens)
	}
	for _, counter := range counters {
		if counter != nil && *counter < 0 {
			return nil, fmt.Errorf("invalid Claude usage")
		}
	}
	var metadata struct {
		UUID      string `json:"uuid"`
		RequestID string `json:"requestId"`
		Message   struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		} `json:"message"`
	}
	if json.Unmarshal(b, &metadata) != nil {
		return nil, fmt.Errorf("invalid Claude usage")
	}
	u := &UsageObservation{RecordLine: line, EventID: metadata.UUID, RequestID: metadata.RequestID, MessageID: metadata.Message.ID, Model: metadata.Message.Model, MessageUsage: &usage}
	if timestamp != "" {
		t, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return nil, fmt.Errorf("invalid timestamp")
		}
		t = t.UTC()
		u.ObservedAt = &t
	}
	return u, nil
}
