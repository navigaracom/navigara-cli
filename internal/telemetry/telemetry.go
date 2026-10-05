// Package telemetry rebuilds the OpenTelemetry a coding agent would have sent live from its
// local session history: log events and cost/token metrics, keyed by the native session id.
package telemetry

import (
	"sort"
	"time"
)

// Event is one OTLP log record. Attrs hold string, int64, float64 or bool values.
type Event struct {
	Name  string
	Time  time.Time
	Attrs map[string]any
}

// Point is one delta datapoint of a usage metric.
type Point struct {
	// Metric is "cost.usage" or "token.usage".
	Metric string
	Time   time.Time
	Model  string
	// Type is the token type of a token.usage point.
	Type  string
	Value float64
}

// Session is everything one native session reports.
type Session struct {
	Source    string
	SessionID string
	// Service is the OTLP service.name the tool reports.
	Service string
	// Common attributes ride on every log record and datapoint.
	Common map[string]any
	Events []Event
	Points []Point
	Start  time.Time
	End    time.Time
	// Estimated is true when cost comes from the price table instead of the tool's own total.
	Estimated bool
	// Missing counts records the builder could not map, by reason.
	Missing map[string]int

	seen *seen
	// lastSettled is the end of the last process run a cost-state row settled.
	lastSettled time.Time
}

// seen dedupes records across a session's transcripts: subagent and fork transcripts repeat
// the parent's rows, requests and tool calls.
type seen struct {
	rows, requests, tools, results map[string]bool
}

func newSeen() *seen {
	return &seen{rows: map[string]bool{}, requests: map[string]bool{}, tools: map[string]bool{}, results: map[string]bool{}}
}

func (s *Session) event(name string, at time.Time, attrs map[string]any) {
	s.Events = append(s.Events, Event{Name: name, Time: at, Attrs: attrs})
	s.span(at)
}

func (s *Session) span(at time.Time) {
	if at.IsZero() {
		return
	}
	if s.Start.IsZero() || at.Before(s.Start) {
		s.Start = at
	}
	if at.After(s.End) {
		s.End = at
	}
}

func (s *Session) miss(reason string) {
	if s.Missing == nil {
		s.Missing = map[string]int{}
	}
	s.Missing[reason]++
}

// sortRecords orders events and points by time; ties keep their build order.
func (s *Session) sortRecords() {
	sort.SliceStable(s.Events, func(i, j int) bool { return s.Events[i].Time.Before(s.Events[j].Time) })
	sort.SliceStable(s.Points, func(i, j int) bool { return s.Points[i].Time.Before(s.Points[j].Time) })
}

// Within keeps only the events and points in [since, until]; a zero bound is open.
func (s *Session) Within(since, until time.Time) {
	in := func(t time.Time) bool {
		return (since.IsZero() || !t.Before(since)) && (until.IsZero() || !t.After(until))
	}
	events := s.Events[:0]
	for _, e := range s.Events {
		if in(e.Time) {
			events = append(events, e)
		}
	}
	s.Events = events
	points := s.Points[:0]
	for _, p := range s.Points {
		if in(p.Time) {
			points = append(points, p)
		}
	}
	s.Points = points
}

// mergePoints sums points of the same metric, model, type and time: the receiver keeps only one.
func (s *Session) mergePoints() {
	type key struct {
		metric, model, kind string
		at                  int64
	}
	index := map[key]int{}
	merged := s.Points[:0]
	for _, p := range s.Points {
		k := key{p.Metric, p.Model, p.Type, p.Time.UnixNano()}
		if i, ok := index[k]; ok {
			merged[i].Value += p.Value
			continue
		}
		index[k] = len(merged)
		merged = append(merged, p)
	}
	s.Points = merged
}
