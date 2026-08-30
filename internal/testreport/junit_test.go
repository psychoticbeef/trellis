package testreport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleXML = `<?xml version="1.0"?>
<testsuites>
  <testsuite name="pkg">
    <testcase classname="pkg" name="TestLogin_AT_1"/>
    <testcase classname="pkg" name="TestTokens_UT_3"/>
    <testcase classname="pkg" name="TestTokens_UT_31"/>
    <testcase classname="pkg" name="TestBroken_IT_1"><failure message="boom"/></testcase>
    <testcase classname="pkg" name="TestSkipped_UT_4"><skipped/></testcase>
    <testsuite name="nested">
      <testcase classname="pkg.sub" name="deep UT-5 test"/>
    </testsuite>
  </testsuite>
</testsuites>`

func writeReport(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.xml"), []byte(sampleXML), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestParseAndMatch_UT_6(t *testing.T) {
	dir := writeReport(t)
	cases, err := ParseGlob(dir, "*.xml")
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 6 {
		t.Fatalf("got %d cases, want 6 (nested suites must be collected)", len(cases))
	}
	if got := Match("UT-3", cases); len(got) != 1 || got[0].Name != "TestTokens_UT_3" {
		t.Fatalf("UT-3 must match exactly TestTokens_UT_3, not UT_31; got %v", got)
	}
	if got := Match("UT-5", cases); len(got) != 1 {
		t.Fatalf("UT-5 in nested suite not found: %v", got)
	}
}

func TestVerify_UT_6(t *testing.T) {
	dir := writeReport(t)
	cases, err := ParseGlob(dir, "*.xml")
	if err != nil {
		t.Fatal(err)
	}
	problems := Verify([]string{"AT-1", "IT-1", "UT-4", "UT-9"}, cases)
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"IT-1", "failed", "UT-4", "skipped", "UT-9", "no test references"} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "AT-1:") {
		t.Errorf("AT-1 passed but was reported:\n%s", joined)
	}
}

func TestEmptyGlobIsError_UT_6(t *testing.T) {
	if _, err := ParseGlob(t.TempDir(), "*.xml"); err == nil {
		t.Fatal("zero matching reports must be an error, not an empty pass")
	}
}
