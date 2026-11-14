// Package dashboard serves fanout's Session view over a localhost HTTP server.
// It binds 127.0.0.1 only, serves an embedded single-page UI, and reads through
// GET-only endpoints (a JSON snapshot, an SSE live stream, pane captures, and
// worktree diffs). A per-start random token gates /api/* so other local
// users/processes cannot read your issue/PR data off the loopback port.
//
// There are exactly two mutation endpoints, and each is scoped to one GitHub
// pull request: POST /api/pr/merge merges it, and POST /api/pr/delete-branch
// removes its remote head ref once it is merged. Neither touches the local
// working tree, local git refs, worktrees, state.json, or pane input. Both sit
// behind postOnly + sameOriginOnly in addition to the token gate.
package dashboard

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/butaosuinu/fanout/internal/app/prmerge"
	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/gitstat"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
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
	// ListLive observes panes through the runtime backend routes selected by cmd;
	// partial observations may accompany an error from another route.
	ListLive func() ([]backend.LivePane, error)
	// CapturePane is the read-only tmux pane capture behind GET /api/peek.
	// Owned Herdr rows use ReadManagedPane instead. nil defaults to
	// tmuxrun.CapturePaneOutput; tests inject a fake.
	CapturePane func(paneID string, lines int) (string, error)
	// CapturePlan is the read-only tmux capture behind GET /api/plan (scrollback
	// plus alternate screen, wrapped lines joined). nil defaults to
	// tmuxrun.CapturePlanSource; tests inject a fake.
	CapturePlan func(paneID string, lines int) (string, error)
	// DiffWorktree is the read-only Git collector behind GET /api/diff. nil
	// defaults to gitstat.Runner.WorktreePatch; tests inject a fake.
	DiffWorktree func(path, baseRef string) (gitstat.Patch, error)
	// VerifyPane re-checks, at request time, that the snapshot pane is still
	// the same live tmux pane. Keyed panes verify paneID + shellKey; legacy
	// unkeyed agent panes verify paneID + worktree path. nil defaults to a
	// tmuxrun.ListLivePanes-backed check; tests inject a fake.
	VerifyPane func(sessionview.PaneView) error
	// Owned Herdr peek is read-only but uses persisted ownership identity rather
	// than a tmux pane id. OwnsManagedPane performs request-time immutable admission;
	// ReadManagedPane repeats that binding immediately before the read.
	OwnsManagedPane func(sessionview.PaneView) bool
	ReadManagedPane func(sessionview.PaneView, int) (string, error)
	// MergePR performs the dashboard's single mutation: merging one GitHub pull
	// request, and optionally deleting its remote head ref. Unlike every other
	// injectable here it has NO in-package default. A nil MergePR makes
	// POST /api/pr/merge answer 503, so the capability to change GitHub state
	// exists only when the composition root granted it explicitly.
	MergePR func(context.Context, prmerge.Request) (prmerge.Result, error)
	// DeleteBranch removes a merged pull request's remote head ref. It shares
	// MergePR's fail-closed wiring: nil disables POST /api/pr/delete-branch.
	DeleteBranch func(context.Context, prmerge.DeleteRequest) error
}

// Server is a bound, ready-to-run dashboard. New binds the listener (so a
// port-in-use error surfaces synchronously) and computes the URL; Run serves
// until the context is canceled.
type Server struct {
	listener        net.Listener
	httpServer      *http.Server
	poller          *poller
	hub             *hub
	token           string
	base            string // http://127.0.0.1:<port>
	serveErr        chan error
	capturePane     func(paneID string, lines int) (string, error)
	capturePlan     func(paneID string, lines int) (string, error)
	diffWorktree    func(path, baseRef string) (gitstat.Patch, error)
	verifyPane      func(sessionview.PaneView) error
	ownsManagedPane func(sessionview.PaneView) bool
	readManagedPane func(sessionview.PaneView, int) (string, error)
	mergePR         func(context.Context, prmerge.Request) (prmerge.Result, error)
	deleteBranch    func(context.Context, prmerge.DeleteRequest) error
	// hostPort is the bound "127.0.0.1:<port>". sameOriginOnly demands an exact
	// Host match against it, which is what pins DNS rebinding: a rebound name
	// arrives as Host: evil.test:<port>, not as the loopback literal.
	hostPort string

	// mergeMu guards mergeInFlight and the on-disk merge claims. mergeInFlight is
	// the per-PR lock that keeps an impatient double-click from racing two gh
	// processes on the same pull request; the claims file holds a PR whose merge
	// command may have reached GitHub without a readable outcome, because one
	// tab's in-memory guard cannot stop another tab, a reload, or a restart.
	mergeMu sync.Mutex
	// claimsPath is the repository-common claims file, resolved on first use.
	// claimsErr remembers a failed resolution so the mutation stays closed
	// instead of retrying a git call per request.
	claimsPath    string
	claimsErr     error
	mergeInFlight map[string]struct{}
	// mergeHeld mirrors the claims file in memory so a failed write still blocks
	// a repeat within this process.
	mergeHeld map[string]time.Time
}

// New binds the loopback listener and assembles the handler. The returned
// Server's URL is final and can be printed/opened before Run.
//
//nolint:funlen // Keep the complete dependency-defaulting and server assembly transaction together.
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
	p := newLazyPoller(opts.ProjectRoot, opts.ResolveGH, h)
	p.listLive = opts.ListLive
	s := &Server{
		listener:        ln,
		hub:             h,
		poller:          p,
		token:           opts.Token,
		base:            fmt.Sprintf("http://%s:%d", loopbackInterface, addr.Port),
		serveErr:        make(chan error, 1),
		capturePane:     capture,
		capturePlan:     capturePlan,
		diffWorktree:    opts.DiffWorktree,
		verifyPane:      verify,
		ownsManagedPane: opts.OwnsManagedPane,
		readManagedPane: opts.ReadManagedPane,
		mergePR:         opts.MergePR,
		deleteBranch:    opts.DeleteBranch,
		hostPort:        net.JoinHostPort(loopbackInterface, strconv.Itoa(addr.Port)),
		mergeInFlight:   map[string]struct{}{},
		mergeHeld:       map[string]time.Time{},
	}
	handler, err := s.handler()
	if err != nil {
		// Same cleanup-only close as above.
		_ = ln.Close()
		return nil, err
	}
	s.httpServer = &http.Server{
		Handler: handler,
		// Bound the header phase and idle connections. ReadTimeout and
		// WriteTimeout are deliberately unset: they are whole-request deadlines
		// and would sever the long-lived SSE stream at /api/stream. Per-request
		// deadlines belong in the handlers that need them.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
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
