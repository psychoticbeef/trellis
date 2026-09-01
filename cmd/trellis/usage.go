package main

import (
	"fmt"
	"strconv"
	"strings"
)

func cmdUsage(args []string) error {
	if len(args) == 0 || args[0] != "add" {
		return fmt.Errorf("usage requires: add <project-id> <story-id> --main N --subagents N")
	}
	if len(args) < 3 {
		return fmt.Errorf("usage add requires a project id and story id")
	}
	projectID, storyID := args[1], args[2]
	mainTokens, subagentTokens, err := parseUsageFlags(storyID, args[3:])
	if err != nil {
		return err
	}
	e, st, err := engine(projectID)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := e.AddUsage(storyID, mainTokens, subagentTokens); err != nil {
		return err
	}
	fmt.Printf("usage added: %s main=%d subagents=%d\n", storyID, mainTokens, subagentTokens)
	return nil
}

func parseUsageFlags(storyID string, args []string) (int64, int64, error) {
	values := map[string]string{}
	seen := map[string]bool{}
	var problems []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := "", "", false
		switch {
		case strings.HasPrefix(arg, "--main="):
			name, value, hasValue = "main", strings.TrimPrefix(arg, "--main="), true
		case strings.HasPrefix(arg, "--subagents="):
			name, value, hasValue = "subagents", strings.TrimPrefix(arg, "--subagents="), true
		case arg == "--main" || arg == "--subagents":
			name = strings.TrimPrefix(arg, "--")
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
				value, hasValue = args[i], true
			}
		case strings.HasPrefix(arg, "--"):
			problems = append(problems, fmt.Sprintf("unknown flag %s", arg))
			continue
		default:
			problems = append(problems, fmt.Sprintf("unexpected argument %q", arg))
			continue
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
	for _, name := range []string{"main", "subagents"} {
		if !seen[name] {
			problems = append(problems, fmt.Sprintf("--%s is required", name))
		}
	}
	parse := func(name string) int64 {
		value, ok := values[name]
		if !ok {
			return 0
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 0 {
			problems = append(problems, fmt.Sprintf("--%s must be a nonnegative integer", name))
			return 0
		}
		return n
	}
	mainTokens := parse("main")
	subagentTokens := parse("subagents")
	if len(problems) > 0 {
		return 0, 0, fmt.Errorf("usage for story %s rejected: %s", storyID, strings.Join(problems, "; "))
	}
	return mainTokens, subagentTokens, nil
}
