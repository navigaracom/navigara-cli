package telemetry

import (
	"encoding/json"
	"testing"
	"time"
)

func TestResourceCarriesTheExportWindow(t *testing.T) {
	s := &Session{Service: "claude-code"}
	s.Within(time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC), time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC))
	var got struct {
		Attributes []struct {
			Key   string
			Value map[string]any
		}
	}
	if err := json.Unmarshal(resource(s), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"navigara.backfill.since": "2026-09-01T08:00:00Z", "navigara.backfill.until": "2026-09-03T00:00:00Z"}
	for _, a := range got.Attributes {
		if w, ok := want[a.Key]; ok {
			if a.Value["stringValue"] != w {
				t.Errorf("%s = %v, want %s", a.Key, a.Value, w)
			}
			delete(want, a.Key)
		}
	}
	if len(want) > 0 {
		t.Errorf("missing %v", want)
	}
}

func TestOpenEndCoversAPointAfterTheLastEvent(t *testing.T) {
	s := &Session{Service: "codex"}
	s.event("codex.user_prompt", time.Date(2026, 9, 1, 23, 58, 0, 0, time.UTC), nil)
	s.Points = append(s.Points, Point{Metric: "token.usage", Time: time.Date(2026, 9, 2, 0, 1, 0, 0, time.UTC)})
	var got struct {
		Attributes []struct {
			Key   string
			Value map[string]any
		}
	}
	if err := json.Unmarshal(resource(s), &got); err != nil {
		t.Fatal(err)
	}
	for _, a := range got.Attributes {
		if a.Key == "navigara.backfill.until" && a.Value["stringValue"] != "2026-09-03T00:00:00Z" {
			t.Errorf("until = %v, want the end of the point's day", a.Value)
		}
	}
}
