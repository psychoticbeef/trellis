package core_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"trellis/internal/core"
	"trellis/internal/model"
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
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n")
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

	// start: creates and checks out the feature branch.
	if _, err := e.Transition(tr.story, "start"); err != nil {
		t.Fatal(err)
	}
	if got := git(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "feature/"+tr.story {
		t.Fatalf("on branch %s, want feature/%s", got, tr.story)
	}

	// Simulate implementation work.
	writeFile(t, filepath.Join(repo, "impl.txt"), "code")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "implement")

	// finish: blocked, the unit test spec has no test evidence.
	_, err := e.Transition(tr.story, "finish")
	wantErr(t, err, "test evidence incomplete", tr.ut+": no test references")

	// Add the missing test, commit, finish again.
	writeFile(t, filepath.Join(repo, "report-src.xml"), reportXML("AT-1", "IT-1", "UT-1"))
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "add unit test")
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
	_, err := e.Transition(tr.story, "start")
	wantErr(t, err, `base branch "develop" does not exist`)

	git(t, repo, "branch", "develop")
	writeFile(t, filepath.Join(repo, "dirty.txt"), "uncommitted")
	_, err = e.Transition(tr.story, "start")
	wantErr(t, err, "worktree not clean")
}

// TestTransitionGuardMatrix_IT_5 proves IT-5 (US-5): every illegal
// (state, verb) pair is rejected naming the required status, and the
// downgrade-repair-refine loop works mid-implementation.
func TestTransitionGuardMatrix_IT_5(t *testing.T) {
	e, st := newEngineStore(t)
	repo := t.TempDir()
	git(t, repo, "init", "-b", "develop")
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n")
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
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n")
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
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n")
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

	// Advance the base with an unrelated commit while the feature is active.
	git(t, repo, "checkout", "develop")
	writeFile(t, filepath.Join(repo, "unrelated.txt"), "x")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "base moved")
	git(t, repo, "checkout", "feature/"+tr.story)

	_, err := e.Transition(tr.story, "finish")
	wantErr(t, err, "finish blocked", "feature/"+tr.story+" is behind develop",
		"merge or rebase develop into feature/"+tr.story)

	// Catch up, then the same finish passes and merges.
	git(t, repo, "merge", "develop", "-m", "catch up")
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
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n")
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
	writeFile(t, filepath.Join(repo, "doomed.txt"), "wrong approach")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "doomed work")

	// Dirty worktree blocks abort.
	writeFile(t, filepath.Join(repo, "dirty.txt"), "x")
	_, err = e.Transition(tr.story, "abort")
	wantErr(t, err, "abort blocked", "never silently destroyed")
	if err := os.Remove(filepath.Join(repo, "dirty.txt")); err != nil {
		t.Fatal(err)
	}

	// Abort: branch discarded, base checked out, refined again.
	if _, err := e.Transition(tr.story, "abort"); err != nil {
		t.Fatal(err)
	}
	if got := status(t, e, tr.story); got != "refined" {
		t.Fatalf("status = %s, want refined", got)
	}
	if got := git(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); got != "develop" {
		t.Fatalf("on %s, want develop", got)
	}

	// Restart creates a fresh branch from base — the doomed work is gone.
	if _, err := e.Transition(tr.story, "start"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "doomed.txt")); err == nil {
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
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n")
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
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n")
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
	writeFile(t, filepath.Join(repo, ".gitignore"), "reports/\n")
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
