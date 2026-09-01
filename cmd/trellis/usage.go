package main

import (
	"flag"
	"fmt"
	"io"
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
	fs := flag.NewFlagSet("usage add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mainValue := fs.String("main", "", "main-agent token count")
	subagentValue := fs.String("subagents", "", "subagent token count")
	if err := fs.Parse(args[3:]); err != nil {
		return fmt.Errorf("usage add: %w", err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage add: unexpected arguments: %v", fs.Args())
	}
	mainTokens, subagentTokens, err := parseTokenCounts(storyID, *mainValue, *subagentValue)
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

func parseTokenCounts(storyID, mainValue, subagentValue string) (int64, int64, error) {
	var problems []string
	parse := func(name, value string) int64 {
		if value == "" {
			problems = append(problems, fmt.Sprintf("--%s is required", name))
			return 0
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 0 {
			problems = append(problems, fmt.Sprintf("--%s must be a nonnegative integer", name))
			return 0
		}
		return n
	}
	mainTokens := parse("main", mainValue)
	subagentTokens := parse("subagents", subagentValue)
	if len(problems) > 0 {
		return 0, 0, fmt.Errorf("usage for story %s rejected: %s", storyID, strings.Join(problems, "; "))
	}
	return mainTokens, subagentTokens, nil
}
