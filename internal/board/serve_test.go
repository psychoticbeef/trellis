package board

import (
	"bufio"
	"io"
	"net/http"
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

// TestMultiHandlerUnit_UT_24 proves UT-24 (DD-24 "MultiHandler and serve
// wiring"): index rendering with escaping, single-project redirect,
// unknown-id 404, relative events URL in the reload script.
func TestMultiHandlerUnit_UT_24(t *testing.T) {
	_, st := liveSetup(t) // creates project p1
	srv := httptest.NewServer(MultiHandler(st))
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	// Single project: redirect to its board.
	res, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/p/p1/" {
		t.Fatalf("single-project root: %d -> %q", res.StatusCode, res.Header.Get("Location"))
	}

	// Second project with hostile name: index lists both, escaped.
	if err := st.CreateProject(store.Project{ID: "p2", Name: `<script>x</script>`, BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	res, _ = client.Get(srv.URL + "/")
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("index status %d", res.StatusCode)
	}
	page := string(body)
	for _, want := range []string{`href="/p/p1/"`, `href="/p/p2/"`, "&lt;script&gt;"} {
		if !strings.Contains(page, want) {
			t.Errorf("index missing %q:\n%s", want, page)
		}
	}
	if strings.Contains(page, "<script>x</script>") {
		t.Fatal("project name not escaped")
	}

	// Unknown project: 404.
	res, _ = client.Get(srv.URL + "/p/nope/")
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown project: %d", res.StatusCode)
	}

	// Relative events URL in the reload script.
	if !strings.Contains(reloadScript, `EventSource("events")`) || strings.Contains(reloadScript, `"/events"`) {
		t.Fatalf("reload script must use a relative events URL: %s", reloadScript)
	}
}

// TestMultiBoardIntegration_IT_23 proves IT-23 (US-23): per-project boards
// render fresh and stream their own SSE ticks.
func TestMultiBoardIntegration_IT_23(t *testing.T) {
	old := pollInterval
	pollInterval = 20 * time.Millisecond
	t.Cleanup(func() { pollInterval = old })
	e1, st := liveSetup(t)
	if err := st.CreateProject(store.Project{ID: "p2", Name: "second", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e2, err := core.NewEngine(st, "p2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e1.CreateNode(model.KindStory, "", "story in one", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e2.CreateNode(model.KindStory, "", "story in two", "", nil); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(MultiHandler(st))
	t.Cleanup(srv.Close)

	fetch := func(path string) string {
		t.Helper()
		res, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b)
	}
	if page := fetch("/p/p1/"); !strings.Contains(page, "story in one") || strings.Contains(page, "story in two") {
		t.Fatalf("p1 board wrong:\n%.300s", page)
	}
	if page := fetch("/p/p2/"); !strings.Contains(page, "story in two") || strings.Contains(page, "story in one") {
		t.Fatalf("p2 board wrong:\n%.300s", page)
	}

	// SSE is per project: a p1 mutation ticks p1's stream, not p2's.
	es1, err := srv.Client().Get(srv.URL + "/p/p1/events")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { es1.Body.Close() })
	es2, err := srv.Client().Get(srv.URL + "/p/p2/events")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { es2.Body.Close() })
	r1, r2 := bufio.NewReader(es1.Body), bufio.NewReader(es2.Body)
	r1.ReadString('\n')
	r2.ReadString('\n')
	tick := func(r *bufio.Reader, ch chan<- string) {
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(l, "data:") {
				ch <- l
				return
			}
		}
	}
	c1, c2 := make(chan string, 1), make(chan string, 1)
	go tick(r1, c1)
	go tick(r2, c2)
	if _, err := e1.CreateNode(model.KindStory, "", "another in one", "", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c1:
	case <-time.After(2 * time.Second):
		t.Fatal("p1 stream did not tick")
	}
	select {
	case l := <-c2:
		t.Fatalf("p2 stream ticked on a p1 mutation: %q", l)
	case <-time.After(150 * time.Millisecond):
	}
}
