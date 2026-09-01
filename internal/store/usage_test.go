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
