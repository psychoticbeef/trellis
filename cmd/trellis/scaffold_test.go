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
