// Package gitops runs the git side effects of story transitions.
// The LLM never merges; trellis does, and only after every gate passed.
package gitops

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type Git struct {
	Dir string
}

func (g Git) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.Dir
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("git %s: %v\n%s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

func (g Git) IsRepo() bool {
	_, err := g.run("rev-parse", "--git-dir")
	return err == nil
}

func (g Git) CurrentBranch() (string, error) {
	return g.run("rev-parse", "--abbrev-ref", "HEAD")
}

func (g Git) BranchExists(branch string) bool {
	_, err := g.run("rev-parse", "--verify", "refs/heads/"+branch)
	return err == nil
}

func (g Git) IsClean() (bool, error) {
	out, err := g.run("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out == "", nil
}

// EnsureFeatureBranch checks out the feature branch, creating it from base if
// it does not exist yet. Requires a clean worktree.
func (g Git) EnsureFeatureBranch(branch, base string) error {
	if !g.IsRepo() {
		return fmt.Errorf("%s is not a git repository", g.Dir)
	}
	clean, err := g.IsClean()
	if err != nil {
		return err
	}
	if !clean {
		return fmt.Errorf("worktree not clean: commit or stash changes before switching branches")
	}
	if g.BranchExists(branch) {
		_, err := g.run("checkout", branch)
		return err
	}
	if !g.BranchExists(base) {
		return fmt.Errorf("base branch %q does not exist: create it first (git branch %s)", base, base)
	}
	_, err = g.run("checkout", "-b", branch, base)
	return err
}

// IsAncestor reports whether ancestor is an ancestor of (or equal to) the
// descendant ref.
func (g Git) IsAncestor(ancestor, descendant string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant)
	cmd.Dir = g.Dir
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %v", ancestor, descendant, err)
}

// MergeToBase merges the feature branch into base with --no-ff and deletes the
// feature branch. On merge failure it aborts and returns to the feature branch.
func (g Git) MergeToBase(branch, base, message string) error {
	clean, err := g.IsClean()
	if err != nil {
		return err
	}
	if !clean {
		return fmt.Errorf("worktree not clean: commit all changes before finishing")
	}
	cur, err := g.CurrentBranch()
	if err != nil {
		return err
	}
	if cur != branch {
		return fmt.Errorf("on branch %q, expected feature branch %q", cur, branch)
	}
	if _, err := g.run("checkout", base); err != nil {
		return err
	}
	if _, err := g.run("merge", "--no-ff", "-m", message, branch); err != nil {
		_, _ = g.run("merge", "--abort")
		_, _ = g.run("checkout", branch)
		return fmt.Errorf("merge into %s failed, aborted and returned to %s: %w", base, branch, err)
	}
	if _, err := g.run("branch", "-d", branch); err != nil {
		return fmt.Errorf("merged, but deleting branch failed: %w", err)
	}
	return nil
}
