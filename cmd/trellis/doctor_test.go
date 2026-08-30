package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"trellis/internal/store"
)

func findingLevels(fs []finding) map[string]string {
	out := map[string]string{}
	for _, f := range fs {
		out[f.name] = f.level
	}
	return out
}

// TestDoctorChecks_UT_35 proves UT-35 (DD-36 "Doctor checks and fixers"):
// each drift class detected with the right severity and classification.
func TestDoctorChecks_UT_35(t *testing.T) {
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	p := store.Project{ID: "p1", Name: "t", RepoPath: repo, BaseBranch: "develop", ReleaseBranch: "main"}

	// Broken state: no wiring at all; PATH keeps git but loses trellis.
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	levels := findingLevels(doctorChecks(p))
	for name, want := range map[string]string{
		"trellis on PATH": "fail", "git repository": "ok", "base branch": "ok",
		"release branch": "warn", "worktree ignore line": "fail",
		".git/hooks/pre-commit": "fail", ".mcp.json": "fail", "AGENTS.md": "fail",
		"gate config": "fail", "coverage config": "warn",
	} {
		if levels[name] != want {
			t.Errorf("%s = %s, want %s", name, levels[name], want)
		}
	}

	// Old-generation hook: exists but lacks the branch gate.
	os.MkdirAll(filepath.Join(repo, ".git/hooks"), 0o755)
	os.WriteFile(filepath.Join(repo, ".git/hooks/pre-commit"),
		[]byte("#!/bin/sh\nexec trellis gate lint --project p1\n"), 0o755)
	for _, f := range doctorChecks(p) {
		if f.name == ".git/hooks/pre-commit" {
			if f.level != "fail" || !f.fixable || !strings.Contains(f.hint, "outdated") {
				t.Fatalf("old hook: %+v", f)
			}
		}
	}

	// User-owned findings are never fixable.
	for _, f := range doctorChecks(p) {
		if f.name == "gate config" && f.fixable {
			t.Fatal("gate config must be user-owned")
		}
		if f.name == "trellis on PATH" && f.fixable {
			t.Fatal("PATH must be user-owned")
		}
	}
}

// TestDoctorIntegration_IT_34 proves IT-34 (US-34): healthy repo all-ok, and
// --fix repairing exactly the trellis-owned artifacts.
func TestDoctorIntegration_IT_34(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	gitRun(t, repo, "branch", "main")
	if err := run([]string{"init", "--name", "demo", "--repo", repo}); err != nil {
		t.Fatal(err)
	}
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	projects, _ := st.ListProjects()
	p := projects[0]
	p.TestCmd, p.JUnitGlob, p.CoverageGlob = "true", "reports/*.xml", "reports/cov"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}

	// Healthy: all ok (trellis IS on PATH in dev env or not — tolerate warn
	// only for PATH by pinning it).
	bin := t.TempDir()
	if err := os.Symlink(os.Args[0], filepath.Join(bin, "trellis")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, f := range doctorChecks(p) {
		if f.level != "ok" {
			t.Errorf("healthy repo: %s = %s (%s)", f.name, f.level, f.hint)
		}
	}

	// Sabotage trellis-owned artifacts, then --fix repairs them.
	os.WriteFile(filepath.Join(repo, ".git/hooks/pre-commit"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	os.Remove(filepath.Join(repo, ".git/hooks/pre-push"))
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules/\n"), 0o644)
	if err := run([]string{"doctor", p.ID, "--fix"}); err != nil {
		t.Fatalf("doctor --fix: %v", err)
	}
	hook, _ := os.ReadFile(filepath.Join(repo, ".git/hooks/pre-commit"))
	if string(hook) != preCommitHook(p.ID) {
		t.Fatal("pre-commit not rewritten to current generation")
	}
	if _, err := os.Stat(filepath.Join(repo, ".git/hooks/pre-push")); err != nil {
		t.Fatal("pre-push not restored")
	}
	ignore, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if !strings.Contains(string(ignore), ".trellis-worktrees/") || !strings.Contains(string(ignore), "node_modules/") {
		t.Fatalf("ignore line not ensured or content lost: %q", ignore)
	}
}

// TestDoctorAcceptance_AT_38 proves AT-38 (US-34 "Setup doctor"): read-only
// default (file hashes unchanged), exit codes both ways, user-owned hints.
func TestDoctorAcceptance_AT_38(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Unwired project: doctor must fail with actionable hints, mutating nothing.
	if err := st.CreateProject(store.Project{ID: "p1", Name: "t", RepoPath: repo, BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	err = run([]string{"doctor", "p1"})
	if err == nil || !strings.Contains(err.Error(), "failure") {
		t.Fatalf("broken wiring must exit nonzero: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if string(before) != string(after) {
		t.Fatal("doctor without --fix mutated the repo")
	}
	if _, err := os.Stat(filepath.Join(repo, ".git/hooks/pre-commit")); err == nil {
		t.Fatal("doctor without --fix created a hook")
	}

	// After --fix plus user-side completion: healthy exit 0.
	if err := run([]string{"doctor", "p1", "--fix"}); err == nil {
		t.Fatal("gate config is user-owned; --fix alone cannot heal everything")
	}
	p, _ := st.GetProject("p1")
	p.TestCmd, p.JUnitGlob, p.CoverageGlob, p.ReleaseBranch = "true", "r/*.xml", "c/*", "main"
	st.UpdateProject(p)
	gitRun(t, repo, "add", ".gitignore")
	gitRun(t, repo, "commit", "--no-verify", "-m", "wiring") // the repaired hook now enforces the branch gate
	gitRun(t, repo, "branch", "main")
	bin := t.TempDir()
	if err := os.Symlink(os.Args[0], filepath.Join(bin, "trellis")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := run([]string{"doctor", "p1"}); err != nil {
		t.Fatalf("healthy doctor: %v", err)
	}
}
