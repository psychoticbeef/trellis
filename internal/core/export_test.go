package core

import (
	"path/filepath"
	"strings"
	"testing"

	"trellis/internal/model"
	"trellis/internal/store"
)

// TestExportFormat_UT_26 proves UT-26 (DD-26 "Export document and
// importer"): yaml round-tripping of hostile content, version rejection,
// counter preservation.
func TestExportFormat_UT_26(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateProject(store.Project{ID: "p1", Name: "orig", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
	hostile := "line one\nline two: with colon\n\ttabbed \"quotes\" & 🌱 unicode\n- looks: like yaml"
	s, err := e.CreateNode("story", "", "title: with colon", hostile, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddAC(s.ID, "a 'given'", "w\nmultiline", "t"); err != nil {
		t.Fatal(err)
	}
	position := 0
	activity, err := e.CreateNodeWithPosition(model.KindActivity, "", "activity: zero", "", nil, &position)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetStoryActivity("p1", s.ID, activity.ID); err != nil {
		t.Fatal(err)
	}
	doc, err := e.ExportYAML()
	if err != nil {
		t.Fatal(err)
	}

	// Import the hostile content and compare the round trip.
	if err := Import(st, []byte(doc), store.Project{ID: "p2", Name: "orig", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e2, err := NewEngine(st, "p2")
	if err != nil {
		t.Fatal(err)
	}
	doc2, err := e2.ExportYAML()
	if err != nil {
		t.Fatal(err)
	}
	if doc != doc2 {
		t.Fatalf("round trip diverged:\n--- original ---\n%s\n--- reimport ---\n%s", doc, doc2)
	}
	if !strings.Contains(doc, "activities:") || !strings.Contains(doc, "position: 0") || !strings.Contains(doc, "activity: UA-1") {
		t.Fatalf("story placement missing from export:\n%s", doc)
	}

	// Counters preserved: new story and activity ids continue, never reuse.
	e2b, _ := NewEngine(st, "p2")
	n, err := e2b.CreateNode("story", "", "next", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != "US-2" {
		t.Fatalf("counter not preserved: new story id %s, want US-2", n.ID)
	}
	a, err := e2b.CreateNode(model.KindActivity, "", "next activity", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "UA-2" {
		t.Fatalf("activity counter not preserved: new activity id %s, want UA-2", a.ID)
	}

	// Version rejection.
	bad := strings.Replace(doc, "trellis_export: 1", "trellis_export: 99", 1)
	if err := Import(st, []byte(bad), store.Project{ID: "p3", Name: "x"}); err == nil ||
		!strings.Contains(err.Error(), "unsupported export version") {
		t.Fatalf("version rejection missing: %v", err)
	}
	if err := Import(st, []byte("not: [valid"), store.Project{ID: "p4", Name: "x"}); err == nil {
		t.Fatal("invalid yaml must error")
	}
}
