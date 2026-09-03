package mcpserver_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

// TestPlacementHintAcceptance_AT_59_UT_59_IT_51 proves all US-47 acceptance
// criteria through MCP over a real SQLite FTS5 store.
func TestPlacementHintAcceptance_AT_59_UT_59_IT_51(t *testing.T) {
	cs := client(t)

	legacyRaw := rawCall(t, cs, "create_node", map[string]any{
		"kind": "story", "title": "legacy story", "body": "unchanged response",
	})
	var legacy map[string]any
	if err := json.Unmarshal([]byte(legacyRaw), &legacy); err != nil {
		t.Fatal(err)
	}
	if _, ok := legacy["placement_hint"]; ok {
		t.Fatalf("no story map create response carries hint: %s", legacyRaw)
	}
	if got := rawCall(t, cs, "get_node", map[string]any{"id": legacy["id"]}); got != legacyRaw {
		t.Fatalf("no story map create response changed:\ncreate %s\nnode   %s", legacyRaw, got)
	}

	definitions := []map[string]any{
		{"kind": "activity", "title": "quantum launch", "body": "quantum launch quantum launch", "position": 4},
		{"kind": "activity", "title": "quantum launch", "position": 1},
		{"kind": "activity", "title": "quantum launch", "position": 2},
		{"kind": "activity", "title": "quantum launch", "position": 3},
	}
	activities := make([]string, len(definitions))
	for i, definition := range definitions {
		activities[i] = call(t, cs, "create_node", definition)["id"].(string)
	}
	mapped := call(t, cs, "create_node", map[string]any{
		"kind": "story", "title": "placed", "activity_id": activities[0], "slice": 2,
	})
	if _, ok := mapped["placement_hint"]; ok {
		t.Fatalf("mapped creation carries hint: %v", mapped)
	}
	created := call(t, cs, "create_node", map[string]any{
		"kind": "story", "title": "quantum", "body": "launch",
	})
	hint, ok := created["placement_hint"].(map[string]any)
	if !ok {
		t.Fatalf("incomplete-map create response lacks hint: %v", created)
	}
	rawCandidates := hint["activities"].([]any)
	gotCandidates := make([]string, 0, len(rawCandidates))
	for _, raw := range rawCandidates {
		gotCandidates = append(gotCandidates, raw.(map[string]any)["id"].(string))
	}
	if want := []string{activities[0], activities[1], activities[2]}; !reflect.DeepEqual(gotCandidates, want) {
		t.Fatalf("candidates=%v want %v", gotCandidates, want)
	}
	rawGaps := hint["gaps"].([]any)
	gotGaps := make([]string, 0, len(rawGaps))
	for _, raw := range rawGaps {
		gap := raw.(map[string]any)
		gotGaps = append(gotGaps, fmt.Sprintf("%s:%d", gap["activity_id"], int(gap["slice"].(float64))))
	}
	wantGaps := []string{
		activities[1] + ":1", activities[1] + ":2",
		activities[2] + ":1", activities[2] + ":2",
		activities[3] + ":1", activities[3] + ":2",
		activities[0] + ":1",
	}
	if !reflect.DeepEqual(gotGaps, wantGaps) {
		t.Fatalf("gaps=%v want %v", gotGaps, wantGaps)
	}
	if persisted := call(t, cs, "get_node", map[string]any{"id": created["id"]}); persisted["placement_hint"] != nil {
		t.Fatalf("placement hint persisted: %v", persisted)
	}

	call(t, cs, "update_node", map[string]any{"id": activities[3], "title": "plasma deploy", "body": "plasma deploy"})
	updatedIndexStory := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "plasma", "body": "deploy"})
	updatedCandidates := updatedIndexStory["placement_hint"].(map[string]any)["activities"].([]any)
	if len(updatedCandidates) != 1 || updatedCandidates[0].(map[string]any)["id"] != activities[3] {
		t.Fatalf("updated activity index candidates=%v", updatedCandidates)
	}
	call(t, cs, "delete_node", map[string]any{"id": activities[3]})
	deletedIndexStory := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "plasma", "body": "deploy"})
	if candidates := deletedIndexStory["placement_hint"].(map[string]any)["activities"].([]any); len(candidates) != 0 {
		t.Fatalf("deleted activity remained candidate: %v", candidates)
	}

	for i, story := range []any{legacy["id"], created["id"], updatedIndexStory["id"], deletedIndexStory["id"]} {
		call(t, cs, "set_map_position", map[string]any{"story_id": story, "activity_id": activities[i%3], "slice": 1})
	}
	before := call(t, cs, "get_overview", map[string]any{})["stories"].([]any)
	callErr(t, cs, "create_node", map[string]any{"kind": "story", "title": "blocked"},
		"create_node placement gate rejected", "map complete", "activities:", "open slices:")
	after := call(t, cs, "get_overview", map[string]any{})["stories"].([]any)
	if len(after) != len(before) {
		t.Fatalf("placement gate rejection wrote story: before=%d after=%d", len(before), len(after))
	}
}
