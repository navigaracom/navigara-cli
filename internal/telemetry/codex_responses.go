package telemetry

import (
	"encoding/json"
	"fmt"
	"time"
)

type codexResponse struct {
	responseID, turnID, rootTurnID, processID string
	usage                                     codexTokens
	at                                        time.Time
}

type codexResponseContext struct {
	Model, Effort string
	ambiguous     bool
}

func readCodexResponse(data []byte, threadID string, at time.Time) (*codexResponse, error) {
	var owner struct {
		ThreadID string `json:"thread_id"`
	}
	if json.Unmarshal(data, &owner) != nil || owner.ThreadID == "" {
		return nil, fmt.Errorf("missing response thread identity")
	}
	if owner.ThreadID != threadID {
		return nil, nil
	}
	var record struct {
		ResponseID string          `json:"response_id"`
		TurnID     string          `json:"turn_id"`
		RootTurnID string          `json:"root_turn_id"`
		SessionID  string          `json:"session_id"`
		Usage      json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(data, &record) != nil || record.ResponseID == "" {
		return nil, fmt.Errorf("invalid response identity")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(record.Usage, &fields) != nil || fields == nil {
		return nil, fmt.Errorf("missing response usage")
	}
	response := &codexResponse{responseID: record.ResponseID, turnID: record.TurnID,
		rootTurnID: record.RootTurnID, processID: record.SessionID, at: at}
	for _, field := range []struct {
		name  string
		value *int64
	}{
		{"input_tokens", &response.usage.Input}, {"cached_input_tokens", &response.usage.CachedInput},
		{"cache_write_input_tokens", &response.usage.CacheWriteInput}, {"output_tokens", &response.usage.Output},
		{"reasoning_output_tokens", &response.usage.ReasoningOutput}, {"total_tokens", &response.usage.Total},
	} {
		raw, present := fields[field.name]
		if !present && field.name == "cache_write_input_tokens" {
			continue // Older Codex records omit this optional counter.
		}
		var value *int64
		if json.Unmarshal(raw, &value) != nil || value == nil || *value < 0 {
			return nil, fmt.Errorf("invalid response %s", field.name)
		}
		*field.value = *value
	}
	return response, nil
}

func recordCodexContext(data []byte, contexts map[string]codexResponseContext) {
	var turn struct {
		TurnID string `json:"turn_id"`
		Model  string `json:"model"`
		Effort string `json:"effort"`
	}
	if json.Unmarshal(data, &turn) != nil || turn.TurnID == "" {
		return
	}
	context := codexResponseContext{Model: turn.Model, Effort: turn.Effort}
	if prior, exists := contexts[turn.TurnID]; exists && prior != context {
		context.ambiguous = true
	}
	contexts[turn.TurnID] = context
}

func (response codexResponse) attributes(context codexResponseContext) map[string]any {
	attrs := map[string]any{
		"event.kind": "response.completed", "response_id": response.responseID,
		"input_token_count":       fmt.Sprint(response.usage.Input),
		"cached_token_count":      fmt.Sprint(response.usage.CachedInput),
		"cache_write_token_count": fmt.Sprint(response.usage.CacheWriteInput),
		"output_token_count":      fmt.Sprint(response.usage.Output),
		"reasoning_token_count":   fmt.Sprint(response.usage.ReasoningOutput),
		"tool_token_count":        fmt.Sprint(response.usage.Total),
	}
	for key, value := range map[string]string{"turn_id": response.turnID,
		"root_turn_id": response.rootTurnID, "codex.session_id": response.processID} {
		if value != "" {
			attrs[key] = value
		}
	}
	if !context.ambiguous {
		if context.Model != "" {
			attrs["model"] = context.Model
		}
		if context.Effort != "" {
			attrs["model_reasoning_effort"] = context.Effort
		}
	}
	return attrs
}
