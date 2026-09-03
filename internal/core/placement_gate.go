package core

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"trellis/internal/model"
)

// placementGateState is derived from persisted activities and story placement
// on every guarded call. It is deliberately never stored.
type placementGateState struct {
	activities []model.Node
	stories    []model.Node
	unmapped   []model.Node
	openSlices []int
	complete   bool
}

func (e *Engine) derivePlacementGateState() (placementGateState, error) {
	activities, err := e.st.ListActivities(e.pid())
	if err != nil {
		return placementGateState{}, err
	}
	stories, err := e.st.ListNodesByKind(e.pid(), model.KindStory)
	if err != nil {
		return placementGateState{}, err
	}
	return makePlacementGateState(activities, stories), nil
}

func makePlacementGateState(activities, stories []model.Node) placementGateState {
	knownActivities := make(map[string]bool, len(activities))
	for _, activity := range activities {
		knownActivities[activity.ID] = true
	}
	state := placementGateState{activities: activities, stories: stories}
	for _, story := range stories {
		if !storyHasPlacement(story, knownActivities) {
			state.unmapped = append(state.unmapped, story)
		}
	}
	sort.Slice(state.unmapped, func(i, j int) bool { return state.unmapped[i].ID < state.unmapped[j].ID })
	state.openSlices = deriveOpenSlices(stories, knownActivities)
	state.complete = len(activities) > 0 && len(state.unmapped) == 0
	return state
}

func storyHasPlacement(story model.Node, knownActivities map[string]bool) bool {
	return story.ActivityID != "" && knownActivities[story.ActivityID] && story.Rank > 0 && story.Slice > 0
}

// Open slices are every slice already present plus the next slice. Empty maps
// start at slice 1. This finite set gives callers deterministic repair choices.
func deriveOpenSlices(stories []model.Node, knownActivities map[string]bool) []int {
	seen := map[int]bool{}
	maxSlice := 0
	for _, story := range stories {
		if !storyHasPlacement(story, knownActivities) {
			continue
		}
		seen[story.Slice] = true
		if story.Slice > maxSlice {
			maxSlice = story.Slice
		}
	}
	if maxSlice == 0 {
		return []int{1}
	}
	seen[maxSlice+1] = true
	out := make([]int, 0, len(seen))
	for slice := range seen {
		out = append(out, slice)
	}
	sort.Ints(out)
	return out
}

func placementGateError(operation string, state placementGateState, offenders []string) error {
	activities := make([]string, 0, len(state.activities))
	for _, activity := range state.activities {
		activities = append(activities, fmt.Sprintf("%s (%s)", activity.ID, activity.Title))
	}
	if len(activities) == 0 {
		activities = append(activities, "(none)")
	}
	slices := make([]string, 0, len(state.openSlices))
	for _, slice := range state.openSlices {
		slices = append(slices, strconv.Itoa(slice))
	}
	if len(slices) == 0 {
		slices = append(slices, "(none)")
	}
	parts := []string{fmt.Sprintf("%s placement gate rejected: map complete; operation would leave an unmapped story", operation)}
	if len(offenders) > 0 {
		sorted := append([]string(nil), offenders...)
		sort.Strings(sorted)
		parts = append(parts, "unmapped stories:\n- "+strings.Join(sorted, "\n- "))
	}
	parts = append(parts,
		"activities:\n- "+strings.Join(activities, "\n- "),
		"open slices:\n- "+strings.Join(slices, "\n- "))
	return fmt.Errorf("%s", strings.Join(parts, "\n"))
}
