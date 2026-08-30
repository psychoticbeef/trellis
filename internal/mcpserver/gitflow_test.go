package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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

func setReport(t *testing.T, dir string, cases map[string]string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "report-src.xml"), []byte(reportSrc(cases)), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-m", "update report")
}

func wtOf(repo, story string) string {
	return filepath.Join(repo, ".trellis-worktrees", story)
}

// TestGitFlowAcceptance_AT_6 proves AT-6 (US-6 "Git flow and test
// evidence") against a real git repository driven through MCP: start/finish
// happy path, each blocking condition (dirty worktree, missing base, missing
// spec evidence, failing test, skipped test, absent report) and conflict
// abort behaviour.
func TestGitFlowAcceptance_AT_6(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
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

	// Missing base branch blocks start (separate repo without develop).
	repo2 := t.TempDir()
	gitRun(t, repo2, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo2, ".gitignore"), []byte(".trellis-worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo2, "add", ".")
	gitRun(t, repo2, "commit", "-m", "initial")
	cs2 := clientFor(t, store.Project{ID: "p2", Name: "test2", RepoPath: repo2, BaseBranch: "develop"})
	story2, _, _, _, _, _ := fullTreeMCP(t, cs2)
	call(t, cs2, "transition", map[string]any{"story_id": story2, "action": "refine"})
	callErr(t, cs2, "transition", map[string]any{"story_id": story2, "action": "start"},
		"start blocked", `base branch "develop" does not exist`)

	// Happy start: story worktree on the feature branch, main stays on base.
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "start"})
	wt := wtOf(repo, story)
	if got := gitRun(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/"+story {
		t.Fatalf("worktree on %s, want feature/%s", got, story)
	}
	if got := gitRun(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "develop" {
		t.Fatalf("main worktree on %s, want develop", got)
	}

	// Absent report: an error, never an empty pass.
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"},
		"finish blocked", "no junit reports match")

	// Missing spec evidence: every uncovered spec is named.
	setReport(t, wt, map[string]string{at: ""})
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"},
		"test evidence incomplete", it+": no test references", ut+": no test references")

	// Failing and skipped tests count against their spec.
	setReport(t, wt, map[string]string{at: "", it: "failure", ut: "skipped"})
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"},
		it+": test", "failed", ut+": test", "skipped")

	// An advanced base — even a conflicting one — is caught by the up-to-date
	// gate before any merge is attempted (US-9); develop is never touched.
	setReport(t, wt, map[string]string{at: "", it: "", ut: ""})
	if err := os.WriteFile(filepath.Join(repo, "clash.txt"), []byte("develop"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "conflicting base work")
	baseSHA := gitRun(t, repo, "rev-parse", "develop")
	if err := os.WriteFile(filepath.Join(wt, "clash.txt"), []byte("feature"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wt, "add", ".")
	gitRun(t, wt, "commit", "-m", "conflicting feature work")
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"},
		"finish blocked", "feature/"+story+" is behind develop")
	if got := gitRun(t, repo, "rev-parse", "develop"); got != baseSHA {
		t.Fatal("develop moved although finish was blocked")
	}

	// Catch up inside the story worktree, resolving the conflict there — the
	// merge into develop afterwards is conflict-free by construction.
	cmd := exec.Command("git", "merge", "develop", "-m", "catch up")
	cmd.Dir = wt
	_ = cmd.Run() // conflict expected
	if err := os.WriteFile(filepath.Join(wt, "clash.txt"), []byte("resolved"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wt, "add", ".")
	gitRun(t, wt, "commit", "-m", "resolve conflict on feature branch")
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
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
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
	setReport(t, wtOf(repo, story), map[string]string{at: "", it: "", ut: ""})
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
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
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
	wt := wtOf(repo, story)
	setReport(t, wt, map[string]string{at: "", it: "", ut: ""})

	if err := os.WriteFile(filepath.Join(repo, "unrelated.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "base moved")

	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"},
		"finish blocked", "feature/"+story+" is behind develop", "merge or rebase develop into")

	gitRun(t, wt, "merge", "develop", "-m", "catch up")
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
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	cs := clientFor(t, store.Project{ID: "p1", Name: "test", RepoPath: repo, BaseBranch: "develop"})
	story, _, _, _, _, _ := fullTreeMCP(t, cs)
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"})
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "start"})
	wt := wtOf(repo, story)

	if err := os.WriteFile(filepath.Join(wt, "wip.txt"), []byte("w"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wt, "add", ".")
	gitRun(t, wt, "commit", "-m", "work")

	// Dirty story worktree blocks abort.
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "abort"},
		"abort blocked", "never silently destroyed")
	os.Remove(filepath.Join(wt, "dirty.txt"))

	// Clean abort: worktree and branch gone, story refined, main untouched.
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "abort"})
	if st, _ := nodeStatus(t, cs, story); st != "refined" {
		t.Fatalf("status = %s, want refined", st)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Fatal("story worktree still exists after abort")
	}
	if out := gitRun(t, repo, "branch", "--list", "feature/"+story); out != "" {
		t.Fatalf("feature branch still exists: %s", out)
	}

	// Abort from refined is rejected; restart begins fresh from base.
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "abort"},
		"abort blocked", `abort requires "in_progress"`)
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "start"})
	if _, err := os.Stat(filepath.Join(wtOf(repo, story), "wip.txt")); err == nil {
		t.Fatal("discarded work survived into the fresh worktree")
	}
}

// TestSequencingAcceptance_AT_13 proves AT-13 (US-11 "Story sequencing
// dependencies") through MCP: linking to an unrefined story, start blocked
// naming the dependency, unblocked once it is done, cycle rejected.
func TestSequencingAcceptance_AT_13(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	cs := clientFor(t, store.Project{ID: "p1", Name: "test", RepoPath: repo, BaseBranch: "develop",
		LintCmd:   "true",
		TestCmd:   "rm -rf reports && mkdir -p reports && cp report-src.xml reports/report.xml 2>/dev/null || true",
		JUnitGlob: "reports/*.xml"})

	a, atA, _, itA, _, utA := fullTreeMCP(t, cs) // prerequisite, still todo
	b, _, _, _, _, _ := fullTreeMCP(t, cs)

	// Sequencing link onto a todo story works.
	call(t, cs, "link_dependency", map[string]any{"node_id": b, "target_id": a})
	call(t, cs, "transition", map[string]any{"story_id": b, "action": "refine"})
	callErr(t, cs, "transition", map[string]any{"story_id": b, "action": "start"},
		"start blocked: unfinished dependencies", a+" (todo)")

	// Cycle rejected.
	callErr(t, cs, "link_dependency", map[string]any{"node_id": a, "target_id": b},
		"sequencing cycle")

	// Drive the prerequisite to done, then start proceeds.
	call(t, cs, "transition", map[string]any{"story_id": a, "action": "refine"})
	call(t, cs, "transition", map[string]any{"story_id": a, "action": "start"})
	setReport(t, wtOf(repo, a), map[string]string{atA: "", itA: "", utA: ""})
	call(t, cs, "transition", map[string]any{"story_id": a, "action": "finish"})
	call(t, cs, "transition", map[string]any{"story_id": b, "action": "start"})
	if st, _ := nodeStatus(t, cs, b); st != "in_progress" {
		t.Fatalf("status = %s, want in_progress", st)
	}
}

// TestPathPointerAcceptance_AT_15 proves AT-15 (US-13 "Spec-to-code paths")
// through MCP: declare paths, read them back, finish blocked on a missing
// path, green after fixing, reverse lookup for a nested file.
func TestPathPointerAcceptance_AT_15(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "pkg/auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "pkg/auth/auth.go"), []byte("package auth"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	cs := clientFor(t, store.Project{ID: "p1", Name: "test", RepoPath: repo, BaseBranch: "develop",
		LintCmd:   "true",
		TestCmd:   "rm -rf reports && mkdir -p reports && cp report-src.xml reports/report.xml 2>/dev/null || true",
		JUnitGlob: "reports/*.xml"})
	story, at, _, it, _, ut := fullTreeMCP(t, cs)

	call(t, cs, "set_paths", map[string]any{"story_id": story, "paths": []string{"pkg/auth", "missing/file.go"}})
	n := call(t, cs, "get_node", map[string]any{"id": story})
	blob, _ := json.Marshal(n["paths"])
	if !strings.Contains(string(blob), "pkg/auth") {
		t.Fatalf("paths not visible in node report: %s", blob)
	}

	call(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"})
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "start"})
	setReport(t, wtOf(repo, story), map[string]string{at: "", it: "", ut: ""})
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"},
		"declared paths missing in repo", "missing/file.go")

	call(t, cs, "set_paths", map[string]any{"story_id": story, "paths": []string{"pkg/auth"}})
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "specs_for_path",
		Arguments: map[string]any{"path": "pkg/auth/auth.go"}})
	if err != nil || res.IsError {
		t.Fatalf("specs_for_path: %v %s", err, text(res))
	}
	if !strings.Contains(text(res), story) {
		t.Fatalf("reverse lookup missing %s: %s", story, text(res))
	}
}

// TestEvidenceAcceptance_AT_17 proves AT-17 (US-14 "Test evidence on
// record") through MCP: after finish, get_node lists the proving tests with
// timestamp and get_tree carries per-spec evidence.
func TestEvidenceAcceptance_AT_17(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
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
	setReport(t, wtOf(repo, story), map[string]string{at: "", it: "", ut: ""})
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"})

	n := call(t, cs, "get_node", map[string]any{"id": ut})
	ev, ok := n["evidence"].(map[string]any)
	if !ok {
		t.Fatalf("get_node(%s) missing evidence: %v", ut, n)
	}
	tests := fmt.Sprintf("%v", ev["tests"])
	if !strings.Contains(tests, "Test_"+strings.ReplaceAll(ut, "-", "_")) {
		t.Fatalf("evidence tests = %s, want the proving test name", tests)
	}
	if ev["recorded_at"] == "" {
		t.Fatal("evidence missing timestamp")
	}

	tree := call(t, cs, "get_tree", map[string]any{"story_id": story})
	blob, _ := json.Marshal(tree)
	if !strings.Contains(string(blob), `"evidence"`) {
		t.Fatalf("get_tree missing evidence blocks:\n%s", blob)
	}
}

// TestWorktreeAcceptance_AT_20 proves AT-20 (US-17 "Worktree isolation")
// through MCP: parallel starts with separate worktrees and an untouched main
// worktree, finish gating inside the worktree with cleanup, abort cleanup,
// and the ignore guard.
func TestWorktreeAcceptance_AT_20(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	cs := clientFor(t, store.Project{ID: "p1", Name: "test", RepoPath: repo, BaseBranch: "develop",
		LintCmd:   "true",
		TestCmd:   "rm -rf reports && mkdir -p reports && cp report-src.xml reports/report.xml 2>/dev/null || true",
		JUnitGlob: "reports/*.xml"})

	a, atA, _, itA, _, utA := fullTreeMCP(t, cs)
	b, _, _, _, _, _ := fullTreeMCP(t, cs)
	for _, s := range []string{a, b} {
		call(t, cs, "transition", map[string]any{"story_id": s, "action": "refine"})
		msg := call(t, cs, "transition", map[string]any{"story_id": s, "action": "start"})["message"].(string)
		if !strings.Contains(msg, wtOf(repo, s)) {
			t.Fatalf("start message must name the worktree path, got %q", msg)
		}
	}

	// Parallel by construction: two worktrees, main untouched on base.
	for _, s := range []string{a, b} {
		if got := gitRun(t, wtOf(repo, s), "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/"+s {
			t.Fatalf("worktree of %s on %s", s, got)
		}
	}
	if got := gitRun(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "develop" {
		t.Fatalf("main worktree on %s, want develop", got)
	}

	// Finish story A: gates ran in its worktree, merge landed on base, cleanup.
	setReport(t, wtOf(repo, a), map[string]string{atA: "", itA: "", utA: ""})
	call(t, cs, "transition", map[string]any{"story_id": a, "action": "finish"})
	if _, err := os.Stat(wtOf(repo, a)); err == nil {
		t.Fatal("worktree of finished story still exists")
	}
	if log := gitRun(t, repo, "log", "--merges", "--oneline", "develop"); !strings.Contains(log, "Merge feature/"+a) {
		t.Fatalf("merge missing on develop:\n%s", log)
	}

	// Abort story B: worktree and branch gone, story refined.
	call(t, cs, "transition", map[string]any{"story_id": b, "action": "abort"})
	if _, err := os.Stat(wtOf(repo, b)); err == nil {
		t.Fatal("worktree of aborted story still exists")
	}
	if st, _ := nodeStatus(t, cs, b); st != "refined" {
		t.Fatalf("aborted story status = %s, want refined", st)
	}

	// Ignore guard: a repo without the .gitignore entry blocks start.
	repo2 := t.TempDir()
	gitRun(t, repo2, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo2, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo2, "add", ".")
	gitRun(t, repo2, "commit", "-m", "initial")
	cs2 := clientFor(t, store.Project{ID: "p2", Name: "t2", RepoPath: repo2, BaseBranch: "develop"})
	s2, _, _, _, _, _ := fullTreeMCP(t, cs2)
	call(t, cs2, "transition", map[string]any{"story_id": s2, "action": "refine"})
	callErr(t, cs2, "transition", map[string]any{"story_id": s2, "action": "start"},
		"start blocked", "not git-ignored", ".gitignore")
}

// TestCoverageAcceptance_AT_36 proves AT-36 (US-32 "Coverage visibility")
// through MCP plus the board: snapshot after finish, summary in the message,
// notice on broken input, flagged gaps on the board.
func TestCoverageAcceptance_AT_36(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "cov-src.txt"),
		[]byte("TN:\nSF:src/low.ts\nDA:1,0\nDA:2,0\nDA:3,1\nend_of_record\nSF:src/high.ts\nDA:1,1\nend_of_record\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	cs := clientFor(t, store.Project{ID: "p1", Name: "test", RepoPath: repo, BaseBranch: "develop",
		LintCmd:      "true",
		TestCmd:      "rm -rf reports && mkdir -p reports && cp report-src.xml reports/report.xml 2>/dev/null; cp cov-src.txt reports/cov.data",
		JUnitGlob:    "reports/*.xml",
		CoverageGlob: "reports/cov.data"})
	story, at, _, it, _, ut := fullTreeMCP(t, cs)
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"})
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "start"})
	setReport(t, wtOf(repo, story), map[string]string{at: "", it: "", ut: ""})
	msg := call(t, cs, "transition", map[string]any{"story_id": story, "action": "finish"})["message"].(string)
	if !strings.Contains(msg, "coverage 50.0%") || !strings.Contains(msg, "src/low.ts") {
		t.Fatalf("finish message: %s", msg)
	}

	o := call(t, cs, "get_overview", map[string]any{})
	blob, _ := json.Marshal(o["coverage"])
	if !strings.Contains(string(blob), `"total_pct":50`) || !strings.Contains(string(blob), "src/low.ts") ||
		strings.Contains(string(blob), "src/high.ts") {
		t.Fatalf("overview coverage: %s", blob)
	}

	html := renderBoard(t, cs)
	if !strings.Contains(html, `id="coverage"`) || !strings.Contains(html, "src/low.ts") ||
		!strings.Contains(html, `class="stale">33%`) {
		t.Fatal("board coverage section missing or gap not flagged")
	}
}
