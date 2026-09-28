package herdrrun

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type coordinatorCloser struct {
	*boundBackend
	session *OwnedSession
}

// BindOwnedCoordinatorClose binds a saved tokenless coordinator intent. The
// caller holds the journal lock and records close-pending before CloseOwned.
// A launcher EOF removes only this workspace; workspace/pane close can close
// every workspace in the repository group on Herdr 0.8.2.
func (s *OwnedSession) BindOwnedCoordinatorClose(intent state.LaunchIntent) (corebackend.OwnedClosingBackend, error) {
	identity := []bool{
		intent.Kind == state.IntentCoordinator, intent.Status == state.IntentRealized, intent.Launch == nil,
		intent.Resource.RepoKey == "", intent.Resource.RepoRoot == "", intent.BranchName == "",
		intent.WorkspaceLabel == intent.Resource.Label, intent.WorktreePath == intent.Resource.CurrentPath,
	}
	if slices.Contains(identity, false) {
		return nil, fmt.Errorf("%w: close requires a realized tokenless coordinator intent", corebackend.ErrOwnedIdentityMismatch)
	}
	if s == nil || s.backend == nil {
		return nil, fmt.Errorf("herdr owned session is nil")
	}
	bound, err := s.backend.bindOwnedWorkspaceClose(corebackend.OwnedPaneIdentity{
		Ref:       corebackend.PaneRef{Backend: corebackend.Herdr, Workspace: intent.Resource.WorkspaceID, Pane: intent.Resource.PaneID},
		SessionID: intent.Session, SocketPath: intent.SocketPath, WorkspaceLabel: intent.Resource.Label,
		TerminalID: intent.Resource.TerminalID, CurrentPath: intent.WorktreePath,
	})
	if err != nil {
		return nil, err
	}
	return &coordinatorCloser{boundBackend: bound, session: s}, nil
}

func (c *coordinatorCloser) CloseOwned(req corebackend.CloseRequest) (corebackend.CloseResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*commandTimeout)
	defer cancel()
	return c.close(ctx, req)
}

func (c *coordinatorCloser) close(ctx context.Context, req corebackend.CloseRequest) (corebackend.CloseResult, error) {
	failed := corebackend.CloseResult{Status: corebackend.CloseFailed}
	if req != c.target.closeFingerprint {
		return failed, fmt.Errorf("%w: %w", corebackend.ErrOwnedMutationNotIssued, corebackend.ErrOwnedIdentityMismatch)
	}
	notIssued := func(err error) error { return fmt.Errorf("%w: %w", corebackend.ErrOwnedMutationNotIssued, err) }
	err := c.withOwned(ctx, ownedMutationLane, sameOwnedErrors(notIssued), func(call ownedCall) error {
		probed, err := c.admitLauncherEOF(ctx, call)
		if err != nil {
			return notIssued(err)
		}
		callCtx, cancel := context.WithTimeout(ctx, commandTimeout)
		defer cancel()
		if _, err := c.runWorktreeMutation(callCtx, probed.binary, probed.route, "pane", "send-keys", c.target.target.Ref.Pane, "ctrl+d"); err != nil {
			if errors.Is(err, corebackend.ErrMutationNotIssued) {
				return notIssued(err)
			}
			return err
		}
		return c.waitCoordinatorAbsent(ctx, call)
	})
	if err != nil {
		return failed, err
	}
	return corebackend.CloseResult{Status: corebackend.CloseConfirmed}, nil
}

func (c *coordinatorCloser) admitLauncherEOF(ctx context.Context, call ownedCall) (probeResult, error) {
	if processErr := c.verifyLauncherEOFProcess(ctx, call.probed, call.admission.marker.LauncherPath); processErr != nil {
		return probeResult{}, processErr
	}
	target, probed, view, err := call.resolveOwnedTargetView(ctx, c.target.target)
	if err != nil {
		return probeResult{}, err
	}
	if err := verifyGenericWorkspaceCheckout(view, target); err != nil {
		return probeResult{}, err
	}
	if view.panes[target.Ref].agentPresent {
		return probeResult{}, fmt.Errorf("%w: coordinator has an agent record", corebackend.ErrOwnedIdentityMismatch)
	}
	return probed, verifyWorkspaceClosePanes(view, target.Ref)
}

func (c *coordinatorCloser) verifyLauncherEOFProcess(ctx context.Context, probed probeResult, launcherPath string) error {
	target := c.target.target
	info, err := c.session.processInfoProbed(ctx, probed, target.Ref.Pane, commandTimeout)
	if err != nil {
		return err
	}
	if len(info.ForegroundProcesses) != 1 {
		return fmt.Errorf("%w: coordinator must contain only the idle launcher", corebackend.ErrOwnedIdentityMismatch)
	}
	return corebackend.VerifyLauncherProcess(info, target.CurrentPath, launcherPath)
}

func (c *coordinatorCloser) waitCoordinatorAbsent(ctx context.Context, call ownedCall) error {
	for {
		view, err := call.ownedSnapshotView(ctx)
		if err != nil {
			return err
		}
		if !view.workspacePresent(c.target.target.Ref.Workspace) {
			return nil
		}
		if err := c.sleep(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
}
