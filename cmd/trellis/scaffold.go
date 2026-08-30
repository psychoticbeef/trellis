package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// scaffold wires a repo to a trellis project: MCP registration, agent
// instructions and git hooks. Create-if-absent, never overwrite — existing
// files are reported and preserved.
func scaffold(repo, projectID string) []string {
	var msgs []string
	write := func(rel, content string, mode os.FileMode) {
		path := filepath.Join(repo, rel)
		if _, err := os.Stat(path); err == nil {
			msgs = append(msgs, rel+" exists, left untouched")
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			msgs = append(msgs, rel+": "+err.Error())
			return
		}
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			msgs = append(msgs, rel+": "+err.Error())
			return
		}
		msgs = append(msgs, rel+" created")
	}

	write(".mcp.json", fmt.Sprintf(`{
  "mcpServers": {
    "trellis": {
      "command": "trellis",
      "args": ["serve", "--project", "%s"]
    }
  }
}
`, projectID), 0o644)

	write("AGENTS.md", fmt.Sprintf(`# Agent instructions

trellis-project: %s

Specs, tickets and story state for this repository live in trellis — use its
MCP tools (server "trellis"). It is the single source of truth:

- Check get_overview and search_specs before picking up work; work only on
  stories, never on ad-hoc tasks outside a story.
- Done stories are the context source: read the relevant done trees
  (get_tree) and cross-cutting specs before designing or implementing. When
  reality diverges from a done spec, correct it in place and re-approve.
- Implement only via transition(story, "start") and complete via "finish".
  Never merge to the base branch yourself.
- Test names must reference the spec ids they prove (e.g. TestFoo_UT_3).
`, projectID), 0o644)

	if fi, err := os.Stat(filepath.Join(repo, ".git")); err == nil && fi.IsDir() {
		write(filepath.Join(".git", "hooks", "pre-commit"), fmt.Sprintf(`#!/bin/sh
# installed by trellis init — second defense line; finish stays the authority
trellis gate branch --project %s || exit 1
exec trellis gate lint --project %s
`, projectID, projectID), 0o755)
		write(filepath.Join(".git", "hooks", "pre-push"), fmt.Sprintf(`#!/bin/sh
# installed by trellis init — second defense line; finish stays the authority
exec trellis gate test --project %s
`, projectID), 0o755)
	} else {
		msgs = append(msgs, "no .git directory, hooks skipped")
	}
	return msgs
}

// cmdGate runs a configured gate command (lint or test) in the project repo.
// It reads the CURRENT config at run time, so reconfiguration needs no hook
// reinstall. Unconfigured gates pass with a notice.
func cmdGate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: trellis gate <lint|test> --project <id>")
	}
	which, rest := args[0], args[1:]
	fs := flag.NewFlagSet("gate", flag.ExitOnError)
	project := fs.String("project", "", "project id (required)")
	fs.Parse(rest)
	if *project == "" {
		return fmt.Errorf("gate requires --project")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	p, err := st.GetProject(*project)
	if err != nil {
		return err
	}
	var cmd string
	switch which {
	case "lint":
		cmd = p.LintCmd
	case "test":
		cmd = p.TestCmd
	case "branch":
		cur, err := exec.Command("git", "-C", ".", "rev-parse", "--abbrev-ref", "HEAD").Output()
		if err != nil {
			return fmt.Errorf("gate branch: %v", err)
		}
		if branch := trimNewline(string(cur)); branch == p.BaseBranch {
			return fmt.Errorf("gate branch failed: direct commits on %q are blocked — work happens in story worktrees, the base branch only receives trellis merges", p.BaseBranch)
		}
		return nil
	default:
		return fmt.Errorf("unknown gate %q; valid gates: lint, test, branch", which)
	}
	if cmd == "" {
		fmt.Printf("trellis gate %s: not configured for %s, passing\n", which, *project)
		return nil
	}
	c := exec.Command("sh", "-c", cmd)
	c.Dir = p.RepoPath
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("gate %s failed (%s): %w", which, cmd, err)
	}
	return nil
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
