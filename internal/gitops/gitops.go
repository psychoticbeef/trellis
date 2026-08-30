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

// MergeBranch merges a feature branch into base with --no-ff via the main
// worktree, which must be clean; it is checked out on base first. On merge
// failure the merge is aborted and the main worktree stays on base.
func (g Git) MergeBranch(branch, base, message string) error {
	clean, err := g.IsClean()
	if err != nil {
		return err
	}
	if !clean {
		return fmt.Errorf("main worktree not clean: commit or stash its changes before finishing")
	}
	if _, err := g.run("checkout", base); err != nil {
		return err
	}
	if _, err := g.run("merge", "--no-ff", "-m", message, branch); err != nil {
		_, _ = g.run("merge", "--abort")
		return fmt.Errorf("merge into %s failed and was aborted: %w", base, err)
	}
	return nil
}

// WorktreeAdd creates a worktree at path, reusing an existing branch or
// creating it fresh from base.
func (g Git) WorktreeAdd(path, branch, base string) error {
	if g.BranchExists(branch) {
		_, err := g.run("worktree", "add", path, branch)
		return err
	}
	if !g.BranchExists(base) {
		return fmt.Errorf("base branch %q does not exist: create it first (git branch %s)", base, base)
	}
	_, err := g.run("worktree", "add", "-b", branch, path, base)
	return err
}

// WorktreeRemove removes a worktree registration and its directory.
func (g Git) WorktreeRemove(path string) error {
	_, err := g.run("worktree", "remove", "--force", path)
	return err
}

// DeleteBranch force-deletes a branch (used after its worktree is gone).
func (g Git) DeleteBranch(branch string) error {
	_, err := g.run("branch", "-D", branch)
	return err
}

// IsIgnored reports whether a repo-relative path is covered by .gitignore.
func (g Git) IsIgnored(rel string) bool {
	cmd := exec.Command("git", "check-ignore", "-q", rel)
	cmd.Dir = g.Dir
	return cmd.Run() == nil
}
