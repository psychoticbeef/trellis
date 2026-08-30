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

// TestBranchAndMerge_UT_7 proves UT-7 (DD-7 "Merge orchestration"): branch
// creation from base, reuse of an existing feature branch, dirty-worktree
// rejection, and merge-abort recovery.
func TestBranchAndMerge_UT_7(t *testing.T) {
	g, dir := repo(t)

	// Creation from base.
	if err := g.EnsureFeatureBranch("feature/A", "develop"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := g.CurrentBranch(); cur != "feature/A" {
		t.Fatalf("on %s, want feature/A", cur)
	}
	commitFile(t, dir, "a.txt", "work", "feature work")

	// Missing base is named.
	if err := g.EnsureFeatureBranch("feature/B", "nope"); err == nil ||
		!strings.Contains(err.Error(), `base branch "nope" does not exist`) {
		t.Fatalf("want missing-base error, got %v", err)
	}

	// Dirty worktree blocks switching.
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.EnsureFeatureBranch("feature/B", "develop"); err == nil ||
		!strings.Contains(err.Error(), "worktree not clean") {
		t.Fatalf("want dirty-worktree error, got %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "dirty.txt")); err != nil {
		t.Fatal(err)
	}

	// Reuse: an existing branch is checked out, its commits intact.
	git(t, dir, "checkout", "develop")
	if err := g.EnsureFeatureBranch("feature/A", "develop"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Fatal("feature commit lost after re-checkout")
	}

	// MergeToBase refuses to run from the wrong branch.
	git(t, dir, "checkout", "develop")
	if err := g.MergeToBase("feature/A", "develop", "m"); err == nil ||
		!strings.Contains(err.Error(), `expected feature branch "feature/A"`) {
		t.Fatalf("want wrong-branch error, got %v", err)
	}
	git(t, dir, "checkout", "feature/A")

	// Happy merge: --no-ff commit on base, branch deleted.
	if err := g.MergeToBase("feature/A", "develop", "Merge feature/A"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := g.CurrentBranch(); cur != "develop" {
		t.Fatalf("after merge on %s, want develop", cur)
	}
	if out := git(t, dir, "branch", "--list", "feature/A"); out != "" {
		t.Fatalf("feature/A still exists: %s", out)
	}
	if log := git(t, dir, "log", "--merges", "--oneline"); !strings.Contains(log, "Merge feature/A") {
		t.Fatalf("no merge commit on develop:\n%s", log)
	}

	// Conflict: merge aborts and returns to the feature branch, base untouched.
	if err := g.EnsureFeatureBranch("feature/C", "develop"); err != nil {
		t.Fatal(err)
	}
	commitFile(t, dir, "base.txt", "feature change", "conflicting feature work")
	git(t, dir, "checkout", "develop")
	commitFile(t, dir, "base.txt", "develop change", "conflicting base work")
	baseSHA := git(t, dir, "rev-parse", "develop")
	git(t, dir, "checkout", "feature/C")
	err := g.MergeToBase("feature/C", "develop", "m")
	if err == nil || !strings.Contains(err.Error(), "aborted and returned to feature/C") {
		t.Fatalf("want abort error, got %v", err)
	}
	if cur, _ := g.CurrentBranch(); cur != "feature/C" {
		t.Fatalf("after abort on %s, want feature/C", cur)
	}
	if clean, _ := g.IsClean(); !clean {
		t.Fatal("worktree dirty after aborted merge")
	}
	if got := git(t, dir, "rev-parse", "develop"); got != baseSHA {
		t.Fatal("develop moved despite aborted merge")
	}
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
