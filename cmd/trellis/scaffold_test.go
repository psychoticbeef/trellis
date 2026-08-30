package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

// TestScaffoldUnit_UT_17 proves UT-17 (DD-17 "Scaffold writer and gate
// command"): create-if-absent behaviour, hook content and mode, and gate's
// three outcomes.
func TestScaffoldUnit_UT_17(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")

	scaffMsgs, _ := scaffold(repo, "p1")
	msgs := strings.Join(scaffMsgs, "\n")
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
	scaffMsgs2, _ := scaffold(repo, "p2")
	msgs = strings.Join(scaffMsgs2, "\n")
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

// TestBootstrapUnit_UT_30 proves UT-30 (DD-31 "Scaffold commit mechanics"):
// ensureIgnoreLine matrix, created-files reporting excluding hooks, warning
// construction off-PATH.
func TestBootstrapUnit_UT_30(t *testing.T) {
	repo := t.TempDir()

	// Create.
	changed, msg := ensureIgnoreLine(repo)
	if !changed || !strings.Contains(msg, "created") {
		t.Fatalf("create: %v %q", changed, msg)
	}
	// No-op when present.
	changed, msg = ensureIgnoreLine(repo)
	if changed || !strings.Contains(msg, "already ignores") {
		t.Fatalf("no-op: %v %q", changed, msg)
	}
	// Append preserving content (also without trailing newline).
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules/"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, msg = ensureIgnoreLine(repo)
	if !changed || !strings.Contains(msg, "amended") {
		t.Fatalf("append: %v %q", changed, msg)
	}
	data, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if string(data) != "node_modules/\n.trellis-worktrees/\n" {
		t.Fatalf("append content: %q", data)
	}

	// Created files exclude hooks; repo files included.
	repo2 := t.TempDir()
	gitRun(t, repo2, "init", "-b", "develop")
	_, created := scaffold(repo2, "p1")
	joined := strings.Join(created, ",")
	for _, want := range []string{".gitignore", ".mcp.json", "AGENTS.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("created files missing %s: %v", want, created)
		}
	}
	if strings.Contains(joined, ".git/") {
		t.Fatalf("hooks must not be in the commit list: %v", created)
	}

	// Off-PATH warning.
	t.Setenv("PATH", t.TempDir())
	msgs, _ := scaffold(t.TempDir(), "p1")
	if !strings.Contains(strings.Join(msgs, "\n"), "not on PATH") {
		t.Fatalf("missing PATH warning: %v", msgs)
	}
}

// TestBootstrapIntegration_IT_29 proves IT-29 (US-29): wiring commit
// contents, gitignore matrix through init, no-commit cases.
func TestBootstrapIntegration_IT_29(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	// Unrelated dirt must not be swept into the wiring commit.
	if err := os.WriteFile(filepath.Join(repo, "wip.txt"), []byte("w"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"init", "--name", "demo", "--repo", repo}); err != nil {
		t.Fatalf("init: %v", err)
	}
	subject := gitRun(t, repo, "log", "-1", "--pretty=%s")
	if subject != "trellis init: repo wiring" {
		t.Fatalf("wiring commit missing, HEAD is %q", subject)
	}
	files := gitRun(t, repo, "show", "--name-only", "--pretty=format:", "HEAD")
	for _, want := range []string{".gitignore", ".mcp.json", "AGENTS.md"} {
		if !strings.Contains(files, want) {
			t.Errorf("wiring commit missing %s:\n%s", want, files)
		}
	}
	if strings.Contains(files, "wip.txt") {
		t.Fatal("unrelated dirt swept into the wiring commit")
	}
	ignore, _ := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if !strings.Contains(string(ignore), "node_modules/") || !strings.Contains(string(ignore), ".trellis-worktrees/") {
		t.Fatalf("gitignore content: %q", ignore)
	}

	// Second init: no new commit.
	head := gitRun(t, repo, "rev-parse", "HEAD")
	if err := run([]string{"init", "--name", "demo2", "--repo", repo}); err != nil {
		t.Fatalf("second init: %v", err)
	}
	if got := gitRun(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatal("second init created a commit")
	}

	// No git repo: init still succeeds, nothing committed.
	bare := t.TempDir()
	if err := run([]string{"init", "--name", "demo3", "--repo", bare}); err != nil {
		t.Fatalf("init without git: %v", err)
	}
}

// TestBootstrapAcceptance_AT_33 proves AT-33 (US-29 "Init wires and commits
// its own scaffold"): one-step bootstrap leaving a clean worktree and an
// immediately startable project.
func TestBootstrapAcceptance_AT_33(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, "report-src.xml"),
		[]byte(`<?xml version="1.0"?><testsuite><testcase name="Test_AT_1"/><testcase name="Test_IT_1"/><testcase name="Test_UT_1"/></testsuite>`), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")

	if err := run([]string{"init", "--name", "demo", "--repo", repo}); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Clean worktree right after init — the wiring commit covered everything.
	if dirt := gitRun(t, repo, "status", "--porcelain"); dirt != "" {
		t.Fatalf("worktree dirty after init:\n%s", dirt)
	}

	// start works immediately: ignore entry is in place, no manual editing.
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	projects, _ := st.ListProjects()
	if len(projects) != 1 {
		t.Fatalf("projects: %v", projects)
	}
	e, err := core.NewEngine(st, projects[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := e.CreateNode(model.KindStory, "", "s", "", nil)
	e.AddAC(s.ID, "g", "w", "t")
	at, _ := e.CreateNode(model.KindAcceptanceTest, s.ID, "at", "", []string{s.ID + ".AC-1"})
	arch, _ := e.CreateNode(model.KindArch, s.ID, "as", "", nil)
	it, _ := e.CreateNode(model.KindIntegrationTest, arch.ID, "it", "", nil)
	dd, _ := e.CreateNode(model.KindDetailDesign, arch.ID, "dd", "", nil)
	ut, _ := e.CreateNode(model.KindUnitTest, dd.ID, "ut", "", nil)
	for _, id := range []string{s.ID, at.ID, arch.ID, it.ID, dd.ID, ut.ID} {
		r, err := e.Node(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Approve(id, r.Hash, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Transition(s.ID, "refine"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Transition(s.ID, "start"); err != nil {
		t.Fatalf("start immediately after init: %v", err)
	}
}
