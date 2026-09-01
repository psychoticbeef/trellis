package core

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAuditUnit_UT_34 proves UT-34 (DD-35 "Audit mechanics"): spec-id
// extraction boundaries, meta-file exclusion, envelope serialization.
func TestAuditUnit_UT_34(t *testing.T) {
	extract := func(name string) []string {
		var out []string
		for _, m := range testSpecIDRe.FindAllStringSubmatch(name, -1) {
			out = append(out, strings.ToUpper(strings.ReplaceAll(m[1], "_", "-")))
		}
		return out
	}
	if got := extract("pkg::TestFoo_UT_3"); len(got) != 1 || got[0] != "UT-3" {
		t.Fatalf("extract UT_3: %v", got)
	}
	if got := extract("suite AT-12 and IT_4 combined"); len(got) != 2 {
		t.Fatalf("multi extract: %v", got)
	}
	// Mirrors the finish verifier's boundary semantics: a trailing letter
	// still binds (UT_31x -> UT-31), a leading letter does not.
	if got := extract("TestBrutal_UT_31x"); len(got) != 1 || got[0] != "UT-31" {
		t.Fatalf("trailing-letter boundary: %v", got)
	}
	if got := extract("TestxUT_3"); len(got) != 0 {
		t.Fatalf("leading-letter boundary: %v", got)
	}
	if got := extract("Test_US_1_story"); len(got) != 0 {
		t.Fatalf("US ids are not test-bound: %v", got)
	}
	if got := extract("nothing here"); len(got) != 0 {
		t.Fatalf("no refs: %v", got)
	}

	for _, meta := range []string{"README.md", "docs/README.txt", "LICENSE", "package.json",
		"go.mod", ".gitignore", "vitest.config.ts", "FEATURES.md", "AGENTS.md", ".pi/settings.json"} {
		if !isMetaFile(meta) {
			t.Errorf("meta file %s not excluded", meta)
		}
	}
	for _, src := range []string{"src/index.ts", "internal/core/engine.go", "cmd/main.go"} {
		if isMetaFile(src) {
			t.Errorf("source file %s wrongly excluded", src)
		}
	}

	b, _ := json.Marshal(AuditReport{Violations: []string{}, Infos: []string{}})
	if string(b) != `{"violations":[],"infos":[]}` {
		t.Fatalf("envelope: %s", b)
	}
}

// TestUnclaimedFileViolation_UT_40 proves UT-40 (DD-40 "Unclaimed-file
// violation aggregation"): complete sorted unclaimed set, path coverage, and
// unchanged meta-file exclusions.
func TestUnclaimedFileViolation_UT_40(t *testing.T) {
	tracked := strings.Join([]string{
		"z/orphan.go",
		"README.md",
		"pkg/claimed.go",
		".github/workflows/test.yml",
		"a/orphan.go",
		"go.mod",
	}, "\n")
	got := unclaimedFiles(tracked, []string{"pkg"})
	want := []string{"a/orphan.go", "z/orphan.go"}
	if len(got) != len(want) || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("unclaimed files = %v, want %v", got, want)
	}
	if got := unclaimedFiles("README.md\ngo.mod\n", nil); len(got) != 0 {
		t.Fatalf("meta files became unclaimed: %v", got)
	}
}
