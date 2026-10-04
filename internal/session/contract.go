// Package session defines the local session request contract.
package session

import "time"

const SchemaVersion = 1

// Envelope requires Sessions to be a nonnil slice so an empty inventory is [].
type Envelope struct {
	SchemaVersion int       `json:"schema_version"`
	Sessions      []Session `json:"sessions"`
}

// Session contains native identity and observation bounds, not lifecycle events.
// Adapters map claude to claude_code and codex to codex and normalize times to UTC.
type Session struct {
	Source          string     `json:"source"`
	SessionID       string     `json:"session_id"`
	FirstObservedAt *time.Time `json:"first_observed_at,omitempty"`
	LastObservedAt  *time.Time `json:"last_observed_at,omitempty"`
	SourceVersion   string     `json:"source_version,omitempty"`
}
