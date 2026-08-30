package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"trellis/internal/gitops"
	"trellis/internal/store"
)

type finding struct {
	name    string
	level   string // ok | warn | fail
	hint    string
	fixable bool
	fix     func() // rewrites a trellis-owned artifact
}

// doctorChecks inspects the wiring matrix. Pure inspection: fixes are
// returned as closures and only run under --fix.
func doctorChecks(p store.Project) []finding {
	var fs []finding
	add := func(name, level, hint string, fixable bool, fix func()) {
		fs = append(fs, finding{name, level, hint, fixable, fix})
	}
	repo := p.RepoPath

	if _, err := exec.LookPath("trellis"); err != nil {
		add("trellis on PATH", "fail", "install the binary into your PATH (e.g. cp trellis /usr/local/bin/) — hooks and .mcp.json invoke it by name", false, nil)
	} else {
		add("trellis on PATH", "ok", "", false, nil)
	}

	g := gitops.Git{Dir: repo}
	if !g.IsRepo() {
		add("git repository", "fail", repo+" is not a git repository", false, nil)
		return fs
	}
	add("git repository", "ok", "", false, nil)

	for _, br := range []struct{ name, branch string }{{"base branch", p.BaseBranch}, {"release branch", p.ReleaseBranch}} {
		if br.branch == "" || !g.BranchExists(br.branch) {
			level, hint := "warn", fmt.Sprintf("branch %q does not exist: git branch %s", br.branch, br.branch)
			if br.name == "base branch" {
				level = "fail"
			} else {
				hint = fmt.Sprintf("branch %q does not exist yet — the first trellis release creates it", br.branch)
			}
			add(br.name, level, hint, false, nil)
		} else {
			add(br.name, "ok", "", false, nil)
		}
	}

	if !g.IsIgnored(".trellis-worktrees/probe") {
		add("worktree ignore line", "fail", ".trellis-worktrees/ missing in .gitignore — start is blocked without it", true, func() { ensureIgnoreLine(repo) })
	} else {
		add("worktree ignore line", "ok", "", false, nil)
	}

	hooks := []struct{ file, want string }{
		{".git/hooks/pre-commit", preCommitHook(p.ID)},
		{".git/hooks/pre-push", prePushHook(p.ID)},
	}
	for _, h := range hooks {
		h := h
		path := filepath.Join(repo, h.file)
		data, err := os.ReadFile(path)
		writeHook := func() { os.WriteFile(path, []byte(h.want), 0o755) }
		switch {
		case err != nil:
			add(h.file, "fail", "hook missing", true, writeHook)
		case string(data) != h.want:
			add(h.file, "fail", "hook outdated (not current generation)", true, writeHook)
		default:
			add(h.file, "ok", "", false, nil)
		}
	}

	mcp := filepath.Join(repo, ".mcp.json")
	if data, err := os.ReadFile(mcp); err != nil {
		add(".mcp.json", "fail", "missing MCP registration", true, func() { scaffold(repo, p.ID) })
	} else if !strings.Contains(string(data), p.ID) {
		add(".mcp.json", "warn", "exists but does not reference project "+p.ID+" — check manually, doctor never overwrites user config", false, nil)
	} else {
		add(".mcp.json", "ok", "", false, nil)
	}

	agents := filepath.Join(repo, "AGENTS.md")
	if data, err := os.ReadFile(agents); err != nil {
		add("AGENTS.md", "fail", "missing agent instructions", true, func() { scaffold(repo, p.ID) })
	} else if !strings.Contains(string(data), "trellis-project: "+p.ID) {
		add("AGENTS.md", "warn", "exists but lacks the pointer line 'trellis-project: "+p.ID+"' — add it manually", false, nil)
	} else {
		add("AGENTS.md", "ok", "", false, nil)
	}

	if p.TestCmd == "" || p.JUnitGlob == "" {
		add("gate config", "fail", fmt.Sprintf("test_cmd/junit_glob unset: trellis config %s --test '<cmd>' --junit '<glob>'", p.ID), false, nil)
	} else {
		add("gate config", "ok", "", false, nil)
	}
	if p.CoverageGlob == "" {
		add("coverage config", "warn", fmt.Sprintf("optional: trellis config %s --coverage '<glob>' makes test gaps visible", p.ID), false, nil)
	} else {
		add("coverage config", "ok", "", false, nil)
	}
	return fs
}

func cmdDoctor(args []string) error {
	// Accept --fix in any position (flag stops at the first positional arg).
	fixFlag := false
	var rest []string
	for _, a := range args {
		if a == "--fix" || a == "-fix" {
			fixFlag = true
			continue
		}
		rest = append(rest, a)
	}
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	fs.Parse(rest)
	fix := &fixFlag
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: trellis doctor <project-id> [--fix]")
	}
	st, err := openStore()
	if err != nil {
		return err
	}
	defer st.Close()
	p, err := st.GetProject(fs.Arg(0))
	if err != nil {
		return err
	}
	findings := doctorChecks(p)
	fixed := 0
	for _, f := range findings {
		if *fix && f.level == "fail" && f.fixable {
			f.fix()
			fixed++
			fmt.Printf("%-24s FIXED\n", f.name)
			continue
		}
		mark := map[string]string{"ok": "ok", "warn": "WARN", "fail": "FAIL"}[f.level]
		line := fmt.Sprintf("%-24s %s", f.name, mark)
		if f.hint != "" {
			line += "  — " + f.hint
		}
		fmt.Println(line)
	}
	fails := 0
	for _, f := range findings {
		if f.level == "fail" && !(*fix && f.fixable) {
			fails++
		}
	}
	if fixed > 0 {
		fmt.Printf("%d artifact(s) repaired\n", fixed)
	}
	if fails > 0 {
		return fmt.Errorf("%d failure(s) need attention", fails)
	}
	fmt.Println("doctor: wiring healthy")
	return nil
}
