// Package tmuxbackend adapts the runtime-neutral backend contract to tmuxrun.
// It also owns fanout's window layout: the grid policy and the tmux custom
// layout string it is applied through are one mechanism, so both live behind
// the LayoutManager capability here.
package tmuxbackend

import (
	"errors"
	"fmt"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

// Backend is the tmux implementation of backend.Backend.
//
// Popups, global key shortcuts, and viewer-scoped focus are optional host
// capabilities rather than part of the base contract; their tmux delegations
// live in host.go. Session management stays outside the adapter entirely.
type Backend struct{}

var (
	_ backend.Backend         = (*Backend)(nil)
	_ backend.OwnedCloser     = (*Backend)(nil)
	_ backend.FreshCloser     = (*Backend)(nil)
	_ backend.PaneDecorator   = (*Backend)(nil)
	_ backend.LivenessStamper = (*Backend)(nil)
	_ backend.DryRunPreviewer = (*Backend)(nil)
	_ backend.LayoutManager   = (*Backend)(nil)
)

// New constructs a tmux backend.
func New() *Backend {
	return &Backend{}
}

// Name returns the stable backend identifier persisted in fanout state.
func (*Backend) Name() backend.Name {
	return backend.Tmux
}

// MutationModel reports that tmux mutations are atomic: split-window and
// kill-pane are single local calls whose result the adapter observes before
// returning, so a failed launch needs no intent journal to be reconciled.
func (*Backend) MutationModel() backend.MutationModel {
	return backend.MutationAtomic
}

// CheckAvailable verifies that the installed tmux satisfies fanout's minimum
// version requirement.
func (*Backend) CheckAvailable() error {
	return tmuxrun.CheckMinimumVersion()
}

// Launch creates one tmux pane. A start gate is acquired before the split and
// injected at the front of the pane command; the caller releases it only after
// the state consumed by the agent has been committed.
func (*Backend) Launch(req backend.LaunchRequest) (backend.PaneRef, error) {
	gate := strings.TrimSpace(req.StartGate)
	if gate != "" {
		if err := tmuxrun.LockWaitChannel(gate); err != nil {
			return backend.PaneRef{}, err
		}
	}

	command := req.Command
	if gate != "" {
		wait := tmuxrun.WaitForLockCommand(gate)
		command = wait + " && " + command
	}
	paneID, err := tmuxrun.SplitPaneWithAgentCommand(req.Target, req.WorktreePath, command)
	if err != nil {
		if gate != "" {
			if unlockErr := tmuxrun.UnlockWaitChannel(gate); unlockErr != nil {
				return backend.PaneRef{}, errors.Join(err, fmt.Errorf("release start gate after failed launch: %w", unlockErr))
			}
		}
		return backend.PaneRef{}, err
	}

	return backend.PaneRef{Backend: backend.Tmux, Pane: paneID}, nil
}

// ReleaseStartGate allows a successfully launched pane to start its command.
func (*Backend) ReleaseStartGate(gate string) error {
	return tmuxrun.UnlockWaitChannel(gate)
}

// PreviewLaunch renders the tmux commands Launch and the pane decoration that
// follows it would run, one per line and without indentation or color. The
// window re-layout is part of the preview because grid layout is a tmux-only
// step. The exact text is pinned by the Tier 2 dry-run goldens.
func (*Backend) PreviewLaunch(preview backend.LaunchPreview) []string {
	lines := []string{previewSplitWindow(preview)}
	if preview.PaneTitle != "" {
		lines = append(lines, "$ tmux select-pane -t <pane_id> -T "+backend.PreviewQuote(preview.PaneTitle))
	}
	return append(lines,
		"$ tmux set-option -p -t <pane_id> @fanout_pane_label "+backend.PreviewQuote(tmuxrun.NeutralizePaneLabel(preview.PaneLabel)),
		"$ tmux set-option -w -t <pane_id> pane-border-status top",
		"$ tmux set-option -w -t <pane_id> pane-border-format "+backend.PreviewQuote(tmuxrun.PaneBorderFormat()),
		"$ tmux set-option -w -t <pane_id> pane-active-border-style "+backend.PreviewQuote(tmuxrun.PaneActiveBorderStyle()),
		"$ tmux set-option -w -t <pane_id> pane-border-style "+backend.PreviewQuote(tmuxrun.PaneBorderStyle()),
		"# would re-layout the window: fanout grid (sidebar + comfortable-width grid),",
		"#   falling back to main-vertical then tiled",
	)
}

// previewSplitWindow renders the split that starts the agent. A gated launch
// shows the wait-for prefix in front of the agent command, exactly where
// Launch injects it.
func previewSplitWindow(preview backend.LaunchPreview) string {
	command := preview.Command
	if wait := tmuxrun.WaitForLockCommand(preview.StartGate); wait != "" {
		command = wait + " && " + command
	}
	target := ""
	if preview.Target != "" {
		target = " -t " + backend.PreviewQuote(preview.Target)
	}
	return "$ tmux split-window" + target + " -d -h -P -F '#{pane_id}' -c " +
		backend.PreviewQuote(preview.WorktreePath) + " " +
		backend.PreviewQuote(tmuxrun.BuildPaneLaunchCommand(command))
}

// ListLive returns all live tmux panes mapped to the runtime-neutral view.
func (*Backend) ListLive() ([]backend.LivePane, error) {
	panes, err := tmuxrun.ListLivePanes()
	if err != nil {
		return nil, err
	}
	return mapLivePanes(panes), nil
}

// ListLiveForIdentity performs the strict tmux identity sweep used before
// lifecycle decisions that must fail closed on incomplete pane metadata.
func (*Backend) ListLiveForIdentity() ([]backend.LivePane, error) {
	panes, err := tmuxrun.ListLivePanesForIdentity()
	if err != nil {
		return nil, err
	}
	return mapLivePanes(panes), nil
}

func mapLivePanes(panes []tmuxrun.LivePane) []backend.LivePane {
	live := make([]backend.LivePane, len(panes))
	for i, pane := range panes {
		state, _ := backend.ParseAgentState(pane.AgentState)
		live[i] = backend.LivePane{
			Ref:              backend.PaneRef{Backend: backend.Tmux, Pane: pane.ID},
			CurrentPath:      pane.CurrentPath,
			Title:            pane.Title,
			AgentState:       state,
			NativeAgentState: pane.AgentState,
			ShellKey:         pane.ShellKey,
			ProjectRoot:      pane.ProjectRoot,
			WorktreePath:     pane.WorktreePath,
			Role:             pane.Role,
			SessionID:        pane.SessionID,
		}
	}
	return live
}

// Read returns the current tmux pane output.
func (*Backend) Read(ref backend.PaneRef, lines int) (string, error) {
	paneID, err := tmuxPaneID(ref)
	if err != nil {
		return "", err
	}
	return tmuxrun.CapturePaneOutput(paneID, lines)
}

// SendLine types one literal line into a tmux pane and submits it.
func (*Backend) SendLine(ref backend.PaneRef, line string) error {
	paneID, err := tmuxPaneID(ref)
	if err != nil {
		return err
	}
	return tmuxrun.SendLiteralLine(paneID, line)
}

// Focus selects a tmux pane for the current client.
func (*Backend) Focus(ref backend.PaneRef) error {
	paneID, err := tmuxPaneID(ref)
	if err != nil {
		return err
	}
	return tmuxrun.SelectPane(paneID)
}

// Close kills a tmux pane.
func (*Backend) Close(ref backend.PaneRef) error {
	paneID, err := tmuxPaneID(ref)
	if err != nil {
		return err
	}
	return tmuxrun.KillPane(paneID)
}

// CloseOwned performs tmux's strict identity check, closes the confirmed pane,
// and verifies that it disappeared before reporting success.
func (*Backend) CloseOwned(req backend.CloseRequest) (backend.CloseResult, error) {
	paneID, err := tmuxPaneID(req.Ref)
	if err != nil {
		return backend.CloseResult{Status: backend.CloseFailed}, err
	}
	result, err := tmuxrun.ClosePaneIfOwned(paneID, req.WorktreePath, req.ShellKey)
	mapped := backend.CloseResult{Status: backend.CloseFailed, ContainerID: result.WindowID}
	switch result.Status {
	case backend.ClosePaneClosed:
		mapped.Status = backend.CloseConfirmed
	case backend.ClosePaneStale:
		mapped.Status = backend.CloseStale
	case backend.ClosePaneFailed:
		mapped.Status = backend.CloseFailed
	}
	return mapped, err
}

// CloseFresh closes and then strictly verifies a pane returned by Launch before
// fanout has stamped a durable identity on it.
func (*Backend) CloseFresh(ref backend.PaneRef) error {
	paneID, err := tmuxPaneID(ref)
	if err != nil {
		return err
	}
	return tmuxrun.CloseFreshPane(paneID)
}

// SetPaneTitle sets the tmux pane title fanout displays for a pane.
func (*Backend) SetPaneTitle(paneID, title string) error {
	return tmuxrun.SetPaneTitle(paneID, title)
}

// SetPaneLabel records the border label fanout renders on a pane's top border.
func (*Backend) SetPaneLabel(paneID, label string) error {
	return tmuxrun.SetPaneLabel(paneID, label)
}

// EnablePaneBorderTitles turns on fanout's pane-border titles and border
// styles for the window holding paneID.
func (*Backend) EnablePaneBorderTitles(paneID string) error {
	return tmuxrun.EnablePaneBorderTitles(paneID)
}

// SetPaneProjectRoot records the fanout state owner the dashboard keybinding
// reads instead of the pane's (possibly stale) current path.
func (*Backend) SetPaneProjectRoot(paneID, projectRoot string) error {
	return tmuxrun.SetPaneProjectRoot(paneID, projectRoot)
}

// SetPaneWorktreePath records the worktree a fanout pane belongs to.
func (*Backend) SetPaneWorktreePath(paneID, worktreePath string) error {
	return tmuxrun.SetPaneWorktreePath(paneID, worktreePath)
}

// StampPaneShellKey records a pane's unique liveness token, the identity every
// later close and staleness check verifies before acting on the pane.
func (*Backend) StampPaneShellKey(paneID, shellKey string) error {
	return tmuxrun.SetPaneShellKey(paneID, shellKey)
}

func tmuxPaneID(ref backend.PaneRef) (string, error) {
	if name := backend.NormalizeName(ref.Backend); name != backend.Tmux {
		return "", fmt.Errorf("tmux backend cannot use %s pane reference", name)
	}
	return ref.Pane, nil
}
