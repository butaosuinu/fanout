package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

const (
	peekDefaultLines = 80 // matches the TUI console's peekLines
	peekMaxLines     = 400
)

// paneIDRe accepts only bare tmux pane ids ("%N"). Anything else — tmux target
// syntax, flag-like strings, whitespace — is rejected before capture-pane sees
// it, so the endpoint can never address an arbitrary tmux target.
var paneIDRe = regexp.MustCompile(`^%[0-9]{1,9}$`)

// hasPane reports whether paneID appears in the poller's latest committed
// snapshot. Defined here rather than in poller.go because /api/peek is its
// only consumer. The zero-value snapshot has nil Sessions, which ranges as
// empty, so an unpublished snapshot simply reports false.
func (p *poller) hasPane(paneID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, sess := range p.latest.Sessions {
		for i := range sess.Panes {
			if sess.Panes[i].PaneID == paneID {
				return true
			}
		}
	}
	return false
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
// dashboard stays mutation-free. Only panes present in the current snapshot
// may be peeked; lines is clamped to [1, peekMaxLines].
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
	if !s.poller.hasPane(paneID) {
		peekError(w, http.StatusNotFound, fmt.Sprintf("pane %s is not in the current sessions", paneID))
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
