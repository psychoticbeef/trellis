package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	story := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "done story"})["id"].(string)
	activity := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Build"})["id"].(string)
	approveMCP(t, cs, activity)
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

// TestNoActivityCreationCompatibility_AT_50_AT_67_IT_57 proves story creation
// stays placement-optional when no activity exists.
func TestNoActivityCreationCompatibility_AT_50_AT_67_IT_57(t *testing.T) {
	cs := client(t)
	story := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "unmapped"})
	for _, field := range []string{"activity", "rank", "slice"} {
		if _, ok := story[field]; ok {
			t.Fatalf("unmapped story unexpectedly has %s: %v", field, story)
		}
	}
}

// TestPlacementActivityApprovalAcceptance_AT_65_IT_57 proves both MCP
// placement entry points reject never-approved and stale activity targets,
// then succeed after approval.
func TestPlacementActivityApprovalAcceptance_AT_65_IT_57(t *testing.T) {
	cs := client(t)
	story := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "unmapped"})["id"].(string)
	activity := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Build", "body": "original"})["id"].(string)

	callErr(t, cs, "set_map_position", map[string]any{"story_id": story, "activity_id": activity, "slice": 1},
		"set_map_position placement rejected", "activity "+activity+" is never approved")
	callErr(t, cs, "create_node", map[string]any{"kind": "story", "title": "blocked", "activity_id": activity, "slice": 2},
		"create_node placement rejected", "activity "+activity+" is never approved")
	unmapped := call(t, cs, "get_node", map[string]any{"id": story})
	if _, ok := unmapped["activity"]; ok {
		t.Fatalf("rejected placement wrote story: %v", unmapped)
	}

	approveMCP(t, cs, activity)
	call(t, cs, "update_node", map[string]any{"id": activity, "body": "changed"})
	callErr(t, cs, "set_map_position", map[string]any{"story_id": story, "activity_id": activity, "slice": 1},
		"activity "+activity+" is stale", "changed since approval")
	callErr(t, cs, "create_node", map[string]any{"kind": "story", "title": "still blocked", "activity_id": activity, "slice": 2},
		"activity "+activity+" is stale", "changed since approval")

	approveMCP(t, cs, activity)
	placed := call(t, cs, "set_map_position", map[string]any{"story_id": story, "activity_id": activity, "slice": 1})
	created := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "placed", "activity_id": activity, "slice": 1})
	if created["id"] != "US-2" || placed["rank"] != float64(1) || created["rank"] != float64(2) || created["activity"] != activity {
		t.Fatalf("rejected create wrote state or approved placement changed: placed=%v created=%v", placed, created)
	}
}

// TestPlacedStoryActivityFreshnessAcceptance_AT_66_IT_57 proves activity
// edits stale only the activity, not placed-story approval.
func TestPlacedStoryActivityFreshnessAcceptance_AT_66_IT_57(t *testing.T) {
	cs := client(t)
	story := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "story"})["id"].(string)
	activity := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Build", "body": "original"})["id"].(string)
	approveMCP(t, cs, activity)
	approveMCP(t, cs, story)
	call(t, cs, "set_map_position", map[string]any{"story_id": story, "activity_id": activity, "slice": 1})
	before := call(t, cs, "get_node", map[string]any{"id": story})

	call(t, cs, "update_node", map[string]any{"id": activity, "title": "Build products", "body": "changed"})
	after := call(t, cs, "get_node", map[string]any{"id": story})
	activityAfter := call(t, cs, "get_node", map[string]any{"id": activity})
	if before["content_hash"] != after["content_hash"] || before["fresh"] != true || after["fresh"] != true || fmt.Sprint(before["problems"]) != fmt.Sprint(after["problems"]) {
		t.Fatalf("activity edit changed story approval: before=%v after=%v", before, after)
	}
	if activityAfter["fresh"] != false || !strings.Contains(fmt.Sprint(activityAfter["problems"]), "changed since approval") {
		t.Fatalf("edited activity did not report stale: %v", activityAfter)
	}
	approveMCP(t, cs, activity)
	if fresh := call(t, cs, "get_node", map[string]any{"id": activity})["fresh"]; fresh != true {
		t.Fatalf("re-approved activity fresh=%v", fresh)
	}
}

// TestPlacementGateAcceptance_AT_52_IT_47 proves dynamic map-complete
// derivation and mutation guards through MCP.
func TestPlacementGateAcceptance_AT_52_IT_47(t *testing.T) {
	cs := client(t)
	first := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "first unmapped"})["id"].(string)
	activity := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Build"})["id"].(string)
	approveMCP(t, cs, activity)
	second := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "second unmapped"})["id"].(string)
	call(t, cs, "set_map_position", map[string]any{"story_id": first, "activity_id": activity, "slice": 1})
	call(t, cs, "set_map_position", map[string]any{"story_id": second, "activity_id": activity, "slice": 2})

	callErr(t, cs, "create_node", map[string]any{"kind": "story", "title": "blocked"},
		"create_node placement gate rejected", "map complete", activity, "Build", "open slices", "1", "2", "3")
	third := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "placed after rejection", "activity_id": activity, "slice": 3})
	if third["id"] != "US-3" {
		t.Fatalf("rejected create consumed story id: %v", third)
	}
	callErr(t, cs, "set_map_position", map[string]any{"story_id": first, "activity_id": "", "slice": 0},
		"set_map_position placement gate rejected", "map complete", first, activity, "open slices")
	placed := call(t, cs, "get_node", map[string]any{"id": first})
	if placed["activity"] != activity || placed["slice"] != float64(1) {
		t.Fatalf("rejected placement clear changed story: %v", placed)
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

	unmapped := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "unmapped"})
	for _, field := range []string{"activity", "rank", "slice"} {
		if _, ok := unmapped[field]; ok {
			t.Fatalf("unmapped projection contains %s: %v", field, unmapped)
		}
	}
	build := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Build", "position": 2})["id"].(string)
	ship := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Ship", "position": 1})["id"].(string)
	approveMCP(t, cs, build)
	approveMCP(t, cs, ship)
	placed := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "placed", "activity_id": build, "slice": 3})
	if placed["activity"] != build || placed["rank"] != float64(1) || placed["slice"] != float64(3) {
		t.Fatalf("create_node placement=%v", placed)
	}
	call(t, cs, "create_node", map[string]any{"kind": "story", "title": "other activity", "activity_id": ship, "slice": 1})
	call(t, cs, "create_node", map[string]any{"kind": "story", "title": "other slice", "activity_id": build, "slice": 4})
	tree := call(t, cs, "get_tree", map[string]any{"story_id": placed["id"]})["story"].(map[string]any)
	if tree["activity"] != build || tree["rank"] != float64(1) || tree["slice"] != float64(3) {
		t.Fatalf("get_tree placement=%v", tree)
	}
	call(t, cs, "delete_node", map[string]any{"id": unmapped["id"]})
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
	callErr(t, cs, "set_map_position", map[string]any{"story_id": "US-999", "activity_id": "UA-999", "slice": 0},
		"set_map_position rejected", "US-999", "not found", "unknown activity", "slice must be at least 1", build, ship)
}
