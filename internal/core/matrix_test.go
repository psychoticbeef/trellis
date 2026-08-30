package core_test

import (
	"testing"

	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

// newMemEngine backs the engine with an in-memory SQLite store.
func newMemEngine(t *testing.T) *core.Engine {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateProject(store.Project{ID: "p1", Name: "test", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// TestParentKindMatrix_UT_1 proves UT-1 (DD-1 "Tree rule validation"):
// the full parent/child legality matrix for all seven kinds, the
// arch-singleton rule and id allocation monotonicity, directly against the
// engine with an in-memory store.
func TestParentKindMatrix_UT_1(t *testing.T) {
	e := newMemEngine(t)

	// Fixture parents, one per kind that can carry children.
	story := mustCreate(t, e, model.KindStory, "", "s", nil)
	e.AddAC(story.ID, "g", "w", "t")
	at := mustCreate(t, e, model.KindAcceptanceTest, story.ID, "at", []string{story.ID + ".AC-1"})
	arch := mustCreate(t, e, model.KindArch, story.ID, "as", nil)
	it := mustCreate(t, e, model.KindIntegrationTest, arch.ID, "it", nil)
	dd := mustCreate(t, e, model.KindDetailDesign, arch.ID, "dd", nil)
	ut := mustCreate(t, e, model.KindUnitTest, dd.ID, "ut", nil)
	cc := mustCreate(t, e, model.KindCrossCutting, "", "cc", nil)

	parents := map[model.Kind]string{
		model.KindStory:           story.ID,
		model.KindAcceptanceTest:  at.ID,
		model.KindArch:            arch.ID,
		model.KindIntegrationTest: it.ID,
		model.KindDetailDesign:    dd.ID,
		model.KindUnitTest:        ut.ID,
		model.KindCrossCutting:    cc.ID,
	}
	legalParent := map[model.Kind]model.Kind{
		model.KindAcceptanceTest:  model.KindStory,
		model.KindArch:            model.KindStory,
		model.KindIntegrationTest: model.KindArch,
		model.KindDetailDesign:    model.KindArch,
		model.KindUnitTest:        model.KindDetailDesign,
	}
	kinds := []model.Kind{model.KindStory, model.KindAcceptanceTest, model.KindArch,
		model.KindIntegrationTest, model.KindDetailDesign, model.KindUnitTest, model.KindCrossCutting}

	for _, child := range kinds {
		want, isChild := legalParent[child]
		for _, parent := range kinds {
			var covers []string
			if child == model.KindAcceptanceTest {
				covers = []string{story.ID + ".AC-1"}
			}
			_, err := e.CreateNode(child, parents[parent], "x", "", covers)
			legal := isChild && parent == want && child != model.KindArch // arch: singleton already exists
			if legal && err != nil {
				t.Errorf("%s under %s: want ok, got %v", child, parent, err)
			}
			if !legal && err == nil {
				t.Errorf("%s under %s: want rejection, got ok", child, parent)
			}
		}
		// Root kinds must also reject any parent and accept none.
		if !isChild {
			if _, err := e.CreateNode(child, "", "root ok", "", nil); err != nil {
				t.Errorf("%s as root: want ok, got %v", child, err)
			}
		} else if _, err := e.CreateNode(child, "", "x", "", nil); err == nil {
			t.Errorf("%s without parent: want rejection, got ok", child)
		}
	}

	// Arch singleton: a second story may have its own arch.
	story2 := mustCreate(t, e, model.KindStory, "", "s2", nil)
	mustCreate(t, e, model.KindArch, story2.ID, "as2", nil)
	if _, err := e.CreateNode(model.KindArch, story2.ID, "as3", "", nil); err == nil {
		t.Error("second arch on story2: want rejection, got ok")
	}

	// Id monotonicity: deleting never frees an id.
	d1 := mustCreate(t, e, model.KindDetailDesign, arch.ID, "tmp", nil)
	if err := e.DeleteNode(d1.ID); err != nil {
		t.Fatal(err)
	}
	d2 := mustCreate(t, e, model.KindDetailDesign, arch.ID, "tmp2", nil)
	if d2.ID <= d1.ID {
		t.Errorf("id after delete: %s not greater than %s (ids must never be reused)", d2.ID, d1.ID)
	}
}
