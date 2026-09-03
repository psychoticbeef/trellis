package store

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"trellis/internal/model"
)

// TestActivityFTSCandidates_UT_58_IT_51 proves activity-only relevance,
// deterministic ties, result caps, literal query safety, and index lifecycle.
func TestActivityFTSCandidates_UT_58_IT_51(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "trellis.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateProject(Project{ID: "p1", Name: "test"}); err != nil {
		t.Fatal(err)
	}
	activities := []model.Node{
		{ID: "UA-1", ProjectID: "p1", Kind: model.KindActivity, Title: "quantum launch", Body: "quantum launch quantum launch", Position: 4},
		{ID: "UA-2", ProjectID: "p1", Kind: model.KindActivity, Title: "quantum launch", Position: 1},
		{ID: "UA-3", ProjectID: "p1", Kind: model.KindActivity, Title: "quantum launch", Position: 2},
		{ID: "UA-4", ProjectID: "p1", Kind: model.KindActivity, Title: "quantum launch", Position: 3},
	}
	for _, activity := range activities {
		if err := st.InsertNode(activity); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.InsertNode(model.Node{ID: "US-1", ProjectID: "p1", Kind: model.KindStory, Title: "quantum launch quantum launch", Body: "quantum launch"}); err != nil {
		t.Fatal(err)
	}

	got, err := st.SearchActivityFTS("p1", "quantum launch", 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"UA-1", "UA-2", "UA-3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ranked activities=%v want %v", got, want)
	}
	if got, err := st.SearchActivityFTS("p1", `NEAR(x) OR "`, 3); err != nil || len(got) != 0 {
		t.Fatalf("literal hostile query: got=%v err=%v", got, err)
	}

	activity := activities[3]
	activity.Title = "plasma deploy"
	activity.Body = "plasma deploy"
	if err := st.UpdateNodeContentAndPosition(activity); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.SearchActivityFTS("p1", "plasma deploy", 3); !reflect.DeepEqual(got, []string{"UA-4"}) {
		t.Fatalf("updated activity index=%v", got)
	}
	if err := st.DeleteNode("p1", activity.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.SearchActivityFTS("p1", "plasma deploy", 3); len(got) != 0 {
		t.Fatalf("deleted activity remained indexed: %v", got)
	}
	if hits, _ := st.SearchFTS("p1", "quantum launch"); len(hits) != 4 {
		t.Fatalf("search_specs index changed: %v", hits)
	}

	for count := 0; count <= 4; count++ {
		t.Run(fmt.Sprintf("cardinality_%d", count), func(t *testing.T) {
			cardinalityStore, err := Open(filepath.Join(t.TempDir(), "trellis.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer cardinalityStore.Close()
			if err := cardinalityStore.CreateProject(Project{ID: "p1", Name: "test"}); err != nil {
				t.Fatal(err)
			}
			var want []string
			for i := 1; i <= count; i++ {
				id := fmt.Sprintf("UA-%d", i)
				if err := cardinalityStore.InsertNode(model.Node{ID: id, ProjectID: "p1", Kind: model.KindActivity, Title: "cardinality probe", Position: 1}); err != nil {
					t.Fatal(err)
				}
				if len(want) < 3 {
					want = append(want, id)
				}
			}
			got, err := cardinalityStore.SearchActivityFTS("p1", "cardinality probe", 3)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("same-position numeric id order=%v want %v", got, want)
			}
		})
	}
}
