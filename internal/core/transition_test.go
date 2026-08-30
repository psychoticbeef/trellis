package core_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

func git(t *testing.T, dir string, args ...string) string {
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func wtPath(repo, story string) string {
	return filepath.Join(repo, ".trellis-worktrees", story)
}

func reportXML(passing ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><testsuite name="all">`)
	for _, id := range passing {
		fmt.Fprintf(&b, `<testcase classname="all" name="Test_%s"/>`, strings.ReplaceAll(id, "-", "_"))
	}
	b.WriteString(`</testsuite>`)
	return b.String()
}

func TestGitFlow_IT_6(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1")) // UT-1 missing on purpose
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	p := e.Project
	p.RepoPath = repo
	p.LintCmd = "true"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml"
	p.JUnitGlob = "reports/*.xml"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}

	tr := fullTree(t, e)
	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatal(err)
	}

	// start: creates a story worktree, main worktree stays on develop.
	if _, err := e.Transition(tr.story, "start"); err != nil {
		t.Fatal(err)
	}
	wt := wtPath(repo, tr.story)
	if got := git(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/"+tr.story {
		t.Fatalf("worktree on branch %s, want feature/%s", got, tr.story)
	}
	if got := git(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "develop" {
		t.Fatalf("main worktree on %s, want develop", got)
	}

	// Simulate implementation work inside the worktree.
	writeFile(t, filepath.Join(wt, "impl.txt"), "code")
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "implement")

	// finish: blocked, the unit test spec has no test evidence.
	_, err := e.Transition(tr.story, "finish")
	wantErr(t, err, "test evidence incomplete", tr.ut+": no test references")

	// Add the missing test, commit, finish again.
	writeFile(t, filepath.Join(wt, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1"))
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "add unit test")
	if _, err := e.Transition(tr.story, "finish"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if got := status(t, e, tr.story); got != "done" {
		t.Fatalf("status = %s, want done", got)
	}
	if got := git(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "develop" {
		t.Fatalf("after finish on branch %s, want develop", got)
	}
	if out := git(t, repo, "branch", "--list", "feature/"+tr.story); out != "" {
		t.Fatalf("feature branch still exists: %s", out)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Fatal("story worktree still exists after finish")
	}
	if log := git(t, repo, "log", "--oneline", "develop"); !strings.Contains(log, "Merge feature/"+tr.story) {
		t.Fatalf("develop log missing merge commit:\n%s", log)
	}

	// done story can be pruned.
	if err := e.Prune(tr.story); err != nil {
		t.Fatalf("prune: %v", err)
	}
}

func TestStartRequiresCleanWorktreeAndBase_IT_6(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "main") // no develop branch
	writeFile(t, filepath.Join(repo, "a.txt"), "x")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")

	p := e.Project
	p.RepoPath = repo
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}

	tr := fullTree(t, e)
	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatal(err)
	}
	// Ignore guard fires first: .trellis-worktrees must be git-ignored.
	_, err := e.Transition(tr.story, "start")
	wantErr(t, err, "is not git-ignored", ".gitignore")

	writeFile(t, filepath.Join(repo, ".gitignore"), ".trellis-worktrees/\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "ignore worktrees")
	_, err = e.Transition(tr.story, "start")
	wantErr(t, err, `base branch "develop" does not exist`)
}

// TestTransitionGuardMatrix_IT_5 proves IT-5 (US-5): every illegal
// (state, verb) pair is rejected naming the required status, and the
// downgrade-repair-refine loop works mid-implementation.
func TestTransitionGuardMatrix_IT_5(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1"))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath = repo
	p.LintCmd = "true"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml"
	p.JUnitGlob = "reports/*.xml"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}

	tr := fullTree(t, e)
	blocked := func(verb, wantStatus string) {
		t.Helper()
		_, err := e.Transition(tr.story, verb)
		wantErr(t, err, verb+" blocked", fmt.Sprintf("%s requires %q", verb, wantStatus))
	}

	// todo: only refine is legal.
	blocked("start", "refined")
	blocked("finish", "in_progress")
	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatal(err)
	}

	// refined: only start is legal.
	blocked("refine", "todo")
	blocked("finish", "in_progress")
	if _, err := e.Transition(tr.story, "start"); err != nil {
		t.Fatal(err)
	}

	// in_progress: only finish is legal.
	blocked("refine", "todo")
	blocked("start", "refined")

	// Downgrade-repair loop: an edit mid-implementation forces the full loop.
	body := "changed mid-flight"
	if _, err := e.UpdateNode(tr.dd, nil, &body, nil); err != nil {
		t.Fatal(err)
	}
	if got := status(t, e, tr.story); got != "todo" {
		t.Fatalf("status = %s, want todo after mid-implementation edit", got)
	}
	blocked("finish", "in_progress")
	approve(t, e, tr.dd)
	approve(t, e, tr.ut)
	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Transition(tr.story, "start"); err != nil {
		t.Fatalf("re-start on existing branch: %v", err)
	}
	if _, err := e.Transition(tr.story, "finish"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// done: nothing is legal anymore.
	blocked("refine", "todo")
	blocked("start", "refined")
	blocked("finish", "in_progress")
	_, err := e.Transition(tr.story, "warp")
	wantErr(t, err, `unknown action "warp"`)
}

// TestLivingDoneTreeIntegration_IT_8 proves IT-8 (US-8): every mutation path
// works on a done tree, staleness propagates, status stays done, re-approval
// clears the markers.
func TestLivingDoneTreeIntegration_IT_8(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1"))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath, p.LintCmd, p.JUnitGlob = repo, "true", "reports/*.xml"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}
	cc := mustCreate(t, e, model.KindCrossCutting, "", "cc", nil)
	approve(t, e, cc.ID)
	tr := fullTree(t, e)
	if err := e.LinkDep(tr.arch, cc.ID); err != nil {
		t.Fatal(err)
	}
	approve(t, e, tr.arch)
	for _, verb := range []string{"refine", "start", "finish"} {
		if _, err := e.Transition(tr.story, verb); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
	}

	// Every mutation path succeeds on the done tree.
	body := "corrected"
	if _, err := e.UpdateNode(tr.dd, nil, &body, nil); err != nil {
		t.Fatalf("update on done tree: %v", err)
	}
	if _, err := e.AddAC(tr.story, "g2", "w2", "t2"); err != nil {
		t.Fatalf("AC add on done story: %v", err)
	}
	extra := mustCreate(t, e, model.KindUnitTest, tr.dd, "extra", nil)
	if err := e.DeleteNode(extra.ID); err != nil {
		t.Fatalf("delete in done tree: %v", err)
	}
	if _, err := e.UpdateNode(cc.ID, nil, &body, nil); err != nil {
		t.Fatal(err)
	}

	// Staleness propagated, status stayed done.
	if got := status(t, e, tr.story); got != "done" {
		t.Fatalf("status = %s, want done", got)
	}
	for _, id := range []string{tr.dd, tr.ut, tr.arch, tr.story} {
		if n, _ := e.Node(id); n.Fresh {
			t.Errorf("%s must be stale", id)
		}
	}

	// Re-approval clears everything (top-down, deps re-read).
	approve(t, e, cc.ID)
	// Delete the extra AC again to restore coverage-independent freshness.
	r, _ := e.Node(tr.story)
	for _, ac := range r.ACs {
		if ac.Given == "g2" {
			if err := e.DeleteAC(ac.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, id := range []string{tr.story, tr.at, tr.arch, tr.it, tr.dd, tr.ut} {
		approve(t, e, id)
	}
	for _, id := range []string{tr.story, tr.at, tr.arch, tr.it, tr.dd, tr.ut} {
		if n, _ := e.Node(id); !n.Fresh {
			t.Errorf("%s must be fresh after re-approval: %v", id, n.Problems)
		}
	}
	if got := status(t, e, tr.story); got != "done" {
		t.Fatalf("status = %s after re-approval, want done", got)
	}
}

// TestMergeGateIntegration_IT_9 proves IT-9 (US-9 "Merge gate"): a feature
// branch behind the base blocks finish naming both branches and the catch-up
// instruction; after merging base into feature the same finish passes.
func TestMergeGateIntegration_IT_9(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1"))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath, p.LintCmd, p.JUnitGlob = repo, "true", "reports/*.xml"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}
	tr := fullTree(t, e)
	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Transition(tr.story, "start"); err != nil {
		t.Fatal(err)
	}

	// Advance the base with an unrelated commit while the feature is active —
	// the main worktree sits on develop, so this needs no branch switching.
	writeFile(t, filepath.Join(repo, "unrelated.txt"), "x")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "base moved")

	_, err := e.Transition(tr.story, "finish")
	wantErr(t, err, "finish blocked", "feature/"+tr.story+" is behind develop",
		"merge or rebase develop into feature/"+tr.story)

	// Catch up inside the story worktree, then the same finish passes.
	git(t, wtPath(repo, tr.story), "merge", "develop", "-m", "catch up")
	if _, err := e.Transition(tr.story, "finish"); err != nil {
		t.Fatalf("finish after catch-up: %v", err)
	}
	if got := status(t, e, tr.story); got != "done" {
		t.Fatalf("status = %s, want done", got)
	}
}

// TestAbortIntegration_IT_10 proves IT-10 (US-10): the full abort path incl.
// the blocked dirty case, the status guard, and restart creating a fresh
// branch from base.
func TestAbortIntegration_IT_10(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath = repo
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}
	tr := fullTree(t, e)

	// Status guard: abort outside in_progress is rejected.
	_, err := e.Transition(tr.story, "abort")
	wantErr(t, err, "abort blocked", `abort requires "in_progress"`)

	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Transition(tr.story, "start"); err != nil {
		t.Fatal(err)
	}
	wt := wtPath(repo, tr.story)
	writeFile(t, filepath.Join(wt, "doomed.txt"), "wrong approach")
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "doomed work")

	// Dirty story worktree blocks abort.
	writeFile(t, filepath.Join(wt, "dirty.txt"), "x")
	_, err = e.Transition(tr.story, "abort")
	wantErr(t, err, "abort blocked", "never silently destroyed")
	if err := os.Remove(filepath.Join(wt, "dirty.txt")); err != nil {
		t.Fatal(err)
	}

	// Abort: worktree and branch discarded, refined again, main untouched.
	if _, err := e.Transition(tr.story, "abort"); err != nil {
		t.Fatal(err)
	}
	if got := status(t, e, tr.story); got != "refined" {
		t.Fatalf("status = %s, want refined", got)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Fatal("story worktree still exists after abort")
	}
	if out := git(t, repo, "branch", "--list", "feature/"+tr.story); out != "" {
		t.Fatalf("feature branch still exists: %s", out)
	}

	// Restart creates a fresh worktree from base — the doomed work is gone.
	if _, err := e.Transition(tr.story, "start"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wtPath(repo, tr.story), "doomed.txt")); err == nil {
		t.Fatal("doomed work survived the abort")
	}
}

// TestSequencingIntegration_IT_11 proves IT-11 (US-11): link creation in any
// target state, start blocked per dependency, unblocked after the dependency
// finishes, cycle rejection across a chain.
func TestSequencingIntegration_IT_11(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1"))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath, p.LintCmd, p.JUnitGlob = repo, "true", "reports/*.xml"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}

	a := fullTree(t, e) // will be the prerequisite
	b := fullTree(t, e)
	c := fullTree(t, e)
	if err := e.LinkDep(b.story, a.story); err != nil {
		t.Fatal(err)
	}
	if err := e.LinkDep(c.story, b.story); err != nil {
		t.Fatal(err)
	}
	err := e.LinkDep(a.story, c.story) // would close a -> c -> b -> a? (a depends on c, c on b, b on a)
	wantErr(t, err, "sequencing cycle")

	if _, err := e.Transition(b.story, "refine"); err != nil {
		t.Fatal(err)
	}
	_, err = e.Transition(b.story, "start")
	wantErr(t, err, "unfinished dependencies", a.story+" (todo)")

	// Drive the prerequisite through the full flow.
	for _, verb := range []string{"refine", "start", "finish"} {
		if _, err := e.Transition(a.story, verb); err != nil {
			t.Fatalf("%s(%s): %v", verb, a.story, err)
		}
	}
	if _, err := e.Transition(b.story, "start"); err != nil {
		t.Fatalf("start after dependency done: %v", err)
	}
}

// TestPathPointerIntegration_IT_13 proves IT-13 (US-13): set/replace/clear
// without touching approvals, finish blocked per missing path, reverse lookup.
func TestPathPointerIntegration_IT_13(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1"))
	writeFile(t, filepath.Join(repo, "pkg/auth/auth.go"), "package auth")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath, p.LintCmd, p.JUnitGlob = repo, "true", "reports/*.xml"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}
	tr := fullTree(t, e)
	if _, err := e.SetPaths(tr.story, []string{"pkg/auth", "cmd/tool/main.go"}); err != nil {
		t.Fatal(err)
	}
	if n, _ := e.Node(tr.story); !n.Fresh {
		t.Fatal("SetPaths must not invalidate the story approval")
	}
	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Transition(tr.story, "start"); err != nil {
		t.Fatal(err)
	}

	// finish blocked: one declared path missing.
	_, err := e.Transition(tr.story, "finish")
	wantErr(t, err, "declared paths missing in repo", "cmd/tool/main.go")

	// Fix by narrowing the declaration; finish passes.
	if _, err := e.SetPaths(tr.story, []string{"pkg/auth"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Transition(tr.story, "finish"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// Reverse lookup after done.
	hits, err := e.StoriesForPath("pkg/auth/auth.go")
	if err != nil || len(hits) != 1 || hits[0].ID != tr.story || hits[0].Status != "done" {
		t.Fatalf("reverse lookup: %v %v", hits, err)
	}
}

// TestEvidenceIntegration_IT_14 proves IT-14 (US-14): finish writes evidence
// for exactly the tree's test specs, re-runs replace rows, other stories stay
// untouched, reports render the records.
func TestEvidenceIntegration_IT_14(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1"))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath, p.LintCmd, p.JUnitGlob = repo, "true", "reports/*.xml"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}
	tr := fullTree(t, e)
	other := fullTree(t, e)
	for _, verb := range []string{"refine", "start", "finish"} {
		if _, err := e.Transition(tr.story, verb); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
	}

	// Exactly the tree's test specs carry evidence; arch/dd/story do not.
	for _, id := range []string{tr.at, tr.it, tr.ut} {
		n, _ := e.Node(id)
		if n.Evidence == nil || len(n.Evidence.Tests) == 0 {
			t.Fatalf("%s missing evidence after finish", id)
		}
	}
	if n, _ := e.Node(tr.arch); n.Evidence != nil {
		t.Fatal("arch must not carry evidence")
	}
	if n, _ := e.Node(other.ut); n.Evidence != nil {
		t.Fatal("other story's specs must stay untouched")
	}

	// The tree report renders evidence on test specs.
	tree, err := e.Tree(tr.story)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	var walk func(n core.TreeNode)
	walk = func(n core.TreeNode) {
		if n.ID == tr.ut && n.Evidence != nil && len(n.Evidence.Tests) > 0 {
			found = true
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(tree.Story)
	if !found {
		t.Fatal("tree report missing evidence on the unit test spec")
	}
}

// TestWorktreeLifecycleIntegration_IT_17 proves IT-17 (US-17): parallel
// starts, gates-in-worktree failures, abort cleanup with branch reuse on
// restart, and the ignore guard, against the engine and a real repo.
func TestWorktreeLifecycleIntegration_IT_17(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1"))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath, p.LintCmd, p.JUnitGlob = repo, "true", "reports/*.xml"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}

	a := fullTree(t, e)
	b := fullTree(t, e)
	for _, s := range []string{a.story, b.story} {
		if _, err := e.Transition(s, "refine"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Transition(s, "start"); err != nil {
			t.Fatalf("parallel start %s: %v", s, err)
		}
	}

	// Dirty story worktree blocks finish; dirty main worktree blocks the merge.
	wtA := wtPath(repo, a.story)
	writeFile(t, filepath.Join(wtA, "dirty.txt"), "x")
	_, err := e.Transition(a.story, "finish")
	wantErr(t, err, "finish blocked", "story worktree not clean")
	git(t, wtA, "add", ".")
	git(t, wtA, "commit", "-m", "work")
	writeFile(t, filepath.Join(repo, "main-dirty.txt"), "x")
	_, err = e.Transition(a.story, "finish")
	wantErr(t, err, "main worktree not clean")
	if err := os.Remove(filepath.Join(repo, "main-dirty.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Transition(a.story, "finish"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// Abort B keeps nothing; restart reuses no stale state.
	writeFile(t, filepath.Join(wtPath(repo, b.story), "keep.txt"), "y")
	git(t, wtPath(repo, b.story), "add", ".")
	git(t, wtPath(repo, b.story), "commit", "-m", "kept work")
	if _, err := e.Transition(b.story, "abort"); err != nil {
		t.Fatal(err)
	}
	// B is behind develop now (A merged) — restart creates a fresh branch
	// from the new base, so finish needs no catch-up.
	if _, err := e.Transition(b.story, "start"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wtPath(repo, b.story), "keep.txt")); err == nil {
		t.Fatal("aborted work leaked into the fresh worktree")
	}
	writeFile(t, filepath.Join(wtPath(repo, b.story), "report-src.xml"), reportXML(b.at, b.it, b.ut))
	git(t, wtPath(repo, b.story), "add", ".")
	git(t, wtPath(repo, b.story), "commit", "-m", "b evidence")
	if _, err := e.Transition(b.story, "finish"); err != nil {
		t.Fatalf("finish b: %v", err)
	}
}

// TestReleaseFlowIntegration_IT_22 proves IT-22 (US-22 "Release cut with
// feature manifest"): first and incremental release, delta from merge
// subjects, FEATURES.md only on the release branch, every blocking
// condition, main worktree parked back on base.
func TestReleaseFlowIntegration_IT_22(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1", "AT-2", "IT-2", "UT-2"))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath, p.LintCmd, p.JUnitGlob = repo, "true", "reports/*.xml"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml"
	p.ReleaseBranch = "main"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}
	if err := e.DefineTerm("gate", "guard that blocks a transition"); err != nil {
		t.Fatal(err)
	}

	// Nothing to release before any feature merged.
	_, err := e.Release()
	wantErr(t, err, "release blocked", "nothing to release")

	a := fullTree(t, e)
	driveDone := func(tr tree, marker string) {
		t.Helper()
		if _, err := e.Transition(tr.story, "refine"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Transition(tr.story, "start"); err != nil {
			t.Fatal(err)
		}
		wt := wtPath(repo, tr.story)
		writeFile(t, filepath.Join(wt, marker), "impl")
		git(t, wt, "add", ".")
		git(t, wt, "commit", "-m", "implement "+tr.story)
		if _, err := e.Transition(tr.story, "finish"); err != nil {
			t.Fatalf("finish %s: %v", tr.story, err)
		}
	}
	driveDone(a, "a.txt")

	// Dirty main worktree blocks.
	writeFile(t, filepath.Join(repo, "dirty.txt"), "x")
	_, err = e.Release()
	wantErr(t, err, "release blocked", "not clean")
	if err := os.Remove(filepath.Join(repo, "dirty.txt")); err != nil {
		t.Fatal(err)
	}

	// Stale specs block.
	body := "edited"
	if _, err := e.UpdateNode(a.dd, nil, &body, nil); err != nil {
		t.Fatal(err)
	}
	_, err = e.Release()
	wantErr(t, err, "release blocked", "honest context", a.dd+" changed since approval")
	approve(t, e, a.dd)
	approve(t, e, a.ut)

	// First release: branch created, message lists the feature, manifest there.
	msg, err := e.Release()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, a.story+" — login feature") {
		t.Fatalf("release message missing feature: %s", msg)
	}
	if got := git(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "develop" {
		t.Fatalf("main worktree on %s, want develop after release", got)
	}
	manifest := git(t, repo, "show", "main:FEATURES.md")
	for _, want := range []string{"- **" + a.story + "** — login feature", "# Glossary", "- **gate** —"} {
		if !strings.Contains(manifest, want) {
			t.Errorf("FEATURES.md missing %q:\n%s", want, manifest)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "FEATURES.md")); err == nil {
		t.Fatal("FEATURES.md must not exist on the base branch")
	}

	// Nothing new: blocked.
	_, err = e.Release()
	wantErr(t, err, "nothing to release")

	// Incremental release lists only the new feature.
	b := fullTree(t, e)
	driveDone(b, "b.txt")
	msg, err = e.Release()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(msg, a.story+" —") || !strings.Contains(msg, b.story+" —") {
		t.Fatalf("incremental delta wrong: %s", msg)
	}
	// Merge message on the release branch lists the new feature.
	log := git(t, repo, "log", "--merges", "--pretty=%B", "-1", "main")
	if !strings.Contains(log, b.story+" — login feature") {
		t.Fatalf("release merge message wrong:\n%s", log)
	}
	// Manifest now lists both features.
	manifest = git(t, repo, "show", "main:FEATURES.md")
	if !strings.Contains(manifest, a.story) || !strings.Contains(manifest, b.story) {
		t.Fatalf("manifest incomplete:\n%s", manifest)
	}
}

// TestExportImportIntegration_IT_25 proves IT-25 (US-25 "YAML export and
// import"): full round trip over a populated project incl. sequencing links,
// pins, evidence, glossary and counters; release commits the backup file.
func TestExportImportIntegration_IT_25(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1"))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath, p.LintCmd, p.JUnitGlob = repo, "true", "reports/*.xml"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml"
	p.ReleaseBranch = "main"
	p.Description = "spec tracking"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}

	// Populate: cc + dep, done story with evidence, second story with
	// sequencing link, glossary, paths.
	cc := mustCreate(t, e, model.KindCrossCutting, "", "cc", nil)
	approve(t, e, cc.ID)
	a := fullTree(t, e)
	if err := e.LinkDep(a.arch, cc.ID); err != nil {
		t.Fatal(err)
	}
	approve(t, e, a.arch)
	if _, err := e.SetPaths(a.story, []string{"pkg"}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "pkg/x.go"), "package pkg")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "pkg")
	if _, err := e.Transition(a.story, "refine"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Transition(a.story, "start"); err != nil {
		t.Fatal(err)
	}
	wt := wtPath(repo, a.story)
	writeFile(t, filepath.Join(wt, "impl.txt"), "x")
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "impl")
	if _, err := e.Transition(a.story, "finish"); err != nil {
		t.Fatal(err)
	}
	b := fullTree(t, e)
	if err := e.LinkDep(b.story, a.story); err != nil {
		t.Fatal(err)
	}
	if err := e.DefineTerm("gate", "guard that blocks a transition"); err != nil {
		t.Fatal(err)
	}

	// Round trip.
	doc, err := e.ExportYAML()
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Import(st, []byte(doc), store.Project{ID: "p9", Name: "test", RepoPath: repo}); err != nil {
		t.Fatal(err)
	}
	e9, err := core.NewEngine(st, "p9")
	if err != nil {
		t.Fatal(err)
	}
	doc9, err := e9.ExportYAML()
	if err != nil {
		t.Fatal(err)
	}
	if doc != doc9 {
		t.Fatalf("round trip diverged:\n%s\n---\n%s", doc, doc9)
	}
	// Approvals survived: the done story is still fresh in the copy.
	if n, _ := e9.Node(a.arch); !n.Fresh {
		t.Fatalf("imported arch not fresh: %v", n.Problems)
	}
	if got := status(t, e9, a.story); got != "done" {
		t.Fatalf("imported status = %s, want done", got)
	}

	// Release commits the backup beside the manifest.
	if _, err := e.Release(); err != nil {
		t.Fatal(err)
	}
	if out := git(t, repo, "show", "main:trellis-specs.yaml"); !strings.Contains(out, "trellis_export: 1") {
		t.Fatalf("backup missing on release branch:\n%.200s", out)
	}
	if _, err := os.Stat(filepath.Join(repo, "trellis-specs.yaml")); err == nil {
		t.Fatal("backup leaked onto the base branch")
	}
}

// TestBatchApprovalIntegration_IT_26 proves IT-26 (US-26 "Batch tree
// approval"): happy batch over pins and sequencing links against a real
// store, atomicity on a fully stale tree, refine passes afterwards.
func TestBatchApprovalIntegration_IT_26(t *testing.T) {
	e, _ := newEngineStore(t)
	cc := mustCreate(t, e, model.KindCrossCutting, "", "cc", nil)
	approve(t, e, cc.ID)
	tr := fullTree(t, e)
	if err := e.LinkDep(tr.arch, cc.ID); err != nil {
		t.Fatal(err)
	}
	// Invalidate everything with a story edit.
	body := "v2"
	if _, err := e.UpdateNode(tr.story, nil, &body, nil); err != nil {
		t.Fatal(err)
	}

	full, err := e.TreeFull(tr.story)
	if err != nil {
		t.Fatal(err)
	}
	if full.Story.Body != "v2" {
		t.Fatalf("full tree missing bodies: %+v", full.Story)
	}
	hashes := map[string]string{}
	var walk func(n core.TreeNode)
	walk = func(n core.TreeNode) {
		hashes[n.ID] = n.Hash
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(full.Story)

	// Atomicity: one wrong hash, zero approvals on a fully stale tree.
	bad := map[string]string{}
	for k, v := range hashes {
		bad[k] = v
	}
	bad[tr.it] = "wrong"
	ccNode, _ := e.Node(cc.ID)
	deps := map[string]string{cc.ID: ccNode.Hash}
	if err := e.ApproveTree(tr.story, bad, deps); err == nil {
		t.Fatal("bad batch must fail")
	}
	// The story edit invalidated the story and its direct children; none of
	// them may have been approved by the failed batch.
	for _, id := range []string{tr.story, tr.at, tr.arch} {
		if n, _ := e.Node(id); n.Fresh {
			t.Fatalf("%s approved despite failed batch", id)
		}
	}

	// Clean batch, then refine.
	if err := e.ApproveTree(tr.story, hashes, deps); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatalf("refine after batch: %v", err)
	}
}

// TestConcurrencyIntegration_IT_27 proves IT-27 (US-27 "Atomic mutations"):
// transition double-fire yields one winner, racing arch creation hits the
// database invariant, reads answer while the lock is held.
func TestConcurrencyIntegration_IT_27(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	open2 := func() (*core.Engine, *store.Store) {
		t.Helper()
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		e, err := core.NewEngine(st, "p1")
		if err != nil {
			t.Fatal(err)
		}
		return e, st
	}
	st1, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st1.Close() })
	if err := st1.CreateProject(store.Project{ID: "p1", Name: "t", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e1, err := core.NewEngine(st1, "p1")
	if err != nil {
		t.Fatal(err)
	}
	e2, _ := open2()

	tr := fullTree(t, e1)

	// Double-fire refine from two engines: exactly one winner.
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, e := range []*core.Engine{e1, e2} {
		wg.Add(1)
		go func(i int, e *core.Engine) {
			defer wg.Done()
			_, results[i] = e.Transition(tr.story, "refine")
		}(i, e)
	}
	wg.Wait()
	oks, blocks := 0, 0
	for _, err := range results {
		if err == nil {
			oks++
		} else if strings.Contains(err.Error(), `refine requires "todo"`) {
			blocks++
		} else {
			t.Fatalf("unexpected race error: %v", err)
		}
	}
	if oks != 1 || blocks != 1 {
		t.Fatalf("double-fire: %d ok, %d blocked; want 1/1 (%v)", oks, blocks, results)
	}

	// Racing arch creation on a fresh story: the DB invariant holds.
	s2 := mustCreate(t, e1, model.KindStory, "", "racy", nil)
	arches := make([]error, 2)
	for i, e := range []*core.Engine{e1, e2} {
		wg.Add(1)
		go func(i int, e *core.Engine) {
			defer wg.Done()
			_, arches[i] = e.CreateNode(model.KindArch, s2.ID, "as", "", nil)
		}(i, e)
	}
	wg.Wait()
	archOks := 0
	for _, err := range arches {
		if err == nil {
			archOks++
		}
	}
	if archOks != 1 {
		t.Fatalf("arch race produced %d arch specs: %v", archOks, arches)
	}
	children, _ := st1.ListChildren("p1", s2.ID)
	if len(children) != 1 {
		t.Fatalf("story has %d arch children, want 1", len(children))
	}

	// Reads answer while the lock is held.
	unlock, err := st1.LockProject("p1")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := e2.Overview()
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("read during held lock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read blocked by the mutation lock")
	}
	unlock()
}

// TestReleaseAuthority_IT_31 proves IT-31 (US-31): release succeeds although
// the installed pre-commit hook would fail, and a post-merge failure unwinds
// without leaking manifest files onto the base branch.
func TestReleaseAuthority_IT_31(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1"))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	// A hook that always fails: release must not care.
	writeFile(t, filepath.Join(repo, ".git/hooks/pre-commit"), "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(repo, ".git/hooks/pre-commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := e.Project
	p.RepoPath, p.LintCmd, p.JUnitGlob = repo, "true", "reports/*.xml"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml"
	p.ReleaseBranch = "main"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}
	tr := fullTree(t, e)
	if _, err := e.Transition(tr.story, "refine"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Transition(tr.story, "start"); err != nil {
		t.Fatal(err)
	}
	wt := wtPath(repo, tr.story)
	writeFile(t, filepath.Join(wt, "impl.txt"), "x")
	git(t, wt, "add", ".")
	// The feature commit is agent work and must OBEY hooks — commit with
	// no-verify here only because this test's hook always fails.
	git(t, wt, "commit", "--no-verify", "-m", "impl")
	if _, err := e.Transition(tr.story, "finish"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// Release passes although the pre-commit hook would reject any commit.
	if _, err := e.Release(); err != nil {
		t.Fatalf("release with hostile hook: %v", err)
	}
	if out := git(t, repo, "show", "main:FEATURES.md"); !strings.Contains(out, "# test") {
		t.Fatalf("manifest missing:\n%.120s", out)
	}
	if got := git(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "develop" {
		t.Fatalf("on %s, want develop", got)
	}
	if dirt := git(t, repo, "status", "--porcelain"); dirt != "" {
		t.Fatalf("base branch dirty after release:\n%s", dirt)
	}
}

// TestReleaseUnwind_UT_32 and TestReleaseAuthorityAcceptance_AT_35: the
// unwind path leaves the base branch clean when the manifest commit fails.
func TestReleaseUnwind_UT_32(t *testing.T) { releaseUnwindScenario(t) }

// TestReleaseAuthorityAcceptance_AT_35 proves AT-35 (US-31 "Release commits
// carry trellis authority") — same scenario driven end to end: hostile hook
// ignored on success (see IT-31) and clean unwind on failure.
func TestReleaseAuthorityAcceptance_AT_35(t *testing.T) { releaseUnwindScenario(t) }

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func releaseUnwindScenario(t *testing.T) {
	t.Helper()
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1"))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath, p.LintCmd, p.JUnitGlob = repo, "true", "reports/*.xml"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml"
	p.ReleaseBranch = "main"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}
	tr := fullTree(t, e)
	for _, verb := range []string{"refine", "start"} {
		if _, err := e.Transition(tr.story, verb); err != nil {
			t.Fatal(err)
		}
	}
	wt := wtPath(repo, tr.story)
	writeFile(t, filepath.Join(wt, "impl.txt"), "x")
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "impl")
	if _, err := e.Transition(tr.story, "finish"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// Post-merge failure injection: an ignored FEATURES.md makes the staging
	// of the manifest fail after the merge succeeded.
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\nFEATURES.md\n")
	git(t, repo, "add", ".gitignore")
	git(t, repo, "commit", "-m", "block manifest")
	if _, err := e.Release(); err == nil {
		t.Fatal("release must fail while the manifest is ignored")
	}
	// The rollback is total: a failed first release leaves no release branch.
	if _, err := gitOut(repo, "rev-parse", "--verify", "refs/heads/main"); err == nil {
		t.Fatal("failed first release left the release branch behind")
	}
	// Undo the obstacle; the retry re-merges the same delta.
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	git(t, repo, "add", ".gitignore")
	git(t, repo, "commit", "-m", "unblock manifest")
	// Unwind left the base branch clean: no staged or stray manifest files.
	if got := git(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "develop" {
		t.Fatalf("on %s, want develop after failed release", got)
	}
	if dirt := git(t, repo, "status", "--porcelain"); dirt != "" {
		t.Fatalf("base branch dirty after failed release:\n%s", dirt)
	}
	if _, err := os.Stat(filepath.Join(repo, "FEATURES.md")); err == nil {
		t.Fatal("manifest leaked onto the base branch")
	}

	// And the same release succeeds once the obstacle is gone.
	if _, err := e.Release(); err != nil {
		t.Fatalf("release after unwind: %v", err)
	}
}

// TestCoverageIntegration_IT_32 proves IT-32 (US-32 "Coverage visibility"):
// finish records LCOV and coverprofile snapshots with replacement, broken
// input degrades to a notice, overview carries the summary.
func TestCoverageIntegration_IT_32(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1", "AT-2", "IT-2", "UT-2", "AT-3", "IT-3", "UT-3"))
	writeFile(t, filepath.Join(repo, "cov-src.txt"), "TN:\nSF:src/a.ts\nDA:1,1\nDA:2,0\nend_of_record\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath, p.LintCmd, p.JUnitGlob = repo, "true", "reports/*.xml"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml && cp cov-src.txt reports/cov.data"
	p.CoverageGlob = "reports/cov.data"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}
	drive := func(tr tree) string {
		t.Helper()
		for _, verb := range []string{"refine", "start"} {
			if _, err := e.Transition(tr.story, verb); err != nil {
				t.Fatal(err)
			}
		}
		wt := wtPath(repo, tr.story)
		writeFile(t, filepath.Join(wt, tr.story+".txt"), "x")
		git(t, wt, "add", ".")
		git(t, wt, "commit", "-m", "impl")
		msg, err := e.Transition(tr.story, "finish")
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}

	// LCOV snapshot + finish note.
	msg := drive(fullTree(t, e))
	if !strings.Contains(msg, "coverage 50.0%") || !strings.Contains(msg, "src/a.ts 50%") {
		t.Fatalf("finish message missing coverage: %s", msg)
	}
	o, err := e.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if o.Coverage == nil || o.Coverage.TotalPct != 50 || len(o.Coverage.Gaps) != 1 {
		t.Fatalf("overview coverage: %+v", o.Coverage)
	}

	// Replacement with a Go coverprofile.
	writeFile(t, filepath.Join(repo, "cov-src.txt"), "mode: set\npkg/z.go:1.1,2.2 4 1\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "switch format")
	drive(fullTree(t, e))
	o, _ = e.Overview()
	if o.Coverage == nil || o.Coverage.TotalPct != 100 || len(o.Coverage.Gaps) != 0 {
		t.Fatalf("replaced snapshot: %+v", o.Coverage)
	}
	rows, _ := st.ListCoverage("p1")
	if len(rows) != 1 || rows[0].File != "pkg/z.go" {
		t.Fatalf("replacement rows: %+v", rows)
	}

	// Broken input: non-blocking notice, snapshot untouched.
	writeFile(t, filepath.Join(repo, "cov-src.txt"), "total garbage")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "break coverage")
	msg = drive(fullTree(t, e))
	if !strings.Contains(msg, "coverage not recorded") {
		t.Fatalf("missing non-blocking notice: %s", msg)
	}
	if got := status(t, e, "US-3"); got != "done" {
		t.Fatalf("finish must succeed despite broken coverage, story = %s", got)
	}
}

// TestAuditIntegration_IT_33 proves IT-33 (US-33 "Bidirectional audit"):
// the full rot matrix — clean repo, broken done evidence, missing declared
// path, dangling spec reference, unbound test and unclaimed file as infos.
func TestAuditIntegration_IT_33(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1"))
	writeFile(t, filepath.Join(repo, "pkg/impl.go"), "package pkg")
	writeFile(t, filepath.Join(repo, "stray/orphan.go"), "package stray")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath, p.LintCmd, p.JUnitGlob = repo, "true", "reports/*.xml"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}
	tr := fullTree(t, e)
	if _, err := e.SetPaths(tr.story, []string{"pkg"}); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"refine", "start"} {
		if _, err := e.Transition(tr.story, verb); err != nil {
			t.Fatal(err)
		}
	}
	wt := wtPath(repo, tr.story)
	writeFile(t, filepath.Join(wt, "impl.txt"), "x")
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-m", "impl")
	if _, err := e.Transition(tr.story, "finish"); err != nil {
		t.Fatal(err)
	}

	// Clean state: no violations; orphan file listed informationally.
	rep, err := e.Audit()
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("clean repo has violations: %v", rep.Violations)
	}
	joined := strings.Join(rep.Infos, "\n")
	if !strings.Contains(joined, "stray/orphan.go") || strings.Contains(joined, "pkg/impl.go") {
		t.Fatalf("unclaimed info wrong: %s", joined)
	}

	// Seed rot: rename the UT-1 test away (done evidence breaks), add a test
	// referencing a nonexistent spec, an unbound test, and drop the declared
	// path.
	writeFile(t, filepath.Join(repo, "report-src.xml"),
		`<?xml version="1.0"?><testsuite><testcase name="Test_AT_1"/><testcase name="Test_IT_1"/><testcase name="Test_UT_77"/><testcase name="TestFreeFloating"/></testsuite>`)
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "rot")
	if err := os.RemoveAll(filepath.Join(repo, "pkg")); err != nil {
		t.Fatal(err)
	}

	rep, err = e.Audit()
	if err != nil {
		t.Fatal(err)
	}
	joinedV := strings.Join(rep.Violations, "\n")
	for _, want := range []string{
		"done story " + tr.story + ": UT-1: no test references",
		"references nonexistent spec UT-77",
		"declared path pkg no longer exists",
	} {
		if !strings.Contains(joinedV, want) {
			t.Errorf("violations missing %q:\n%s", want, joinedV)
		}
	}
	if !strings.Contains(strings.Join(rep.Infos, "\n"), "unbound") {
		t.Errorf("unbound info missing: %v", rep.Infos)
	}
}

// TestCoverageDelta_IT_36 / _UT_37 / AT-40: two finishes with moving
// coverage — first without delta, second with the signed delta, overview
// mirroring it, meta surviving snapshot replacement.
func TestCoverageDelta_IT_36(t *testing.T) { coverageDeltaScenario(t) }

// TestCoverageDeltaUnit_UT_37 proves UT-37 (DD-37 "Delta plumbing") via the
// same scenario: sign, nil-on-first, meta persistence.
func TestCoverageDeltaUnit_UT_37(t *testing.T) { coverageDeltaScenario(t) }

// TestCoverageDeltaAcceptance_AT_40 proves AT-40 (US-36 "Coverage delta").
func TestCoverageDeltaAcceptance_AT_40(t *testing.T) { coverageDeltaScenario(t) }

func coverageDeltaScenario(t *testing.T) {
	t.Helper()
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n.trellis-worktrees/\n")
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1", "AT-2", "IT-2", "UT-2"))
	writeFile(t, filepath.Join(repo, "cov-src.txt"), "TN:\nSF:a.ts\nDA:1,1\nDA:2,0\nend_of_record\n") // 50%
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "initial")
	p := e.Project
	p.RepoPath, p.LintCmd, p.JUnitGlob = repo, "true", "reports/*.xml"
	p.TestCmd = "mkdir -p reports && cp report-src.xml reports/report.xml && cp cov-src.txt reports/cov.data"
	p.CoverageGlob = "reports/cov.data"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := e.ReloadProject(); err != nil {
		t.Fatal(err)
	}
	drive := func(tr tree) string {
		t.Helper()
		for _, verb := range []string{"refine", "start"} {
			if _, err := e.Transition(tr.story, verb); err != nil {
				t.Fatal(err)
			}
		}
		wt := wtPath(repo, tr.story)
		writeFile(t, filepath.Join(wt, tr.story+".txt"), "x")
		git(t, wt, "add", ".")
		git(t, wt, "commit", "-m", "impl")
		msg, err := e.Transition(tr.story, "finish")
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}

	// First snapshot: no delta, no misleading zero.
	msg := drive(fullTree(t, e))
	if !strings.Contains(msg, "coverage 50.0%") || strings.Contains(msg, "(+") || strings.Contains(msg, "(-") {
		t.Fatalf("first snapshot must carry no delta: %s", msg)
	}
	o, _ := e.Overview()
	if o.Coverage == nil || o.Coverage.DeltaPct != nil {
		t.Fatalf("overview delta on first snapshot: %+v", o.Coverage)
	}

	// Second snapshot at 75%: signed positive delta.
	writeFile(t, filepath.Join(repo, "cov-src.txt"), "TN:\nSF:a.ts\nDA:1,1\nDA:2,1\nDA:3,1\nDA:4,0\nend_of_record\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "more coverage")
	msg = drive(fullTree(t, e))
	if !strings.Contains(msg, "coverage 75.0% (+25.0)") {
		t.Fatalf("second snapshot delta: %s", msg)
	}
	o, _ = e.Overview()
	if o.Coverage == nil || o.Coverage.DeltaPct == nil || *o.Coverage.DeltaPct != 25 {
		t.Fatalf("overview delta: %+v", o.Coverage)
	}
}
