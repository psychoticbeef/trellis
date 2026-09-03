package core_test

import (
	"reflect"
	"strings"
	"testing"

	"trellis/internal/core"
	"trellis/internal/model"
)

// TestStoryPlacementEngine_UT_50 proves validation, optional story creation,
// and append ranking scoped by activity and slice.
func TestStoryPlacementEngine_UT_50(t *testing.T) {
	e := newEngine(t)
	// Existing unmapped story keeps map incomplete while optional placement
	// behavior from US-44 is exercised.
	mustCreate(t, e, model.KindStory, "", "incomplete seed", nil)
	build := mustCreate(t, e, model.KindActivity, "", "Build", nil)
	ship := mustCreate(t, e, model.KindActivity, "", "Ship", nil)
	approve(t, e, build.ID)
	approve(t, e, ship.ID)

	one, err := e.CreateNodeWithPlacement(model.KindStory, "", "one", "", nil, nil, build.ID, intPtr(1))
	if err != nil {
		t.Fatal(err)
	}
	two, err := e.CreateNodeWithPlacement(model.KindStory, "", "two", "", nil, nil, build.ID, intPtr(1))
	if err != nil {
		t.Fatal(err)
	}
	otherSlice, err := e.CreateNodeWithPlacement(model.KindStory, "", "slice two", "", nil, nil, build.ID, intPtr(2))
	if err != nil {
		t.Fatal(err)
	}
	otherActivity, err := e.CreateNodeWithPlacement(model.KindStory, "", "ship", "", nil, nil, ship.ID, intPtr(1))
	if err != nil {
		t.Fatal(err)
	}
	if one.Rank != 1 || two.Rank != 2 || otherSlice.Rank != 1 || otherActivity.Rank != 1 {
		t.Fatalf("ranks scoped by activity and slice: one=%+v two=%+v slice=%+v activity=%+v", one, two, otherSlice, otherActivity)
	}
	moved, err := e.SetMapPosition(one.ID, build.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Rank != 3 || moved.Slice != 1 || moved.ActivityID != build.ID {
		t.Fatalf("reposition did not append: %+v", moved)
	}
	unmapped, err := e.CreateNode(model.KindStory, "", "unmapped", "", nil)
	if err != nil || unmapped.ActivityID != "" || unmapped.Rank != 0 || unmapped.Slice != 0 {
		t.Fatalf("optional unmapped story=%+v err=%v", unmapped, err)
	}

	_, err = e.SetMapPosition(one.ID, "UA-999", 0)
	wantErr(t, err, "set_map_position placement rejected", "unknown activity \"UA-999\"", "slice must be at least 1", build.ID, ship.ID)
	_, err = e.SetMapPosition("US-999", "UA-999", 0)
	wantErr(t, err, "set_map_position rejected", "US-999", "not found", "unknown activity", "slice must be at least 1", build.ID, ship.ID)
	_, err = e.CreateNodeWithPlacement(model.KindStory, "", "missing slice", "", nil, nil, build.ID, nil)
	wantErr(t, err, "slice is required", build.ID, ship.ID)
	_, err = e.CreateNodeWithPlacement(model.KindStory, "", "missing activity", "", nil, nil, "", intPtr(1))
	wantErr(t, err, "activity_id is required", build.ID, ship.ID)
	_, err = e.CreateNodeWithPlacement(model.KindActivity, "", "bad", "", nil, nil, build.ID, intPtr(1))
	wantErr(t, err, "only valid on story nodes")
}

// TestPlacementRequiresApprovedActivity_UT_66_IT_56 proves both placement
// boundaries reject never-approved and stale activities before writing, then
// retain existing placement behavior after approval.
func TestPlacementRequiresApprovedActivity_UT_66_IT_56(t *testing.T) {
	e, st := newEngineStore(t)
	seed := mustCreate(t, e, model.KindStory, "", "incomplete seed", nil)
	activity := mustCreate(t, e, model.KindActivity, "", "Build", nil)

	beforeSeed, err := e.Node(seed.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.SetMapPosition(seed.ID, activity.ID, 1)
	wantErr(t, err, "set_map_position placement rejected", "activity "+activity.ID+" is never approved")
	afterSeed, err := e.Node(seed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if beforeSeed.Activity != afterSeed.Activity || beforeSeed.Rank != nil || afterSeed.Rank != nil || beforeSeed.Slice != nil || afterSeed.Slice != nil {
		t.Fatalf("rejected placement wrote story: before=%+v after=%+v", beforeSeed, afterSeed)
	}

	beforeCounters, _ := st.ListCounters(e.Project.ID)
	beforeEvents, _ := st.ListEvents(e.Project.ID, 1000)
	_, err = e.CreateNodeWithPlacement(model.KindStory, "", "blocked", "", nil, nil, activity.ID, intPtr(2))
	wantErr(t, err, "create_node placement rejected", "activity "+activity.ID+" is never approved")
	afterCounters, _ := st.ListCounters(e.Project.ID)
	afterEvents, _ := st.ListEvents(e.Project.ID, 1000)
	if !reflect.DeepEqual(beforeCounters, afterCounters) || !reflect.DeepEqual(beforeEvents, afterEvents) {
		t.Fatalf("rejected create wrote state: counters %v -> %v, events %v -> %v", beforeCounters, afterCounters, beforeEvents, afterEvents)
	}

	approve(t, e, activity.ID)
	newBody := "changed"
	if _, err := e.UpdateNode(activity.ID, nil, &newBody, nil); err != nil {
		t.Fatal(err)
	}
	beforeStaleCounters, _ := st.ListCounters(e.Project.ID)
	beforeStaleEvents, _ := st.ListEvents(e.Project.ID, 1000)
	_, err = e.SetMapPosition(seed.ID, activity.ID, 1)
	wantErr(t, err, "activity "+activity.ID+" is stale", "changed since approval")
	_, err = e.CreateNodeWithPlacement(model.KindStory, "", "still blocked", "", nil, nil, activity.ID, intPtr(0))
	wantErr(t, err, "activity "+activity.ID+" is stale", "changed since approval", "slice must be at least 1")
	afterStaleCounters, _ := st.ListCounters(e.Project.ID)
	afterStaleEvents, _ := st.ListEvents(e.Project.ID, 1000)
	if !reflect.DeepEqual(beforeStaleCounters, afterStaleCounters) || !reflect.DeepEqual(beforeStaleEvents, afterStaleEvents) {
		t.Fatalf("stale activity rejections wrote state: counters %v -> %v, events %v -> %v", beforeStaleCounters, afterStaleCounters, beforeStaleEvents, afterStaleEvents)
	}

	approve(t, e, activity.ID)
	placed, err := e.SetMapPosition(seed.ID, activity.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	created, err := e.CreateNodeWithPlacement(model.KindStory, "", "placed", "", nil, nil, activity.ID, intPtr(1))
	if err != nil {
		t.Fatal(err)
	}
	if placed.Rank != 1 || created.Rank != 2 || created.ActivityID != activity.ID || created.Slice != 1 {
		t.Fatalf("approved activity placement changed: placed=%+v created=%+v", placed, created)
	}
}

// TestActivityEditPlacementDecoupling_UT_67_IT_56 proves activity content
// invalidation stays isolated from placed-story hashes and approvals.
func TestActivityEditPlacementDecoupling_UT_67_IT_56(t *testing.T) {
	e := newEngine(t)
	story := mustCreate(t, e, model.KindStory, "", "story", nil)
	activity := mustCreate(t, e, model.KindActivity, "", "Build", nil)
	approve(t, e, activity.ID)
	approve(t, e, story.ID)
	if _, err := e.SetMapPosition(story.ID, activity.ID, 1); err != nil {
		t.Fatal(err)
	}
	beforeStory, err := e.Node(story.ID)
	if err != nil {
		t.Fatal(err)
	}

	newTitle, newBody := "Build products", "changed body"
	if _, err := e.UpdateNode(activity.ID, &newTitle, &newBody, nil); err != nil {
		t.Fatal(err)
	}
	afterStory, err := e.Node(story.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterActivity, err := e.Node(activity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if beforeStory.Hash != afterStory.Hash || !beforeStory.Fresh || !afterStory.Fresh || !reflect.DeepEqual(beforeStory.Problems, afterStory.Problems) {
		t.Fatalf("activity edit changed placed story: before=%+v after=%+v", beforeStory, afterStory)
	}
	if afterActivity.Fresh || !strings.Contains(strings.Join(afterActivity.Problems, "\n"), "changed since approval") {
		t.Fatalf("edited activity did not report stale: %+v", afterActivity)
	}
	approve(t, e, activity.ID)
	reapproved, err := e.Node(activity.ID)
	if err != nil || !reapproved.Fresh {
		t.Fatalf("activity did not become fresh after re-approval: node=%+v err=%v", reapproved, err)
	}
}

// TestPlacementGateState_UT_52_UT_57_IT_47 proves mutation guards, per-call
// derivation without a flag, exhaustive candidates, and map-incomplete
// compatibility.
func TestPlacementGateState_UT_52_UT_57_IT_47(t *testing.T) {
	t.Run("activities with zero stories", func(t *testing.T) {
		e, st := newEngineStore(t)
		build := mustCreate(t, e, model.KindActivity, "", "Build", nil)
		ship := mustCreate(t, e, model.KindActivity, "", "Ship", nil)
		beforeCounters, _ := st.ListCounters(e.Project.ID)
		beforeEvents, _ := st.ListEvents(e.Project.ID, 1000)

		_, err := e.CreateNode(model.KindStory, "", "blocked", "", nil)
		wantErr(t, err, "create_node placement gate rejected", "map complete",
			"activities:\n- "+build.ID+" (Build)\n- "+ship.ID+" (Ship)", "open slices:\n- 1")
		afterCounters, _ := st.ListCounters(e.Project.ID)
		afterEvents, _ := st.ListEvents(e.Project.ID, 1000)
		if !reflect.DeepEqual(beforeCounters, afterCounters) || !reflect.DeepEqual(beforeEvents, afterEvents) {
			t.Fatalf("rejected create wrote state: counters %v -> %v, events %v -> %v", beforeCounters, afterCounters, beforeEvents, afterEvents)
		}
	})

	e, st := newEngineStore(t)
	first := mustCreate(t, e, model.KindStory, "", "first unmapped", nil)
	build := mustCreate(t, e, model.KindActivity, "", "Build", nil)
	ship := mustCreate(t, e, model.KindActivity, "", "Ship", nil)
	approve(t, e, build.ID)
	approve(t, e, ship.ID)

	// Existing unmapped story means map incomplete: old behavior stays for
	// story creation and placement-clear validation.
	second := mustCreate(t, e, model.KindStory, "", "second unmapped", nil)
	_, err := e.SetMapPosition(first.ID, "", 0)
	wantErr(t, err, "activity_id is required", "slice must be at least 1")
	if strings.Contains(err.Error(), "placement gate") {
		t.Fatalf("map-incomplete clear gained placement gate: %v", err)
	}
	if _, err := e.SetMapPosition(first.ID, build.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SetMapPosition(second.ID, build.ID, 3); err != nil {
		t.Fatal(err)
	}

	// Placement of last unmapped story changes next call immediately. No flag.
	beforeCounters, _ := st.ListCounters(e.Project.ID)
	beforeEvents, _ := st.ListEvents(e.Project.ID, 1000)
	_, err = e.CreateNode(model.KindStory, "", "blocked", "", nil)
	wantErr(t, err, "create_node placement gate rejected", "map complete",
		"activities:\n- "+build.ID+" (Build)\n- "+ship.ID+" (Ship)", "open slices:\n- 1\n- 3\n- 4")
	afterCounters, _ := st.ListCounters(e.Project.ID)
	afterEvents, _ := st.ListEvents(e.Project.ID, 1000)
	if !reflect.DeepEqual(beforeCounters, afterCounters) || !reflect.DeepEqual(beforeEvents, afterEvents) {
		t.Fatalf("rejected create wrote state: counters %v -> %v, events %v -> %v", beforeCounters, afterCounters, beforeEvents, afterEvents)
	}

	before, err := e.Node(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.SetMapPosition(first.ID, "", 0)
	wantErr(t, err, "set_map_position placement gate rejected", "map complete", first.ID, build.ID, ship.ID, "open slices")
	after, err := e.Node(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Activity != after.Activity || before.Rank == nil || after.Rank == nil || *before.Rank != *after.Rank || before.Slice == nil || after.Slice == nil || *before.Slice != *after.Slice {
		t.Fatalf("rejected clear changed placement: before=%+v after=%+v", before, after)
	}

	// Permitted deletions return project to map incomplete; next call derives it.
	if err := e.DeleteNode(first.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteNode(second.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteNode(build.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteNode(ship.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateNode(model.KindStory, "", "allowed again", "", nil); err != nil {
		t.Fatalf("map-incomplete behavior changed: %v", err)
	}
}

// TestPlacementPersistenceAndLifecycle_IT_45 proves real-store reload and
// placement metadata isolation for approved done stories.
func TestPlacementPersistenceAndLifecycle_IT_45(t *testing.T) {
	e, st := newEngineStore(t)
	mustCreate(t, e, model.KindStory, "", "incomplete seed", nil)
	build := mustCreate(t, e, model.KindActivity, "", "Build", nil)
	ship := mustCreate(t, e, model.KindActivity, "", "Ship", nil)
	approve(t, e, build.ID)
	approve(t, e, ship.ID)
	todo := mustCreate(t, e, model.KindStory, "", "todo story", nil)
	if _, err := e.SetMapPosition(todo.ID, build.ID, 2); err != nil {
		t.Fatal(err)
	}
	story := mustCreate(t, e, model.KindStory, "", "done story", nil)
	approve(t, e, story.ID)
	if err := st.SetNodeStatus("p1", story.ID, model.StatusDone); err != nil {
		t.Fatal(err)
	}
	beforeNode, err := e.Node(story.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeTree, err := e.Tree(story.ID)
	if err != nil {
		t.Fatal(err)
	}
	placed, err := e.SetMapPosition(story.ID, build.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if placed.Status != model.StatusDone {
		t.Fatalf("done status changed: %+v", placed)
	}
	afterNode, err := e.Node(story.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterTree, err := e.Tree(story.ID)
	if err != nil {
		t.Fatal(err)
	}
	if beforeNode.Hash != afterNode.Hash || !afterNode.Fresh || !reflect.DeepEqual(beforeTree.Integrity, afterTree.Integrity) {
		t.Fatalf("placement changed content hash, approval freshness, or blocking problems:\nbefore=%+v\nafter=%+v", beforeNode, afterNode)
	}
	if afterTree.Story.Activity != build.ID || afterTree.Story.Rank == nil || *afterTree.Story.Rank != 2 || afterTree.Story.Slice == nil || *afterTree.Story.Slice != 2 {
		t.Fatalf("tree placement missing: %+v", afterTree.Story)
	}

	if _, err := e.SetMapPosition(todo.ID, ship.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SetMapPosition(todo.ID, ship.ID, 1); err != nil {
		t.Fatal(err)
	}
	reloaded, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.Node(story.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Activity != build.ID || got.Rank == nil || *got.Rank != 2 || got.Slice == nil || *got.Slice != 2 || got.Status != model.StatusDone || !got.Fresh {
		t.Fatalf("reloaded placement/lifecycle mismatch: %+v", got)
	}
	if strings.Join(got.Problems, "\n") != strings.Join(beforeNode.Problems, "\n") {
		t.Fatalf("blocking problems changed: before=%v after=%v", beforeNode.Problems, got.Problems)
	}
	movedTodo, err := reloaded.Node(todo.ID)
	if err != nil || movedTodo.Activity != ship.ID || movedTodo.Rank == nil || *movedTodo.Rank != 2 || movedTodo.Slice == nil || *movedTodo.Slice != 1 || movedTodo.Status != model.StatusTodo {
		t.Fatalf("reloaded repositioned todo story=%+v err=%v", movedTodo, err)
	}
}
