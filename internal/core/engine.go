// Package core is the trellis engine: every mutation goes through here and is
// checked against the tree rules, approval hashes and the story state machine.
// Illegal moves are rejected with errors that list precisely what was illegal.
package core

import (
	"fmt"
	"sort"
	"strings"

	"trellis/internal/model"
	"trellis/internal/store"
)

type Engine struct {
	st      *store.Store
	Project store.Project
}

func NewEngine(st *store.Store, projectID string) (*Engine, error) {
	p, err := st.GetProject(projectID)
	if err != nil {
		return nil, err
	}
	return &Engine{st: st, Project: p}, nil
}

func (e *Engine) pid() string { return e.Project.ID }

func (e *Engine) ReloadProject() error {
	p, err := e.st.GetProject(e.pid())
	if err != nil {
		return err
	}
	e.Project = p
	return nil
}

// hashOf computes the current content hash of a node, loading the story's
// acceptance criteria when needed.
func (e *Engine) hashOf(n model.Node) (string, error) {
	var acs []model.AC
	if n.Kind == model.KindStory {
		var err error
		acs, err = e.st.ListACs(e.pid(), n.ID)
		if err != nil {
			return "", err
		}
	}
	return model.ContentHash(&n, acs), nil
}

// storyOf walks up the parent chain to the story root. Returns ok=false for
// nodes outside any story tree (cross-cutting specs).
func (e *Engine) storyOf(n model.Node) (model.Node, bool, error) {
	cur := n
	for cur.Kind != model.KindStory {
		if cur.ParentID == "" {
			return model.Node{}, false, nil
		}
		p, err := e.st.GetNode(e.pid(), cur.ParentID)
		if err != nil {
			return model.Node{}, false, err
		}
		cur = p
	}
	return cur, true, nil
}

// treeNodes returns the story plus all descendants, breadth-first.
func (e *Engine) treeNodes(storyID string) ([]model.Node, error) {
	root, err := e.st.GetNode(e.pid(), storyID)
	if err != nil {
		return nil, err
	}
	out := []model.Node{root}
	queue := []string{storyID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		children, err := e.st.ListChildren(e.pid(), id)
		if err != nil {
			return nil, err
		}
		for _, c := range children {
			out = append(out, c)
			queue = append(queue, c.ID)
		}
	}
	return out, nil
}

// ---- node CRUD ----

func (e *Engine) CreateNode(kind model.Kind, parentID, title, body string, covers []string) (model.Node, error) {
	if !model.ValidKind(kind) {
		return model.Node{}, fmt.Errorf("unknown kind %q; valid kinds: story, acceptance_test, arch, integration_test, detail_design, unit_test, cross_cutting", kind)
	}
	if strings.TrimSpace(title) == "" {
		return model.Node{}, fmt.Errorf("title must not be empty")
	}
	wantParent, needsParent := model.ParentKind(kind)
	var parent model.Node
	if needsParent {
		if parentID == "" {
			return model.Node{}, fmt.Errorf("kind %s requires a parent of kind %s", kind, wantParent)
		}
		var err error
		parent, err = e.st.GetNode(e.pid(), parentID)
		if err != nil {
			return model.Node{}, err
		}
		if parent.Kind != wantParent {
			return model.Node{}, fmt.Errorf("illegal parent: %s is a %s, but a %s must be child of a %s", parentID, parent.Kind, kind, wantParent)
		}
		if kind == model.KindArch {
			siblings, err := e.st.ListChildren(e.pid(), parentID)
			if err != nil {
				return model.Node{}, err
			}
			for _, s := range siblings {
				if s.Kind == model.KindArch {
					return model.Node{}, fmt.Errorf("story %s already has arch spec %s; a story has exactly one arch spec — update or delete it instead", parentID, s.ID)
				}
			}
		}
	} else if parentID != "" {
		return model.Node{}, fmt.Errorf("kind %s is a root node and must not have a parent", kind)
	}

	if len(covers) > 0 && kind != model.KindAcceptanceTest {
		return model.Node{}, fmt.Errorf("covers is only valid on acceptance_test nodes")
	}
	if kind == model.KindAcceptanceTest {
		if err := e.validateCovers(parent.ID, covers); err != nil {
			return model.Node{}, err
		}
	}

	num, err := e.st.NextID(e.pid(), model.Prefix(kind))
	if err != nil {
		return model.Node{}, err
	}
	n := model.Node{
		ID:        fmt.Sprintf("%s-%d", model.Prefix(kind), num),
		ProjectID: e.pid(),
		Kind:      kind,
		ParentID:  parentID,
		Title:     title,
		Body:      body,
		Covers:    covers,
	}
	if kind == model.KindStory {
		n.Status = model.StatusTodo
	}
	if err := e.st.InsertNode(n); err != nil {
		return model.Node{}, err
	}
	e.st.AppendEvent(e.pid(), "create", n.ID, fmt.Sprintf("kind=%s parent=%s title=%q", kind, parentID, title))
	if err := e.downgradeAffected(n, "node created"); err != nil {
		return model.Node{}, err
	}
	return e.st.GetNode(e.pid(), n.ID)
}

func (e *Engine) validateCovers(storyID string, covers []string) error {
	if len(covers) == 0 {
		return fmt.Errorf("an acceptance_test must declare which acceptance criteria it covers (covers: [\"%s.AC-1\", ...])", storyID)
	}
	acs, err := e.st.ListACs(e.pid(), storyID)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, ac := range acs {
		known[ac.ID] = true
	}
	var bad []string
	for _, c := range covers {
		if !known[c] {
			bad = append(bad, c)
		}
	}
	if len(bad) > 0 {
		var have []string
		for _, ac := range acs {
			have = append(have, ac.ID)
		}
		sort.Strings(have)
		return fmt.Errorf("covers references unknown acceptance criteria %v; story %s has: %v", bad, storyID, have)
	}
	return nil
}

// UpdateNode applies a partial content update. Nil fields stay unchanged.
// Approval invalidation is implicit: the content hash changes, so the node is
// no longer approved and all children/dependents go stale.
func (e *Engine) UpdateNode(id string, title, body *string, covers *[]string) (model.Node, error) {
	n, err := e.st.GetNode(e.pid(), id)
	if err != nil {
		return model.Node{}, err
	}
	if title == nil && body == nil && covers == nil {
		return model.Node{}, fmt.Errorf("nothing to update: provide title, body and/or covers")
	}
	if title != nil {
		if strings.TrimSpace(*title) == "" {
			return model.Node{}, fmt.Errorf("title must not be empty")
		}
		n.Title = *title
	}
	if body != nil {
		n.Body = *body
	}
	if covers != nil {
		if n.Kind != model.KindAcceptanceTest {
			return model.Node{}, fmt.Errorf("covers is only valid on acceptance_test nodes")
		}
		if err := e.validateCovers(n.ParentID, *covers); err != nil {
			return model.Node{}, err
		}
		n.Covers = *covers
	}
	if err := e.st.UpdateNodeContent(n); err != nil {
		return model.Node{}, err
	}
	e.st.AppendEvent(e.pid(), "update", n.ID, "content changed")
	if err := e.downgradeAffected(n, "node "+n.ID+" edited"); err != nil {
		return model.Node{}, err
	}
	return e.st.GetNode(e.pid(), n.ID)
}

func (e *Engine) DeleteNode(id string) error {
	n, err := e.st.GetNode(e.pid(), id)
	if err != nil {
		return err
	}
	children, err := e.st.ListChildren(e.pid(), id)
	if err != nil {
		return err
	}
	dependents, err := e.st.ListDependents(e.pid(), id)
	if err != nil {
		return err
	}
	var blocks []string
	for _, c := range children {
		blocks = append(blocks, fmt.Sprintf("child %s", c.ID))
	}
	for _, d := range dependents {
		blocks = append(blocks, fmt.Sprintf("dependent %s", d.NodeID))
	}
	if len(blocks) > 0 {
		return fmt.Errorf("delete blocked: %s is referenced by %s; delete or relink those first", id, strings.Join(blocks, ", "))
	}
	if n.Kind == model.KindStory {
		if err := e.st.DeleteACsForStory(e.pid(), id); err != nil {
			return err
		}
	}
	if err := e.st.DeleteNode(e.pid(), id); err != nil {
		return err
	}
	e.st.AppendEvent(e.pid(), "delete", id, fmt.Sprintf("kind=%s title=%q", n.Kind, n.Title))
	return e.downgradeAffected(n, "node "+id+" deleted")
}

// ---- acceptance criteria ----

func (e *Engine) AddAC(storyID, given, when, then string) (model.AC, error) {
	story, err := e.st.GetNode(e.pid(), storyID)
	if err != nil {
		return model.AC{}, err
	}
	if story.Kind != model.KindStory {
		return model.AC{}, fmt.Errorf("%s is a %s; acceptance criteria belong to stories", storyID, story.Kind)
	}
	if strings.TrimSpace(given) == "" || strings.TrimSpace(when) == "" || strings.TrimSpace(then) == "" {
		return model.AC{}, fmt.Errorf("given, when and then must all be non-empty")
	}
	num, err := e.st.NextID(e.pid(), "ac:"+storyID)
	if err != nil {
		return model.AC{}, err
	}
	ac := model.AC{
		ID:       fmt.Sprintf("%s.AC-%d", storyID, num),
		StoryID:  storyID,
		Given:    given,
		When:     when,
		Then:     then,
		Position: num,
	}
	if err := e.st.InsertAC(e.pid(), ac); err != nil {
		return model.AC{}, err
	}
	e.st.AppendEvent(e.pid(), "ac_add", storyID, ac.ID)
	if err := e.downgradeAffected(story, "acceptance criterion added"); err != nil {
		return model.AC{}, err
	}
	return ac, nil
}

func (e *Engine) UpdateAC(acID string, given, when, then *string) (model.AC, error) {
	ac, err := e.st.GetAC(e.pid(), acID)
	if err != nil {
		return model.AC{}, err
	}
	if given != nil {
		ac.Given = *given
	}
	if when != nil {
		ac.When = *when
	}
	if then != nil {
		ac.Then = *then
	}
	if strings.TrimSpace(ac.Given) == "" || strings.TrimSpace(ac.When) == "" || strings.TrimSpace(ac.Then) == "" {
		return model.AC{}, fmt.Errorf("given, when and then must all be non-empty")
	}
	if err := e.st.UpdateAC(e.pid(), ac); err != nil {
		return model.AC{}, err
	}
	story, err := e.st.GetNode(e.pid(), ac.StoryID)
	if err != nil {
		return model.AC{}, err
	}
	e.st.AppendEvent(e.pid(), "ac_update", ac.StoryID, acID)
	if err := e.downgradeAffected(story, "acceptance criterion "+acID+" edited"); err != nil {
		return model.AC{}, err
	}
	return ac, nil
}

func (e *Engine) DeleteAC(acID string) error {
	ac, err := e.st.GetAC(e.pid(), acID)
	if err != nil {
		return err
	}
	// An AC referenced by an acceptance test must not silently vanish.
	tests, err := e.st.ListChildren(e.pid(), ac.StoryID)
	if err != nil {
		return err
	}
	var refs []string
	for _, t := range tests {
		for _, c := range t.Covers {
			if c == acID {
				refs = append(refs, t.ID)
			}
		}
	}
	if len(refs) > 0 {
		return fmt.Errorf("delete blocked: %s is covered by acceptance tests %v; update their covers first", acID, refs)
	}
	if err := e.st.DeleteAC(e.pid(), acID); err != nil {
		return err
	}
	story, err := e.st.GetNode(e.pid(), ac.StoryID)
	if err != nil {
		return err
	}
	e.st.AppendEvent(e.pid(), "ac_delete", ac.StoryID, acID)
	return e.downgradeAffected(story, "acceptance criterion "+acID+" deleted")
}

// ---- dependencies (cross-cutting references) ----

func (e *Engine) LinkDep(nodeID, targetID string) error {
	if nodeID == targetID {
		return fmt.Errorf("a node cannot depend on itself")
	}
	node, err := e.st.GetNode(e.pid(), nodeID)
	if err != nil {
		return err
	}
	target, err := e.st.GetNode(e.pid(), targetID)
	if err != nil {
		return err
	}
	// Story-to-story links are sequencing edges: no hash pin, no freshness
	// coupling — only start gates on them. They may target any story state,
	// but must not form a cycle.
	if node.Kind == model.KindStory && target.Kind == model.KindStory {
		if cycle, err := e.sequencingCycle(nodeID, targetID); err != nil {
			return err
		} else if cycle != "" {
			return fmt.Errorf("link blocked: sequencing cycle %s", cycle)
		}
		if err := e.st.LinkDep(e.pid(), model.Dep{NodeID: nodeID, TargetID: targetID, PinnedHash: ""}); err != nil {
			return err
		}
		e.st.AppendEvent(e.pid(), "link", nodeID, "sequencing depends_on "+targetID)
		return nil
	}
	fresh, reasons, err := e.freshness(target)
	if err != nil {
		return err
	}
	if !fresh {
		return fmt.Errorf("link blocked: target %s is not approved/fresh: %s", targetID, strings.Join(reasons, "; "))
	}
	hash, err := e.hashOf(target)
	if err != nil {
		return err
	}
	if err := e.st.LinkDep(e.pid(), model.Dep{NodeID: nodeID, TargetID: targetID, PinnedHash: hash}); err != nil {
		return err
	}
	e.st.AppendEvent(e.pid(), "link", nodeID, "depends_on "+targetID)
	return nil
}

// sequencingCycle reports the story chain that linking from -> to would close
// into a cycle, or "" when the link is safe.
func (e *Engine) sequencingCycle(from, to string) (string, error) {
	path := []string{from, to}
	var walk func(cur string) (string, error)
	seen := map[string]bool{}
	walk = func(cur string) (string, error) {
		if cur == from {
			return strings.Join(path, " -> "), nil
		}
		if seen[cur] {
			return "", nil
		}
		seen[cur] = true
		deps, err := e.st.ListDeps(e.pid(), cur)
		if err != nil {
			return "", err
		}
		for _, d := range deps {
			target, err := e.st.GetNode(e.pid(), d.TargetID)
			if err != nil {
				return "", err
			}
			if target.Kind != model.KindStory {
				continue
			}
			path = append(path, d.TargetID)
			if cycle, err := walk(d.TargetID); err != nil || cycle != "" {
				return cycle, err
			}
			path = path[:len(path)-1]
		}
		return "", nil
	}
	return walk(to)
}

func (e *Engine) UnlinkDep(nodeID, targetID string) error {
	if err := e.st.UnlinkDep(e.pid(), nodeID, targetID); err != nil {
		return err
	}
	e.st.AppendEvent(e.pid(), "unlink", nodeID, "depends_on "+targetID)
	return nil
}

// ---- approval ----

// Approve marks a node as reviewed. The caller must pass the hash of the
// content it reviewed (proof of reading); for every dependency it must pass
// the hash of the dependency target it reviewed. Approval re-pins all
// dependency edges.
func (e *Engine) Approve(nodeID, contentHash string, depHashes map[string]string) error {
	n, err := e.st.GetNode(e.pid(), nodeID)
	if err != nil {
		return err
	}
	current, err := e.hashOf(n)
	if err != nil {
		return err
	}
	if contentHash != current {
		return fmt.Errorf("approve rejected: hash mismatch for %s — the node changed since you read it; call get_node(%s), review the current content, then approve with its hash", nodeID, nodeID)
	}
	if n.ParentID != "" {
		parent, err := e.st.GetNode(e.pid(), n.ParentID)
		if err != nil {
			return err
		}
		fresh, reasons, err := e.freshness(parent)
		if err != nil {
			return err
		}
		if !fresh {
			return fmt.Errorf("approve rejected: parent %s must be approved first (top-down): %s", parent.ID, strings.Join(reasons, "; "))
		}
	}
	deps, err := e.st.ListDeps(e.pid(), nodeID)
	if err != nil {
		return err
	}
	var problems []string
	pins := map[string]string{}
	sequencing := map[string]bool{}
	for _, d := range deps {
		if d.PinnedHash == "" {
			sequencing[d.TargetID] = true
			continue // sequencing edge: no proof-of-reading required
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
			problems = append(problems, fmt.Sprintf("missing dep_hashes entry for %s: read it and pass its content_hash", d.TargetID))
		case seen != targetHash:
			problems = append(problems, fmt.Sprintf("dep_hashes[%s] is stale — %s changed since you read it; re-read it", d.TargetID, d.TargetID))
		default:
			pins[d.TargetID] = targetHash
		}
	}
	for tid := range depHashes {
		if sequencing[tid] {
			continue // provided but not needed; harmless
		}
		found := false
		for _, d := range deps {
			if d.TargetID == tid {
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("dep_hashes contains %s, but %s has no dependency on it", tid, nodeID))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("approve rejected:\n- %s", strings.Join(problems, "\n- "))
	}

	parentHash := ""
	if n.ParentID != "" {
		parent, err := e.st.GetNode(e.pid(), n.ParentID)
		if err != nil {
			return err
		}
		parentHash, err = e.hashOf(parent)
		if err != nil {
			return err
		}
	}
	if err := e.st.SetApproval(e.pid(), nodeID, current, parentHash); err != nil {
		return err
	}
	for tid, h := range pins {
		if err := e.st.PinDep(e.pid(), nodeID, tid, h); err != nil {
			return err
		}
	}
	e.st.AppendEvent(e.pid(), "approve", nodeID, "content "+short(current))
	return nil
}

// downgradeAffected reverts every story whose gate the mutation broke back to
// todo: the story owning the mutated node plus the stories of all direct
// dependents. done stories stay done — their truth lives in merged code.
func (e *Engine) downgradeAffected(mutated model.Node, why string) error {
	affected := map[string]bool{}
	if story, ok, err := e.storyOf(mutated); err != nil {
		return err
	} else if ok {
		affected[story.ID] = true
	}
	dependents, err := e.st.ListDependents(e.pid(), mutated.ID)
	if err != nil {
		return err
	}
	for _, d := range dependents {
		dn, err := e.st.GetNode(e.pid(), d.NodeID)
		if err != nil {
			return err
		}
		if story, ok, err := e.storyOf(dn); err != nil {
			return err
		} else if ok {
			affected[story.ID] = true
		}
	}
	for id := range affected {
		story, err := e.st.GetNode(e.pid(), id)
		if err != nil {
			return err
		}
		if story.Status == model.StatusRefined || story.Status == model.StatusInProgress {
			if err := e.st.SetNodeStatus(e.pid(), id, model.StatusTodo); err != nil {
				return err
			}
			e.st.AppendEvent(e.pid(), "auto_downgrade", id, fmt.Sprintf("%s -> todo: %s", story.Status, why))
		}
	}
	return nil
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
