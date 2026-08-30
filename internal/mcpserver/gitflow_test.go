package mcpserver_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"trellis/internal/store"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// reportSrc renders a JUnit report; status per case: "" pass, "failure",
// "skipped".
func reportSrc(cases map[string]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><testsuite name="all">`)
	for id, status := range cases {
		name := "Test_" + strings.ReplaceAll(id, "-", "_")
		if status == "" {
			fmt.Fprintf(&b, `<testcase name="%s"/>`, name)
		} else {
			fmt.Fprintf(&b, `<testcase name="%s"><%s/></testcase>`, name, status)
		}
	}
	b.WriteString(`</testsuite>`)
	return b.String()
}

func setReport(t *testing.T, repo string, cases map[string]string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "report-src.xml"), []byte(reportSrc(cases)), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "update report")
}

// TestGitFlowAcceptance_AT_6 proves AT-6 (US-6 "Git flow and test
// evidence") against a real git repository driven through MCP: start/finish
// happy path, each blocking condition (dirty worktree, missing base, missing
// spec evidence, failing test, skipped test, absent report) and conflict
// abort behaviour.
func TestGitFlowAcceptance_AT_6(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	cs := clientFor(t, store.Project{ID: "p1", Name: "test", RepoPath: repo, BaseBranch: "develop",
		LintCmd:   "true",
		TestCmd:   "rm -rf reports && mkdir -p reports && cp report-src.xml reports/report.xml 2>/dev/null || true",
		JUnitGlob: "reports/*.xml"})

	story, at, _, it, _, ut := fullTreeMCP(t, cs)
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"})

	// Dirty worktree blocks start.
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "start"},
		"start blocked", "worktree not clean")
	os.Remove(filepath.Join(repo, "dirty.txt"))

	// Missing base branch blocks start (separate repo without develop).
	repo2 := t.TempDir()
	gitRun(t, repo2, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo2, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo2, "add", ".")
	gitRun(t, repo2, "commit", "-m", "initial")
	cs2 := clientFor(t, store.Project{ID: "p2", Name: "test2", RepoPath: repo2, BaseBranch: "develop"})
	story2, _, _, _, _, _ := fullTreeMCP(t, cs2)
	call(t, cs2, "transition", map[string]any{"story_id": story2, "action": "refine"})
	callErr(t, cs2, "transition", map[string]any{"story_id": story2, "action": "start"},
		"start blocked", `base branch "develop" does not exist`)

	// Happy start: feature branch created from base and checked out.
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "start"})
	if got := gitRun(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/"+story {
		t.Fatalf("on %s, want feature/%s", got, story)
	}

	// Absent report: an error, never an empty pass.
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"},
		"finish blocked", "no junit reports match")

	// Missing spec evidence: every uncovered spec is named.
	setReport(t, repo, map[string]string{at: ""})
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"},
		"test evidence incomplete", it+": no test references", ut+": no test references")

	// Failing and skipped tests count against their spec.
	setReport(t, repo, map[string]string{at: "", it: "failure", ut: "skipped"})
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"},
		it+": test", "failed", ut+": test", "skipped")

	// Merge conflict: aborted, back on the feature branch, develop untouched.
	setReport(t, repo, map[string]string{at: "", it: "", ut: ""})
	gitRun(t, repo, "checkout", "develop")
	if err := os.WriteFile(filepath.Join(repo, "clash.txt"), []byte("develop"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "conflicting base work")
	baseSHA := gitRun(t, repo, "rev-parse", "develop")
	gitRun(t, repo, "checkout", "feature/"+story)
	if err := os.WriteFile(filepath.Join(repo, "clash.txt"), []byte("feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "conflicting feature work")
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"},
		"merge into develop failed", "aborted and returned to feature/"+story)
	if got := gitRun(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/"+story {
		t.Fatalf("after abort on %s, want the feature branch", got)
	}
	if got := gitRun(t, repo, "rev-parse", "develop"); got != baseSHA {
		t.Fatal("develop moved despite aborted merge")
	}

	// Resolve by rebuilding develop without the clash, then finish: merged
	// --no-ff, branch deleted, story done.
	gitRun(t, repo, "branch", "-f", "develop", baseSHA+"~1")
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"})
	st, _ := nodeStatus(t, cs, story)
	if st != "done" {
		t.Fatalf("status = %s, want done", st)
	}
	if got := gitRun(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "develop" {
		t.Fatalf("after finish on %s, want develop", got)
	}
	if out := gitRun(t, repo, "branch", "--list", "feature/"+story); out != "" {
		t.Fatalf("feature branch still exists: %s", out)
	}
	if log := gitRun(t, repo, "log", "--merges", "--oneline", "develop"); !strings.Contains(log, "Merge feature/"+story) {
		t.Fatalf("develop log missing merge commit:\n%s", log)
	}
}
