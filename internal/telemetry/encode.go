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

// Body is one OTLP/HTTP JSON request: Path is "/v1/logs", "/v1/traces" or "/v1/metrics".
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

// Encode turns a session into bounded OTLP/HTTP JSON request bodies: logs, traces, then metrics. It
// hands each body to emit as soon as the body is full, so memory holds one body at a time.
func Encode(s *Session, emit func(Body) error) error {
	if err := encodeLogs(s, emit); err != nil {
		return err
	}
	if err := encodeTraces(s, emit); err != nil {
		return err
	}
	return encodeMetrics(s, emit)
}

func resource(s *Session) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"attributes": attributes(map[string]any{"service.name": s.Service, "navigara.backfill": true})})
	return b
}

// chunk encodes items 0..n-1 in order and packs them into bodies of at most MaxBodyBytes.
// wrap receives the index of a batch's first item.
func chunk(path string, n int, item func(i int) ([]byte, error), wrap func(first int, batch []json.RawMessage) ([]byte, error), emit func(Body) error) error {
	var batch []json.RawMessage
	size, first := 0, 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		data, err := wrap(first, batch)
		if err != nil {
			return err
		}
		first += len(batch)
		batch, size = nil, 0
		return emit(Body{Path: path, Data: data})
	}
	for i := range n {
		b, err := item(i)
		if err != nil {
			return err
		}
		if size+len(b)+1024 > MaxBodyBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, b)
		size += len(b) + 1
	}
	return flush()
}

func encodeLogs(s *Session, emit func(Body) error) error {
	res := resource(s)
	return chunk("/v1/logs", len(s.Events), func(i int) ([]byte, error) {
		e := s.Events[i]
		t := nanos(e.Time)
		body := map[string]any{"stringValue": eventPrefix(s.Source) + e.Name}
		record := map[string]any{"event.name": e.Name}
		// Copilot sends neither live.
		if s.Source != "copilot" {
			record["event.timestamp"] = e.Time.UTC().Format("2006-01-02T15:04:05.000Z")
			record["event.sequence"] = int64(i)
		}
		return json.Marshal(map[string]any{
			"timeUnixNano": t, "observedTimeUnixNano": t, "eventName": eventPrefix(s.Source) + e.Name, "body": body,
			"attributes": attributes(s.Common, e.Attrs, record),
		})
	}, func(_ int, batch []json.RawMessage) ([]byte, error) {
		return json.Marshal(map[string]any{"resourceLogs": []any{map[string]any{
			"resource":  res,
			"scopeLogs": []any{map[string]any{"scope": map[string]any{"name": "navigara.backfill"}, "logRecords": batch}},
		}}})
	}, emit)
}

func eventPrefix(source string) string {
	if source == "claude_code" {
		return "claude_code."
	}
	return ""
}

// spanKindClient is the kind of a call to a model, as Copilot sends it.
const spanKindClient = 3

func encodeTraces(s *Session, emit func(Body) error) error {
	res := resource(s)
	return chunk("/v1/traces", len(s.Spans), func(i int) ([]byte, error) {
		sp := s.Spans[i]
		return json.Marshal(map[string]any{
			"traceId": sp.TraceID, "spanId": sp.SpanID, "name": sp.Name, "kind": spanKindClient,
			"startTimeUnixNano": nanos(sp.Start), "endTimeUnixNano": nanos(sp.End),
			"attributes": attributes(s.Common, sp.Attrs),
		})
	}, func(_ int, batch []json.RawMessage) ([]byte, error) {
		return json.Marshal(map[string]any{"resourceSpans": []any{map[string]any{
			"resource":   res,
			"scopeSpans": []any{map[string]any{"scope": map[string]any{"name": "navigara.backfill"}, "spans": batch}},
		}}})
	}, emit)
}

func encodeMetrics(s *Session, emit func(Body) error) error {
	res := resource(s)
	return chunk("/v1/metrics", len(s.Points), func(i int) ([]byte, error) {
		p := s.Points[i]
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
		return json.Marshal(dp)
	}, func(first int, batch []json.RawMessage) ([]byte, error) {
		byName := map[string][]json.RawMessage{}
		var order []string
		for i, dp := range batch {
			name := metricName(s.Source, s.Points[first+i].Metric)
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
	}, emit)
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
