package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

func findingHints(findings []finding) []string {
	hints := make([]string, 0, len(findings))
	for _, item := range findings {
		hints = append(hints, item.hint)
	}
	return hints
}

// TestStoryMapDoctorDerivation_UT_62 proves UT-62: walking skeleton gaps,
// unmapped stories, threshold guidance, and informational classification are
// derived deterministically without a fixer.
func TestStoryMapDoctorDerivation_UT_62(t *testing.T) {
	for _, count := range []int{0, 9, 10, 11} {
		stories := make([]model.Node, count)
		findings := storyMapDoctorFindings(nil, stories)
		if count < 10 && len(findings) != 0 {
			t.Errorf("%d no-map stories produced findings: %v", count, findingHints(findings))
		}
		if count >= 10 {
			if len(findings) != 1 || !strings.Contains(findings[0].hint, fmt.Sprintf("%d stories", count)) || !strings.Contains(findings[0].hint, "consider creating a story map") {
				t.Errorf("%d no-map stories findings=%v", count, findingHints(findings))
			}
		}
	}

	activities := []model.Node{
		{ID: "UA-1", Kind: model.KindActivity, Title: "Discover", Position: 1},
		{ID: "UA-2", Kind: model.KindActivity, Title: "Build", Position: 2},
	}
	completeStories := []model.Node{
		{ID: "US-1", Kind: model.KindStory, ActivityID: "UA-1", Rank: 1, Slice: 1},
		{ID: "US-2", Kind: model.KindStory, ActivityID: "UA-2", Rank: 1, Slice: 1},
	}
	if findings := storyMapDoctorFindings(activities, completeStories); len(findings) != 0 {
		t.Fatalf("complete walking skeleton produced findings: %v", findingHints(findings))
	}

	gapFindings := storyMapDoctorFindings(activities, nil)
	if got := strings.Join(findingHints(gapFindings), "\n"); len(gapFindings) != 2 || !strings.Contains(got, "UA-1") || !strings.Contains(got, "UA-2") {
		t.Fatalf("multiple walking skeleton gaps not exhaustive: %v", findingHints(gapFindings))
	}

	incompleteStories := []model.Node{
		{ID: "US-1", Kind: model.KindStory, ActivityID: "UA-1", Rank: 1, Slice: 1},
		{ID: "US-10", Kind: model.KindStory},
		{ID: "US-2", Kind: model.KindStory},
	}
	want := []string{
		`walking skeleton gap: activity UA-2 "Build" has no story in slice 1`,
		"unmapped story: US-2",
		"unmapped story: US-10",
		"placement gate activates when no unmapped stories remain",
	}
	findings := storyMapDoctorFindings(activities, incompleteStories)
	if got := fmt.Sprint(findingHints(findings)); got != fmt.Sprint(want) {
		t.Fatalf("incomplete findings=%s want=%s", got, fmt.Sprint(want))
	}
	for _, item := range findings {
		if item.level != "info" || item.fixable || item.fix != nil {
			t.Errorf("map finding not informational/read-only: %+v", item)
		}
	}
}

func captureDoctorStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "doctor-output-*")
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = old }()
	runErr := fn()
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data), runErr
}

// TestDoctorMapOutputIsolation_UT_63 proves UT-63: map information uses INFO,
// never changes failure count, and invokes no fixer under --fix.
func TestDoctorMapOutputIsolation_UT_63(t *testing.T) {
	mapFixCalled := false
	findings := []finding{
		{name: "existing", level: "fail", hint: "existing failure"},
		{name: "story map", level: "info", hint: "walking skeleton gap", fixable: false, fix: func() { mapFixCalled = true }},
	}
	output, err := captureDoctorStdout(t, func() error {
		fixed, fails := printDoctorFindings(findings, true)
		if fixed != 0 || fails != 1 {
			t.Fatalf("fixed=%d fails=%d", fixed, fails)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if mapFixCalled || !strings.Contains(output, "story map                INFO") || !strings.Contains(output, "existing                 FAIL") {
		t.Fatalf("doctor isolation failed; fix=%v output=%q", mapFixCalled, output)
	}
}

type healthyDoctorFixture struct {
	project store.Project
	store   *store.Store
	engine  *core.Engine
}

func newHealthyDoctorFixture(t *testing.T, projectID string) healthyDoctorFixture {
	t.Helper()
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	repo := t.TempDir()
	gitRun(t, repo, "init", "-b", "develop")
	if err := os.WriteFile(filepath.Join(repo, "seed.txt"), []byte("seed"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "initial")
	gitRun(t, repo, "branch", "main")
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	project := store.Project{
		ID: projectID, Name: projectID, RepoPath: repo, BaseBranch: "develop", ReleaseBranch: "main",
		TestCmd: "true", JUnitGlob: "reports/*.xml", CoverageGlob: "reports/coverage.out",
	}
	if err := st.CreateProject(project); err != nil {
		t.Fatal(err)
	}
	scaffold(repo, projectID)
	bin := t.TempDir()
	if err := os.Symlink(os.Args[0], filepath.Join(bin, "trellis")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e, err := core.NewEngine(st, projectID)
	if err != nil {
		t.Fatal(err)
	}
	return healthyDoctorFixture{project: project, store: st, engine: e}
}

func createDoctorStory(t *testing.T, e *core.Engine, title string) model.Node {
	t.Helper()
	story, err := e.CreateNode(model.KindStory, "", title, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	return story
}

func storyMapSnapshot(t *testing.T, e *core.Engine) string {
	t.Helper()
	overview, err := e.Overview()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct {
		Activities []core.ActivitySummary `json:"activities"`
		StoryMap   *core.StoryMapOverview `json:"story_map"`
	}{overview.Activities, overview.StoryMap})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestStoryMapDoctorIntegration_IT_53 drives real doctor CLI against complete,
// incomplete, threshold, --fix, and existing-failure project states.
func TestStoryMapDoctorIntegration_IT_53(t *testing.T) {
	t.Run("incomplete complete fix and failure", func(t *testing.T) {
		fixture := newHealthyDoctorFixture(t, "map-project")
		mapped := createDoctorStory(t, fixture.engine, "mapped")
		unmappedA := createDoctorStory(t, fixture.engine, "unmapped a")
		unmappedB := createDoctorStory(t, fixture.engine, "unmapped b")
		positionOne, positionTwo, positionThree := 1, 2, 3
		discover, err := fixture.engine.CreateNodeWithPosition(model.KindActivity, "", "Discover", "", nil, &positionOne)
		if err != nil {
			t.Fatal(err)
		}
		build, err := fixture.engine.CreateNodeWithPosition(model.KindActivity, "", "Build", "", nil, &positionTwo)
		if err != nil {
			t.Fatal(err)
		}
		ship, err := fixture.engine.CreateNodeWithPosition(model.KindActivity, "", "Ship", "", nil, &positionThree)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.SetStoryPlacement(fixture.project.ID, mapped.ID, ship.ID, 1, 1); err != nil {
			t.Fatal(err)
		}

		before := storyMapSnapshot(t, fixture.engine)
		output, runErr := captureDoctorStdout(t, func() error { return run([]string{"doctor", fixture.project.ID}) })
		if runErr != nil {
			t.Fatalf("informational map diagnostics changed healthy exit: %v", runErr)
		}
		ordered := []string{"activity " + discover.ID, "activity " + build.ID, "unmapped story: " + unmappedA.ID, "unmapped story: " + unmappedB.ID, "placement gate activates when no unmapped stories remain"}
		cursor := -1
		for _, want := range ordered {
			next := strings.Index(output, want)
			if next <= cursor {
				t.Fatalf("doctor map information missing or unordered %q: %s", want, output)
			}
			cursor = next
		}
		fixOutput, fixErr := captureDoctorStdout(t, func() error { return run([]string{"doctor", fixture.project.ID, "--fix"}) })
		if fixErr != nil || fixOutput != output {
			t.Fatalf("--fix changed map information: err=%v\nnormal=%s\nfix=%s", fixErr, output, fixOutput)
		}
		if after := storyMapSnapshot(t, fixture.engine); after != before {
			t.Fatalf("--fix mutated activities or placements:\nbefore=%s\nafter=%s", before, after)
		}

		broken := fixture.project
		broken.TestCmd = ""
		if err := fixture.store.UpdateProject(broken); err != nil {
			t.Fatal(err)
		}
		failedOutput, failedErr := captureDoctorStdout(t, func() error { return run([]string{"doctor", fixture.project.ID}) })
		if failedErr == nil || !strings.Contains(failedOutput, "placement gate activates when no unmapped stories remain") {
			t.Fatalf("existing failure exit changed by map information: err=%v output=%s", failedErr, failedOutput)
		}
		if err := fixture.store.UpdateProject(fixture.project); err != nil {
			t.Fatal(err)
		}

		if err := fixture.store.SetStoryPlacement(fixture.project.ID, unmappedA.ID, discover.ID, 1, 1); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.SetStoryPlacement(fixture.project.ID, unmappedB.ID, build.ID, 1, 1); err != nil {
			t.Fatal(err)
		}
		completeOutput, completeErr := captureDoctorStdout(t, func() error { return run([]string{"doctor", fixture.project.ID}) })
		if completeErr != nil || strings.Contains(completeOutput, "story map") {
			t.Fatalf("complete map emitted diagnostics or failed: err=%v output=%s", completeErr, completeOutput)
		}
	})

	t.Run("no map threshold", func(t *testing.T) {
		fixture := newHealthyDoctorFixture(t, "threshold-project")
		for i := 1; i <= 9; i++ {
			createDoctorStory(t, fixture.engine, fmt.Sprintf("story %d", i))
		}
		nineOutput, nineErr := captureDoctorStdout(t, func() error { return run([]string{"doctor", fixture.project.ID}) })
		if nineErr != nil || strings.Contains(nineOutput, "story map") {
			t.Fatalf("nine no-map stories emitted map output or failed: err=%v output=%s", nineErr, nineOutput)
		}
		createDoctorStory(t, fixture.engine, "story 10")
		tenOutput, tenErr := captureDoctorStdout(t, func() error { return run([]string{"doctor", fixture.project.ID}) })
		if tenErr != nil || strings.Count(tenOutput, "story map") != 2 || strings.Count(tenOutput, "consider creating a story map") != 1 {
			t.Fatalf("ten-story suggestion wrong: err=%v output=%s", tenErr, tenOutput)
		}
	})
}

// TestWalkingSkeletonDoctorAcceptance_AT_61 proves AT-61 through real doctor
// runs, including exhaustive gaps/unmapped stories, threshold behavior, zero
// exit for information, and --fix mutation isolation.
func TestWalkingSkeletonDoctorAcceptance_AT_61(t *testing.T) {
	fixture := newHealthyDoctorFixture(t, "acceptance-project")
	unmappedA := createDoctorStory(t, fixture.engine, "unmapped a")
	unmappedB := createDoctorStory(t, fixture.engine, "unmapped b")
	positionOne, positionTwo := 1, 2
	first, err := fixture.engine.CreateNodeWithPosition(model.KindActivity, "", "First", "", nil, &positionOne)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fixture.engine.CreateNodeWithPosition(model.KindActivity, "", "Second", "", nil, &positionTwo)
	if err != nil {
		t.Fatal(err)
	}
	before := storyMapSnapshot(t, fixture.engine)
	output, runErr := captureDoctorStdout(t, func() error { return run([]string{"doctor", fixture.project.ID, "--fix"}) })
	if runErr != nil {
		t.Fatalf("map information must exit zero: %v", runErr)
	}
	for _, want := range []string{first.ID, second.ID, unmappedA.ID, unmappedB.ID, "walking skeleton gap", "placement gate activates when no unmapped stories remain"} {
		if !strings.Contains(output, want) {
			t.Errorf("acceptance output missing %q: %s", want, output)
		}
	}
	if after := storyMapSnapshot(t, fixture.engine); after != before {
		t.Fatalf("doctor --fix changed story map:\nbefore=%s\nafter=%s", before, after)
	}

	threshold := newHealthyDoctorFixture(t, "acceptance-threshold")
	for i := 1; i <= 9; i++ {
		createDoctorStory(t, threshold.engine, fmt.Sprintf("story %d", i))
	}
	nine, err := captureDoctorStdout(t, func() error { return run([]string{"doctor", threshold.project.ID}) })
	if err != nil || strings.Contains(nine, "story map") {
		t.Fatalf("acceptance nine-story output: err=%v output=%s", err, nine)
	}
	createDoctorStory(t, threshold.engine, "story 10")
	ten, err := captureDoctorStdout(t, func() error { return run([]string{"doctor", threshold.project.ID}) })
	if err != nil || strings.Count(ten, "consider creating a story map") != 1 {
		t.Fatalf("acceptance ten-story output: err=%v output=%s", err, ten)
	}
}
