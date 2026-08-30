package core_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
