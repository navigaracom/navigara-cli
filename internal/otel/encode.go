// Package otel encodes the supported local-session usage facts as OTLP/HTTP JSON.
package otel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

const maxBodyBytes = 4 << 20

// Encoder batches points into complete OTLP/HTTP JSON request bodies.
type Encoder struct {
	emit   func([]byte) error
	source string
	body   []byte
	failed error
}

// NewEncoder creates an encoder which calls emit synchronously for every body.
// The bytes passed to emit are valid only until emit returns.
func NewEncoder(emit func([]byte) error) *Encoder { return &Encoder{emit: emit} }

// Add encodes points in order, flushing on source transitions and the fixed body limit.
func (e *Encoder) Add(points []Point) error {
	if e.failed != nil {
		return e.failed
	}
	for _, point := range points {
		encoded, source, err := encodePoint(point)
		if err != nil {
			return e.fail(err)
		}
		if len(prefix(source))+len(encoded)+len(suffix(source)) > maxBodyBytes {
			return e.fail(fmt.Errorf("OTLP datapoint exceeds %d-byte body limit", maxBodyBytes))
		}
		if e.source != "" && e.source != source {
			if err := e.flush(); err != nil {
				return err
			}
		}
		if e.source == "" {
			e.source = source
			e.body = append(e.body[:0], prefix(source)...)
		}
		separator := 0
		if !bytes.HasSuffix(e.body, []byte("[")) {
			separator = 1
		}
		if len(e.body)+separator+len(encoded)+len(suffix(source)) > maxBodyBytes {
			if err := e.flush(); err != nil {
				return err
			}
			e.source = source
			e.body = append(e.body[:0], prefix(source)...)
		}
		if !bytes.HasSuffix(e.body, []byte("[")) {
			e.body = append(e.body, ',')
		}
		e.body = append(e.body, encoded...)
	}
	return nil
}

// Flush emits the pending complete OTLP body. It does nothing when no point is pending.
func (e *Encoder) Flush() error {
	if e.failed != nil {
		return e.failed
	}
	return e.flush()
}

func (e *Encoder) flush() error {
	if e.source == "" {
		return nil
	}
	e.body = append(e.body, suffix(e.source)...)
	if err := e.emit(e.body); err != nil {
		return e.fail(err)
	}
	e.source = ""
	e.body = e.body[:0]
	return nil
}

func (e *Encoder) fail(err error) error {
	e.failed = err
	return err
}

func encodePoint(point Point) ([]byte, string, error) {
	source := point.Source
	if source != "claude_code" && source != "codex" {
		return nil, "", fmt.Errorf("unsupported OTLP source %q", source)
	}
	if point.Value < 0 || point.Value > maxJSONInteger {
		return nil, "", fmt.Errorf("OTLP datapoint value must be between 0 and %d", maxJSONInteger)
	}
	nanos, ok := timestampNanos(point.Time)
	if !ok {
		return nil, "", errors.New("OTLP datapoint time must fit unsigned nanoseconds")
	}
	attrs := []attribute{
		stringAttribute("session.id", point.SessionID),
		stringAttribute("model", point.Model),
		stringAttribute(tokenAttribute(source), point.TokenType),
	}
	if point.RepositoryURL != "" {
		attrs = append(attrs, stringAttribute("vcs.repository.url.full", point.RepositoryURL))
	}
	if source == "claude_code" {
		body, err := json.Marshal(claudeDatapoint{Attributes: attrs, Time: strconv.FormatUint(nanos, 10), AsInt: strconv.FormatInt(point.Value, 10)})
		return body, source, err
	}
	body, err := json.Marshal(codexDatapoint{Attributes: attrs, Time: strconv.FormatUint(nanos, 10), Count: "1", Sum: point.Value, Buckets: []string{"1"}})
	return body, source, err
}

func tokenAttribute(source string) string {
	if source == "codex" {
		return "token_type"
	}
	return "type"
}

func prefix(source string) string {
	if source == "claude_code" {
		return `{"resourceMetrics":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"claude-code"}}]},"scopeMetrics":[{"metrics":[{"name":"claude_code.token.usage","sum":{"dataPoints":[`
	}
	return `{"resourceMetrics":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"codex"}}]},"scopeMetrics":[{"metrics":[{"name":"codex.turn.token_usage","histogram":{"dataPoints":[`
}

func suffix(source string) string {
	if source == "claude_code" {
		return `],"aggregationTemporality":1,"isMonotonic":true}}]}]}]}`
	}
	return `],"aggregationTemporality":1}}]}]}]}`
}

type attribute struct {
	Key   string `json:"key"`
	Value struct {
		String string `json:"stringValue"`
	} `json:"value"`
}

func stringAttribute(key, value string) attribute {
	var out attribute
	out.Key, out.Value.String = key, value
	return out
}

type claudeDatapoint struct {
	Attributes []attribute `json:"attributes"`
	Time       string      `json:"timeUnixNano"`
	AsInt      string      `json:"asInt"`
}

type codexDatapoint struct {
	Attributes []attribute `json:"attributes"`
	Time       string      `json:"timeUnixNano"`
	Count      string      `json:"count"`
	Sum        int64       `json:"sum"`
	Buckets    []string    `json:"bucketCounts"`
}
