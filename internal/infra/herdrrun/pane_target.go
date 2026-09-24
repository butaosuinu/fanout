package herdrrun

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/naming"
)

type OwnedCloseRequest struct {
	Target                 corebackend.OwnedPaneIdentity
	WorktreeOwnershipNonce string
	WorktreeGitDir         string
}

type ownedTargetAdmission struct {
	target           corebackend.OwnedPaneIdentity
	closeRequest     *OwnedCloseRequest
	workspaceClose   bool
	closeFingerprint corebackend.CloseRequest
}

func (b *Backend) BindOwnedTarget(target corebackend.OwnedPaneIdentity) (*Backend, error) {
	return b.bindOwnedTarget(target, nil)
}

// VerifyOwnedTarget admits target on this session and keeps only the verdict,
// so a caller that just revalidates a saved row never handles a bound backend.
func (s *OwnedSession) VerifyOwnedTarget(target corebackend.OwnedPaneIdentity) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("herdr owned session is nil")
	}
	_, err := s.backend.BindOwnedTarget(target)
	return err
}

func ownedTargetFromBinding(binding corebackend.PaneBinding) corebackend.OwnedPaneIdentity {
	return corebackend.OwnedPaneIdentity{
		Ref: binding.Ref, SessionID: binding.SessionID, SocketPath: binding.SocketPath,
		WorkspaceLabel: binding.WorkspaceLabel, TerminalID: binding.TerminalID,
		RepoKey: binding.RepoKey, WorktreePath: binding.WorktreePath,
		CurrentPath: binding.WorktreePath, Agent: binding.Agent, AgentID: binding.AgentID,
		AgentSession: cloneAgentSession(binding.AgentSession),
	}
}

func (b *Backend) BindOwnedClose(req OwnedCloseRequest) (*Backend, error) {
	cloned := cloneOwnedCloseRequest(req)
	return b.bindOwnedTarget(cloned.Target, &cloned)
}

func (b *Backend) bindOwnedTarget(target corebackend.OwnedPaneIdentity, closeRequest *OwnedCloseRequest) (*Backend, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*commandTimeout)
	defer cancel()
	var bound *Backend
	err := b.withOwnedAdmission(ctx, ownedOperationLane, nil, func(call ownedCall) error {
		target = cloneOwnedPaneIdentity(target)
		if err := validateSavedTarget(target, call.admission); err != nil {
			return err
		}
		if _, _, err := call.resolveOwnedTarget(ctx, target); err != nil {
			return err
		}
		targetAdmission := &ownedTargetAdmission{target: target}
		if closeRequest != nil {
			cloned := cloneOwnedCloseRequest(*closeRequest)
			if err := verifyWorktreeOwnership(cloned); err != nil {
				return err
			}
			targetAdmission.closeRequest = &cloned
			targetAdmission.closeFingerprint = corebackend.CloseRequest{Ref: target.Ref, WorktreePath: target.WorktreePath, ShellKey: target.TerminalID}
		}
		bound = b.cloneWithTarget(targetAdmission)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return bound, nil
}

// restoreOwnedAgentName re-asserts the agent name fanout minted for this row
// when the runtime is holding the record without one. A provider that restarts
// its conversation in place — Claude's /clear — makes the runtime re-register
// the agent anonymously; the row keeps working either way, because
// observedAgentMatches admits an unnamed record, but fanout puts its own name
// back the next time it mutates the pane.
//
// It runs only from the mutation lane. Every read path resolves the same target
// without it, so no GET can rename anything, and the repair rides an operation
// the caller already asked to change the pane.
//
// The rename is admitted only where it restores what fanout established at
// launch: the pane is otherwise exactly the recorded one, the runtime holds no
// name for the agent, and the recorded name is one fanout minted. It therefore
// never takes a name another agent already answers to. A failure is not fatal —
// the caller's own mutation is still admissible against an unnamed record.
func (c ownedCall) restoreOwnedAgentName(
	ctx context.Context,
	target corebackend.OwnedPaneIdentity,
) {
	if !naming.IsManagedAgentName(target.AgentID) {
		return
	}
	view, err := c.ownedSnapshotView(ctx)
	if err != nil {
		return
	}
	current, ok := view.find(target.Ref)
	if !ok || !current.agentUnnamed ||
		!ownedPaneMatchesExceptAgentName(target, current.identity) {
		return
	}
	probed, err := c.probe(ctx)
	if err != nil {
		return
	}
	out, err := c.b.runContext(ctx, commandTimeout, probed.binary, probed.route,
		"agent", "rename", target.Ref.Pane, target.AgentID)
	if err == nil {
		_ = validateAgentRenameResponse(out)
	}
}

func validateAgentRenameResponse(data []byte) error {
	var envelope agentRenameEnvelope
	if err := decodeOne(data, &envelope); err != nil {
		return err
	}
	if envelope.ID != "cli:agent:rename" || envelope.Result == nil {
		return fmt.Errorf("herdr agent rename returned an unexpected response")
	}
	return nil
}

func (b *Backend) cloneWithTarget(target *ownedTargetAdmission) *Backend {
	clone := &Backend{herdrCLI: b.clone(), target: target}
	if b.owner != nil {
		owner := *b.owner
		clone.owner = &owner
	}
	return clone
}

func (b *Backend) boundOwnedTarget(ref corebackend.PaneRef, operation string) (corebackend.OwnedPaneIdentity, error) {
	if b == nil || b.target == nil {
		return corebackend.OwnedPaneIdentity{}, corebackend.Unsupported(corebackend.Herdr, operation+" without an immutable target admission")
	}
	if ref != b.target.target.Ref {
		return corebackend.OwnedPaneIdentity{}, fmt.Errorf("%w: %s reference does not match immutable admission", corebackend.ErrOwnedIdentityMismatch, operation)
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
func (s *OwnedSession) ReadOwnedPane(ctx context.Context, target corebackend.OwnedPaneIdentity, lines int) (string, error) {
	if s == nil || s.backend == nil {
		return "", fmt.Errorf("herdr owned session is nil")
	}
	if ctx == nil {
		return "", fmt.Errorf("read Herdr owned pane requires a context")
	}
	return s.backend.readOwned(ctx, target, lines)
}

func (b *Backend) readOwned(ctx context.Context, saved corebackend.OwnedPaneIdentity, lines int) (string, error) {
	if lines < 0 {
		return "", fmt.Errorf("herdr read lines must be non-negative")
	}
	var text string
	err := b.withOwnedAdmission(ctx, ownedOperationLane, nil, func(call ownedCall) error {
		target, probed, err := call.resolveOwnedTarget(ctx, saved)
		if err != nil {
			return err
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
			return methodUnavailable("pane.read")
		}
		if err := call.verifyOwnedTargetAfter(ctx, target); err != nil {
			return fmt.Errorf("discard herdr pane read result: %w", err)
		}
		text = string(out)
		return nil
	})
	if err != nil {
		return "", err
	}
	return text, nil
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

func (b *Backend) sendLineOwned(ctx context.Context, saved corebackend.OwnedPaneIdentity, line string) error {
	if strings.ContainsAny(line, "\x00\r\n") {
		return fmt.Errorf("herdr send line contains a NUL, CR, or LF byte")
	}
	if saved.AgentID == "" || saved.AgentSession == nil {
		return fmt.Errorf("%w: send line requires a saved live-agent identity", corebackend.ErrOwnedIdentityMismatch)
	}
	return b.withOwnedAdmission(ctx, ownedMutationLane, nil, func(call ownedCall) error {
		call.restoreOwnedAgentName(ctx, saved)
		target, probed, err := call.resolveOwnedTarget(ctx, saved)
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
		if err := call.verifyOwnedTargetAfter(ctx, target); err != nil {
			return fmt.Errorf("verify herdr pane after prompt: %w", err)
		}
		return nil
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

func (b *Backend) focusOwned(ctx context.Context, saved corebackend.OwnedPaneIdentity) error {
	if (saved.AgentID == "") != (saved.AgentSession == nil) {
		return fmt.Errorf("%w: focus target has a partial live-agent identity", corebackend.ErrOwnedIdentityMismatch)
	}
	return b.withOwnedAdmission(ctx, ownedMutationLane, nil, func(call ownedCall) error {
		call.restoreOwnedAgentName(ctx, saved)
		target, probed, err := call.resolveOwnedTarget(ctx, saved)
		if err != nil {
			return err
		}
		args := []string{"workspace", "focus", target.Ref.Workspace}
		method := "workspace.focus"
		if target.AgentID != "" {
			args = []string{"agent", "focus", target.Ref.Pane}
			method = "agent.focus"
		}
		_, err = b.runContext(ctx, commandTimeout, probed.binary, probed.route, args...)
		if err != nil {
			return methodUnavailable(method)
		}
		view, err := call.ownedSnapshotView(ctx)
		if err != nil {
			return err
		}
		current, ok := view.find(target.Ref)
		if !ok || !ownedPaneMatches(target, current) || !current.paneFocused {
			return fmt.Errorf("%w: focus did not select the admitted pane", corebackend.ErrOwnedIdentityMismatch)
		}
		return nil
	})
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

func (b *Backend) closePaneOwned(ctx context.Context, saved corebackend.OwnedPaneIdentity) error {
	return b.withOwnedAdmission(ctx, ownedMutationLane, nil, func(call ownedCall) error {
		call.restoreOwnedAgentName(ctx, saved)
		target, probed, err := call.resolveOwnedTarget(ctx, saved)
		if err != nil {
			return err
		}
		_, err = b.runContext(ctx, commandTimeout, probed.binary, probed.route, "pane", "close", target.Ref.Pane)
		if err != nil {
			return methodUnavailable("pane.close")
		}
		view, err := call.ownedSnapshotView(ctx)
		if err != nil {
			return err
		}
		if current, ok := view.find(target.Ref); ok {
			if ownedPaneMatches(target, current) {
				return fmt.Errorf("herdr pane close returned success but target remains live")
			}
			return fmt.Errorf("%w: pane id was reused after close", corebackend.ErrOwnedIdentityMismatch)
		}
		return nil
	})
}

func (b *Backend) CloseOwned(req corebackend.CloseRequest) (corebackend.CloseResult, error) {
	failed := corebackend.CloseResult{Status: corebackend.CloseFailed}
	if b == nil || b.target == nil || (!b.target.workspaceClose && b.target.closeRequest == nil) {
		return failed, corebackend.Unsupported(corebackend.Herdr, "owned close without an immutable target admission")
	}
	if req != b.target.closeFingerprint {
		return failed, fmt.Errorf("%w: close request does not match immutable admission", corebackend.ErrOwnedIdentityMismatch)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*commandTimeout)
	defer cancel()
	if b.target.workspaceClose {
		return b.closeOwnedWorkspace(ctx, b.target.target)
	}
	return b.closeOwnedSession(ctx, cloneOwnedCloseRequest(*b.target.closeRequest))
}

func (b *Backend) closeOwnedSession(ctx context.Context, req OwnedCloseRequest) (corebackend.CloseResult, error) {
	failed := corebackend.CloseResult{Status: corebackend.CloseFailed}
	if err := verifyWorktreeOwnership(req); err != nil {
		return failed, err
	}
	return failed, b.withOwnedAdmission(ctx, ownedMutationLane, nil, func(call ownedCall) error {
		return call.closeSessionWorkspace(ctx, req)
	})
}

// closeSessionWorkspace closes the admitted target's workspace. It never
// returns nil: this path does not remove the checkout, so a confirmed close
// still reports ErrOwnedCheckoutRetained.
func (c ownedCall) closeSessionWorkspace(ctx context.Context, req OwnedCloseRequest) error {
	target, probed, err := c.resolveOwnedTarget(ctx, req.Target)
	if err != nil {
		return err
	}
	err = verifyWorktreeOwnership(req)
	if err != nil {
		return err
	}
	_, err = c.b.runContext(ctx, commandTimeout, probed.binary, probed.route, "workspace", "close", target.Ref.Workspace)
	if err != nil {
		return methodUnavailable("workspace.close")
	}
	view, err := c.ownedSnapshotView(ctx)
	if err != nil {
		return err
	}
	if view.workspacePresent(target.Ref.Workspace) {
		return fmt.Errorf("herdr workspace close returned success but workspace remains live")
	}
	if err := verifyWorktreeOwnership(req); err != nil {
		return fmt.Errorf("verify retained checkout after workspace close: %w", err)
	}
	return fmt.Errorf("%w: workspace %s is closed but checkout %s was not removed", corebackend.ErrOwnedCheckoutRetained, target.Ref.Workspace, target.WorktreePath)
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
		return fmt.Errorf("%w: owned close requires matching worktree ownership", corebackend.ErrOwnedIdentityMismatch)
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
			return fmt.Errorf("%w: %s path is not canonical", corebackend.ErrOwnedIdentityMismatch, description)
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			return fmt.Errorf("%w: %s path does not resolve to its saved identity", corebackend.ErrOwnedIdentityMismatch, description)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s path is not a real directory", corebackend.ErrOwnedIdentityMismatch, description)
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
		return fmt.Errorf("%w: worktree ownership marker does not match saved identity", corebackend.ErrOwnedIdentityMismatch)
	}
	return nil
}
