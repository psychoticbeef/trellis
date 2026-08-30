package testreport

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCoverageParsers_UT_33 proves UT-33 (DD-33 "Coverage parsing and
// plumbing"): both formats, mixed globs, malformed input, aggregation and
// worst-file ordering with ties.
func TestCoverageParsers_UT_33(t *testing.T) {
	dir := t.TempDir()
	lcov := `TN:
SF:src/a.ts
DA:1,1
DA:2,0
DA:3,4
end_of_record
SF:src/b.ts
DA:1,0
DA:2,0
end_of_record
`
	gocov := `mode: set
pkg/x.go:3.10,5.2 2 1
pkg/x.go:7.1,9.2 2 0
pkg/y.go:1.1,2.2 4 7
`
	if err := os.WriteFile(filepath.Join(dir, "lcov.info"), []byte(lcov), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cover.out"), []byte(gocov), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := ParseCoverage(dir, "*")
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]FileCov{}
	for _, f := range files {
		byPath[f.Path] = f
	}
	if f := byPath["src/a.ts"]; f.Covered != 2 || f.Total != 3 {
		t.Fatalf("a.ts = %+v", f)
	}
	if f := byPath["src/b.ts"]; f.Covered != 0 || f.Total != 2 {
		t.Fatalf("b.ts = %+v", f)
	}
	if f := byPath["pkg/x.go"]; f.Covered != 2 || f.Total != 4 {
		t.Fatalf("x.go = %+v", f)
	}
	if f := byPath["pkg/y.go"]; f.Covered != 4 || f.Total != 4 {
		t.Fatalf("y.go = %+v", f)
	}

	// Aggregation and ordering: b.ts (0%) before x.go (50%) before a.ts
	// (66.7%); y.go (100%) last; ties break by path.
	total, worst := Summarize(files, 10)
	if total < 61 || total > 62 { // 8 covered of 13
		t.Fatalf("total = %f", total)
	}
	if worst[0].Path != "src/b.ts" || worst[1].Path != "pkg/x.go" || worst[2].Path != "src/a.ts" {
		t.Fatalf("ordering: %+v", worst)
	}

	// Malformed and empty inputs error (the caller degrades to a notice).
	bad := t.TempDir()
	os.WriteFile(filepath.Join(bad, "x.info"), []byte("DA:garbage"), 0o644)
	if _, err := ParseCoverage(bad, "*"); err == nil {
		t.Fatal("unrecognized format must error")
	}
	empty := t.TempDir()
	os.WriteFile(filepath.Join(empty, "x.info"), []byte(""), 0o644)
	if _, err := ParseCoverage(empty, "*"); err == nil {
		t.Fatal("empty file must error")
	}
	if _, err := ParseCoverage(t.TempDir(), "*.nope"); err == nil {
		t.Fatal("no matching files must error")
	}
}
