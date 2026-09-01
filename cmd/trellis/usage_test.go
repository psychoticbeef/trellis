package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http/httptest"
	"strings"
	"testing"

	"trellis/internal/board"
	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

// TestCategorizedUsageFlagValidation_UT_41 proves UT-41: categorized flags
// support partial zero values and report every syntax problem without mutation.
func TestCategorizedUsageFlagValidation_UT_41(t *testing.T) {
	values, problems := parseUsageFlags([]string{"--main-output", "0", "--subagents-cache-read=7"})
	if len(problems) != 0 || !values.Categorized || values.MainCats.Output != 0 || values.SubagentCats.CacheRead != 7 {
		t.Fatalf("partial categorized flags: values=%+v problems=%v", values, problems)
	}
	_, problems = parseUsageFlags([]string{"--main", "1", "--main-input", "-1", "--main-input", "2", "extra"})
	joined := strings.Join(problems, "; ")
	for _, want := range []string{"specified more than once", "unexpected argument", "cannot be mixed", "must be a nonnegative integer"} {
		if !strings.Contains(joined, want) {
			t.Errorf("validation missing %q: %s", want, joined)
		}
	}
	_, problems = parseUsageFlags(nil)
	joined = strings.Join(problems, "; ")
	for _, want := range []string{"--main is required", "--subagents is required"} {
		if !strings.Contains(joined, want) {
			t.Errorf("empty flags missing %q: %s", want, joined)
		}
	}
}

// TestExhaustiveOverflowErrorAcceptance_AT_44 proves AT-44: real CLI overflow
// errors name every affected counter and rejected reports remain atomic.
func TestExhaustiveOverflowErrorAcceptance_AT_44(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(store.Project{ID: "p1", Name: "test", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e, _ := core.NewEngine(st, "p1")
	single, _ := e.CreateNode(model.KindStory, "", "single overflow", "", nil)
	multi, _ := e.CreateNode(model.KindStory, "", "multi overflow", "", nil)
	st.Close()

	max := fmt.Sprint(int64(math.MaxInt64))
	for _, args := range [][]string{
		{"usage", "add", "p1", single.ID, "--main", max, "--subagents", "0"},
		{"usage", "add", "p1", multi.ID, "--main-input", max, "--main-output", max, "--subagents-cache-write", max},
	} {
		if err := run(args); err != nil {
			t.Fatalf("exact MaxInt64 boundary %v: %v", args, err)
		}
	}

	st, err = openStore()
	if err != nil {
		t.Fatal(err)
	}
	singleBefore, _, err := st.GetStoryUsage("p1", single.ID)
	if err != nil {
		t.Fatal(err)
	}
	multiBefore, _, err := st.GetStoryUsage("p1", multi.ID)
	if err != nil {
		t.Fatal(err)
	}
	seqBefore, err := st.MaxEventSeq("p1")
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	singleErr := run([]string{"usage", "add", "p1", single.ID, "--main", "1", "--subagents", "7"})
	singleWant := "token usage overflow for story " + single.ID + ": tokens_main"
	if singleErr == nil || singleErr.Error() != singleWant {
		t.Fatalf("single overflow error = %v, want %q", singleErr, singleWant)
	}
	multiErr := run([]string{"usage", "add", "p1", multi.ID,
		"--main-input", "1", "--main-output", "2", "--main-cache-read", "9", "--subagents-cache-write", "3"})
	multiWant := "token usage overflow for story " + multi.ID + ": tokens_main_input, tokens_main_output, tokens_subagents_cache_write"
	if multiErr == nil || multiErr.Error() != multiWant {
		t.Fatalf("multi overflow error = %v, want %q", multiErr, multiWant)
	}

	st, err = openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	singleAfter, _, err := st.GetStoryUsage("p1", single.ID)
	if err != nil || singleAfter != singleBefore {
		t.Fatalf("single overflow changed counters: before=%+v after=%+v err=%v", singleBefore, singleAfter, err)
	}
	multiAfter, _, err := st.GetStoryUsage("p1", multi.ID)
	if err != nil || multiAfter != multiBefore {
		t.Fatalf("multi overflow changed counters: before=%+v after=%+v err=%v", multiBefore, multiAfter, err)
	}
	seqAfter, err := st.MaxEventSeq("p1")
	if err != nil || seqAfter != seqBefore {
		t.Fatalf("overflow changed event sequence: before=%d after=%d err=%v", seqBefore, seqAfter, err)
	}
}

// TestCategorizedUsageCLIAcceptance_AT_43 proves AT-43: real CLI keeps legacy
// totals separate, accumulates partial categories, rejects invalid modes, and
// renders identical compact overview and board usage.
func TestCategorizedUsageCLIAcceptance_AT_43(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(store.Project{ID: "p1", Name: "test", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e, _ := core.NewEngine(st, "p1")
	categorized, _ := e.CreateNode(model.KindStory, "", "categorized", "", nil)
	legacy, _ := e.CreateNode(model.KindStory, "", "legacy", "", nil)
	legacyCompat := map[string][2]string{}
	for i := 3; i <= 38; i++ {
		n, createErr := e.CreateNode(model.KindStory, "", fmt.Sprintf("legacy compatibility %d", i), "", nil)
		if createErr != nil {
			t.Fatal(createErr)
		}
		switch i {
		case 14, 15, 16, 17:
			legacyCompat[n.ID] = [2]string{fmt.Sprintf("%d000", i), fmt.Sprintf("%d00", i)}
		case 37:
			legacyCompat[n.ID] = [2]string{"105000", "5800000"}
		case 38:
			legacyCompat[n.ID] = [2]string{"135000", "5400000"}
		}
	}
	st.Close()

	valid := [][]string{
		{"usage", "add", "p1", categorized.ID, "--main-input", "1000", "--main-output", "200", "--main-cache-read", "3000", "--main-cache-write", "400", "--subagents-input", "500", "--subagents-output", "300", "--subagents-cache-read", "2000", "--subagents-cache-write", "100"},
		{"usage", "add", "p1", categorized.ID, "--main-output=100", "--subagents-cache-read=500"},
		{"usage", "add", "p1", categorized.ID, "--main", "1999", "--subagents", "999"},
		{"usage", "add", "p1", legacy.ID, "--main", "120000", "--subagents", "90000"},
	}
	for id, counts := range legacyCompat {
		valid = append(valid, []string{"usage", "add", "p1", id, "--main", counts[0], "--subagents", counts[1]})
	}
	for _, args := range valid {
		if err := run(args); err != nil {
			t.Fatalf("run(%v): %v", args, err)
		}
	}

	st, err = openStore()
	if err != nil {
		t.Fatal(err)
	}
	before, _, err := st.GetStoryUsage("p1", categorized.ID)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	invalid := [][]string{
		{"usage", "add", "p1", categorized.ID, "--main", "1", "--subagents", "2", "--main-output", "3"},
		{"usage", "add", "p1", categorized.ID, "--main-input", "-1"},
		{"usage", "add", "p1", categorized.ID, "--main-output", "wat"},
		{"usage", "add", "p1", categorized.ID, "--main-cache-read"},
		{"usage", "add", "p1", categorized.ID},
		{"usage", "add", "p1", "US-999", "--main-output", "wat", "--subagents-cache-read", "-1"},
	}
	for _, args := range invalid {
		if err := run(args); err == nil {
			t.Fatalf("run(%v) succeeded", args)
		}
	}
	mixed := run(invalid[0])
	if mixed == nil || !strings.Contains(mixed.Error(), "cannot be mixed") {
		t.Fatalf("mixed-mode error = %v", mixed)
	}
	exhaustive := run(invalid[len(invalid)-1])
	for _, want := range []string{"US-999", "does not exist", "--main-output", "--subagents-cache-read"} {
		if exhaustive == nil || !strings.Contains(exhaustive.Error(), want) {
			t.Fatalf("exhaustive rejection missing %q: %v", want, exhaustive)
		}
	}

	st, err = openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	after, _, err := st.GetStoryUsage("p1", categorized.ID)
	if err != nil || after != before {
		t.Fatalf("invalid reports changed usage: before=%+v after=%+v err=%v", before, after, err)
	}
	e, _ = core.NewEngine(st, "p1")
	o, err := e.Overview()
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(o)
	text := string(blob)
	for _, summary := range o.Stories {
		if _, ok := legacyCompat[summary.ID]; ok && (strings.Contains(summary.Usage, "·") || summary.TokensMainInput != nil) {
			t.Errorf("legacy compatibility story reinterpreted: %+v", summary)
		}
	}
	for _, want := range []string{
		`"tokens_main":1999`, `"tokens_subagents":999`, `"tokens_main_input":1000`,
		`"tokens_main_output":300`, `"tokens_main_cache_read":3000`, `"tokens_main_cache_write":400`,
		`"tokens_subagents_input":500`, `"tokens_subagents_output":300`,
		`"tokens_subagents_cache_read":2500`, `"tokens_subagents_cache_write":100`,
		`"usage":"11k (4k sub) · out 600 · cache 5k/500 r/w"`,
		`"usage":"210k (90k sub)"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("overview missing %s: %s", want, text)
		}
	}
	html, err := board.Render(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"11k (4k sub) · out 600 · cache 5k/500 r/w", "210k (90k sub)"} {
		if strings.Count(html, want) != 2 {
			t.Errorf("board usage %q count=%d", want, strings.Count(html, want))
		}
	}
	lower := strings.ToLower(html)
	for _, forbidden := range []string{"cost", "currency", "euro", "€"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("board contains forbidden conversion text %q", forbidden)
		}
	}
}

// TestTokenUsageCLIAcceptance_AT_41 proves AT-41: real CLI reports accumulate,
// invalid reports have no effect, and overview plus board expose compact usage.
func TestTokenUsageCLIAcceptance_AT_41(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(store.Project{ID: "p1", Name: "test", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e, _ := core.NewEngine(st, "p1")
	used, _ := e.CreateNode(model.KindStory, "", "used", "", nil)
	raw, _ := e.CreateNode(model.KindStory, "", "raw", "", nil)
	boundary999, _ := e.CreateNode(model.KindStory, "", "boundary 999", "", nil)
	boundary1000, _ := e.CreateNode(model.KindStory, "", "boundary 1000", "", nil)
	boundary1999, _ := e.CreateNode(model.KindStory, "", "boundary 1999", "", nil)
	silent, _ := e.CreateNode(model.KindStory, "", "silent", "", nil)
	st.Close()

	for _, args := range [][]string{
		{"usage", "add", "p1", used.ID, "--main", "100000", "--subagents", "60000"},
		{"usage", "add", "p1", used.ID, "--main", "20000", "--subagents", "30000"},
		{"usage", "add", "p1", used.ID, "--main", "0", "--subagents", "0"},
		{"usage", "add", "p1", raw.ID, "--main", "500", "--subagents", "100"},
		{"usage", "add", "p1", boundary999.ID, "--main", "999", "--subagents", "0"},
		{"usage", "add", "p1", boundary1000.ID, "--main", "1000", "--subagents", "0"},
		{"usage", "add", "p1", boundary1999.ID, "--main", "1999", "--subagents", "0"},
	} {
		if err := run(args); err != nil {
			t.Fatalf("run(%v): %v", args, err)
		}
	}
	for _, args := range [][]string{
		{"usage", "add", "p1", "US-999", "--main", "1", "--subagents", "1"},
		{"usage", "add", "p1", used.ID, "--main", "-1", "--subagents", "1"},
		{"usage", "add", "p1", used.ID, "--main", "wat", "--subagents", "1"},
		{"usage", "add", "p1", used.ID, "--main", "1"},
	} {
		if err := run(args); err == nil {
			t.Fatalf("run(%v) succeeded", args)
		}
	}
	bothErr := run([]string{"usage", "add", "p1", used.ID, "--main", "bad", "--subagents", "-2"})
	if bothErr == nil || !strings.Contains(bothErr.Error(), used.ID) || !strings.Contains(bothErr.Error(), "--main") || !strings.Contains(bothErr.Error(), "--subagents") {
		t.Fatalf("aggregate validation error = %v", bothErr)
	}
	exhaustiveErr := run([]string{"usage", "add", "p1", used.ID, "--bogus", "x"})
	for _, want := range []string{used.ID, "unknown flag --bogus", "--main is required", "--subagents is required"} {
		if exhaustiveErr == nil || !strings.Contains(exhaustiveErr.Error(), want) {
			t.Fatalf("exhaustive flag error missing %q: %v", want, exhaustiveErr)
		}
	}

	st, err = openStore()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e, _ = core.NewEngine(st, "p1")
	o, err := e.Overview()
	if err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(o)
	text := string(blob)
	for _, want := range []string{`"usage":"210k (90k sub)"`, `"usage":"600 (100 sub)"`, `"usage":"999 (0 sub)"`, `"usage":"1k (0 sub)"`, `"tokens_main":120000`, `"tokens_subagents":90000`} {
		if !strings.Contains(text, want) {
			t.Errorf("overview missing %s: %s", want, text)
		}
	}
	for _, s := range o.Stories {
		if s.ID == silent.ID && (s.Usage != "" || s.TokensMain != nil || s.TokensSubagents != nil) {
			t.Fatalf("silent story has usage: %+v", s)
		}
	}
	html, err := board.Render(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"210k (90k sub)", "600 (100 sub)", "999 (0 sub)", "1k (0 sub)"} {
		if !strings.Contains(html, want) {
			t.Errorf("board missing %q", want)
		}
	}
	server := httptest.NewServer(board.Handler(e, st))
	defer server.Close()
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	served, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(served), "210k (90k sub)") || !strings.Contains(string(served), "new EventSource") {
		t.Fatalf("served board missing usage or reload script: %.500s", served)
	}
	if strings.Contains(strings.ToLower(html), "euro") || strings.Contains(html, "€") {
		t.Fatal("cost conversion present")
	}
}
