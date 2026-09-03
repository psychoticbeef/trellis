package core

import (
	"fmt"
	"sort"

	"trellis/internal/model"
)

type SliceProgress struct {
	Slice int `json:"slice"`
	Done  int `json:"done"`
	Total int `json:"total"`
}

type StoryMapGroup struct {
	Activity      *ActivitySummary `json:"activity,omitempty"`
	Unmapped      bool             `json:"unmapped,omitempty"`
	Stories       []StorySummary   `json:"stories"`
	SliceProgress []SliceProgress  `json:"slice_progress,omitempty"`
}

type StoryMapGap struct {
	ActivityID string `json:"activity_id"`
	Slice      int    `json:"slice"`
}

type StoryMapOverview struct {
	Status           string          `json:"status"`
	UnmappedStoryIDs []string        `json:"unmapped_story_ids"`
	Groups           []StoryMapGroup `json:"groups"`
	Gaps             []StoryMapGap   `json:"gaps"`
}

func deriveStoryMapGaps(activities, stories []model.Node) []StoryMapGap {
	knownActivities := make(map[string]bool, len(activities))
	for _, activity := range activities {
		knownActivities[activity.ID] = true
	}
	highestSlice := 0
	occupied := make(map[string]map[int]bool, len(activities))
	for _, story := range stories {
		if !storyHasPlacement(story, knownActivities) {
			continue
		}
		if occupied[story.ActivityID] == nil {
			occupied[story.ActivityID] = map[int]bool{}
		}
		occupied[story.ActivityID][story.Slice] = true
		if story.Slice > highestSlice {
			highestSlice = story.Slice
		}
	}
	gaps := []StoryMapGap{}
	for _, activity := range activities {
		for slice := 1; slice <= highestSlice; slice++ {
			if !occupied[activity.ID][slice] {
				gaps = append(gaps, StoryMapGap{ActivityID: activity.ID, Slice: slice})
			}
		}
	}
	return gaps
}

func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

func buildStoryMapOverview(activities, stories []model.Node, summaries []StorySummary) StoryMapOverview {
	byID := make(map[string]StorySummary, len(summaries))
	for _, summary := range summaries {
		byID[summary.ID] = summary
	}
	activityIndex := make(map[string]int, len(activities))
	knownActivities := make(map[string]bool, len(activities))
	groups := make([]StoryMapGroup, 0, len(activities)+1)
	for i, activity := range activities {
		summary := ActivitySummary{ID: activity.ID, Title: activity.Title, Position: activity.Position}
		groups = append(groups, StoryMapGroup{Activity: &summary, Stories: []StorySummary{}})
		activityIndex[activity.ID] = i
		knownActivities[activity.ID] = true
	}

	unmapped := make([]StorySummary, 0)
	highestSlice := 0
	for _, story := range stories {
		summary := byID[story.ID]
		if !storyHasPlacement(story, knownActivities) {
			unmapped = append(unmapped, summary)
			continue
		}
		idx := activityIndex[story.ActivityID]
		groups[idx].Stories = append(groups[idx].Stories, summary)
		if story.Slice > highestSlice {
			highestSlice = story.Slice
		}
	}
	for i := range groups {
		sort.Slice(groups[i].Stories, func(a, b int) bool {
			left, right := groups[i].Stories[a], groups[i].Stories[b]
			if *left.Slice != *right.Slice {
				return *left.Slice < *right.Slice
			}
			if *left.Rank != *right.Rank {
				return *left.Rank < *right.Rank
			}
			return idLess(left.ID, right.ID)
		})
	}
	sort.Slice(unmapped, func(i, j int) bool { return idLess(unmapped[i].ID, unmapped[j].ID) })

	for i := range activities {
		counts := make(map[int]SliceProgress, highestSlice)
		for slice := 1; slice <= highestSlice; slice++ {
			counts[slice] = SliceProgress{Slice: slice}
		}
		for _, story := range groups[i].Stories {
			progress := counts[*story.Slice]
			progress.Total++
			if story.Status == model.StatusDone {
				progress.Done++
			}
			counts[*story.Slice] = progress
		}
		for slice := 1; slice <= highestSlice; slice++ {
			progress := counts[slice]
			groups[i].SliceProgress = append(groups[i].SliceProgress, progress)
		}
	}
	groups = append(groups, StoryMapGroup{Unmapped: true, Stories: unmapped})

	unmappedIDs := make([]string, 0, len(unmapped))
	for _, story := range unmapped {
		unmappedIDs = append(unmappedIDs, story.ID)
	}
	status := "map complete"
	if len(unmappedIDs) > 0 {
		status = fmt.Sprintf("%d unmapped", len(unmappedIDs))
	}
	return StoryMapOverview{Status: status, UnmappedStoryIDs: unmappedIDs, Groups: groups, Gaps: deriveStoryMapGaps(activities, stories)}
}
