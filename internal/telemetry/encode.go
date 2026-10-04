package telemetry

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"
)

// MaxBodyBytes bounds one OTLP request body, well below the receiver's 16 MiB limit.
const MaxBodyBytes = 4 << 20

// Body is one OTLP/HTTP JSON request: Path is "/v1/logs" or "/v1/metrics".
type Body struct {
	Path string
	Data []byte
}

type anyValue struct {
	String *string  `json:"stringValue,omitempty"`
	Int    *string  `json:"intValue,omitempty"`
	Double *float64 `json:"doubleValue,omitempty"`
	Bool   *bool    `json:"boolValue,omitempty"`
}

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

func value(v any) anyValue {
	switch x := v.(type) {
	case string:
		return anyValue{String: &x}
	case bool:
		return anyValue{Bool: &x}
	case int:
		s := strconv.Itoa(x)
		return anyValue{Int: &s}
	case int64:
		s := strconv.FormatInt(x, 10)
		return anyValue{Int: &s}
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			x = 0
		}
		return anyValue{Double: &x}
	}
	s := fmt.Sprint(v)
	return anyValue{String: &s}
}

func attributes(maps ...map[string]any) []keyValue {
	merged := map[string]any{}
	for _, m := range maps {
		for k, v := range m {
			merged[k] = v
		}
	}
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]keyValue, 0, len(keys))
	for _, k := range keys {
		out = append(out, keyValue{Key: k, Value: value(merged[k])})
	}
	return out
}

func nanos(t time.Time) string { return strconv.FormatInt(t.UnixNano(), 10) }

// Encode turns sessions into bounded OTLP/HTTP JSON request bodies, logs before metrics.
func Encode(sessions []*Session) ([]Body, error) {
	var out []Body
	for _, s := range sessions {
		logs, err := encodeLogs(s)
		if err != nil {
			return nil, err
		}
		out = append(out, logs...)
	}
	for _, s := range sessions {
		metrics, err := encodeMetrics(s)
		if err != nil {
			return nil, err
		}
		out = append(out, metrics...)
	}
	return out, nil
}

func resource(s *Session) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"attributes": attributes(map[string]any{"service.name": s.Service, "navigara.backfill": true})})
	return b
}

// chunk packs encoded items into bodies of at most MaxBodyBytes.
func chunk(path string, items []json.RawMessage, wrap func(items []json.RawMessage) ([]byte, error)) ([]Body, error) {
	var out []Body
	var batch []json.RawMessage
	size := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		data, err := wrap(batch)
		if err != nil {
			return err
		}
		out = append(out, Body{Path: path, Data: data})
		batch, size = nil, 0
		return nil
	}
	for _, item := range items {
		if size+len(item)+1024 > MaxBodyBytes {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		batch = append(batch, item)
		size += len(item) + 1
	}
	return out, flush()
}

func encodeLogs(s *Session) ([]Body, error) {
	items := make([]json.RawMessage, 0, len(s.Events))
	for i, e := range s.Events {
		t := nanos(e.Time)
		body := map[string]any{"stringValue": eventPrefix(s.Source) + e.Name}
		record := map[string]any{
			"timeUnixNano": t, "observedTimeUnixNano": t, "eventName": eventPrefix(s.Source) + e.Name, "body": body,
			"attributes": attributes(s.Common, e.Attrs, map[string]any{
				"event.name":      e.Name,
				"event.timestamp": e.Time.UTC().Format("2006-01-02T15:04:05.000Z"),
				"event.sequence":  int64(i),
			}),
		}
		b, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	res := resource(s)
	return chunk("/v1/logs", items, func(batch []json.RawMessage) ([]byte, error) {
		return json.Marshal(map[string]any{"resourceLogs": []any{map[string]any{
			"resource":  res,
			"scopeLogs": []any{map[string]any{"scope": map[string]any{"name": "navigara.backfill"}, "logRecords": batch}},
		}}})
	})
}

func eventPrefix(source string) string {
	if source == "codex" {
		return ""
	}
	return "claude_code."
}

func encodeMetrics(s *Session) ([]Body, error) {
	var items []json.RawMessage
	var names []string
	for _, p := range s.Points {
		attrs := map[string]any{"model": p.Model}
		if p.Type != "" {
			attrs[tokenTypeKey(s.Source)] = p.Type
		}
		t := nanos(p.Time)
		dp := map[string]any{"timeUnixNano": t, "startTimeUnixNano": t, "attributes": attributes(s.Common, attrs)}
		if p.Metric == "cost.usage" {
			dp["asDouble"] = p.Value
		} else {
			dp["asInt"] = strconv.FormatInt(int64(math.Round(p.Value)), 10)
		}
		b, err := json.Marshal(dp)
		if err != nil {
			return nil, err
		}
		items = append(items, b)
		names = append(names, metricName(s.Source, p.Metric))
	}
	res := resource(s)
	next := 0
	return chunk("/v1/metrics", items, func(batch []json.RawMessage) ([]byte, error) {
		// Batches arrive in item order, so the matching metric names follow on from the last batch.
		byName := map[string][]json.RawMessage{}
		var order []string
		for _, dp := range batch {
			name := names[next]
			next++
			if _, ok := byName[name]; !ok {
				order = append(order, name)
			}
			byName[name] = append(byName[name], dp)
		}
		metrics := make([]any, 0, len(order))
		for _, name := range order {
			metrics = append(metrics, map[string]any{
				"name": name, "sum": map[string]any{"aggregationTemporality": 1, "isMonotonic": true, "dataPoints": byName[name]},
			})
		}
		return json.Marshal(map[string]any{"resourceMetrics": []any{map[string]any{
			"resource":     res,
			"scopeMetrics": []any{map[string]any{"scope": map[string]any{"name": "navigara.backfill"}, "metrics": metrics}},
		}}})
	})
}

func metricName(source, metric string) string {
	if source == "codex" {
		if metric == "cost.usage" {
			return "codex.cost.usage"
		}
		return "codex.turn.token_usage"
	}
	return "claude_code." + metric
}

func tokenTypeKey(source string) string {
	if source == "codex" {
		return "token_type"
	}
	return "type"
}
