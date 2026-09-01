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
	if err := e.DeleteNode(story.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.GetStoryUsage("p1", story.ID); err != nil || ok {
		t.Fatalf("deleted story retained usage: ok=%v err=%v", ok, err)
	}
}

// TestOverflowRejectionIntegration_IT_40 proves IT-40: engine propagation keeps
// categorized counters and event sequence unchanged after exhaustive rejection.
func TestOverflowRejectionIntegration_IT_40(t *testing.T) {
	e, st := newEngineStore(t)
	story := mustCreate(t, e, model.KindStory, "", "overflow story", nil)
	maxMain := core.TokenCategories{Input: math.MaxInt64, CacheRead: math.MaxInt64}
	maxSubagents := core.TokenCategories{Output: math.MaxInt64}
	if err := e.AddCategorizedUsage(story.ID, maxMain, maxSubagents); err != nil {
		t.Fatal(err)
	}
	before, ok, err := st.GetStoryUsage("p1", story.ID)
	if err != nil || !ok {
		t.Fatalf("usage before overflow: %+v ok=%v err=%v", before, ok, err)
	}
	seqBefore, err := st.MaxEventSeq("p1")
	if err != nil {
		t.Fatal(err)
	}
	err = e.AddCategorizedUsage(story.ID,
		core.TokenCategories{Input: 1, Output: 9, CacheRead: 2},
		core.TokenCategories{Output: 3})
	want := "token usage overflow for story " + story.ID + ": tokens_main_input, tokens_main_cache_read, tokens_subagents_output"
	if err == nil || err.Error() != want {
		t.Fatalf("overflow error = %v, want %q", err, want)
	}
	after, ok, err := st.GetStoryUsage("p1", story.ID)
	if err != nil || !ok || after != before {
		t.Fatalf("overflow changed usage: before=%+v after=%+v ok=%v err=%v", before, after, ok, err)
	}
	seqAfter, err := st.MaxEventSeq("p1")
	if err != nil || seqAfter != seqBefore {
		t.Fatalf("overflow changed event sequence: before=%d after=%d err=%v", seqBefore, seqAfter, err)
	}
}

// TestUsageOverview_UT_39 proves UT-39: exact optional counters and compact
// floor-to-k formatting appear only after first report.
// TestCategorizedUsageFormatting_UT_42 proves UT-42: overview exposes exact
// category counters and shared compact formatting preserves legacy-only output.
func TestCategorizedUsageFormatting_UT_42(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1k", 1999: "1k"} {
		if got := core.FormatTokenCount(in); got != want {
			t.Errorf("FormatTokenCount(%d) = %q, want %q", in, got, want)
		}
	}
	e := newEngine(t)
	categorized := mustCreate(t, e, model.KindStory, "", "categorized", nil)
	legacy := mustCreate(t, e, model.KindStory, "", "legacy", nil)
	zero := mustCreate(t, e, model.KindStory, "", "categorized zero", nil)
	if err := e.AddUsage(categorized.ID, 100, 50); err != nil {
		t.Fatal(err)
	}
	if err := e.AddCategorizedUsage(categorized.ID,
		core.TokenCategories{Input: 1000, Output: 200, CacheRead: 3000, CacheWrite: 400},
		core.TokenCategories{Input: 500, Output: 300, CacheRead: 2000, CacheWrite: 100}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddUsage(legacy.ID, 1999, 999); err != nil {
		t.Fatal(err)
	}
	if err := e.AddCategorizedUsage(zero.ID, core.TokenCategories{}, core.TokenCategories{}); err != nil {
		t.Fatal(err)
	}
	o, err := e.Overview()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]core.StorySummary{}
	for _, summary := range o.Stories {
		byID[summary.ID] = summary
	}
	got := byID[categorized.ID]
	if got.Usage != "7k (2k sub) · out 500 · cache 5k/500 r/w" ||
		got.TokensMainInput == nil || *got.TokensMainInput != 1000 ||
		got.TokensMainOutput == nil || *got.TokensMainOutput != 200 ||
		got.TokensMainCacheRead == nil || *got.TokensMainCacheRead != 3000 ||
		got.TokensMainCacheWrite == nil || *got.TokensMainCacheWrite != 400 ||
		got.TokensSubagentsInput == nil || *got.TokensSubagentsInput != 500 ||
		got.TokensSubagentsOutput == nil || *got.TokensSubagentsOutput != 300 ||
		got.TokensSubagentsCacheRead == nil || *got.TokensSubagentsCacheRead != 2000 ||
		got.TokensSubagentsCacheWrite == nil || *got.TokensSubagentsCacheWrite != 100 {
		t.Fatalf("categorized summary = %+v", got)
	}
	legacySummary := byID[legacy.ID]
	if legacySummary.Usage != "2k (999 sub)" || legacySummary.TokensMainInput != nil {
		t.Fatalf("legacy summary changed = %+v", legacySummary)
	}
	zeroSummary := byID[zero.ID]
	if zeroSummary.Usage != "0 (0 sub) · out 0 · cache 0/0 r/w" || zeroSummary.TokensMainInput == nil || *zeroSummary.TokensMainInput != 0 {
		t.Fatalf("zero categorized summary = %+v", zeroSummary)
	}
}

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
