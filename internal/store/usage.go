package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// StoryUsage holds accumulated token counts for one story.
type StoryUsage struct {
	TokensMain      int64
	TokensSubagents int64
}

// AddStoryUsage atomically increments both counters for a story.
func (s *Store) AddStoryUsage(projectID, storyID string, main, subagents int64) error {
	result, err := s.db.Exec(`INSERT INTO story_usage (project_id, story_id, tokens_main, tokens_subagents)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id, story_id) DO UPDATE SET
			tokens_main = tokens_main + excluded.tokens_main,
			tokens_subagents = tokens_subagents + excluded.tokens_subagents
		WHERE story_usage.tokens_main <= ? - excluded.tokens_main
		  AND story_usage.tokens_subagents <= ? - excluded.tokens_subagents`,
		projectID, storyID, main, subagents, int64(math.MaxInt64), int64(math.MaxInt64))
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		return fmt.Errorf("token usage overflow for story %s", storyID)
	}
	return nil
}

// GetStoryUsage returns ok=false when no usage was reported for the story.
func (s *Store) GetStoryUsage(projectID, storyID string) (usage StoryUsage, ok bool, err error) {
	err = s.db.QueryRow(`SELECT tokens_main, tokens_subagents FROM story_usage WHERE project_id=? AND story_id=?`,
		projectID, storyID).Scan(&usage.TokensMain, &usage.TokensSubagents)
	if errors.Is(err, sql.ErrNoRows) {
		return StoryUsage{}, false, nil
	}
	return usage, err == nil, err
}
