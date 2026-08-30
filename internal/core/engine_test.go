package core_test

import (
	"path/filepath"
	"strings"
	"testing"

	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

func newEngineStore(t *testing.T) (*core.Engine, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "trellis.db"))
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
	return e, st
}

func newEngine(t *testing.T) *core.Engine {
	t.Helper()
	e, _ := newEngineStore(t)
	return e
}

func mustCreate(t *testing.T, e *core.Engine, kind model.Kind, parent, title string, covers []string) model.Node {
	t.Helper()
	n, err := e.CreateNode(kind, parent, title, "body of "+title, covers)
	if err != nil {
		t.Fatalf("create %s under %q: %v", kind, parent, err)
	}
	return n
}

func approve(t *testing.T, e *core.Engine, id string) {
	t.Helper()
	r, err := e.Node(id)
	if err != nil {
		t.Fatal(err)
	}
	deps := map[string]string{}
	for _, d := range r.Deps {
		deps[d.Target] = d.TargetHash
	}
	if err := e.Approve(id, r.Hash, deps); err != nil {
		t.Fatalf("approve %s: %v", id, err)
	}
}

type tree struct {
	story, at, arch, it, dd, ut string
}

// fullTree builds and approves a complete valid story tree.
func fullTree(t *testing.T, e *core.Engine) tree {
	t.Helper()
	story := mustCreate(t, e, model.KindStory, "", "login feature", nil)
	ac, err := e.AddAC(story.ID, "a registered user", "they submit valid credentials", "they are logged in")
	if err != nil {
		t.Fatal(err)
	}
	at := mustCreate(t, e, model.KindAcceptanceTest, story.ID, "login acceptance", []string{ac.ID})
	arch := mustCreate(t, e, model.KindArch, story.ID, "auth architecture", nil)
	it := mustCreate(t, e, model.KindIntegrationTest, arch.ID, "auth integration", nil)
	dd := mustCreate(t, e, model.KindDetailDesign, arch.ID, "token detail design", nil)
	ut := mustCreate(t, e, model.KindUnitTest, dd.ID, "token unit tests", nil)
	for _, id := range []string{story.ID, at.ID, arch.ID, it.ID, dd.ID, ut.ID} {
		approve(t, e, id)
	}
	return tree{story.ID, at.ID, arch.ID, it.ID, dd.ID, ut.ID}
}

func wantErr(t *testing.T, err error, substrings ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %v, got nil", substrings)
	}
	for _, s := range substrings {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error missing %q:\n%v", s, err)
		}
	}
}

func status(t *testing.T, e *core.Engine, storyID string) string {
	t.Helper()
	r, err := e.Node(storyID)
	if err != nil {
		t.Fatal(err)
	}
	return r.Status
}

func TestTreeRules_IT_1(t *testing.T) {
	e := newEngine(t)
	story := mustCreate(t, e, model.KindStory, "", "s", nil)

	_, err := e.CreateNode(model.KindStory, story.ID, "sub story", "", nil)
	wantErr(t, err, "root node")

	_, err = e.CreateNode(model.KindAcceptanceTest, story.ID, "at", "", nil)
	wantErr(t, err, "must declare which acceptance criteria")

	ac, _ := e.AddAC(story.ID, "g", "w", "t")
	_, err = e.CreateNode(model.KindAcceptanceTest, story.ID, "at", "", []string{"US-1.AC-99"})
	wantErr(t, err, "unknown acceptance criteria", ac.ID)

	mustCreate(t, e, model.KindArch, story.ID, "arch", nil)
	_, err = e.CreateNode(model.KindArch, story.ID, "arch2", "", nil)
	wantErr(t, err, "exactly one arch spec")

	_, err = e.CreateNode(model.KindUnitTest, story.ID, "ut", "", nil)
	wantErr(t, err, "illegal parent")

	_, err = e.CreateNode(model.KindUnitTest, "", "ut", "", nil)
	wantErr(t, err, "requires a parent")
}

func TestRefineGuardsListEverything(t *testing.T) {
	e := newEngine(t)
	story := mustCreate(t, e, model.KindStory, "", "s", nil)
	_, err := e.Transition(story.ID, "refine")
	wantErr(t, err, "no acceptance criteria", "no acceptance test specs", "no arch spec", "never approved")
}

func TestApproveRequiresReadAndTopDown(t *testing.T) {
	e := newEngine(t)
	story := mustCreate(t, e, model.KindStory, "", "s", nil)
	e.AddAC(story.ID, "g", "w", "t")
	arch := mustCreate(t, e, model.KindArch, story.ID, "arch", nil)

	err := e.Approve(story.ID, "wrong-hash", nil)
	wantErr(t, err, "hash mismatch")

	r, _ := e.Node(arch.ID)
	err = e.Approve(arch.ID, r.Hash, nil)
	wantErr(t, err, "parent", "approved first")

	approve(t, e, story.ID)
	approve(t, e, arch.ID)
}

func TestFullFlowAndInvalidation(t *testing.T) {
	e := newEngine(t)
	tr := fullTree(t, e)

	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatalf("refine: %v", err)
	}
	if got := status(t, e, tr.story); got != "refined" {
		t.Fatalf("status = %s, want refined", got)
	}

	// Illegal transition: finish from refined.
	_, err := e.Transition(tr.story, "finish")
	wantErr(t, err, "finish blocked", `requires "in_progress"`)

	// Editing the detail design drops the story back to todo and stales the unit test.
	newBody := "changed"
	if _, err := e.UpdateNode(tr.dd, nil, &newBody, nil); err != nil {
		t.Fatal(err)
	}
	if got := status(t, e, tr.story); got != "todo" {
		t.Fatalf("status after edit = %s, want todo (auto downgrade)", got)
	}
	_, err = e.Transition(tr.story, "refine")
	wantErr(t, err, tr.dd+" changed since approval", tr.ut+" stale: parent "+tr.dd)

	// Re-approving top-down repairs the tree.
	approve(t, e, tr.dd)
	approve(t, e, tr.ut)
	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatalf("re-refine: %v", err)
	}
}

func TestACEditInvalidatesChildren(t *testing.T) {
	e := newEngine(t)
	tr := fullTree(t, e)
	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatal(err)
	}

	r, _ := e.Node(tr.story)
	newThen := "they see the dashboard"
	if _, err := e.UpdateAC(r.ACs[0].ID, nil, nil, &newThen); err != nil {
		t.Fatal(err)
	}
	if got := status(t, e, tr.story); got != "todo" {
		t.Fatalf("status = %s, want todo after AC edit", got)
	}
	// Story hash changed: story unapproved, direct children stale.
	_, err := e.Transition(tr.story, "refine")
	wantErr(t, err, tr.story+" changed since approval", "parent "+tr.story+" changed")
}

func TestCrossCuttingDependencies(t *testing.T) {
	e := newEngine(t)
	tr := fullTree(t, e)
	cc := mustCreate(t, e, model.KindCrossCutting, "", "structured logging", nil)

	// Linking to an unapproved cross-cutting spec is illegal.
	err := e.LinkDep(tr.arch, cc.ID)
	wantErr(t, err, "link blocked", "never approved")

	approve(t, e, cc.ID)
	if err := e.LinkDep(tr.arch, cc.ID); err != nil {
		t.Fatal(err)
	}

	// Approving the arch now requires proof of having read the dependency.
	r, _ := e.Node(tr.arch)
	err = e.Approve(tr.arch, r.Hash, nil)
	wantErr(t, err, "missing dep_hashes entry for "+cc.ID)
	approve(t, e, tr.arch) // helper passes dep hashes

	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatalf("refine with fresh dep: %v", err)
	}

	// Editing the cross-cutting spec stales the arch and downgrades the story.
	newBody := "log as json"
	if _, err := e.UpdateNode(cc.ID, nil, &newBody, nil); err != nil {
		t.Fatal(err)
	}
	if got := status(t, e, tr.story); got != "todo" {
		t.Fatalf("status = %s, want todo after cross-cutting edit", got)
	}
	_, err = e.Transition(tr.story, "refine")
	wantErr(t, err, "dependency "+cc.ID+" changed since pin")

	// Deleting a referenced cross-cutting spec is blocked.
	err = e.DeleteNode(cc.ID)
	wantErr(t, err, "delete blocked", "dependent "+tr.arch)
}

func TestDeleteGuards_IT_1(t *testing.T) {
	e := newEngine(t)
	tr := fullTree(t, e)

	err := e.DeleteNode(tr.story)
	wantErr(t, err, "delete blocked", "child")

	r, _ := e.Node(tr.story)
	err = e.DeleteAC(r.ACs[0].ID)
	wantErr(t, err, "delete blocked", "covered by acceptance tests", tr.at)
}

func TestPruneOnlyDone(t *testing.T) {
	e := newEngine(t)
	tr := fullTree(t, e)
	err := e.Prune(tr.story)
	wantErr(t, err, "prune blocked", "only done stories")
}

// TestACStoryHashIntegration_IT_2 proves IT-2 (US-2): AC add/edit/delete each
// change the story hash against a real store, child freshness and story
// status react, and covers validation lists the known AC ids.
func TestACStoryHashIntegration_IT_2(t *testing.T) {
	e := newEngine(t)
	tr := fullTree(t, e)
	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatal(err)
	}

	hash := func() string {
		r, err := e.Node(tr.story)
		if err != nil {
			t.Fatal(err)
		}
		return r.Hash
	}

	h0 := hash()
	ac2, err := e.AddAC(tr.story, "g2", "w2", "t2")
	if err != nil {
		t.Fatal(err)
	}
	h1 := hash()
	if h1 == h0 {
		t.Fatal("AC add must change the story hash")
	}
	newWhen := "changed"
	if _, err := e.UpdateAC(ac2.ID, nil, &newWhen, nil); err != nil {
		t.Fatal(err)
	}
	h2 := hash()
	if h2 == h1 {
		t.Fatal("AC edit must change the story hash")
	}
	// While the story hash differs from the approved one, children are stale
	// and the story dropped to todo.
	if got := status(t, e, tr.story); got != "todo" {
		t.Fatalf("status = %s, want todo", got)
	}
	at, err := e.Node(tr.at)
	if err != nil {
		t.Fatal(err)
	}
	if at.Fresh {
		t.Fatal("acceptance test must be stale while the story hash differs")
	}

	// Freshness is content-addressed: deleting the added AC restores the
	// original story hash, so the child approval becomes valid again.
	if err := e.DeleteAC(ac2.ID); err != nil {
		t.Fatal(err)
	}
	if h3 := hash(); h3 != h0 {
		t.Fatal("deleting the added AC must restore the original story hash")
	}
	at, err = e.Node(tr.at)
	if err != nil {
		t.Fatal(err)
	}
	if !at.Fresh {
		t.Fatal("acceptance test must be fresh again once the story content is restored")
	}

	// Covers validation names the known AC ids.
	_, err = e.CreateNode(model.KindAcceptanceTest, tr.story, "at2", "", []string{"nope"})
	wantErr(t, err, "unknown acceptance criteria", tr.story+".AC-1")
}

// TestInvalidationCascade_IT_3 proves IT-3 (US-3): edits at every tree level
// (story AC, arch, detail design, cross-cutting target) produce the correct
// stale set with reasons and the automatic downgrade, against a real store.
func TestInvalidationCascade_IT_3(t *testing.T) {
	e := newEngine(t)
	tr := fullTree(t, e)
	cc := mustCreate(t, e, model.KindCrossCutting, "", "cc", nil)
	approve(t, e, cc.ID)
	if err := e.LinkDep(tr.arch, cc.ID); err != nil {
		t.Fatal(err)
	}
	approve(t, e, tr.arch) // re-approve with dep pin
	body := "edited"

	refineOK := func() {
		t.Helper()
		if _, err := e.Transition(tr.story, "refine"); err != nil {
			t.Fatalf("refine: %v", err)
		}
	}
	repair := func(ids ...string) {
		t.Helper()
		for _, id := range ids {
			approve(t, e, id)
		}
	}

	refineOK()

	// Story-level edit (AC): everything below the story goes stale.
	r, _ := e.Node(tr.story)
	if _, err := e.UpdateAC(r.ACs[0].ID, &body, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := status(t, e, tr.story); got != "todo" {
		t.Fatalf("after AC edit: status %s, want todo", got)
	}
	_, err := e.Transition(tr.story, "refine")
	wantErr(t, err, tr.story+" changed since approval",
		tr.at+" stale: parent "+tr.story, tr.arch+" stale: parent "+tr.story)
	repair(tr.story, tr.at, tr.arch)
	refineOK()

	// Arch-level edit: integration test and detail design stale, not the AT.
	if _, err := e.UpdateNode(tr.arch, nil, &body, nil); err != nil {
		t.Fatal(err)
	}
	_, err = e.Transition(tr.story, "refine")
	wantErr(t, err, tr.it+" stale: parent "+tr.arch, tr.dd+" stale: parent "+tr.arch)
	if at, _ := e.Node(tr.at); !at.Fresh {
		t.Fatal("acceptance test must stay fresh on arch edit")
	}
	repair(tr.arch, tr.it, tr.dd)
	refineOK()

	// Detail-design edit: only the unit test below goes stale.
	if _, err := e.UpdateNode(tr.dd, nil, &body, nil); err != nil {
		t.Fatal(err)
	}
	_, err = e.Transition(tr.story, "refine")
	wantErr(t, err, tr.ut+" stale: parent "+tr.dd)
	if it, _ := e.Node(tr.it); !it.Fresh {
		t.Fatal("integration test must stay fresh on detail design edit")
	}
	repair(tr.dd, tr.ut)
	refineOK()

	// Cross-cutting edit: dependent arch stale via pin, story downgraded.
	if _, err := e.UpdateNode(cc.ID, nil, &body, nil); err != nil {
		t.Fatal(err)
	}
	if got := status(t, e, tr.story); got != "todo" {
		t.Fatalf("after cc edit: status %s, want todo", got)
	}
	_, err = e.Transition(tr.story, "refine")
	wantErr(t, err, tr.arch+" stale: dependency "+cc.ID+" changed since pin")
	approve(t, e, cc.ID)
	repair(tr.arch, tr.it, tr.dd, tr.ut)
	refineOK()
}

// TestFreshnessReasons_UT_3 proves UT-3 (DD-3 "Freshness computation"): each
// freshness reason string, and approval re-pinning dependency hashes.
func TestFreshnessReasons_UT_3(t *testing.T) {
	e := newMemEngine(t)
	story := mustCreate(t, e, model.KindStory, "", "s", nil)
	arch := mustCreate(t, e, model.KindArch, story.ID, "as", nil)
	body := "edited"

	problems := func(id string) string {
		t.Helper()
		r, err := e.Node(id)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(r.Problems, "\n")
	}

	// Reason: never approved.
	if p := problems(story.ID); !strings.Contains(p, story.ID+" never approved") {
		t.Fatalf("want 'never approved', got %q", p)
	}

	// Reason: content changed since approval (with short hashes).
	approve(t, e, story.ID)
	if _, err := e.UpdateNode(story.ID, nil, &body, nil); err != nil {
		t.Fatal(err)
	}
	if p := problems(story.ID); !strings.Contains(p, "changed since approval (approved ") {
		t.Fatalf("want 'changed since approval', got %q", p)
	}
	approve(t, e, story.ID)

	// Reason: parent changed since approval.
	approve(t, e, arch.ID)
	newBody2 := "edited again"
	if _, err := e.UpdateNode(story.ID, nil, &newBody2, nil); err != nil {
		t.Fatal(err)
	}
	if p := problems(arch.ID); !strings.Contains(p, "stale: parent "+story.ID+" changed since approval") {
		t.Fatalf("want parent-changed reason, got %q", p)
	}
	approve(t, e, story.ID)
	approve(t, e, arch.ID)

	// Reason: dependency changed since pin; approval re-pins.
	cc := mustCreate(t, e, model.KindCrossCutting, "", "cc", nil)
	approve(t, e, cc.ID)
	if err := e.LinkDep(arch.ID, cc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.UpdateNode(cc.ID, nil, &body, nil); err != nil {
		t.Fatal(err)
	}
	if p := problems(arch.ID); !strings.Contains(p, "stale: dependency "+cc.ID+" changed since pin") {
		t.Fatalf("want dependency-changed reason, got %q", p)
	}
	approve(t, e, cc.ID)
	approve(t, e, arch.ID) // helper passes current dep hash: re-pins
	if p := problems(arch.ID); p != "" {
		t.Fatalf("arch must be fresh after re-pinning approval, got %q", p)
	}
}
