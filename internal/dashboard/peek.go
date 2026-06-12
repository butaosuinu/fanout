package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/sessionview"
	"github.com/butaosuinu/fanout/internal/tmuxrun"
)

const (
	peekDefaultLines = 80 // matches the TUI console's peekLines
	peekMaxLines     = 400
)

// paneIDRe accepts only bare tmux pane ids ("%N"). Anything else — tmux target
// syntax, flag-like strings, whitespace — is rejected before capture-pane sees
// it, so the endpoint can never address an arbitrary tmux target.
var paneIDRe = regexp.MustCompile(`^%[0-9]{1,9}$`)

// livePaneView returns the snapshot row whose pane id is paneID, but only when
// the poller's latest committed snapshot reports it as a live pane. Requiring
// Alive (not mere presence in state) closes a pane-id-reuse hole: after a tmux
// server restart an unrelated pane can take over a recorded id, and only the
// snapshot's liveness check (pane id plus worktree-path match, see
// sessionview's paneAlive) ties the id back to this child — a bare id match
// would let /api/peek or /api/plan capture a stranger's terminal. Defined here
// rather than in poller.go because the capture endpoints are its only
// consumers. The zero-value snapshot has nil Sessions, which ranges as empty,
// so an unpublished snapshot simply reports false.
func (p *poller) livePaneView(paneID string) (sessionview.PaneView, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, sess := range p.latest.Sessions {
		for i := range sess.Panes {
			if sess.Panes[i].PaneID == paneID && sess.Panes[i].Alive {
				return sess.Panes[i], true
			}
		}
	}
	return sessionview.PaneView{}, false
}

// requireLivePane is the request-validation chain GET /api/peek and
// GET /api/plan share: pane-id shape (400), snapshot liveness via livePaneView
// (404), and recorded-worktree presence (404 — legacy/hand-written rows
// without a worktree are alive on an id-only basis, which cannot survive tmux
// pane-id reuse; without a path to verify against, capture could read an
// unrelated pane, so refuse). On ok=false the JSON error response has already
// been written.
func (s *Server) requireLivePane(w http.ResponseWriter, paneID string) (sessionview.PaneView, bool) {
	if !paneIDRe.MatchString(paneID) {
		peekError(w, http.StatusBadRequest, fmt.Sprintf("invalid pane id %q: want a tmux pane id like %%5", paneID))
		return sessionview.PaneView{}, false
	}
	pv, ok := s.poller.livePaneView(paneID)
	if !ok {
		peekError(w, http.StatusNotFound, fmt.Sprintf("pane %s is not live in the current sessions", paneID))
		return sessionview.PaneView{}, false
	}
	if pv.WorktreePath == "" {
		peekError(w, http.StatusNotFound, fmt.Sprintf("pane %s has no recorded worktree to verify against", paneID))
		return sessionview.PaneView{}, false
	}
	return pv, true
}

// beginPaneCapture is the capture preamble shared by GET /api/peek and
// GET /api/plan: response headers, the HEAD early-return (getOnly permits
// HEAD; answer it before running tmux — mirrors handleStream's HEAD
// early-return — so a probe never triggers a capture), and the request-time
// tmux revalidation. The snapshot check in requireLivePane can be a
// cheap-tick stale, which is enough for a tmux restart to hand the id to an
// unrelated pane; verifying id+path right before reading shrinks that reuse
// window to the instant before capture. A false return means the response is
// complete (HEAD answered, or verify failed with 404) and the handler must
// not capture.
func (s *Server) beginPaneCapture(w http.ResponseWriter, r *http.Request, paneID, worktree string) bool {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return false
	}
	if err := s.verifyPane(paneID, worktree); err != nil {
		peekError(w, http.StatusNotFound, err.Error())
		return false
	}
	return true
}

// verifyLivePane is the default Options.VerifyPane: it re-resolves the pane id
// against tmux at request time and requires the pane's current path to sit
// at/under the recorded worktree (same rule as sessionview's paneAlive). The
// snapshot's Alive flag can be up to one cheap-tick stale, which is enough for
// a tmux restart to hand %N to an unrelated pane; this check shrinks that
// reuse window from seconds to the instant before capture.
func verifyLivePane(paneID, worktree string) error {
	panes, err := tmuxrun.ListLivePanes()
	if err != nil {
		return fmt.Errorf("tmux list-panes: %w", err)
	}
	for _, pane := range panes {
		if pane.ID != paneID {
			continue
		}
		if worktree == "" || (pane.CurrentPath != worktree &&
			!strings.HasPrefix(pane.CurrentPath, worktree+string(filepath.Separator))) {
			return fmt.Errorf("pane %s is no longer at its recorded worktree", paneID)
		}
		return nil
	}
	return fmt.Errorf("pane %s is gone", paneID)
}

// peekResponse is the GET /api/peek wire contract the SPA consumes.
type peekResponse struct {
	PaneID     string `json:"paneId"`
	Lines      int    `json:"lines"`
	CapturedAt string `json:"capturedAt"` // RFC3339 UTC
	Output     string `json:"output"`
}

// handlePeek serves GET /api/peek?pane=%N[&lines=K]: a one-shot capture-pane
// snapshot of one recorded pane. capture-pane only reads the pane, so the
// dashboard stays mutation-free. Only panes live in the current snapshot may
// be peeked; lines is clamped to [1, peekMaxLines].
func (s *Server) handlePeek(w http.ResponseWriter, r *http.Request) {
	paneID := r.URL.Query().Get("pane")
	pv, ok := s.requireLivePane(w, paneID)
	if !ok {
		return
	}
	lines := peekDefaultLines
	if raw := r.URL.Query().Get("lines"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			peekError(w, http.StatusBadRequest, fmt.Sprintf("invalid lines %q: want an integer", raw))
			return
		}
		lines = min(max(n, 1), peekMaxLines)
	}
	if !s.beginPaneCapture(w, r, paneID, pv.WorktreePath) {
		return
	}
	out, err := s.capturePane(paneID, lines)
	if err != nil {
		peekError(w, http.StatusBadGateway, "tmux capture-pane: "+err.Error())
		return
	}
	// A failed response write means the client went away; nothing to do here.
	_ = json.NewEncoder(w).Encode(peekResponse{
		PaneID:     paneID,
		Lines:      lines,
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		Output:     out,
	})
}

// peekError writes a JSON error body so the SPA can surface the message
// verbatim instead of parsing a text/plain http.Error.
func peekError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
