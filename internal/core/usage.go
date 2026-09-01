package core

import (
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"trellis/internal/model"
	"trellis/internal/store"
)

// TokenCategories holds categorized token counts for one agent group.
type TokenCategories = store.TokenCategories

// UsageReport is one legacy or categorized trusted extension report.
type UsageReport struct {
	Categorized  bool
	Main         int64
	Subagents    int64
	MainCats     TokenCategories
	SubagentCats TokenCategories
}

// AddUsage preserves the legacy engine API while routing through one mutation.
func (e *Engine) AddUsage(storyID string, main, subagents int64) error {
	return e.AddUsageReport(storyID, UsageReport{Main: main, Subagents: subagents})
}

// AddCategorizedUsage preserves the categorized convenience API.
func (e *Engine) AddCategorizedUsage(storyID string, main, subagents TokenCategories) error {
	return e.AddUsageReport(storyID, UsageReport{Categorized: true, MainCats: main, SubagentCats: subagents})
}

// AddUsageReport validates and atomically accumulates one report in its selected mode.
func (e *Engine) AddUsageReport(storyID string, report UsageReport) error {
	return e.locked(func() error {
		problems, err := e.usageProblems(storyID, report)
		if err != nil {
			return err
		}
		if len(problems) > 0 {
			return fmt.Errorf("usage rejected: %s", strings.Join(problems, "; "))
		}
		if report.Categorized {
			if err := e.st.AddCategorizedStoryUsage(e.pid(), storyID, report.MainCats, report.SubagentCats); err != nil {
				return err
			}
			e.st.AppendEvent(e.pid(), "usage_add", storyID, fmt.Sprintf(
				"main_input=%d main_output=%d main_cache_read=%d main_cache_write=%d subagents_input=%d subagents_output=%d subagents_cache_read=%d subagents_cache_write=%d",
				report.MainCats.Input, report.MainCats.Output, report.MainCats.CacheRead, report.MainCats.CacheWrite,
				report.SubagentCats.Input, report.SubagentCats.Output, report.SubagentCats.CacheRead, report.SubagentCats.CacheWrite))
			return nil
		}
		if err := e.st.AddStoryUsage(e.pid(), storyID, report.Main, report.Subagents); err != nil {
			return err
		}
		e.st.AppendEvent(e.pid(), "usage_add", storyID, fmt.Sprintf("main=%d subagents=%d", report.Main, report.Subagents))
		return nil
	})
}

// UsageProblems returns all core validation problems without mutating usage.
func (e *Engine) UsageProblems(storyID string, report UsageReport) ([]string, error) {
	return e.usageProblems(storyID, report)
}

type namedToken struct {
	name  string
	value int64
}

func (e *Engine) usageProblems(storyID string, report UsageReport) ([]string, error) {
	tokens := []namedToken{
		{"tokens_main", report.Main}, {"tokens_subagents", report.Subagents},
		{"tokens_main_input", report.MainCats.Input}, {"tokens_main_output", report.MainCats.Output},
		{"tokens_main_cache_read", report.MainCats.CacheRead}, {"tokens_main_cache_write", report.MainCats.CacheWrite},
		{"tokens_subagents_input", report.SubagentCats.Input}, {"tokens_subagents_output", report.SubagentCats.Output},
		{"tokens_subagents_cache_read", report.SubagentCats.CacheRead}, {"tokens_subagents_cache_write", report.SubagentCats.CacheWrite},
	}
	var problems []string
	for _, token := range tokens {
		if token.value < 0 {
			problems = append(problems, token.name+" must be a nonnegative integer")
		}
	}
	categoriesPresent := report.MainCats != (TokenCategories{}) || report.SubagentCats != (TokenCategories{})
	legacyPresent := report.Main != 0 || report.Subagents != 0
	if (report.Categorized && legacyPresent) || (!report.Categorized && categoriesPresent) {
		problems = append(problems, "legacy and categorized usage counters cannot be mixed")
	}
	story, err := e.st.GetNode(e.pid(), storyID)
	if errors.Is(err, store.ErrNotFound) {
		problems = append(problems, fmt.Sprintf("story %s does not exist", storyID))
	} else if err != nil {
		return nil, err
	} else if story.Kind != model.KindStory {
		problems = append(problems, fmt.Sprintf("%s is a %s, not a story", storyID, story.Kind))
	}
	return problems, nil
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

func formatBigTokenCount(tokens *big.Int) string {
	if tokens.Cmp(big.NewInt(1000)) < 0 {
		return tokens.String()
	}
	return new(big.Int).Quo(new(big.Int).Set(tokens), big.NewInt(1000)).String() + "k"
}

// FormatUsage renders legacy total token usage and its subagent share without signed overflow.
func FormatUsage(main, subagents int64) string {
	total := uint64(main) + uint64(subagents)
	return fmt.Sprintf("%s (%s sub)", formatTokenCount(total), formatTokenCount(uint64(subagents)))
}

// FormatStoryUsage renders combined totals and categorized detail when present.
func FormatStoryUsage(usage store.StoryUsage) string {
	main := sumTokens(usage.TokensMain, usage.Main.Input, usage.Main.Output, usage.Main.CacheRead, usage.Main.CacheWrite)
	subagents := sumTokens(usage.TokensSubagents, usage.Subagents.Input, usage.Subagents.Output, usage.Subagents.CacheRead, usage.Subagents.CacheWrite)
	total := new(big.Int).Add(new(big.Int).Set(main), subagents)
	formatted := fmt.Sprintf("%s (%s sub)", formatBigTokenCount(total), formatBigTokenCount(subagents))
	if !hasCategorizedUsage(usage) {
		return formatted
	}
	output := sumTokens(usage.Main.Output, usage.Subagents.Output)
	cacheRead := sumTokens(usage.Main.CacheRead, usage.Subagents.CacheRead)
	cacheWrite := sumTokens(usage.Main.CacheWrite, usage.Subagents.CacheWrite)
	return fmt.Sprintf("%s · out %s · cache %s/%s r/w", formatted,
		formatBigTokenCount(output), formatBigTokenCount(cacheRead), formatBigTokenCount(cacheWrite))
}

func hasCategorizedUsage(usage store.StoryUsage) bool {
	return usage.Categorized
}

func sumTokens(tokens ...int64) *big.Int {
	total := new(big.Int)
	for _, token := range tokens {
		total.Add(total, big.NewInt(token))
	}
	return total
}
