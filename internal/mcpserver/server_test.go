package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"trellis/internal/board"
	"trellis/internal/core"
	"trellis/internal/mcpserver"
	"trellis/internal/store"
)

// client connects an MCP client to a trellis server over in-memory transports,
// backed by a real store — the exact surface an agent sees.
func client(t *testing.T) *mcp.ClientSession {
	t.Helper()
	return clientFor(t, store.Project{ID: "p1", Name: "test", BaseBranch: "develop"})
}

// clientFor is client with full control over the project config (repo, gates).
func clientFor(t *testing.T, p store.Project) *mcp.ClientSession {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "trellis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateProject(p); err != nil {
		t.Fatal(err)
	}
	lastEngine = nil
	lastStore = st
	engine, err := core.NewEngine(st, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	lastEngine = engine
	ct, srvT := mcp.NewInMemoryTransports()
	srv := mcpserver.New(engine, "test")
	ctx := context.Background()
	srvSession, err := srv.Connect(ctx, srvT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srvSession.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	if res.IsError {
		t.Fatalf("%s%v: tool error: %s", tool, args, text(res))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text(res)), &out); err != nil {
		t.Fatalf("%s: bad json output %q: %v", tool, text(res), err)
	}
	return out
}

func callErr(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, substrings ...string) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	if !res.IsError {
		t.Fatalf("%s%v: expected tool error containing %v, got success: %s", tool, args, substrings, text(res))
	}
	for _, s := range substrings {
		if !strings.Contains(text(res), s) {
			t.Errorf("%s error missing %q:\n%s", tool, s, text(res))
		}
	}
}

// approveMCP approves a node the way an agent must: read it first, pass its
// hash and the hashes of all dependency targets.
func approveMCP(t *testing.T, cs *mcp.ClientSession, id string) {
	t.Helper()
	n := call(t, cs, "get_node", map[string]any{"id": id})
	deps := map[string]any{}
	if ds, ok := n["depends_on"].([]any); ok {
		for _, d := range ds {
			dep := d.(map[string]any)
			deps[dep["target"].(string)] = dep["target_content_hash"]
		}
	}
	call(t, cs, "approve", map[string]any{"node_id": id, "content_hash": n["content_hash"], "dep_hashes": deps})
}

// fullTreeMCP builds and approves a complete story tree through the MCP
// surface and returns the created ids in creation order.
func fullTreeMCP(t *testing.T, cs *mcp.ClientSession) (story, at, arch, it, dd, ut string) {
	t.Helper()
	s := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "feature", "body": "b"})
	story = s["id"].(string)
	call(t, cs, "add_acceptance_criterion", map[string]any{"story_id": story, "given": "g", "when": "w", "then": "t"})
	n := call(t, cs, "create_node", map[string]any{"kind": "acceptance_test", "parent_id": story, "title": "at", "covers": []string{story + ".AC-1"}})
	at = n["id"].(string)
	n = call(t, cs, "create_node", map[string]any{"kind": "arch", "parent_id": story, "title": "as"})
	arch = n["id"].(string)
	n = call(t, cs, "create_node", map[string]any{"kind": "integration_test", "parent_id": arch, "title": "it"})
	it = n["id"].(string)
	n = call(t, cs, "create_node", map[string]any{"kind": "detail_design", "parent_id": arch, "title": "dd"})
	dd = n["id"].(string)
	n = call(t, cs, "create_node", map[string]any{"kind": "unit_test", "parent_id": dd, "title": "ut"})
	ut = n["id"].(string)
	for _, id := range []string{story, at, arch, it, dd, ut} {
		approveMCP(t, cs, id)
	}
	return
}

func nodeStatus(t *testing.T, cs *mcp.ClientSession, id string) (status, hash string) {
	t.Helper()
	n := call(t, cs, "get_node", map[string]any{"id": id})
	status, _ = n["status"].(string)
	return status, n["content_hash"].(string)
}

func text(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	if tc, ok := res.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

// TestNodeLifecycleAcceptance_AT_1 proves AT-1 (US-1 "Spec tree management"):
// full node lifecycle through the MCP interface — valid tree creation with
// stored ids and returned hashes, every illegal structural move rejected with
// the violated rule named, and delete blocked while referenced.
func TestNodeLifecycleAcceptance_AT_1(t *testing.T) {
	cs := client(t)

	// Valid tree, asserting ids and hashes.
	story := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "s"})
	if story["id"] != "US-1" {
		t.Fatalf("story id = %v, want US-1", story["id"])
	}
	if h, _ := story["content_hash"].(string); len(h) != 64 {
		t.Fatalf("story content_hash = %v, want 64-char sha256 hex", story["content_hash"])
	}
	call(t, cs, "add_acceptance_criterion", map[string]any{"story_id": "US-1", "given": "g", "when": "w", "then": "t"})
	at := call(t, cs, "create_node", map[string]any{"kind": "acceptance_test", "parent_id": "US-1", "title": "at", "covers": []string{"US-1.AC-1"}})
	if at["id"] != "AT-1" {
		t.Fatalf("acceptance test id = %v, want AT-1", at["id"])
	}
	arch := call(t, cs, "create_node", map[string]any{"kind": "arch", "parent_id": "US-1", "title": "as"})
	call(t, cs, "create_node", map[string]any{"kind": "detail_design", "parent_id": arch["id"], "title": "dd"})
	activity := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "activity"})
	if activity["id"] != "UA-1" || activity["position"] != float64(1) {
		t.Fatalf("activity root = %v", activity)
	}
	call(t, cs, "update_node", map[string]any{"id": "UA-1", "position": 2})

	// Each illegal structural move names its violated rule.
	callErr(t, cs, "create_node", map[string]any{"kind": "story", "parent_id": "US-1", "title": "x"},
		"root node")
	callErr(t, cs, "create_node", map[string]any{"kind": "activity", "parent_id": "US-1", "title": "x"},
		"root node")
	callErr(t, cs, "create_node", map[string]any{"kind": "arch", "parent_id": "US-1", "title": "x"},
		"exactly one arch spec", "AS-1")
	callErr(t, cs, "create_node", map[string]any{"kind": "unit_test", "parent_id": "US-1", "title": "x"},
		"illegal parent", "must be child of a detail_design")
	callErr(t, cs, "create_node", map[string]any{"kind": "unit_test", "title": "x"},
		"requires a parent")
	callErr(t, cs, "create_node", map[string]any{"kind": "epic", "title": "x"},
		"unknown kind")
	callErr(t, cs, "create_node", map[string]any{"kind": "acceptance_test", "parent_id": "US-1", "title": "x", "covers": []string{"US-1.AC-9"}},
		"unknown acceptance criteria", "US-1.AC-1")

	// Delete blocked while referenced, listing every referencing node.
	callErr(t, cs, "delete_node", map[string]any{"id": "US-1"},
		"delete blocked", "child AT-1", "child AS-1")
	callErr(t, cs, "delete_node", map[string]any{"id": "AS-1"},
		"delete blocked", "child DD-1")

	// Leaf delete works; ids are never reused.
	call(t, cs, "delete_node", map[string]any{"id": "DD-1"})
	dd2 := call(t, cs, "create_node", map[string]any{"kind": "detail_design", "parent_id": arch["id"], "title": "dd2"})
	if dd2["id"] != "DD-2" {
		t.Fatalf("id after delete = %v, want DD-2 (ids must never be reused)", dd2["id"])
	}
}

// TestACLifecycleAcceptance_AT_2 proves AT-2 (US-2 "Structured acceptance
// criteria"): AC add/update/delete through MCP, hash change on edit, automatic
// story downgrade, and the blocked delete while covered.
func TestACLifecycleAcceptance_AT_2(t *testing.T) {
	cs := client(t)
	story, at, _, _, _, _ := fullTreeMCP(t, cs)
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"})
	st, before := nodeStatus(t, cs, story)
	if st != "refined" {
		t.Fatalf("status = %s, want refined", st)
	}

	// Editing an AC changes the story hash and drops the story to todo.
	call(t, cs, "update_acceptance_criterion", map[string]any{"ac_id": story + ".AC-1", "then": "changed outcome"})
	st, after := nodeStatus(t, cs, story)
	if after == before {
		t.Fatal("story hash unchanged after AC edit")
	}
	if st != "todo" {
		t.Fatalf("status = %s, want todo after AC edit", st)
	}

	// Adding and deleting an uncovered AC works; ids are per-story monotonic.
	call(t, cs, "add_acceptance_criterion", map[string]any{"story_id": story, "given": "g2", "when": "w2", "then": "t2"})
	n := call(t, cs, "get_node", map[string]any{"id": story})
	acs := n["acceptance_criteria"].([]any)
	if len(acs) != 2 {
		t.Fatalf("got %d ACs, want 2", len(acs))
	}
	call(t, cs, "delete_acceptance_criterion", map[string]any{"ac_id": story + ".AC-2"})

	// Deleting a covered AC is blocked, naming the covering test.
	callErr(t, cs, "delete_acceptance_criterion", map[string]any{"ac_id": story + ".AC-1"},
		"delete blocked", "covered by acceptance tests", at)

	// Empty fields are rejected.
	callErr(t, cs, "add_acceptance_criterion", map[string]any{"story_id": story, "given": "", "when": "w", "then": "t"},
		"must all be non-empty")
}

// TestApprovalFlowAcceptance_AT_3 proves AT-3 (US-3 "Hash-based approval and
// invalidation"): approve with a wrong hash, approve child before parent,
// approve a full tree top-down, then edit a mid-tree node and assert the
// stale cascade and the automatic story downgrade.
func TestApprovalFlowAcceptance_AT_3(t *testing.T) {
	cs := client(t)
	story := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "s"})["id"].(string)
	call(t, cs, "add_acceptance_criterion", map[string]any{"story_id": story, "given": "g", "when": "w", "then": "t"})
	arch := call(t, cs, "create_node", map[string]any{"kind": "arch", "parent_id": story, "title": "as"})["id"].(string)

	// Wrong hash: rejected without revealing the expected hash.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "approve",
		Arguments: map[string]any{"node_id": story, "content_hash": "0000"}})
	if err != nil || !res.IsError {
		t.Fatalf("approve with wrong hash must fail as tool error, got err=%v", err)
	}
	_, realHash := nodeStatus(t, cs, story)
	if !strings.Contains(text(res), "hash mismatch") || strings.Contains(text(res), realHash) {
		t.Fatalf("error must say hash mismatch and never reveal the real hash:\n%s", text(res))
	}

	// Child before parent: rejected demanding top-down order.
	_, archHash := nodeStatus(t, cs, arch)
	callErr(t, cs, "approve", map[string]any{"node_id": arch, "content_hash": archHash},
		"parent "+story+" must be approved first")

	// Full tree top-down on a fresh story, then refine succeeds.
	story, _, _, _, dd, ut := fullTreeMCP(t, cs)
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"})

	// Edit mid-tree: node unapproved, child stale with reasons, story todo.
	call(t, cs, "update_node", map[string]any{"id": dd, "body": "changed"})
	st, _ := nodeStatus(t, cs, story)
	if st != "todo" {
		t.Fatalf("status = %s, want todo after mid-tree edit", st)
	}
	tree := call(t, cs, "get_tree", map[string]any{"story_id": story})
	problems, _ := json.Marshal(tree["blocking_problems"])
	for _, want := range []string{dd + " changed since approval", ut + " stale: parent " + dd} {
		if !strings.Contains(string(problems), want) {
			t.Errorf("blocking_problems missing %q:\n%s", want, problems)
		}
	}

	// Repair top-down and refine again.
	approveMCP(t, cs, dd)
	approveMCP(t, cs, ut)
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"})
}

// TestCrossCuttingFlowAcceptance_AT_4 proves AT-4 (US-4 "Cross-cutting
// architecture dependencies"): link before/after approval, approve a
// dependent with and without dep hashes, cascade on target edit, blocked
// delete while referenced.
func TestCrossCuttingFlowAcceptance_AT_4(t *testing.T) {
	cs := client(t)
	story, _, arch, _, _, _ := fullTreeMCP(t, cs)
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"})
	cc := call(t, cs, "create_node", map[string]any{"kind": "cross_cutting", "title": "logging", "body": "b"})["id"].(string)

	// Linking to an unapproved target is blocked.
	callErr(t, cs, "link_dependency", map[string]any{"node_id": arch, "target_id": cc},
		"link blocked", cc+" never approved")
	approveMCP(t, cs, cc)
	call(t, cs, "link_dependency", map[string]any{"node_id": arch, "target_id": cc})

	// Approving the dependent without proof of having read the target fails.
	_, archHash := nodeStatus(t, cs, arch)
	callErr(t, cs, "approve", map[string]any{"node_id": arch, "content_hash": archHash},
		"missing dep_hashes entry for "+cc)
	approveMCP(t, cs, arch)

	// Editing the target stales the dependent and downgrades the story.
	call(t, cs, "update_node", map[string]any{"id": cc, "body": "changed"})
	st, _ := nodeStatus(t, cs, story)
	if st != "todo" {
		t.Fatalf("status = %s, want todo after target edit", st)
	}
	tree := call(t, cs, "get_tree", map[string]any{"story_id": story})
	problems, _ := json.Marshal(tree["blocking_problems"])
	if !strings.Contains(string(problems), arch+" stale: dependency "+cc+" changed since pin") {
		t.Fatalf("blocking_problems missing dependency staleness:\n%s", problems)
	}

	// Deleting a referenced target is blocked; after unlink it works.
	callErr(t, cs, "delete_node", map[string]any{"id": cc},
		"delete blocked", "dependent "+arch)
	call(t, cs, "unlink_dependency", map[string]any{"node_id": arch, "target_id": cc})
	call(t, cs, "delete_node", map[string]any{"id": cc})
}

// TestGateAcceptance_AT_5 proves AT-5 (US-5 "Story state machine with
// gates"): refine on an empty story lists every problem, the tree is
// completed stepwise, and the tool surface has no status-setting tool.
func TestGateAcceptance_AT_5(t *testing.T) {
	cs := client(t)
	story := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "s"})["id"].(string)

	// Empty story: exhaustive problem list in one response.
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"},
		"refine blocked", "no acceptance criteria", "no acceptance test specs", "no arch spec", story+" never approved")

	// Stepwise completion: each repair shrinks the list until refine passes.
	call(t, cs, "add_acceptance_criterion", map[string]any{"story_id": story, "given": "g", "when": "w", "then": "t"})
	at := call(t, cs, "create_node", map[string]any{"kind": "acceptance_test", "parent_id": story, "title": "at", "covers": []string{story + ".AC-1"}})["id"].(string)
	arch := call(t, cs, "create_node", map[string]any{"kind": "arch", "parent_id": story, "title": "as"})["id"].(string)
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"},
		arch+" has no integration test specs", arch+" has no detail designs")
	it := call(t, cs, "create_node", map[string]any{"kind": "integration_test", "parent_id": arch, "title": "it"})["id"].(string)
	dd := call(t, cs, "create_node", map[string]any{"kind": "detail_design", "parent_id": arch, "title": "dd"})["id"].(string)
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"},
		dd+" has no unit test specs")
	ut := call(t, cs, "create_node", map[string]any{"kind": "unit_test", "parent_id": dd, "title": "ut"})["id"].(string)
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"},
		"never approved")
	for _, id := range []string{story, at, arch, it, dd, ut} {
		approveMCP(t, cs, id)
	}
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"})
	if st, _ := nodeStatus(t, cs, story); st != "refined" {
		t.Fatalf("status = %s, want refined", st)
	}

	// No status-setting tool exists; transition is the only mover.
	tools, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, tool := range tools.Tools {
		seen[tool.Name] = true
		if strings.Contains(tool.Name, "status") || strings.Contains(tool.Name, "state") {
			t.Errorf("tool surface must not offer status setting, found %q", tool.Name)
		}
	}
	if !seen["transition"] {
		t.Error("transition tool missing from tool surface")
	}

	// Unknown verbs are rejected.
	callErr(t, cs, "transition", map[string]any{"story_id": story, "action": "teleport"},
		"unknown action", "refine, start, finish")
}

// TestSearchAcceptance_AT_14 proves AT-14 (US-12 "Spec search") through MCP:
// body-only, AC-only and mixed-case queries, empty result behaviour, and the
// complete product map in get_overview.
func TestSearchAcceptance_AT_14(t *testing.T) {
	cs := client(t)
	story, _, _, _, dd, _ := fullTreeMCP(t, cs)
	call(t, cs, "update_node", map[string]any{"id": dd, "body": "uses a quorum snapshotter"})
	call(t, cs, "add_acceptance_criterion", map[string]any{"story_id": story, "given": "a wombat admin", "when": "they log in", "then": "they see the dashboard"})
	cc := call(t, cs, "create_node", map[string]any{"kind": "cross_cutting", "title": "zebra caching", "body": "b"})["id"].(string)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_specs",
		Arguments: map[string]any{"query": "QUORUM"}})
	if err != nil || res.IsError {
		t.Fatalf("search failed: %v %s", err, text(res))
	}
	var out struct {
		Hits []map[string]any `json:"hits"`
	}
	if err := json.Unmarshal([]byte(text(res)), &out); err != nil {
		t.Fatalf("bad search output %q: %v", text(res), err)
	}
	hits := out.Hits
	if len(hits) != 1 || hits[0]["id"] != dd || hits[0]["story"] != story {
		t.Fatalf("mixed-case body search: %v", hits)
	}
	if !strings.Contains(hits[0]["snippet"].(string), "quorum snapshotter") {
		t.Fatalf("snippet missing match: %v", hits[0])
	}

	// AC-only match.
	res, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_specs",
		Arguments: map[string]any{"query": "wombat"}})
	out.Hits = nil
	json.Unmarshal([]byte(text(res)), &out)
	hits = out.Hits
	if len(hits) != 1 || hits[0]["id"] != story {
		t.Fatalf("AC search: %v", hits)
	}

	// No match: empty list, not an error.
	res, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_specs",
		Arguments: map[string]any{"query": "nothing-matches-this"}})
	if res.IsError || strings.TrimSpace(text(res)) != `{"hits":[]}` {
		t.Fatalf("no-match must be an empty envelope: err=%v %q", res.IsError, text(res))
	}

	// Overview is the complete product map.
	o := call(t, cs, "get_overview", map[string]any{})
	blob, _ := json.Marshal(o)
	for _, want := range []string{story, cc, "zebra caching"} {
		if !strings.Contains(string(blob), want) {
			t.Errorf("overview missing %q:\n%s", want, blob)
		}
	}
}

// TestFTSSearchAcceptance_AT_23 proves AT-23 (US-19 "FTS5 search") through
// MCP: multi-term AND with BM25 order, prefix on the last term, immediate
// index updates, hostile queries staying safe.
func TestFTSSearchAcceptance_AT_23(t *testing.T) {
	cs := client(t)
	search := func(q string) []map[string]any {
		t.Helper()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_specs",
			Arguments: map[string]any{"query": q}})
		if err != nil || res.IsError {
			t.Fatalf("search %q: %v %s", q, err, text(res))
		}
		var out struct {
			Hits []map[string]any `json:"hits"`
		}
		if err := json.Unmarshal([]byte(text(res)), &out); err != nil {
			t.Fatalf("bad output %q: %v", text(res), err)
		}
		return out.Hits
	}

	strong := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "quorum snapshot",
		"body": "quorum snapshot quorum snapshot everywhere"})["id"].(string)
	weak := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "other",
		"body": "one quorum snapshot mention buried in lots of entirely unrelated prose text"})["id"].(string)

	// AND semantics + BM25 order.
	hits := search("quorum snapshot")
	if len(hits) != 2 || hits[0]["id"] != strong || hits[1]["id"] != weak {
		t.Fatalf("ranked AND search: %v", hits)
	}
	if len(search("quorum nothingelse")) != 0 {
		t.Fatal("AND semantics violated")
	}

	// Prefix on the last term.
	if len(search("quorum snap")) != 2 {
		t.Fatal("prefix match failed")
	}

	// Immediate index updates across create/update/delete and AC ops.
	call(t, cs, "add_acceptance_criterion", map[string]any{"story_id": strong, "given": "a krakenlike load", "when": "w", "then": "t"})
	if len(search("krakenlike")) != 1 {
		t.Fatal("AC not searchable immediately")
	}
	call(t, cs, "update_node", map[string]any{"id": weak, "body": "rewritten entirely"})
	if len(search("buried")) != 0 {
		t.Fatal("stale text after update")
	}
	call(t, cs, "delete_acceptance_criterion", map[string]any{"ac_id": strong + ".AC-1"})
	if len(search("krakenlike")) != 0 {
		t.Fatal("deleted AC still searchable")
	}

	// Hostile queries are literal and safe.
	for _, q := range []string{`"quorum" OR`, "NEAR(a b)", "col:val", "(((", `""`} {
		if hits := search(q); len(hits) > 2 {
			t.Fatalf("hostile query %q misbehaved: %v", q, hits)
		}
	}
}

// TestGlossaryAcceptance_AT_25 proves AT-25 (US-21 "Project glossary")
// through MCP: define with limit rejections, redefine, delete, overview
// inclusion — plus the board rendering with marked terms via the engine.
func TestGlossaryAcceptance_AT_25(t *testing.T) {
	cs := client(t)

	call(t, cs, "define_term", map[string]any{"term": "evidence", "definition": "recorded proving tests per test spec"})
	call(t, cs, "define_term", map[string]any{"term": "evidence", "definition": "the proving tests recorded at finish"})
	callErr(t, cs, "define_term", map[string]any{"term": "wall", "definition": strings.Repeat("x", 300)},
		"exceeds 240 characters", "ultra short")
	callErr(t, cs, "define_term", map[string]any{"term": strings.Repeat("t", 70), "definition": "d"},
		"exceeds 64 characters")
	call(t, cs, "define_term", map[string]any{"term": "doomed", "definition": "to be deleted"})
	call(t, cs, "delete_term", map[string]any{"term": "doomed"})
	callErr(t, cs, "delete_term", map[string]any{"term": "doomed"}, "not found")

	o := call(t, cs, "get_overview", map[string]any{})
	blob, _ := json.Marshal(o["glossary"])
	if !strings.Contains(string(blob), "the proving tests recorded at finish") {
		t.Fatalf("overview glossary wrong: %s", blob)
	}
	if strings.Contains(string(blob), "doomed") {
		t.Fatalf("deleted term still listed: %s", blob)
	}

	// Board: glossary section and hover-marked, linked occurrences.
	call(t, cs, "create_node", map[string]any{"kind": "story", "title": "s", "body": "check the evidence after finish"})
	html := renderBoard(t, cs)
	for _, want := range []string{`id="glossary"`, `id="gloss-evidence"`,
		`href="#gloss-evidence" title="the proving tests recorded at finish">evidence</a>`} {
		if !strings.Contains(html, want) {
			t.Errorf("board missing %q", want)
		}
	}
}

// lastEngine lets acceptance tests reach the engine behind the MCP session
// for surfaces that are human-facing (the board), not part of the tool API.
var (
	lastEngine *core.Engine
	lastStore  *store.Store
)

func renderBoard(t *testing.T, _ *mcp.ClientSession) string {
	t.Helper()
	if lastEngine == nil {
		t.Fatal("no engine captured")
	}
	html, err := board.Render(lastEngine)
	if err != nil {
		t.Fatal(err)
	}
	return html
}

func withoutBoardStamp(html string) string {
	start := strings.Index(html, " · generated ")
	if start < 0 {
		return html
	}
	end := strings.Index(html[start:], "</p>")
	if end < 0 {
		return html
	}
	return html[:start] + html[start+end:]
}

// TestDescriptionAcceptance_AT_28 proves AT-28 (US-24 "Project
// description") through MCP plus the rendered surfaces.
func TestDescriptionAcceptance_AT_28(t *testing.T) {
	cs := client(t)
	call(t, cs, "set_description", map[string]any{"description": "deterministic spec tracking for LLM-driven development"})
	callErr(t, cs, "set_description", map[string]any{"description": strings.Repeat("x", 250)},
		"exceeds 200 characters", "GitHub style")

	o := call(t, cs, "get_overview", map[string]any{})
	if o["description"] != "deterministic spec tracking for LLM-driven development" {
		t.Fatalf("overview description = %v", o["description"])
	}

	html := renderBoard(t, cs)
	if !strings.Contains(html, "deterministic spec tracking for LLM-driven development") {
		t.Fatal("board missing description")
	}
}

// TestBatchApprovalAcceptance_AT_30 proves AT-30 (US-26 "Batch tree
// approval") through MCP: full tree read, batch approval, all-or-nothing
// rejection, sequencing exemption.
func TestBatchApprovalAcceptance_AT_30(t *testing.T) {
	cs := client(t)
	story := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "s", "body": "story body text"})["id"].(string)
	call(t, cs, "add_acceptance_criterion", map[string]any{"story_id": story, "given": "g", "when": "w", "then": "t"})
	at := call(t, cs, "create_node", map[string]any{"kind": "acceptance_test", "parent_id": story, "title": "at", "body": "at body", "covers": []string{story + ".AC-1"}})["id"].(string)
	arch := call(t, cs, "create_node", map[string]any{"kind": "arch", "parent_id": story, "title": "as", "body": "arch body"})["id"].(string)
	it := call(t, cs, "create_node", map[string]any{"kind": "integration_test", "parent_id": arch, "title": "it"})["id"].(string)
	dd := call(t, cs, "create_node", map[string]any{"kind": "detail_design", "parent_id": arch, "title": "dd"})["id"].(string)
	ut := call(t, cs, "create_node", map[string]any{"kind": "unit_test", "parent_id": dd, "title": "ut"})["id"].(string)
	prereq := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "prereq"})["id"].(string)
	call(t, cs, "link_dependency", map[string]any{"node_id": story, "target_id": prereq})

	// Full tree read carries the bodies.
	tree := call(t, cs, "get_tree", map[string]any{"story_id": story, "full": true})
	blob, _ := json.Marshal(tree)
	for _, want := range []string{"story body text", "at body", "arch body"} {
		if !strings.Contains(string(blob), want) {
			t.Fatalf("full tree missing %q", want)
		}
	}
	hashes := map[string]any{}
	var walk func(n map[string]any)
	walk = func(n map[string]any) {
		hashes[n["id"].(string)] = n["content_hash"]
		if cs, ok := n["children"].([]any); ok {
			for _, c := range cs {
				walk(c.(map[string]any))
			}
		}
	}
	walk(tree["story"].(map[string]any))

	// Partial batch: rejected, everything named, nothing approved.
	partial := map[string]any{story: hashes[story], at: hashes[at], "US-77": "x"}
	callErr(t, cs, "approve_tree", map[string]any{"story_id": story, "hashes": partial},
		"nothing was approved", "missing hash for "+arch, "missing hash for "+it,
		"missing hash for "+dd, "missing hash for "+ut, "US-77, which is not part of")
	if n := call(t, cs, "get_node", map[string]any{"id": story}); n["fresh"].(bool) {
		t.Fatal("nothing may be approved after a rejected batch")
	}

	// Edit invalidates a submitted hash: batch rejected with the mismatch.
	call(t, cs, "update_node", map[string]any{"id": dd, "body": "changed"})
	callErr(t, cs, "approve_tree", map[string]any{"story_id": story, "hashes": hashes},
		"hash mismatch for "+dd)

	// Fresh read, clean batch (sequencing link needs no hash), refine passes.
	tree = call(t, cs, "get_tree", map[string]any{"story_id": story, "full": true})
	hashes = map[string]any{}
	walk(tree["story"].(map[string]any))
	call(t, cs, "approve_tree", map[string]any{"story_id": story, "hashes": hashes})
	call(t, cs, "transition", map[string]any{"story_id": story, "action": "refine"})
	if st, _ := nodeStatus(t, cs, story); st != "refined" {
		t.Fatalf("status = %s, want refined", st)
	}
}

// clientOnPath builds an MCP session over a store opened on an existing
// database file — the shape two concurrent agent sessions have in reality.
func clientOnPath(t *testing.T, path, projectID string) *mcp.ClientSession {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	engine, err := core.NewEngine(st, projectID)
	if err != nil {
		t.Fatal(err)
	}
	lastEngine = engine
	lastStore = st
	ct, srvT := mcp.NewInMemoryTransports()
	srv := mcpserver.New(engine, "test")
	ctx := context.Background()
	srvSession, err := srv.Connect(ctx, srvT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srvSession.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// TestConcurrencyAcceptance_AT_31 proves AT-31 (US-27 "Atomic mutations")
// through two MCP sessions on one database: transition double-fire with one
// winner and single side effects, arch race hitting the invariant, reads
// while a mutation lock is held.
func TestConcurrencyAcceptance_AT_31(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	seed, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.CreateProject(store.Project{ID: "p1", Name: "t", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	seed.Close()
	cs1 := clientOnPath(t, path, "p1")
	cs2 := clientOnPath(t, path, "p1")

	story, _, _, _, _, _ := fullTreeMCP(t, cs1)

	// Double-fire refine from two sessions.
	type outcome struct {
		isErr bool
		text  string
	}
	res := make(chan outcome, 2)
	for _, cs := range []*mcp.ClientSession{cs1, cs2} {
		go func(cs *mcp.ClientSession) {
			r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "transition",
				Arguments: map[string]any{"story_id": story, "action": "refine"}})
			if err != nil {
				res <- outcome{true, err.Error()}
				return
			}
			res <- outcome{r.IsError, text(r)}
		}(cs)
	}
	var oks, blocks int
	for i := 0; i < 2; i++ {
		o := <-res
		switch {
		case !o.isErr:
			oks++
		case strings.Contains(o.text, `refine requires "todo"`):
			blocks++
		default:
			t.Fatalf("unexpected race outcome: %s", o.text)
		}
	}
	if oks != 1 || blocks != 1 {
		t.Fatalf("double-fire: %d ok / %d blocked, want 1/1", oks, blocks)
	}
	if st, _ := nodeStatus(t, cs1, story); st != "refined" {
		t.Fatalf("status = %s, want refined exactly once", st)
	}

	// Arch race on a fresh story: exactly one arch spec survives.
	s2 := call(t, cs1, "create_node", map[string]any{"kind": "story", "title": "racy"})["id"].(string)
	arch := make(chan bool, 2)
	for _, cs := range []*mcp.ClientSession{cs1, cs2} {
		go func(cs *mcp.ClientSession) {
			r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "create_node",
				Arguments: map[string]any{"kind": "arch", "parent_id": s2, "title": "as"}})
			arch <- err == nil && !r.IsError
		}(cs)
	}
	archOks := 0
	for i := 0; i < 2; i++ {
		if <-arch {
			archOks++
		}
	}
	if archOks != 1 {
		t.Fatalf("arch race produced %d arch specs, want 1", archOks)
	}

	// Reads answer while a mutation lock is held elsewhere.
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	unlock, err := st2.LockProject("p1")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		call(t, cs1, "get_overview", map[string]any{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("get_overview blocked by the mutation lock")
	}
	unlock()
}

// TestSchemaInteropAcceptance_AT_34 proves AT-34 (US-30 "Interoperable MCP
// output schemas"): every declared output schema is object-typed (the lint
// that guards regressions), and the wrapped tools return their envelopes.
func TestSchemaInteropAcceptance_AT_34(t *testing.T) {
	cs := client(t)
	tools, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools listed")
	}
	for _, tool := range tools.Tools {
		if tool.OutputSchema == nil {
			continue // omitted schema is fine; a wrong-typed one is not
		}
		blob, _ := json.Marshal(tool.OutputSchema)
		var schema struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(blob, &schema); err != nil {
			t.Fatalf("tool %s: unparsable output schema: %v", tool.Name, err)
		}
		if schema.Type != "object" {
			t.Errorf("tool %s: outputSchema.type = %q, want object — strict MCP clients reject the whole listing", tool.Name, schema.Type)
		}
	}

	// Envelope shapes.
	story := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "envelope probe"})["id"].(string)
	call(t, cs, "set_paths", map[string]any{"story_id": story, "paths": []string{"pkg"}})
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_specs",
		Arguments: map[string]any{"query": "envelope probe"}})
	if err != nil || res.IsError {
		t.Fatalf("search: %v %s", err, text(res))
	}
	var sOut struct {
		Hits []map[string]any `json:"hits"`
	}
	if err := json.Unmarshal([]byte(text(res)), &sOut); err != nil || len(sOut.Hits) != 1 {
		t.Fatalf("search envelope: %v %q", err, text(res))
	}
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "specs_for_path",
		Arguments: map[string]any{"path": "pkg/x.go"}})
	if err != nil || res.IsError {
		t.Fatalf("specs_for_path: %v %s", err, text(res))
	}
	var pOut struct {
		Stories []map[string]any `json:"stories"`
	}
	if err := json.Unmarshal([]byte(text(res)), &pOut); err != nil || len(pOut.Stories) != 1 {
		t.Fatalf("stories envelope: %v %q", err, text(res))
	}
}

// TestSchemaLintIntegration_IT_30 proves IT-30 (US-30): envelope roundtrips
// with hits present and empty — empty results are empty arrays, never null.
func TestSchemaLintIntegration_IT_30(t *testing.T) {
	cs := client(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "specs_for_path",
		Arguments: map[string]any{"path": "nowhere/x.go"}})
	if err != nil || res.IsError {
		t.Fatalf("specs_for_path: %v %s", err, text(res))
	}
	if got := strings.TrimSpace(text(res)); got != `{"stories":[]}` {
		t.Fatalf("empty stories envelope = %q", got)
	}
	res, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "search_specs",
		Arguments: map[string]any{"query": "zzz-nothing"}})
	if got := strings.TrimSpace(text(res)); got != `{"hits":[]}` {
		t.Fatalf("empty hits envelope = %q", got)
	}
}

// TestUnclaimedFilesMCPAcceptance_AT_42 proves AT-42 (US-38) at the actual
// MCP boundary: report arrays stay machine-readable and exhaustive.
func TestUnclaimedFilesMCPAcceptance_AT_42(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	for name, content := range map[string]string{
		".gitignore":     "reports/\n.trellis-worktrees/\n",
		"report-src.xml": `<?xml version="1.0"?><testsuite><testcase name="Test_UT_999"/><testcase name="TestFreeFloating"/></testsuite>`,
		"a/orphan.go":    "package orphan",
		"z/orphan.go":    "package orphan",
		"README.md":      "meta",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	cs := clientFor(t, store.Project{ID: "p1", Name: "test", RepoPath: repo, BaseBranch: "develop",
		TestCmd: "mkdir -p reports && cp report-src.xml reports/report.xml", JUnitGlob: "reports/*.xml"})
	story := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "claim fixture"})["id"].(string)
	call(t, cs, "set_paths", map[string]any{"story_id": story, "paths": []string{"report-src.xml"}})

	out := call(t, cs, "audit", map[string]any{})
	violations, ok := out["violations"].([]any)
	if !ok || len(violations) != 2 {
		t.Fatalf("machine-readable exhaustive violations: %#v", out)
	}
	joined := fmt.Sprint(violations)
	for _, want := range []string{"references nonexistent spec UT-999", "2 file(s) claimed by no story", "a/orphan.go", "z/orphan.go"} {
		if !strings.Contains(joined, want) {
			t.Errorf("MCP violations missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "README.md") {
		t.Fatalf("meta file became violation: %s", joined)
	}
	infos, ok := out["infos"].([]any)
	if !ok || !strings.Contains(fmt.Sprint(infos), "unbound") {
		t.Fatalf("unbound test must remain machine-readable info: %#v", out)
	}
}

// TestNextAcceptance_AT_39 proves AT-39 (US-35 "Next story") through MCP:
// candidates, blocked with dependency status, empty answer.
func TestNextAcceptance_AT_39(t *testing.T) {
	cs := client(t)
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "next_story", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("next_story: %v %s", err, text(res))
	}
	var empty struct {
		Candidates []any `json:"candidates"`
		Blocked    []any `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(text(res)), &empty); err != nil ||
		empty.Candidates == nil || empty.Blocked == nil ||
		len(empty.Candidates)+len(empty.Blocked) != 0 {
		t.Fatalf("empty backlog envelope: %q (%v)", text(res), err)
	}

	free, _, _, _, _, _ := fullTreeMCP(t, cs)
	waiting, _, _, _, _, _ := fullTreeMCP(t, cs)
	pre := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "prereq"})["id"].(string)
	call(t, cs, "link_dependency", map[string]any{"node_id": waiting, "target_id": pre})
	for _, s := range []string{free, waiting} {
		call(t, cs, "transition", map[string]any{"story_id": s, "action": "refine"})
	}
	res, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "next_story", Arguments: map[string]any{}})
	var out struct {
		Candidates []map[string]any `json:"candidates"`
		Blocked    []map[string]any `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(text(res)), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Candidates) != 1 || out.Candidates[0]["id"] != free {
		t.Fatalf("candidates: %v", out.Candidates)
	}
	if len(out.Blocked) != 1 || out.Blocked[0]["id"] != waiting ||
		!strings.Contains(fmt.Sprint(out.Blocked[0]["waiting_on"]), pre+" (todo)") {
		t.Fatalf("blocked: %v", out.Blocked)
	}
}

// TestActivityBackboneAcceptance_AT_47_UT_47_IT_44 proves compatibility
// without an activity and automatic, position-ordered activity creation.
func TestActivityBackboneAcceptance_AT_47_UT_47_IT_44(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	for name, content := range map[string]string{
		".gitignore":     "reports/\n",
		"report-src.xml": `<?xml version="1.0"?><testsuite></testsuite>`,
		"README.md":      "meta",
	} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	cs := clientFor(t, store.Project{ID: "p1", Name: "test", RepoPath: repo, BaseBranch: "develop",
		TestCmd: "mkdir -p reports && cp report-src.xml reports/report.xml", JUnitGlob: "reports/*.xml"})
	story := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "story"})["id"].(string)

	raw := func(tool string, args map[string]any) string {
		t.Helper()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: %v %s", tool, err, text(res))
		}
		return text(res)
	}
	baseline := map[string]string{
		"get_overview": raw("get_overview", map[string]any{}),
		"get_tree":     raw("get_tree", map[string]any{"story_id": story}),
		"next_story":   raw("next_story", map[string]any{}),
		"audit":        raw("audit", map[string]any{}),
		"board":        withoutBoardStamp(renderBoard(t, cs)),
	}
	if strings.Contains(baseline["get_overview"], `"activities"`) {
		t.Fatalf("overview without activity gained activities field: %s", baseline["get_overview"])
	}

	temporary := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "temporary"})["id"].(string)
	call(t, cs, "delete_node", map[string]any{"id": temporary})
	after := map[string]string{
		"get_overview": raw("get_overview", map[string]any{}),
		"get_tree":     raw("get_tree", map[string]any{"story_id": story}),
		"next_story":   raw("next_story", map[string]any{}),
		"audit":        raw("audit", map[string]any{}),
		"board":        withoutBoardStamp(renderBoard(t, cs)),
	}
	for surface, want := range baseline {
		if got := after[surface]; got != want {
			t.Errorf("%s changed without activity:\nwant %s\ngot  %s", surface, want, got)
		}
	}

	a1 := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Build", "position": 5})
	a2 := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Ship"})
	a3 := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Learn", "position": 1})
	if a1["id"] != "UA-2" || a1["position"] != float64(5) || a2["id"] != "UA-3" || a2["position"] != float64(6) || a3["position"] != float64(1) {
		t.Fatalf("activity creation: a1=%v a2=%v a3=%v", a1, a2, a3)
	}
	o := call(t, cs, "get_overview", map[string]any{})
	activities := o["activities"].([]any)
	got := []string{}
	for _, item := range activities {
		got = append(got, item.(map[string]any)["id"].(string))
	}
	if fmt.Sprint(got) != "[UA-4 UA-2 UA-3]" {
		t.Fatalf("activity order=%v", got)
	}
	call(t, cs, "update_node", map[string]any{"id": "UA-3", "position": 0})
	o = call(t, cs, "get_overview", map[string]any{})
	if o["activities"].([]any)[0].(map[string]any)["id"] != "UA-3" {
		t.Fatalf("updated activity position ignored: %v", o["activities"])
	}
}

// TestActivityReferenceAcceptance_AT_48_UT_47 proves exhaustive activity
// deletion guards and placed-story freshness isolation through MCP.
func TestActivityReferenceAcceptance_AT_48_UT_47(t *testing.T) {
	cs := client(t)
	activity := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Build", "body": "old"})["id"].(string)
	story1 := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "one"})["id"].(string)
	story2 := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "two"})["id"].(string)
	for _, story := range []string{story1, story2} {
		if err := lastStore.SetStoryActivity("p1", story, activity); err != nil {
			t.Fatal(err)
		}
	}
	callErr(t, cs, "delete_node", map[string]any{"id": activity}, "delete blocked", "placed story "+story1, "placed story "+story2)

	approveMCP(t, cs, story1)
	before := call(t, cs, "get_tree", map[string]any{"story_id": story1})
	boardBefore := withoutBoardStamp(renderBoard(t, cs))
	call(t, cs, "update_node", map[string]any{"id": activity, "title": "Build products", "body": "new"})
	after := call(t, cs, "get_tree", map[string]any{"story_id": story1})
	boardAfter := withoutBoardStamp(renderBoard(t, cs))
	beforeStory := before["story"].(map[string]any)
	afterStory := after["story"].(map[string]any)
	if beforeStory["content_hash"] != afterStory["content_hash"] || beforeStory["fresh"] != afterStory["fresh"] || fmt.Sprint(before["blocking_problems"]) != fmt.Sprint(after["blocking_problems"]) {
		t.Fatalf("activity edit changed story content hash, approval freshness, or blocking problems:\nbefore=%v\nafter=%v", before, after)
	}
	if boardAfter != boardBefore {
		t.Fatal("activity edit changed placed story integrity marker on board")
	}
}
