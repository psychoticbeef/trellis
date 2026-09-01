package core_test

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"trellis/internal/core"
	"trellis/internal/model"
)

// TestUsageValidation_UT_38 proves UT-38: core rejects every invalid report
// before changing persisted counters.
func TestUsageValidation_UT_38(t *testing.T) {
	e, st := newEngineStore(t)
	story := mustCreate(t, e, model.KindStory, "", "usage story", nil)
	other := mustCreate(t, e, model.KindCrossCutting, "", "not story", nil)
	if err := e.AddUsage(story.ID, 10, 4); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id        string
		main, sub int64
		want      string
	}{
		{"US-999", 1, 1, "does not exist"},
		{other.ID, 1, 1, "not a story"},
		{story.ID, -1, -2, "tokens_main"},
	} {
		err := e.AddUsage(tc.id, tc.main, tc.sub)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("AddUsage(%q, %d, %d) = %v, want %q", tc.id, tc.main, tc.sub, err, tc.want)
		}
	}
	got, ok, err := st.GetStoryUsage("p1", story.ID)
	if err != nil || !ok || got.TokensMain != 10 || got.TokensSubagents != 4 {
		t.Fatalf("usage changed after rejection: %+v ok=%v err=%v", got, ok, err)
	}
}

// TestUsageOverview_UT_39 proves UT-39: exact optional counters and compact
// floor-to-k formatting appear only after first report.
func TestUsageOverview_UT_39(t *testing.T) {
	e := newEngine(t)
	story := mustCreate(t, e, model.KindStory, "", "usage story", nil)
	other := mustCreate(t, e, model.KindStory, "", "silent story", nil)

	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1k", 1999: "1k"} {
		if got := core.FormatTokenCount(in); got != want {
			t.Errorf("FormatTokenCount(%d) = %q, want %q", in, got, want)
		}
	}
	if got := core.FormatUsage(math.MaxInt64, math.MaxInt64); got != "18446744073709551k (9223372036854775k sub)" {
		t.Fatalf("overflow-safe FormatUsage = %q", got)
	}
	if err := e.AddUsage(story.ID, 120999, 90001); err != nil {
		t.Fatal(err)
	}
	o, err := e.Overview()
	if err != nil {
		t.Fatal(err)
	}
	var found, silent core.StorySummary
	for _, s := range o.Stories {
		if s.ID == story.ID {
			found = s
		}
		if s.ID == other.ID {
			silent = s
		}
	}
	if found.TokensMain == nil || *found.TokensMain != 120999 || found.TokensSubagents == nil || *found.TokensSubagents != 90001 || found.Usage != "211k (90k sub)" {
		t.Fatalf("usage summary = %+v", found)
	}
	blob, _ := json.Marshal(silent)
	if strings.Contains(string(blob), "tokens_") || strings.Contains(string(blob), "usage") {
		t.Fatalf("silent story exposes usage: %s", blob)
	}
}
