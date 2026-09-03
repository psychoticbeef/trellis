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

// TestPlacementGateState_UT_52_UT_57_IT_47 proves mutation guards, per-call
// derivation without a flag, exhaustive candidates, and map-incomplete
// compatibility.
func TestPlacementGateState_UT_52_UT_57_IT_47(t *testing.T) {
	e := newEngine(t)
	first := mustCreate(t, e, model.KindStory, "", "first unmapped", nil)
	activity := mustCreate(t, e, model.KindActivity, "", "Build", nil)

	// Existing unmapped story means map incomplete: old creation behavior stays.
	second := mustCreate(t, e, model.KindStory, "", "second unmapped", nil)
	if _, err := e.SetMapPosition(first.ID, activity.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SetMapPosition(second.ID, activity.ID, 3); err != nil {
		t.Fatal(err)
	}

	// Placement of last unmapped story changes next call immediately. No flag.
	_, err := e.CreateNode(model.KindStory, "", "blocked", "", nil)
	wantErr(t, err, "create_node placement gate rejected", "map complete", activity.ID, "Build", "open slices", "1", "3", "4")
	before, err := e.Node(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.SetMapPosition(first.ID, "", 0)
	wantErr(t, err, "set_map_position placement gate rejected", "map complete", first.ID, activity.ID, "open slices")
	after, err := e.Node(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Activity != after.Activity || before.Rank == nil || after.Rank == nil || *before.Rank != *after.Rank || before.Slice == nil || after.Slice == nil || *before.Slice != *after.Slice {
		t.Fatalf("rejected clear changed placement: before=%+v after=%+v", before, after)
	}

	// Data alone returns project to no-map state; unmapped creation works again.
	if err := e.DeleteNode(first.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteNode(second.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteNode(activity.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateNode(model.KindStory, "", "allowed again", "", nil); err != nil {
		t.Fatalf("no-map behavior changed: %v", err)
	}
}

// TestPlacementPersistenceAndLifecycle_IT_45 proves real-store reload and
// placement metadata isolation for approved done stories.
func TestPlacementPersistenceAndLifecycle_IT_45(t *testing.T) {
	e, st := newEngineStore(t)
	mustCreate(t, e, model.KindStory, "", "incomplete seed", nil)
	build := mustCreate(t, e, model.KindActivity, "", "Build", nil)
	ship := mustCreate(t, e, model.KindActivity, "", "Ship", nil)
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
