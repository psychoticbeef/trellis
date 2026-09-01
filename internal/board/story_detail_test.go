package board_test

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"trellis/internal/board"
	"trellis/internal/core"
	"trellis/internal/model"
)

func approvedStoryDetail_UT_44(t *testing.T, e *core.Engine) (string, string) {
	t.Helper()
	story, err := e.CreateNode(model.KindStory, "", "story title", "story description", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddAC(story.ID, "story given", "story when", "story then"); err != nil {
		t.Fatal(err)
	}
	at, _ := e.CreateNode(model.KindAcceptanceTest, story.ID, "story acceptance", "acceptance body", []string{story.ID + ".AC-1"})
	arch, _ := e.CreateNode(model.KindArch, story.ID, "story architecture", "architecture body", nil)
	it, _ := e.CreateNode(model.KindIntegrationTest, arch.ID, "story integration", "integration body", nil)
	dd, _ := e.CreateNode(model.KindDetailDesign, arch.ID, "story design", "design body", nil)
	ut, _ := e.CreateNode(model.KindUnitTest, dd.ID, "story unit", "unit body", nil)
	for _, id := range []string{story.ID, at.ID, arch.ID, it.ID, dd.ID, ut.ID} {
		report, err := e.Node(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Approve(id, report.Hash, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.SetPaths(story.ID, []string{"internal/board"}); err != nil {
		t.Fatal(err)
	}
	return story.ID, ut.ID
}

func cardHTML_UT_44(t *testing.T, html, storyID string) string {
	t.Helper()
	start := strings.Index(html, `data-story-open="`+storyID+`"`)
	if start < 0 {
		t.Fatalf("card %s missing", storyID)
	}
	end := strings.Index(html[start:], "</button>")
	if end < 0 {
		t.Fatalf("card %s is not a button", storyID)
	}
	return html[start : start+end]
}

// TestBoardOverviewAndOverlay_UT_44 proves UT-44: compact cards carry one
// integrity marker while embedded story detail overlays carry complete escaped
// story context, categorized token usage, paths, and evidence.
func TestBoardOverviewAndOverlay_UT_44(t *testing.T) {
	e, st := newEngine(t)
	freshID, evidenceID := approvedStoryDetail_UT_44(t, e)
	if err := e.AddCategorizedUsage(freshID,
		core.TokenCategories{Input: 11, Output: 12, CacheRead: 13, CacheWrite: 14},
		core.TokenCategories{Input: 21, Output: 22, CacheRead: 23, CacheWrite: 24}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEvidence("p1", evidenceID, []string{"pkg::TestStoryDetail_UT_44"}); err != nil {
		t.Fatal(err)
	}

	blocked, _ := e.CreateNode(model.KindStory, "", "blocked title", "blocked detail", nil)
	blockedReport, _ := e.Node(blocked.ID)
	if err := e.Approve(blocked.ID, blockedReport.Hash, nil); err != nil {
		t.Fatal(err)
	}
	stale, _ := e.CreateNode(model.KindStory, "", "stale title", "stale detail", nil)
	staleReport, _ := e.Node(stale.ID)
	if err := e.Approve(stale.ID, staleReport.Hash, nil); err != nil {
		t.Fatal(err)
	}
	staleDetail := "changed stale detail"
	if _, err := e.UpdateNode(stale.ID, nil, &staleDetail, nil); err != nil {
		t.Fatal(err)
	}

	html, err := board.Render(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`data-story-open="` + freshID + `"`, `id="story-` + freshID + `"`,
		`id="project-context" data-modal role="dialog" aria-modal="true" aria-labelledby="project-context-title" hidden`,
		"story description", "story given", "story when", "story then",
		"internal/board", "story architecture", "pkg::TestStoryDetail_UT_44",
		">input<", ">output<", ">cache_read<", ">cache_write<",
		">11<", ">12<", ">13<", ">14<", ">21<", ">22<", ">23<", ">24<",
		"data-modal-close", "event.key === 'Escape'", "event.target.matches('[data-modal]')",
		"overview.inert = inert", "event.key !== 'Tab'", "revealLinkTarget", "parent.tagName === 'DETAILS'",
		"padding-left: 16px",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("board missing %q", want)
		}
	}
	freshCard := cardHTML_UT_44(t, html, freshID)
	for _, want := range []string{freshID, "story title", "fresh", "140 (90 sub)"} {
		if !strings.Contains(freshCard, want) {
			t.Errorf("fresh card missing %q: %s", want, freshCard)
		}
	}
	for _, forbidden := range []string{"story description", "story given", "story architecture", "pkg::TestStoryDetail_UT_44"} {
		if strings.Contains(freshCard, forbidden) {
			t.Errorf("card leaks detail %q: %s", forbidden, freshCard)
		}
	}
	if !strings.Contains(cardHTML_UT_44(t, html, blocked.ID), ">blocked<") {
		t.Error("non-stale incomplete story must carry blocked integrity marker")
	}
	if !strings.Contains(cardHTML_UT_44(t, html, stale.ID), ">stale<") {
		t.Error("stale marker must win over blocked integrity marker")
	}
	if strings.Contains(html, "https://") || strings.Contains(html, "http://") {
		t.Fatal("static board contains external resource")
	}
	for _, forbidden := range []string{"data-story-edit", "data-story-transition", "fetch("} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("board contains write surface %q", forbidden)
		}
	}
}

// TestStaticAndServedStoryDetail_IT_41 proves IT-41: static, single-project,
// and multi-project render paths expose identical embedded details while live
// variants retain relative SSE reload.
func TestStaticAndServedStoryDetail_IT_41(t *testing.T) {
	e, st := newEngine(t)
	storyID, _ := approvedStoryDetail_UT_44(t, e)
	static, err := board.Render(e)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest("GET", "/", nil)
	recorder := httptest.NewRecorder()
	board.Handler(e, st).ServeHTTP(recorder, request)
	single, _ := io.ReadAll(recorder.Result().Body)

	request = httptest.NewRequest("GET", "/p/p1/", nil)
	recorder = httptest.NewRecorder()
	board.MultiHandler(st).ServeHTTP(recorder, request)
	multi, _ := io.ReadAll(recorder.Result().Body)

	for name, html := range map[string]string{"static": static, "single": string(single), "multi": string(multi)} {
		for _, want := range []string{`data-story-open="` + storyID + `"`, `id="story-` + storyID + `"`, "story description", "function openStoryDetail"} {
			if !strings.Contains(html, want) {
				t.Errorf("%s render missing %q", name, want)
			}
		}
		if strings.Contains(html, "fetch(") {
			t.Errorf("%s render contains write-capable fetch", name)
		}
	}
	if strings.Contains(static, "EventSource") {
		t.Fatal("static export contains SSE reload")
	}
	for name, html := range map[string]string{"single": string(single), "multi": string(multi)} {
		if !strings.Contains(html, `new EventSource("events")`) {
			t.Errorf("%s render lost relative SSE reload", name)
		}
	}
}
