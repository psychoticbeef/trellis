package store_test

import (
	"database/sql"
	"math"
	"path/filepath"
	"sync"
	"testing"

	"trellis/internal/store"
)

// TestUsageAccumulation_UT_38 proves UT-38: additive persistence survives
// reopen and atomic increments do not lose concurrent reports.
// TestCategorizedUsagePersistence_UT_41 proves UT-41: categorized and legacy
// counters accumulate independently, survive reopen, and reject overflow atomically.
func TestCategorizedUsagePersistence_UT_41(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trellis.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddStoryUsage("p1", "US-1", 100, 50); err != nil {
		t.Fatal(err)
	}
	main := store.TokenCategories{Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40}
	sub := store.TokenCategories{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4}
	if err := st.AddCategorizedStoryUsage("p1", "US-1", main, sub); err != nil {
		t.Fatal(err)
	}
	const categorizedReports = 20
	var categorizedWG sync.WaitGroup
	for range categorizedReports {
		categorizedWG.Add(1)
		go func() {
			defer categorizedWG.Done()
			if err := st.AddCategorizedStoryUsage("p1", "US-1", main, sub); err != nil {
				t.Errorf("concurrent categorized add: %v", err)
			}
		}()
	}
	categorizedWG.Wait()
	if err := st.AddCategorizedStoryUsage("p1", "US-zero", store.TokenCategories{}, store.TokenCategories{}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, ok, err := st.GetStoryUsage("p1", "US-1")
	if err != nil || !ok {
		t.Fatalf("GetStoryUsage: %+v ok=%v err=%v", got, ok, err)
	}
	multiplier := int64(categorizedReports + 1)
	if got.TokensMain != 100 || got.TokensSubagents != 50 ||
		got.Main != (store.TokenCategories{Input: 10 * multiplier, Output: 20 * multiplier, CacheRead: 30 * multiplier, CacheWrite: 40 * multiplier}) ||
		got.Subagents != (store.TokenCategories{Input: multiplier, Output: 2 * multiplier, CacheRead: 3 * multiplier, CacheWrite: 4 * multiplier}) {
		t.Fatalf("reopened categorized usage = %+v", got)
	}
	zero, ok, err := st.GetStoryUsage("p1", "US-zero")
	if err != nil || !ok || !zero.Categorized || zero.Main != (store.TokenCategories{}) || zero.Subagents != (store.TokenCategories{}) {
		t.Fatalf("zero categorized report = %+v ok=%v err=%v", zero, ok, err)
	}

	max := store.TokenCategories{Input: math.MaxInt64}
	if err := st.AddCategorizedStoryUsage("p1", "US-2", max, store.TokenCategories{}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddCategorizedStoryUsage("p1", "US-2", store.TokenCategories{Input: 1, Output: 7}, store.TokenCategories{}); err == nil {
		t.Fatal("overflowing categorized add succeeded")
	}
	got, ok, err = st.GetStoryUsage("p1", "US-2")
	if err != nil || !ok || got.Main.Input != math.MaxInt64 || got.Main.Output != 0 {
		t.Fatalf("overflow changed categorized usage: %+v ok=%v err=%v", got, ok, err)
	}

	legacyPath := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE story_usage (
		project_id TEXT NOT NULL, story_id TEXT NOT NULL,
		tokens_main INTEGER NOT NULL, tokens_subagents INTEGER NOT NULL,
		PRIMARY KEY (project_id, story_id));
		INSERT INTO story_usage VALUES ('p1', 'US-37', 105000, 5800000)`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	migrated, err := store.Open(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	got, ok, err = migrated.GetStoryUsage("p1", "US-37")
	if err != nil || !ok || got.TokensMain != 105000 || got.TokensSubagents != 5800000 || got.Categorized {
		t.Fatalf("legacy row reinterpreted by migration: %+v ok=%v err=%v", got, ok, err)
	}
}

// TestOverflowEnumeration_UT_43 proves UT-43: overflow errors enumerate every
// affected counter in schema order and rejected additions remain atomic.
func TestOverflowEnumeration_UT_43(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "trellis.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	allCategories := func(value int64) store.TokenCategories {
		return store.TokenCategories{Input: value, Output: value, CacheRead: value, CacheWrite: value}
	}

	if err := st.AddStoryUsage("p1", "US-single", math.MaxInt64, 0); err != nil {
		t.Fatal(err)
	}
	beforeSingle, _, err := st.GetStoryUsage("p1", "US-single")
	if err != nil {
		t.Fatal(err)
	}
	err = st.AddStoryUsage("p1", "US-single", 1, 7)
	if err == nil || err.Error() != "token usage overflow for story US-single: tokens_main" {
		t.Fatalf("single uncategorized overflow error = %v", err)
	}
	afterSingle, _, err := st.GetStoryUsage("p1", "US-single")
	if err != nil || afterSingle != beforeSingle {
		t.Fatalf("single overflow changed usage: before=%+v after=%+v err=%v", beforeSingle, afterSingle, err)
	}

	if err := st.AddCategorizedStoryUsage("p1", "US-single-category", store.TokenCategories{Output: math.MaxInt64}, store.TokenCategories{}); err != nil {
		t.Fatal(err)
	}
	err = st.AddCategorizedStoryUsage("p1", "US-single-category", store.TokenCategories{Input: 9, Output: 1}, store.TokenCategories{})
	if err == nil || err.Error() != "token usage overflow for story US-single-category: tokens_main_output" {
		t.Fatalf("single categorized overflow error = %v", err)
	}

	if err := st.AddStoryUsage("p1", "US-all", math.MaxInt64, math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	if err := st.AddCategorizedStoryUsage("p1", "US-all", allCategories(math.MaxInt64), allCategories(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	beforeAll, _, err := st.GetStoryUsage("p1", "US-all")
	if err != nil {
		t.Fatal(err)
	}
	err = st.AddStoryUsage("p1", "US-all", 1, 1)
	wantUncategorized := "token usage overflow for story US-all: tokens_main, tokens_subagents"
	if err == nil || err.Error() != wantUncategorized {
		t.Fatalf("multi uncategorized overflow error = %v, want %q", err, wantUncategorized)
	}
	err = st.AddCategorizedStoryUsage("p1", "US-all", allCategories(1), allCategories(1))
	wantCategorized := "token usage overflow for story US-all: tokens_main_input, tokens_main_output, tokens_main_cache_read, tokens_main_cache_write, tokens_subagents_input, tokens_subagents_output, tokens_subagents_cache_read, tokens_subagents_cache_write"
	if err == nil || err.Error() != wantCategorized {
		t.Fatalf("multi categorized overflow error = %v, want %q", err, wantCategorized)
	}
	afterAll, _, err := st.GetStoryUsage("p1", "US-all")
	if err != nil || afterAll != beforeAll {
		t.Fatalf("multi overflow changed usage: before=%+v after=%+v err=%v", beforeAll, afterAll, err)
	}

	if err := st.AddStoryUsage("p1", "US-boundary", math.MaxInt64-1, math.MaxInt64-1); err != nil {
		t.Fatal(err)
	}
	if err := st.AddCategorizedStoryUsage("p1", "US-boundary", allCategories(math.MaxInt64-1), allCategories(math.MaxInt64-1)); err != nil {
		t.Fatal(err)
	}
	if err := st.AddStoryUsage("p1", "US-boundary", 1, 1); err != nil {
		t.Fatalf("uncategorized exact-boundary update: %v", err)
	}
	if err := st.AddCategorizedStoryUsage("p1", "US-boundary", allCategories(1), allCategories(1)); err != nil {
		t.Fatalf("categorized exact-boundary update: %v", err)
	}
	boundary, _, err := st.GetStoryUsage("p1", "US-boundary")
	if err != nil || boundary.TokensMain != math.MaxInt64 || boundary.TokensSubagents != math.MaxInt64 ||
		boundary.Main != allCategories(math.MaxInt64) || boundary.Subagents != allCategories(math.MaxInt64) {
		t.Fatalf("exact-boundary usage = %+v err=%v", boundary, err)
	}
}

func TestUsageAccumulation_UT_38(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trellis.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddStoryUsage("p1", "US-1", 100, 20); err != nil {
		t.Fatal(err)
	}
	if err := st.AddStoryUsage("p1", "US-1", 0, 5); err != nil {
		t.Fatal(err)
	}
	st.Close()

	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, ok, err := st.GetStoryUsage("p1", "US-1")
	if err != nil || !ok || got.TokensMain != 100 || got.TokensSubagents != 25 {
		t.Fatalf("reopened usage = %+v, ok=%v, err=%v", got, ok, err)
	}
	if _, ok, err := st.GetStoryUsage("p1", "US-2"); err != nil || ok {
		t.Fatalf("unreported story: ok=%v err=%v", ok, err)
	}

	const reports = 20
	var wg sync.WaitGroup
	for i := 0; i < reports; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := st.AddStoryUsage("p1", "US-1", 3, 2); err != nil {
				t.Errorf("concurrent add: %v", err)
			}
		}()
	}
	wg.Wait()
	got, ok, err = st.GetStoryUsage("p1", "US-1")
	if err != nil || !ok || got.TokensMain != 100+3*reports || got.TokensSubagents != 25+2*reports {
		t.Fatalf("concurrent usage = %+v, ok=%v, err=%v", got, ok, err)
	}

	if err := st.AddStoryUsage("p1", "US-3", math.MaxInt64, math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	if err := st.AddStoryUsage("p1", "US-3", 1, 1); err == nil {
		t.Fatal("overflowing add succeeded")
	}
	got, ok, err = st.GetStoryUsage("p1", "US-3")
	if err != nil || !ok || got.TokensMain != math.MaxInt64 || got.TokensSubagents != math.MaxInt64 {
		t.Fatalf("overflow changed usage: %+v ok=%v err=%v", got, ok, err)
	}
}
