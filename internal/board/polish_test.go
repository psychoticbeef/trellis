package board_test

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"trellis/internal/board"
	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

func categorizedPolishStory(t *testing.T, e *core.Engine) string {
	t.Helper()
	story, err := e.CreateNode(model.KindStory, "", "polished <story>", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AddCategorizedUsage(story.ID,
		core.TokenCategories{Input: 999, Output: 1000, CacheRead: 111000, CacheWrite: 1113014},
		core.TokenCategories{Input: 16400000, Output: 1499, CacheRead: 1500, CacheWrite: 1000000}); err != nil {
		t.Fatal(err)
	}
	return story.ID
}

func assertPolishedTokenHTML(t *testing.T, html string) {
	t.Helper()
	for _, want := range []string{
		`title="999">999</td>`,
		`title="1000">1k</td>`,
		`title="111000">111k</td>`,
		`title="1113014">1.1M</td>`,
		`title="16400000">16.4M</td>`,
		`title="1499">1k</td>`,
		`title="1500">2k</td>`,
		`title="1000000">1.0M</td>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("board missing formatted categorized token usage value %q", want)
		}
	}
	if strings.Contains(html, "observability, not a gate: closing a gap stays a judgment call") {
		t.Error("board exposes design-philosophy coverage commentary")
	}
}

// TestBoardFormattingAndMeta_UT_45 proves UT-45: every categorized token
// value uses shared raw/k/M formatting with exact title metadata, and board UI
// omits coverage design philosophy.
func TestBoardFormattingAndMeta_UT_45(t *testing.T) {
	e, st := newEngine(t)
	categorizedPolishStory(t, e)
	if err := st.SetCoverage("p1", []store.CoverageRow{{File: "internal/board/board.go", Covered: 1, Total: 2}}); err != nil {
		t.Fatal(err)
	}
	html, err := board.Render(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "Largest gaps") {
		t.Fatal("coverage UI missing; meta-line assertion would not exercise coverage rendering")
	}
	if !strings.Contains(html, "polished &lt;story&gt;") {
		t.Fatal("board did not HTML-escape story content")
	}
	assertPolishedTokenHTML(t, html)
}

// TestStaticLiveAndMultiBoardPolish_IT_42 proves IT-42: static, single-board,
// and multi-project handlers share polished categorized token usage rendering.
func TestStaticLiveAndMultiBoardPolish_IT_42(t *testing.T) {
	e, st := newEngine(t)
	categorizedPolishStory(t, e)

	static, err := board.Render(e)
	if err != nil {
		t.Fatal(err)
	}

	singleRecorder := httptest.NewRecorder()
	board.Handler(e, st).ServeHTTP(singleRecorder, httptest.NewRequest("GET", "/", nil))
	single, err := io.ReadAll(singleRecorder.Result().Body)
	if err != nil {
		t.Fatal(err)
	}

	multiRecorder := httptest.NewRecorder()
	board.MultiHandler(st).ServeHTTP(multiRecorder, httptest.NewRequest("GET", "/p/p1/", nil))
	multi, err := io.ReadAll(multiRecorder.Result().Body)
	if err != nil {
		t.Fatal(err)
	}

	for name, html := range map[string]string{"static": static, "single": string(single), "multi": string(multi)} {
		t.Run(name, func(t *testing.T) { assertPolishedTokenHTML(t, html) })
	}
}
