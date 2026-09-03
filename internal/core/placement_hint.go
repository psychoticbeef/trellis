package core

import (
	"strings"

	"trellis/internal/model"
)

// PlacementHint is transient guidance returned only by successful unmapped
// story creation while a story map exists and remains incomplete.
type PlacementHint struct {
	Activities []ActivitySummary `json:"activities"`
	Gaps       []StoryMapGap     `json:"gaps"`
}

// PlacementHint derives guidance from current persisted map data. Map state is
// never stored; map complete keeps using the existing placement gate.
func (e *Engine) PlacementHint(title, body string) (*PlacementHint, error) {
	activities, err := e.st.ListActivities(e.pid())
	if err != nil {
		return nil, err
	}
	if len(activities) == 0 {
		return nil, nil
	}
	stories, err := e.st.ListNodesByKind(e.pid(), model.KindStory)
	if err != nil {
		return nil, err
	}
	if makePlacementGateState(activities, stories).complete {
		return nil, nil
	}

	ids, err := e.st.SearchActivityFTS(e.pid(), strings.TrimSpace(title+" "+body), 3)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]model.Node, len(activities))
	for _, activity := range activities {
		byID[activity.ID] = activity
	}
	candidates := make([]ActivitySummary, 0, len(ids))
	for _, id := range ids {
		activity, ok := byID[id]
		if !ok {
			continue
		}
		candidates = append(candidates, ActivitySummary{ID: activity.ID, Title: activity.Title, Position: activity.Position})
	}
	return &PlacementHint{Activities: candidates, Gaps: deriveStoryMapGaps(activities, stories)}, nil
}
