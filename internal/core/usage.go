package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"trellis/internal/model"
	"trellis/internal/store"
)

// AddUsage accumulates one trusted extension report for an existing story.
func (e *Engine) AddUsage(storyID string, main, subagents int64) error {
	return e.locked(func() error {
		var problems []string
		if main < 0 {
			problems = append(problems, "tokens_main must be a nonnegative integer")
		}
		if subagents < 0 {
			problems = append(problems, "tokens_subagents must be a nonnegative integer")
		}
		story, err := e.st.GetNode(e.pid(), storyID)
		if errors.Is(err, store.ErrNotFound) {
			problems = append(problems, fmt.Sprintf("story %s does not exist", storyID))
		} else if err != nil {
			return err
		} else if story.Kind != model.KindStory {
			problems = append(problems, fmt.Sprintf("%s is a %s, not a story", storyID, story.Kind))
		}
		if len(problems) > 0 {
			return fmt.Errorf("usage rejected: %s", strings.Join(problems, "; "))
		}
		if err := e.st.AddStoryUsage(e.pid(), storyID, main, subagents); err != nil {
			return err
		}
		e.st.AppendEvent(e.pid(), "usage_add", storyID, fmt.Sprintf("main=%d subagents=%d", main, subagents))
		return nil
	})
}

// FormatTokenCount renders tokens below 1000 raw and floors larger values to whole k.
func FormatTokenCount(tokens int64) string {
	return formatTokenCount(uint64(tokens))
}

func formatTokenCount(tokens uint64) string {
	if tokens < 1000 {
		return strconv.FormatUint(tokens, 10)
	}
	return strconv.FormatUint(tokens/1000, 10) + "k"
}

// FormatUsage renders total token usage and its subagent share without signed overflow.
func FormatUsage(main, subagents int64) string {
	total := uint64(main) + uint64(subagents)
	return fmt.Sprintf("%s (%s sub)", formatTokenCount(total), formatTokenCount(uint64(subagents)))
}
