package board

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
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

// liveDragScript is injected only by live handlers and only when Render emitted
// a story map. Static exports remain read-only and byte-for-byte unchanged.
const liveDragScript = `<script>
(function () {
	var drag = null;
	var errorBox = document.createElement('pre');
	errorBox.setAttribute('role', 'alert');
	errorBox.setAttribute('data-map-move-error', '');
	errorBox.hidden = true;
	var mapPanel = document.querySelector('[data-board-panel="map"]');
	if (!mapPanel) return;
	mapPanel.insertBefore(errorBox, mapPanel.firstChild);

	function restore(state) {
		if (state.next && state.next.parentNode === state.parent) state.parent.insertBefore(state.card, state.next);
		else state.parent.appendChild(state.card);
	}
	function showError(message) {
		errorBox.textContent = message;
		errorBox.hidden = false;
	}
	function target(cell) {
		if (!cell) return null;
		var value = cell.getAttribute('data-map-cell') || '';
		var split = value.lastIndexOf(':');
		var slice = Number(value.slice(split + 1));
		if (split < 1 || !Number.isInteger(slice) || slice < 1) return null;
		return {activity_id: value.slice(0, split), slice: slice};
	}

	Array.prototype.forEach.call(mapPanel.querySelectorAll('.map-card'), function (card) {
		card.draggable = true;
		card.addEventListener('dragstart', function (event) {
			drag = {card: card, parent: card.parentNode, next: card.nextSibling};
			errorBox.hidden = true;
			errorBox.textContent = '';
			event.dataTransfer.effectAllowed = 'move';
			event.dataTransfer.setData('text/plain', card.getAttribute('data-story-open'));
		});
		card.addEventListener('dragend', function () {
			if (drag && drag.card === card) drag = null;
		});
	});
	mapPanel.addEventListener('dragover', function (event) {
		if (drag && target(event.target.closest('[data-map-cell]'))) event.preventDefault();
	});
	mapPanel.addEventListener('drop', function (event) {
		var cell = event.target.closest('[data-map-cell]');
		var destination = target(cell);
		if (!drag || !destination) return;
		event.preventDefault();
		var current = drag;
		drag = null;
		current.card.draggable = false;
		cell.appendChild(current.card);
		fetch('map-position', {
			method: 'POST',
			headers: {'Content-Type': 'application/json'},
			body: JSON.stringify({story_id: current.card.getAttribute('data-story-open'), activity_id: destination.activity_id, slice: destination.slice})
		}).then(function (response) {
			if (response.ok) return;
			return response.text().then(function (text) {
				var message = text;
				try { var decoded = JSON.parse(text); if (typeof decoded.error === 'string') message = decoded.error; } catch (_) {}
				throw new Error(message);
			});
		}).catch(function (error) {
			restore(current);
			current.card.draggable = true;
			showError(error.message);
		});
	});
}());
</script>`

func liveHTML(html string) string {
	scripts := reloadScript
	if strings.Contains(html, `data-board-panel="map"`) {
		scripts = liveDragScript + reloadScript
	}
	return strings.Replace(html, "</body>", scripts+"</body>", 1)
}

// Handler serves one live board: GET / renders fresh with auto-reload,
// GET /events streams event-log ticks, and POST /map-position applies
// project-scoped story map placement through the engine.
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
		fmt.Fprint(w, liveHTML(html))
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		serveSSE(w, r, st, e.Project.ID)
	})
	mux.Handle("/map-position", mapPositionHandler(e))
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

// MultiHandler serves every project's board from one store: / lists or
// redirects, /p/<id>/ renders fresh, /p/<id>/events streams reload ticks,
// and POST /p/<id>/map-position applies that project's story map placement.
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
			fmt.Fprint(w, liveHTML(html))
		case "events":
			serveSSE(w, r, st, id)
		case "map-position":
			mapPositionHandler(e).ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	return mux
}

type mapPositionRequest struct {
	StoryID    string `json:"story_id"`
	ActivityID string `json:"activity_id"`
	Slice      int    `json:"slice"`
}

func mapPositionHandler(e *core.Engine) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSONError(w, http.StatusMethodNotAllowed, "method must be POST")
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeJSONError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Host != r.Host {
				writeJSONError(w, http.StatusForbidden, "origin must match board host")
				return
			}
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var request mapPositionRequest
		if err := decoder.Decode(&request); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid map-position request: "+err.Error())
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				err = fmt.Errorf("multiple JSON values")
			}
			writeJSONError(w, http.StatusBadRequest, "invalid map-position request: "+err.Error())
			return
		}
		if _, err := e.SetMapPosition(request.StoryID, request.ActivityID, request.Slice); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
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
.pdesc { color: var(--muted); font-size: 0.9em; margin-top: 4px; }
</style></head><body><div class="wrap"><h1><span>trellis</span> boards</h1>
`)
	for _, p := range projects {
		desc := ""
		if p.Description != "" {
			desc = `<div class="pdesc">` + template.HTMLEscapeString(p.Description) + `</div>`
		}
		fmt.Fprintf(&b, `<a class="card" href="/p/%s/">%s<span class="pid">%s</span>%s</a>`+"\n",
			template.HTMLEscapeString(p.ID), template.HTMLEscapeString(p.Name), template.HTMLEscapeString(p.ID), desc)
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
