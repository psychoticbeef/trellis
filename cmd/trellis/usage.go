package main

import (
	"fmt"
	"strconv"
	"strings"

	"trellis/internal/core"
)

type usageValues struct {
	Categorized  bool
	Main         int64
	Subagents    int64
	MainCats     core.TokenCategories
	SubagentCats core.TokenCategories
}

var categorizedUsageFlags = []string{
	"main-input", "main-output", "main-cache-read", "main-cache-write",
	"subagents-input", "subagents-output", "subagents-cache-read", "subagents-cache-write",
}

func cmdUsage(args []string) error {
	if len(args) == 0 || args[0] != "add" {
		return fmt.Errorf("usage requires: add <project-id> <story-id> --main N --subagents N")
	}
	if len(args) < 3 {
		return fmt.Errorf("usage add requires a project id and story id")
	}
	projectID, storyID := args[1], args[2]
	values, parseProblems := parseUsageFlags(args[3:])
	e, st, err := engine(projectID)
	if err != nil {
		return err
	}
	defer st.Close()
	report := core.UsageReport{
		Categorized: values.Categorized, Main: values.Main, Subagents: values.Subagents,
		MainCats: values.MainCats, SubagentCats: values.SubagentCats,
	}
	if len(parseProblems) > 0 {
		coreProblems, err := e.UsageProblems(storyID, report)
		if err != nil {
			return err
		}
		problems := append(parseProblems, coreProblems...)
		return fmt.Errorf("usage for story %s rejected: %s", storyID, strings.Join(problems, "; "))
	}
	if err := e.AddUsageReport(storyID, report); err != nil {
		return err
	}
	if values.Categorized {
		fmt.Printf("usage added: %s main-input=%d main-output=%d main-cache-read=%d main-cache-write=%d subagents-input=%d subagents-output=%d subagents-cache-read=%d subagents-cache-write=%d\n",
			storyID, values.MainCats.Input, values.MainCats.Output, values.MainCats.CacheRead, values.MainCats.CacheWrite,
			values.SubagentCats.Input, values.SubagentCats.Output, values.SubagentCats.CacheRead, values.SubagentCats.CacheWrite)
	} else {
		fmt.Printf("usage added: %s main=%d subagents=%d\n", storyID, values.Main, values.Subagents)
	}
	return nil
}

func parseUsageFlags(args []string) (usageValues, []string) {
	allowed := map[string]bool{"main": true, "subagents": true}
	for _, name := range categorizedUsageFlags {
		allowed[name] = true
	}
	values := map[string]string{}
	seen := map[string]bool{}
	var problems []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			problems = append(problems, fmt.Sprintf("unexpected argument %q", arg))
			continue
		}
		flag := strings.TrimPrefix(arg, "--")
		name, value, hasValue := flag, "", false
		if before, after, ok := strings.Cut(flag, "="); ok {
			name, value, hasValue = before, after, true
		}
		if !allowed[name] {
			problems = append(problems, fmt.Sprintf("unknown flag --%s", name))
			continue
		}
		if !hasValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
			i++
			value, hasValue = args[i], true
		}
		if seen[name] {
			problems = append(problems, fmt.Sprintf("--%s specified more than once", name))
			continue
		}
		seen[name] = true
		if !hasValue || value == "" {
			problems = append(problems, fmt.Sprintf("--%s requires a value", name))
			continue
		}
		values[name] = value
	}

	legacySeen := seen["main"] || seen["subagents"]
	categorizedSeen := false
	for _, name := range categorizedUsageFlags {
		categorizedSeen = categorizedSeen || seen[name]
	}
	if legacySeen && categorizedSeen {
		problems = append(problems, "legacy and categorized usage flags cannot be mixed")
	}
	if !categorizedSeen {
		for _, name := range []string{"main", "subagents"} {
			if !seen[name] {
				problems = append(problems, fmt.Sprintf("--%s is required", name))
			}
		}
	}

	parsed := map[string]int64{}
	for name, value := range values {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 0 {
			problems = append(problems, fmt.Sprintf("--%s must be a nonnegative integer", name))
			continue
		}
		parsed[name] = n
	}
	result := usageValues{
		Categorized: categorizedSeen,
		Main:        parsed["main"],
		Subagents:   parsed["subagents"],
		MainCats: core.TokenCategories{
			Input: parsed["main-input"], Output: parsed["main-output"],
			CacheRead: parsed["main-cache-read"], CacheWrite: parsed["main-cache-write"],
		},
		SubagentCats: core.TokenCategories{
			Input: parsed["subagents-input"], Output: parsed["subagents-output"],
			CacheRead: parsed["subagents-cache-read"], CacheWrite: parsed["subagents-cache-write"],
		},
	}
	return result, problems
}
