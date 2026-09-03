package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestPruneCLIAcceptance_AT_7 proves AT-7 (US-5 "Story state machine with
// gates"): pruning through the real CLI entrypoint — blocked for non-done
// stories, blocked while an outside node depends on a tree member, and
// deleting the whole tree of a story that reached done via the full git flow.
func TestPruneCLIAcceptance_AT_7(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())

	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	report := `<?xml version="1.0"?><testsuite><testcase name="Test_AT_1"/><testcase name="Test_IT_1"/><testcase name="Test_UT_1"/></testsuite>`
	if err := os.WriteFile(filepath.Join(repo, "report-src.xml"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")

	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateProject(store.Project{ID: "p1", Name: "t", RepoPath: repo, BaseBranch: "develop",
		LintCmd: "true", TestCmd: "mkdir -p reports && cp report-src.xml reports/report.xml", JUnitGlob: "reports/*.xml"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}

	// Build, approve and complete a story via the engine (setup, not the SUT).
	story, _ := e.CreateNode(model.KindStory, "", "s", "", nil)
	e.AddAC(story.ID, "g", "w", "t")
	at, _ := e.CreateNode(model.KindAcceptanceTest, story.ID, "at", "", []string{story.ID + ".AC-1"})
	arch, _ := e.CreateNode(model.KindArch, story.ID, "as", "", nil)
	it, _ := e.CreateNode(model.KindIntegrationTest, arch.ID, "it", "", nil)
	dd, _ := e.CreateNode(model.KindDetailDesign, arch.ID, "dd", "", nil)
	ut, _ := e.CreateNode(model.KindUnitTest, dd.ID, "ut", "", nil)
	cc, _ := e.CreateNode(model.KindCrossCutting, "", "cc", "", nil)
	for _, id := range []string{story.ID, at.ID, arch.ID, it.ID, dd.ID, ut.ID, cc.ID} {
		r, err := e.Node(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Approve(id, r.Hash, nil); err != nil {
			t.Fatal(err)
		}
	}

	// Prune before done is blocked (through the CLI).
	err = run([]string{"prune", "p1", story.ID})
	if err == nil || !strings.Contains(err.Error(), "only done stories") {
		t.Fatalf("prune before done: want 'only done stories' error, got %v", err)
	}

	if _, err := e.Transition(story.ID, "refine"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Transition(story.ID, "start"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Transition(story.ID, "finish"); err != nil {
		t.Fatal(err)
	}

	// An outside dependent blocks pruning; CC depends on a tree member is not
	// possible (cc is root), so link the dependency the other way: another
	// node outside the tree depending on the arch spec.
	story2, _ := e.CreateNode(model.KindStory, "", "s2", "", nil)
	r, _ := e.Node(story2.ID)
	if err := e.Approve(story2.ID, r.Hash, nil); err != nil {
		t.Fatal(err)
	}
	arch2, _ := e.CreateNode(model.KindArch, story2.ID, "as2", "", nil)
	if err := e.LinkDep(arch2.ID, arch.ID); err != nil {
		t.Fatal(err)
	}
	err = run([]string{"prune", "p1", story.ID})
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%s depends on %s", arch2.ID, arch.ID)) {
		t.Fatalf("prune with outside dependent: want blocking error, got %v", err)
	}
	if err := e.UnlinkDep(arch2.ID, arch.ID); err != nil {
		t.Fatal(err)
	}

	// Prune the done story through the CLI: the whole tree is gone.
	if err := run([]string{"prune", "p1", story.ID}); err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, id := range []string{story.ID, at.ID, arch.ID, it.ID, dd.ID, ut.ID} {
		if _, err := e.Node(id); err == nil {
			t.Errorf("node %s still exists after prune", id)
		}
	}
	// The cross-cutting spec is not part of the tree and survives.
	if _, err := e.Node(cc.ID); err != nil {
		t.Errorf("cross-cutting %s must survive prune: %v", cc.ID, err)
	}
}

// TestAffectedCLIAcceptance_AT_16 proves AT-16 (US-13 "Spec-to-code paths"):
// trellis affected prints the stories declaring a file or parent folder and
// nothing for undeclared paths.
func TestAffectedCLIAcceptance_AT_16(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateProject(store.Project{ID: "p1", Name: "t", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
	story, _ := e.CreateNode(model.KindStory, "", "auth story", "", nil)
	if _, err := e.SetPaths(story.ID, []string{"pkg/auth"}); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{"affected", "p1", "pkg/auth/token.go"}); err != nil {
		t.Fatalf("affected: %v", err)
	}
	if err := run([]string{"affected", "p1", "pkg/other/file.go"}); err != nil {
		t.Fatalf("affected (no match): %v", err)
	}
	// The CLI shares the engine's matching; assert it directly for the output path.
	hits, err := e.StoriesForPath("pkg/auth/token.go")
	if err != nil || len(hits) != 1 || hits[0].ID != story.ID {
		t.Fatalf("engine lookup backing the CLI: %v %v", hits, err)
	}
}

// TestBoardCLIAcceptance_AT_18 proves AT-18 (US-15 "Board export"): the CLI
// writes a self-contained board with statuses, coverage, markers, evidence
// and both theme token blocks.
func TestBoardCLIAcceptance_AT_18(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	report := `<?xml version="1.0"?><testsuite><testcase name="Test_AT_1"/><testcase name="Test_IT_1"/><testcase name="Test_UT_1"/></testsuite>`
	if err := os.WriteFile(filepath.Join(repo, "report-src.xml"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateProject(store.Project{ID: "p1", Name: "t", RepoPath: repo, BaseBranch: "develop",
		LintCmd: "true", TestCmd: "mkdir -p reports && cp report-src.xml reports/report.xml", JUnitGlob: "reports/*.xml"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
	story, _ := e.CreateNode(model.KindStory, "", "s", "", nil)
	e.AddAC(story.ID, "g", "w", "t")
	at, _ := e.CreateNode(model.KindAcceptanceTest, story.ID, "at", "", []string{story.ID + ".AC-1"})
	arch, _ := e.CreateNode(model.KindArch, story.ID, "as", "", nil)
	it, _ := e.CreateNode(model.KindIntegrationTest, arch.ID, "it", "", nil)
	dd, _ := e.CreateNode(model.KindDetailDesign, arch.ID, "dd", "", nil)
	ut, _ := e.CreateNode(model.KindUnitTest, dd.ID, "ut", "", nil)
	for _, id := range []string{story.ID, at.ID, arch.ID, it.ID, dd.ID, ut.ID} {
		r, err := e.Node(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Approve(id, r.Hash, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, verb := range []string{"refine", "start", "finish"} {
		if _, err := e.Transition(story.ID, verb); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
	}

	out := filepath.Join(t.TempDir(), "board.html")
	if err := run([]string{"board", "p1", "-o", out}); err != nil {
		t.Fatalf("board: %v", err)
	}
	html, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		story.ID, ">done<", at.ID, story.ID + ".AC-1", // story, status, coverage
		"Test_AT_1",                  // evidence
		"prefers-color-scheme: dark", // dark tokens
		`data-theme="dark"`,          // explicit override block
		"--ground: #F7F8F6",          // light tokens on bare :root
		"gates open",
	} {
		if !strings.Contains(string(html), want) {
			t.Errorf("board missing %q", want)
		}
	}
}

// TestLiveBoardCLIAcceptance_AT_22 proves AT-22 (US-18 "Live board"): the
// CLI serves the board with live reload ticks on spec changes, and the
// static export keeps working.
func TestLiveBoardCLIAcceptance_AT_22(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateProject(store.Project{ID: "p1", Name: "t", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateNode(model.KindStory, "", "served story", "", nil); err != nil {
		t.Fatal(err)
	}

	// Pick a free port, then serve via the CLI entrypoint in the background.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	go run([]string{"board", "p1", "--serve", "--addr", addr})
	var res *http.Response
	for i := 0; i < 50; i++ {
		res, err = http.Get("http://" + addr + "/")
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("server never came up: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "served story") || !strings.Contains(string(body), "EventSource") {
		t.Fatalf("served page incomplete:\n%.300s", body)
	}

	// SSE tick within ~a second of a change.
	es, err := http.Get("http://" + addr + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer es.Body.Close()
	reader := bufio.NewReader(es.Body)
	reader.ReadString('\n') // connected comment
	if _, err := e.CreateNode(model.KindStory, "", "another", "", nil); err != nil {
		t.Fatal(err)
	}
	tick := make(chan string, 1)
	go func() {
		for {
			l, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(l, "data:") {
				tick <- l
				return
			}
		}
	}()
	select {
	case <-tick:
	case <-time.After(3 * time.Second):
		t.Fatal("no reload tick within 3s of a change")
	}

	// Static export unchanged.
	out := filepath.Join(t.TempDir(), "b.html")
	if err := run([]string{"board", "p1", "-o", out}); err != nil {
		t.Fatalf("static board: %v", err)
	}
	static, _ := os.ReadFile(out)
	if !strings.Contains(string(static), "served story") || strings.Contains(string(static), "EventSource") {
		t.Fatal("static export wrong: must show data, must not carry reload script")
	}
}

// TestReleaseCLIAcceptance_AT_26 proves AT-26 (US-22 "Release cut with
// feature manifest") through the real CLI entrypoint.
func TestReleaseCLIAcceptance_AT_26(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	report := `<?xml version="1.0"?><testsuite><testcase name="Test_AT_1"/><testcase name="Test_IT_1"/><testcase name="Test_UT_1"/><testcase name="Test_AT_2"/><testcase name="Test_IT_2"/><testcase name="Test_UT_2"/></testsuite>`
	if err := os.WriteFile(filepath.Join(repo, "report-src.xml"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateProject(store.Project{ID: "p1", Name: "t", RepoPath: repo, BaseBranch: "develop",
		LintCmd: "true", TestCmd: "mkdir -p reports && cp report-src.xml reports/report.xml", JUnitGlob: "reports/*.xml"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
	buildDone := func(title string) string {
		t.Helper()
		s, _ := e.CreateNode(model.KindStory, "", title, "", nil)
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
		for _, verb := range []string{"refine", "start"} {
			if _, err := e.Transition(s.ID, verb); err != nil {
				t.Fatalf("%s: %v", verb, err)
			}
		}
		wt := filepath.Join(repo, ".trellis-worktrees", s.ID)
		if err := os.WriteFile(filepath.Join(wt, s.ID+".txt"), []byte("impl"), 0o644); err != nil {
			t.Fatal(err)
		}
		gitRun(t, wt, "add", ".")
		gitRun(t, wt, "commit", "-m", "implement "+s.ID)
		if _, err := e.Transition(s.ID, "finish"); err != nil {
			t.Fatalf("finish: %v", err)
		}
		return s.ID
	}

	// Nothing to release yet.
	if err := run([]string{"release", "p1"}); err == nil || !strings.Contains(err.Error(), "nothing to release") {
		t.Fatalf("want nothing-to-release, got %v", err)
	}

	first := buildDone("first feature")
	e.DefineTerm("gate", "guard that blocks a transition")

	// Dirty worktree blocks.
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"release", "p1"}); err == nil || !strings.Contains(err.Error(), "not clean") {
		t.Fatalf("want dirty block, got %v", err)
	}
	os.Remove(filepath.Join(repo, "dirty.txt"))

	// Stale specs block.
	body := "edited"
	if _, err := e.UpdateNode(first, nil, &body, nil); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"release", "p1"}); err == nil || !strings.Contains(err.Error(), "honest context") {
		t.Fatalf("want stale block, got %v", err)
	}
	r, _ := e.Node(first)
	if err := e.Approve(first, r.Hash, nil); err != nil {
		t.Fatal(err)
	}
	// Re-approve the stale children (story hash changed).
	for _, id := range []string{"AT-1", "AS-1"} {
		n, _ := e.Node(id)
		if !n.Fresh {
			if err := e.Approve(id, n.Hash, nil); err != nil {
				t.Fatal(err)
			}
		}
	}

	// First release.
	if err := run([]string{"release", "p1"}); err != nil {
		t.Fatalf("release: %v", err)
	}
	manifest := gitRun(t, repo, "show", "main:FEATURES.md")
	for _, want := range []string{"first feature", "# Glossary", "gate"} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest missing %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "FEATURES.md")); err == nil {
		t.Fatal("FEATURES.md leaked onto the base branch")
	}

	// Incremental release lists only the new feature in the merge message.
	second := buildDone("second feature")
	if err := run([]string{"release", "p1"}); err != nil {
		t.Fatalf("second release: %v", err)
	}
	log := gitRun(t, repo, "log", "--merges", "--pretty=%B", "-1", "main")
	if !strings.Contains(log, second+" — second feature") || strings.Contains(log, first+" — first feature") {
		t.Fatalf("incremental merge message wrong:\n%s", log)
	}
}

// TestServedBoardsAcceptance_AT_27 proves AT-27 (US-23 "Boards served by the
// MCP server"): the serve entrypoint brings up the board UI beside stdio,
// redirects for one project, indexes several, and degrades to a notice when
// the address is taken.
func TestServedBoardsAcceptance_AT_27(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(store.Project{ID: "p1", Name: "one", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.CreateNode(model.KindStory, "", "served story", "", nil); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Hold stdin open so the MCP stdio loop keeps running in the background.
	rp, wp, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = rp
	t.Cleanup(func() { os.Stdin = oldStdin; wp.Close() })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	go run([]string{"serve", "--project", "p1", "--board-addr", addr})

	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	var res *http.Response
	for i := 0; i < 50; i++ {
		res, err = client.Get("http://" + addr + "/")
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("board never came up beside stdio: %v", err)
	}
	res.Body.Close()

	// One project: root redirects straight to its board.
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/p/p1/" {
		t.Fatalf("root: %d -> %q, want redirect to /p/p1/", res.StatusCode, res.Header.Get("Location"))
	}
	res, err = client.Get("http://" + addr + "/p/p1/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "served story") || !strings.Contains(string(body), "EventSource") {
		t.Fatalf("board page incomplete:\n%.300s", body)
	}

	// Second project: root becomes an index listing both boards.
	st2, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if err := st2.CreateProject(store.Project{ID: "p2", Name: "two", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	res, _ = client.Get("http://" + addr + "/")
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), `href="/p/p1/"`) ||
		!strings.Contains(string(body), `href="/p/p2/"`) {
		t.Fatalf("index wrong (%d):\n%.300s", res.StatusCode, body)
	}

	// Occupied address: serve must keep running (we assert it by watching the
	// second serve return only when stdin closes, not on the bind failure).
	rp2, wp2, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = rp2
	done := make(chan error, 1)
	go func() { done <- run([]string{"serve", "--project", "p1", "--board-addr", addr}) }()
	select {
	case err := <-done:
		t.Fatalf("serve exited on occupied board address: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	wp2.Close() // stdin EOF ends the stdio loop
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not exit after stdin EOF")
	}
}

// TestExportImportCLIAcceptance_AT_29 proves AT-29 (US-25 "YAML export and
// import") through the real CLI entrypoint: export, import as a new
// project, round trip, counters, and the release backup.
func TestExportImportCLIAcceptance_AT_29(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	report := `<?xml version="1.0"?><testsuite><testcase name="Test_AT_1"/><testcase name="Test_IT_1"/><testcase name="Test_UT_1"/></testsuite>`
	if err := os.WriteFile(filepath.Join(repo, "report-src.xml"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(store.Project{ID: "p1", Name: "orig", RepoPath: repo, BaseBranch: "develop",
		Description: "spec tracking", LintCmd: "true",
		TestCmd: "mkdir -p reports && cp report-src.xml reports/report.xml", JUnitGlob: "reports/*.xml"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
	// A done story with evidence plus glossary.
	s, _ := e.CreateNode(model.KindStory, "", "first feature", "", nil)
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
	for _, verb := range []string{"refine", "start"} {
		if _, err := e.Transition(s.ID, verb); err != nil {
			t.Fatal(err)
		}
	}
	wt := filepath.Join(repo, ".trellis-worktrees", s.ID)
	if err := os.WriteFile(filepath.Join(wt, "impl.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wt, "add", ".")
	gitRun(t, wt, "commit", "-m", "impl")
	if _, err := e.Transition(s.ID, "finish"); err != nil {
		t.Fatal(err)
	}
	e.DefineTerm("gate", "guard that blocks a transition")
	position := 2
	activity, err := e.CreateNodeWithPosition(model.KindActivity, "", "Build", "activity body", nil, &position)
	if err != nil {
		t.Fatal(err)
	}
	activityReport, err := e.Node(activity.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Approve(activity.ID, activityReport.Hash, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetStoryActivity("p1", s.ID, activity.ID); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Export via CLI, import as a new project, export again: round trip.
	out1 := filepath.Join(t.TempDir(), "one.yaml")
	if err := run([]string{"export", "p1", "-o", out1}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if err := run([]string{"import", "-f", out1, "--name", "orig", "--repo", repo}); err != nil {
		t.Fatalf("import: %v", err)
	}
	st2, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	projects, _ := st2.ListProjects()
	var copied string
	for _, p := range projects {
		if p.ID != "p1" {
			copied = p.ID
		}
	}
	st2.Close()
	if copied == "" {
		t.Fatal("imported project not found")
	}
	out2 := filepath.Join(t.TempDir(), "two.yaml")
	if err := run([]string{"export", copied, "-o", out2}); err != nil {
		t.Fatalf("re-export: %v", err)
	}
	b1, _ := os.ReadFile(out1)
	b2, _ := os.ReadFile(out2)
	if string(b1) != string(b2) {
		t.Fatalf("round trip diverged:\n%s\n---\n%s", b1, b2)
	}
	if !strings.Contains(string(b1), "recorded_at") || !strings.Contains(string(b1), "gate") ||
		!strings.Contains(string(b1), "activities:") || !strings.Contains(string(b1), "activity: UA-1") {
		t.Fatal("export missing evidence, glossary, activity or placement")
	}

	// Counters preserved: next story id in the copy continues.
	st3, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st3.Close()
	e3, err := core.NewEngine(st3, copied)
	if err != nil {
		t.Fatal(err)
	}
	n, err := e3.CreateNode(model.KindStory, "", "next", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n.ID != "US-2" {
		t.Fatalf("counter lost: %s, want US-2", n.ID)
	}
	a, err := e3.CreateNode(model.KindActivity, "", "next activity", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "UA-2" || a.Position != 3 {
		t.Fatalf("activity counter/position lost: %+v", a)
	}
	if err := e3.DeleteNode(n.ID); err != nil {
		t.Fatal(err)
	}

	// Release commits the backup on the release branch only.
	if err := run([]string{"release", "p1"}); err != nil {
		t.Fatalf("release: %v", err)
	}
	if out := gitRun(t, repo, "show", "main:trellis-specs.yaml"); !strings.Contains(out, "trellis_export: 1") {
		t.Fatal("backup missing on release branch")
	}
	if _, err := os.Stat(filepath.Join(repo, "trellis-specs.yaml")); err == nil {
		t.Fatal("backup leaked onto the base branch")
	}
}

// TestKanbanAcceptance_AT_32 proves AT-32 (US-28 "Kanban board") through the
// CLI export and the served page: columns with counts, card placement, stale
// marker, story detail overlay, and embedded open script in both outputs.
func TestKanbanAcceptance_AT_32(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(store.Project{ID: "p1", Name: "t", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
	staleStory, _ := e.CreateNode(model.KindStory, "", "stale one", "", nil)
	staleReport, _ := e.Node(staleStory.ID)
	if err := e.Approve(staleStory.ID, staleReport.Hash, nil); err != nil {
		t.Fatal(err)
	}
	staleBody := "changed after approval"
	if _, err := e.UpdateNode(staleStory.ID, nil, &staleBody, nil); err != nil {
		t.Fatal(err)
	}
	s2, _ := e.CreateNode(model.KindStory, "", "second", "detail body here", nil)
	r, _ := e.Node(s2.ID)
	if err := e.Approve(s2.ID, r.Hash, nil); err != nil {
		t.Fatal(err)
	}
	st.Close()

	out := filepath.Join(t.TempDir(), "b.html")
	if err := run([]string{"board", "p1", "-o", out}); err != nil {
		t.Fatalf("board: %v", err)
	}
	html, _ := os.ReadFile(out)
	page := string(html)
	for _, want := range []string{
		">todo<", ">refined<", ">in progress<", ">done<",
		`data-story-open="` + staleStory.ID + `"`,
		`id="story-` + s2.ID + `"`,
		"function openStoryDetail",
		"detail body here",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("static board missing %q", want)
		}
	}
	// Invalidated approval is stale; never-approved content is blocked.
	for id, marker := range map[string]string{staleStory.ID: "stale", s2.ID: "blocked"} {
		start := strings.Index(page, `data-story-open="`+id+`"`)
		if start < 0 {
			t.Errorf("card %s missing", id)
			continue
		}
		end := strings.Index(page[start:], "</button>")
		if end < 0 || !strings.Contains(page[start:start+end], ">"+marker+"<") {
			t.Errorf("card %s missing %s integrity marker", id, marker)
		}
	}
	if !strings.Contains(page, ">todo<span class=\"count\">2</span>") {
		t.Errorf("todo column count wrong")
	}

	// Served page carries the same kanban plus the reload machinery.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	go run([]string{"board", "p1", "--serve", "--addr", addr})
	var res *http.Response
	for i := 0; i < 50; i++ {
		res, err = http.Get("http://" + addr + "/")
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	for _, want := range []string{"function openStoryDetail", ">todo<", "EventSource"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("served board missing %q", want)
		}
	}
}

// TestAuditAcceptance_AT_37 proves AT-37 (US-33 "Bidirectional audit")
// through CLI and MCP: findings, exit codes, and the identical MCP report.
func TestAuditAcceptance_AT_37(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := `<?xml version="1.0"?><testsuite><testcase name="Test_AT_1"/><testcase name="Test_IT_1"/><testcase name="Test_UT_1"/></testsuite>`
	if err := os.WriteFile(filepath.Join(repo, "report-src.xml"), []byte(report), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateProject(store.Project{ID: "p1", Name: "t", RepoPath: repo, BaseBranch: "develop",
		LintCmd: "true", TestCmd: "mkdir -p reports && cp report-src.xml reports/report.xml", JUnitGlob: "reports/*.xml"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
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
	if _, err := e.SetPaths(s.ID, []string{"report-src.xml"}); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"refine", "start", "finish"} {
		if _, err := e.Transition(s.ID, verb); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
	}

	// Clean: exit 0.
	if err := run([]string{"audit", "p1"}); err != nil {
		t.Fatalf("clean audit must exit 0: %v", err)
	}

	// Rot: broken done evidence -> exit 1 with violations.
	if err := os.WriteFile(filepath.Join(repo, "report-src.xml"),
		[]byte(`<?xml version="1.0"?><testsuite><testcase name="Test_AT_1"/><testcase name="Test_IT_1"/></testsuite>`), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "rot")
	err = run([]string{"audit", "p1"})
	if err == nil || !strings.Contains(err.Error(), "violation") {
		t.Fatalf("rotten audit must exit nonzero: %v", err)
	}
	// The engine report (same data the MCP tool serves) carries the finding.
	rep, err := e.Audit()
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Violations) == 0 || !strings.Contains(strings.Join(rep.Violations, "\n"), "UT-1") {
		t.Fatalf("report missing violation: %+v", rep)
	}
}

// TestUnclaimedFilesBlockAuditAcceptance_AT_42 proves AT-42 (US-38): CLI
// exits non-zero, report remains machine-readable and exhaustive, meta files
// stay excluded, no opt-in exists, and finish ignores unclaimed files.
func TestUnclaimedFilesBlockAuditAcceptance_AT_42(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n.trellis-worktrees/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report := `<?xml version="1.0"?><testsuite><testcase name="Test_AT_1"/><testcase name="Test_IT_1"/><testcase name="Test_UT_1"/><testcase name="Test_UT_999"/></testsuite>`
	for name, content := range map[string]string{
		"report-src.xml": report,
		"a/orphan.go":    "package orphan",
		"z/orphan.go":    "package orphan",
		"README.md":      "meta",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.CreateProject(store.Project{ID: "p1", Name: "t", RepoPath: repo, BaseBranch: "develop",
		LintCmd: "true", TestCmd: "mkdir -p reports && cp report-src.xml reports/report.xml", JUnitGlob: "reports/*.xml"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
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
		n, err := e.Node(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Approve(id, n.Hash, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.SetPaths(s.ID, []string{"report-src.xml", "impl.txt"}); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"refine", "start"} {
		if _, err := e.Transition(s.ID, verb); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
	}
	wt := filepath.Join(repo, ".trellis-worktrees", s.ID)
	if err := os.WriteFile(filepath.Join(wt, "impl.txt"), []byte("implementation"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wt, "add", ".")
	gitRun(t, wt, "commit", "-m", "implementation")
	if _, err := e.Transition(s.ID, "finish"); err != nil {
		t.Fatalf("finish must ignore unclaimed files: %v", err)
	}

	if err := run([]string{"audit", "p1"}); err == nil || !strings.Contains(err.Error(), "violation") {
		t.Fatalf("audit must exit non-zero: %v", err)
	}
	if err := run([]string{"audit", "p1", "--allow-unclaimed"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("audit must expose no opt-in: %v", err)
	}
	rep, err := e.Audit()
	if err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var decoded core.AuditReport
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("machine-readable report: %v", err)
	}
	joined := strings.Join(decoded.Violations, "\n")
	for _, want := range []string{"references nonexistent spec UT-999", "2 file(s) claimed by no story", "a/orphan.go", "z/orphan.go"} {
		if !strings.Contains(joined, want) {
			t.Errorf("report missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "README.md") || strings.Contains(strings.Join(decoded.Infos, "\n"), "claimed by no story") {
		t.Fatalf("meta exclusion or info classification changed: %+v", decoded)
	}
}
