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

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

const (
	peekDefaultLines = 80 // matches the TUI console's peekLines
	peekMaxLines     = 400
)

// paneIDRe accepts only bare tmux pane ids ("%N") on the tmux capture route.
// Herdr uses its separately admitted persisted identity.
var paneIDRe = regexp.MustCompile(`^%[0-9]{1,9}$`)

type snapshotPaneSelection struct {
	staleTmux      sessionview.PaneView
	staleTmuxFound bool
	nonTmux        sessionview.PaneView
	nonTmuxFound   bool
	nonTmuxUnique  bool
}

func (s *snapshotPaneSelection) observe(pv sessionview.PaneView) (sessionview.PaneView, bool) {
	if backend.NormalizeName(pv.Backend) == backend.Tmux {
		if pv.Alive {
			return pv, true
		}
		if !s.staleTmuxFound {
			s.staleTmux, s.staleTmuxFound = pv, true
		}
		return sessionview.PaneView{}, false
	}
	if s.nonTmuxFound {
		s.nonTmuxUnique = false
	} else {
		s.nonTmux, s.nonTmuxFound, s.nonTmuxUnique = pv, true, true
	}
	return sessionview.PaneView{}, false
}

func (s snapshotPaneSelection) result() (sessionview.PaneView, bool, bool) {
	if s.nonTmuxFound {
		return s.nonTmux, true, s.nonTmuxUnique
	}
	return s.staleTmux, s.staleTmuxFound, true
}

// snapshotPaneView selects one latest-snapshot row whose runtime-native pane id
// is paneID. A live tmux row wins over stale duplicates. Multiple non-tmux rows
// are reported as non-unique so backend admission cannot authorize the wrong
// persisted identity after releasing this lock.
func (p *poller) snapshotPaneView(paneID string) (sessionview.PaneView, bool, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	selection := snapshotPaneSelection{}
	for _, sess := range p.latest.Sessions {
		for i := range sess.Panes {
			pv := sess.Panes[i]
			if pv.PaneID != paneID {
				continue
			}
			if selected, found := selection.observe(pv); found {
				return selected, true, true
			}
		}
	}
	return selection.result()
}

// requireLivePane is the request-validation chain GET /api/peek and
// GET /api/plan share: snapshot selection, owned Herdr admission, or tmux pane-id
// shape and request-time identity verification. On ok=false the JSON error
// response has already been written.
func (s *Server) requireLivePane(w http.ResponseWriter, paneID string) (sessionview.PaneView, bool) {
	pv, recorded, unique := s.poller.snapshotPaneView(paneID)
	if recorded && !unique {
		peekError(w, http.StatusNotFound, fmt.Sprintf("pane %s does not identify one recorded runtime pane", paneID))
		return sessionview.PaneView{}, false
	}
	runtimeBackend := backend.NormalizeName(pv.Backend)
	if recorded && runtimeBackend == backend.Herdr {
		return s.requireOwnedHerdrPane(w, pv)
	}
	if recorded && runtimeBackend != backend.Tmux {
		peekError(w, http.StatusNotFound, fmt.Sprintf("pane %s uses unsupported backend %q", paneID, runtimeBackend))
		return sessionview.PaneView{}, false
	}
	return requireLiveTmuxPane(w, paneID, pv, recorded)
}

func requireLiveTmuxPane(
	w http.ResponseWriter,
	paneID string,
	pv sessionview.PaneView,
	recorded bool,
) (sessionview.PaneView, bool) {
	if !paneIDRe.MatchString(paneID) {
		peekError(w, http.StatusBadRequest, fmt.Sprintf("invalid pane id %q: want a tmux pane id like %%5", paneID))
		return sessionview.PaneView{}, false
	}
	if !recorded || !pv.Alive {
		peekError(w, http.StatusNotFound, fmt.Sprintf("pane %s is not live in the current sessions", paneID))
		return sessionview.PaneView{}, false
	}
	if pv.Kind == state.PaneKindShell {
		if strings.TrimSpace(pv.ShellKey) == "" {
			peekError(w, http.StatusNotFound, fmt.Sprintf("pane %s has no recorded shell key to verify against", paneID))
			return sessionview.PaneView{}, false
		}
		return pv, true
	}
	if pv.WorktreePath == "" {
		peekError(w, http.StatusNotFound, fmt.Sprintf("pane %s has no recorded worktree to verify against", paneID))
		return sessionview.PaneView{}, false
	}
	return pv, true
}

func (s *Server) requireOwnedHerdrPane(
	w http.ResponseWriter,
	pv sessionview.PaneView,
) (sessionview.PaneView, bool) {
	owned := pv.Alive && s.ownsHerdrPane != nil && s.readHerdrPane != nil &&
		s.ownsHerdrPane(pv)
	if !owned {
		peekError(
			w,
			http.StatusNotFound,
			fmt.Sprintf(
				"pane %s is not in this repository's fanout-owned Herdr session",
				pv.PaneID,
			),
		)
		return sessionview.PaneView{}, false
	}
	return pv, true
}

// beginPaneCapture is the capture preamble shared by GET /api/peek and
// GET /api/plan: response headers, the HEAD early-return (getOnly permits
// HEAD; answer it before running tmux — mirrors handleStream's HEAD
// early-return — so a probe never triggers a capture), and the request-time
// tmux revalidation. The snapshot check in requireLivePane can be a cheap-tick
// stale, which is enough for a tmux restart to hand the id to an unrelated
// pane; verifying the row identity right before reading shrinks that reuse
// window to the instant before capture. A false return means the response is
// complete (HEAD answered, or verify failed with 404) and the handler must not
// capture.
func (s *Server) beginPaneCapture(w http.ResponseWriter, r *http.Request, pv sessionview.PaneView) bool {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return false
	}
	if backend.NormalizeName(pv.Backend) == backend.Herdr {
		return true
	}
	if err := s.verifyPane(pv); err != nil {
		peekError(w, http.StatusNotFound, err.Error())
		return false
	}
	return true
}

// verifyLivePane is the default Options.VerifyPane: it re-resolves the pane id
// against tmux at request time. Keyed panes require the recorded
// @fanout_shell_key; legacy unkeyed agent panes fall back to their recorded
// worktree for this read-only operation. The snapshot's Alive flag can be up to one cheap-tick
// stale, which is enough for a tmux restart to hand %N to an unrelated pane;
// this check shrinks that reuse window from seconds to the instant before
// capture.
func verifyLivePane(pv sessionview.PaneView) error {
	panes, err := tmuxrun.ListLivePanes()
	if err != nil {
		return fmt.Errorf("tmux list-panes: %w", err)
	}
	return verifyPaneAgainstLive(pv, panes)
}

func verifyPaneAgainstLive(pv sessionview.PaneView, panes []tmuxrun.LivePane) error {
	for _, pane := range panes {
		if pane.ID != pv.PaneID {
			continue
		}
		if strings.TrimSpace(pv.ShellKey) != "" {
			if pane.ShellKey != pv.ShellKey {
				return fmt.Errorf("pane %s is no longer the recorded fanout pane", pv.PaneID)
			}
			return nil
		}
		if pv.Kind == state.PaneKindShell {
			return fmt.Errorf("pane %s has no recorded shell key", pv.PaneID)
		}
		worktree := pv.WorktreePath
		if worktree == "" || (pane.CurrentPath != worktree &&
			!strings.HasPrefix(pane.CurrentPath, worktree+string(filepath.Separator))) {
			return fmt.Errorf("pane %s is no longer at its recorded worktree", pv.PaneID)
		}
		return nil
	}
	return fmt.Errorf("pane %s is gone", pv.PaneID)
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
	if !s.beginPaneCapture(w, r, pv) {
		return
	}
	out, err := s.readPane(pv, lines)
	if err != nil {
		peekError(w, http.StatusBadGateway, err.Error())
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

func (s *Server) readPane(pv sessionview.PaneView, lines int) (string, error) {
	if backend.NormalizeName(pv.Backend) == backend.Herdr {
		out, err := s.readHerdrPane(pv, lines)
		if err != nil {
			return "", fmt.Errorf("herdr pane read: %w", err)
		}
		return out, nil
	}
	out, err := s.capturePane(pv.PaneID, lines)
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %w", err)
	}
	return out, nil
}

// peekError writes a JSON error body so the SPA can surface the message
// verbatim instead of parsing a text/plain http.Error.
func peekError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
