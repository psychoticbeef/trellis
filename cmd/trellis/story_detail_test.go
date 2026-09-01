package main

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

// TestBoardStoryDetailAcceptance_AT_45 proves AT-45 and US-41.AC-1 through
// US-41.AC-5 through real static and served board CLI entrypoints.
func TestBoardStoryDetailAcceptance_AT_45(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateProject(store.Project{ID: "p1", Name: "story board", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}

	story, _ := e.CreateNode(model.KindStory, "", "story title", "story description", nil)
	e.AddAC(story.ID, "acceptance given", "acceptance when", "acceptance then")
	at, _ := e.CreateNode(model.KindAcceptanceTest, story.ID, "story acceptance", "acceptance body", []string{story.ID + ".AC-1"})
	arch, _ := e.CreateNode(model.KindArch, story.ID, "story architecture", "architecture body", nil)
	it, _ := e.CreateNode(model.KindIntegrationTest, arch.ID, "story integration", "integration body", nil)
	dd, _ := e.CreateNode(model.KindDetailDesign, arch.ID, "story design", "design body", nil)
	ut, _ := e.CreateNode(model.KindUnitTest, dd.ID, "story unit", "unit body", nil)
	for _, id := range []string{story.ID, at.ID, arch.ID, it.ID, dd.ID, ut.ID} {
		r, _ := e.Node(id)
		if err := e.Approve(id, r.Hash, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.SetPaths(story.ID, []string{"cmd/trellis"}); err != nil {
		t.Fatal(err)
	}
	if err := e.AddCategorizedUsage(story.ID,
		core.TokenCategories{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4},
		core.TokenCategories{Input: 5, Output: 6, CacheRead: 7, CacheWrite: 8}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEvidence("p1", ut.ID, []string{"trellis/cmd/trellis::TestBoardStoryDetailAcceptance_AT_45"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Transition(story.ID, "refine"); err != nil {
		t.Fatal(err)
	}
	blocked, _ := e.CreateNode(model.KindStory, "", "blocked title", "blocked description", nil)
	blockedReport, _ := e.Node(blocked.ID)
	if err := e.Approve(blocked.ID, blockedReport.Hash, nil); err != nil {
		t.Fatal(err)
	}
	stale, _ := e.CreateNode(model.KindStory, "", "stale title", "stale description", nil)
	staleReport, _ := e.Node(stale.ID)
	if err := e.Approve(stale.ID, staleReport.Hash, nil); err != nil {
		t.Fatal(err)
	}
	staleDescription := "changed stale description"
	if _, err := e.UpdateNode(stale.ID, nil, &staleDescription, nil); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "board.html")
	if err := run([]string{"board", "p1", "-o", out}); err != nil {
		t.Fatalf("static board: %v", err)
	}
	staticBytes, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	static := string(staticBytes)
	for _, want := range []string{
		">todo<", ">refined<", ">in progress<", ">done<",
		`data-story-open="` + story.ID + `"`, `id="story-` + story.ID + `"`,
		"story description", "acceptance given", "acceptance when", "acceptance then",
		"story architecture", "cmd/trellis", "cache_read", "cache_write",
		"TestBoardStoryDetailAcceptance_AT_45", "data-modal-close",
		"event.key === 'Escape'", "event.target.matches('[data-modal]')", "overview.inert = inert",
	} {
		if !strings.Contains(static, want) {
			t.Errorf("static board missing %q", want)
		}
	}
	card := func(id string) string {
		start := strings.Index(static, `data-story-open="`+id+`"`)
		if start < 0 {
			return ""
		}
		end := strings.Index(static[start:], "</button>")
		if end < 0 {
			return ""
		}
		return static[start : start+end]
	}
	if !strings.Contains(card(story.ID), ">fresh<") || !strings.Contains(card(blocked.ID), ">blocked<") || !strings.Contains(card(stale.ID), ">stale<") {
		t.Fatalf("card markers wrong: fresh=%q blocked=%q stale=%q", card(story.ID), card(blocked.ID), card(stale.ID))
	}
	for _, forbidden := range []string{"story description", "acceptance given", "story architecture"} {
		if strings.Contains(card(story.ID), forbidden) {
			t.Errorf("compact card leaks %q", forbidden)
		}
	}
	for _, forbidden := range []string{"https://", "http://", "fetch(", "data-story-edit", "data-story-transition"} {
		if strings.Contains(static, forbidden) {
			t.Errorf("static board contains forbidden %q", forbidden)
		}
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	go run([]string{"board", "p1", "--serve", "--addr", addr})
	var response *http.Response
	for i := 0; i < 50; i++ {
		response, err = http.Get("http://" + addr + "/")
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("served board unavailable: %v", err)
	}
	servedBytes, _ := io.ReadAll(response.Body)
	response.Body.Close()
	served := string(servedBytes)
	for _, want := range []string{`id="story-` + story.ID + `"`, "story description", `new EventSource("events")`} {
		if !strings.Contains(served, want) {
			t.Errorf("served board missing %q", want)
		}
	}

	events, err := http.Get("http://" + addr + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer events.Body.Close()
	ticks := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(events.Body)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "data: reload") {
				ticks <- scanner.Text()
				return
			}
		}
	}()
	updated := "updated story description"
	if _, err := e.UpdateNode(story.ID, nil, &updated, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ticks:
	case <-time.After(3 * time.Second):
		t.Fatal("served board emitted no SSE reload after story change")
	}
	response, err = http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	refreshed, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(refreshed), updated) {
		t.Fatal("served story detail did not reveal changed data after SSE reload")
	}
}
