package herdrrun

import (
	"bytes"
	"context"
	"encoding/json"
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

// ErrOwnedIdentityMismatch reports that a persisted herdr target is gone or
// no longer denotes the same terminal and worktree. Callers must not rebind a
// target from the current snapshot after receiving this error.
var ErrOwnedIdentityMismatch = errors.New("herdr owned pane identity mismatch")

// OwnedPaneIdentity is the immutable comparison baseline for one targeted
// herdr operation. SessionID and SocketPath bind the request to the admitted
// owned backend. WorkspaceLabel is the saved ownership nonce carried by the
// workspace label. TerminalID and the worktree (or generic-workspace cwd)
// provenance protect the public pane id from reuse.
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

// ReadRequest carries the saved identity required for a targeted content read.
type ReadRequest struct {
	Target OwnedPaneIdentity
	Lines  int
}

// SendLineRequest carries the saved identity and one literal submitted line.
type SendLineRequest struct {
	Target OwnedPaneIdentity
	Line   string
}

// FocusRequest carries the saved identity for a workspace-level focus change.
type FocusRequest struct {
	Target OwnedPaneIdentity
}

// ClosePaneRequest carries the saved identity for closing only one pane.
type ClosePaneRequest struct {
	Target OwnedPaneIdentity
}

// OwnedCloseRequest removes an owned worktree and then closes any workspace
// left behind. Force is never inferred; only this explicit field adds --force.
type OwnedCloseRequest struct {
	Target                 OwnedPaneIdentity
	WorktreeOwnershipNonce string
	WorktreeGitDir         string
	Force                  bool
}

const (
	worktreeOwnershipMarkerName     = "fanout-herdr-worktree-owner.json"
	worktreeOwnershipMarkerSchema   = 1
	worktreeOwnershipMarkerMaxBytes = 16 << 10
)

type worktreeOwnershipMarker struct {
	SchemaVersion int    `json:"schema_version"`
	Nonce         string `json:"nonce"`
	WorkspaceID   string `json:"workspace_id"`
	RepoKey       string `json:"repo_key"`
	CheckoutPath  string `json:"checkout_path"`
	GitDir        string `json:"git_dir"`
}

type ownedTargetAdmission struct {
	target           OwnedPaneIdentity
	closeRequest     *OwnedCloseRequest
	closeFingerprint corebackend.CloseRequest
}

// ReadOwned reads pane content only while the saved target matches immediately
// before and after the read. Content is discarded on a post-read mismatch.
func (b *Backend) ReadOwned(ctx context.Context, req ReadRequest) (string, error) {
	target := cloneOwnedPaneIdentity(req.Target)
	return b.readOwned(ctx, target.Ref, &target, req.Lines)
}

// readCore uses only a target admitted by BindOwnedTarget or BindOwnedClose.
func (b *Backend) readCore(ref corebackend.PaneRef, lines int) (string, error) {
	target, err := b.boundOwnedTarget(ref, "read")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 16*commandTimeout)
	defer cancel()
	return b.readOwned(ctx, ref, &target, lines)
}

func (b *Backend) readOwned(ctx context.Context, ref corebackend.PaneRef, expected *OwnedPaneIdentity, lines int) (string, error) {
	if lines < 0 {
		return "", fmt.Errorf("herdr read lines must be non-negative")
	}
	admission, lock, err := b.acquireOwnedOperation(ctx)
	if err != nil {
		return "", err
	}
	defer unlockPrivateFile(lock)

	target, probed, _, err := b.resolveOwnedTarget(ctx, admission, ref, expected)
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
	out, err := b.runOwnedOperation(ctx, admission, probed, args...)
	if err != nil {
		return "", fmt.Errorf("herdr pane read: %w", err)
	}
	if err := b.verifyOwnedTargetAfter(ctx, admission, target); err != nil {
		return "", fmt.Errorf("discard herdr pane read result: %w", err)
	}
	return string(out), nil
}

// SendLineOwned submits one line only after the complete saved identity has
// matched. A successful command is post-checked, but it is never retried when
// the CLI response is lost.
func (b *Backend) SendLineOwned(ctx context.Context, req SendLineRequest) error {
	target := cloneOwnedPaneIdentity(req.Target)
	return b.sendLineOwned(ctx, target.Ref, &target, req.Line)
}

// sendLineCore uses only a target admitted by BindOwnedTarget or
// BindOwnedClose.
func (b *Backend) sendLineCore(ref corebackend.PaneRef, line string) error {
	target, err := b.boundOwnedTarget(ref, "send line")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 16*commandTimeout)
	defer cancel()
	return b.sendLineOwned(ctx, ref, &target, line)
}

func (b *Backend) sendLineOwned(ctx context.Context, ref corebackend.PaneRef, expected *OwnedPaneIdentity, line string) error {
	if strings.ContainsAny(line, "\x00\r\n") {
		return fmt.Errorf("herdr send line contains a NUL, CR, or LF byte")
	}
	admission, lock, err := b.acquireOwnedOperation(ctx)
	if err != nil {
		return err
	}
	defer unlockPrivateFile(lock)

	target, probed, _, err := b.resolveOwnedTarget(ctx, admission, ref, expected)
	if err != nil {
		return err
	}
	out, err := b.runOwnedOperation(ctx, admission, probed, "pane", "run", target.Ref.Pane, line)
	if err != nil {
		return fmt.Errorf("herdr pane run: %w", err)
	}
	if len(out) != 0 {
		return fmt.Errorf("herdr pane run returned unexpected output")
	}
	if err := b.verifyOwnedTargetAfter(ctx, admission, target); err != nil {
		return fmt.Errorf("verify herdr pane after send: %w", err)
	}
	return nil
}

// FocusOwned focuses the target workspace. Agent focus is deliberately not
// required: workspace focus is the stable unit, and a future caller may add an
// agent focus only when it has already proven a unique native agent target.
func (b *Backend) FocusOwned(ctx context.Context, req FocusRequest) error {
	target := cloneOwnedPaneIdentity(req.Target)
	return b.focusOwned(ctx, target.Ref, &target)
}

// focusCore uses only a target admitted by BindOwnedTarget or BindOwnedClose.
func (b *Backend) focusCore(ref corebackend.PaneRef) error {
	target, err := b.boundOwnedTarget(ref, "focus")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 16*commandTimeout)
	defer cancel()
	return b.focusOwned(ctx, ref, &target)
}

func (b *Backend) focusOwned(ctx context.Context, ref corebackend.PaneRef, expected *OwnedPaneIdentity) error {
	admission, lock, err := b.acquireOwnedOperation(ctx)
	if err != nil {
		return err
	}
	defer unlockPrivateFile(lock)

	target, probed, _, err := b.resolveOwnedTarget(ctx, admission, ref, expected)
	if err != nil {
		return err
	}
	out, err := b.runOwnedOperation(ctx, admission, probed, "workspace", "focus", target.Ref.Workspace)
	if err != nil {
		return fmt.Errorf("herdr workspace focus: %w", err)
	}
	if err := validateWorkspaceFocused(out, target.Ref.Workspace, target.WorkspaceLabel); err != nil {
		return fmt.Errorf("validate herdr workspace focus response: %w", err)
	}
	if err := b.verifyOwnedFocusAfter(ctx, admission, target); err != nil {
		return fmt.Errorf("verify herdr pane after focus: %w", err)
	}
	return nil
}

// ClosePaneOwned closes only the pane in Target. It never removes a worktree or
// closes the containing workspace.
func (b *Backend) ClosePaneOwned(ctx context.Context, req ClosePaneRequest) error {
	target := cloneOwnedPaneIdentity(req.Target)
	return b.closePaneOwned(ctx, target.Ref, &target)
}

// closeCore closes only the pane admitted by BindOwnedTarget or
// BindOwnedClose. Worktree cleanup remains exclusive to CloseOwned.
func (b *Backend) closeCore(ref corebackend.PaneRef) error {
	target, err := b.boundOwnedTarget(ref, "close pane")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 16*commandTimeout)
	defer cancel()
	return b.closePaneOwned(ctx, ref, &target)
}

func (b *Backend) closePaneOwned(ctx context.Context, ref corebackend.PaneRef, expected *OwnedPaneIdentity) error {
	admission, lock, err := b.acquireOwnedOperation(ctx)
	if err != nil {
		return err
	}
	defer unlockPrivateFile(lock)

	target, probed, _, err := b.resolveOwnedTarget(ctx, admission, ref, expected)
	if err != nil {
		return err
	}
	out, err := b.runOwnedOperation(ctx, admission, probed, "pane", "close", target.Ref.Pane)
	if err != nil {
		return fmt.Errorf("herdr pane close: %w", err)
	}
	if responseErr := validateOKEnvelope(out, "cli:pane:close"); responseErr != nil {
		return fmt.Errorf("validate herdr pane close response: %w", responseErr)
	}
	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return fmt.Errorf("verify herdr pane close: %w", err)
	}
	if current, ok := findOwnedPane(view.panes, target.Ref); ok {
		if ownedPaneMatches(target, current) {
			return fmt.Errorf("herdr pane close returned success but target is still live")
		}
		return fmt.Errorf("%w: pane id was reused after close", ErrOwnedIdentityMismatch)
	}
	if workspace, ok := view.workspaces[target.Ref.Workspace]; ok && !workspace.matchesOwnedTarget(target) {
		return fmt.Errorf("%w: workspace changed after pane close", ErrOwnedIdentityMismatch)
	}
	return nil
}

// BindOwnedTarget returns a new backend handle containing one immutable target
// admission for the runtime-neutral Read, SendLine, Focus, and Close methods.
// It never mutates the source backend or derives identity from a live snapshot.
func (b *Backend) BindOwnedTarget(target OwnedPaneIdentity) (*Backend, error) {
	return b.bindOwnedTarget(target, nil)
}

// BindOwnedClose additionally admits the composite runtime-neutral CloseOwned
// method. Force cannot be represented by CloseRequest, so forced closes remain
// typed-only operations.
func (b *Backend) BindOwnedClose(req OwnedCloseRequest) (*Backend, error) {
	if req.Force {
		return nil, fmt.Errorf("herdr core owned-close admission cannot bind force")
	}
	saved := cloneOwnedCloseRequest(req)
	return b.bindOwnedTarget(saved.Target, &saved)
}

func (b *Backend) bindOwnedTarget(target OwnedPaneIdentity, closeRequest *OwnedCloseRequest) (*Backend, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*commandTimeout)
	defer cancel()
	admission, lock, err := b.acquireOwnedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer unlockPrivateFile(lock)

	target = cloneOwnedPaneIdentity(target)
	if validationErr := validateOwnedPaneIdentity(target, admission); validationErr != nil {
		return nil, validationErr
	}
	targetAdmission := ownedTargetAdmission{target: target}
	if closeRequest != nil {
		saved := cloneOwnedCloseRequest(*closeRequest)
		if saved.Target.WorktreePath == "" {
			return nil, fmt.Errorf("herdr owned close requires saved worktree provenance")
		}
		if err := verifyWorktreeOwnershipMarker(saved, target); err != nil {
			return nil, err
		}
		targetAdmission.closeRequest = &saved
		targetAdmission.closeFingerprint = corebackend.CloseRequest{
			Ref:          target.Ref,
			WorktreePath: target.WorktreePath,
			ShellKey:     target.TerminalID,
		}
	}
	return b.cloneWithOwnedTargetAdmission(ctx, targetAdmission)
}

func (b *Backend) cloneWithOwnedTargetAdmission(ctx context.Context, targetAdmission ownedTargetAdmission) (*Backend, error) {
	if b == nil {
		return nil, fmt.Errorf("herdr target admission requires a backend")
	}
	if b.targetAdmission != nil {
		return nil, fmt.Errorf("herdr backend already has an immutable target admission")
	}
	select {
	case b.probeGate <- struct{}{}:
		defer func() { <-b.probeGate }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cloned := &Backend{
		session:         b.session,
		socketPath:      b.socketPath,
		probeGate:       make(chan struct{}, 1),
		lookPath:        b.lookPath,
		hashFile:        b.hashFile,
		output:          b.output,
		now:             b.now,
		sleep:           b.sleep,
		admitted:        make(map[string]binaryAdmission, len(b.admitted)),
		targetAdmission: &targetAdmission,
	}
	maps.Copy(cloned.admitted, b.admitted)
	if b.control != nil {
		control := *b.control
		cloned.control = &control
	}
	if b.owner != nil {
		owner := *b.owner
		cloned.owner = &owner
	}
	cloned.targetAdmission.target = cloneOwnedPaneIdentity(cloned.targetAdmission.target)
	if cloned.targetAdmission.closeRequest != nil {
		closeRequest := cloneOwnedCloseRequest(*cloned.targetAdmission.closeRequest)
		cloned.targetAdmission.closeRequest = &closeRequest
	}
	return cloned, nil
}

func (b *Backend) boundOwnedTarget(ref corebackend.PaneRef, operation string) (OwnedPaneIdentity, error) {
	if b == nil || b.targetAdmission == nil {
		return OwnedPaneIdentity{}, corebackend.Unsupported(corebackend.Herdr, operation+" without an immutable target admission")
	}
	if ref != b.targetAdmission.target.Ref {
		return OwnedPaneIdentity{}, fmt.Errorf("%w: %s reference does not match the immutable herdr target admission", ErrOwnedIdentityMismatch, operation)
	}
	return cloneOwnedPaneIdentity(b.targetAdmission.target), nil
}

// CloseOwned executes the saved composite close only when the runtime-neutral
// request exactly matches the fingerprint bound by BindOwnedClose.
func (b *Backend) CloseOwned(req corebackend.CloseRequest) (corebackend.CloseResult, error) {
	failed := corebackend.CloseResult{Status: corebackend.CloseFailed}
	if b == nil || b.targetAdmission == nil || b.targetAdmission.closeRequest == nil {
		return failed, corebackend.Unsupported(corebackend.Herdr, "owned close without an immutable target admission")
	}
	if req != b.targetAdmission.closeFingerprint {
		return failed, fmt.Errorf("%w: core close request does not match the immutable herdr owned-close admission", ErrOwnedIdentityMismatch)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 24*commandTimeout)
	defer cancel()
	return b.closeOwnedSession(ctx, cloneOwnedCloseRequest(*b.targetAdmission.closeRequest))
}

// CloseOwnedSession rechecks ownership and the complete saved identity, asks
// herdr to remove the worktree once, and closes a residual workspace once. It
// never deletes a local branch. Any lost response remains failed and is not
// retried.
func (b *Backend) CloseOwnedSession(ctx context.Context, req OwnedCloseRequest) (corebackend.CloseResult, error) {
	return b.closeOwnedSession(ctx, req)
}

func (b *Backend) closeOwnedSession(ctx context.Context, req OwnedCloseRequest) (corebackend.CloseResult, error) {
	failed := corebackend.CloseResult{Status: corebackend.CloseFailed}
	admission, lock, err := b.acquireOwnedOperation(ctx)
	if err != nil {
		return failed, err
	}
	defer unlockPrivateFile(lock)

	target := cloneOwnedPaneIdentity(req.Target)
	if target.WorktreePath == "" {
		return failed, fmt.Errorf("herdr owned close requires saved worktree provenance")
	}
	target, probed, preView, err := b.resolveOwnedTarget(ctx, admission, target.Ref, &target)
	if err != nil {
		return failed, err
	}
	current, ok := findOwnedPane(preView.panes, target.Ref)
	if !ok || !ownedPaneMatches(target, current) {
		return failed, fmt.Errorf("%w: saved pane changed before worktree removal", ErrOwnedIdentityMismatch)
	}
	workspace, ok := preView.workspaces[target.Ref.Workspace]
	if !ok || !workspace.matchesOwnedTarget(target) {
		return failed, fmt.Errorf("%w: saved workspace changed before worktree removal", ErrOwnedIdentityMismatch)
	}

	args := []string{"worktree", "remove", "--workspace", target.Ref.Workspace}
	if req.Force {
		args = append(args, "--force")
	}
	args = append(args, "--json")
	out, err := b.runOwnedOperationAfter(ctx, admission, probed, func() error {
		return b.verifyOwnedClosePreflight(ctx, admission, req, target)
	}, args...)
	if err != nil {
		return failed, fmt.Errorf("herdr worktree remove (not retried): %w", err)
	}
	if responseErr := validateWorktreeRemoved(out, target, req.Force); responseErr != nil {
		return failed, fmt.Errorf("validate herdr worktree remove response (not retried): %w", responseErr)
	}
	if absenceErr := verifyRemovedCheckout(target.WorktreePath); absenceErr != nil {
		return failed, absenceErr
	}

	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return failed, fmt.Errorf("verify herdr worktree removal: %w", err)
	}
	if !view.workspacePresent(target.Ref.Workspace) {
		return corebackend.CloseResult{Status: corebackend.CloseConfirmed}, nil
	}
	residual := view.workspaces[target.Ref.Workspace]
	if !workspace.matchesResidual(residual) {
		return failed, fmt.Errorf("%w: workspace changed after worktree removal", ErrOwnedIdentityMismatch)
	}
	if current, ok := findOwnedPane(view.panes, target.Ref); ok && !ownedPaneMatchesAfterWorktreeRemoval(target, current) {
		return failed, fmt.Errorf("%w: pane id was reused after worktree removal", ErrOwnedIdentityMismatch)
	}
	probed, err = b.probeOwned(ctx, admission)
	if err != nil {
		return failed, fmt.Errorf("recheck herdr ownership before workspace close: %w", err)
	}
	out, err = b.runOwnedOperationAfter(ctx, admission, probed, func() error {
		return b.verifyResidualWorkspacePreflight(ctx, admission, workspace, target)
	}, "workspace", "close", target.Ref.Workspace)
	if err != nil {
		return failed, fmt.Errorf("herdr workspace close (not retried): %w", err)
	}
	if responseErr := validateOKEnvelope(out, "cli:workspace:close"); responseErr != nil {
		return failed, fmt.Errorf("validate herdr workspace close response (not retried): %w", responseErr)
	}
	view, err = b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return failed, fmt.Errorf("verify herdr workspace close: %w", err)
	}
	if view.workspacePresent(target.Ref.Workspace) {
		return failed, fmt.Errorf("herdr workspace close returned success but workspace %q is still live", target.Ref.Workspace)
	}
	return corebackend.CloseResult{Status: corebackend.CloseConfirmed}, nil
}

func (b *Backend) verifyOwnedClosePreflight(
	ctx context.Context,
	admission ownedAdmission,
	req OwnedCloseRequest,
	target OwnedPaneIdentity,
) error {
	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return fmt.Errorf("refresh herdr target immediately before worktree removal: %w", err)
	}
	current, ok := findOwnedPane(view.panes, target.Ref)
	if !ok || !ownedPaneMatches(target, current) {
		return fmt.Errorf("%w: pane changed immediately before worktree removal", ErrOwnedIdentityMismatch)
	}
	workspace, ok := view.workspaces[target.Ref.Workspace]
	if !ok || !workspace.matchesOwnedTarget(target) {
		return fmt.Errorf("%w: workspace changed immediately before worktree removal", ErrOwnedIdentityMismatch)
	}
	return verifyWorktreeOwnershipMarker(req, target)
}

func (b *Backend) verifyResidualWorkspacePreflight(
	ctx context.Context,
	admission ownedAdmission,
	workspace ownedWorkspaceView,
	target OwnedPaneIdentity,
) error {
	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return fmt.Errorf("refresh residual herdr workspace immediately before close: %w", err)
	}
	residual, ok := view.workspaces[target.Ref.Workspace]
	if !ok || !workspace.matchesResidual(residual) {
		return fmt.Errorf("%w: residual workspace changed immediately before close", ErrOwnedIdentityMismatch)
	}
	if current, ok := findOwnedPane(view.panes, target.Ref); ok && !ownedPaneMatchesAfterWorktreeRemoval(target, current) {
		return fmt.Errorf("%w: pane id was reused immediately before workspace close", ErrOwnedIdentityMismatch)
	}
	return nil
}

func verifyRemovedCheckout(path string) error {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("verify removed herdr checkout %s: %w", path, err)
	}
	return fmt.Errorf("herdr worktree remove returned success but checkout %s still exists", path)
}

func worktreeOwnershipMarkerPath(gitDir string) string {
	return filepath.Join(gitDir, worktreeOwnershipMarkerName)
}

func verifyWorktreeOwnershipMarker(req OwnedCloseRequest, target OwnedPaneIdentity) error {
	nonce := req.WorktreeOwnershipNonce
	if !validHexToken(nonce) {
		return fmt.Errorf("%w: owned close requires a saved worktree ownership nonce", ErrOwnedIdentityMismatch)
	}
	if target.WorkspaceLabel != nonce {
		return fmt.Errorf("%w: workspace label does not match the saved worktree ownership nonce", ErrOwnedIdentityMismatch)
	}
	checkout, err := canonicalOwnedDirectory(target.WorktreePath, "checkout")
	if err != nil {
		return err
	}
	if checkout != target.WorktreePath {
		return fmt.Errorf("%w: saved checkout path is not canonical", ErrOwnedIdentityMismatch)
	}
	gitDir, err := checkoutGitDir(checkout)
	if err != nil {
		return err
	}
	savedGitDir, err := canonicalOwnedDirectory(req.WorktreeGitDir, "saved checkout git directory")
	if err != nil {
		return err
	}
	if req.WorktreeGitDir != savedGitDir || gitDir != savedGitDir {
		return fmt.Errorf("%w: checkout git directory does not match the saved identity", ErrOwnedIdentityMismatch)
	}
	commonDir, err := canonicalOwnedDirectory(target.RepoKey, "repository git common directory")
	if err != nil {
		return err
	}
	if target.RepoKey != commonDir || !pathWithinDirectory(commonDir, gitDir) {
		return fmt.Errorf("%w: checkout git directory is outside the admitted repository", ErrOwnedIdentityMismatch)
	}

	marker, err := readWorktreeOwnershipMarker(worktreeOwnershipMarkerPath(gitDir))
	if err != nil {
		return fmt.Errorf("verify herdr worktree ownership marker: %w", err)
	}
	if marker.SchemaVersion != worktreeOwnershipMarkerSchema || marker.Nonce != nonce ||
		marker.WorkspaceID != target.Ref.Workspace || marker.RepoKey != commonDir ||
		marker.CheckoutPath != checkout || marker.GitDir != gitDir {
		return fmt.Errorf("%w: checkout git-dir marker does not match the saved worktree identity", ErrOwnedIdentityMismatch)
	}
	return nil
}

func canonicalOwnedDirectory(raw, description string) (string, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || !filepath.IsAbs(raw) || filepath.Clean(raw) != raw {
		return "", fmt.Errorf("%w: %s path is not an exact absolute path", ErrOwnedIdentityMismatch, description)
	}
	resolved, err := filepath.EvalSymlinks(raw)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", description, err)
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", description, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s %s is not a directory", ErrOwnedIdentityMismatch, description, resolved)
	}
	if err := validateOwnerUID(resolved, info); err != nil {
		return "", err
	}
	return resolved, nil
}

func checkoutGitDir(checkout string) (string, error) {
	gitFilePath := filepath.Join(checkout, ".git")
	data, err := readOwnedRegularFile(gitFilePath, false, worktreeOwnershipMarkerMaxBytes)
	if err != nil {
		return "", fmt.Errorf("read checkout git-dir pointer: %w", err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' || bytes.ContainsAny(data[:len(data)-1], "\r\n") {
		return "", fmt.Errorf("%w: checkout .git must contain exactly one LF-terminated gitdir line", ErrOwnedIdentityMismatch)
	}
	line := string(data[:len(data)-1])
	rawGitDir, ok := strings.CutPrefix(line, "gitdir: ")
	if !ok || rawGitDir == "" || strings.TrimSpace(rawGitDir) != rawGitDir {
		return "", fmt.Errorf("%w: checkout .git has an invalid gitdir line", ErrOwnedIdentityMismatch)
	}
	if !filepath.IsAbs(rawGitDir) {
		rawGitDir = filepath.Join(checkout, rawGitDir)
	}
	absGitDir, err := filepath.Abs(rawGitDir)
	if err != nil {
		return "", fmt.Errorf("resolve checkout git directory: %w", err)
	}
	return canonicalOwnedDirectory(filepath.Clean(absGitDir), "checkout git directory")
}

func pathWithinDirectory(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil || relative == "." || relative == ".." {
		return false
	}
	return !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func readWorktreeOwnershipMarker(path string) (worktreeOwnershipMarker, error) {
	data, err := readOwnedRegularFile(path, true, worktreeOwnershipMarkerMaxBytes)
	if err != nil {
		return worktreeOwnershipMarker{}, err
	}
	return decodeWorktreeOwnershipMarker(data)
}

func readOwnedRegularFile(path string, private bool, maxBytes int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect owned file %s: %w", path, err)
	}
	if private {
		if validationErr := validatePrivateRegular(path, before); validationErr != nil {
			return nil, validationErr
		}
	} else {
		if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("owned path %s is not a regular file", path)
		}
		if validationErr := validateOwnerUID(path, before); validationErr != nil {
			return nil, validationErr
		}
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open owned file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }() // The read-only descriptor has no buffered state to flush.
	opened, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened owned file %s: %w", path, err)
	}
	if !os.SameFile(before, opened) {
		return nil, fmt.Errorf("owned file %s changed while opening", path)
	}
	if private {
		if validationErr := validatePrivateRegular(path, opened); validationErr != nil {
			return nil, validationErr
		}
	} else if !opened.Mode().IsRegular() {
		return nil, fmt.Errorf("owned path %s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read owned file %s: %w", path, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("owned file %s exceeds %d bytes", path, maxBytes)
	}
	return data, nil
}

func decodeWorktreeOwnershipMarker(data []byte) (worktreeOwnershipMarker, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return worktreeOwnershipMarker{}, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return worktreeOwnershipMarker{}, fmt.Errorf("worktree ownership marker must be one JSON object")
	}
	marker := worktreeOwnershipMarker{}
	seen := make(map[string]struct{}, 6)
	for decoder.More() {
		keyToken, tokenErr := decoder.Token()
		if tokenErr != nil {
			return worktreeOwnershipMarker{}, tokenErr
		}
		key, ok := keyToken.(string)
		if !ok {
			return worktreeOwnershipMarker{}, fmt.Errorf("worktree ownership marker contains a non-string key")
		}
		if _, duplicate := seen[key]; duplicate {
			return worktreeOwnershipMarker{}, fmt.Errorf("worktree ownership marker contains duplicate field %q", key)
		}
		seen[key] = struct{}{}
		switch key {
		case "schema_version":
			err = decoder.Decode(&marker.SchemaVersion)
		case "nonce":
			err = decoder.Decode(&marker.Nonce)
		case "workspace_id":
			err = decoder.Decode(&marker.WorkspaceID)
		case "repo_key":
			err = decoder.Decode(&marker.RepoKey)
		case "checkout_path":
			err = decoder.Decode(&marker.CheckoutPath)
		case "git_dir":
			err = decoder.Decode(&marker.GitDir)
		default:
			return worktreeOwnershipMarker{}, fmt.Errorf("worktree ownership marker contains unknown field %q", key)
		}
		if err != nil {
			return worktreeOwnershipMarker{}, fmt.Errorf("decode worktree ownership marker field %q: %w", key, err)
		}
	}
	closingToken, err := decoder.Token()
	if err != nil {
		return worktreeOwnershipMarker{}, err
	}
	if delimiter, ok := closingToken.(json.Delim); !ok || delimiter != '}' {
		return worktreeOwnershipMarker{}, fmt.Errorf("worktree ownership marker has an invalid object terminator")
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return worktreeOwnershipMarker{}, fmt.Errorf("worktree ownership marker contains a trailing JSON value")
		}
		return worktreeOwnershipMarker{}, err
	}
	if len(seen) != 6 {
		return worktreeOwnershipMarker{}, fmt.Errorf("worktree ownership marker is missing a required field")
	}
	return marker, nil
}

type worktreeRemovedEnvelope struct {
	ID     string                 `json:"id"`
	Result *worktreeRemovedResult `json:"result"`
}

type worktreeRemovedResult struct {
	Type        string `json:"type"`
	WorkspaceID string `json:"workspace_id"`
	Path        string `json:"path"`
	Forced      *bool  `json:"forced"`
}

type operationOKEnvelope struct {
	ID     string `json:"id"`
	Result *struct {
		Type string `json:"type"`
	} `json:"result"`
}

type workspaceFocusedEnvelope struct {
	ID     string `json:"id"`
	Result *struct {
		Type      string `json:"type"`
		Workspace *struct {
			WorkspaceID *string `json:"workspace_id"`
			Number      *int    `json:"number"`
			Label       *string `json:"label"`
			Focused     *bool   `json:"focused"`
			PaneCount   *int    `json:"pane_count"`
			TabCount    *int    `json:"tab_count"`
			ActiveTabID *string `json:"active_tab_id"`
			AgentStatus *string `json:"agent_status"`
		} `json:"workspace"`
	} `json:"result"`
}

func validateOKEnvelope(data []byte, id string) error {
	var envelope operationOKEnvelope
	if err := decodeOne(data, &envelope); err != nil {
		return err
	}
	if envelope.ID != id || envelope.Result == nil || envelope.Result.Type != "ok" {
		return fmt.Errorf("unexpected operation envelope")
	}
	return nil
}

func validateWorkspaceFocused(data []byte, workspace, workspaceLabel string) error {
	var envelope workspaceFocusedEnvelope
	if err := decodeOne(data, &envelope); err != nil {
		return err
	}
	if envelope.ID != "cli:workspace:focus" || envelope.Result == nil || envelope.Result.Type != "workspace_info" ||
		envelope.Result.Workspace == nil {
		return fmt.Errorf("unexpected workspace focus envelope")
	}
	result := envelope.Result.Workspace
	if result.WorkspaceID == nil || *result.WorkspaceID != workspace || result.Number == nil || *result.Number <= 0 ||
		result.Label == nil || *result.Label != workspaceLabel || result.Focused == nil || !*result.Focused ||
		result.PaneCount == nil || *result.PaneCount < 0 ||
		result.TabCount == nil || *result.TabCount < 0 || result.ActiveTabID == nil || strings.TrimSpace(*result.ActiveTabID) == "" ||
		result.AgentStatus == nil || !validNativeAgentState(*result.AgentStatus) {
		return fmt.Errorf("workspace focus response is missing required fields or does not report the requested focused workspace")
	}
	return nil
}

func validateWorktreeRemoved(data []byte, target OwnedPaneIdentity, forced bool) error {
	var envelope worktreeRemovedEnvelope
	if err := decodeOne(data, &envelope); err != nil {
		return err
	}
	if envelope.ID != "cli:worktree:remove" || envelope.Result == nil || envelope.Result.Type != "worktree_removed" {
		return fmt.Errorf("unexpected worktree remove envelope")
	}
	result := envelope.Result
	if result.WorkspaceID != target.Ref.Workspace || result.Path != target.WorktreePath || result.Forced == nil || *result.Forced != forced {
		return fmt.Errorf("worktree remove response does not match the requested workspace, path, and force mode")
	}
	return nil
}

func (b *Backend) acquireOwnedOperation(ctx context.Context) (ownedAdmission, *os.File, error) {
	if ctx == nil {
		return ownedAdmission{}, nil, fmt.Errorf("herdr owned operation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return ownedAdmission{}, nil, err
	}
	if b == nil || b.owner == nil {
		return ownedAdmission{}, nil, fmt.Errorf("herdr mutation requires a fanout-owned session")
	}
	admission := *b.owner
	if err := validateOwnedAdmissionShape(admission); err != nil {
		return ownedAdmission{}, nil, err
	}
	lock, err := lockPrivateFileContext(ctx, admission.lockPath)
	if err != nil {
		return ownedAdmission{}, nil, fmt.Errorf("lock herdr owned operation: %w", err)
	}
	if err := ctx.Err(); err != nil {
		unlockPrivateFile(lock)
		return ownedAdmission{}, nil, err
	}
	if err := b.verifyImmutableOwnedAdmission(admission); err != nil {
		unlockPrivateFile(lock)
		return ownedAdmission{}, nil, err
	}
	return admission, lock, nil
}

func validateOwnedAdmissionShape(admission ownedAdmission) error {
	marker := admission.marker
	if admission.markerPath != filepath.Join(marker.RuntimeDir, ownedMarkerName) ||
		admission.lockPath != filepath.Join(marker.RuntimeDir, ownedLifecycleLockName) {
		return fmt.Errorf("herdr owned admission has inconsistent marker or lock paths")
	}
	return nil
}

func (b *Backend) verifyImmutableOwnedAdmission(admission ownedAdmission) error {
	if b == nil || b.owner == nil || *b.owner != admission {
		return fmt.Errorf("herdr owned admission changed; refusing operation")
	}
	marker := admission.marker
	if b.session != marker.Session || b.socketPath != marker.SocketPath || b.control == nil ||
		b.control.xdgConfigHome != marker.XDGConfigHome || b.control.xdgStateHome != marker.XDGStateHome ||
		b.control.xdgDataHome != marker.XDGDataHome || b.control.xdgCacheHome != marker.XDGCacheHome ||
		b.control.configPath != marker.ConfigPath || b.control.clientSocketPath != marker.ClientSocketPath {
		return fmt.Errorf("herdr owned backend route or control environment changed; refusing operation")
	}
	if err := b.verifyOwnedBinding(); err != nil {
		return err
	}
	return nil
}

func (b *Backend) probeOwned(ctx context.Context, admission ownedAdmission) (probeResult, error) {
	if err := b.verifyImmutableOwnedAdmission(admission); err != nil {
		return probeResult{}, err
	}
	probed, err := b.probeContext(ctx)
	if err != nil {
		return probeResult{}, err
	}
	marker := admission.marker
	if probed.binary != marker.BinaryPath || probed.sha256 != marker.BinarySHA256 || probed.version != marker.BinaryVersion ||
		probed.protocol != supportedProtocol || probed.route.session != marker.Session || probed.route.socketPath != marker.SocketPath {
		return probeResult{}, fmt.Errorf("herdr owned binary or route admission changed; refusing operation")
	}
	return probed, nil
}

type ownedSnapshotView struct {
	panes      []corebackend.LivePane
	workspaces map[string]ownedWorkspaceView
}

type ownedWorkspaceView struct {
	workspaceID      string
	number           int
	label            string
	focused          bool
	hasWorktree      bool
	isLinkedWorktree bool
	repoKey          string
	repoRoot         string
	checkout         string
}

type ownedWorkspaceEnvelope struct {
	Result *struct {
		Snapshot struct {
			Workspaces *[]struct {
				WorkspaceID string            `json:"workspace_id"`
				Number      *int              `json:"number"`
				Label       *string           `json:"label"`
				Focused     *bool             `json:"focused"`
				Worktree    *worktreeInfoJSON `json:"worktree"`
			} `json:"workspaces"`
		} `json:"snapshot"`
	} `json:"result"`
}

func (workspace ownedWorkspaceView) matchesOwnedTarget(target OwnedPaneIdentity) bool {
	if workspace.workspaceID != target.Ref.Workspace ||
		(target.WorkspaceLabel != "" && workspace.label != target.WorkspaceLabel) {
		return false
	}
	if target.WorktreePath == "" {
		return !workspace.hasWorktree
	}
	if !workspace.hasWorktree || !workspace.isLinkedWorktree ||
		workspace.checkout != target.WorktreePath || workspace.repoKey == "" || workspace.repoRoot == "" {
		return false
	}
	return workspace.repoKey == target.RepoKey
}

func (workspace ownedWorkspaceView) matchesResidual(current ownedWorkspaceView) bool {
	return current.workspaceID == workspace.workspaceID && current.number == workspace.number &&
		current.label == workspace.label && !current.hasWorktree
}

func (view ownedSnapshotView) workspacePresent(workspace string) bool {
	_, ok := view.workspaces[workspace]
	return ok
}

func (b *Backend) ownedSnapshotView(ctx context.Context, admission ownedAdmission) (ownedSnapshotView, error) {
	probed, err := b.probeOwned(ctx, admission)
	if err != nil {
		return ownedSnapshotView{}, err
	}
	out, err := b.runAdmittedContext(
		ctx,
		commandTimeout,
		binaryAdmission{path: probed.binary, sha256: probed.sha256, version: probed.version, protocol: probed.protocol},
		probed.route,
		"api",
		"snapshot",
	)
	if err != nil {
		return ownedSnapshotView{}, fmt.Errorf("herdr api snapshot: %w", err)
	}
	var envelope snapshotEnvelope
	if parseErr := decodeOne(out, &envelope); parseErr != nil {
		return ownedSnapshotView{}, fmt.Errorf("parse herdr api snapshot: %w", parseErr)
	}
	panes, err := projectSnapshot(envelope, probed.route, probed.version, probed.protocol)
	if err != nil {
		return ownedSnapshotView{}, err
	}
	var identityEnvelope ownedWorkspaceEnvelope
	if parseErr := decodeOne(out, &identityEnvelope); parseErr != nil {
		return ownedSnapshotView{}, fmt.Errorf("parse herdr workspace identity snapshot: %w", parseErr)
	}
	if identityEnvelope.Result == nil || identityEnvelope.Result.Snapshot.Workspaces == nil {
		return ownedSnapshotView{}, fmt.Errorf("herdr workspace identity snapshot is missing workspaces")
	}
	workspaces := make(map[string]ownedWorkspaceView, len(*identityEnvelope.Result.Snapshot.Workspaces))
	for _, workspace := range *identityEnvelope.Result.Snapshot.Workspaces {
		if strings.TrimSpace(workspace.WorkspaceID) == "" || workspace.Number == nil || *workspace.Number <= 0 ||
			workspace.Label == nil || workspace.Focused == nil {
			return ownedSnapshotView{}, fmt.Errorf("herdr workspace identity snapshot is missing workspace_id, number, label, or focused")
		}
		view := ownedWorkspaceView{
			workspaceID: workspace.WorkspaceID,
			number:      *workspace.Number,
			label:       *workspace.Label,
			focused:     *workspace.Focused,
		}
		if workspace.Worktree != nil {
			view.hasWorktree = true
			view.isLinkedWorktree = workspace.Worktree.IsLinkedWorktree != nil && *workspace.Worktree.IsLinkedWorktree
			view.repoKey = workspace.Worktree.RepoKey
			view.repoRoot = workspace.Worktree.RepoRoot
			view.checkout = workspace.Worktree.CheckoutPath
		}
		if _, duplicate := workspaces[workspace.WorkspaceID]; duplicate {
			return ownedSnapshotView{}, fmt.Errorf("herdr snapshot contains duplicate workspace identity %q", workspace.WorkspaceID)
		}
		workspaces[workspace.WorkspaceID] = view
	}
	if len(workspaces) != len(*envelope.Result.Snapshot.Workspaces) {
		return ownedSnapshotView{}, fmt.Errorf("herdr workspace identity projection disagrees with snapshot")
	}
	return ownedSnapshotView{panes: panes, workspaces: workspaces}, nil
}

func (b *Backend) runOwnedOperation(ctx context.Context, admission ownedAdmission, previous probeResult, args ...string) ([]byte, error) {
	return b.runOwnedOperationAfter(ctx, admission, previous, nil, args...)
}

func (b *Backend) runOwnedOperationAfter(
	ctx context.Context,
	admission ownedAdmission,
	previous probeResult,
	preflight func() error,
	args ...string,
) ([]byte, error) {
	if err := b.verifyImmutableOwnedAdmission(admission); err != nil {
		return nil, err
	}
	probed, err := b.probeOwned(ctx, admission)
	if err != nil {
		return nil, err
	}
	if probed != previous {
		return nil, fmt.Errorf("herdr binary or route changed between identity check and operation")
	}
	if preflight != nil {
		if preflightErr := preflight(); preflightErr != nil {
			return nil, fmt.Errorf("herdr owned operation preflight: %w", preflightErr)
		}
	}
	out, err := b.runAdmittedContext(
		ctx,
		commandTimeout,
		binaryAdmission{path: probed.binary, sha256: probed.sha256, version: probed.version, protocol: probed.protocol},
		probed.route,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("verify and run herdr owned operation: %w", err)
	}
	return out, nil
}

func (b *Backend) resolveOwnedTarget(
	ctx context.Context,
	admission ownedAdmission,
	ref corebackend.PaneRef,
	expected *OwnedPaneIdentity,
) (OwnedPaneIdentity, probeResult, ownedSnapshotView, error) {
	if ref.Backend != corebackend.Herdr || strings.TrimSpace(ref.Workspace) == "" || strings.TrimSpace(ref.Pane) == "" {
		return OwnedPaneIdentity{}, probeResult{}, ownedSnapshotView{}, fmt.Errorf("herdr owned operation requires an exact herdr workspace and pane reference")
	}
	if expected == nil {
		return OwnedPaneIdentity{}, probeResult{}, ownedSnapshotView{},
			fmt.Errorf("herdr owned operation requires a saved target identity")
	}
	target := cloneOwnedPaneIdentity(*expected)
	if target.Ref != ref {
		return OwnedPaneIdentity{}, probeResult{}, ownedSnapshotView{}, fmt.Errorf("herdr request target and pane reference disagree")
	}
	if validationErr := validateOwnedPaneIdentity(target, admission); validationErr != nil {
		return OwnedPaneIdentity{}, probeResult{}, ownedSnapshotView{}, validationErr
	}
	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return OwnedPaneIdentity{}, probeResult{}, ownedSnapshotView{}, err
	}
	current, ok := findOwnedPane(view.panes, ref)
	if !ok {
		return OwnedPaneIdentity{}, probeResult{}, ownedSnapshotView{}, fmt.Errorf("%w: pane %q is not live", ErrOwnedIdentityMismatch, ref.Pane)
	}
	if !ownedPaneMatches(target, current) {
		return OwnedPaneIdentity{}, probeResult{}, ownedSnapshotView{}, fmt.Errorf("%w: saved target differs from the live snapshot", ErrOwnedIdentityMismatch)
	}
	workspace, ok := view.workspaces[ref.Workspace]
	if !ok || !workspace.matchesOwnedTarget(target) {
		return OwnedPaneIdentity{}, probeResult{}, ownedSnapshotView{}, fmt.Errorf("%w: saved workspace ownership differs from the live snapshot", ErrOwnedIdentityMismatch)
	}
	probed, err := b.probeOwned(ctx, admission)
	if err != nil {
		return OwnedPaneIdentity{}, probeResult{}, ownedSnapshotView{}, err
	}
	return target, probed, view, nil
}

func (b *Backend) verifyOwnedTargetAfter(ctx context.Context, admission ownedAdmission, target OwnedPaneIdentity) error {
	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return err
	}
	current, ok := findOwnedPane(view.panes, target.Ref)
	if !ok || !ownedPaneMatches(target, current) {
		return fmt.Errorf("%w: target changed during operation", ErrOwnedIdentityMismatch)
	}
	workspace, ok := view.workspaces[target.Ref.Workspace]
	if !ok || !workspace.matchesOwnedTarget(target) {
		return fmt.Errorf("%w: workspace ownership changed during operation", ErrOwnedIdentityMismatch)
	}
	return nil
}

func (b *Backend) verifyOwnedFocusAfter(ctx context.Context, admission ownedAdmission, target OwnedPaneIdentity) error {
	view, err := b.ownedSnapshotView(ctx, admission)
	if err != nil {
		return err
	}
	current, ok := findOwnedPane(view.panes, target.Ref)
	if !ok || !ownedPaneMatches(target, current) {
		return fmt.Errorf("%w: target changed during operation", ErrOwnedIdentityMismatch)
	}
	workspace, ok := view.workspaces[target.Ref.Workspace]
	if !ok || !workspace.matchesOwnedTarget(target) {
		return fmt.Errorf("%w: workspace ownership changed during operation", ErrOwnedIdentityMismatch)
	}
	if !workspace.focused {
		return fmt.Errorf("herdr workspace focus returned success but workspace %q is not focused", target.Ref.Workspace)
	}
	return nil
}

func validateOwnedPaneIdentity(target OwnedPaneIdentity, admission ownedAdmission) error {
	if target.Ref.Backend != corebackend.Herdr || strings.TrimSpace(target.Ref.Workspace) == "" || strings.TrimSpace(target.Ref.Pane) == "" {
		return fmt.Errorf("herdr owned request requires an exact herdr workspace and pane reference")
	}
	if target.SessionID != admission.marker.Session || target.SocketPath != admission.marker.SocketPath {
		return fmt.Errorf("herdr owned request targets a foreign session or socket")
	}
	if strings.TrimSpace(target.WorkspaceLabel) == "" {
		return fmt.Errorf("herdr owned request requires a saved workspace ownership label")
	}
	if strings.TrimSpace(target.TerminalID) == "" {
		return fmt.Errorf("herdr owned request requires a saved terminal id")
	}
	if target.WorktreePath != "" {
		if target.RepoKey != admission.marker.GitCommonDir {
			return fmt.Errorf("herdr owned request targets foreign repository provenance")
		}
	} else {
		if target.RepoKey != "" || target.CurrentPath == "" {
			return fmt.Errorf("herdr owned request requires worktree provenance or an exact generic-workspace cwd")
		}
	}
	hasAgentID := strings.TrimSpace(target.AgentID) != ""
	hasSession := target.AgentSession != nil
	if hasAgentID != hasSession || (hasSession && !target.AgentSession.Valid()) {
		return fmt.Errorf("herdr owned request has incomplete optional agent identity")
	}
	return nil
}

func ownedPaneMatches(target OwnedPaneIdentity, current corebackend.LivePane) bool {
	if current.Ref != target.Ref || current.SessionID != target.SessionID || current.SocketPath != target.SocketPath ||
		current.TerminalID != target.TerminalID {
		return false
	}
	if target.WorktreePath != "" {
		if current.WorktreePath != target.WorktreePath || current.RepoKey == "" || current.ProjectRoot == "" {
			return false
		}
		if current.RepoKey != target.RepoKey {
			return false
		}
		if target.CurrentPath != "" && current.CurrentPath != target.CurrentPath {
			return false
		}
	} else if current.RepoKey != "" || current.WorktreePath != "" || current.ProjectRoot != "" || current.CurrentPath != target.CurrentPath {
		return false
	}
	if target.AgentID == "" && target.AgentSession == nil {
		return true
	}
	return current.AgentPresent && current.AgentID == target.AgentID &&
		agentSessionRefsEqual(current.AgentSession, target.AgentSession)
}

func ownedPaneMatchesAfterWorktreeRemoval(target OwnedPaneIdentity, current corebackend.LivePane) bool {
	if current.Ref != target.Ref || current.SessionID != target.SessionID || current.SocketPath != target.SocketPath ||
		current.TerminalID != target.TerminalID || current.RepoKey != "" || current.WorktreePath != "" || current.ProjectRoot != "" {
		return false
	}
	if target.AgentID == "" && target.AgentSession == nil {
		return true
	}
	return current.AgentPresent && current.AgentID == target.AgentID &&
		agentSessionRefsEqual(current.AgentSession, target.AgentSession)
}

func findOwnedPane(panes []corebackend.LivePane, ref corebackend.PaneRef) (corebackend.LivePane, bool) {
	for _, pane := range panes {
		if pane.Ref == ref {
			return pane, true
		}
	}
	return corebackend.LivePane{}, false
}

func cloneOwnedPaneIdentity(target OwnedPaneIdentity) OwnedPaneIdentity {
	target.AgentSession = cloneAgentSessionRef(target.AgentSession)
	return target
}

func cloneOwnedCloseRequest(req OwnedCloseRequest) OwnedCloseRequest {
	req.Target = cloneOwnedPaneIdentity(req.Target)
	return req
}

func cloneAgentSessionRef(ref *corebackend.AgentSessionRef) *corebackend.AgentSessionRef {
	if ref == nil {
		return nil
	}
	cloned := *ref
	return &cloned
}

func agentSessionRefsEqual(left, right *corebackend.AgentSessionRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
