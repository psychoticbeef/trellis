package core_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

func intPtr(v int) *int { return &v }

func TestActivityCRUDAndOverview_UT_47(t *testing.T) {
	e := newEngine(t)

	before, err := e.Overview()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "activities") {
		t.Fatalf("no-activity overview changed shape: %s", encoded)
	}
	candidates, blocked, err := e.NextStories()
	if err != nil || len(candidates) != 0 || len(blocked) != 0 {
		t.Fatalf("no-activity next story changed: candidates=%v blocked=%v err=%v", candidates, blocked, err)
	}

	a1, err := e.CreateNodeWithPosition(model.KindActivity, "", "Build", "", nil, intPtr(5))
	if err != nil {
		t.Fatal(err)
	}
	a2, err := e.CreateNodeWithPosition(model.KindActivity, "", "Ship", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a3, err := e.CreateNodeWithPosition(model.KindActivity, "", "Learn", "", nil, intPtr(1))
	if err != nil {
		t.Fatal(err)
	}
	if a1.ID != "UA-1" || a1.Position != 5 || a2.ID != "UA-2" || a2.Position != 6 || a3.Position != 1 {
		t.Fatalf("activity allocation: a1=%+v a2=%+v a3=%+v", a1, a2, a3)
	}

	o, err := e.Overview()
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, a := range o.Activities {
		got = append(got, a.ID)
	}
	if want := []string{"UA-3", "UA-1", "UA-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("activity order=%v want %v", got, want)
	}

	approve(t, e, a1.ID)
	reportBefore, err := e.Node(a1.ID)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := e.UpdateNodeWithPosition(a1.ID, nil, nil, nil, intPtr(-2))
	if err != nil {
		t.Fatal(err)
	}
	reportAfter, err := e.Node(a1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Position != -2 || reportAfter.Hash != reportBefore.Hash || !reportAfter.Fresh {
		t.Fatalf("position metadata changed approval: before=%+v after=%+v", reportBefore, reportAfter)
	}

	story := mustCreate(t, e, model.KindStory, "", "story", nil)
	if _, err := e.CreateNodeWithPosition(model.KindStory, "", "bad", "", nil, intPtr(3)); err == nil || !strings.Contains(err.Error(), "position is only valid on activity") {
		t.Fatalf("story position error=%v", err)
	}
	emptyTitle := " "
	badCovers := []string{"US-999.AC-1"}
	if _, err := e.UpdateNodeWithPosition(story.ID, &emptyTitle, nil, &badCovers, intPtr(3)); err == nil ||
		!strings.Contains(err.Error(), story.ID) || !strings.Contains(err.Error(), "position is only valid on activity") ||
		!strings.Contains(err.Error(), "title must not be empty") || !strings.Contains(err.Error(), "covers is only valid on acceptance_test") {
		t.Fatalf("exhaustive story update error=%v", err)
	}
}

func TestActivityPersistenceAndExport_IT_43(t *testing.T) {
	e, st := newEngineStore(t)
	withoutActivity, err := e.ExportYAML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withoutActivity, "\nactivities:") || strings.Contains(withoutActivity, "\n    position:") || strings.Contains(withoutActivity, "\n    activity:") {
		t.Fatalf("export without activity changed shape:\n%s", withoutActivity)
	}
	a1, err := e.CreateNodeWithPosition(model.KindActivity, "", "Build", "body", nil, intPtr(4))
	if err != nil {
		t.Fatal(err)
	}
	a2, err := e.CreateNodeWithPosition(model.KindActivity, "", "Ship", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteNode(a1.ID); err != nil {
		t.Fatal(err)
	}
	a3, err := e.CreateNodeWithPosition(model.KindActivity, "", "Learn", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a3.ID != "UA-3" || a3.Position != 6 {
		t.Fatalf("monotonic activity allocation after delete: %+v", a3)
	}
	if _, err := e.UpdateNodeWithPosition(a2.ID, nil, nil, nil, intPtr(2)); err != nil {
		t.Fatal(err)
	}
	reloaded, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
	reloadedOverview, err := reloaded.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloadedOverview.Activities) != 2 || reloadedOverview.Activities[0].ID != a2.ID || reloadedOverview.Activities[0].Position != 2 {
		t.Fatalf("reloaded activity state=%+v", reloadedOverview.Activities)
	}

	doc, err := e.ExportYAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, "activities:") || !strings.Contains(doc, "position: 2") || !strings.Contains(doc, "position: 6") {
		t.Fatalf("activity export missing positions:\n%s", doc)
	}
	if err := core.Import(st, []byte(doc), store.Project{ID: "p2", Name: "copy", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	copyEngine, err := core.NewEngine(st, "p2")
	if err != nil {
		t.Fatal(err)
	}
	copyOverview, err := copyEngine.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if len(copyOverview.Activities) != 2 || copyOverview.Activities[0].Position != 2 || copyOverview.Activities[1].Position != 6 {
		t.Fatalf("activity import=%+v", copyOverview.Activities)
	}
	next, err := copyEngine.CreateNode(model.KindActivity, "", "Operate", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != "UA-4" || next.Position != 7 {
		t.Fatalf("imported counters/position not preserved: %+v", next)
	}
}

func TestActivityGuardsAndFreshness_IT_44(t *testing.T) {
	e, st := newEngineStore(t)
	activity, err := e.CreateNode(model.KindActivity, "", "Build", "old body", nil)
	if err != nil {
		t.Fatal(err)
	}
	story1 := mustCreate(t, e, model.KindStory, "", "one", nil)
	story2 := mustCreate(t, e, model.KindStory, "", "two", nil)
	for _, id := range []string{story1.ID, story2.ID} {
		if err := st.SetStoryActivity("p1", id, activity.ID); err != nil {
			t.Fatal(err)
		}
	}

	err = e.DeleteNode(activity.ID)
	if err == nil || !strings.Contains(err.Error(), "placed story "+story1.ID) || !strings.Contains(err.Error(), "placed story "+story2.ID) {
		t.Fatalf("delete must list every placed story: %v", err)
	}

	approve(t, e, story1.ID)
	before, err := e.Tree(story1.ID)
	if err != nil {
		t.Fatal(err)
	}
	newTitle, newBody := "Build products", "new body"
	if _, err := e.UpdateNode(activity.ID, &newTitle, &newBody, nil); err != nil {
		t.Fatal(err)
	}
	after, err := e.Tree(story1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Story.Hash != after.Story.Hash || before.Story.Fresh != after.Story.Fresh || !reflect.DeepEqual(before.Integrity, after.Integrity) {
		t.Fatalf("activity edit changed story content hash, approval freshness, or blocking problems:\nbefore=%+v\nafter=%+v", before, after)
	}
}
