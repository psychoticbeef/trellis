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

	// An advanced base — even a conflicting one — is caught by the up-to-date
	// gate before any merge is attempted (US-9); develop is never touched.
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
		"finish blocked", "feature/"+story+" is behind develop")
	if got := gitRun(t, repo, "rev-parse", "develop"); got != baseSHA {
		t.Fatal("develop moved although finish was blocked")
	}

	// Catch up on the feature branch, resolving the conflict there — the
	// merge into develop afterwards is conflict-free by construction.
	cmd := exec.Command("git", "merge", "develop", "-m", "catch up")
	cmd.Dir = repo
	_ = cmd.Run() // conflict expected
	if err := os.WriteFile(filepath.Join(repo, "clash.txt"), []byte("resolved"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "resolve conflict on feature branch")
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

// TestLivingDoneTreeAcceptance_AT_10 proves AT-10 (US-8 "Done trees are
// living context"): done trees stay editable through MCP, stale markers flag
// unreviewed changes, status never reopens, re-approval clears the markers.
func TestLivingDoneTreeAcceptance_AT_10(t *testing.T) {
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
	story, at, arch, it, dd, ut := fullTreeMCP(t, cs)
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"})
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "start"})
	setReport(t, repo, map[string]string{at: "", it: "", ut: ""})
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"})

	// Edits on the done tree succeed; stale markers appear; status stays done.
	call(t, cs, "update_node", map[string]any{"id": dd, "body": "corrected to match reality"})
	call(t, cs, "update_acceptance_criterion", map[string]any{"ac_id": story + ".AC-1", "then": "corrected outcome"})
	st, _ := nodeStatus(t, cs, story)
	if st != "done" {
		t.Fatalf("status = %s, done must never reopen automatically", st)
	}
	tree := call(t, cs, "get_tree", map[string]any{"story_id": story})
	problems := fmt.Sprintf("%v", tree["blocking_problems"])
	for _, want := range []string{dd + " changed since approval", ut + " stale: parent " + dd, story + " changed since approval"} {
		if !strings.Contains(problems, want) {
			t.Errorf("stale markers missing %q:\n%s", want, problems)
		}
	}

	// Creating and deleting nodes in the done tree works.
	extra := call(t, cs, "create_node", map[string]any{"kind": "unit_test", "parent_id": dd, "title": "extra"})["id"].(string)
	call(t, cs, "delete_node", map[string]any{"id": extra})

	// Re-approving the stale chain clears every marker.
	for _, id := range []string{story, at, arch, it, dd, ut} {
		approveMCP(t, cs, id)
	}
	tree = call(t, cs, "get_tree", map[string]any{"story_id": story})
	if problems := tree["blocking_problems"].([]any); len(problems) != 0 {
		t.Fatalf("markers must clear after re-approval, got %v", problems)
	}
	if st, _ := nodeStatus(t, cs, story); st != "done" {
		t.Fatal("status must still be done after re-approval")
	}
}

// TestUpToDateMergeGateAcceptance_AT_11 proves AT-11 (US-9 "Merge gate: test
// what will merge") through MCP: an advanced base blocks finish with the
// catch-up instruction; after merging the base into the feature branch the
// same finish succeeds.
func TestUpToDateMergeGateAcceptance_AT_11(t *testing.T) {
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
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "start"})
	setReport(t, repo, map[string]string{at: "", it: "", ut: ""})

	gitRun(t, repo, "checkout", "develop")
	if err := os.WriteFile(filepath.Join(repo, "unrelated.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "base moved")
	gitRun(t, repo, "checkout", "feature/"+story)

	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"},
		"finish blocked", "feature/"+story+" is behind develop", "merge or rebase develop into")

	gitRun(t, repo, "merge", "develop", "-m", "catch up")
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"})
	if st, _ := nodeStatus(t, cs, story); st != "done" {
		t.Fatalf("status = %s, want done", st)
	}
}

// TestAbortAcceptance_AT_12 proves AT-12 (US-10 "Abort transition") through
// MCP: dirty worktree blocks, clean abort discards the branch and returns to
// refined, abort from refined is rejected, restart begins fresh from base.
func TestAbortAcceptance_AT_12(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	cs := clientFor(t, store.Project{ID: "p1", Name: "test", RepoPath: repo, BaseBranch: "develop"})
	story, _, _, _, _, _ := fullTreeMCP(t, cs)
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"})
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "start"})

	if err := os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("w"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "work")

	// Dirty worktree blocks abort.
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "abort"},
		"abort blocked", "never silently destroyed")
	os.Remove(filepath.Join(repo, "dirty.txt"))

	// Clean abort: branch gone, base checked out, story refined.
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "abort"})
	if st, _ := nodeStatus(t, cs, story); st != "refined" {
		t.Fatalf("status = %s, want refined", st)
	}
	if got := gitRun(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "develop" {
		t.Fatalf("on %s, want develop", got)
	}
	if out := gitRun(t, repo, "branch", "--list", "feature/"+story); out != "" {
		t.Fatalf("feature branch still exists: %s", out)
	}

	// Abort from refined is rejected; restart begins fresh from base.
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "abort"},
		"abort blocked", `abort requires "in_progress"`)
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "start"})
	if _, err := os.Stat(filepath.Join(repo, "wip.txt")); err == nil {
		t.Fatal("discarded work survived into the fresh branch")
	}
}
