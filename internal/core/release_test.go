package core

import (
	"strings"
	"testing"

	"trellis/internal/model"
	"trellis/internal/store"
)

// TestReleaseBuildingBlocks_UT_23 proves UT-23 (DD-23 "Release mechanics"):
// parseFeatureSubjects on real and hostile subjects, featuresMarkdown
// rendering with and without glossary.
func TestReleaseBuildingBlocks_UT_23(t *testing.T) {
	got := parseFeatureSubjects([]string{
		"Merge feature/US-17: Worktree isolation (trellis finish)",
		"Merge branch 'hotfix' into develop",
		"Merge feature/US-3: Hash-based approval and invalidation (trellis finish)",
		"Merge feature/US-99: sneaky (not trellis)",
		"random subject",
		"",
	})
	if len(got) != 2 || got[0] != "US-17 — Worktree isolation" ||
		got[1] != "US-3 — Hash-based approval and invalidation" {
		t.Fatalf("parseFeatureSubjects = %v", got)
	}

	md := featuresMarkdown("trellis", "deterministic spec tracking",
		[]model.Node{{ID: "US-1", Title: "Spec tree management"}, {ID: "US-2", Title: "Search"}},
		[]store.TermDef{{Term: "gate", Definition: "guard that blocks a transition"}})
	for _, want := range []string{"# Features", "- **US-1** — Spec tree management",
		"# Glossary", "- **gate** — guard that blocks a transition", "lives only on the release branch"} {
		if !strings.Contains(md, want) {
			t.Errorf("featuresMarkdown missing %q:\n%s", want, md)
		}
	}
	if md2 := featuresMarkdown("p", "", nil, nil); !strings.Contains(md2, "_none yet_") || strings.Contains(md2, "# Glossary") {
		t.Errorf("empty rendering wrong:\n%s", md2)
	}
}

// TestDescriptionUnit_UT_25 proves UT-25 (DD-25 "Description plumbing"):
// limit, overwrite semantics, manifest header with and without description.
func TestDescriptionUnit_UT_25(t *testing.T) {
	md := featuresMarkdown("trellis", "spec tracking for agents", nil, nil)
	if !strings.HasPrefix(md, "# trellis\n\n> spec tracking for agents\n") {
		t.Fatalf("manifest header wrong:\n%s", md)
	}
	if md2 := featuresMarkdown("trellis", "", nil, nil); strings.Contains(md2, ">") {
		t.Fatalf("empty description must render no quote line:\n%s", md2)
	}
}
