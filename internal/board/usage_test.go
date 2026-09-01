package board_test

import (
	"bufio"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"trellis/internal/board"
	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

// TestUsageRendering_UT_39 proves UT-39: board cards and story details use
// overview's compact usage and omit it for stories without reports.
func TestUsageRendering_UT_39(t *testing.T) {
	e, _ := newEngine(t)
	used, _ := e.CreateNode(model.KindStory, "", "used story", "", nil)
	_, _ = e.CreateNode(model.KindStory, "", "silent story", "", nil)
	if err := e.AddUsage(used.ID, 120000, 90000); err != nil {
		t.Fatal(err)
	}
	html, err := board.Render(e)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(html, "210k (90k sub)") != 2 {
		t.Fatalf("usage must appear on card and detail: count=%d", strings.Count(html, "210k (90k sub)"))
	}
	if strings.Contains(strings.ToLower(html), "euro") || strings.Contains(html, "€") {
		t.Fatal("board must not convert tokens to cost")
	}
}

// TestCategorizedUsageBoardFormatting_UT_42 proves UT-42: static and served
// boards consume identical categorized overview formatting on cards and headers.
func TestCategorizedUsageBoardFormatting_UT_42(t *testing.T) {
	e, st := newEngine(t)
	story, _ := e.CreateNode(model.KindStory, "", "categorized <story>", "", nil)
	if err := e.AddCategorizedUsage(story.ID,
		core.TokenCategories{Output: 999, CacheRead: 1000},
		core.TokenCategories{Output: 1000, CacheWrite: 1999}); err != nil {
		t.Fatal(err)
	}
	want := "4k (2k sub) · out 1k · cache 1k/1k r/w"
	static, err := board.Render(e)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(static, want) != 2 || !strings.Contains(static, "categorized &lt;story&gt;") {
		t.Fatalf("static categorized rendering mismatch: count=%d", strings.Count(static, want))
	}
	server := httptest.NewServer(board.Handler(e, st))
	defer server.Close()
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	served, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if strings.Count(string(served), want) != 2 || !strings.Contains(string(served), "categorized &lt;story&gt;") {
		t.Fatalf("served categorized rendering mismatch: count=%d", strings.Count(string(served), want))
	}
}

// TestCategorizedUsageIntegration_IT_39 proves IT-39: concurrent categorized
// reports persist separately from legacy totals and refresh static/live boards.
func TestCategorizedUsageIntegration_IT_39(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trellis.db")
	st1, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st1.CreateProject(store.Project{ID: "p1", Name: "test", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e1, _ := core.NewEngine(st1, "p1")
	story, err := e1.CreateNode(model.KindStory, "", "categorized usage", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := e1.CreateNode(model.KindStory, "", "legacy usage", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e1.AddUsage(story.ID, 100, 200); err != nil {
		t.Fatal(err)
	}
	if err := e1.AddUsage(legacy.ID, 120000, 90000); err != nil {
		t.Fatal(err)
	}
	before, _ := st1.MaxEventSeq("p1")

	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e2, _ := core.NewEngine(st2, "p1")
	main := core.TokenCategories{Input: 100, Output: 20, CacheRead: 1000, CacheWrite: 10}
	sub := core.TokenCategories{Input: 50, Output: 30, CacheRead: 2000, CacheWrite: 20}
	var wg sync.WaitGroup
	for _, engine := range []*core.Engine{e1, e2, e1, e2} {
		wg.Add(1)
		go func(engine *core.Engine) {
			defer wg.Done()
			if err := engine.AddCategorizedUsage(story.ID, main, sub); err != nil {
				t.Errorf("AddCategorizedUsage: %v", err)
			}
		}(engine)
	}
	wg.Wait()
	st2.Close()
	after, _ := st1.MaxEventSeq("p1")
	if after <= before {
		t.Fatalf("event sequence did not grow: %d -> %d", before, after)
	}
	st1.Close()

	st3, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st3.Close()
	e3, _ := core.NewEngine(st3, "p1")
	persisted, ok, err := st3.GetStoryUsage("p1", story.ID)
	if err != nil || !ok || persisted.TokensMain != 100 || persisted.TokensSubagents != 200 ||
		persisted.Main != (store.TokenCategories{Input: 400, Output: 80, CacheRead: 4000, CacheWrite: 40}) ||
		persisted.Subagents != (store.TokenCategories{Input: 200, Output: 120, CacheRead: 8000, CacheWrite: 80}) || !persisted.Categorized {
		t.Fatalf("exact categorized counters after reopen = %+v ok=%v err=%v", persisted, ok, err)
	}
	o, err := e3.Overview()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]core.StorySummary{}
	for _, summary := range o.Stories {
		byID[summary.ID] = summary
	}
	categorizedSummary := byID[story.ID]
	if categorizedSummary.Usage != "13k (8k sub) · out 200 · cache 12k/120 r/w" ||
		categorizedSummary.TokensMainCacheRead == nil || *categorizedSummary.TokensMainCacheRead != 4000 ||
		categorizedSummary.TokensSubagentsCacheRead == nil || *categorizedSummary.TokensSubagentsCacheRead != 8000 {
		t.Fatalf("categorized overview after reopen = %+v", categorizedSummary)
	}
	legacySummary := byID[legacy.ID]
	if legacySummary.Usage != "210k (90k sub)" || legacySummary.TokensMainInput != nil {
		t.Fatalf("legacy-only overview changed = %+v", legacySummary)
	}
	html, err := board.Render(e3)
	if err != nil || !strings.Contains(html, "13k (8k sub) · out 200 · cache 12k/120 r/w") {
		t.Fatalf("categorized board after reopen: err=%v", err)
	}

	server := httptest.NewServer(board.Handler(e3, st3))
	defer server.Close()
	events, err := server.Client().Get(server.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer events.Body.Close()
	reader := bufio.NewReader(events.Body)
	if line, err := reader.ReadString('\n'); err != nil || !strings.Contains(line, "connected") {
		t.Fatalf("SSE greeting = %q, err=%v", line, err)
	}
	_, _ = reader.ReadString('\n')
	reload := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		reload <- line
	}()
	if err := e3.AddCategorizedUsage(story.ID, core.TokenCategories{Output: 1000}, core.TokenCategories{}); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-reload:
		if !strings.Contains(line, "data: reload") {
			t.Fatalf("SSE update = %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSE did not report categorized usage update")
	}
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	served, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(served), "14k (8k sub) · out 1k · cache 12k/120 r/w") || !strings.Contains(string(served), "new EventSource") {
		t.Fatalf("served board did not refresh categorized usage: %.500s", served)
	}
}

// TestUsageReportingIntegration_IT_37 proves IT-37: repeated concurrent engine
// reports persist, feed overview and board, and advance live-board event state.
func TestUsageReportingIntegration_IT_37(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trellis.db")
	st1, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st1.Close()
	if err := st1.CreateProject(store.Project{ID: "p1", Name: "test", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e1, _ := core.NewEngine(st1, "p1")
	story, err := e1.CreateNode(model.KindStory, "", "usage story", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := st1.MaxEventSeq("p1")

	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e2, _ := core.NewEngine(st2, "p1")
	var wg sync.WaitGroup
	for _, e := range []*core.Engine{e1, e2, e1, e2} {
		wg.Add(1)
		go func(engine *core.Engine) {
			defer wg.Done()
			if err := engine.AddUsage(story.ID, 30000, 22500); err != nil {
				t.Errorf("AddUsage: %v", err)
			}
		}(e)
	}
	wg.Wait()
	st2.Close()
	after, _ := st1.MaxEventSeq("p1")
	if after <= before {
		t.Fatalf("event sequence did not grow: %d -> %d", before, after)
	}
	st1.Close()

	st3, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st3.Close()
	e3, _ := core.NewEngine(st3, "p1")
	o, err := e3.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Stories) != 1 || o.Stories[0].Usage != "210k (90k sub)" || *o.Stories[0].TokensMain != 120000 || *o.Stories[0].TokensSubagents != 90000 {
		t.Fatalf("overview after reopen = %+v", o.Stories)
	}
	html, err := board.Render(e3)
	if err != nil || !strings.Contains(html, "210k (90k sub)") {
		t.Fatalf("board after reopen: err=%v", err)
	}

	server := httptest.NewServer(board.Handler(e3, st3))
	defer server.Close()
	events, err := server.Client().Get(server.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer events.Body.Close()
	reader := bufio.NewReader(events.Body)
	if line, err := reader.ReadString('\n'); err != nil || !strings.Contains(line, "connected") {
		t.Fatalf("SSE greeting = %q, err=%v", line, err)
	}
	_, _ = reader.ReadString('\n')
	reload := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		reload <- line
	}()
	if err := e3.AddUsage(story.ID, 1000, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-reload:
		if !strings.Contains(line, "data: reload") {
			t.Fatalf("SSE update = %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSE did not report usage update")
	}
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	served, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(served), "211k (90k sub)") || !strings.Contains(string(served), "new EventSource") {
		t.Fatalf("served board did not refresh usage: %.500s", served)
	}
}
