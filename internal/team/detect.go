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
	Issue  int        // peer number: child IssueNum, or synthetic for plan tasks
	TaskID string     // plan task id (state.Pane.TaskID); "" for issue panes
	Parent string     // parent ref: issue number string, Projects URL, or plan:<slug>
	Pane   state.Pane // full state row, for slug/agent/worktree consumers
}

// Sentinel errors for the detection taxonomy; match with errors.Is.
var (
	ErrNotInTmux    = errors.New("cannot detect the invoking pane: TMUX_PANE is unset and the working directory is not a fanout worktree")
	ErrPaneNotFound = errors.New("current pane is not recorded in fanout state")
)

// IdentifyPane is the pure detection core: it resolves the invoking pane
// against an already-loaded state store. worktree, when non-empty, is the
// fanout child worktree the caller runs in and is the primary key: a
// WorktreePath match identifies the managed issue/task pane even after tmux
// restarts. Shell terminal rows can share the same worktree, so the worktree
// match skips them and lets pane id fallback identify the shell only when no
// managed row owns that worktree. Rows whose recorded WorktreePath conflicts
// with the live worktree are skipped because tmux reuses pane ids across
// server restarts and such a row belongs to a dead pane. Rows are scanned
// newest-first (state appends in launch order). The matched row's recorded
// IssueNum/Parent are authoritative; the prompt-tag parse only fills fields
// a degenerate row is missing. A missing state file yields ErrPaneNotFound
// because state.Load returns an empty store for absent files.
func IdentifyPane(paneID, worktree string, st state.Store) (Identity, error) {
	if paneID == "" && worktree == "" {
		return Identity{}, ErrNotInTmux
	}
	if worktree != "" {
		for _, pane := range slices.Backward(st.Panes) {
			if pane.WorktreePath == worktree && !pane.IsShell() {
				return paneIdentity(pane), nil
			}
		}
	}
	if paneID == "" {
		return Identity{}, fmt.Errorf("worktree %s: %w", worktree, ErrPaneNotFound)
	}
	for _, pane := range slices.Backward(st.Panes) {
		if pane.PaneID != paneID {
			continue
		}
		if worktree != "" && pane.WorktreePath != "" && pane.WorktreePath != worktree {
			continue
		}
		return paneIdentity(pane), nil
	}
	return Identity{}, fmt.Errorf("pane %s: %w", paneID, ErrPaneNotFound)
}

func paneIdentity(pane state.Pane) Identity {
	id := Identity{Issue: pane.IssueNum, TaskID: pane.TaskID, Parent: pane.Parent, Pane: pane}
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
	// Plan-task panes carry a string TaskID and IssueNum 0; synthesize the
	// same stable peer number the registry seed and the
	// `fanout msg --to <task-id>` translation use, so a plan pane self-detects
	// a non-zero, message-addressable self without changing the int schema.
	if id.Issue == 0 && id.TaskID != "" && id.Parent != "" {
		id.Issue = TaskPeerNum(id.Parent, id.TaskID)
	}
	return id
}

// Detect resolves the invoking pane's identity from the environment. Two
// signals feed IdentifyPane: TMUX_PANE names the pane, and the current git
// toplevel — when it sits at the fanout child-worktree convention — names
// the worktree, which keeps detection correct across tmux server restarts
// and even works from a plain shell inside the worktree. The state file
// comes from FANOUT_STATE_PATH when set (same semantics as cmd/fanout) or
// from the owning project root's .fanout/state.json (OwnerProjectRoot).
func Detect() (Identity, error) {
	paneID := os.Getenv("TMUX_PANE")
	worktree := ""
	if top, err := gitToplevel(); err == nil {
		if _, ok := childWorktreeOwner(top); ok {
			worktree = top
		}
	}
	if paneID == "" && worktree == "" {
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
	return IdentifyPane(paneID, worktree, st)
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
	top, err := gitToplevel()
	if err != nil {
		return "", err
	}
	if owner, ok := childWorktreeOwner(top); ok {
		return owner, nil
	}
	return top, nil
}

func gitToplevel() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("current directory is not inside a git work tree")
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", fmt.Errorf("git rev-parse --show-toplevel returned an empty path")
	}
	return top, nil
}

// childWorktreeOwner reports whether top sits at the fanout child-worktree
// convention <owner>/.fanout/worktrees/<slug> (internal/worktree) and
// returns the owner root when it does.
func childWorktreeOwner(top string) (string, bool) {
	parent := filepath.Dir(top)
	if filepath.Base(parent) == "worktrees" && filepath.Base(filepath.Dir(parent)) == ".fanout" {
		return filepath.Dir(filepath.Dir(parent)), true
	}
	return "", false
}
