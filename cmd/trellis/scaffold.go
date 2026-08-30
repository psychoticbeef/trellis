package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// scaffold wires a repo to a trellis project: MCP registration, agent
// instructions and git hooks. Create-if-absent, never overwrite — existing
// files are reported and preserved. It returns the messages plus the list of
// created repo files (hooks excluded), so init can commit exactly those.
func scaffold(repo, projectID string) ([]string, []string) {
	var msgs []string
	var created []string
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
		if !strings.HasPrefix(rel, ".git"+string(os.PathSeparator)) {
			created = append(created, rel)
		}
	}
	// The worktree ignore line is the one sanctioned amendment to an existing
	// file: start depends on it (US-29).
	if changed, msg := ensureIgnoreLine(repo); msg != "" {
		msgs = append(msgs, msg)
		if changed {
			created = append(created, ".gitignore")
		}
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
- Check the glossary (get_overview) and reuse its exact wording in every
  spec; define new project terms with define_term, ultra short.
`, projectID), 0o644)

	if fi, err := os.Stat(filepath.Join(repo, ".git")); err == nil && fi.IsDir() {
		write(filepath.Join(".git", "hooks", "pre-commit"), preCommitHook(projectID), 0o755)
		write(filepath.Join(".git", "hooks", "pre-push"), prePushHook(projectID), 0o755)
	} else {
		msgs = append(msgs, "no .git directory, hooks skipped")
	}
	if _, err := exec.LookPath("trellis"); err != nil {
		msgs = append(msgs, "warning: trellis is not on PATH — the installed hooks and .mcp.json invoke `trellis`; install the binary into your PATH (e.g. cp trellis /usr/local/bin/)")
	}
	return msgs, created
}

// ensureIgnoreLine makes sure .trellis-worktrees/ is git-ignored: creates
// .gitignore or appends the line, preserving existing content.
func ensureIgnoreLine(repo string) (changed bool, msg string) {
	const line = ".trellis-worktrees/"
	path := filepath.Join(repo, ".gitignore")
	data, err := os.ReadFile(path)
	if err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(l) == line {
				return false, ".gitignore already ignores " + line
			}
		}
		content := string(data)
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		if err := os.WriteFile(path, []byte(content+line+"\n"), 0o644); err != nil {
			return false, ".gitignore: " + err.Error()
		}
		return true, ".gitignore amended with " + line
	}
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		return false, ".gitignore: " + err.Error()
	}
	return true, ".gitignore created with " + line
}

// commitScaffold commits the wiring files init just created. Init bypasses
// the hooks it installed — trellis is the authority those hooks defend.
func commitScaffold(repo string, files []string) string {
	if len(files) == 0 {
		return "nothing new to commit"
	}
	if fi, err := os.Stat(filepath.Join(repo, ".git")); err != nil || !fi.IsDir() {
		return "no .git directory, nothing committed"
	}
	args := append([]string{"-C", repo, "add", "--"}, files...)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return "staging wiring files failed: " + strings.TrimSpace(string(out))
	}
	out, err := exec.Command("git", "-C", repo, "commit", "--no-verify", "-m", "trellis init: repo wiring").CombinedOutput()
	if err != nil {
		return "wiring commit failed: " + strings.TrimSpace(string(out))
	}
	return "wiring committed (" + strings.Join(files, ", ") + ")"
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
	c.Dir = resolveGateDir(p.RepoPath)
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

// resolveGateDir picks where a gate command runs: the caller's worktree when
// the current directory belongs to the project repository (so hook feedback
// reflects the committing worktree), otherwise the configured repo path.
func resolveGateDir(repoPath string) string {
	top, err := gitOut(".", "rev-parse", "--show-toplevel")
	if err != nil {
		return repoPath
	}
	cwdCommon, err := gitOut(".", "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return repoPath
	}
	repoCommon, err := gitOut(repoPath, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return repoPath
	}
	if cwdCommon == repoCommon {
		return top
	}
	return repoPath
}

func gitOut(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	out, err := c.Output()
	if err != nil {
		return "", err
	}
	return trimNewline(string(out)), nil
}

// Hook templates: the single source of the "current generation" — doctor
// judges installed hooks against exactly these.
func preCommitHook(projectID string) string {
	return fmt.Sprintf(`#!/bin/sh
# installed by trellis init — second defense line; finish stays the authority
trellis gate branch --project %s || exit 1
exec trellis gate lint --project %s
`, projectID, projectID)
}

func prePushHook(projectID string) string {
	return fmt.Sprintf(`#!/bin/sh
# installed by trellis init — second defense line; finish stays the authority
exec trellis gate test --project %s
`, projectID)
}
