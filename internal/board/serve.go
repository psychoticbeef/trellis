package board

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"trellis/internal/core"
	"trellis/internal/store"
)

// pollInterval is how often the SSE watcher checks the event log for changes.
var pollInterval = time.Second

const reloadScript = `<script>new EventSource("/events").onmessage = () => location.reload();</script>`

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
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		last, err := st.MaxEventSeq(e.Project.ID)
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
				seq, err := st.MaxEventSeq(e.Project.ID)
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
