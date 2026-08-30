package core

import (
	"fmt"
	"sort"

	"trellis/internal/model"
)

type BlockedStory struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	WaitingOn []string `json:"waiting_on"` // "US-n (status)" per unfinished dependency
}

// NextStories splits the refined backlog into start candidates and stories
// still waiting on sequencing dependencies — the same check start enforces.
func (e *Engine) NextStories() ([]StorySummary, []BlockedStory, error) {
	stories, err := e.st.ListNodesByKind(e.pid(), model.KindStory)
	if err != nil {
		return nil, nil, err
	}
	candidates := []StorySummary{}
	blocked := []BlockedStory{}
	for _, s := range stories {
		if s.Status != model.StatusRefined {
			continue
		}
		deps, err := e.st.ListDeps(e.pid(), s.ID)
		if err != nil {
			return nil, nil, err
		}
		var waiting []string
		for _, d := range deps {
			target, err := e.st.GetNode(e.pid(), d.TargetID)
			if err != nil {
				return nil, nil, err
			}
			if target.Kind == model.KindStory && target.Status != model.StatusDone {
				waiting = append(waiting, fmt.Sprintf("%s (%s)", target.ID, target.Status))
			}
		}
		if len(waiting) > 0 {
			blocked = append(blocked, BlockedStory{ID: s.ID, Title: s.Title, WaitingOn: waiting})
		} else {
			candidates = append(candidates, StorySummary{ID: s.ID, Title: s.Title, Status: s.Status})
		}
	}
	byID := func(a, b string) bool {
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		return a < b
	}
	sort.Slice(candidates, func(i, j int) bool { return byID(candidates[i].ID, candidates[j].ID) })
	sort.Slice(blocked, func(i, j int) bool { return byID(blocked[i].ID, blocked[j].ID) })
	return candidates, blocked, nil
}
