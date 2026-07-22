package herdrrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

var ErrOwnedIdentityMismatch = errors.New("herdr owned pane identity mismatch")

type OwnedPaneIdentity struct {
	Ref            corebackend.PaneRef
	SessionID      string
	SocketPath     string
	WorkspaceLabel string
	TerminalID     string
	RepoKey        string
	WorktreePath   string
	CurrentPath    string
	AgentID        string
	AgentSession   *corebackend.AgentSessionRef
}

type ReadRequest struct {
	Target OwnedPaneIdentity
	Lines  int
}

type SendLineRequest struct {
	Target OwnedPaneIdentity
	Line   string
}

type FocusRequest struct {
	Target OwnedPaneIdentity
}

type ClosePaneRequest struct {
	Target OwnedPaneIdentity
}

type OwnedCloseRequest struct {
	Target                 OwnedPaneIdentity
	WorktreeOwnershipNonce string
	WorktreeGitDir         string
	Force                  bool
}

type ownedTargetAdmission struct {
	target           OwnedPaneIdentity
	closeRequest     *OwnedCloseRequest
	closeFingerprint corebackend.CloseRequest
}

func (b *Backend) BindOwnedTarget(target OwnedPaneIdentity) (*Backend, error) {
	return b.bindOwnedTarget(target, nil)
}

func (b *Backend) BindOwnedClose(req OwnedCloseRequest) (*Backend, error) {
	if req.Force {
		return nil, fmt.Errorf("herdr core owned-close admission cannot bind force")
	}
	copy := cloneOwnedCloseRequest(req)
	return b.bindOwnedTarget(copy.Target, &copy)
}

func (b *Backend) bindOwnedTarget(target OwnedPaneIdentity, closeRequest *OwnedCloseRequest) (*Backend, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*commandTimeout)
	defer cancel()
	admission, lock, err := b.acquireOwnedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer unlockPrivateFile(lock)
	target = cloneOwnedPaneIdentity(target)
	if err := validateSavedTarget(target, admission); err != nil {
		return nil, err
	}
	if _, _, err := b.resolveOwnedTarget(ctx, admission, target); err != nil {
		return nil, err
	}
	targetAdmission := &ownedTargetAdmission{target: target}
	if closeRequest != nil {
		copy := cloneOwnedCloseRequest(*closeRequest)
		if err := verifyWorktreeOwnership(copy); err != nil {
			return nil, err
		}
		targetAdmission.closeRequest = &copy
		targetAdmission.closeFingerprint = corebackend.CloseRequest{Ref: target.Ref, WorktreePath: target.WorktreePath, ShellKey: target.TerminalID}
	}
	return b.cloneWithTarget(targetAdmission), nil
}

func (b *Backend) cloneWithTarget(target *ownedTargetAdmission) *Backend {
	clone := &Backend{
		session: b.session, socketPath: b.socketPath, probeGate: make(chan struct{}, 1),
		lookPath: b.lookPath, hashFile: b.hashFile, output: b.output, helpOutput: b.helpOutput,
		now: b.now, sleep: b.sleep, admitted: map[string]binaryAdmission{}, target: target,
	}
	maps.Copy(clone.admitted, b.admitted)
	if b.control != nil {
		control := *b.control
		clone.control = &control
	}
	if b.owner != nil {
		owner := *b.owner
		clone.owner = &owner
	}
	return clone
}

func (b *Backend) boundOwnedTarget(ref corebackend.PaneRef, operation string) (OwnedPaneIdentity, error) {
	if b == nil || b.target == nil {
		return OwnedPaneIdentity{}, corebackend.Unsupported(corebackend.Herdr, operation+" without an immutable target admission")
	}
	if ref != b.target.target.Ref {
		return OwnedPaneIdentity{}, fmt.Errorf("%w: %s reference does not match immutable admission", ErrOwnedIdentityMismatch, operation)
	}
	return cloneOwnedPaneIdentity(b.target.target), nil
}

func (b *Backend) ReadOwned(ctx context.Context, req ReadRequest) (string, error) {
	return b.readOwned(ctx, cloneOwnedPaneIdentity(req.Target), req.Lines)
}

func (b *Backend) readCore(ref corebackend.PaneRef, lines int) (string, error) {
	target, err := b.boundOwnedTarget(ref, "read")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*commandTimeout)
	defer cancel()
	return b.readOwned(ctx, target, lines)
}

func (b *Backend) readOwned(ctx context.Context, target OwnedPaneIdentity, lines int) (string, error) {
	if lines < 0 {
		return "", fmt.Errorf("herdr read lines must be non-negative")
	}
	admission, lock, err := b.acquireOwnedOperation(ctx)
	if err != nil {
		return "", err
	}
	defer unlockPrivateFile(lock)
	target, probed, err := b.resolveOwnedTarget(ctx, admission, target)
	if err != nil {
		return "", err
	}
	args := []string{"pane", "read", target.Ref.Pane}
	if lines == 0 {
		args = append(args, "--source", "visible")
	} else {
		args = append(args, "--source", "recent-unwrapped", "--lines", strconv.Itoa(lines))
	}
	args = append(args, "--format", "text")
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, args...)
	if err != nil {
		return "", fmt.Errorf("herdr pane read: %w", err)
	}
	if err := b.verifyOwnedTargetAfter(ctx, admission, target); err != nil {
		return "", fmt.Errorf("discard herdr pane read result: %w", err)
	}
	return string(out), nil
}

func (b *Backend) SendLineOwned(ctx context.Context, req SendLineRequest) error {
	return b.sendLineOwned(ctx, cloneOwnedPaneIdentity(req.Target), req.Line)
}

func (b *Backend) sendLineCore(ref corebackend.PaneRef, line string) error {
	target, err := b.boundOwnedTarget(ref, "send line")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*commandTimeout)
	defer cancel()
	return b.sendLineOwned(ctx, target, line)
}

func (b *Backend) sendLineOwned(ctx context.Context, target OwnedPaneIdentity, line string) error {
	if strings.ContainsAny(line, "\x00\r\n") {
		return fmt.Errorf("herdr send line contains a NUL, CR, or LF byte")
	}
	if target.AgentID == "" || target.AgentSession == nil {
		return fmt.Errorf("%w: send line requires a saved live-agent identity", ErrOwnedIdentityMismatch)
	}
	admission, lock, err := b.acquireOwnedOperation(ctx)
	if err != nil {
		return err
	}
	defer unlockPrivateFile(lock)
	target, probed, err := b.resolveOwnedTarget(ctx, admission, target)
	if err != nil {
		return err
	}
	if _, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, "agent", "prompt", target.Ref.Pane, line); err != nil {
		return fmt.Errorf("herdr agent prompt (not retried): %w", err)
	}
	if err := b.verifyOwnedTargetAfter(ctx, admission, target); err != nil {
		return fmt.Errorf("verify herdr pane after prompt: %w", err)
	}
	return nil
}

func (b *Backend) FocusOwned(ctx context.Context, req FocusRequest) error {
	return b.focusOwned(ctx, cloneOwnedPaneIdentity(req.Target))
}

func (b *Backend) focusCore(ref corebackend.PaneRef) error {
	target, err := b.boundOwnedTarget(ref, "focus")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*commandTimeout)
	defer cancel()
	return b.focusOwned(ctx, target)
}

func (b *Backend) focusOwned(ctx context.Context, target OwnedPaneIdentity) error {
	admission, lock, err := b.acquireOwnedOperation(ctx)
	if err != nil {
		return err
	}
	defer unlockPrivateFile(lock)
	target, probed, err := b.resolveOwnedTarget(ctx, admission, target)
	if err != nil {
		return err
	}
	if _, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, "workspace", "focus", target.Ref.Workspace); err != nil {
		return fmt.Errorf("herdr workspace focus (not retried): %w", err)
	}
	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return err
	}
	current, ok := view.find(target.Ref)
	if !ok || !ownedPaneMatches(target, current) || !current.workspaceFocused {
		return fmt.Errorf("%w: workspace focus did not preserve the admitted target", ErrOwnedIdentityMismatch)
	}
	return nil
}

func (b *Backend) ClosePaneOwned(ctx context.Context, req ClosePaneRequest) error {
	return b.closePaneOwned(ctx, cloneOwnedPaneIdentity(req.Target))
}

func (b *Backend) closeCore(ref corebackend.PaneRef) error {
	target, err := b.boundOwnedTarget(ref, "close pane")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*commandTimeout)
	defer cancel()
	return b.closePaneOwned(ctx, target)
}

func (b *Backend) closePaneOwned(ctx context.Context, target OwnedPaneIdentity) error {
	admission, lock, err := b.acquireOwnedOperation(ctx)
	if err != nil {
		return err
	}
	defer unlockPrivateFile(lock)
	target, probed, err := b.resolveOwnedTarget(ctx, admission, target)
	if err != nil {
		return err
	}
	if _, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, "pane", "close", target.Ref.Pane); err != nil {
		return fmt.Errorf("herdr pane close (not retried): %w", err)
	}
	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return err
	}
	if current, ok := view.find(target.Ref); ok {
		if ownedPaneMatches(target, current) {
			return fmt.Errorf("herdr pane close returned success but target remains live")
		}
		return fmt.Errorf("%w: pane id was reused after close", ErrOwnedIdentityMismatch)
	}
	return nil
}

func (b *Backend) CloseOwned(req corebackend.CloseRequest) (corebackend.CloseResult, error) {
	failed := corebackend.CloseResult{Status: corebackend.CloseFailed}
	if b == nil || b.target == nil || b.target.closeRequest == nil {
		return failed, corebackend.Unsupported(corebackend.Herdr, "owned close without an immutable target admission")
	}
	if req != b.target.closeFingerprint {
		return failed, fmt.Errorf("%w: close request does not match immutable admission", ErrOwnedIdentityMismatch)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*commandTimeout)
	defer cancel()
	return b.closeOwnedSession(ctx, cloneOwnedCloseRequest(*b.target.closeRequest))
}

func (b *Backend) CloseOwnedSession(ctx context.Context, req OwnedCloseRequest) (corebackend.CloseResult, error) {
	return b.closeOwnedSession(ctx, cloneOwnedCloseRequest(req))
}

func (b *Backend) closeOwnedSession(ctx context.Context, req OwnedCloseRequest) (corebackend.CloseResult, error) {
	failed := corebackend.CloseResult{Status: corebackend.CloseFailed}
	if err := verifyWorktreeOwnership(req); err != nil {
		return failed, err
	}
	admission, lock, err := b.acquireOwnedOperation(ctx)
	if err != nil {
		return failed, err
	}
	defer unlockPrivateFile(lock)
	target, probed, err := b.resolveOwnedTarget(ctx, admission, req.Target)
	if err != nil {
		return failed, err
	}
	if err := verifyWorktreeOwnership(req); err != nil {
		return failed, err
	}
	args := []string{"worktree", "remove", "--workspace", target.Ref.Workspace}
	if req.Force {
		args = append(args, "--force")
	}
	args = append(args, "--json")
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, args...)
	if err != nil {
		return failed, fmt.Errorf("herdr worktree remove (not retried): %w", err)
	}
	if err := validateWorktreeRemoved(out, target, req.Force); err != nil {
		return failed, err
	}
	if _, err := os.Lstat(target.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		return failed, fmt.Errorf("herdr worktree remove returned success but checkout still exists")
	}
	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return failed, err
	}
	if !view.workspacePresent(target.Ref.Workspace) {
		return corebackend.CloseResult{Status: corebackend.CloseConfirmed}, nil
	}
	workspace := view.workspaces[target.Ref.Workspace]
	if workspace.label != target.WorkspaceLabel || workspace.worktreePath != "" {
		return failed, fmt.Errorf("%w: residual workspace changed after worktree removal", ErrOwnedIdentityMismatch)
	}
	probed, err = b.probeOwned(ctx, admission)
	if err != nil {
		return failed, err
	}
	if _, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, "workspace", "close", target.Ref.Workspace); err != nil {
		return failed, fmt.Errorf("herdr workspace close (not retried): %w", err)
	}
	view, err = b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return failed, err
	}
	if view.workspacePresent(target.Ref.Workspace) {
		return failed, fmt.Errorf("herdr workspace close returned success but workspace remains live")
	}
	return corebackend.CloseResult{Status: corebackend.CloseConfirmed}, nil
}

type ownedSnapshotView struct {
	panes      map[corebackend.PaneRef]ownedPaneView
	workspaces map[string]ownedWorkspaceView
}

type ownedWorkspaceView struct {
	label        string
	focused      bool
	repoKey      string
	worktreePath string
}

type ownedPaneView struct {
	identity         OwnedPaneIdentity
	workspaceFocused bool
}

func (b *Backend) ownedSnapshotView(ctx context.Context, admission ownedAdmission) (ownedSnapshotView, error) {
	probed, err := b.probeOwned(ctx, admission)
	if err != nil {
		return ownedSnapshotView{}, err
	}
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, "api", "snapshot")
	if err != nil {
		return ownedSnapshotView{}, err
	}
	var envelope snapshotEnvelope
	if err := decodeOne(out, &envelope); err != nil {
		return ownedSnapshotView{}, err
	}
	panes, err := projectSnapshot(envelope, probed)
	if err != nil {
		return ownedSnapshotView{}, err
	}
	view := ownedSnapshotView{panes: map[corebackend.PaneRef]ownedPaneView{}, workspaces: map[string]ownedWorkspaceView{}}
	for _, workspace := range *envelope.Result.Snapshot.Workspaces {
		worktreePath, repoKey := "", ""
		if workspace.Worktree != nil {
			worktreePath, repoKey = workspace.Worktree.CheckoutPath, workspace.Worktree.RepoKey
		}
		view.workspaces[workspace.WorkspaceID] = ownedWorkspaceView{label: workspace.Label, focused: workspace.Focused != nil && *workspace.Focused, repoKey: repoKey, worktreePath: worktreePath}
	}
	for _, pane := range panes {
		workspace := view.workspaces[pane.Ref.Workspace]
		identity := OwnedPaneIdentity{
			Ref: pane.Ref, SessionID: pane.SessionID, SocketPath: pane.SocketPath,
			WorkspaceLabel: workspace.label, TerminalID: pane.TerminalID, RepoKey: pane.RepoKey,
			WorktreePath: pane.WorktreePath, CurrentPath: pane.CurrentPath, AgentID: pane.AgentID,
			AgentSession: cloneAgentSession(pane.AgentSession),
		}
		view.panes[pane.Ref] = ownedPaneView{identity: identity, workspaceFocused: workspace.focused}
	}
	return view, nil
}

func (v ownedSnapshotView) find(ref corebackend.PaneRef) (ownedPaneView, bool) {
	pane, ok := v.panes[ref]
	return pane, ok
}

func (v ownedSnapshotView) workspacePresent(id string) bool {
	_, ok := v.workspaces[id]
	return ok
}

func (b *Backend) resolveOwnedTarget(ctx context.Context, admission ownedAdmission, expected OwnedPaneIdentity) (OwnedPaneIdentity, probeResult, error) {
	if err := validateSavedTarget(expected, admission); err != nil {
		return OwnedPaneIdentity{}, probeResult{}, err
	}
	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return OwnedPaneIdentity{}, probeResult{}, err
	}
	current, ok := view.find(expected.Ref)
	if !ok || !ownedPaneMatches(expected, current) {
		return OwnedPaneIdentity{}, probeResult{}, fmt.Errorf("%w: saved target is not live", ErrOwnedIdentityMismatch)
	}
	probed, err := b.probeOwned(ctx, admission)
	return cloneOwnedPaneIdentity(expected), probed, err
}

func (b *Backend) verifyOwnedTargetAfter(ctx context.Context, admission ownedAdmission, target OwnedPaneIdentity) error {
	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return err
	}
	current, ok := view.find(target.Ref)
	if !ok || !ownedPaneMatches(target, current) {
		return ErrOwnedIdentityMismatch
	}
	return nil
}

func validateSavedTarget(target OwnedPaneIdentity, admission ownedAdmission) error {
	if target.Ref.Backend != corebackend.Herdr || target.Ref.Workspace == "" || target.Ref.Pane == "" ||
		target.SessionID != admission.marker.Session || target.SocketPath != admission.marker.SocketPath ||
		target.WorkspaceLabel == "" || target.TerminalID == "" || target.CurrentPath == "" {
		return fmt.Errorf("%w: saved target is incomplete or belongs to a foreign route", ErrOwnedIdentityMismatch)
	}
	if (target.RepoKey == "") != (target.WorktreePath == "") {
		return fmt.Errorf("%w: saved worktree provenance is incomplete", ErrOwnedIdentityMismatch)
	}
	return nil
}

func ownedPaneMatches(expected OwnedPaneIdentity, current ownedPaneView) bool {
	return equalOwnedPane(expected, current.identity)
}

func equalOwnedPane(left, right OwnedPaneIdentity) bool {
	if left.Ref != right.Ref || left.SessionID != right.SessionID || left.SocketPath != right.SocketPath ||
		left.WorkspaceLabel != right.WorkspaceLabel || left.TerminalID != right.TerminalID || left.RepoKey != right.RepoKey ||
		left.WorktreePath != right.WorktreePath || left.CurrentPath != right.CurrentPath || left.AgentID != right.AgentID {
		return false
	}
	if left.AgentSession == nil || right.AgentSession == nil {
		return left.AgentSession == nil && right.AgentSession == nil
	}
	return *left.AgentSession == *right.AgentSession
}

func cloneOwnedPaneIdentity(target OwnedPaneIdentity) OwnedPaneIdentity {
	target.AgentSession = cloneAgentSession(target.AgentSession)
	return target
}

func cloneAgentSession(ref *corebackend.AgentSessionRef) *corebackend.AgentSessionRef {
	if ref == nil {
		return nil
	}
	copy := *ref
	return &copy
}

func cloneOwnedCloseRequest(req OwnedCloseRequest) OwnedCloseRequest {
	req.Target = cloneOwnedPaneIdentity(req.Target)
	return req
}

const worktreeOwnershipMarkerName = "fanout-herdr-worktree-owner.json"

type worktreeOwnershipMarker struct {
	Nonce        string `json:"nonce"`
	WorkspaceID  string `json:"workspace_id"`
	RepoKey      string `json:"repo_key"`
	CheckoutPath string `json:"checkout_path"`
	GitDir       string `json:"git_dir"`
}

func verifyWorktreeOwnership(req OwnedCloseRequest) error {
	target := req.Target
	if !validHexToken(req.WorktreeOwnershipNonce) || target.WorkspaceLabel != req.WorktreeOwnershipNonce || target.RepoKey == "" || target.WorktreePath == "" {
		return fmt.Errorf("%w: owned close requires matching worktree ownership", ErrOwnedIdentityMismatch)
	}
	paths := []struct {
		description string
		path        string
	}{
		{description: "repo key", path: target.RepoKey},
		{description: "checkout", path: target.WorktreePath},
		{description: "git dir", path: req.WorktreeGitDir},
	}
	for _, candidate := range paths {
		description, path := candidate.description, candidate.path
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("%w: %s path is not canonical", ErrOwnedIdentityMismatch, description)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			return fmt.Errorf("%w: %s path does not resolve to its saved identity", ErrOwnedIdentityMismatch, description)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s path is not a real directory", ErrOwnedIdentityMismatch, description)
		}
		if err := validateOwnerUID(path, info); err != nil {
			return err
		}
	}
	markerPath := filepath.Join(req.WorktreeGitDir, worktreeOwnershipMarkerName)
	f, err := os.OpenFile(markerPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("verify herdr worktree ownership marker: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if err := validatePrivateRegular(markerPath, info); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxOwnerMarkerBytes+1))
	if err != nil {
		return fmt.Errorf("read herdr worktree ownership marker: %w", err)
	}
	if len(data) > maxOwnerMarkerBytes {
		return fmt.Errorf("herdr worktree ownership marker exceeds %d bytes", maxOwnerMarkerBytes)
	}
	var marker worktreeOwnershipMarker
	if err := decodeStrictCanonical(data, &marker); err != nil {
		return err
	}
	want := worktreeOwnershipMarker{Nonce: req.WorktreeOwnershipNonce, WorkspaceID: target.Ref.Workspace, RepoKey: target.RepoKey, CheckoutPath: target.WorktreePath, GitDir: req.WorktreeGitDir}
	if marker != want {
		return fmt.Errorf("%w: worktree ownership marker does not match saved identity", ErrOwnedIdentityMismatch)
	}
	return nil
}

func validateWorktreeRemoved(data []byte, target OwnedPaneIdentity, force bool) error {
	var response struct {
		ID     string `json:"id"`
		Result *struct {
			Type        string `json:"type"`
			WorkspaceID string `json:"workspace_id"`
			Path        string `json:"path"`
			Forced      bool   `json:"forced"`
		} `json:"result"`
	}
	if err := decodeOne(data, &response); err != nil {
		return fmt.Errorf("parse herdr worktree remove response: %w", err)
	}
	if response.ID != "cli:worktree:remove" || response.Result == nil || response.Result.Type != "worktree_removed" || response.Result.WorkspaceID != target.Ref.Workspace || response.Result.Path != target.WorktreePath || response.Result.Forced != force {
		return fmt.Errorf("herdr worktree remove response does not match admitted target")
	}
	return nil
}
