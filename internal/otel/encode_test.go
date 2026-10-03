package otel

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

var encodeTime = time.Date(2026, 10, 3, 8, 0, 0, 123, time.UTC)

func TestEncoderClaudeJSON(t *testing.T) {
	bodies := encode(t, []Point{{
		Source: "claude_code", SessionID: "claude-session", Model: "claude-opus-4-8", TokenType: "input",
		RepositoryURL: "https://github.com/acme/project", Time: encodeTime, Value: 42,
	}})
	var got export
	if err := json.Unmarshal(bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	metric := got.ResourceMetrics[0].ScopeMetrics[0].Metrics[0]
	if service(got) != "claude-code" || metric.Name != "claude_code.token.usage" || metric.Sum == nil || metric.Histogram != nil {
		t.Fatalf("unexpected metric: %+v", got)
	}
	if metric.Sum.AggregationTemporality != 1 || !metric.Sum.IsMonotonic || len(metric.Sum.DataPoints) != 1 {
		t.Fatalf("unexpected sum: %+v", metric.Sum)
	}
	dp := metric.Sum.DataPoints[0]
	if dp.Time != "1791014400000000123" || dp.AsInt != "42" || attrs(dp.Attributes)["type"] != "input" || attrs(dp.Attributes)["vcs.repository.url.full"] == "" {
		t.Fatalf("unexpected datapoint: %+v", dp)
	}
}

func TestEncoderCodexJSON(t *testing.T) {
	bodies := encode(t, []Point{{Source: "codex", SessionID: "codex-session", Model: "gpt-5", TokenType: "output", Time: encodeTime, Value: 7}})
	var got export
	if err := json.Unmarshal(bodies[0], &got); err != nil {
		t.Fatal(err)
	}
	metric := got.ResourceMetrics[0].ScopeMetrics[0].Metrics[0]
	if service(got) != "codex" || metric.Name != "codex.turn.token_usage" || metric.Sum != nil || metric.Histogram == nil {
		t.Fatalf("unexpected metric: %+v", got)
	}
	if metric.Histogram.AggregationTemporality != 1 || len(metric.Histogram.DataPoints) != 1 {
		t.Fatalf("unexpected histogram: %+v", metric.Histogram)
	}
	dp := metric.Histogram.DataPoints[0]
	if dp.Time != "1791014400000000123" || dp.Count != "1" || dp.Sum != 7 || len(dp.Buckets) != 1 || dp.Buckets[0] != "1" || attrs(dp.Attributes)["token_type"] != "output" {
		t.Fatalf("unexpected datapoint: %+v", dp)
	}
}

func TestEncoderEmptyFlushAndSourceChange(t *testing.T) {
	var bodies [][]byte
	e := NewEncoder(func(body []byte) error { bodies = append(bodies, append([]byte(nil), body...)); return nil })
	if err := e.Flush(); err != nil || len(bodies) != 0 {
		t.Fatalf("empty flush: err=%v bodies=%d", err, len(bodies))
	}
	if err := e.Add([]Point{
		{Source: "claude_code", SessionID: "a", Model: "m", TokenType: "input", Time: encodeTime, Value: 1},
		{Source: "codex", SessionID: "b", Model: "m", TokenType: "input", Time: encodeTime, Value: 1},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.Flush(); err != nil || len(bodies) != 2 {
		t.Fatalf("source flush: err=%v bodies=%d", err, len(bodies))
	}
}

func TestEncoderSplitsAtActualFourMiBLimitDeterministically(t *testing.T) {
	points := make([]Point, 5)
	for i := range points {
		points[i] = Point{Source: "claude_code", SessionID: "s", Model: "m", TokenType: "input", RepositoryURL: strings.Repeat(string([]byte{byte('a' + i)}), 1<<20), Time: encodeTime, Value: int64(i)}
	}
	first, second := encode(t, points), encode(t, points)
	if len(first) < 2 || len(first) != len(second) {
		t.Fatalf("batches=%d, second=%d", len(first), len(second))
	}
	var values []string
	for i, body := range first {
		if len(body) > maxBodyBytes || !json.Valid(body) || !bytes.Equal(body, second[i]) {
			t.Fatalf("body %d len=%d valid=%t deterministic=%t", i, len(body), json.Valid(body), bytes.Equal(body, second[i]))
		}
		var got export
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		for _, point := range got.ResourceMetrics[0].ScopeMetrics[0].Metrics[0].Sum.DataPoints {
			values = append(values, point.AsInt)
		}
	}
	for i, value := range values {
		if value != strconv.Itoa(i) {
			t.Fatalf("values=%v", values)
		}
	}
	if len(values) != len(points) {
		t.Fatalf("points=%d want=%d", len(values), len(points))
	}
}

func TestEncoderRejectsInvalidOrOversizedPoint(t *testing.T) {
	valid := Point{Source: "claude_code", SessionID: "s", Model: "m", TokenType: "input", Time: encodeTime, Value: 1}
	for _, point := range []Point{
		{Source: "other", Time: encodeTime},
		{Source: "codex", Time: time.Time{}},
		{Source: "codex", Time: time.Unix(0, 0)},
		{Source: "codex", Time: time.Unix(-1, 0)},
		{Source: "codex", Time: encodeTime, Value: -1},
		{Source: "codex", Time: encodeTime, Value: maxJSONInteger + 1},
		{Source: valid.Source, SessionID: valid.SessionID, Model: valid.Model, TokenType: valid.TokenType, Time: valid.Time, Value: valid.Value, RepositoryURL: strings.Repeat("x", maxBodyBytes)},
	} {
		e := NewEncoder(func([]byte) error { return nil })
		if err := e.Add([]Point{point}); err == nil {
			t.Fatalf("point accepted: %+v", point)
		}
		if err := e.Add([]Point{valid}); err == nil {
			t.Fatal("encoder continued after error")
		}
	}
}

func TestEncoderRejectsOversizedPointAfterPendingPoint(t *testing.T) {
	called := 0
	e := NewEncoder(func([]byte) error { called++; return nil })
	valid := Point{Source: "claude_code", SessionID: "s", Model: "m", TokenType: "input", Time: encodeTime, Value: 1}
	oversized := valid
	oversized.RepositoryURL = strings.Repeat("x", maxBodyBytes)
	if err := e.Add([]Point{valid, oversized}); err == nil || called != 0 {
		t.Fatalf("err=%v calls=%d", err, called)
	}
}

func TestEncoderStopsAfterEmitFailure(t *testing.T) {
	want := errors.New("emit failed")
	calls := 0
	e := NewEncoder(func([]byte) error { calls++; return want })
	p := Point{Source: "claude_code", SessionID: "s", Model: "m", TokenType: "input", Time: encodeTime, Value: 1}
	if err := e.Add([]Point{p}); err != nil {
		t.Fatal(err)
	}
	if err := e.Flush(); !errors.Is(err, want) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if err := e.Add([]Point{p}); !errors.Is(err, want) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func encode(t *testing.T, points []Point) [][]byte {
	t.Helper()
	var out [][]byte
	e := NewEncoder(func(body []byte) error {
		out = append(out, append([]byte(nil), body...))
		return nil
	})
	if err := e.Add(points); err != nil {
		t.Fatal(err)
	}
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	return out
}

type export struct {
	ResourceMetrics []struct {
		Resource struct {
			Attributes []kv `json:"attributes"`
		} `json:"resource"`
		ScopeMetrics []struct {
			Metrics []struct {
				Name      string     `json:"name"`
				Sum       *sum       `json:"sum"`
				Histogram *histogram `json:"histogram"`
			} `json:"metrics"`
		} `json:"scopeMetrics"`
	} `json:"resourceMetrics"`
}

type kv struct {
	Key   string `json:"key"`
	Value struct {
		String string `json:"stringValue"`
	} `json:"value"`
}
type sum struct {
	DataPoints             []number `json:"dataPoints"`
	AggregationTemporality int      `json:"aggregationTemporality"`
	IsMonotonic            bool     `json:"isMonotonic"`
}
type number struct {
	Attributes []kv   `json:"attributes"`
	Time       string `json:"timeUnixNano"`
	AsInt      string `json:"asInt"`
}
type histogram struct {
	DataPoints             []histogramPoint `json:"dataPoints"`
	AggregationTemporality int              `json:"aggregationTemporality"`
}
type histogramPoint struct {
	Attributes []kv     `json:"attributes"`
	Time       string   `json:"timeUnixNano"`
	Count      string   `json:"count"`
	Sum        int64    `json:"sum"`
	Buckets    []string `json:"bucketCounts"`
}

func service(got export) string {
	return attrs(got.ResourceMetrics[0].Resource.Attributes)["service.name"]
}
func attrs(values []kv) map[string]string {
	out := make(map[string]string, len(values))
	for _, value := range values {
		out[value.Key] = value.Value.String
	}
	return out
}
