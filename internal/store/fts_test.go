package store

import (
	"path/filepath"
	"strings"
	"testing"

	"trellis/internal/model"
)

// TestFTSQueryBuilder_UT_20 proves UT-20 (DD-20 "FTS table and query
// builder"): quoting/escaping of operators and quotes, prefix behaviour,
// wordless-term dropping, and index-text composition for stories with ACs.
func TestFTSQueryBuilder_UT_20(t *testing.T) {
	cases := map[string]string{
		"merge gate":      `"merge" AND "gate"*`,
		"one":             `"one"*`,
		`say "hi" now`:    `"say" AND """hi""" AND "now"*`,
		"NEAR(x) OR y":    `"NEAR(x)" AND "OR" AND "y"*`,
		"!!! ???":         "",
		"":                "",
		"  spaced   out ": `"spaced" AND "out"*`,
	}
	for in, want := range cases {
		if got := buildFTSQuery(in); got != want {
			t.Errorf("buildFTSQuery(%q) = %q, want %q", in, got, want)
		}
	}

	// Index text composition: story rows fold in AC text.
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateProject(Project{ID: "p1", Name: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertNode(model.Node{ID: "US-1", ProjectID: "p1", Kind: model.KindStory, Title: "story title", Body: "story body"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertAC("p1", model.AC{ID: "US-1.AC-1", StoryID: "US-1", Given: "a flumph", When: "it hovers", Then: "it telepaths", Position: 1}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"flumph", "telepaths", "story body"} {
		hits, err := st.SearchFTS("p1", q)
		if err != nil || len(hits) != 1 || hits[0].NodeID != "US-1" {
			t.Errorf("SearchFTS(%q): %v %v", q, hits, err)
		}
	}
}

// TestFTSIndexIntegration_IT_19 proves the store half of IT-19 (US-19):
// rebuild-on-open of a pre-FTS database and write-path maintenance incl.
// deletes.
func TestFTSIndexIntegration_IT_19(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(Project{ID: "p1", Name: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertNode(model.Node{ID: "US-1", ProjectID: "p1", Kind: model.KindStory, Title: "quantum ledger", Body: ""}); err != nil {
		t.Fatal(err)
	}

	// Simulate a pre-FTS database: wipe the index, reopen, expect a rebuild.
	if _, err := st.db.Exec(`DELETE FROM specs_fts`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if hits, _ := st.SearchFTS("p1", "quantum"); len(hits) != 1 {
		t.Fatalf("rebuild on open failed: %v", hits)
	}

	// Update reindexes; delete removes the row.
	n, _ := st.GetNode("p1", "US-1")
	n.Title = "plasma ledger"
	if err := st.UpdateNodeContent(n); err != nil {
		t.Fatal(err)
	}
	if hits, _ := st.SearchFTS("p1", "quantum"); len(hits) != 0 {
		t.Fatal("stale index text after update")
	}
	if hits, _ := st.SearchFTS("p1", "plasma"); len(hits) != 1 {
		t.Fatal("update not indexed")
	}
	if err := st.DeleteNode("p1", "US-1"); err != nil {
		t.Fatal(err)
	}
	if hits, _ := st.SearchFTS("p1", "plasma"); len(hits) != 0 {
		t.Fatal("deleted node still indexed")
	}

	// AC lifecycle keeps the story row current.
	if err := st.InsertNode(model.Node{ID: "US-2", ProjectID: "p1", Kind: model.KindStory, Title: "s2", Body: ""}); err != nil {
		t.Fatal(err)
	}
	ac := model.AC{ID: "US-2.AC-1", StoryID: "US-2", Given: "a zorbler", When: "w", Then: "t", Position: 1}
	if err := st.InsertAC("p1", ac); err != nil {
		t.Fatal(err)
	}
	if hits, _ := st.SearchFTS("p1", "zorbler"); len(hits) != 1 {
		t.Fatal("AC insert not indexed")
	}
	ac.Given = "a glimmerbeast"
	if err := st.UpdateAC("p1", ac); err != nil {
		t.Fatal(err)
	}
	if hits, _ := st.SearchFTS("p1", "zorbler"); len(hits) != 0 {
		t.Fatal("AC update left stale text")
	}
	if err := st.DeleteAC("p1", ac.ID); err != nil {
		t.Fatal(err)
	}
	if hits, _ := st.SearchFTS("p1", "glimmerbeast"); len(hits) != 0 {
		t.Fatal("AC delete left stale text")
	}
	if !strings.Contains(buildFTSQuery("sanity"), "sanity") {
		t.Fatal("query builder sanity")
	}
}
