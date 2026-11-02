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
