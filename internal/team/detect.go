package team

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/state"
)

// FanoutTagRE is the canonical capture regex for the one-line prompt prefix
// built by cmd/fanout (oneLinePrompt): "[fanout #N of #P]". Group 1 is the
// child issue number, group 3 the parent ref ("" for tag-only prompts).
// Later sub-issues must reference this definition instead of redefining it.
var FanoutTagRE = regexp.MustCompile(`^\[fanout #([0-9]+)( of #([^\]]+))?\]`)

// ParseFanoutTag extracts (issue, parent) from a pane prompt that starts
// with the [fanout #N of #P] tag. ok is false when the prompt does not
// start with the tag.
func ParseFanoutTag(prompt string) (issue int, parent string, ok bool) {
	m := FanoutTagRE.FindStringSubmatch(prompt)
	if m == nil {
		return 0, "", false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, "", false
	}
	return n, m[3], true
}

// Identity is the resolved "who am I" of a fanout pane.
type Identity struct {
	Issue  int        // child issue number (state.Pane.IssueNum)
	Parent string     // parent ref: issue number string or Projects URL
	Pane   state.Pane // full state row, for slug/agent/worktree consumers
}

// Sentinel errors for the detection taxonomy; match with errors.Is.
var (
	ErrNotInTmux    = errors.New("not inside a tmux pane (TMUX_PANE is unset)")
	ErrPaneNotFound = errors.New("current tmux pane is not recorded in fanout state")
)

// IdentifyPane is the pure detection core: it resolves paneID against an
// already-loaded state store. Rows are scanned newest-first because state
// appends in launch order and tmux reuses pane ids across server restarts,
// so a stale row can share a pane id with the live pane. The matched row's
// recorded IssueNum/Parent are authoritative (cmd/fanout records them from
// the same request that built the prompt); the prompt-tag parse only fills
// fields a degenerate row is missing. A missing state file yields
// ErrPaneNotFound because state.Load returns an empty store for absent
// files.
func IdentifyPane(paneID string, st state.Store) (Identity, error) {
	if paneID == "" {
		return Identity{}, ErrNotInTmux
	}
	for _, pane := range slices.Backward(st.Panes) {
		if pane.PaneID != paneID {
			continue
		}
		id := Identity{Issue: pane.IssueNum, Parent: pane.Parent, Pane: pane}
		if id.Issue <= 0 || id.Parent == "" {
			if n, parent, ok := ParseFanoutTag(pane.Prompt); ok {
				if id.Issue <= 0 {
					id.Issue = n
				}
				if id.Parent == "" {
					id.Parent = parent
				}
			}
		}
		return id, nil
	}
	return Identity{}, fmt.Errorf("pane %s: %w", paneID, ErrPaneNotFound)
}

// Detect resolves the invoking pane's identity from the environment:
// TMUX_PANE names the pane, and the state file comes from FANOUT_STATE_PATH
// when set (same semantics as cmd/fanout) or from the owning project root's
// .fanout/state.json (OwnerProjectRoot), so detection also works from
// inside a child worktree, where the toplevel is the worktree, not the
// owner.
func Detect() (Identity, error) {
	paneID := os.Getenv("TMUX_PANE")
	if paneID == "" {
		return Identity{}, ErrNotInTmux
	}
	statePath := os.Getenv(fanoutStatePathEnv)
	if statePath != "" {
		if abs, err := filepath.Abs(statePath); err == nil {
			statePath = abs
		}
	} else {
		root, err := OwnerProjectRoot()
		if err != nil {
			return Identity{}, fmt.Errorf("detect fanout pane: %w", err)
		}
		statePath = state.Path(root)
	}
	st, err := state.Load(statePath)
	if err != nil {
		return Identity{}, fmt.Errorf("detect fanout pane: %w", err)
	}
	return IdentifyPane(paneID, st)
}

const fanoutStatePathEnv = "FANOUT_STATE_PATH"

// OwnerProjectRoot resolves the project root whose .fanout/state.json covers
// the current working directory. Fanout places child worktrees under
// <owner>/.fanout/worktrees/<slug>/ (internal/worktree), so when the current
// git toplevel sits at that path the owner is three levels up. Deriving the
// owner from the path — instead of the shared git common dir — stays correct
// when the owning checkout is itself a linked worktree, where the common dir
// points at the original checkout that holds no fanout state. Anywhere else
// the toplevel itself is the owner.
func OwnerProjectRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("current directory is not inside a git work tree")
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", fmt.Errorf("git rev-parse --show-toplevel returned an empty path")
	}
	parent := filepath.Dir(top)
	if filepath.Base(parent) == "worktrees" && filepath.Base(filepath.Dir(parent)) == ".fanout" {
		return filepath.Dir(filepath.Dir(parent)), nil
	}
	return top, nil
}
