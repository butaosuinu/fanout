package herdrrun

import (
	"cmp"
	"context"
	"fmt"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

type ownedSnapshotView struct {
	panes      map[corebackend.PaneRef]ownedPaneView
	workspaces map[string]ownedWorkspaceView
}

type ownedWorkspaceView struct {
	label        string
	repoKey      string
	repoRoot     string
	worktreePath string
	isLinked     bool
}

type ownedPaneView struct {
	identity     corebackend.OwnedPaneIdentity
	paneFocused  bool
	agentPresent bool
	// agentUnnamed marks a pane whose agent record the runtime holds without a
	// name of its own, which is how it leaves the record after a provider
	// restarts its conversation in place.
	agentUnnamed bool
}

func (c ownedCall) ownedSnapshotView(ctx context.Context) (ownedSnapshotView, error) {
	probed, err := c.probe(ctx)
	if err != nil {
		return ownedSnapshotView{}, err
	}
	out, err := c.b.runReadContext(ctx, probed.binary, probed.route, "api", "snapshot")
	if err != nil {
		return ownedSnapshotView{}, readMethodError("session.snapshot", err)
	}
	var envelope snapshotEnvelope
	err = decodeOne(out, &envelope)
	if err != nil {
		return ownedSnapshotView{}, methodUnavailable("session.snapshot")
	}
	panes, err := projectSnapshot(envelope, probed)
	if err != nil {
		return ownedSnapshotView{}, methodUnavailable("session.snapshot")
	}
	workspaces := ownedWorkspaceViews(envelope)
	return ownedSnapshotView{panes: ownedPaneViews(panes, workspaces), workspaces: workspaces}, nil
}

// ownedWorkspaceViews projects the snapshot's workspaces into the ownership
// label and checkout provenance every pane comparison reads off them.
func ownedWorkspaceViews(envelope snapshotEnvelope) map[string]ownedWorkspaceView {
	workspaces := map[string]ownedWorkspaceView{}
	for _, workspace := range *envelope.Result.Snapshot.Workspaces {
		worktreePath, repoKey, repoRoot := "", "", ""
		isLinked := false
		if workspace.Worktree != nil {
			worktreePath, repoKey = workspace.Worktree.CheckoutPath, workspace.Worktree.RepoKey
			repoRoot = workspace.Worktree.RepoRoot
			isLinked = workspace.Worktree.IsLinked
		}
		workspaces[workspace.WorkspaceID] = ownedWorkspaceView{
			label: workspace.Label, repoKey: repoKey, repoRoot: repoRoot, worktreePath: worktreePath, isLinked: isLinked,
		}
	}
	return workspaces
}

// ownedPaneViews projects each observed pane into the identity shape a saved
// target is compared against, carrying its workspace's ownership label.
func ownedPaneViews(
	panes []corebackend.LivePane,
	workspaces map[string]ownedWorkspaceView,
) map[corebackend.PaneRef]ownedPaneView {
	views := map[corebackend.PaneRef]ownedPaneView{}
	for _, pane := range panes {
		identity := corebackend.OwnedPaneIdentity{
			Ref: pane.Ref, SessionID: pane.SessionID, SocketPath: pane.SocketPath,
			WorkspaceLabel: workspaces[pane.Ref.Workspace].label,
			TerminalID:     pane.TerminalID, RepoKey: pane.RepoKey,
			WorktreePath: pane.WorktreePath, CurrentPath: pane.CurrentPath,
			Agent: pane.AgentProvider, AgentID: pane.AgentID,
			AgentSession: cloneAgentSession(pane.AgentSession),
		}
		views[pane.Ref] = ownedPaneView{
			identity: identity, paneFocused: pane.FocusKnown && pane.Focused,
			agentPresent: pane.AgentPresent,
			agentUnnamed: pane.AgentPresent && !pane.AgentNamed,
		}
	}
	return views
}

func (v ownedSnapshotView) find(ref corebackend.PaneRef) (ownedPaneView, bool) {
	pane, ok := v.panes[ref]
	return pane, ok
}

func (v ownedSnapshotView) workspacePresent(id string) bool {
	_, ok := v.workspaces[id]
	return ok
}

func (v ownedSnapshotView) workspaceContainsOnly(target corebackend.PaneRef) bool {
	for ref := range v.panes {
		if ref.Workspace == target.Workspace && ref != target {
			return false
		}
	}
	return true
}

func (v ownedSnapshotView) paneLessAttachedWorkspaceMatches(expected corebackend.OwnedPaneIdentity) bool {
	workspace, ok := v.workspaces[expected.Ref.Workspace]
	if !ok || workspace.label != expected.WorkspaceLabel || !attachedWorkspaceCheckoutMatches(workspace, expected) {
		return false
	}
	for _, pane := range v.panes {
		if pane.identity.Ref.Workspace == expected.Ref.Workspace || pane.identity.TerminalID == expected.TerminalID {
			return false
		}
	}
	return true
}

func attachedWorkspaceCheckoutMatches(workspace ownedWorkspaceView, expected corebackend.OwnedPaneIdentity) bool {
	if expected.RepoKey == "" && workspace.repoKey == "" && workspace.worktreePath == "" {
		return true // A generic pane-less workspace has no cwd or Git metadata.
	}
	return sameOwnedCheckout(expected, corebackend.OwnedPaneIdentity{
		RepoKey: workspace.repoKey, WorktreePath: workspace.worktreePath, CurrentPath: workspace.worktreePath,
	})
}

func (c ownedCall) resolveOwnedTarget(ctx context.Context, expected corebackend.OwnedPaneIdentity) (corebackend.OwnedPaneIdentity, probeResult, error) {
	target, probed, _, err := c.resolveOwnedTargetView(ctx, expected)
	return target, probed, err
}

func (c ownedCall) resolveOwnedTargetView(
	ctx context.Context,
	expected corebackend.OwnedPaneIdentity,
) (corebackend.OwnedPaneIdentity, probeResult, ownedSnapshotView, error) {
	if err := validateSavedTarget(expected, c.admission); err != nil {
		return corebackend.OwnedPaneIdentity{}, probeResult{}, ownedSnapshotView{}, err
	}
	view, err := c.ownedSnapshotView(ctx)
	if err != nil {
		return corebackend.OwnedPaneIdentity{}, probeResult{}, ownedSnapshotView{}, err
	}
	current, ok := view.find(expected.Ref)
	if !ok || !ownedPaneMatches(expected, current) {
		return corebackend.OwnedPaneIdentity{}, probeResult{}, ownedSnapshotView{}, fmt.Errorf("%w: saved target is not live", corebackend.ErrOwnedIdentityMismatch)
	}
	probed, err := c.probe(ctx)
	return cloneOwnedPaneIdentity(expected), probed, view, err
}

func (c ownedCall) verifyOwnedTargetAfter(ctx context.Context, target corebackend.OwnedPaneIdentity) error {
	view, err := c.ownedSnapshotView(ctx)
	if err != nil {
		return err
	}
	current, ok := view.find(target.Ref)
	if !ok || !ownedPaneMatches(target, current) {
		return corebackend.ErrOwnedIdentityMismatch
	}
	return nil
}

func validateSavedTarget(target corebackend.OwnedPaneIdentity, admission ownedAdmission) error {
	if target.Ref.Backend != corebackend.Herdr || target.Ref.Workspace == "" || target.Ref.Pane == "" ||
		target.SessionID != admission.marker.Session || target.SocketPath != admission.marker.SocketPath ||
		target.WorkspaceLabel == "" || target.TerminalID == "" || target.CurrentPath == "" {
		return fmt.Errorf("%w: saved target is incomplete or belongs to a foreign route", corebackend.ErrOwnedIdentityMismatch)
	}
	if (target.RepoKey == "") != (target.WorktreePath == "") {
		return fmt.Errorf("%w: saved worktree provenance is incomplete", corebackend.ErrOwnedIdentityMismatch)
	}
	return nil
}

// ownedPaneMatches admits the exact recorded identity, plus the one shape that
// is this row's pane without carrying its agent name: the runtime holds the
// record unnamed and the row recorded a name fanout minted. It mirrors core's
// observedAgentMatches, so a row the display treats as live is one the owned
// operations act on rather than refuse. Mutations put the name back first.
func ownedPaneMatches(expected corebackend.OwnedPaneIdentity, current ownedPaneView) bool {
	if equalOwnedPane(expected, current.identity) {
		return true
	}
	return corebackend.AgentRecordMatches(
		current.identity.AgentID, !current.agentUnnamed, expected.AgentID,
	) && ownedPaneMatchesExceptAgentName(expected, current.identity)
}

// equalOwnedPane is the fence every owned read and mutation resolves through.
// Route, checkout, and agent record compare exactly; only the conversation
// admits the provider's current one rather than freezing the first observed id,
// and AgentID is per-launch, so the pane stays fenced to this launch.
func equalOwnedPane(left, right corebackend.OwnedPaneIdentity) bool {
	return ownedPaneMatchesExceptAgentName(left, right) && left.AgentID == right.AgentID
}

// ownedPaneMatchesExceptAgentName is equalOwnedPane with the agent record's
// name set aside. That name is the one component the runtime drops on its own,
// so restoring it needs a comparison that holds everything else exact.
func ownedPaneMatchesExceptAgentName(left, right corebackend.OwnedPaneIdentity) bool {
	return sameOwnedRoute(left, right) && sameOwnedCheckout(left, right) &&
		left.Agent == right.Agent &&
		corebackend.AgentSessionAdmits(left.Agent, left.AgentSession, right.AgentSession)
}

// sameOwnedRoute compares where the pane lives: the addressed pane, the server
// it is on, and the workspace and terminal records that pin it there.
func sameOwnedRoute(left, right corebackend.OwnedPaneIdentity) bool {
	return left.Ref == right.Ref && left.SessionID == right.SessionID &&
		left.SocketPath == right.SocketPath && left.WorkspaceLabel == right.WorkspaceLabel &&
		left.TerminalID == right.TerminalID
}

// sameOwnedCheckout compares the checkout the pane owns and works in.
func sameOwnedCheckout(left, right corebackend.OwnedPaneIdentity) bool {
	if left.RepoKey == "" {
		return corebackend.CheckoutMatchesLive(left.RepoKey, cmp.Or(left.WorktreePath, left.CurrentPath), corebackend.LivePane{
			WorktreePath: right.WorktreePath, CurrentPath: right.CurrentPath,
		})
	}
	// Recorded checkouts retain the owned-operation fence's byte-exact cwd comparison.
	return left.RepoKey == right.RepoKey && left.WorktreePath == right.WorktreePath &&
		left.CurrentPath == right.CurrentPath
}

func cloneOwnedPaneIdentity(target corebackend.OwnedPaneIdentity) corebackend.OwnedPaneIdentity {
	target.AgentSession = cloneAgentSession(target.AgentSession)
	return target
}

func cloneAgentSession(ref *corebackend.AgentSessionRef) *corebackend.AgentSessionRef {
	if ref == nil {
		return nil
	}
	cloned := *ref
	return &cloned
}
