// Package testreport parses JUnit XML reports and matches test cases to
// trellis test-spec ids. JUnit XML is the lingua franca of test runners
// (pytest, go test via gotestsum, cargo-nextest, vitest, ...), which keeps
// trellis language-agnostic.
package testreport

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Case struct {
	Class   string
	Name    string
	Failed  bool
	Skipped bool
}

func (c Case) FullName() string {
	if c.Class == "" {
		return c.Name
	}
	return c.Class + "::" + c.Name
}

type junitTestCase struct {
	Name      string     `xml:"name,attr"`
	ClassName string     `xml:"classname,attr"`
	Failures  []xml.Name `xml:"failure"`
	Errors    []xml.Name `xml:"error"`
	Skipped   []xml.Name `xml:"skipped"`
}

type junitSuite struct {
	Suites []junitSuite    `xml:"testsuite"`
	Cases  []junitTestCase `xml:"testcase"`
}

type junitRoot struct {
	XMLName xml.Name
	Suites  []junitSuite    `xml:"testsuite"`
	Cases   []junitTestCase `xml:"testcase"`
}

// ParseGlob reads all report files matching glob (relative to dir) and
// returns every test case found. A glob matching zero files is an error:
// a silently missing report must never look like a passing run.
func ParseGlob(dir, glob string) ([]Case, error) {
	pattern := glob
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(dir, glob)
	}
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("bad junit glob %q: %w", glob, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no junit reports match %q: check junit_glob config and that the test command writes reports", glob)
	}
	var cases []Case
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var root junitRoot
		if err := xml.Unmarshal(data, &root); err != nil {
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		cases = append(cases, collect(root.Cases)...)
		for _, s := range root.Suites {
			cases = append(cases, collectSuite(s)...)
		}
	}
	return cases, nil
}

func collectSuite(s junitSuite) []Case {
	out := collect(s.Cases)
	for _, sub := range s.Suites {
		out = append(out, collectSuite(sub)...)
	}
	return out
}

func collect(tcs []junitTestCase) []Case {
	out := make([]Case, 0, len(tcs))
	for _, tc := range tcs {
		out = append(out, Case{
			Class:   tc.ClassName,
			Name:    tc.Name,
			Failed:  len(tc.Failures) > 0 || len(tc.Errors) > 0,
			Skipped: len(tc.Skipped) > 0,
		})
	}
	return out
}

// specPattern matches a spec id like "UT-3" inside a test name, accepting
// "UT-3" or "UT_3" (Go test functions cannot contain hyphens), guarded by
// boundaries so UT-3 does not match UT-31.
func specPattern(specID string) *regexp.Regexp {
	parts := strings.SplitN(specID, "-", 2)
	quoted := regexp.QuoteMeta(parts[0])
	if len(parts) == 2 {
		quoted += `[-_]` + regexp.QuoteMeta(parts[1])
	}
	return regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])` + quoted + `([^0-9]|$)`)
}

// Match returns the test cases whose full name references the spec id.
func Match(specID string, cases []Case) []Case {
	re := specPattern(specID)
	var out []Case
	for _, c := range cases {
		if re.MatchString(c.FullName()) {
			out = append(out, c)
		}
	}
	return out
}

// Verify checks that every spec id is covered by at least one test case and
// that all matching cases passed. It returns the full list of violations,
// not just the first, so the caller can fix everything in one pass.
func Verify(specIDs []string, cases []Case) []string {
	var problems []string
	for _, id := range specIDs {
		matched := Match(id, cases)
		if len(matched) == 0 {
			problems = append(problems, fmt.Sprintf("%s: no test references this spec (test name must contain %q)", id, id))
			continue
		}
		for _, c := range matched {
			switch {
			case c.Failed:
				problems = append(problems, fmt.Sprintf("%s: test %s failed", id, c.FullName()))
			case c.Skipped:
				problems = append(problems, fmt.Sprintf("%s: test %s was skipped; a skipped test proves nothing", id, c.FullName()))
			}
		}
	}
	return problems
}
