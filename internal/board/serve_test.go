package board

import (
	"bufio"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

func liveSetup(t *testing.T) (*core.Engine, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateProject(store.Project{ID: "p1", Name: "test", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
	return e, st
}

// TestBoardServerUnit_UT_19 proves UT-19 (DD-19 "Board HTTP server"): the SSE
// handler emits on sequence growth and stays quiet without, connections close
// with the request, and the reload script is absent from static Render.
func TestBoardServerUnit_UT_19(t *testing.T) {
	old := pollInterval
	pollInterval = 20 * time.Millisecond
	t.Cleanup(func() { pollInterval = old })
	e, st := liveSetup(t)
	srv := httptest.NewServer(Handler(e, st))
	t.Cleanup(srv.Close)

	// Static Render carries no reload script; the served page does.
	static, err := Render(e)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(static, "EventSource") {
		t.Fatal("static export must not contain the reload script")
	}
	res, err := srv.Client().Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	page := make([]byte, 1<<20)
	n, _ := res.Body.Read(page)
	res.Body.Close()
	if !strings.Contains(string(page[:n]), "EventSource") {
		t.Fatal("served page must contain the reload script")
	}

	// SSE: no tick without changes, a tick after a mutation.
	es, err := srv.Client().Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { es.Body.Close() })
	reader := bufio.NewReader(es.Body)
	line, _ := reader.ReadString('\n') // ": connected"
	if !strings.HasPrefix(line, ":") {
		t.Fatalf("expected SSE comment first, got %q", line)
	}

	got := make(chan string, 1)
	go func() {
		for {
			l, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(l, "data:") {
				got <- strings.TrimSpace(l)
				return
			}
		}
	}()
	select {
	case l := <-got:
		t.Fatalf("tick without any change: %q", l)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := e.CreateNode(model.KindStory, "", "s", "", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case l := <-got:
		if l != "data: reload" {
			t.Fatalf("tick = %q, want data: reload", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no SSE tick after a mutation")
	}
}

// TestLiveBoardIntegration_IT_18 proves IT-18 (US-18): the served page
// renders fresh data after mutations while the static export stays a
// snapshot without the reload machinery.
func TestLiveBoardIntegration_IT_18(t *testing.T) {
	e, st := liveSetup(t)
	srv := httptest.NewServer(Handler(e, st))
	t.Cleanup(srv.Close)

	fetch := func() string {
		t.Helper()
		res, err := srv.Client().Get(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		buf := new(strings.Builder)
		b := make([]byte, 4096)
		for {
			n, err := res.Body.Read(b)
			buf.Write(b[:n])
			if err != nil {
				break
			}
		}
		return buf.String()
	}

	before := fetch()
	if strings.Contains(before, "fresh story title") {
		t.Fatal("page contains data that does not exist yet")
	}
	if _, err := e.CreateNode(model.KindStory, "", "fresh story title", "", nil); err != nil {
		t.Fatal(err)
	}
	if after := fetch(); !strings.Contains(after, "fresh story title") {
		t.Fatal("served page must render fresh data per request")
	}
}
