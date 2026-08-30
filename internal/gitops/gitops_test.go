package gitops

import (
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

func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-m", msg)
}

func repo(t *testing.T) (Git, string) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-b", "develop")
	commitFile(t, dir, "base.txt", "base", "initial")
	return Git{Dir: dir}, dir
}

// TestAncestorPairs_UT_10 proves UT-10 (DD-10 "gitops.IsAncestor"): diverged,
// fast-forwardable and up-to-date branch pairs.
func TestAncestorPairs_UT_10(t *testing.T) {
	g, dir := repo(t)
	git(t, dir, "branch", "feature")

	// Up to date (equal): ancestor in both directions.
	if ok, err := g.IsAncestor("develop", "feature"); err != nil || !ok {
		t.Fatalf("equal branches: want true, got %v %v", ok, err)
	}

	// Fast-forwardable: feature ahead of develop.
	git(t, dir, "checkout", "feature")
	commitFile(t, dir, "f.txt", "x", "feature work")
	if ok, _ := g.IsAncestor("develop", "feature"); !ok {
		t.Fatal("develop must be ancestor of ahead feature")
	}
	if ok, _ := g.IsAncestor("feature", "develop"); ok {
		t.Fatal("ahead feature must not be ancestor of develop")
	}

	// Diverged: both moved.
	git(t, dir, "checkout", "develop")
	commitFile(t, dir, "d.txt", "y", "base work")
	if ok, _ := g.IsAncestor("develop", "feature"); ok {
		t.Fatal("diverged: develop must not be ancestor of feature")
	}
	if ok, _ := g.IsAncestor("feature", "develop"); ok {
		t.Fatal("diverged: feature must not be ancestor of develop")
	}
}

// TestMergeOrchestration_UT_7 proves UT-7 (DD-7 "Merge orchestration"): the
// worktree-era MergeBranch — clean main worktree required, --no-ff commit on
// base, abort on conflict with the main worktree staying on base.
func TestMergeOrchestration_UT_7(t *testing.T) {
	g, dir := repo(t)
	wtA := filepath.Join(t.TempDir(), "wt-A")
	if err := g.WorktreeAdd(wtA, "feature/A", "develop"); err != nil {
		t.Fatal(err)
	}
	commitFile(t, wtA, "a.txt", "work", "feature work")

	// Dirty main worktree blocks the merge.
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.MergeBranch("feature/A", "develop", "m"); err == nil ||
		!strings.Contains(err.Error(), "main worktree not clean") {
		t.Fatalf("want dirty-main rejection, got %v", err)
	}
	os.Remove(filepath.Join(dir, "dirty.txt"))

	// Happy merge: --no-ff commit on base; cleanup removes worktree and branch.
	if err := g.MergeBranch("feature/A", "develop", "Merge feature/A"); err != nil {
		t.Fatal(err)
	}
	if log := git(t, dir, "log", "--merges", "--oneline"); !strings.Contains(log, "Merge feature/A") {
		t.Fatalf("no merge commit on develop:\n%s", log)
	}
	if err := g.WorktreeRemove(wtA); err != nil {
		t.Fatal(err)
	}
	if err := g.DeleteBranch("feature/A"); err != nil {
		t.Fatal(err)
	}

	// Conflict: merge aborts, main worktree stays on base and clean.
	wtC := filepath.Join(t.TempDir(), "wt-C")
	if err := g.WorktreeAdd(wtC, "feature/C", "develop"); err != nil {
		t.Fatal(err)
	}
	commitFile(t, wtC, "base.txt", "feature change", "conflicting feature work")
	commitFile(t, dir, "base.txt", "develop change", "conflicting base work")
	baseSHA := git(t, dir, "rev-parse", "develop")
	err := g.MergeBranch("feature/C", "develop", "m")
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("want abort error, got %v", err)
	}
	if cur, _ := g.CurrentBranch(); cur != "develop" {
		t.Fatalf("after abort on %s, want develop", cur)
	}
	if clean, _ := g.IsClean(); !clean {
		t.Fatal("main worktree dirty after aborted merge")
	}
	if got := git(t, dir, "rev-parse", "develop"); got != baseSHA {
		t.Fatal("develop moved despite aborted merge")
	}
}

// TestWorktreeOps_UT_18 proves UT-18 (DD-18 "gitops worktree operations"):
// WorktreeAdd create and branch reuse, WorktreeRemove, IsIgnored.
func TestWorktreeOps_UT_18(t *testing.T) {
	g, dir := repo(t)
	wt := filepath.Join(t.TempDir(), "wt-X")

	// Create fresh from base.
	if err := g.WorktreeAdd(wt, "feature/X", "develop"); err != nil {
		t.Fatal(err)
	}
	commitFile(t, filepath.Join(dir), "base2.txt", "y", "more base") // main on develop
	commitFile(t, wt, "x.txt", "work", "feature work")
	if err := g.WorktreeRemove(wt); err != nil {
		t.Fatal(err)
	}

	// Reuse the surviving branch: the commit is still there.
	if err := g.WorktreeAdd(wt, "feature/X", "develop"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt, "x.txt")); err != nil {
		t.Fatal("branch reuse lost the feature commit")
	}
	if err := g.WorktreeRemove(wt); err != nil {
		t.Fatal(err)
	}
	if err := g.DeleteBranch("feature/X"); err != nil {
		t.Fatal(err)
	}

	// Missing base is named.
	if err := g.WorktreeAdd(wt, "feature/Y", "nope"); err == nil ||
		!strings.Contains(err.Error(), `base branch "nope" does not exist`) {
		t.Fatalf("want missing-base error, got %v", err)
	}

	// IsIgnored honors .gitignore for hypothetical contained paths.
	if g.IsIgnored(".trellis-worktrees/probe") {
		t.Fatal("must not be ignored without a .gitignore entry")
	}
	commitFile(t, dir, ".gitignore", ".trellis-worktrees/\n", "ignore worktrees")
	if !g.IsIgnored(".trellis-worktrees/probe") {
		t.Fatal("must be ignored with the .gitignore entry")
	}
}

// TestWorktreeDiscard_UT_11 proves UT-11 (DD-11, US-10 "Abort transition")
// in its worktree-era form: WorktreeRemove and DeleteBranch as the abort
// mechanics, and their error behaviour on missing targets.
func TestWorktreeDiscard_UT_11(t *testing.T) {
	g, _ := repo(t)
	wt := filepath.Join(t.TempDir(), "wt-D")
	if err := g.WorktreeAdd(wt, "feature/D", "develop"); err != nil {
		t.Fatal(err)
	}
	commitFile(t, wt, "d.txt", "doomed", "doomed work")
	if err := g.WorktreeRemove(wt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Fatal("worktree dir survived removal")
	}
	if err := g.DeleteBranch("feature/D"); err != nil {
		t.Fatal(err)
	}
	if g.BranchExists("feature/D") {
		t.Fatal("branch survived deletion")
	}
	if err := g.WorktreeRemove(wt); err == nil {
		t.Fatal("removing a missing worktree must error")
	}
	if err := g.DeleteBranch("feature/D"); err == nil {
		t.Fatal("deleting a missing branch must error")
	}
}
