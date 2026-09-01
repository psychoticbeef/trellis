package main

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"trellis/internal/board"
	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

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
