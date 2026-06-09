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
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"time"
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
}

// Server is a bound, ready-to-run dashboard. New binds the listener (so a
// port-in-use error surfaces synchronously) and computes the URL; Run serves
// until the context is cancelled.
type Server struct {
	listener   net.Listener
	httpServer *http.Server
	poller     *poller
	hub        *hub
	token      string
	base       string // http://127.0.0.1:<port>
	serveErr   chan error
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
		ln.Close()
		return nil, fmt.Errorf("unexpected listener address %T", ln.Addr())
	}

	h := newHub()
	s := &Server{
		listener: ln,
		hub:      h,
		poller:   newLazyPoller(opts.ProjectRoot, opts.ResolveGH, h),
		token:    opts.Token,
		base:     fmt.Sprintf("http://%s:%d", loopbackInterface, addr.Port),
		serveErr: make(chan error, 1),
	}
	handler, err := s.handler()
	if err != nil {
		ln.Close()
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

// Wait blocks until ctx is cancelled (then shuts down gracefully) or the server
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

// Run starts polling and serves until ctx is cancelled, then shuts down
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
	// http.FileServer(http.FS(sub)) is equivalent to http.FileServerFS but does
	// not require Go 1.22+, keeping the package buildable on any supported Go.
	fileServer := http.FileServer(http.FS(sub))

	mux := http.NewServeMux()
	mux.HandleFunc(healthzPath, s.getOnly(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "ok")
	}))
	mux.HandleFunc("/api/snapshot", s.getOnly(s.requireToken(s.handleSnapshot)))
	mux.HandleFunc("/api/stream", s.getOnly(s.requireToken(s.handleStream)))
	// Catch-all: the embedded SPA. The HTML shell is token-free so the page can
	// load and then read ?token= for its /api/* calls.
	mux.Handle("/", s.getOnly(fileServer.ServeHTTP))
	return mux, nil
}

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
	w.Write(s.poller.snapshotJSON())
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

	// Paint immediately so a fresh tab doesn't wait for the next tick.
	w.Write(sseFrame(s.poller.snapshotJSON()))
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
			w.Write(payload)
			flusher.Flush()
		case <-heartbeat.C:
			w.Write([]byte(": ping\n\n"))
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
