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

// hasLivePane reports whether paneID appears in the poller's latest committed
// snapshot as a live pane. Requiring Alive (not mere presence in state) closes
// a pane-id-reuse hole: after a tmux server restart an unrelated pane can take
// over a recorded id, and only the snapshot's liveness check (pane id plus
// worktree-path match, see sessionview's paneAlive) ties the id back to this
// child — a bare id match would let /api/peek capture a stranger's terminal.
// Defined here rather than in poller.go because /api/peek is its only
// consumer. The zero-value snapshot has nil Sessions, which ranges as empty,
// so an unpublished snapshot simply reports false.
func (p *poller) livePaneWorktree(paneID string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, sess := range p.latest.Sessions {
		for i := range sess.Panes {
			if sess.Panes[i].PaneID == paneID && sess.Panes[i].Alive {
				return sess.Panes[i].WorktreePath, true
			}
		}
	}
	return "", false
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
	if !paneIDRe.MatchString(paneID) {
		peekError(w, http.StatusBadRequest, fmt.Sprintf("invalid pane id %q: want a tmux pane id like %%5", paneID))
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
	worktree, ok := s.poller.livePaneWorktree(paneID)
	if !ok {
		peekError(w, http.StatusNotFound, fmt.Sprintf("pane %s is not live in the current sessions", paneID))
		return
	}
	// Legacy/hand-written rows without a recorded worktree are alive on an
	// id-only basis, which cannot survive tmux pane-id reuse. Without a path
	// to verify against, capture could read an unrelated pane — refuse.
	if worktree == "" {
		peekError(w, http.StatusNotFound, fmt.Sprintf("pane %s has no recorded worktree to verify against", paneID))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	// getOnly permits HEAD; answer it before running tmux (mirrors handleStream's
	// HEAD early-return) so a probe never triggers a capture.
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	// The snapshot check above can be a cheap-tick stale; revalidate id+path
	// against tmux right before reading so a freshly reused pane id cannot
	// expose an unrelated pane's contents.
	if err := s.verifyPane(paneID, worktree); err != nil {
		peekError(w, http.StatusNotFound, err.Error())
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
