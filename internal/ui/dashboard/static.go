package dashboard

import (
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// staticHandler serves the embedded SPA bundle (built from web/ by Vite). The
// bundle uses stable filenames (assets/app.js, no content hash), so responses
// are marked no-store to keep browsers from caching a bundle across binary
// updates. When the bundle is absent (a fresh checkout builds fine because
// static/ tracks only .gitkeep), "/" serves a small instruction page instead
// of http.FileServer's directory listing, and every other path 404s. With a
// bundle, missing extensionless paths serve index.html for SPA deep links.
func (s *Server) staticHandler(sub fs.FS) http.HandlerFunc {
	// http.FileServer(http.FS(sub)) is equivalent to http.FileServerFS but does
	// not require Go 1.22+, keeping the package buildable on any supported Go.
	fileServer := http.FileServer(http.FS(sub))
	_, statErr := fs.Stat(sub, "index.html")
	built := statErr == nil
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !built {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// A failed write means the client disconnected.
			_, _ = io.WriteString(w, fallbackIndexHTML)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if _, err := fs.Stat(sub, name); errors.Is(err, fs.ErrNotExist) && path.Ext(name) == "" {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
			r.URL.RawPath = ""
		}
		fileServer.ServeHTTP(w, r)
	}
}

// fallbackIndexHTML is served at "/" when the binary was built without the web
// assets (e.g. `go build` without `make build-web`). It keeps the title marker
// "fanout dashboard" that tests and health tooling look for.
const fallbackIndexHTML = `<!doctype html>
<html lang="ja"><head><meta charset="utf-8"><title>fanout dashboard</title></head>
<body><h1>fanout dashboard</h1>
<p>Web UI assets are not built into this binary. Run <code>make build-web</code>
(or <code>make build-go</code>) and rebuild, then restart the dashboard.</p>
</body></html>
`
