package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
)

// TokenCategories holds categorized token counts for one agent group.
type TokenCategories struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

// StoryUsage holds accumulated legacy and categorized token counts for one story.
type StoryUsage struct {
	TokensMain      int64
	TokensSubagents int64
	Main            TokenCategories
	Subagents       TokenCategories
	Categorized     bool
}

// AddStoryUsage atomically increments both uncategorized legacy counters.
func (s *Store) AddStoryUsage(projectID, storyID string, main, subagents int64) error {
	return s.addStoryUsage(projectID, storyID, StoryUsage{TokensMain: main, TokensSubagents: subagents})
}

// AddCategorizedStoryUsage atomically increments all categorized counters.
func (s *Store) AddCategorizedStoryUsage(projectID, storyID string, main, subagents TokenCategories) error {
	return s.addStoryUsage(projectID, storyID, StoryUsage{Main: main, Subagents: subagents, Categorized: true})
}

func (s *Store) addStoryUsage(projectID, storyID string, usage StoryUsage) error {
	categorized := int64(0)
	if usage.Categorized {
		categorized = 1
	}
	values := []any{
		projectID, storyID, usage.TokensMain, usage.TokensSubagents,
		usage.Main.Input, usage.Main.Output, usage.Main.CacheRead, usage.Main.CacheWrite,
		usage.Subagents.Input, usage.Subagents.Output, usage.Subagents.CacheRead, usage.Subagents.CacheWrite,
		categorized,
	}
	for range 10 {
		values = append(values, int64(math.MaxInt64))
	}
	result, err := s.db.Exec(`INSERT INTO story_usage (
			project_id, story_id, tokens_main, tokens_subagents,
			tokens_main_input, tokens_main_output, tokens_main_cache_read, tokens_main_cache_write,
			tokens_subagents_input, tokens_subagents_output, tokens_subagents_cache_read, tokens_subagents_cache_write,
			categorized)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, story_id) DO UPDATE SET
			tokens_main = tokens_main + excluded.tokens_main,
			tokens_subagents = tokens_subagents + excluded.tokens_subagents,
			tokens_main_input = tokens_main_input + excluded.tokens_main_input,
			tokens_main_output = tokens_main_output + excluded.tokens_main_output,
			tokens_main_cache_read = tokens_main_cache_read + excluded.tokens_main_cache_read,
			tokens_main_cache_write = tokens_main_cache_write + excluded.tokens_main_cache_write,
			tokens_subagents_input = tokens_subagents_input + excluded.tokens_subagents_input,
			tokens_subagents_output = tokens_subagents_output + excluded.tokens_subagents_output,
			tokens_subagents_cache_read = tokens_subagents_cache_read + excluded.tokens_subagents_cache_read,
			tokens_subagents_cache_write = tokens_subagents_cache_write + excluded.tokens_subagents_cache_write,
			categorized = max(categorized, excluded.categorized)
		WHERE story_usage.tokens_main <= ? - excluded.tokens_main
		  AND story_usage.tokens_subagents <= ? - excluded.tokens_subagents
		  AND story_usage.tokens_main_input <= ? - excluded.tokens_main_input
		  AND story_usage.tokens_main_output <= ? - excluded.tokens_main_output
		  AND story_usage.tokens_main_cache_read <= ? - excluded.tokens_main_cache_read
		  AND story_usage.tokens_main_cache_write <= ? - excluded.tokens_main_cache_write
		  AND story_usage.tokens_subagents_input <= ? - excluded.tokens_subagents_input
		  AND story_usage.tokens_subagents_output <= ? - excluded.tokens_subagents_output
		  AND story_usage.tokens_subagents_cache_read <= ? - excluded.tokens_subagents_cache_read
		  AND story_usage.tokens_subagents_cache_write <= ? - excluded.tokens_subagents_cache_write`, values...)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil {
		return err
	} else if changed == 0 {
		current, ok, err := s.GetStoryUsage(projectID, storyID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("token usage overflow for story %s: persisted counters unavailable", storyID)
		}
		affected := overflowingUsageCounters(current, usage)
		if len(affected) == 0 {
			return fmt.Errorf("token usage overflow for story %s: affected counters unavailable", storyID)
		}
		return fmt.Errorf("token usage overflow for story %s: %s", storyID, strings.Join(affected, ", "))
	}
	return nil
}

func overflowingUsageCounters(current, added StoryUsage) []string {
	counters := []struct {
		name           string
		current, added int64
	}{
		{"tokens_main", current.TokensMain, added.TokensMain},
		{"tokens_subagents", current.TokensSubagents, added.TokensSubagents},
		{"tokens_main_input", current.Main.Input, added.Main.Input},
		{"tokens_main_output", current.Main.Output, added.Main.Output},
		{"tokens_main_cache_read", current.Main.CacheRead, added.Main.CacheRead},
		{"tokens_main_cache_write", current.Main.CacheWrite, added.Main.CacheWrite},
		{"tokens_subagents_input", current.Subagents.Input, added.Subagents.Input},
		{"tokens_subagents_output", current.Subagents.Output, added.Subagents.Output},
		{"tokens_subagents_cache_read", current.Subagents.CacheRead, added.Subagents.CacheRead},
		{"tokens_subagents_cache_write", current.Subagents.CacheWrite, added.Subagents.CacheWrite},
	}
	var affected []string
	for _, counter := range counters {
		if counter.added > 0 && counter.current > math.MaxInt64-counter.added {
			affected = append(affected, counter.name)
		}
	}
	return affected
}

// GetStoryUsage returns ok=false when no usage was reported for the story.
func (s *Store) GetStoryUsage(projectID, storyID string) (usage StoryUsage, ok bool, err error) {
	var categorized int64
	err = s.db.QueryRow(`SELECT tokens_main, tokens_subagents,
			tokens_main_input, tokens_main_output, tokens_main_cache_read, tokens_main_cache_write,
			tokens_subagents_input, tokens_subagents_output, tokens_subagents_cache_read, tokens_subagents_cache_write,
			categorized
		FROM story_usage WHERE project_id=? AND story_id=?`, projectID, storyID).Scan(
		&usage.TokensMain, &usage.TokensSubagents,
		&usage.Main.Input, &usage.Main.Output, &usage.Main.CacheRead, &usage.Main.CacheWrite,
		&usage.Subagents.Input, &usage.Subagents.Output, &usage.Subagents.CacheRead, &usage.Subagents.CacheWrite,
		&categorized,
	)
	usage.Categorized = categorized != 0
	if errors.Is(err, sql.ErrNoRows) {
		return StoryUsage{}, false, nil
	}
	return usage, err == nil, err
}
