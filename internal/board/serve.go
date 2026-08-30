package board

import (
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"
	"time"

	"trellis/internal/core"
	"trellis/internal/store"
)

// pollInterval is how often the SSE watcher checks the event log for changes.
var pollInterval = time.Second

// reloadScript uses a relative events URL so the same page works at any
// mount point (/ for the single board, /p/<id>/ for served boards).
const reloadScript = `<script>new EventSource("events").onmessage = () => location.reload();</script>`

// Handler serves the live board: GET / renders fresh from the engine on
// every request (plus an auto-reload script), GET /events is an SSE stream
// that ticks whenever the project's event log grows.
func Handler(e *core.Engine, st *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		html, err := Render(e)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, strings.Replace(html, "</body>", reloadScript+"</body>", 1))
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, r, st, e.Project.ID)
	})
	return mux
}

// Serve runs the live board on addr until the process ends.
func Serve(e *core.Engine, st *store.Store, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Printf("live board at http://%s (reloads on spec changes)\n", ln.Addr())
	return http.Serve(ln, Handler(e, st))
}

// MultiHandler serves every project's board from one store: / lists the
// projects (or redirects when there is only one), /p/<id>/ renders that
// project's board fresh per request, /p/<id>/events streams reload ticks.
func MultiHandler(st *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		projects, err := st.ListProjects()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if len(projects) == 1 {
			http.Redirect(w, r, "/p/"+projects[0].ID+"/", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, indexHTML(projects))
	})
	mux.HandleFunc("/p/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/p/")
		id, tail, _ := strings.Cut(rest, "/")
		e, err := core.NewEngine(st, id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch tail {
		case "":
			html, err := Render(e)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, strings.Replace(html, "</body>", reloadScript+"</body>", 1))
		case "events":
			serveSSE(w, r, st, id)
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

func indexHTML(projects []store.Project) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>trellis boards</title><style>
:root { --ground: #F7F8F6; --surface: #FFFFFF; --ink: #1C221D; --muted: #5B6560; --line: #DDE2DC; --accent: #2F6B3F; }
@media (prefers-color-scheme: dark) { :root:not([data-theme="light"]) {
  --ground: #141815; --surface: #1C221E; --ink: #E4EAE4; --muted: #97A29A; --line: #313A33; --accent: #7FB88A; } }
:root[data-theme="dark"] { --ground: #141815; --surface: #1C221E; --ink: #E4EAE4; --muted: #97A29A; --line: #313A33; --accent: #7FB88A; }
body { background: var(--ground); color: var(--ink); margin: 0; font: 16px/1.55 system-ui, sans-serif; }
.wrap { max-width: 640px; margin: 0 auto; padding: 48px 24px; }
h1 { font-size: 1.6rem; margin: 0 0 20px; } h1 span { color: var(--accent); }
a.card { display: block; background: var(--surface); border: 1px solid var(--line); border-radius: 6px;
  padding: 14px 18px; margin: 0 0 10px; color: var(--ink); text-decoration: none; }
a.card:hover { border-color: var(--accent); }
.pid { color: var(--muted); font-family: ui-monospace, monospace; font-size: 0.85em; margin-left: 8px; }
</style></head><body><div class="wrap"><h1><span>trellis</span> boards</h1>
`)
	for _, p := range projects {
		fmt.Fprintf(&b, `<a class="card" href="/p/%s/">%s<span class="pid">%s</span></a>`+"\n",
			template.HTMLEscapeString(p.ID), template.HTMLEscapeString(p.Name), template.HTMLEscapeString(p.ID))
	}
	b.WriteString("</div></body></html>\n")
	return b.String()
}

func serveSSE(w http.ResponseWriter, r *http.Request, st *store.Store, projectID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	last, err := st.MaxEventSeq(projectID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			seq, err := st.MaxEventSeq(projectID)
			if err != nil {
				return
			}
			if seq > last {
				last = seq
				fmt.Fprint(w, "data: reload\n\n")
				flusher.Flush()
			}
		}
	}
}
