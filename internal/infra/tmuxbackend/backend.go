// Package tmuxbackend adapts the runtime-neutral backend contract to tmuxrun.
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
// Pane decoration, popup/key bindings, layout, and session management are
// intentionally outside this adapter. They remain tmux-only UI concerns.
type Backend struct{}

var (
	_ backend.Backend     = (*Backend)(nil)
	_ backend.OwnedCloser = (*Backend)(nil)
	_ backend.FreshCloser = (*Backend)(nil)
)

// New constructs a tmux backend.
func New() *Backend {
	return &Backend{}
}

// Name returns the stable backend identifier persisted in fanout state.
func (*Backend) Name() backend.Name {
	return backend.Tmux
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
	if err := tmuxrun.LockWaitChannel(gate); err != nil {
		return backend.PaneRef{}, err
	}

	command := req.Command
	if wait := tmuxrun.WaitForLockCommand(gate); wait != "" {
		command = wait + " && " + command
	}
	paneID, err := tmuxrun.SplitPaneWithAgentCommand(req.Target, req.WorktreePath, command)
	if err != nil {
		if unlockErr := tmuxrun.UnlockWaitChannel(gate); unlockErr != nil {
			return backend.PaneRef{}, errors.Join(err, fmt.Errorf("release start gate after failed launch: %w", unlockErr))
		}
		return backend.PaneRef{}, err
	}

	return backend.PaneRef{Backend: backend.Tmux, Pane: paneID}, nil
}

// ReleaseStartGate allows a successfully launched pane to start its command.
func (*Backend) ReleaseStartGate(gate string) error {
	return tmuxrun.UnlockWaitChannel(gate)
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
	mapped := backend.CloseResult{ContainerID: result.WindowID}
	switch result.Status {
	case tmuxrun.ClosePaneClosed:
		mapped.Status = backend.CloseConfirmed
	case tmuxrun.ClosePaneStale:
		mapped.Status = backend.CloseStale
	case tmuxrun.ClosePaneFailed:
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

func tmuxPaneID(ref backend.PaneRef) (string, error) {
	if name := backend.NormalizeName(ref.Backend); name != backend.Tmux {
		return "", fmt.Errorf("tmux backend cannot use %s pane reference", name)
	}
	return ref.Pane, nil
}
