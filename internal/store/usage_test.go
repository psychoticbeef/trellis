package store_test

import (
	"math"
	"path/filepath"
	"sync"
	"testing"

	"trellis/internal/store"
)

// TestUsageAccumulation_UT_38 proves UT-38: additive persistence survives
// reopen and atomic increments do not lose concurrent reports.
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
