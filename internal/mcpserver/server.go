// Package mcpserver exposes the trellis engine over MCP (stdio). This is the
// only write interface an agent gets: the database itself stays hidden, and
// every illegal move comes back as a tool error explaining what was illegal.
package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"trellis/internal/core"
	"trellis/internal/model"
)

const instructions = `trellis is the single source of truth for specs and story state.

Workflow per story:
1. create_node kind=story, add acceptance criteria (add_acceptance_criterion).
2. Build the full spec tree: acceptance_test specs (each AC covered), exactly one
   arch spec, under it integration_test specs and detail_design nodes, under each
   detail design unit_test specs. Cross-cutting architecture lives in
   cross_cutting root nodes; reference them via link_dependency.
3. Approve: get_tree(story, full=true) then approve_tree(story, hashes) in one
   batch — or node by node with approve(node_id, content_hash). Either way the
   hashes come with the content: you read what you approve.
4. transition(story, "refine") -> "start" (creates/checks out feature branch)
   -> implement -> "finish" (lint, tests, verifies every test spec has a
   passing test referencing its id, merges into the base branch).

Check the glossary in get_overview and reuse its exact wording in every spec
you write; define new project terms with define_term (ultra short).
Test naming: a test proves spec UT-3 iff its name contains "UT-3" or "UT_3".
Editing any node invalidates approvals of its children/dependents and drops
affected refined/in_progress stories back to todo. Statuses can never be set
directly; only transitions move them.`

type Server struct {
	engine *core.Engine
}

func New(engine *core.Engine, version string) *mcp.Server {
	s := &Server{engine: engine}
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "trellis", Title: "trellis spec tracker", Version: version},
		&mcp.ServerOptions{Instructions: instructions},
	)

	mcp.AddTool(srv, &mcp.Tool{Name: "get_overview",
		Description: "Project overview: all stories with status and gate readiness, cross-cutting specs, stale nodes."},
		s.getOverview)
	mcp.AddTool(srv, &mcp.Tool{Name: "create_node",
		Description: "Create a spec node. Kinds and required parents: story (none; optional activity_id and slice placement), activity (none; optional position, otherwise appended), acceptance_test (story, needs covers), arch (story, exactly one), integration_test (arch), detail_design (arch), unit_test (detail_design), cross_cutting (none)."},
		s.createNode)
	mcp.AddTool(srv, &mcp.Tool{Name: "set_map_position",
		Description: "Place a story in an existing activity and slice; rank appends automatically within that activity and slice. Placement is metadata and does not invalidate approvals."},
		s.setMapPosition)
	mcp.AddTool(srv, &mcp.Tool{Name: "update_node",
		Description: "Update a node's title/body/covers, or an activity's position. Content changes invalidate the node's approval and make children and dependents stale; position is metadata."},
		s.updateNode)
	mcp.AddTool(srv, &mcp.Tool{Name: "delete_node",
		Description: "Delete a node. Blocked while it has children or dependents, or while an activity has placed stories."},
		s.deleteNode)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_node",
		Description: "Full content of one node incl. content_hash (needed for approve) and current hashes of its dependencies."},
		s.getNode)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_tree",
		Description: "Full spec tree of a story: nodes with hashes and freshness, AC coverage, and the exact list of problems blocking refine/start/finish. Pass full=true to include every node's body — the read side of approve_tree."},
		s.getTree)
	mcp.AddTool(srv, &mcp.Tool{Name: "add_acceptance_criterion",
		Description: "Add a structured acceptance criterion (given/when/then) to a story."},
		s.addAC)
	mcp.AddTool(srv, &mcp.Tool{Name: "update_acceptance_criterion",
		Description: "Update an acceptance criterion. Changes the story's content hash and invalidates child approvals."},
		s.updateAC)
	mcp.AddTool(srv, &mcp.Tool{Name: "delete_acceptance_criterion",
		Description: "Delete an acceptance criterion. Blocked while an acceptance test covers it."},
		s.deleteAC)
	mcp.AddTool(srv, &mcp.Tool{Name: "approve_tree",
		Description: "Approve a whole story tree in one call: pass the current content_hash of EVERY tree node (from get_tree full=true) and of every pinned dependency target via dep_hashes. All-or-nothing: any mismatch rejects the batch listing every problem."},
		s.approveTree)
	mcp.AddTool(srv, &mcp.Tool{Name: "approve",
		Description: "Approve a node after reviewing it. content_hash must be the node's current hash (from get_node) — proof you read it. If the node has dependencies, pass dep_hashes {target_id: its current content_hash}. Parents must be approved before children."},
		s.approve)
	mcp.AddTool(srv, &mcp.Tool{Name: "link_dependency",
		Description: "Declare that a node depends on another node (typically a cross_cutting spec). Target must be approved and fresh."},
		s.linkDep)
	mcp.AddTool(srv, &mcp.Tool{Name: "unlink_dependency",
		Description: "Remove a dependency link."},
		s.unlinkDep)
	mcp.AddTool(srv, &mcp.Tool{Name: "search_specs",
		Description: "Full-text search over spec titles, bodies and acceptance criteria: every term must match (last term as word prefix), results ranked by relevance, snippet per hit. Use this to find relevant context before designing or implementing."},
		s.searchSpecs)
	mcp.AddTool(srv, &mcp.Tool{Name: "set_paths",
		Description: "Declare which repo-relative files/folders realize a story (metadata, does not invalidate approvals; empty list clears). finish verifies the paths exist."},
		s.setPaths)
	mcp.AddTool(srv, &mcp.Tool{Name: "specs_for_path",
		Description: "Reverse lookup: stories whose declared paths cover a file (exact or folder prefix). Check this before changing a file to find the specs it belongs to."},
		s.specsForPath)
	mcp.AddTool(srv, &mcp.Tool{Name: "set_description",
		Description: "Set the GitHub-style one-line project description (max 200 chars). Shown in overview, board and release manifest — keep it current."},
		s.setDescription)
	mcp.AddTool(srv, &mcp.Tool{Name: "define_term",
		Description: "Define or update a project glossary term (term <= 64 chars, definition <= 240 chars). The glossary keeps wording consistent; reuse its exact terms in specs."},
		s.defineTerm)
	mcp.AddTool(srv, &mcp.Tool{Name: "delete_term",
		Description: "Remove a glossary term."},
		s.deleteTerm)
	mcp.AddTool(srv, &mcp.Tool{Name: "next_story",
		Description: "The next startable stories: refined with all sequencing dependencies done, plus refined stories still blocked (with their unfinished dependencies)."},
		s.nextStory)
	mcp.AddTool(srv, &mcp.Tool{Name: "audit",
		Description: "Bidirectional repo-wide validation: re-verifies every done story's tests and paths, flags tests referencing nonexistent specs and unclaimed non-meta files (violations), and lists unbound tests (informational). Returns every finding; runs the test command; never mutates."},
		s.audit)
	mcp.AddTool(srv, &mcp.Tool{Name: "transition",
		Description: "Run a story state-machine action: refine (todo->refined, requires complete approved tree), start (refined->in_progress, checks out feature branch), finish (in_progress->done, runs lint+tests, verifies test evidence per spec, merges into base branch), abort (in_progress->refined, discards the feature branch; requires clean worktree)."},
		s.transition)
	return srv
}

// ---- inputs ----

type createNodeIn struct {
	Kind       string   `json:"kind" jsonschema:"story | activity | acceptance_test | arch | integration_test | detail_design | unit_test | cross_cutting"`
	ParentID   string   `json:"parent_id,omitempty" jsonschema:"parent node id; omit for story, activity and cross_cutting"`
	Title      string   `json:"title"`
	Body       string   `json:"body,omitempty" jsonschema:"spec text (markdown)"`
	Covers     []string `json:"covers,omitempty" jsonschema:"acceptance_test only: AC ids this test proves, e.g. [\"US-1.AC-1\"]"`
	Position   *int     `json:"position,omitempty" jsonschema:"activity only: integer story map order; omitted appends"`
	ActivityID string   `json:"activity_id,omitempty" jsonschema:"story only: existing activity id; requires slice"`
	Slice      *int     `json:"slice,omitempty" jsonschema:"story only: integer release cut at least 1; requires activity_id"`
}

type setMapPositionIn struct {
	StoryID    string `json:"story_id"`
	ActivityID string `json:"activity_id"`
	Slice      int    `json:"slice" jsonschema:"integer release cut at least 1"`
}

type updateNodeIn struct {
	ID       string    `json:"id"`
	Title    *string   `json:"title,omitempty"`
	Body     *string   `json:"body,omitempty"`
	Covers   *[]string `json:"covers,omitempty"`
	Position *int      `json:"position,omitempty" jsonschema:"activity only: integer story map order"`
}

type idIn struct {
	ID string `json:"id"`
}

type storyIn struct {
	StoryID string `json:"story_id"`
}

type treeIn struct {
	StoryID string `json:"story_id"`
	Full    bool   `json:"full,omitempty" jsonschema:"include every node's body"`
}

type approveTreeIn struct {
	StoryID   string            `json:"story_id"`
	Hashes    map[string]string `json:"hashes" jsonschema:"current content_hash of every tree node"`
	DepHashes map[string]string `json:"dep_hashes,omitempty" jsonschema:"current content_hash of every pinned dependency target"`
}

type addACIn struct {
	StoryID string `json:"story_id"`
	Given   string `json:"given"`
	When    string `json:"when"`
	Then    string `json:"then"`
}

type updateACIn struct {
	ACID  string  `json:"ac_id"`
	Given *string `json:"given,omitempty"`
	When  *string `json:"when,omitempty"`
	Then  *string `json:"then,omitempty"`
}

type acIDIn struct {
	ACID string `json:"ac_id"`
}

type approveIn struct {
	NodeID      string            `json:"node_id"`
	ContentHash string            `json:"content_hash" jsonschema:"the node's current content_hash from get_node"`
	DepHashes   map[string]string `json:"dep_hashes,omitempty" jsonschema:"for each dependency target: its current content_hash"`
}

type depIn struct {
	NodeID   string `json:"node_id"`
	TargetID string `json:"target_id"`
}

type transitionIn struct {
	StoryID string `json:"story_id"`
	Action  string `json:"action" jsonschema:"refine | start | finish | abort"`
}

type searchIn struct {
	Query string `json:"query"`
}

type setPathsIn struct {
	StoryID string   `json:"story_id"`
	Paths   []string `json:"paths" jsonschema:"repo-relative files or folders; empty list clears"`
}

type pathIn struct {
	Path string `json:"path" jsonschema:"repo-relative file path"`
}

type descIn struct {
	Description string `json:"description" jsonschema:"one line, max 200 chars"`
}

type termIn struct {
	Term       string `json:"term"`
	Definition string `json:"definition,omitempty" jsonschema:"required for define_term; max 240 chars"`
}

type nextOut struct {
	Candidates []core.StorySummary `json:"candidates"`
	Blocked    []core.BlockedStory `json:"blocked"`
}

type okOut struct {
	Message string `json:"message"`
}

// Envelope objects: strict MCP clients require object-typed output schemas,
// so list results never travel as bare arrays (US-30).
type searchOut struct {
	Hits []core.SearchHit `json:"hits"`
}

type storiesOut struct {
	Stories []core.StorySummary `json:"stories"`
}

// ---- handlers ----

func (s *Server) getOverview(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, core.Overview, error) {
	o, err := s.engine.Overview()
	return nil, o, err
}

func (s *Server) createNode(_ context.Context, _ *mcp.CallToolRequest, in createNodeIn) (*mcp.CallToolResult, core.NodeReport, error) {
	kind := model.Kind(in.Kind)
	n, err := s.engine.CreateNodeWithPlacement(kind, in.ParentID, in.Title, in.Body, in.Covers, in.Position, in.ActivityID, in.Slice)
	if err != nil {
		return nil, core.NodeReport{}, err
	}
	r, err := s.engine.Node(n.ID)
	if err != nil {
		return nil, core.NodeReport{}, err
	}
	if kind == model.KindStory && in.ActivityID == "" && in.Slice == nil {
		r.PlacementHint, err = s.engine.PlacementHint(in.Title, in.Body)
		if err != nil {
			return nil, core.NodeReport{}, err
		}
	}
	return nil, r, nil
}

func (s *Server) setMapPosition(_ context.Context, _ *mcp.CallToolRequest, in setMapPositionIn) (*mcp.CallToolResult, core.NodeReport, error) {
	n, err := s.engine.SetMapPosition(in.StoryID, in.ActivityID, in.Slice)
	if err != nil {
		return nil, core.NodeReport{}, err
	}
	r, err := s.engine.Node(n.ID)
	return nil, r, err
}

func (s *Server) updateNode(_ context.Context, _ *mcp.CallToolRequest, in updateNodeIn) (*mcp.CallToolResult, core.NodeReport, error) {
	n, err := s.engine.UpdateNodeWithPosition(in.ID, in.Title, in.Body, in.Covers, in.Position)
	if err != nil {
		return nil, core.NodeReport{}, err
	}
	r, err := s.engine.Node(n.ID)
	return nil, r, err
}

func (s *Server) deleteNode(_ context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, okOut, error) {
	if err := s.engine.DeleteNode(in.ID); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Message: in.ID + " deleted"}, nil
}

func (s *Server) getNode(_ context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, core.NodeReport, error) {
	r, err := s.engine.Node(in.ID)
	return nil, r, err
}

// getTree returns Out as `any`: TreeReport is recursive and the SDK cannot
// infer a JSON schema for cyclic types, so the output schema is omitted.
func (s *Server) getTree(_ context.Context, _ *mcp.CallToolRequest, in treeIn) (*mcp.CallToolResult, any, error) {
	var r core.TreeReport
	var err error
	if in.Full {
		r, err = s.engine.TreeFull(in.StoryID)
	} else {
		r, err = s.engine.Tree(in.StoryID)
	}
	if err != nil {
		return nil, nil, err
	}
	return nil, r, nil
}

func (s *Server) approveTree(_ context.Context, _ *mcp.CallToolRequest, in approveTreeIn) (*mcp.CallToolResult, okOut, error) {
	if err := s.engine.ApproveTree(in.StoryID, in.Hashes, in.DepHashes); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Message: in.StoryID + " tree approved"}, nil
}

func (s *Server) addAC(_ context.Context, _ *mcp.CallToolRequest, in addACIn) (*mcp.CallToolResult, okOut, error) {
	ac, err := s.engine.AddAC(in.StoryID, in.Given, in.When, in.Then)
	if err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Message: fmt.Sprintf("%s added to %s", ac.ID, in.StoryID)}, nil
}

func (s *Server) updateAC(_ context.Context, _ *mcp.CallToolRequest, in updateACIn) (*mcp.CallToolResult, okOut, error) {
	ac, err := s.engine.UpdateAC(in.ACID, in.Given, in.When, in.Then)
	if err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Message: ac.ID + " updated; story hash changed, child approvals invalidated"}, nil
}

func (s *Server) deleteAC(_ context.Context, _ *mcp.CallToolRequest, in acIDIn) (*mcp.CallToolResult, okOut, error) {
	if err := s.engine.DeleteAC(in.ACID); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Message: in.ACID + " deleted"}, nil
}

func (s *Server) approve(_ context.Context, _ *mcp.CallToolRequest, in approveIn) (*mcp.CallToolResult, okOut, error) {
	if err := s.engine.Approve(in.NodeID, in.ContentHash, in.DepHashes); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Message: in.NodeID + " approved"}, nil
}

func (s *Server) linkDep(_ context.Context, _ *mcp.CallToolRequest, in depIn) (*mcp.CallToolResult, okOut, error) {
	if err := s.engine.LinkDep(in.NodeID, in.TargetID); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Message: fmt.Sprintf("%s now depends on %s", in.NodeID, in.TargetID)}, nil
}

func (s *Server) unlinkDep(_ context.Context, _ *mcp.CallToolRequest, in depIn) (*mcp.CallToolResult, okOut, error) {
	if err := s.engine.UnlinkDep(in.NodeID, in.TargetID); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Message: fmt.Sprintf("dependency %s -> %s removed", in.NodeID, in.TargetID)}, nil
}

func (s *Server) searchSpecs(_ context.Context, _ *mcp.CallToolRequest, in searchIn) (*mcp.CallToolResult, searchOut, error) {
	hits, err := s.engine.Search(in.Query)
	if hits == nil {
		hits = []core.SearchHit{}
	}
	return nil, searchOut{Hits: hits}, err
}

func (s *Server) setPaths(_ context.Context, _ *mcp.CallToolRequest, in setPathsIn) (*mcp.CallToolResult, okOut, error) {
	cleaned, err := s.engine.SetPaths(in.StoryID, in.Paths)
	if err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Message: fmt.Sprintf("%s now declares %d path(s): %v", in.StoryID, len(cleaned), cleaned)}, nil
}

func (s *Server) specsForPath(_ context.Context, _ *mcp.CallToolRequest, in pathIn) (*mcp.CallToolResult, storiesOut, error) {
	stories, err := s.engine.StoriesForPath(in.Path)
	if stories == nil {
		stories = []core.StorySummary{}
	}
	return nil, storiesOut{Stories: stories}, err
}

func (s *Server) setDescription(_ context.Context, _ *mcp.CallToolRequest, in descIn) (*mcp.CallToolResult, okOut, error) {
	if err := s.engine.SetDescription(in.Description); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Message: "description set"}, nil
}

func (s *Server) defineTerm(_ context.Context, _ *mcp.CallToolRequest, in termIn) (*mcp.CallToolResult, okOut, error) {
	if err := s.engine.DefineTerm(in.Term, in.Definition); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Message: "term " + in.Term + " defined"}, nil
}

func (s *Server) deleteTerm(_ context.Context, _ *mcp.CallToolRequest, in termIn) (*mcp.CallToolResult, okOut, error) {
	if err := s.engine.DeleteTerm(in.Term); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Message: "term " + in.Term + " deleted"}, nil
}

func (s *Server) nextStory(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, nextOut, error) {
	c, b, err := s.engine.NextStories()
	return nil, nextOut{Candidates: c, Blocked: b}, err
}

func (s *Server) audit(_ context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, core.AuditReport, error) {
	rep, err := s.engine.Audit()
	return nil, rep, err
}

func (s *Server) transition(_ context.Context, _ *mcp.CallToolRequest, in transitionIn) (*mcp.CallToolResult, okOut, error) {
	msg, err := s.engine.Transition(in.StoryID, in.Action)
	if err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Message: msg}, nil
}
