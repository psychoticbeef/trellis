package store_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"trellis/internal/model"
	"trellis/internal/store"
)

func TestActivityStoreMetadata_UT_46(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trellis.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(store.Project{ID: "p1", Name: "test", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	for _, position := range []int{5, 1, 5} {
		n, err := st.NextID("p1", "UA")
		if err != nil {
			t.Fatal(err)
		}
		activity := model.Node{ID: fmt.Sprintf("UA-%d", n), ProjectID: "p1", Kind: model.KindActivity, Title: "activity", Position: position}
		if err := st.InsertNode(activity); err != nil {
			t.Fatal(err)
		}
	}
	activities, err := st.ListActivities("p1")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{activities[0].ID, activities[1].ID, activities[2].ID}
	if want := []string{"UA-2", "UA-1", "UA-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("activity position/id order=%v want %v", got, want)
	}
	if err := st.SetNodePosition("p1", "UA-3", 0); err != nil {
		t.Fatal(err)
	}
	story := model.Node{ID: "US-1", ProjectID: "p1", Kind: model.KindStory, Title: "story", Status: model.StatusTodo}
	if err := st.InsertNode(story); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStoryActivity("p1", story.ID, "UA-2"); err != nil {
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
	stored, err := st.GetNode("p1", story.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ActivityID != "UA-2" {
		t.Fatalf("story placement lost: %+v", stored)
	}
	activities, err = st.ListActivities("p1")
	if err != nil {
		t.Fatal(err)
	}
	if activities[0].ID != "UA-3" || activities[0].Position != 0 {
		t.Fatalf("activity position lost: %+v", activities)
	}
	next, err := st.NextID("p1", "UA")
	if err != nil {
		t.Fatal(err)
	}
	if next != 4 {
		t.Fatalf("UA counter=%d want 4", next)
	}
}
