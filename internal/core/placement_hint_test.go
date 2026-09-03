package core_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"trellis/internal/core"
	"trellis/internal/model"
)

// TestPlacementHintState_UT_59_IT_51 proves transient hint gating, top-three
// activity ranking, exhaustive gap ordering, and current persisted reads.
func TestPlacementHintState_UT_59_IT_51(t *testing.T) {
	e, _ := newEngineStore(t)
	unmapped := mustCreate(t, e, model.KindStory, "", "seed", nil)
	activities := make([]model.Node, 4)
	definitions := []struct {
		title    string
		body     string
		position int
	}{
		{"quantum launch", "quantum launch quantum launch", 4},
		{"quantum launch", "", 1},
		{"quantum launch", "", 2},
		{"quantum launch", "", 3},
	}
	for i, definition := range definitions {
		var err error
		activities[i], err = e.CreateNodeWithPosition(model.KindActivity, "", definition.title, definition.body, nil, intPtr(definition.position))
		if err != nil {
			t.Fatal(err)
		}
		approve(t, e, activities[i].ID)
	}
	if _, err := e.CreateNodeWithPlacement(model.KindStory, "", "placed", "", nil, nil, activities[0].ID, intPtr(2)); err != nil {
		t.Fatal(err)
	}

	hint, err := e.PlacementHint("quantum", "launch")
	if err != nil {
		t.Fatal(err)
	}
	if hint == nil {
		t.Fatal("missing placement hint for incomplete story map")
	}
	gotActivities := make([]string, 0, len(hint.Activities))
	for _, activity := range hint.Activities {
		gotActivities = append(gotActivities, activity.ID)
	}
	if want := []string{activities[0].ID, activities[1].ID, activities[2].ID}; !reflect.DeepEqual(gotActivities, want) {
		t.Fatalf("activity candidates=%v want %v", gotActivities, want)
	}
	wantGaps := []core.StoryMapGap{
		{ActivityID: activities[1].ID, Slice: 1}, {ActivityID: activities[1].ID, Slice: 2},
		{ActivityID: activities[2].ID, Slice: 1}, {ActivityID: activities[2].ID, Slice: 2},
		{ActivityID: activities[3].ID, Slice: 1}, {ActivityID: activities[3].ID, Slice: 2},
		{ActivityID: activities[0].ID, Slice: 1},
	}
	if !reflect.DeepEqual(hint.Gaps, wantGaps) {
		t.Fatalf("gaps=%+v want %+v", hint.Gaps, wantGaps)
	}
	if _, err := e.SetMapPosition(unmapped.ID, activities[0].ID, 1); err != nil {
		t.Fatal(err)
	}
	completeHint, err := e.PlacementHint("quantum", "launch")
	if err != nil {
		t.Fatal(err)
	}
	if completeHint != nil {
		t.Fatalf("map complete returned hint: %+v", completeHint)
	}

	withoutMap := newEngine(t)
	if hint, err := withoutMap.PlacementHint("quantum", "launch"); err != nil || hint != nil {
		t.Fatalf("no story map hint=%+v err=%v", hint, err)
	}

	legacy := core.NodeReport{ID: "US-1", Kind: "story", Title: "legacy", Body: "", Hash: "hash", Fresh: false}
	blob, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	const legacyGolden = `{"id":"US-1","kind":"story","title":"legacy","body":"","content_hash":"hash","fresh":false}`
	if string(blob) != legacyGolden {
		t.Fatalf("no story map NodeReport changed:\nwant %s\ngot  %s", legacyGolden, blob)
	}
}
