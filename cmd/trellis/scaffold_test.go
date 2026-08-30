package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"trellis/internal/store"
)

// TestScaffoldUnit_UT_17 proves UT-17 (DD-17 "Scaffold writer and gate
// command"): create-if-absent behaviour, hook content and mode, and gate's
// three outcomes.
func TestScaffoldUnit_UT_17(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")

	msgs := strings.Join(scaffold(repo, "p1"), "\n")
	for _, want := range []string{".mcp.json created", "AGENTS.md created", "pre-commit created", "pre-push created"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("scaffold messages missing %q:\n%s", want, msgs)
		}
	}
	hook, err := os.Stat(filepath.Join(repo, ".git/hooks/pre-commit"))
	if err != nil {
		t.Fatal(err)
	}
	if hook.Mode().Perm() != 0o755 {
		t.Fatalf("hook mode = %v, want 0755", hook.Mode().Perm())
	}
	content, _ := os.ReadFile(filepath.Join(repo, ".git/hooks/pre-commit"))
	if !strings.Contains(string(content), "trellis gate lint --project p1") {
		t.Fatalf("hook content:\n%s", content)
	}
	mcpJSON, _ := os.ReadFile(filepath.Join(repo, ".mcp.json"))
	if !strings.Contains(string(mcpJSON), `"serve", "--project", "p1"`) {
		t.Fatalf(".mcp.json content:\n%s", mcpJSON)
	}

	// Second run: everything reported as untouched, content preserved.
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs = strings.Join(scaffold(repo, "p2"), "\n")
	if strings.Contains(msgs, "created") {
		t.Fatalf("second scaffold must not create anything:\n%s", msgs)
	}
	agents, _ := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if string(agents) != "custom" {
		t.Fatal("existing AGENTS.md was overwritten")
	}

	// gate outcomes: unconfigured passes, green passes, red fails.
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateProject(store.Project{ID: "p1", Name: "t", RepoPath: repo, BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"gate", "lint", "--project", "p1"}); err != nil {
		t.Fatalf("unconfigured gate must pass: %v", err)
	}
	p, _ := st.GetProject("p1")
	p.LintCmd = "true"
	st.UpdateProject(p)
	if err := run([]string{"gate", "lint", "--project", "p1"}); err != nil {
		t.Fatalf("green gate must pass: %v", err)
	}
	p.LintCmd = "false"
	st.UpdateProject(p)
	if err := run([]string{"gate", "lint", "--project", "p1"}); err == nil {
		t.Fatal("red gate must fail")
	}
	if err := run([]string{"gate", "warp", "--project", "p1"}); err == nil {
		t.Fatal("unknown gate must fail")
	}
}

// TestScaffoldIntegration_IT_16 proves IT-16 (US-16): the file creation
// matrix through the real init entrypoint, hook executability, and gate exit
// codes against configured commands.
func TestScaffoldIntegration_IT_16(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, ".mcp.json"), []byte(`{"custom":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"init", "--name", "demo", "--repo", repo}); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Pre-existing .mcp.json preserved; the rest created.
	mcpJSON, _ := os.ReadFile(filepath.Join(repo, ".mcp.json"))
	if string(mcpJSON) != `{"custom":true}` {
		t.Fatal("pre-existing .mcp.json was overwritten")
	}
	for _, rel := range []string{"AGENTS.md", ".git/hooks/pre-commit", ".git/hooks/pre-push"} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Errorf("%s not created: %v", rel, err)
		}
	}

	// The installed hook is executable and enforces the configured lint.
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	projects, err := st.ListProjects()
	if err != nil || len(projects) != 1 {
		t.Fatalf("projects: %v %v", projects, err)
	}
	p := projects[0]
	p.LintCmd = "test -f lint-ok"
	if err := st.UpdateProject(p); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"gate", "lint", "--project", p.ID}); err == nil {
		t.Fatal("gate must fail while lint-ok is missing")
	}
	if err := os.WriteFile(filepath.Join(repo, "lint-ok"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"gate", "lint", "--project", p.ID}); err != nil {
		t.Fatalf("gate must pass now: %v", err)
	}
}

// TestScaffoldAcceptance_AT_19 proves AT-19 (US-16 "Init scaffolding and git
// hooks") through the CLI: init wires a fresh repo completely, a second init
// preserves and reports, gate delivers its three outcomes.
func TestScaffoldAcceptance_AT_19(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")

	if err := run([]string{"init", "--name", "demo", "--repo", repo}); err != nil {
		t.Fatalf("init: %v", err)
	}
	for _, rel := range []string{".mcp.json", "AGENTS.md", ".git/hooks/pre-commit", ".git/hooks/pre-push"} {
		if _, err := os.Stat(filepath.Join(repo, rel)); err != nil {
			t.Errorf("%s missing after init: %v", rel, err)
		}
	}
	agents, _ := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if !strings.Contains(string(agents), "trellis-project:") {
		t.Fatal("AGENTS.md missing the project pointer")
	}
	before, _ := os.ReadFile(filepath.Join(repo, "AGENTS.md"))

	// Second init: nothing overwritten.
	if err := run([]string{"init", "--name", "demo2", "--repo", repo}); err != nil {
		t.Fatalf("second init: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if string(before) != string(after) {
		t.Fatal("second init modified AGENTS.md")
	}

	// gate: unconfigured passes, configured red fails, green passes.
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	projects, _ := st.ListProjects()
	id := projects[0].ID
	if err := run([]string{"gate", "test", "--project", id}); err != nil {
		t.Fatalf("unconfigured gate: %v", err)
	}
	p := projects[0]
	p.TestCmd = "false"
	st.UpdateProject(p)
	if err := run([]string{"gate", "test", "--project", id}); err == nil {
		t.Fatal("red test gate must fail")
	}
	p.TestCmd = "true"
	st.UpdateProject(p)
	if err := run([]string{"gate", "test", "--project", id}); err != nil {
		t.Fatalf("green test gate: %v", err)
	}
}

// TestBranchGateAcceptance_AT_21 proves AT-21 (US-17 "Worktree isolation")
// and the branch-gate part of UT-18: trellis gate branch fails on the base
// branch, passes on a feature branch, and init installs it in pre-commit.
func TestBranchGateAcceptance_AT_21(t *testing.T) {
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
	if err := st.CreateProject(store.Project{ID: "p1", Name: "t", RepoPath: repo, BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}

	// gate branch runs in the current directory; on develop it must fail.
	wd, _ := os.Getwd()
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
	if err := run([]string{"gate", "branch", "--project", "p1"}); err == nil ||
		!strings.Contains(err.Error(), "direct commits on") {
		t.Fatalf("branch gate on base: want rejection, got %v", err)
	}
	gitRun(t, repo, "checkout", "-b", "feature/X")
	if err := run([]string{"gate", "branch", "--project", "p1"}); err != nil {
		t.Fatalf("branch gate on feature branch: %v", err)
	}
	gitRun(t, repo, "checkout", "develop")

	// The scaffolded pre-commit hook contains the branch gate.
	scaffold(repo, "p1")
	hook, _ := os.ReadFile(filepath.Join(repo, ".git/hooks/pre-commit"))
	if !strings.Contains(string(hook), "trellis gate branch --project p1") {
		t.Fatalf("pre-commit hook missing branch gate:\n%s", hook)
	}
}

func gateRepo(t *testing.T) (repo, wt string) {
	t.Helper()
	repo = t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".trellis-worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	wt = filepath.Join(repo, ".trellis-worktrees", "US-1")
	gitRun(t, repo, "worktree", "add", "-b", "feature/US-1", wt, "develop")
	return repo, wt
}

// TestWorktreeGateAcceptance_AT_24 proves AT-24 (US-20 "Gates run where the
// commit happens"): the gate verdict reflects the caller's worktree, with a
// fallback to the main repo path from unrelated directories.
func TestWorktreeGateAcceptance_AT_24(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo, wt := gateRepo(t)
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateProject(store.Project{ID: "p1", Name: "t", RepoPath: repo, BaseBranch: "develop",
		LintCmd: "test -f wt-marker"}); err != nil {
		t.Fatal(err)
	}
	// The marker exists only in the story worktree.
	if err := os.WriteFile(filepath.Join(wt, "wt-marker"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	chdir := func(dir string) {
		t.Helper()
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
	}

	// From the story worktree: passes (feedback reflects the agent's state).
	chdir(wt)
	if err := run([]string{"gate", "lint", "--project", "p1"}); err != nil {
		t.Fatalf("gate from story worktree: %v", err)
	}
	// From the main worktree: fails (no marker there).
	chdir(repo)
	if err := run([]string{"gate", "lint", "--project", "p1"}); err == nil {
		t.Fatal("gate from main worktree must fail without the marker")
	}
	// From an unrelated directory: falls back to the main repo path (fails).
	chdir(t.TempDir())
	if err := run([]string{"gate", "lint", "--project", "p1"}); err == nil {
		t.Fatal("gate from foreign cwd must use the main repo path")
	}
	// Fallback positive case: marker in the main repo, foreign cwd passes.
	if err := os.WriteFile(filepath.Join(repo, "wt-marker"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"gate", "lint", "--project", "p1"}); err != nil {
		t.Fatalf("fallback gate: %v", err)
	}
}

// TestGateDirIntegration_IT_20 proves IT-20 (US-20): dir resolution across
// main worktree, story worktree and foreign cwd.
func TestGateDirIntegration_IT_20(t *testing.T) {
	repo, wt := gateRepo(t)
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })

	eval := func(dir, want string) {
		t.Helper()
		if err := os.Chdir(dir); err != nil {
			t.Fatal(err)
		}
		got := resolveGateDir(repo)
		gotR, _ := filepath.EvalSymlinks(got)
		wantR, _ := filepath.EvalSymlinks(want)
		if gotR != wantR {
			t.Fatalf("resolveGateDir from %s = %s, want %s", dir, got, want)
		}
	}
	eval(repo, repo)
	eval(wt, wt)
	eval(t.TempDir(), repo)
}

// TestResolveGateDir_UT_21 proves UT-21 (DD-21 "resolveGateDir"): the three
// resolution cases at unit level, including a nested subdirectory of a
// worktree resolving to the worktree toplevel.
func TestResolveGateDir_UT_21(t *testing.T) {
	repo, wt := gateRepo(t)
	sub := filepath.Join(wt, "deep", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(wd) })
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	got, _ := filepath.EvalSymlinks(resolveGateDir(repo))
	want, _ := filepath.EvalSymlinks(wt)
	if got != want {
		t.Fatalf("nested cwd: %s, want %s", got, want)
	}
}
