package core_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"trellis/internal/core"
	"trellis/internal/model"
)

func TestStoryMapOverview_UT_55_IT_49(t *testing.T) {
	e, st := newEngineStore(t)
	stories := make([]model.Node, 7)
	for i := range stories {
		stories[i] = mustCreate(t, e, model.KindStory, "", "story", nil)
	}
	build, err := e.CreateNodeWithPosition(model.KindActivity, "", "Build", "", nil, intPtr(2))
	if err != nil {
		t.Fatal(err)
	}
	ship, err := e.CreateNodeWithPosition(model.KindActivity, "", "Ship", "", nil, intPtr(1))
	if err != nil {
		t.Fatal(err)
	}
	learn, err := e.CreateNodeWithPosition(model.KindActivity, "", "Learn", "", nil, intPtr(2))
	if err != nil {
		t.Fatal(err)
	}
	for _, activity := range []model.Node{build, ship, learn} {
		approve(t, e, activity.ID)
	}
	placements := []struct {
		story       model.Node
		activity    string
		rank, slice int
		status      string
	}{
		{stories[0], build.ID, 2, 2, model.StatusDone},
		{stories[2], ship.ID, 1, 1, model.StatusRefined},
		{stories[3], build.ID, 1, 2, model.StatusTodo},
		{stories[4], build.ID, 1, 3, model.StatusDone},
		{stories[6], learn.ID, 1, 3, model.StatusInProgress},
	}
	for _, placement := range placements {
		if err := st.SetStoryPlacement(e.Project.ID, placement.story.ID, placement.activity, placement.rank, placement.slice); err != nil {
			t.Fatal(err)
		}
		if err := st.SetNodeStatus(e.Project.ID, placement.story.ID, placement.status); err != nil {
			t.Fatal(err)
		}
	}

	overview, err := e.Overview()
	if err != nil {
		t.Fatal(err)
	}
	storyMap := overview.StoryMap
	if storyMap == nil || storyMap.Status != "2 unmapped" || !reflect.DeepEqual(storyMap.UnmappedStoryIDs, []string{stories[1].ID, stories[5].ID}) {
		t.Fatalf("map status=%+v", storyMap)
	}
	if len(storyMap.Groups) != 4 || storyMap.Groups[0].Activity.ID != ship.ID || storyMap.Groups[1].Activity.ID != build.ID || storyMap.Groups[2].Activity.ID != learn.ID || !storyMap.Groups[3].Unmapped {
		t.Fatalf("group order=%+v", storyMap.Groups)
	}
	if got := []string{storyMap.Groups[1].Stories[0].ID, storyMap.Groups[1].Stories[1].ID, storyMap.Groups[1].Stories[2].ID}; !reflect.DeepEqual(got, []string{stories[3].ID, stories[0].ID, stories[4].ID}) {
		t.Fatalf("placed story order=%v", got)
	}
	if len(storyMap.Groups[3].Stories) != 2 || storyMap.Groups[3].Stories[0].ID != stories[1].ID || storyMap.Groups[3].Stories[1].ID != stories[5].ID {
		t.Fatalf("unmapped group=%+v", storyMap.Groups[3])
	}
	wantBuildProgress := []core.SliceProgress{{Slice: 1}, {Slice: 2, Done: 1, Total: 2}, {Slice: 3, Done: 1, Total: 1}}
	if !reflect.DeepEqual(storyMap.Groups[1].SliceProgress, wantBuildProgress) {
		t.Fatalf("build progress=%+v want %+v", storyMap.Groups[1].SliceProgress, wantBuildProgress)
	}
	wantGaps := []core.StoryMapGap{
		{ActivityID: ship.ID, Slice: 2}, {ActivityID: ship.ID, Slice: 3},
		{ActivityID: build.ID, Slice: 1},
		{ActivityID: learn.ID, Slice: 1}, {ActivityID: learn.ID, Slice: 2},
	}
	if !reflect.DeepEqual(storyMap.Gaps, wantGaps) {
		t.Fatalf("gaps=%+v want %+v", storyMap.Gaps, wantGaps)
	}

	if _, err := e.SetMapPosition(stories[1].ID, build.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SetMapPosition(stories[5].ID, learn.ID, 2); err != nil {
		t.Fatal(err)
	}
	complete, err := e.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if complete.StoryMap.Status != "map complete" || len(complete.StoryMap.UnmappedStoryIDs) != 0 {
		t.Fatalf("complete status=%+v", complete.StoryMap)
	}
	reloaded, err := core.NewEngine(st, e.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reloaded.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(complete.StoryMap, again.StoryMap) {
		t.Fatalf("reloaded map changed:\n%+v\n%+v", complete.StoryMap, again.StoryMap)
	}
}

func TestNoMapOverviewSerialization_UT_54_IT_50(t *testing.T) {
	e := newEngine(t)
	empty, err := e.Overview()
	if err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	const emptyGolden = `{"project":"test","stories":[],"cross_cutting":[],"glossary":[],"stale_nodes":[]}`
	if string(blob) != emptyGolden {
		t.Fatalf("empty no-map overview changed:\nwant %s\ngot  %s", emptyGolden, blob)
	}
	mustCreate(t, e, model.KindStory, "", "unapproved", nil)
	populated, err := e.Overview()
	if err != nil {
		t.Fatal(err)
	}
	type legacyOverview struct {
		Project      string                 `json:"project"`
		Description  string                 `json:"description,omitempty"`
		Coverage     *core.CoverageSummary  `json:"coverage,omitempty"`
		Activities   []core.ActivitySummary `json:"activities,omitempty"`
		Stories      []core.StorySummary    `json:"stories"`
		CrossCutting []core.CCSummary       `json:"cross_cutting"`
		Glossary     any                    `json:"glossary"`
		StaleNodes   []string               `json:"stale_nodes"`
	}
	want, _ := json.Marshal(legacyOverview{Project: populated.Project, Description: populated.Description, Coverage: populated.Coverage, Activities: populated.Activities, Stories: populated.Stories, CrossCutting: populated.CrossCutting, Glossary: populated.Glossary, StaleNodes: populated.StaleNodes})
	got, _ := json.Marshal(populated)
	if string(got) != string(want) {
		t.Fatalf("populated no-map overview changed:\nwant %s\ngot  %s", want, got)
	}
}

func TestMapAwareNextStories_UT_56_IT_50(t *testing.T) {
	e, st := newEngineStore(t)
	stories := make([]model.Node, 8)
	for i := range stories {
		stories[i] = mustCreate(t, e, model.KindStory, "", "story", nil)
	}
	activity := mustCreate(t, e, model.KindActivity, "", "Build", nil)
	placements := []struct{ index, rank, slice int }{{0, 2, 2}, {2, 5, 1}, {3, 1, 2}, {4, 1, 2}}
	for _, placement := range placements {
		if err := st.SetStoryPlacement(e.Project.ID, stories[placement.index].ID, activity.ID, placement.rank, placement.slice); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.LinkDep(stories[5].ID, stories[6].ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := st.SetNodeStatus(e.Project.ID, stories[i].ID, model.StatusRefined); err != nil {
			t.Fatal(err)
		}
	}
	candidates, blocked, err := e.NextStories()
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		got = append(got, candidate.ID)
	}
	want := []string{stories[2].ID, stories[3].ID, stories[4].ID, stories[0].ID, stories[1].ID}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("map candidate order=%v want %v", got, want)
	}
	if len(blocked) != 1 || blocked[0].ID != stories[5].ID || !reflect.DeepEqual(blocked[0].WaitingOn, []string{stories[6].ID + " (todo)"}) {
		t.Fatalf("blocked stories=%+v", blocked)
	}

	legacy, legacyStore := newEngineStore(t)
	legacyStories := make([]model.Node, 10)
	for i := range legacyStories {
		legacyStories[i] = mustCreate(t, legacy, model.KindStory, "", "story", nil)
		if err := legacyStore.SetNodeStatus(legacy.Project.ID, legacyStories[i].ID, model.StatusRefined); err != nil {
			t.Fatal(err)
		}
	}
	legacyCandidates, legacyBlocked, err := legacy.NextStories()
	if err != nil {
		t.Fatal(err)
	}
	if len(legacyBlocked) != 0 || len(legacyCandidates) != 10 {
		t.Fatalf("legacy next result candidates=%d blocked=%v", len(legacyCandidates), legacyBlocked)
	}
	for i, candidate := range legacyCandidates {
		if candidate.ID != legacyStories[i].ID {
			t.Fatalf("legacy order at %d=%s want %s", i, candidate.ID, legacyStories[i].ID)
		}
	}
	emptyEngine := newEngine(t)
	emptyCandidates, emptyBlocked, err := emptyEngine.NextStories()
	if err != nil || len(emptyCandidates) != 0 || len(emptyBlocked) != 0 {
		t.Fatalf("legacy empty answer changed: candidates=%v blocked=%v err=%v", emptyCandidates, emptyBlocked, err)
	}
}
