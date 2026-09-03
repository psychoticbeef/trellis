package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

// TestPlacementMutationAndProjection_AT_49_IT_46 proves placement through MCP
// preserves done-story lifecycle and approval state while projecting metadata.
func TestPlacementMutationAndProjection_AT_49_IT_46(t *testing.T) {
	cs := client(t)
	activity := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Build"})["id"].(string)
	story := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "done story"})["id"].(string)
	approveMCP(t, cs, story)
	if err := lastStore.SetNodeStatus("p1", story, model.StatusDone); err != nil {
		t.Fatal(err)
	}
	before := call(t, cs, "get_tree", map[string]any{"story_id": story})
	placed := call(t, cs, "set_map_position", map[string]any{"story_id": story, "activity_id": activity, "slice": 2})
	after := call(t, cs, "get_tree", map[string]any{"story_id": story})
	beforeStory := before["story"].(map[string]any)
	afterStory := after["story"].(map[string]any)
	if placed["status"] != model.StatusDone || after["status"] != model.StatusDone {
		t.Fatalf("done status changed: placed=%v tree=%v", placed, after)
	}
	if beforeStory["content_hash"] != afterStory["content_hash"] || beforeStory["fresh"] != afterStory["fresh"] ||
		fmt.Sprint(before["blocking_problems"]) != fmt.Sprint(after["blocking_problems"]) {
		t.Fatalf("placement changed content hash, approval freshness, or blocking problems:\nbefore=%v\nafter=%v", before, after)
	}
	if afterStory["activity"] != activity || afterStory["rank"] != float64(1) || afterStory["slice"] != float64(2) {
		t.Fatalf("tree placement missing: %v", afterStory)
	}
	node := call(t, cs, "get_node", map[string]any{"id": story})
	if node["activity"] != activity || node["rank"] != float64(1) || node["slice"] != float64(2) {
		t.Fatalf("node placement missing: %v", node)
	}
	overview := call(t, cs, "get_overview", map[string]any{})
	got := overview["stories"].([]any)[0].(map[string]any)
	if got["activity"] != activity || got["rank"] != float64(1) || got["slice"] != float64(2) {
		t.Fatalf("overview placement missing: %v", got)
	}
}

// TestOptionalUnmappedStory_AT_50 proves story creation stays placement-optional
// while the story map is incomplete.
func TestOptionalUnmappedStory_AT_50(t *testing.T) {
	cs := client(t)
	call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Build"})
	story := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "unmapped"})
	for _, field := range []string{"activity", "rank", "slice"} {
		if _, ok := story[field]; ok {
			t.Fatalf("unmapped story unexpectedly has %s: %v", field, story)
		}
	}
}

// TestPlacementRoundTripValidation_AT_51_UT_49_IT_46 proves object schemas,
// optional placement projections, YAML round-trip, and exhaustive errors.
func TestPlacementRoundTripValidation_AT_51_UT_49_IT_46(t *testing.T) {
	cs := client(t)
	tools, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name != "set_map_position" {
			continue
		}
		found = true
		blob, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]any
		if err := json.Unmarshal(blob, &schema); err != nil {
			t.Fatal(err)
		}
		if schema["type"] != "object" {
			t.Fatalf("set_map_position input schema=%s", blob)
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok || props["story_id"] == nil || props["activity_id"] == nil || props["slice"] == nil {
			t.Fatalf("set_map_position properties=%s", blob)
		}
		out, _ := json.Marshal(tool.OutputSchema)
		var outSchema map[string]any
		_ = json.Unmarshal(out, &outSchema)
		if outSchema["type"] != "object" {
			t.Fatalf("set_map_position output schema=%s", out)
		}
	}
	if !found {
		t.Fatal("set_map_position tool missing")
	}

	build := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Build", "position": 2})["id"].(string)
	ship := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Ship", "position": 1})["id"].(string)
	placed := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "placed", "activity_id": build, "slice": 3})
	if placed["activity"] != build || placed["rank"] != float64(1) || placed["slice"] != float64(3) {
		t.Fatalf("create_node placement=%v", placed)
	}
	call(t, cs, "create_node", map[string]any{"kind": "story", "title": "other activity", "activity_id": ship, "slice": 1})
	call(t, cs, "create_node", map[string]any{"kind": "story", "title": "other slice", "activity_id": build, "slice": 4})
	unmapped := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "unmapped"})
	for _, field := range []string{"activity", "rank", "slice"} {
		if _, ok := unmapped[field]; ok {
			t.Fatalf("unmapped projection contains %s: %v", field, unmapped)
		}
	}
	tree := call(t, cs, "get_tree", map[string]any{"story_id": placed["id"]})["story"].(map[string]any)
	if tree["activity"] != build || tree["rank"] != float64(1) || tree["slice"] != float64(3) {
		t.Fatalf("get_tree placement=%v", tree)
	}
	doc, err := lastEngine.ExportYAML()
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Import(lastStore, []byte(doc), store.Project{ID: "p2", Name: "test", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	copyEngine, err := core.NewEngine(lastStore, "p2")
	if err != nil {
		t.Fatal(err)
	}
	copyDoc, err := copyEngine.ExportYAML()
	if err != nil {
		t.Fatal(err)
	}
	if doc != copyDoc {
		t.Fatalf("placement YAML round trip diverged:\n--- original ---\n%s\n--- copy ---\n%s", doc, copyDoc)
	}
	copyNode, err := copyEngine.Node(placed["id"].(string))
	if err != nil || copyNode.Activity != build || copyNode.Rank == nil || *copyNode.Rank != 1 || copyNode.Slice == nil || *copyNode.Slice != 3 {
		t.Fatalf("imported placement=%+v err=%v", copyNode, err)
	}
	copyOverview, err := copyEngine.Overview()
	if err != nil || len(copyOverview.Activities) != 2 || copyOverview.Activities[0].ID != ship || copyOverview.Activities[1].ID != build {
		t.Fatalf("imported activity positions=%+v err=%v", copyOverview.Activities, err)
	}

	callErr(t, cs, "set_map_position", map[string]any{"story_id": placed["id"], "activity_id": "UA-999", "slice": 1},
		"set_map_position placement rejected", "unknown activity", build, ship)
	callErr(t, cs, "set_map_position", map[string]any{"story_id": placed["id"], "activity_id": build, "slice": 0},
		"set_map_position placement rejected", "slice must be at least 1", build, ship)
}
