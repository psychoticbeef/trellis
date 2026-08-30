package core

import (
	"fmt"
	"os/exec"
	"strings"

	"trellis/internal/gitops"
	"trellis/internal/model"
	"trellis/internal/testreport"
)

// Transition executes a story state-machine verb. The state machine:
//
//	todo --refine--> refined --start--> in_progress --finish--> done
//
// Any edit inside the tree automatically drops refined/in_progress back to
// todo (see downgradeAffected). There is no manual way to set a status.
func (e *Engine) Transition(storyID, action string) (string, error) {
	story, err := e.st.GetNode(e.pid(), storyID)
	if err != nil {
		return "", err
	}
	if story.Kind != model.KindStory {
		return "", fmt.Errorf("%s is a %s, not a story", storyID, story.Kind)
	}
	switch action {
	case "refine":
		return e.refine(story)
	case "start":
		return e.start(story)
	case "finish":
		return e.finish(story)
	case "abort":
		return e.abort(story)
	default:
		return "", fmt.Errorf("unknown action %q; valid actions: refine, start, finish, abort", action)
	}
}

func (e *Engine) requireStatus(story model.Node, want, action string) error {
	if story.Status != want {
		return fmt.Errorf("%s blocked: story %s is %q, %s requires %q", action, story.ID, story.Status, action, want)
	}
	return nil
}

func (e *Engine) requireIntegrity(storyID, action string) error {
	problems, err := e.integrity(storyID)
	if err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s blocked for %s:\n- %s", action, storyID, strings.Join(problems, "\n- "))
	}
	return nil
}

func (e *Engine) refine(story model.Node) (string, error) {
	if err := e.requireStatus(story, model.StatusTodo, "refine"); err != nil {
		return "", err
	}
	if err := e.requireIntegrity(story.ID, "refine"); err != nil {
		return "", err
	}
	if err := e.st.SetNodeStatus(e.pid(), story.ID, model.StatusRefined); err != nil {
		return "", err
	}
	e.st.AppendEvent(e.pid(), "transition", story.ID, "todo -> refined")
	return fmt.Sprintf("%s is now refined: tree complete, all nodes approved and fresh", story.ID), nil
}

func branchName(storyID string) string { return "feature/" + storyID }

func (e *Engine) git() (gitops.Git, error) {
	if e.Project.RepoPath == "" {
		return gitops.Git{}, fmt.Errorf("no repo configured for this project: run `trellis config %s --repo <path>`", e.pid())
	}
	return gitops.Git{Dir: e.Project.RepoPath}, nil
}

func (e *Engine) start(story model.Node) (string, error) {
	if err := e.requireStatus(story, model.StatusRefined, "start"); err != nil {
		return "", err
	}
	if err := e.requireIntegrity(story.ID, "start"); err != nil {
		return "", err
	}
	deps, err := e.st.ListDeps(e.pid(), story.ID)
	if err != nil {
		return "", err
	}
	var unfinished []string
	for _, d := range deps {
		target, err := e.st.GetNode(e.pid(), d.TargetID)
		if err != nil {
			return "", err
		}
		if target.Kind == model.KindStory && target.Status != model.StatusDone {
			unfinished = append(unfinished, fmt.Sprintf("%s (%s)", target.ID, target.Status))
		}
	}
	if len(unfinished) > 0 {
		return "", fmt.Errorf("start blocked: unfinished dependencies: %s", strings.Join(unfinished, ", "))
	}
	g, err := e.git()
	if err != nil {
		return "", err
	}
	branch := branchName(story.ID)
	if err := g.EnsureFeatureBranch(branch, e.Project.BaseBranch); err != nil {
		return "", fmt.Errorf("start blocked: %w", err)
	}
	if err := e.st.SetNodeStatus(e.pid(), story.ID, model.StatusInProgress); err != nil {
		return "", err
	}
	e.st.AppendEvent(e.pid(), "transition", story.ID, "refined -> in_progress on "+branch)
	return fmt.Sprintf("%s is in_progress; branch %s checked out (base %s)", story.ID, branch, e.Project.BaseBranch), nil
}

func (e *Engine) finish(story model.Node) (string, error) {
	if err := e.requireStatus(story, model.StatusInProgress, "finish"); err != nil {
		return "", err
	}
	if err := e.requireIntegrity(story.ID, "finish"); err != nil {
		return "", err
	}
	g, err := e.git()
	if err != nil {
		return "", err
	}
	branch := branchName(story.ID)
	cur, err := g.CurrentBranch()
	if err != nil {
		return "", err
	}
	if cur != branch {
		return "", fmt.Errorf("finish blocked: on branch %q, expected %q", cur, branch)
	}
	if clean, err := g.IsClean(); err != nil {
		return "", err
	} else if !clean {
		return "", fmt.Errorf("finish blocked: worktree not clean, commit all changes first")
	}
	// Gate on the future merge result: everything tested below must contain
	// the base tip, otherwise the merge commit itself was never exercised.
	if upToDate, err := g.IsAncestor(e.Project.BaseBranch, branch); err != nil {
		return "", err
	} else if !upToDate {
		return "", fmt.Errorf("finish blocked: %s is behind %s: merge or rebase %s into %s first, then finish again",
			branch, e.Project.BaseBranch, e.Project.BaseBranch, branch)
	}

	if e.Project.LintCmd != "" {
		if out, err := runShell(e.Project.RepoPath, e.Project.LintCmd); err != nil {
			return "", fmt.Errorf("finish blocked: lint failed (%s):\n%s", e.Project.LintCmd, tail(out, 40))
		}
	}
	if e.Project.TestCmd == "" {
		return "", fmt.Errorf("finish blocked: no test_cmd configured: run `trellis config %s --test <cmd> --junit <glob>`", e.pid())
	}
	if e.Project.JUnitGlob == "" {
		return "", fmt.Errorf("finish blocked: no junit_glob configured: run `trellis config %s --junit <glob>`", e.pid())
	}
	testOut, testErr := runShell(e.Project.RepoPath, e.Project.TestCmd)
	// The verdict comes from the parsed reports, not the exit code alone: a
	// failing exit code with parseable reports still yields precise per-spec
	// errors below.
	cases, parseErr := testreport.ParseGlob(e.Project.RepoPath, e.Project.JUnitGlob)
	if parseErr != nil {
		if testErr != nil {
			return "", fmt.Errorf("finish blocked: test command failed (%s):\n%s", e.Project.TestCmd, tail(testOut, 40))
		}
		return "", fmt.Errorf("finish blocked: %w", parseErr)
	}

	nodes, err := e.treeNodes(story.ID)
	if err != nil {
		return "", err
	}
	var specIDs []string
	for _, n := range nodes {
		if model.TestSpecKinds[n.Kind] {
			specIDs = append(specIDs, n.ID)
		}
	}
	if problems := testreport.Verify(specIDs, cases); len(problems) > 0 {
		return "", fmt.Errorf("finish blocked: test evidence incomplete for %s:\n- %s", story.ID, strings.Join(problems, "\n- "))
	}
	if testErr != nil {
		return "", fmt.Errorf("finish blocked: test command exited non-zero (%s) although all referenced specs pass — other tests are failing:\n%s", e.Project.TestCmd, tail(testOut, 40))
	}

	msg := fmt.Sprintf("Merge %s: %s (trellis finish)", branch, story.Title)
	if err := g.MergeToBase(branch, e.Project.BaseBranch, msg); err != nil {
		return "", fmt.Errorf("finish blocked: %w", err)
	}
	if err := e.st.SetNodeStatus(e.pid(), story.ID, model.StatusDone); err != nil {
		return "", err
	}
	e.st.AppendEvent(e.pid(), "transition", story.ID, "in_progress -> done, merged into "+e.Project.BaseBranch)
	return fmt.Sprintf("%s is done: %d test specs verified green, %s merged into %s, branch deleted", story.ID, len(specIDs), branch, e.Project.BaseBranch), nil
}

// abort abandons an in_progress story: the feature branch is discarded and
// the story falls back to refined — the spec survives, the work is dropped.
// Integrity is not re-checked: nothing about the spec changed.
func (e *Engine) abort(story model.Node) (string, error) {
	if err := e.requireStatus(story, model.StatusInProgress, "abort"); err != nil {
		return "", err
	}
	g, err := e.git()
	if err != nil {
		return "", err
	}
	branch := branchName(story.ID)
	if err := g.DiscardBranch(branch, e.Project.BaseBranch); err != nil {
		return "", fmt.Errorf("abort blocked: %w", err)
	}
	if err := e.st.SetNodeStatus(e.pid(), story.ID, model.StatusRefined); err != nil {
		return "", err
	}
	e.st.AppendEvent(e.pid(), "transition", story.ID, "in_progress -> refined (aborted, "+branch+" discarded)")
	return fmt.Sprintf("%s aborted: branch %s discarded, back on %s, story is refined again", story.ID, branch, e.Project.BaseBranch), nil
}

func runShell(dir, cmd string) (string, error) {
	c := exec.Command("sh", "-c", cmd)
	c.Dir = dir
	out, err := c.CombinedOutput()
	return string(out), err
}

func tail(s string, lines int) string {
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

// Prune deletes the whole tree of a done story. Truth lives in code and tests
// after finish; trellis keeps no war stories.
func (e *Engine) Prune(storyID string) error {
	story, err := e.st.GetNode(e.pid(), storyID)
	if err != nil {
		return err
	}
	if story.Kind != model.KindStory {
		return fmt.Errorf("%s is a %s, not a story", storyID, story.Kind)
	}
	if story.Status != model.StatusDone {
		return fmt.Errorf("prune blocked: story %s is %q, only done stories can be pruned", storyID, story.Status)
	}
	nodes, err := e.treeNodes(storyID)
	if err != nil {
		return err
	}
	// Delete leaves first; block if anything outside the tree depends on a tree node.
	inTree := map[string]bool{}
	for _, n := range nodes {
		inTree[n.ID] = true
	}
	for _, n := range nodes {
		deps, err := e.st.ListDependents(e.pid(), n.ID)
		if err != nil {
			return err
		}
		for _, d := range deps {
			if !inTree[d.NodeID] {
				return fmt.Errorf("prune blocked: %s depends on %s; unlink it first", d.NodeID, n.ID)
			}
		}
	}
	for i := len(nodes) - 1; i >= 0; i-- {
		if nodes[i].Kind == model.KindStory {
			if err := e.st.DeleteACsForStory(e.pid(), nodes[i].ID); err != nil {
				return err
			}
		}
		if err := e.st.DeleteNode(e.pid(), nodes[i].ID); err != nil {
			return err
		}
	}
	e.st.AppendEvent(e.pid(), "prune", storyID, fmt.Sprintf("%d nodes deleted", len(nodes)))
	return nil
}
