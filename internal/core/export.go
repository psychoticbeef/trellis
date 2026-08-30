package core

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"

	"trellis/internal/model"
	"trellis/internal/store"
)

// exportVersion guards the format; import refuses anything else.
const exportVersion = 1

type exportDoc struct {
	Version  int             `yaml:"trellis_export"`
	Project  exportProject   `yaml:"project"`
	Glossary []store.TermDef `yaml:"glossary,omitempty"`
	Counters map[string]int  `yaml:"counters,omitempty"`
	Cross    []exportNode    `yaml:"cross_cutting,omitempty"`
	Stories  []exportNode    `yaml:"stories,omitempty"`
}

// exportProject deliberately omits the repo path: it is machine-local and
// supplied again at import time.
type exportProject struct {
	Name          string `yaml:"name"`
	Description   string `yaml:"description,omitempty"`
	BaseBranch    string `yaml:"base_branch"`
	ReleaseBranch string `yaml:"release_branch"`
	LintCmd       string `yaml:"lint_cmd,omitempty"`
	TestCmd       string `yaml:"test_cmd,omitempty"`
	JUnitGlob     string `yaml:"junit_glob,omitempty"`
}

type exportAC struct {
	ID    string `yaml:"id"`
	Given string `yaml:"given"`
	When  string `yaml:"when"`
	Then  string `yaml:"then"`
}

type exportDep struct {
	Target     string `yaml:"target"`
	PinnedHash string `yaml:"pinned_hash,omitempty"`
}

type exportEvidence struct {
	Tests      []string `yaml:"tests"`
	RecordedAt string   `yaml:"recorded_at"`
}

type exportNode struct {
	ID                  string          `yaml:"id"`
	Kind                string          `yaml:"kind"`
	Title               string          `yaml:"title"`
	Body                string          `yaml:"body,omitempty"`
	Status              string          `yaml:"status,omitempty"`
	Covers              []string        `yaml:"covers,omitempty"`
	Paths               []string        `yaml:"paths,omitempty"`
	ACs                 []exportAC      `yaml:"acceptance_criteria,omitempty"`
	DependsOn           []exportDep     `yaml:"depends_on,omitempty"`
	ApprovedContentHash string          `yaml:"approved_content_hash,omitempty"`
	ApprovedParentHash  string          `yaml:"approved_parent_hash,omitempty"`
	Evidence            *exportEvidence `yaml:"evidence,omitempty"`
	Children            []exportNode    `yaml:"children,omitempty"`
}

func (e *Engine) exportNode(n model.Node) (exportNode, error) {
	en := exportNode{
		ID: n.ID, Kind: string(n.Kind), Title: n.Title, Body: n.Body,
		Status: n.Status, Covers: n.Covers, Paths: n.Paths,
		ApprovedContentHash: n.ApprovedContentHash, ApprovedParentHash: n.ApprovedParentHash,
	}
	if n.Kind == model.KindStory {
		acs, err := e.st.ListACs(e.pid(), n.ID)
		if err != nil {
			return en, err
		}
		for _, ac := range acs {
			en.ACs = append(en.ACs, exportAC{ID: ac.ID, Given: ac.Given, When: ac.When, Then: ac.Then})
		}
	}
	deps, err := e.st.ListDeps(e.pid(), n.ID)
	if err != nil {
		return en, err
	}
	for _, d := range deps {
		en.DependsOn = append(en.DependsOn, exportDep{Target: d.TargetID, PinnedHash: d.PinnedHash})
	}
	if model.TestSpecKinds[n.Kind] {
		if ev, ok, err := e.st.GetEvidence(e.pid(), n.ID); err != nil {
			return en, err
		} else if ok {
			en.Evidence = &exportEvidence{Tests: ev.Tests, RecordedAt: ev.RecordedAt}
		}
	}
	children, err := e.st.ListChildren(e.pid(), n.ID)
	if err != nil {
		return en, err
	}
	for _, c := range children {
		ce, err := e.exportNode(c)
		if err != nil {
			return en, err
		}
		en.Children = append(en.Children, ce)
	}
	return en, nil
}

// ExportYAML renders the whole project as one human-readable YAML document:
// the backup that rides along on every release. The event log stays out —
// flight recorder, not state.
func (e *Engine) ExportYAML() (string, error) {
	doc := exportDoc{Version: exportVersion, Project: exportProject{
		Name: e.Project.Name, Description: e.Project.Description,
		BaseBranch: e.Project.BaseBranch, ReleaseBranch: e.Project.ReleaseBranch,
		LintCmd: e.Project.LintCmd, TestCmd: e.Project.TestCmd, JUnitGlob: e.Project.JUnitGlob,
	}}
	var err error
	doc.Glossary, err = e.st.ListTerms(e.pid())
	if err != nil {
		return "", err
	}
	doc.Counters, err = e.st.ListCounters(e.pid())
	if err != nil {
		return "", err
	}
	for _, kind := range []model.Kind{model.KindCrossCutting, model.KindStory} {
		nodes, err := e.st.ListNodesByKind(e.pid(), kind)
		if err != nil {
			return "", err
		}
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].CreatedAt.Before(nodes[j].CreatedAt) })
		for _, n := range nodes {
			en, err := e.exportNode(n)
			if err != nil {
				return "", err
			}
			if kind == model.KindCrossCutting {
				doc.Cross = append(doc.Cross, en)
			} else {
				doc.Stories = append(doc.Stories, en)
			}
		}
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Import restores an export into a NEW project (identity and repo supplied by
// the caller): content, approvals, statuses, dependency pins, evidence and
// counters arrive intact, so ids are never reused.
func Import(st *store.Store, data []byte, proj store.Project) error {
	var doc exportDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("import: invalid yaml: %w", err)
	}
	if doc.Version != exportVersion {
		return fmt.Errorf("import: unsupported export version %d (supported: %d)", doc.Version, exportVersion)
	}
	if proj.BaseBranch == "" {
		proj.BaseBranch = doc.Project.BaseBranch
	}
	if proj.ReleaseBranch == "" {
		proj.ReleaseBranch = doc.Project.ReleaseBranch
	}
	if proj.Description == "" {
		proj.Description = doc.Project.Description
	}
	if proj.LintCmd == "" {
		proj.LintCmd = doc.Project.LintCmd
	}
	if proj.TestCmd == "" {
		proj.TestCmd = doc.Project.TestCmd
	}
	if proj.JUnitGlob == "" {
		proj.JUnitGlob = doc.Project.JUnitGlob
	}
	if err := st.CreateProject(proj); err != nil {
		return err
	}
	pid := proj.ID

	type depEdge struct {
		node string
		dep  exportDep
	}
	var deps []depEdge
	var insert func(en exportNode, parent string) error
	insert = func(en exportNode, parent string) error {
		n := model.Node{
			ID: en.ID, ProjectID: pid, Kind: model.Kind(en.Kind), ParentID: parent,
			Title: en.Title, Body: en.Body, Covers: en.Covers, Paths: en.Paths, Status: en.Status,
			ApprovedContentHash: en.ApprovedContentHash, ApprovedParentHash: en.ApprovedParentHash,
		}
		if err := st.InsertNode(n); err != nil {
			return err
		}
		for i, ac := range en.ACs {
			if err := st.InsertAC(pid, model.AC{ID: ac.ID, StoryID: en.ID,
				Given: ac.Given, When: ac.When, Then: ac.Then, Position: i + 1}); err != nil {
				return err
			}
		}
		for _, d := range en.DependsOn {
			deps = append(deps, depEdge{node: en.ID, dep: d})
		}
		if en.Evidence != nil {
			if err := st.SetEvidenceAt(pid, en.ID, en.Evidence.Tests, en.Evidence.RecordedAt); err != nil {
				return err
			}
		}
		for _, c := range en.Children {
			if err := insert(c, en.ID); err != nil {
				return err
			}
		}
		return nil
	}
	for _, en := range append(append([]exportNode{}, doc.Cross...), doc.Stories...) {
		if err := insert(en, ""); err != nil {
			return err
		}
	}
	for _, de := range deps {
		if err := st.LinkDep(pid, model.Dep{NodeID: de.node, TargetID: de.dep.Target, PinnedHash: de.dep.PinnedHash}); err != nil {
			return err
		}
	}
	for _, td := range doc.Glossary {
		if err := st.DefineTerm(pid, td.Term, td.Definition); err != nil {
			return err
		}
	}
	for scope, n := range doc.Counters {
		if err := st.SetCounter(pid, scope, n); err != nil {
			return err
		}
	}
	return nil
}
