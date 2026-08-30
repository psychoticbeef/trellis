package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
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
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("reports/\n"), 0o644); err != nil {
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
