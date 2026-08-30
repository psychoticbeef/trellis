package mcpserver_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"trellis/internal/core"
	"trellis/internal/mcpserver"
	"trellis/internal/store"
)

// client connects an MCP client to a trellis server over in-memory transports,
// backed by a real store — the exact surface an agent sees.
func client(t *testing.T) *mcp.ClientSession {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "trellis.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateProject(store.Project{ID: "p1", Name: "test", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	engine, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
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

	// Each illegal structural move names its violated rule.
	callErr(t, cs, "create_node", map[string]any{"kind": "story", "parent_id": "US-1", "title": "x"},
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
