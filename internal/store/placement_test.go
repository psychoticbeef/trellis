package store_test

import (
	"path/filepath"
	"testing"

	"trellis/internal/model"
	"trellis/internal/store"
)

// TestStoryPlacementStorage_UT_48 proves placement persistence and metadata
// isolation at the store and model boundary.
func TestStoryPlacementStorage_UT_48(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trellis.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(store.Project{ID: "p1", Name: "test", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	activity := model.Node{ID: "UA-1", ProjectID: "p1", Kind: model.KindActivity, Title: "Build", Position: 1}
	story := model.Node{ID: "US-1", ProjectID: "p1", Kind: model.KindStory, Title: "Story", Status: model.StatusDone,
		ApprovedContentHash: "approved"}
	unmapped := model.Node{ID: "US-2", ProjectID: "p1", Kind: model.KindStory, Title: "Unmapped", Status: model.StatusTodo}
	if err := st.InsertNode(activity); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertNode(story); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertNode(unmapped); err != nil {
		t.Fatal(err)
	}
	beforeHash := model.ContentHash(&story, nil)
	if err := st.SetStoryPlacement("p1", story.ID, activity.ID, 2, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStoryPlacement("p1", story.ID, activity.ID, 3, 2); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	got, err := st.GetNode("p1", story.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActivityID != activity.ID || got.Rank != 3 || got.Slice != 2 {
		t.Fatalf("placement lost after reload: %+v", got)
	}
	if got.Status != model.StatusDone || got.ApprovedContentHash != "approved" || model.ContentHash(&got, nil) != beforeHash {
		t.Fatalf("placement changed lifecycle or content hash: before=%+v after=%+v", story, got)
	}
	unmappedGot, err := st.GetNode("p1", unmapped.ID)
	if err != nil || unmappedGot.ActivityID != "" || unmappedGot.Rank != 0 || unmappedGot.Slice != 0 {
		t.Fatalf("unmapped story read=%+v err=%v", unmappedGot, err)
	}
	if next, err := st.NextStoryRank("p1", activity.ID, 2); err != nil || next != 4 {
		t.Fatalf("next rank=%d err=%v, want 4", next, err)
	}
	if next, err := st.NextStoryRank("p1", activity.ID, 1); err != nil || next != 1 {
		t.Fatalf("slice-scoped next rank=%d err=%v, want 1", next, err)
	}
}
