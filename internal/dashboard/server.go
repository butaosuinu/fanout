// Package dashboard serves fanout's read-only Session view over a localhost
// HTTP server. It binds 127.0.0.1 only, exposes GET-only endpoints (a JSON
// snapshot and an SSE live stream), serves an embedded single-page UI, and never
// mutates repo or GitHub state. A per-start random token gates /api/* so other
// local users/processes cannot read your issue/PR data off the loopback port.
package dashboard

import (
	"bytes"
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/butaosuinu/fanout/internal/tmuxrun"
)

const (
	healthzPath       = "/healthz"
	sseHeartbeat      = 15 * time.Second
	shutdownGrace     = 3 * time.Second
	loopbackInterface = "127.0.0.1"
)

// Options configures a dashboard server.
type Options struct {
	ProjectRoot string
	Port        int    // 0 = OS-assigned ephemeral port
	Token       string // "" disables the token gate
	// ResolveGH resolves the repo label and per-issue PR provider. It is invoked
	// once, lazily, on the poller's background GitHub goroutine, so a slow
	// `gh repo view` never delays binding the server or painting the state-only
	// view. Semantics by return value:
	//   (repo, provider, nil) -> full GitHub tier
	//   ("",  nil,      err) -> sticky Degraded.GitHub
	// A nil ResolveGH disables the GitHub tier entirely (state-only, no degrade).
	ResolveGH func() (repo string, gh GHProvider, err error)
	// CapturePane is the read-only tmux pane capture behind GET /api/peek.
	// nil defaults to tmuxrun.CapturePaneOutput; tests inject a fake.
	CapturePane func(paneID string, lines int) (string, error)
	// CapturePlan is the read-only tmux capture behind GET /api/plan (scrollback
	// plus alternate screen, wrapped lines joined). nil defaults to
	// tmuxrun.CapturePlanSource; tests inject a fake.
	CapturePlan func(paneID string, lines int) (string, error)
	// VerifyPane re-checks, at request time, that paneID is still a live tmux
	// pane sitting at/under the recorded worktree. nil defaults to a
	// tmuxrun.ListLivePanes-backed check; tests inject a fake.
	VerifyPane func(paneID, worktree string) error
}

// Server is a bound, ready-to-run dashboard. New binds the listener (so a
// port-in-use error surfaces synchronously) and computes the URL; Run serves
// until the context is canceled.
type Server struct {
	listener    net.Listener
	httpServer  *http.Server
	poller      *poller
	hub         *hub
	token       string
	base        string // http://127.0.0.1:<port>
	serveErr    chan error
	capturePane func(paneID string, lines int) (string, error)
	capturePlan func(paneID string, lines int) (string, error)
	verifyPane  func(paneID, worktree string) error
}

// New binds the loopback listener and assembles the handler. The returned
// Server's URL is final and can be printed/opened before Run.
func New(opts Options) (*Server, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(loopbackInterface, strconv.Itoa(opts.Port)))
	if err != nil {
		return nil, err
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		// Cleanup of a listener we will never serve on; the type error is the one to report.
		_ = ln.Close()
		return nil, fmt.Errorf("unexpected listener address %T", ln.Addr())
	}

	capture := opts.CapturePane
	if capture == nil {
		capture = tmuxrun.CapturePaneOutput
	}
	capturePlan := opts.CapturePlan
	if capturePlan == nil {
		capturePlan = tmuxrun.CapturePlanSource
	}
	verify := opts.VerifyPane
	if verify == nil {
		verify = verifyLivePane
	}
	h := newHub()
	s := &Server{
		listener:    ln,
		hub:         h,
		poller:      newLazyPoller(opts.ProjectRoot, opts.ResolveGH, h),
		token:       opts.Token,
		base:        fmt.Sprintf("http://%s:%d", loopbackInterface, addr.Port),
		serveErr:    make(chan error, 1),
		capturePane: capture,
		capturePlan: capturePlan,
		verifyPane:  verify,
	}
	handler, err := s.handler()
	if err != nil {
		// Same cleanup-only close as above.
		_ = ln.Close()
		return nil, err
	}
	s.httpServer = &http.Server{Handler: handler}
	return s, nil
}

// URL is the dashboard address, with the token embedded when the gate is on.
func (s *Server) URL() string {
	u := s.base + "/"
	if s.token != "" {
		u += "?token=" + s.token
	}
	return u
}

// HealthURL is the token-free liveness endpoint, used to confirm the server is
// actually serving before a caller hands control to the user.
func (s *Server) HealthURL() string {
	return s.base + healthzPath
}

// Start begins polling and serving in the background and returns immediately.
// Pair with Wait. (Run = Start + Wait.)
func (s *Server) Start(ctx context.Context) {
	s.poller.Start(ctx)
	go func() {
		err := s.httpServer.Serve(s.listener)
		if err == http.ErrServerClosed {
			err = nil
		}
		s.serveErr <- err
	}()
}

// Wait blocks until ctx is canceled (then shuts down gracefully) or the server
// stops on its own. ErrServerClosed (the normal shutdown path) is reported nil.
func (s *Server) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = s.httpServer.Shutdown(shutdownCtx)
		return <-s.serveErr
	case err := <-s.serveErr:
		return err
	}
}

// Run starts polling and serves until ctx is canceled, then shuts down
// gracefully.
func (s *Server) Run(ctx context.Context) error {
	s.Start(ctx)
	return s.Wait(ctx)
}

func (s *Server) handler() (http.Handler, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.HandleFunc(healthzPath, s.getOnly(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "ok")
	}))
	mux.HandleFunc("/api/snapshot", s.getOnly(s.requireToken(s.handleSnapshot)))
	mux.HandleFunc("/api/stream", s.getOnly(s.requireToken(s.handleStream)))
	mux.HandleFunc("/api/peek", s.getOnly(s.requireToken(s.handlePeek)))
	mux.HandleFunc("/api/plan", s.getOnly(s.requireToken(s.handlePlan)))
	// Catch-all: the embedded SPA. The HTML shell is token-free so the page can
	// load and then read ?token= for its /api/* calls.
	mux.Handle("/", s.getOnly(s.staticHandler(sub)))
	return mux, nil
}

// staticHandler serves the embedded SPA bundle (built from web/ by Vite). The
// bundle uses stable filenames (assets/app.js, no content hash), so responses
// are marked no-store to keep browsers from caching a bundle across binary
// updates. When the bundle is absent (a fresh checkout builds fine because
// static/ tracks only .gitkeep), "/" serves a small instruction page instead
// of http.FileServer's directory listing, and every other path 404s.
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
			_, _ = io.WriteString(w, fallbackIndexHTML)
			return
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

// getOnly rejects non-GET/HEAD methods with 405 (the dashboard is read-only).
func (s *Server) getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

// requireToken gates /api/* with a constant-time token compare when a token is
// configured. EventSource cannot set headers, so the token rides as ?token=.
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			got := r.URL.Query().Get("token")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) handleSnapshot(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	// A failed response write means the client went away; nothing to do here.
	_, _ = w.Write(s.poller.snapshotJSON())
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// getOnly permits HEAD; a HEAD probe (e.g. `curl -I`) wants headers only. Open
	// the streaming loop only for GET, or the bodyless HEAD response would block
	// until the client times out.
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)

	// Subscribe before painting the initial snapshot. If we painted first, a
	// broadcast landing in the gap before subscribe() would be lost — and since
	// later ticks only broadcast on a content change, the tab could keep rendering
	// the stale initial snapshot indefinitely. Subscribing first guarantees any
	// post-paint broadcast is queued; the worst case is a harmless duplicate of
	// the initial frame, because a broadcast's payload is the same committed
	// snapshot that snapshotJSON returns.
	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)

	// Paint immediately so a fresh tab doesn't wait for the next tick. Write
	// errors here and below mean the client disconnected; the loop exits via
	// r.Context().Done() rather than by inspecting them.
	_, _ = w.Write(sseFrame(s.poller.snapshotJSON()))
	flusher.Flush()

	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case payload, ok := <-ch:
			if !ok {
				return // hub closed on shutdown
			}
			_, _ = w.Write(payload)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		}
	}
}

// sseFrame wraps a JSON snapshot as a single SSE "snapshot" event. json.Marshal
// emits single-line JSON, so the data field never contains a raw newline.
func sseFrame(data []byte) []byte {
	var b bytes.Buffer
	b.WriteString("event: snapshot\ndata: ")
	b.Write(data)
	b.WriteString("\n\n")
	return b.Bytes()
}
