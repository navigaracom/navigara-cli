// Package session defines the local session observations.
package session

import "time"

// Session contains native identity and observation bounds, not lifecycle events.
// Adapters map claude to claude_code and codex to codex and normalize times to UTC.
type Session struct {
	Source                 string                  `json:"source"`
	SessionID              string                  `json:"session_id"`
	FirstObservedAt        *time.Time              `json:"first_observed_at,omitempty"`
	LastObservedAt         *time.Time              `json:"last_observed_at,omitempty"`
	SourceVersion          string                  `json:"source_version,omitempty"`
	UsageObservations      []UsageObservation      `json:"usage_observations,omitempty"`
	RepositoryObservations []RepositoryObservation `json:"repository_observations,omitempty"`
}

// RepositoryObservation contains recorded context, never inferred from local git.
type RepositoryObservation struct {
	RecordLine    int        `json:"record_line"`
	ObservedAt    *time.Time `json:"observed_at,omitempty"`
	Branch        string     `json:"branch,omitempty"`
	CommitHash    string     `json:"commit_hash,omitempty"`
	RepositoryURL string     `json:"repository_url,omitempty"`
}

// UsageObservation preserves provider-native usage at a physical JSONL line.
// Model records context; it does not attribute cumulative totals to that model.
type UsageObservation struct {
	RecordLine      int          `json:"record_line"`
	ObservedAt      *time.Time   `json:"observed_at,omitempty"`
	EventID         string       `json:"event_id,omitempty"`
	MessageID       string       `json:"message_id,omitempty"`
	RequestID       string       `json:"request_id,omitempty"`
	TurnID          string       `json:"turn_id,omitempty"`
	Model           string       `json:"model,omitempty"`
	MessageUsage    *ClaudeUsage `json:"message_usage,omitempty"`
	TotalTokenUsage *CodexUsage  `json:"total_token_usage,omitempty"`
	LastTokenUsage  *CodexUsage  `json:"last_token_usage,omitempty"`
}

// ClaudeUsage preserves message counters. Nil means unknown; zero is explicit.
type ClaudeUsage struct {
	InputTokens              *int64               `json:"input_tokens,omitempty"`
	OutputTokens             *int64               `json:"output_tokens,omitempty"`
	CacheReadInputTokens     *int64               `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens *int64               `json:"cache_creation_input_tokens,omitempty"`
	CacheCreation            *ClaudeCacheCreation `json:"cache_creation,omitempty"`
}

// ClaudeCacheCreation preserves the native cache-duration counters.
type ClaudeCacheCreation struct {
	Ephemeral5mInputTokens *int64 `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens *int64 `json:"ephemeral_1h_input_tokens,omitempty"`
}

// CodexUsage preserves a native total or last token-count snapshot.
type CodexUsage struct {
	InputTokens           *int64 `json:"input_tokens,omitempty"`
	OutputTokens          *int64 `json:"output_tokens,omitempty"`
	CachedInputTokens     *int64 `json:"cached_input_tokens,omitempty"`
	CacheWriteInputTokens *int64 `json:"cache_write_input_tokens,omitempty"`
	ReasoningOutputTokens *int64 `json:"reasoning_output_tokens,omitempty"`
	TotalTokens           *int64 `json:"total_tokens,omitempty"`
}
