package core

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"trellis/internal/gitops"
	"trellis/internal/model"
	"trellis/internal/testreport"
)

// AuditReport separates hard violations (spec/reality drift) from purely
// informational findings. Unclaimed non-meta files are violations; meta files
// and unbound tests remain non-gating.
type AuditReport struct {
	Violations []string `json:"violations"`
	Infos      []string `json:"infos"`
}

// testSpecIDRe finds test-binding spec references in test names. Story ids
// (US-n) are deliberately excluded: stories are not test-bound.
var testSpecIDRe = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9])((?:AT|IT|UT)[-_]\d+)(?:[^0-9]|$)`)

// metaFileRe excludes common repository meta files from unclaimed-file
// violations: they legitimately belong to no feature.
var metaFileRe = regexp.MustCompile(`(?i)(^|/)(readme[^/]*|license[^/]*|contributing[^/]*|changelog[^/]*|features\.md|trellis-specs\.yaml|agents\.md|package(-lock)?\.json|go\.(mod|sum)|tsconfig[^/]*\.json|[^/]*\.config\.[jt]s|\.[^/]*)$`)

// isMetaFile: meta names plus anything inside a dot-directory (.pi, .github…).
func isMetaFile(path string) bool {
	return metaFileRe.MatchString(path) || strings.HasPrefix(path, ".") || strings.Contains(path, "/.")
}

func unclaimedFiles(tracked string, declared []string) []string {
	var unclaimed []string
	for _, f := range strings.Split(tracked, "\n") {
		f = strings.TrimSpace(f)
		if f == "" || isMetaFile(f) {
			continue
		}
		claimed := false
		for _, d := range declared {
			if PathCovers(d, f) {
				claimed = true
				break
			}
		}
		if !claimed {
			unclaimed = append(unclaimed, f)
		}
	}
	sort.Strings(unclaimed)
	return unclaimed
}

// Audit validates spec and reality in both directions, repo-wide. It runs
// the test command once, never mutates, and returns every finding.
func (e *Engine) Audit() (AuditReport, error) {
	rep := AuditReport{Violations: []string{}, Infos: []string{}}
	if e.Project.RepoPath == "" || e.Project.TestCmd == "" || e.Project.JUnitGlob == "" {
		return rep, fmt.Errorf("audit needs repo, test_cmd and junit_glob configured")
	}
	root := e.Project.RepoPath
	testOut, testErr := runShell(root, e.Project.TestCmd)
	cases, parseErr := testreport.ParseGlob(root, e.Project.JUnitGlob)
	if parseErr != nil {
		if testErr != nil {
			return rep, fmt.Errorf("audit: test command failed (%s):\n%s", e.Project.TestCmd, tail(testOut, 20))
		}
		return rep, fmt.Errorf("audit: %w", parseErr)
	}

	stories, err := e.st.ListNodesByKind(e.pid(), model.KindStory)
	if err != nil {
		return rep, err
	}
	liveSpecIDs := map[string]bool{}
	all, err := e.st.ListNodes(e.pid())
	if err != nil {
		return rep, err
	}
	for _, n := range all {
		if model.TestSpecKinds[n.Kind] {
			liveSpecIDs[strings.ToUpper(n.ID)] = true
		}
	}

	// Forward, repo-wide: every done story's evidence and paths still hold.
	var declared []string
	for _, s := range stories {
		declared = append(declared, s.Paths...)
		if s.Status != model.StatusDone {
			continue
		}
		nodes, err := e.treeNodes(s.ID)
		if err != nil {
			return rep, err
		}
		var specIDs []string
		for _, n := range nodes {
			if model.TestSpecKinds[n.Kind] {
				specIDs = append(specIDs, n.ID)
			}
		}
		for _, p := range testreport.Verify(specIDs, cases) {
			rep.Violations = append(rep.Violations, fmt.Sprintf("done story %s: %s", s.ID, p))
		}
		for _, missing := range e.missingPathsIn(s, root) {
			rep.Violations = append(rep.Violations, fmt.Sprintf("done story %s: declared path %s no longer exists", s.ID, missing))
		}
	}

	// Backward, tests: spec references must resolve; unbound tests are info.
	unbound := 0
	var unboundSample []string
	seenBadRef := map[string]bool{}
	for _, c := range cases {
		name := c.FullName()
		refs := testSpecIDRe.FindAllStringSubmatch(name, -1)
		if len(refs) == 0 {
			unbound++
			if len(unboundSample) < 5 {
				unboundSample = append(unboundSample, name)
			}
			continue
		}
		for _, m := range refs {
			id := strings.ToUpper(strings.ReplaceAll(m[1], "_", "-"))
			if !liveSpecIDs[id] && !seenBadRef[id+name] {
				seenBadRef[id+name] = true
				rep.Violations = append(rep.Violations, fmt.Sprintf("test %s references nonexistent spec %s", name, id))
			}
		}
	}
	if unbound > 0 {
		rep.Infos = append(rep.Infos, fmt.Sprintf("%d test(s) reference no spec (unbound), e.g. %s", unbound, strings.Join(unboundSample, "; ")))
	}

	// Backward, files: every unclaimed non-meta file is one exhaustive violation.
	g := gitops.Git{Dir: root}
	tracked, err := g.Run("ls-files")
	if err == nil {
		unclaimed := unclaimedFiles(tracked, declared)
		if len(unclaimed) > 0 {
			rep.Violations = append(rep.Violations, fmt.Sprintf("%d file(s) claimed by no story: %s", len(unclaimed), strings.Join(unclaimed, ", ")))
		}
	}
	e.st.AppendEvent(e.pid(), "audit", "", fmt.Sprintf("%d violation(s), %d info(s)", len(rep.Violations), len(rep.Infos)))
	return rep, nil
}
