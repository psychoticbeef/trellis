package model

import "testing"

func TestContentHashDeterministic_UT_2(t *testing.T) {
	n := Node{Kind: KindStory, Title: "t", Body: "b"}
	acs := []AC{{ID: "US-1.AC-2", Given: "g2", When: "w2", Then: "t2"}, {ID: "US-1.AC-1", Given: "g", When: "w", Then: "t"}}
	h1 := ContentHash(&n, acs)
	// AC order must not matter.
	h2 := ContentHash(&n, []AC{acs[1], acs[0]})
	if h1 != h2 {
		t.Fatalf("hash depends on AC order: %s vs %s", h1, h2)
	}
}

func TestContentHashChangesOnACEdit_UT_2(t *testing.T) {
	n := Node{Kind: KindStory, Title: "t"}
	h1 := ContentHash(&n, []AC{{ID: "US-1.AC-1", Given: "g", When: "w", Then: "t"}})
	h2 := ContentHash(&n, []AC{{ID: "US-1.AC-1", Given: "g", When: "w", Then: "CHANGED"}})
	if h1 == h2 {
		t.Fatal("editing an AC must change the story hash")
	}
}

func TestCoversOrderIrrelevant_UT_2(t *testing.T) {
	a := Node{Kind: KindAcceptanceTest, Title: "t", Covers: []string{"A", "B"}}
	b := Node{Kind: KindAcceptanceTest, Title: "t", Covers: []string{"B", "A"}}
	if ContentHash(&a, nil) != ContentHash(&b, nil) {
		t.Fatal("covers order must not affect the hash")
	}
}

func TestParentRules(t *testing.T) {
	if _, needs := ParentKind(KindStory); needs {
		t.Fatal("story must be a root")
	}
	if _, needs := ParentKind(KindCrossCutting); needs {
		t.Fatal("cross_cutting must be a root")
	}
	if p, _ := ParentKind(KindUnitTest); p != KindDetailDesign {
		t.Fatalf("unit_test parent = %s, want detail_design", p)
	}
}
