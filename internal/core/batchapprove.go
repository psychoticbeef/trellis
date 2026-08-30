package core

import (
	"fmt"
	"strings"

	"trellis/internal/model"
)

// ApproveTree approves a whole story tree in one call. The caller must pass
// the current content hash of every tree node and of every pinned dependency
// target (sequencing links are exempt) — the read-proof contract stays
// hash-per-content, at tree granularity. Validation is exhaustive and the
// batch applies all-or-nothing.
func (e *Engine) ApproveTree(storyID string, hashes, depHashes map[string]string) error {
	story, err := e.st.GetNode(e.pid(), storyID)
	if err != nil {
		return err
	}
	if story.Kind != model.KindStory {
		return fmt.Errorf("%s is a %s, not a story", storyID, story.Kind)
	}
	nodes, err := e.treeNodes(storyID)
	if err != nil {
		return err
	}

	var problems []string
	inTree := map[string]bool{}
	type approval struct {
		id, contentHash, parentHash string
		pins                        map[string]string
	}
	var apply []approval
	currentByID := map[string]string{}
	for _, n := range nodes {
		h, err := e.hashOf(n)
		if err != nil {
			return err
		}
		currentByID[n.ID] = h
	}
	for _, n := range nodes { // treeNodes is BFS: parents before children
		inTree[n.ID] = true
		current := currentByID[n.ID]
		seen, ok := hashes[n.ID]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("missing hash for %s — read the full tree (get_tree full=true) and pass every node's content_hash", n.ID))
			continue
		case seen != current:
			problems = append(problems, fmt.Sprintf("hash mismatch for %s — the node changed since you read it; re-read and retry", n.ID))
			continue
		}
		a := approval{id: n.ID, contentHash: current, pins: map[string]string{}}
		if n.ParentID != "" {
			a.parentHash = currentByID[n.ParentID]
		}
		deps, err := e.st.ListDeps(e.pid(), n.ID)
		if err != nil {
			return err
		}
		for _, d := range deps {
			if d.PinnedHash == "" {
				continue // sequencing link: no proof-of-reading required
			}
			target, err := e.st.GetNode(e.pid(), d.TargetID)
			if err != nil {
				return err
			}
			targetHash, err := e.hashOf(target)
			if err != nil {
				return err
			}
			seen, ok := depHashes[d.TargetID]
			switch {
			case !ok:
				problems = append(problems, fmt.Sprintf("missing dep_hashes entry for %s (dependency of %s): read it and pass its content_hash", d.TargetID, n.ID))
			case seen != targetHash:
				problems = append(problems, fmt.Sprintf("dep_hashes[%s] is stale — %s changed since you read it; re-read it", d.TargetID, d.TargetID))
			default:
				a.pins[d.TargetID] = targetHash
			}
		}
		apply = append(apply, a)
	}
	for id := range hashes {
		if !inTree[id] {
			problems = append(problems, fmt.Sprintf("hashes contains %s, which is not part of %s's tree", id, storyID))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("approve_tree rejected, nothing was approved:\n- %s", strings.Join(problems, "\n- "))
	}

	for _, a := range apply {
		if err := e.st.SetApproval(e.pid(), a.id, a.contentHash, a.parentHash); err != nil {
			return err
		}
		for tid, h := range a.pins {
			if err := e.st.PinDep(e.pid(), a.id, tid, h); err != nil {
				return err
			}
		}
	}
	e.st.AppendEvent(e.pid(), "approve_tree", storyID, fmt.Sprintf("%d nodes approved", len(apply)))
	return nil
}
