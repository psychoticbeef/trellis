package core

import (
	"fmt"
	"sort"

	"trellis/internal/model"
	"trellis/internal/store"
	"trellis/internal/testreport"
)

// freshness decides whether a node's approval is still valid. A node is fresh
// iff it was approved, its content is unchanged since approval, its parent is
// unchanged since approval, and every dependency target is unchanged since its
// pin. Reasons list everything that is wrong, machine-readably.
func (e *Engine) freshness(n model.Node) (bool, []string, error) {
	var reasons []string
	current, err := e.hashOf(n)
	if err != nil {
		return false, nil, err
	}
	switch {
	case n.ApprovedContentHash == "":
		reasons = append(reasons, fmt.Sprintf("%s never approved", n.ID))
	case n.ApprovedContentHash != current:
		reasons = append(reasons, fmt.Sprintf("%s changed since approval (approved %s, now %s)", n.ID, short(n.ApprovedContentHash), short(current)))
	}
	if n.ParentID != "" && n.ApprovedContentHash != "" {
		parent, err := e.st.GetNode(e.pid(), n.ParentID)
		if err != nil {
			return false, nil, err
		}
		parentHash, err := e.hashOf(parent)
		if err != nil {
			return false, nil, err
		}
		if n.ApprovedParentHash != parentHash {
			reasons = append(reasons, fmt.Sprintf("%s stale: parent %s changed since approval (approved against %s, now %s)", n.ID, parent.ID, short(n.ApprovedParentHash), short(parentHash)))
		}
	}
	deps, err := e.st.ListDeps(e.pid(), n.ID)
	if err != nil {
		return false, nil, err
	}
	for _, d := range deps {
		if d.PinnedHash == "" {
			continue // sequencing edge: no freshness coupling
		}
		target, err := e.st.GetNode(e.pid(), d.TargetID)
		if err != nil {
			return false, nil, err
		}
		targetHash, err := e.hashOf(target)
		if err != nil {
			return false, nil, err
		}
		if d.PinnedHash != targetHash {
			reasons = append(reasons, fmt.Sprintf("%s stale: dependency %s changed since pin (pinned %s, now %s)", n.ID, d.TargetID, short(d.PinnedHash), short(targetHash)))
		}
	}
	return len(reasons) == 0, reasons, nil
}

// integrity returns everything that blocks refine/start/finish for a story:
// structural completeness of the tree, AC coverage, and freshness of every
// node. Empty result means the gate is open.
func (e *Engine) integrity(storyID string) ([]string, error) {
	story, err := e.st.GetNode(e.pid(), storyID)
	if err != nil {
		return nil, err
	}
	if story.Kind != model.KindStory {
		return nil, fmt.Errorf("%s is a %s, not a story", storyID, story.Kind)
	}
	var problems []string

	acs, err := e.st.ListACs(e.pid(), storyID)
	if err != nil {
		return nil, err
	}
	if len(acs) == 0 {
		problems = append(problems, "story has no acceptance criteria")
	}

	children, err := e.st.ListChildren(e.pid(), storyID)
	if err != nil {
		return nil, err
	}
	var arch *model.Node
	covered := map[string][]string{}
	nATs := 0
	for i := range children {
		c := children[i]
		switch c.Kind {
		case model.KindArch:
			arch = &children[i]
		case model.KindAcceptanceTest:
			nATs++
			for _, acID := range c.Covers {
				covered[acID] = append(covered[acID], c.ID)
			}
		}
	}
	if nATs == 0 {
		problems = append(problems, "story has no acceptance test specs")
	}
	for _, ac := range acs {
		if len(covered[ac.ID]) == 0 {
			problems = append(problems, fmt.Sprintf("acceptance criterion %s is not covered by any acceptance test spec", ac.ID))
		}
	}
	if arch == nil {
		problems = append(problems, "story has no arch spec")
	} else {
		archChildren, err := e.st.ListChildren(e.pid(), arch.ID)
		if err != nil {
			return nil, err
		}
		nITs, nDDs := 0, 0
		for _, c := range archChildren {
			switch c.Kind {
			case model.KindIntegrationTest:
				nITs++
			case model.KindDetailDesign:
				nDDs++
				uts, err := e.st.ListChildren(e.pid(), c.ID)
				if err != nil {
					return nil, err
				}
				if len(uts) == 0 {
					problems = append(problems, fmt.Sprintf("detail design %s has no unit test specs", c.ID))
				}
			}
		}
		if nITs == 0 {
			problems = append(problems, fmt.Sprintf("arch spec %s has no integration test specs", arch.ID))
		}
		if nDDs == 0 {
			problems = append(problems, fmt.Sprintf("arch spec %s has no detail designs", arch.ID))
		}
	}

	nodes, err := e.treeNodes(storyID)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if fresh, reasons, err := e.freshness(n); err != nil {
			return nil, err
		} else if !fresh {
			problems = append(problems, reasons...)
		}
	}
	return problems, nil
}

// ---- reports ----

type EvidenceInfo struct {
	Tests      []string `json:"tests"`
	RecordedAt string   `json:"recorded_at"`
}

type DepInfo struct {
	Target     string `json:"target"`
	Fresh      bool   `json:"fresh"`
	Sequencing bool   `json:"sequencing,omitempty"`
}

type TreeNode struct {
	ID       string        `json:"id"`
	Kind     string        `json:"kind"`
	Title    string        `json:"title"`
	Body     string        `json:"body,omitempty"`
	Hash     string        `json:"content_hash"`
	Fresh    bool          `json:"fresh"`
	Problems []string      `json:"problems,omitempty"`
	Covers   []string      `json:"covers,omitempty"`
	Paths    []string      `json:"paths,omitempty"`
	Activity string        `json:"activity,omitempty"`
	Rank     *int          `json:"rank,omitempty"`
	Slice    *int          `json:"slice,omitempty"`
	Deps     []DepInfo     `json:"depends_on,omitempty"`
	Evidence *EvidenceInfo `json:"evidence,omitempty"`
	Children []TreeNode    `json:"children,omitempty"`
}

type ACInfo struct {
	ID        string   `json:"id"`
	Given     string   `json:"given"`
	When      string   `json:"when"`
	Then      string   `json:"then"`
	CoveredBy []string `json:"covered_by"`
}

type TreeReport struct {
	Story     TreeNode `json:"story"`
	Status    string   `json:"status"`
	ACs       []ACInfo `json:"acceptance_criteria"`
	Integrity []string `json:"blocking_problems"`
}

// Tree renders the story report without bodies; TreeFull includes them so a
// single call carries the complete content (the read side of approve_tree).
func (e *Engine) Tree(storyID string) (TreeReport, error) { return e.tree(storyID, false) }

func (e *Engine) TreeFull(storyID string) (TreeReport, error) { return e.tree(storyID, true) }

func (e *Engine) tree(storyID string, full bool) (TreeReport, error) {
	story, err := e.st.GetNode(e.pid(), storyID)
	if err != nil {
		return TreeReport{}, err
	}
	if story.Kind != model.KindStory {
		return TreeReport{}, fmt.Errorf("%s is a %s, not a story", storyID, story.Kind)
	}
	root, err := e.treeNode(story, full)
	if err != nil {
		return TreeReport{}, err
	}
	acs, err := e.st.ListACs(e.pid(), storyID)
	if err != nil {
		return TreeReport{}, err
	}
	children, err := e.st.ListChildren(e.pid(), storyID)
	if err != nil {
		return TreeReport{}, err
	}
	covered := map[string][]string{}
	for _, c := range children {
		if c.Kind == model.KindAcceptanceTest {
			for _, acID := range c.Covers {
				covered[acID] = append(covered[acID], c.ID)
			}
		}
	}
	report := TreeReport{Story: root, Status: story.Status}
	for _, ac := range acs {
		cb := covered[ac.ID]
		if cb == nil {
			cb = []string{}
		}
		report.ACs = append(report.ACs, ACInfo{ID: ac.ID, Given: ac.Given, When: ac.When, Then: ac.Then, CoveredBy: cb})
	}
	report.Integrity, err = e.integrity(storyID)
	if err != nil {
		return TreeReport{}, err
	}
	if report.Integrity == nil {
		report.Integrity = []string{}
	}
	return report, nil
}

func (e *Engine) treeNode(n model.Node, full bool) (TreeNode, error) {
	hash, err := e.hashOf(n)
	if err != nil {
		return TreeNode{}, err
	}
	fresh, reasons, err := e.freshness(n)
	if err != nil {
		return TreeNode{}, err
	}
	tn := TreeNode{ID: n.ID, Kind: string(n.Kind), Title: n.Title, Hash: hash, Fresh: fresh, Problems: reasons, Covers: n.Covers, Paths: n.Paths}
	if n.Kind == model.KindStory && n.ActivityID != "" {
		tn.Activity = n.ActivityID
		if n.Rank > 0 && n.Slice > 0 {
			rank, slice := n.Rank, n.Slice
			tn.Rank, tn.Slice = &rank, &slice
		}
	}
	if full {
		tn.Body = n.Body
	}
	if model.TestSpecKinds[n.Kind] {
		if ev, ok, err := e.st.GetEvidence(e.pid(), n.ID); err != nil {
			return TreeNode{}, err
		} else if ok {
			tn.Evidence = &EvidenceInfo{Tests: ev.Tests, RecordedAt: ev.RecordedAt}
		}
	}
	deps, err := e.st.ListDeps(e.pid(), n.ID)
	if err != nil {
		return TreeNode{}, err
	}
	for _, d := range deps {
		if d.PinnedHash == "" {
			tn.Deps = append(tn.Deps, DepInfo{Target: d.TargetID, Fresh: true, Sequencing: true})
			continue
		}
		target, err := e.st.GetNode(e.pid(), d.TargetID)
		if err != nil {
			return TreeNode{}, err
		}
		targetHash, err := e.hashOf(target)
		if err != nil {
			return TreeNode{}, err
		}
		tn.Deps = append(tn.Deps, DepInfo{Target: d.TargetID, Fresh: d.PinnedHash == targetHash})
	}
	children, err := e.st.ListChildren(e.pid(), n.ID)
	if err != nil {
		return TreeNode{}, err
	}
	for _, c := range children {
		child, err := e.treeNode(c, full)
		if err != nil {
			return TreeNode{}, err
		}
		tn.Children = append(tn.Children, child)
	}
	return tn, nil
}

type NodeReport struct {
	ID       string        `json:"id"`
	Kind     string        `json:"kind"`
	ParentID string        `json:"parent_id,omitempty"`
	Title    string        `json:"title"`
	Body     string        `json:"body"`
	Covers   []string      `json:"covers,omitempty"`
	Paths    []string      `json:"paths,omitempty"`
	Status   string        `json:"status,omitempty"`
	Position *int          `json:"position,omitempty"`
	Activity string        `json:"activity,omitempty"`
	Rank     *int          `json:"rank,omitempty"`
	Slice    *int          `json:"slice,omitempty"`
	Hash     string        `json:"content_hash"`
	Fresh    bool          `json:"fresh"`
	Problems []string      `json:"problems,omitempty"`
	Deps     []NodeDep     `json:"depends_on,omitempty"`
	Evidence *EvidenceInfo `json:"evidence,omitempty"`
	ACs      []ACInfo      `json:"acceptance_criteria,omitempty"`
}

type NodeDep struct {
	Target      string `json:"target"`
	TargetHash  string `json:"target_content_hash"`
	TargetTitle string `json:"target_title"`
	Fresh       bool   `json:"fresh"`
	Sequencing  bool   `json:"sequencing,omitempty"`
}

// Node returns the full content of one node, including the hashes needed to
// approve it: its own content hash plus the current hash of every dependency.
func (e *Engine) Node(id string) (NodeReport, error) {
	n, err := e.st.GetNode(e.pid(), id)
	if err != nil {
		return NodeReport{}, err
	}
	hash, err := e.hashOf(n)
	if err != nil {
		return NodeReport{}, err
	}
	fresh, reasons, err := e.freshness(n)
	if err != nil {
		return NodeReport{}, err
	}
	r := NodeReport{
		ID: n.ID, Kind: string(n.Kind), ParentID: n.ParentID, Title: n.Title, Body: n.Body,
		Covers: n.Covers, Paths: n.Paths, Status: n.Status, Hash: hash, Fresh: fresh, Problems: reasons,
	}
	if n.Kind == model.KindActivity {
		position := n.Position
		r.Position = &position
	}
	if n.Kind == model.KindStory && n.ActivityID != "" {
		r.Activity = n.ActivityID
		if n.Rank > 0 && n.Slice > 0 {
			rank, slice := n.Rank, n.Slice
			r.Rank, r.Slice = &rank, &slice
		}
	}
	if model.TestSpecKinds[n.Kind] {
		if ev, ok, err := e.st.GetEvidence(e.pid(), n.ID); err != nil {
			return NodeReport{}, err
		} else if ok {
			r.Evidence = &EvidenceInfo{Tests: ev.Tests, RecordedAt: ev.RecordedAt}
		}
	}
	deps, err := e.st.ListDeps(e.pid(), n.ID)
	if err != nil {
		return NodeReport{}, err
	}
	for _, d := range deps {
		target, err := e.st.GetNode(e.pid(), d.TargetID)
		if err != nil {
			return NodeReport{}, err
		}
		targetHash, err := e.hashOf(target)
		if err != nil {
			return NodeReport{}, err
		}
		if d.PinnedHash == "" {
			r.Deps = append(r.Deps, NodeDep{Target: d.TargetID, TargetHash: targetHash, TargetTitle: target.Title, Fresh: true, Sequencing: true})
			continue
		}
		r.Deps = append(r.Deps, NodeDep{Target: d.TargetID, TargetHash: targetHash, TargetTitle: target.Title, Fresh: d.PinnedHash == targetHash})
	}
	if n.Kind == model.KindStory {
		acs, err := e.st.ListACs(e.pid(), n.ID)
		if err != nil {
			return NodeReport{}, err
		}
		for _, ac := range acs {
			r.ACs = append(r.ACs, ACInfo{ID: ac.ID, Given: ac.Given, When: ac.When, Then: ac.Then, CoveredBy: []string{}})
		}
	}
	return r, nil
}

type StorySummary struct {
	ID                        string `json:"id"`
	Title                     string `json:"title"`
	Status                    string `json:"status"`
	Ready                     bool   `json:"gates_open"`
	Activity                  string `json:"activity,omitempty"`
	Rank                      *int   `json:"rank,omitempty"`
	Slice                     *int   `json:"slice,omitempty"`
	TokensMain                *int64 `json:"tokens_main,omitempty"`
	TokensSubagents           *int64 `json:"tokens_subagents,omitempty"`
	TokensMainInput           *int64 `json:"tokens_main_input,omitempty"`
	TokensMainOutput          *int64 `json:"tokens_main_output,omitempty"`
	TokensMainCacheRead       *int64 `json:"tokens_main_cache_read,omitempty"`
	TokensMainCacheWrite      *int64 `json:"tokens_main_cache_write,omitempty"`
	TokensSubagentsInput      *int64 `json:"tokens_subagents_input,omitempty"`
	TokensSubagentsOutput     *int64 `json:"tokens_subagents_output,omitempty"`
	TokensSubagentsCacheRead  *int64 `json:"tokens_subagents_cache_read,omitempty"`
	TokensSubagentsCacheWrite *int64 `json:"tokens_subagents_cache_write,omitempty"`
	Usage                     string `json:"usage,omitempty"`
}

type ActivitySummary struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Position int    `json:"position"`
}

type CCSummary struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Accepted bool   `json:"accepted"`
}

type CoverageSummary struct {
	TotalPct   float64        `json:"total_pct"`
	DeltaPct   *float64       `json:"delta_pct,omitempty"` // vs the previous snapshot; nil on the first
	RecordedAt string         `json:"recorded_at"`
	Gaps       []CoverageFile `json:"gaps"`
}

type CoverageFile struct {
	File string  `json:"file"`
	Pct  float64 `json:"pct"`
}

type Overview struct {
	Project      string            `json:"project"`
	Description  string            `json:"description,omitempty"`
	Coverage     *CoverageSummary  `json:"coverage,omitempty"`
	Activities   []ActivitySummary `json:"activities,omitempty"`
	StoryMap     *StoryMapOverview `json:"story_map,omitempty"`
	Stories      []StorySummary    `json:"stories"`
	CrossCutting []CCSummary       `json:"cross_cutting"`
	Glossary     []store.TermDef   `json:"glossary"`
	StaleNodes   []string          `json:"stale_nodes"`
}

func (e *Engine) Overview() (Overview, error) {
	o := Overview{Project: e.Project.Name, Description: e.Project.Description, Stories: []StorySummary{}, CrossCutting: []CCSummary{}, Glossary: []store.TermDef{}, StaleNodes: []string{}}
	if terms, err := e.st.ListTerms(e.pid()); err != nil {
		return o, err
	} else if terms != nil {
		o.Glossary = terms
	}
	if rows, err := e.st.ListCoverage(e.pid()); err != nil {
		return o, err
	} else if len(rows) > 0 {
		files := make([]testreport.FileCov, 0, len(rows))
		for _, r := range rows {
			files = append(files, testreport.FileCov{Path: r.File, Covered: r.Covered, Total: r.Total})
		}
		total, worst := testreport.Summarize(files, 10)
		cs := &CoverageSummary{TotalPct: total, RecordedAt: rows[0].RecordedAt, Gaps: []CoverageFile{}}
		if prev, ok, err := e.st.CoveragePrevTotal(e.pid()); err == nil && ok {
			d := total - prev
			cs.DeltaPct = &d
		}
		for _, w := range worst {
			if w.Pct() < 100 {
				cs.Gaps = append(cs.Gaps, CoverageFile{File: w.Path, Pct: w.Pct()})
			}
		}
		o.Coverage = cs
	}
	activities, err := e.st.ListActivities(e.pid())
	if err != nil {
		return o, err
	}
	for _, activity := range activities {
		o.Activities = append(o.Activities, ActivitySummary{ID: activity.ID, Title: activity.Title, Position: activity.Position})
	}
	stories, err := e.st.ListNodesByKind(e.pid(), model.KindStory)
	if err != nil {
		return o, err
	}
	for _, s := range stories {
		problems, err := e.integrity(s.ID)
		if err != nil {
			return o, err
		}
		summary := StorySummary{ID: s.ID, Title: s.Title, Status: s.Status, Ready: len(problems) == 0}
		if s.ActivityID != "" {
			summary.Activity = s.ActivityID
			if s.Rank > 0 && s.Slice > 0 {
				rank, slice := s.Rank, s.Slice
				summary.Rank, summary.Slice = &rank, &slice
			}
		}
		usage, ok, err := e.st.GetStoryUsage(e.pid(), s.ID)
		if err != nil {
			return o, err
		}
		if ok {
			main, subagents := usage.TokensMain, usage.TokensSubagents
			summary.TokensMain = &main
			summary.TokensSubagents = &subagents
			if hasCategorizedUsage(usage) {
				mainInput, mainOutput := usage.Main.Input, usage.Main.Output
				mainCacheRead, mainCacheWrite := usage.Main.CacheRead, usage.Main.CacheWrite
				subagentsInput, subagentsOutput := usage.Subagents.Input, usage.Subagents.Output
				subagentsCacheRead, subagentsCacheWrite := usage.Subagents.CacheRead, usage.Subagents.CacheWrite
				summary.TokensMainInput = &mainInput
				summary.TokensMainOutput = &mainOutput
				summary.TokensMainCacheRead = &mainCacheRead
				summary.TokensMainCacheWrite = &mainCacheWrite
				summary.TokensSubagentsInput = &subagentsInput
				summary.TokensSubagentsOutput = &subagentsOutput
				summary.TokensSubagentsCacheRead = &subagentsCacheRead
				summary.TokensSubagentsCacheWrite = &subagentsCacheWrite
			}
			summary.Usage = FormatStoryUsage(usage)
		}
		o.Stories = append(o.Stories, summary)
	}
	if len(activities) > 0 {
		storyMap := buildStoryMapOverview(activities, stories, o.Stories)
		o.StoryMap = &storyMap
	}
	ccs, err := e.st.ListNodesByKind(e.pid(), model.KindCrossCutting)
	if err != nil {
		return o, err
	}
	for _, cc := range ccs {
		fresh, _, err := e.freshness(cc)
		if err != nil {
			return o, err
		}
		o.CrossCutting = append(o.CrossCutting, CCSummary{ID: cc.ID, Title: cc.Title, Accepted: fresh})
	}
	all, err := e.st.ListNodes(e.pid())
	if err != nil {
		return o, err
	}
	for _, n := range all {
		fresh, reasons, err := e.freshness(n)
		if err != nil {
			return o, err
		}
		if !fresh && len(reasons) > 0 {
			o.StaleNodes = append(o.StaleNodes, reasons[0])
		}
	}
	sort.Strings(o.StaleNodes)
	return o, nil
}
