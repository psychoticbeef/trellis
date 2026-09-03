package board_test

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"trellis/internal/board"
	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

type storyMapFixture struct {
	engine   *core.Engine
	store    *store.Store
	buildID  string
	shipID   string
	mappedID string
	unmapped string
	rankOneA string
	rankOneB string
}

func newStoryMapFixture(t *testing.T) storyMapFixture {
	t.Helper()
	e, st := newEngine(t)
	stories := make([]model.Node, 5)
	for i, title := range []string{"rank two done", "unmapped story", "ship slice one", "rank one a", "rank one b"} {
		story, err := e.CreateNode(model.KindStory, "", title, title+" body", nil)
		if err != nil {
			t.Fatal(err)
		}
		stories[i] = story
	}
	positionTwo, positionOne := 2, 1
	build, err := e.CreateNodeWithPosition(model.KindActivity, "", "Build", "", nil, &positionTwo)
	if err != nil {
		t.Fatal(err)
	}
	ship, err := e.CreateNodeWithPosition(model.KindActivity, "", "Ship", "", nil, &positionOne)
	if err != nil {
		t.Fatal(err)
	}
	placements := []struct {
		story       model.Node
		activity    string
		rank, slice int
		status      string
	}{
		{stories[0], build.ID, 2, 3, model.StatusDone},
		{stories[2], ship.ID, 1, 1, model.StatusRefined},
		{stories[3], build.ID, 1, 3, model.StatusTodo},
		{stories[4], build.ID, 1, 3, model.StatusInProgress},
	}
	for _, placement := range placements {
		if err := st.SetStoryPlacement(e.Project.ID, placement.story.ID, placement.activity, placement.rank, placement.slice); err != nil {
			t.Fatal(err)
		}
		if err := st.SetNodeStatus(e.Project.ID, placement.story.ID, placement.status); err != nil {
			t.Fatal(err)
		}
	}
	report, err := e.Node(stories[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Approve(stories[0].ID, report.Hash, nil); err != nil {
		t.Fatal(err)
	}
	changed := "changed after approval"
	if _, err := e.UpdateNode(stories[0].ID, nil, &changed, nil); err != nil {
		t.Fatal(err)
	}
	return storyMapFixture{
		engine: e, store: st, buildID: build.ID, shipID: ship.ID,
		mappedID: stories[0].ID, unmapped: stories[1].ID,
		rankOneA: stories[3].ID, rankOneB: stories[4].ID,
	}
}

func mapPanel(t *testing.T, html string) string {
	t.Helper()
	start := strings.Index(html, `id="board-panel-map"`)
	end := strings.Index(html, `id="project-context"`)
	if start < 0 || end <= start {
		t.Fatalf("map panel bounds missing: start=%d end=%d", start, end)
	}
	return html[start:end]
}

func storyPlacement(t *testing.T, html, storyID string) string {
	t.Helper()
	start := strings.Index(html, `data-story-placement="`+storyID+`"`)
	if start < 0 {
		t.Fatalf("story placement %s missing", storyID)
	}
	end := strings.Index(html[start:], "</div>")
	if end < 0 {
		t.Fatalf("story placement %s has no end", storyID)
	}
	return html[start : start+end]
}

func mapCard(t *testing.T, html, storyID string) string {
	t.Helper()
	panel := mapPanel(t, html)
	start := strings.Index(panel, `data-story-open="`+storyID+`"`)
	if start < 0 {
		t.Fatalf("map card %s missing", storyID)
	}
	end := strings.Index(panel[start:], "</button>")
	if end < 0 {
		t.Fatalf("map card %s has no end", storyID)
	}
	return panel[start : start+end]
}

func activateMapCard(t *testing.T, html, storyID string) string {
	t.Helper()
	card := mapCard(t, html, storyID)
	control := `aria-controls="story-` + storyID + `"`
	if !strings.Contains(card, control) {
		t.Fatalf("map card %s does not target story detail overlay", storyID)
	}
	for _, hook := range []string{
		`event.target.closest('[data-story-open]')`,
		`document.getElementById('story-' + storyOpener.getAttribute('data-story-open'))`,
		`openStoryDetail(`,
	} {
		if !strings.Contains(html, hook) {
			t.Fatalf("map card activation hook missing %q", hook)
		}
	}
	start := strings.Index(html, `id="story-`+storyID+`"`)
	if start < 0 {
		t.Fatalf("story detail overlay %s missing", storyID)
	}
	return html[start:]
}

// TestStoryMapProjection_UT_60 proves UT-60: map projection orders activities,
// used slices, and cards while preserving status, integrity marker, and placement.
func TestStoryMapProjection_UT_60(t *testing.T) {
	fixture := newStoryMapFixture(t)
	html, err := board.Render(fixture.engine)
	if err != nil {
		t.Fatal(err)
	}
	panel := mapPanel(t, html)
	ship := strings.Index(panel, `data-map-activity="`+fixture.shipID+`"`)
	build := strings.Index(panel, `data-map-activity="`+fixture.buildID+`"`)
	unmapped := strings.Index(panel, `data-map-unmapped`)
	if !(ship >= 0 && ship < build && build < unmapped) {
		t.Fatalf("activity/unmapped column order wrong: ship=%d build=%d unmapped=%d", ship, build, unmapped)
	}
	sliceOne := strings.Index(panel, `data-map-slice="1"`)
	sliceThree := strings.Index(panel, `data-map-slice="3"`)
	if !(sliceOne >= 0 && sliceOne < sliceThree) || strings.Contains(panel, `data-map-slice="2"`) {
		t.Fatalf("used slice rows wrong: slice1=%d slice3=%d", sliceOne, sliceThree)
	}
	cellStart := strings.Index(panel, `data-map-cell="`+fixture.buildID+`:3"`)
	cellEnd := strings.Index(panel[cellStart:], "</td>") + cellStart
	if cellStart < 0 || cellEnd <= cellStart {
		t.Fatal("build slice 3 cell missing")
	}
	cell := panel[cellStart:cellEnd]
	first := strings.Index(cell, `data-story-open="`+fixture.rankOneA+`"`)
	second := strings.Index(cell, `data-story-open="`+fixture.rankOneB+`"`)
	third := strings.Index(cell, `data-story-open="`+fixture.mappedID+`"`)
	if !(first >= 0 && first < second && second < third) {
		t.Fatalf("rank/natural card order wrong: %d %d %d", first, second, third)
	}
	mappedCard := mapCard(t, html, fixture.mappedID)
	for _, want := range []string{`class="state st-done"`, `class="mark card-marker stale"`, ">stale<"} {
		if !strings.Contains(mappedCard, want) {
			t.Errorf("mapped card missing %q: %s", want, mappedCard)
		}
	}
	mappedPlacement := storyPlacement(t, html, fixture.mappedID)
	for _, want := range []string{fixture.buildID, "Build", `data-placement-rank>2<`, `data-placement-slice>3<`} {
		if !strings.Contains(mappedPlacement, want) {
			t.Errorf("mapped placement missing %q: %s", want, mappedPlacement)
		}
	}
	unmappedPlacement := storyPlacement(t, html, fixture.unmapped)
	if !strings.Contains(unmappedPlacement, `data-placement-unmapped>unmapped<`) ||
		strings.Contains(unmappedPlacement, "data-placement-activity") ||
		strings.Contains(unmappedPlacement, "data-placement-rank") ||
		strings.Contains(unmappedPlacement, "data-placement-slice") {
		t.Fatalf("unmapped story gained invented placement: %s", unmappedPlacement)
	}
}

var generatedStamp = regexp.MustCompile(`generated [0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}`)

func normalizedBoard(html string) string {
	return generatedStamp.ReplaceAllString(html, "generated FIXED-STAMP")
}

// TestConditionalMapHTML_UT_61 proves UT-61: map assets and accessible keyboard
// tabs are conditional, self-contained, read-only, and preserve legacy bytes.
func TestConditionalMapHTML_UT_61(t *testing.T) {
	noMapEngine, _ := newEngine(t)
	legacy, err := board.Render(noMapEngine)
	if err != nil {
		t.Fatal(err)
	}
	const legacySHA256 = "638ce6abd0709db17eb8374824fc0620fced1302829352729e9e808a30d9a2e3"
	gotHash := fmt.Sprintf("%x", sha256.Sum256([]byte(normalizedBoard(legacy))))
	if gotHash != legacySHA256 {
		t.Fatalf("no-activity HTML bytes changed: got %s want %s (len %d)", gotHash, legacySHA256, len(normalizedBoard(legacy)))
	}
	for _, forbidden := range []string{"data-board-tab", "board-panel-map", "map-grid", "activateBoardTab", "ArrowRight", "data-story-placement"} {
		if strings.Contains(legacy, forbidden) {
			t.Errorf("no-activity HTML gained %q", forbidden)
		}
	}

	e, _ := newEngine(t)
	if _, err := e.CreateNode(model.KindActivity, "", "Only activity", "", nil); err != nil {
		t.Fatal(err)
	}
	mapped, err := board.Render(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`role="tablist"`, `role="tab" aria-selected="true"`, `role="tabpanel"`,
		`tabindex="-1" data-board-tab="map"`, `data-map-unmapped`, `data-map-unmapped-cell`,
		"activateBoardTab", "ArrowRight", "ArrowLeft", "Home", "End", ".map-grid", ".board-tab",
		`candidate.setAttribute('aria-selected', selected ? 'true' : 'false')`,
		`candidate.setAttribute('tabindex', selected ? '0' : '-1')`,
		`if (panel) panel.hidden = !selected`, `if (moveFocus) tab.focus()`,
	} {
		if !strings.Contains(mapped, want) {
			t.Errorf("activity HTML missing %q", want)
		}
	}
	for _, forbidden := range []string{"<link ", " src=", "https://", "http://", "fetch(", "XMLHttpRequest", "data-story-edit", "data-story-transition", `method="post"`} {
		if strings.Contains(mapped, forbidden) {
			t.Errorf("map HTML contains external/write surface %q", forbidden)
		}
	}
}

// TestStoryMapSharedRenderPaths_IT_52 proves IT-52: static, --serve, and MCP
// board handlers share map markup and detail placement; shared Render stays
// read-only while later live-only capabilities may augment served output.
func TestStoryMapSharedRenderPaths_IT_52(t *testing.T) {
	fixture := newStoryMapFixture(t)
	static, err := board.Render(fixture.engine)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/", nil)
	recorder := httptest.NewRecorder()
	board.Handler(fixture.engine, fixture.store).ServeHTTP(recorder, request)
	singleBody, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("GET", "/p/p1/", nil)
	recorder = httptest.NewRecorder()
	board.MultiHandler(fixture.store).ServeHTTP(recorder, request)
	multiBody, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	for name, html := range map[string]string{"static": static, "single": string(singleBody), "mcp": string(multiBody)} {
		for _, want := range []string{
			`data-board-tab="map"`, `data-map-activity="` + fixture.shipID + `"`,
			`data-map-slice="1"`, `data-map-slice="3"`, `data-map-unmapped-cell`,
			`data-story-open="` + fixture.mappedID + `"`, `data-story-placement="` + fixture.mappedID + `"`,
			`data-placement-activity`, fixture.buildID, `data-placement-rank>2<`, `data-placement-slice>3<`,
		} {
			if !strings.Contains(html, want) {
				t.Errorf("%s render missing %q", name, want)
			}
		}
		panel := mapPanel(t, html)
		ship := strings.Index(panel, `data-map-activity="`+fixture.shipID+`"`)
		build := strings.Index(panel, `data-map-activity="`+fixture.buildID+`"`)
		sliceOne := strings.Index(panel, `data-map-slice="1"`)
		sliceThree := strings.Index(panel, `data-map-slice="3"`)
		if !(ship >= 0 && ship < build && build < strings.Index(panel, "data-map-unmapped")) {
			t.Errorf("%s activity or unmapped story column order wrong", name)
		}
		if !(sliceOne >= 0 && sliceOne < sliceThree) {
			t.Errorf("%s slice row order wrong", name)
		}
		cellStart := strings.Index(panel, `data-map-cell="`+fixture.buildID+`:3"`)
		if cellStart < 0 {
			t.Errorf("%s build slice 3 cell missing", name)
		} else {
			cellEnd := strings.Index(panel[cellStart:], "</td>")
			if cellEnd < 0 {
				t.Errorf("%s build slice 3 cell has no end", name)
			} else {
				cell := panel[cellStart : cellStart+cellEnd]
				first := strings.Index(cell, fixture.rankOneA)
				second := strings.Index(cell, fixture.rankOneB)
				third := strings.Index(cell, fixture.mappedID)
				if !(first >= 0 && first < second && second < third) {
					t.Errorf("%s placement rank/story id order wrong", name)
				}
			}
		}
		card := mapCard(t, html, fixture.mappedID)
		for _, want := range []string{`class="state st-done"`, `class="mark card-marker stale"`, ">stale<"} {
			if !strings.Contains(card, want) {
				t.Errorf("%s map card missing status or integrity marker %q", name, want)
			}
		}
	}
	if strings.Contains(static, "fetch(") || strings.Contains(static, `method="post"`) {
		t.Error("static Render gained write path")
	}
	if strings.Contains(static, "EventSource") {
		t.Fatal("static map gained live reload")
	}
	for name, html := range map[string]string{"single": string(singleBody), "mcp": string(multiBody)} {
		if !strings.Contains(html, `new EventSource("events")`) {
			t.Errorf("%s map lost live reload", name)
		}
	}

	noMapEngine, noMapStore := newEngine(t)
	noMapStatic, err := board.Render(noMapEngine)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("GET", "/", nil)
	recorder = httptest.NewRecorder()
	board.Handler(noMapEngine, noMapStore).ServeHTTP(recorder, request)
	noMapSingle, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest("GET", "/p/p1/", nil)
	recorder = httptest.NewRecorder()
	board.MultiHandler(noMapStore).ServeHTTP(recorder, request)
	noMapMCP, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	const reload = `<script>new EventSource("events").onmessage = () => location.reload();</script>`
	wantLegacy := normalizedBoard(noMapStatic)
	for name, html := range map[string]string{"single": string(noMapSingle), "mcp": string(noMapMCP)} {
		gotLegacy := normalizedBoard(strings.Replace(html, reload, "", 1))
		if gotLegacy != wantLegacy {
			t.Errorf("%s no-activity render diverged from shared legacy bytes", name)
		}
	}
}

type parsedTag struct {
	name  string
	attrs map[string]string
}

var tagPattern = regexp.MustCompile(`<(button|div|tr|th)\b([^>]*)>`)
var attrPattern = regexp.MustCompile(`([[:alnum:]_-]+)(?:="([^"]*)")?`)

func parseStructuralTags(html string) []parsedTag {
	matches := tagPattern.FindAllStringSubmatch(html, -1)
	tags := make([]parsedTag, 0, len(matches))
	for _, match := range matches {
		attrs := map[string]string{}
		for _, attr := range attrPattern.FindAllStringSubmatch(match[2], -1) {
			attrs[attr[1]] = attr[2]
		}
		tags = append(tags, parsedTag{name: match[1], attrs: attrs})
	}
	return tags
}

// TestStoryMapBoardAcceptance_AT_60 proves AT-60 with parsed structural checks
// over static and live self-contained HTML.
func TestStoryMapBoardAcceptance_AT_60(t *testing.T) {
	fixture := newStoryMapFixture(t)
	static, err := board.Render(fixture.engine)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	board.Handler(fixture.engine, fixture.store).ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
	liveBytes, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	for name, html := range map[string]string{"static": static, "live": string(liveBytes)} {
		tags := parseStructuralTags(html)
		var tabs []parsedTag
		var activityHeaders, sliceRows []string
		unmappedHeader := -1
		for index, tag := range tags {
			if tag.attrs["role"] == "tab" {
				tabs = append(tabs, tag)
			}
			if activityID, ok := tag.attrs["data-map-activity"]; ok {
				activityHeaders = append(activityHeaders, activityID)
			}
			if slice, ok := tag.attrs["data-map-slice"]; ok {
				sliceRows = append(sliceRows, slice)
			}
			if _, ok := tag.attrs["data-map-unmapped"]; ok {
				unmappedHeader = index
			}
		}
		if len(tabs) != 2 || tabs[0].attrs["data-board-tab"] != "overview" || tabs[1].attrs["data-board-tab"] != "map" || tabs[1].attrs["tabindex"] != "-1" {
			t.Errorf("%s tabs not accessible/ordered: %+v", name, tabs)
		}
		if strings.Join(activityHeaders, ",") != fixture.shipID+","+fixture.buildID {
			t.Errorf("%s activity header order=%v", name, activityHeaders)
		}
		if strings.Join(sliceRows, ",") != "1,3" {
			t.Errorf("%s slice rows=%v", name, sliceRows)
		}
		if unmappedHeader < 0 {
			t.Errorf("%s missing trailing unmapped header", name)
		}
		panel := mapPanel(t, html)
		if strings.Index(panel, `data-map-activity="`+fixture.buildID+`"`) > strings.Index(panel, "data-map-unmapped") {
			t.Errorf("%s unmapped column not trailing", name)
		}
		detail := activateMapCard(t, html, fixture.mappedID)
		placement := storyPlacement(t, detail, fixture.mappedID)
		for _, want := range []string{fixture.buildID, `data-placement-rank>2<`, `data-placement-slice>3<`} {
			if !strings.Contains(placement, want) {
				t.Errorf("%s activated detail structure missing %q", name, want)
			}
		}
		if strings.Contains(html, "<link ") {
			t.Errorf("%s document not self-contained", name)
		}
		if name == "static" && strings.Contains(html, "fetch(") {
			t.Error("static document not read-only")
		}
	}

	noMapEngine, _ := newEngine(t)
	noMapHTML, err := board.Render(noMapEngine)
	if err != nil {
		t.Fatal(err)
	}
	const legacySHA256 = "638ce6abd0709db17eb8374824fc0620fced1302829352729e9e808a30d9a2e3"
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(normalizedBoard(noMapHTML)))); got != legacySHA256 {
		t.Fatalf("acceptance no-activity bytes changed: got %s want %s", got, legacySHA256)
	}
	for _, tag := range parseStructuralTags(noMapHTML) {
		if tag.attrs["role"] == "tab" {
			t.Fatalf("no-activity acceptance render gained tab: %+v", tag)
		}
		if _, ok := tag.attrs["data-map-activity"]; ok {
			t.Fatalf("no-activity acceptance render gained map structure: %+v", tag)
		}
	}
}
