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
