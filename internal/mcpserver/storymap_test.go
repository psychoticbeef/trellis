package mcpserver_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"trellis/internal/model"
)

func rawCall(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil || res.IsError {
		t.Fatalf("%s: %v %s", tool, err, text(res))
	}
	return text(res)
}

func TestMapOverviewAcceptance_AT_55_AT_56_IT_49(t *testing.T) {
	cs := client(t)
	storyIDs := make([]string, 7)
	for i := range storyIDs {
		storyIDs[i] = call(t, cs, "create_node", map[string]any{"kind": "story", "title": fmt.Sprintf("story %d", i+1)})["id"].(string)
	}
	build := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Build", "position": 2})["id"].(string)
	ship := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Ship", "position": 1})["id"].(string)
	learn := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Learn", "position": 3})["id"].(string)
	for _, activity := range []string{build, ship, learn} {
		approveMCP(t, cs, activity)
	}
	placements := []struct {
		story, activity string
		rank, slice     int
		status          string
	}{
		{storyIDs[0], build, 2, 2, model.StatusDone},
		{storyIDs[2], ship, 1, 1, model.StatusRefined},
		{storyIDs[3], build, 1, 2, model.StatusTodo},
		{storyIDs[4], build, 1, 3, model.StatusDone},
		{storyIDs[6], learn, 1, 3, model.StatusInProgress},
	}
	for _, placement := range placements {
		if err := lastStore.SetStoryPlacement("p1", placement.story, placement.activity, placement.rank, placement.slice); err != nil {
			t.Fatal(err)
		}
		if err := lastStore.SetNodeStatus("p1", placement.story, placement.status); err != nil {
			t.Fatal(err)
		}
	}

	overview := call(t, cs, "get_overview", map[string]any{})
	storyMap := overview["story_map"].(map[string]any)
	if storyMap["status"] != "2 unmapped" || !reflect.DeepEqual(storyMap["unmapped_story_ids"], []any{storyIDs[1], storyIDs[5]}) {
		t.Fatalf("map status=%v", storyMap)
	}
	groups := storyMap["groups"].([]any)
	activityIDs := []string{}
	for _, rawGroup := range groups[:3] {
		activityIDs = append(activityIDs, rawGroup.(map[string]any)["activity"].(map[string]any)["id"].(string))
	}
	if !reflect.DeepEqual(activityIDs, []string{ship, build, learn}) || groups[3].(map[string]any)["unmapped"] != true {
		t.Fatalf("group order=%v", groups)
	}
	if got := groups[3].(map[string]any)["stories"].([]any); len(got) != 2 || got[0].(map[string]any)["id"] != storyIDs[1] || got[1].(map[string]any)["id"] != storyIDs[5] {
		t.Fatalf("unmapped group=%v", got)
	}
	wantProgress := [][][]int{
		{{1, 0, 1}, {2, 0, 0}, {3, 0, 0}},
		{{1, 0, 0}, {2, 1, 2}, {3, 1, 1}},
		{{1, 0, 0}, {2, 0, 0}, {3, 0, 1}},
	}
	for groupIndex, want := range wantProgress {
		group := groups[groupIndex].(map[string]any)
		for _, rawStory := range group["stories"].([]any) {
			if _, ok := rawStory.(map[string]any)["slice"]; !ok {
				t.Fatalf("placed story lacks slice: %v", rawStory)
			}
		}
		progress := group["slice_progress"].([]any)
		if len(progress) != len(want) {
			t.Fatalf("group %d progress=%v", groupIndex, progress)
		}
		for i, counts := range want {
			got := progress[i].(map[string]any)
			if got["slice"] != float64(counts[0]) || got["done"] != float64(counts[1]) || got["total"] != float64(counts[2]) {
				t.Fatalf("group %d progress[%d]=%v want %v", groupIndex, i, got, counts)
			}
		}
	}
	gaps := storyMap["gaps"].([]any)
	gotGaps := make([]string, 0, len(gaps))
	for _, rawGap := range gaps {
		gap := rawGap.(map[string]any)
		gotGaps = append(gotGaps, fmt.Sprintf("%s:%d", gap["activity_id"], int(gap["slice"].(float64))))
	}
	wantGaps := []string{ship + ":2", ship + ":3", build + ":1", learn + ":1", learn + ":2"}
	if !reflect.DeepEqual(gotGaps, wantGaps) {
		t.Fatalf("gaps=%v want %v", gotGaps, wantGaps)
	}
	call(t, cs, "set_map_position", map[string]any{"story_id": storyIDs[1], "activity_id": build, "slice": 1})
	call(t, cs, "set_map_position", map[string]any{"story_id": storyIDs[5], "activity_id": learn, "slice": 2})
	complete := call(t, cs, "get_overview", map[string]any{})["story_map"].(map[string]any)
	if complete["status"] != "map complete" || len(complete["unmapped_story_ids"].([]any)) != 0 {
		t.Fatalf("complete map=%v", complete)
	}
}

func TestMapAwareNextAcceptance_AT_57_IT_50(t *testing.T) {
	cs := client(t)
	storyIDs := make([]string, 7)
	for i := range storyIDs {
		storyIDs[i] = call(t, cs, "create_node", map[string]any{"kind": "story", "title": fmt.Sprintf("story %d", i+1)})["id"].(string)
	}
	activity := call(t, cs, "create_node", map[string]any{"kind": "activity", "title": "Build"})["id"].(string)
	for _, placement := range []struct{ index, rank, slice int }{{0, 2, 2}, {2, 5, 1}, {3, 1, 2}, {4, 1, 2}} {
		if err := lastStore.SetStoryPlacement("p1", storyIDs[placement.index], activity, placement.rank, placement.slice); err != nil {
			t.Fatal(err)
		}
	}
	if err := lastEngine.LinkDep(storyIDs[5], storyIDs[6]); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if err := lastStore.SetNodeStatus("p1", storyIDs[i], model.StatusRefined); err != nil {
			t.Fatal(err)
		}
	}
	next := call(t, cs, "next_story", map[string]any{})
	candidates := next["candidates"].([]any)
	got := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		got = append(got, candidate.(map[string]any)["id"].(string))
	}
	want := []string{storyIDs[2], storyIDs[3], storyIDs[4], storyIDs[0], storyIDs[1]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidate order=%v want %v", got, want)
	}
	blocked := next["blocked"].([]any)
	if len(blocked) != 1 || blocked[0].(map[string]any)["id"] != storyIDs[5] || !reflect.DeepEqual(blocked[0].(map[string]any)["waiting_on"], []any{storyIDs[6] + " (todo)"}) {
		t.Fatalf("blocked=%v", blocked)
	}
}

func TestNoMapReadCompatibility_AT_58_UT_54_IT_50(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		cs := client(t)
		if got, want := rawCall(t, cs, "get_overview", map[string]any{}), `{"cross_cutting":[],"glossary":[],"project":"test","stale_nodes":[],"stories":[]}`; got != want {
			t.Fatalf("empty overview changed:\nwant %s\ngot  %s", want, got)
		}
		if got, want := rawCall(t, cs, "next_story", map[string]any{}), `{"blocked":[],"candidates":[]}`; got != want {
			t.Fatalf("empty next changed:\nwant %s\ngot  %s", want, got)
		}
	})

	t.Run("candidate and blocked", func(t *testing.T) {
		cs := client(t)
		prerequisite := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "prerequisite"})["id"].(string)
		free := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "free"})["id"].(string)
		waiting := call(t, cs, "create_node", map[string]any{"kind": "story", "title": "waiting"})["id"].(string)
		if err := lastEngine.LinkDep(waiting, prerequisite); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{free, waiting} {
			if err := lastStore.SetNodeStatus("p1", id, model.StatusRefined); err != nil {
				t.Fatal(err)
			}
		}
		wantOverview := fmt.Sprintf(`{"cross_cutting":[],"glossary":[],"project":"test","stale_nodes":["%s never approved","%s never approved","%s never approved"],"stories":[{"gates_open":false,"id":"%s","status":"todo","title":"prerequisite"},{"gates_open":false,"id":"%s","status":"refined","title":"free"},{"gates_open":false,"id":"%s","status":"refined","title":"waiting"}]}`, prerequisite, free, waiting, prerequisite, free, waiting)
		if got := rawCall(t, cs, "get_overview", map[string]any{}); got != wantOverview {
			t.Fatalf("populated overview changed:\nwant %s\ngot  %s", wantOverview, got)
		}
		wantNext := fmt.Sprintf(`{"blocked":[{"id":"%s","title":"waiting","waiting_on":["%s (todo)"]}],"candidates":[{"gates_open":false,"id":"%s","status":"refined","title":"free"}]}`, waiting, prerequisite, free)
		if got := rawCall(t, cs, "next_story", map[string]any{}); got != wantNext {
			t.Fatalf("populated next changed:\nwant %s\ngot  %s", wantNext, got)
		}
	})
}
