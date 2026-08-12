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

var (
	ErrOwnedIdentityMismatch = errors.New("herdr owned pane identity mismatch")
	ErrOwnedCheckoutRetained = errors.New("herdr owned checkout retained for manual reconciliation")
)

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

// NudgeTarget binds one no-wait agent prompt to the route, pane, terminal,
// agent, and provider session that the caller revalidated.
type NudgeTarget struct {
	Ref          corebackend.PaneRef
	SessionID    string
	SocketPath   string
	TerminalID   string
	AgentID      string
	AgentSession *corebackend.AgentSessionRef
}

// NudgePrompt is one fully preflighted no-wait agent prompt. Callers issue it
// at most once after their final cooperative-state gate.
type NudgePrompt func(context.Context) error

type OwnedCloseRequest struct {
	Target                 OwnedPaneIdentity
	WorktreeOwnershipNonce string
	WorktreeGitDir         string
}

type ownedTargetAdmission struct {
	target           OwnedPaneIdentity
	closeRequest     *OwnedCloseRequest
	closeFingerprint corebackend.CloseRequest
}

type agentPromptEnvelope struct {
	ID     string             `json:"id"`
	Result *agentPromptResult `json:"result"`
}

type agentPromptResult struct {
	Type  string    `json:"type"`
	Agent agentJSON `json:"agent"`
}

func (b *Backend) BindOwnedTarget(target OwnedPaneIdentity) (*Backend, error) {
	return b.bindOwnedTarget(target, nil)
}

func (b *Backend) BindOwnedClose(req OwnedCloseRequest) (*Backend, error) {
	cloned := cloneOwnedCloseRequest(req)
	return b.bindOwnedTarget(cloned.Target, &cloned)
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
		cloned := cloneOwnedCloseRequest(*closeRequest)
		if err := verifyWorktreeOwnership(cloned); err != nil {
			return nil, err
		}
		targetAdmission.closeRequest = &cloned
		targetAdmission.closeFingerprint = corebackend.CloseRequest{Ref: target.Ref, WorktreePath: target.WorktreePath, ShellKey: target.TerminalID}
	}
	return b.cloneWithTarget(targetAdmission), nil
}

func (b *Backend) cloneWithTarget(target *ownedTargetAdmission) *Backend {
	clone := &Backend{
		session: b.session, socketPath: b.socketPath, probeGate: make(chan struct{}, 1),
		lookPath: b.lookPath, stageBinary: b.stageBinary, output: b.output,
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

func (b *Backend) readCore(ref corebackend.PaneRef, lines int) (string, error) {
	target, err := b.boundOwnedTarget(ref, "read")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*commandTimeout)
	defer cancel()
	return b.readOwned(ctx, target, lines)
}

// ReadOwnedPane reads an identity-fenced pane within the caller's deadline.
func (s *OwnedSession) ReadOwnedPane(ctx context.Context, target OwnedPaneIdentity, lines int) (string, error) {
	if s == nil || s.backend == nil {
		return "", fmt.Errorf("herdr owned session is nil")
	}
	if ctx == nil {
		return "", fmt.Errorf("read Herdr owned pane requires a context")
	}
	return s.backend.readOwned(ctx, target, lines)
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
		return "", methodUnavailable("pane.read")
	}
	if err := b.verifyOwnedTargetAfter(ctx, admission, target); err != nil {
		return "", fmt.Errorf("discard herdr pane read result: %w", err)
	}
	return string(out), nil
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
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, "agent", "prompt", target.Ref.Pane, line)
	if err != nil {
		return methodUnavailable("agent.prompt")
	}
	if err := validateAgentPromptResponse(out, target); err != nil {
		return methodUnavailable("agent.prompt")
	}
	if err := b.verifyOwnedTargetAfter(ctx, admission, target); err != nil {
		return fmt.Errorf("verify herdr pane after prompt: %w", err)
	}
	return nil
}

// PrepareNudge completes the owned-route preflight before the caller's final
// cooperative-state gate. The returned function issues only agent prompt.
func (s *OwnedSession) PrepareNudge(ctx context.Context, target NudgeTarget, line string) (NudgePrompt, error) {
	if err := validateNudgeRequest(s, line); err != nil {
		return nil, err
	}
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer unlockPrivateFile(lock)
	if !validNudgeTarget(target, admission) {
		return nil, fmt.Errorf("%w: saved nudge target is incomplete or belongs to a foreign route", ErrOwnedIdentityMismatch)
	}
	target.AgentSession = cloneAgentSession(target.AgentSession)
	probed, err := s.backend.probeOwned(ctx, admission)
	if err != nil {
		return nil, err
	}
	return func(promptCtx context.Context) error {
		return s.backend.runNudgePrompt(promptCtx, probed, target, line)
	}, nil
}

// Nudge preserves the direct infra entrypoint for callers that do not have a
// separate cooperative-state gate.
func (s *OwnedSession) Nudge(ctx context.Context, target NudgeTarget, line string) error {
	prompt, err := s.PrepareNudge(ctx, target, line)
	if err != nil {
		return err
	}
	return prompt(ctx)
}

func validateNudgeRequest(session *OwnedSession, line string) error {
	if session == nil || session.backend == nil {
		return fmt.Errorf("herdr owned session is nil")
	}
	if strings.ContainsAny(line, "\x00\r\n") {
		return fmt.Errorf("herdr nudge contains a NUL, CR, or LF byte")
	}
	return nil
}

func (b *Backend) runNudgePrompt(ctx context.Context, probed probeResult, target NudgeTarget, line string) error {
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route,
		"agent", "prompt", target.Ref.Pane, line)
	if err != nil {
		return methodUnavailable("agent.prompt")
	}
	identity := OwnedPaneIdentity{
		Ref: target.Ref, TerminalID: target.TerminalID, AgentID: target.AgentID,
		AgentSession: cloneAgentSession(target.AgentSession),
	}
	if validateAgentPromptResponse(out, identity) != nil {
		return methodUnavailable("agent.prompt")
	}
	return nil
}

func validNudgeTarget(target NudgeTarget, admission ownedAdmission) bool {
	checks := []bool{
		target.Ref.Backend == corebackend.Herdr,
		target.SessionID == admission.marker.Session,
		target.SocketPath == admission.marker.SocketPath,
		target.Ref.Workspace != "", target.Ref.Pane != "",
		target.TerminalID != "", target.AgentID != "",
	}
	for _, ok := range checks {
		if !ok {
			return false
		}
	}
	return target.AgentSession == nil || target.AgentSession.Valid()
}

func validateAgentPromptResponse(data []byte, target OwnedPaneIdentity) error {
	var envelope agentPromptEnvelope
	if err := decodeOne(data, &envelope); err != nil {
		return err
	}
	if envelope.ID != "cli:agent:prompt" || envelope.Result == nil || envelope.Result.Type != "agent_prompted" {
		return fmt.Errorf("unexpected agent prompt envelope")
	}
	agent := envelope.Result.Agent
	if agent.TerminalID != target.TerminalID || agent.WorkspaceID != target.Ref.Workspace || agent.TabID == "" ||
		agent.PaneID != target.Ref.Pane || agent.Focused == nil || agent.Revision == nil {
		return fmt.Errorf("%w: prompted agent identity changed", ErrOwnedIdentityMismatch)
	}
	agentID := optionalString(agent.Name)
	if agentID == "" {
		agentID = optionalString(agent.Agent)
	}
	if agentID != target.AgentID {
		return fmt.Errorf("%w: prompted agent name changed", ErrOwnedIdentityMismatch)
	}
	if !agentPromptSessionMatches(agent.AgentSession, target.AgentSession) {
		return fmt.Errorf("%w: prompted agent session changed", ErrOwnedIdentityMismatch)
	}
	return nil
}

func agentPromptSessionMatches(current *agentSessionJSON, expected *corebackend.AgentSessionRef) bool {
	ref, present, err := parseAgentSession(current)
	if err != nil {
		return false
	}
	if expected == nil {
		return !present
	}
	return present && ref == (agentSessionKey{
		source: expected.Source, agent: expected.Agent, kind: expected.Kind, value: expected.Value,
	})
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
	if target.AgentID == "" || target.AgentSession == nil {
		return fmt.Errorf("%w: focus requires a saved live-agent identity", ErrOwnedIdentityMismatch)
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
	_, err = b.runContext(ctx, commandTimeout, probed.binary, probed.route, "agent", "focus", target.Ref.Pane)
	if err != nil {
		return methodUnavailable("agent.focus")
	}
	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return err
	}
	current, ok := view.find(target.Ref)
	if !ok || !ownedPaneMatches(target, current) || !current.paneFocused {
		return fmt.Errorf("%w: agent focus did not focus the admitted pane", ErrOwnedIdentityMismatch)
	}
	return nil
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
	_, err = b.runContext(ctx, commandTimeout, probed.binary, probed.route, "pane", "close", target.Ref.Pane)
	if err != nil {
		return methodUnavailable("pane.close")
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
	err = verifyWorktreeOwnership(req)
	if err != nil {
		return failed, err
	}
	_, err = b.runContext(ctx, commandTimeout, probed.binary, probed.route, "workspace", "close", target.Ref.Workspace)
	if err != nil {
		return failed, methodUnavailable("workspace.close")
	}
	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return failed, err
	}
	if view.workspacePresent(target.Ref.Workspace) {
		return failed, fmt.Errorf("herdr workspace close returned success but workspace remains live")
	}
	if err := verifyWorktreeOwnership(req); err != nil {
		return failed, fmt.Errorf("verify retained checkout after workspace close: %w", err)
	}
	return failed, fmt.Errorf("%w: workspace %s is closed but checkout %s was not removed", ErrOwnedCheckoutRetained, target.Ref.Workspace, target.WorktreePath)
}

type ownedSnapshotView struct {
	panes      map[corebackend.PaneRef]ownedPaneView
	workspaces map[string]ownedWorkspaceView
}

type ownedWorkspaceView struct {
	label        string
	repoKey      string
	worktreePath string
}

type ownedPaneView struct {
	identity    OwnedPaneIdentity
	paneFocused bool
}

func (b *Backend) ownedSnapshotView(ctx context.Context, admission ownedAdmission) (ownedSnapshotView, error) {
	probed, err := b.probeOwned(ctx, admission)
	if err != nil {
		return ownedSnapshotView{}, err
	}
	out, err := b.runContext(ctx, commandTimeout, probed.binary, probed.route, "api", "snapshot")
	if err != nil {
		return ownedSnapshotView{}, methodUnavailable("session.snapshot")
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
	view := ownedSnapshotView{panes: map[corebackend.PaneRef]ownedPaneView{}, workspaces: map[string]ownedWorkspaceView{}}
	for _, workspace := range *envelope.Result.Snapshot.Workspaces {
		worktreePath, repoKey := "", ""
		if workspace.Worktree != nil {
			worktreePath, repoKey = workspace.Worktree.CheckoutPath, workspace.Worktree.RepoKey
		}
		view.workspaces[workspace.WorkspaceID] = ownedWorkspaceView{label: workspace.Label, repoKey: repoKey, worktreePath: worktreePath}
	}
	for _, pane := range panes {
		workspace := view.workspaces[pane.Ref.Workspace]
		identity := OwnedPaneIdentity{
			Ref: pane.Ref, SessionID: pane.SessionID, SocketPath: pane.SocketPath,
			WorkspaceLabel: workspace.label, TerminalID: pane.TerminalID, RepoKey: pane.RepoKey,
			WorktreePath: pane.WorktreePath, CurrentPath: pane.CurrentPath, AgentID: pane.AgentID,
			AgentSession: cloneAgentSession(pane.AgentSession),
		}
		view.panes[pane.Ref] = ownedPaneView{identity: identity, paneFocused: pane.FocusKnown && pane.Focused}
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
	cloned := *ref
	return &cloned
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
	err = validatePrivateRegular(markerPath, info)
	if err != nil {
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
