package panelaunch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// ShellRequest describes one plain terminal pane to open.
type ShellRequest struct {
	// TargetPath is the directory the shell starts in.
	TargetPath string
	// Root marks the project-root terminal (names the pane "root terminal").
	Root bool
}

// relayoutShellWindow re-tiles the window a rolled-back terminal pane left
// behind, through the launch's own runtime rather than a runtime this package
// constructs. A runtime that arranges its own workspace exposes no layout
// capability and the repair is skipped — the same rule the successful launch
// path above already follows.
func relayoutShellWindow(runtimeBackend backend.Backend, target string) {
	manager, ok := backend.AsLayoutManager(runtimeBackend)
	if !ok {
		return
	}
	_ = manager.Relayout(target, backend.LayoutClose)
}

// Shell opens a plain shell pane at req.TargetPath and records it as an
// @manual shell row in l.Info.ProjectRoot's state. Tmux rows use a liveness
// key; Herdr rows persist the exact owned-session identity.
func (l *Launcher) Shell(req ShellRequest) error {
	if l.Backend == nil {
		return fmt.Errorf("runtime backend is not configured")
	}
	projectRoot := l.Info.ProjectRoot
	targetPath, err := resolveShellTarget(req.TargetPath)
	if err != nil {
		return err
	}
	if excludeErr := worktree.EnsureLocalExclude(projectRoot); excludeErr != nil {
		return fmt.Errorf("prepare local git exclude: %w", excludeErr)
	}

	recorder, err := l.lockShellState(projectRoot)
	if err != nil {
		return err
	}
	defer func() {
		_ = recorder.Unlock()
	}()

	title := shellPaneTitle(targetPath, req.Root)
	if l.Backend.MutationModel() == backend.MutationJournaled {
		return l.shellManagedAllocated(recorder, targetPath, req.Root, title)
	}
	number := NextSyntheticPaneNumber(recorder.Store, ManualParentRef)
	return l.shellDirect(recorder, targetPath, number, shellPaneSlug(targetPath, req.Root, number), title)
}

func (l *Launcher) shellManagedAllocated(
	recorder *state.LockedStore,
	targetPath string,
	root bool,
	title string,
) error {
	if l.Managed == nil {
		return fmt.Errorf("herdr terminal launch requires an owned session")
	}
	projectRoot := l.Info.ProjectRoot
	ReclaimManagedSyntheticPaneNumber(
		context.Background(), projectRoot, recorder, ManualParentRef, l.Managed,
	)
	number := NextManagedSyntheticPaneNumber(projectRoot, recorder.Store, ManualParentRef)
	if err := admitManagedCoordinatorLaunch(recorder, projectRoot, number); err != nil {
		return err
	}
	return l.shellManaged(recorder, targetPath, number, shellPaneSlug(targetPath, root, number), title)
}

func resolveShellTarget(rawPath string) (string, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", fmt.Errorf("terminal path is required")
	}
	targetPath, err := filepath.Abs(rawPath)
	if err != nil {
		return "", fmt.Errorf("resolve terminal path: %w", err)
	}
	st, err := os.Stat(targetPath)
	if err != nil {
		return "", fmt.Errorf("terminal path: %w", err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("terminal path is not a directory: %s", targetPath)
	}
	return targetPath, nil
}

// shellDirect opens the terminal pane on a runtime that realizes a launch
// immediately. The pane runs no command, so the launch request carries neither
// a command nor a start gate; the runtime hands back the pane id synchronously.
func (l *Launcher) shellDirect(
	recorder *state.LockedStore,
	targetPath string,
	number int,
	slug, title string,
) error {
	target := l.Info.Target
	shellKey, err := NewShellPaneKey()
	if err != nil {
		return err
	}
	ref, err := l.Backend.Launch(backend.LaunchRequest{Target: target, WorktreePath: targetPath})
	if err != nil {
		return err
	}
	paneID := ref.Pane
	entry := newShellPaneEntry(number, slug, paneID, shellKey, title, targetPath)
	if err := stampShellPaneLiveness(l.Backend, paneID, shellKey); err != nil {
		return recoverUnstampedShell(l.Backend, recorder, target, entry, err)
	}
	// Shell pane ergonomics are best-effort; the recorded pane id is enough to
	// keep the terminal usable when tmux metadata/layout updates fail.
	decorateShellPane(l.Backend, paneID, title, l.Info.ProjectRoot, targetPath)
	if err := recorder.RecordPane(entry); err != nil {
		return recoverUnrecordedShell(l.Backend, recorder, target, entry, err)
	}
	// Re-layout only after the pane is recorded, so a failed/rolled-back launch
	// never leaves the window arranged around a pane that no longer exists or an
	// orphaned spacer behind.
	if manager, ok := backend.AsLayoutManager(l.Backend); ok {
		_ = manager.Relayout(target, backend.LayoutCreate)
	}
	return nil
}

func newShellPaneEntry(
	number int,
	slug, paneID, shellKey, title, targetPath string,
) state.Pane {
	return state.Pane{
		Parent: ManualParentRef, IssueNum: number, Kind: state.PaneKindShell,
		Slug: slug, PaneID: paneID, ShellKey: shellKey, Agent: state.PaneKindShell,
		DisplayName: title, WorktreePath: targetPath,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// stampShellPaneLiveness applies the terminal pane's liveness token. A backend
// that cannot stamp one could never prove the recorded row live again, so the
// caller treats the missing capability exactly like a failed stamp.
func stampShellPaneLiveness(runtimeBackend backend.Backend, paneID, shellKey string) error {
	stamper, ok := backend.AsLivenessStamper(runtimeBackend)
	if !ok {
		if runtimeBackend == nil {
			return fmt.Errorf("runtime backend is not configured")
		}
		return backend.Unsupported(runtimeBackend.Name(), "pane liveness keys")
	}
	return stamper.StampPaneShellKey(paneID, shellKey)
}

func decorateShellPane(runtimeBackend backend.Backend, paneID, title, projectRoot, targetPath string) {
	decorator, ok := backend.AsPaneDecorator(runtimeBackend)
	if !ok {
		return
	}
	_ = decorator.SetPaneTitle(paneID, title)
	_ = decorator.SetPaneLabel(paneID, BorderLabel(ManualParentRef, title))
	_ = decorator.EnablePaneBorderTitles(paneID)
	_ = decorator.SetPaneProjectRoot(paneID, projectRoot)
	_ = decorator.SetPaneWorktreePath(paneID, targetPath)
}

func recoverUnstampedShell(
	runtimeBackend backend.Backend,
	recorder *state.LockedStore,
	target string,
	entry state.Pane,
	stampCause error,
) error {
	stampErr := fmt.Errorf("set terminal pane liveness key: %w", stampCause)
	if cleanupErr := cleanupFreshPane(runtimeBackend, target, entry.PaneID); cleanupErr != nil {
		recoveryErr := recorder.RecordPane(entry)
		if recoveryErr != nil {
			recoveryErr = fmt.Errorf("preserve live terminal pane %s in fanout state: %w", entry.PaneID, recoveryErr)
		}
		return errors.Join(
			stampErr,
			fmt.Errorf("stop unstamped terminal pane %s: %w", entry.PaneID, cleanupErr),
			recoveryErr,
		)
	}
	return stampErr
}

func recoverUnrecordedShell(
	runtimeBackend backend.Backend,
	recorder *state.LockedStore,
	target string,
	entry state.Pane,
	recordCause error,
) error {
	writeErr := fmt.Errorf("write fanout state: %w", recordCause)
	if cleanupShellPane(runtimeBackend, target, entry.PaneID, entry.WorktreePath, entry.ShellKey) {
		removeErr := recorder.RemovePane(ManualParentRef, entry.IssueNum)
		if removeErr != nil {
			removeErr = fmt.Errorf("remove stopped terminal pane from fanout state: %w", removeErr)
		}
		return errors.Join(writeErr, removeErr)
	}
	recoveryErr := recorder.RecordPane(entry)
	if recoveryErr != nil {
		recoveryErr = fmt.Errorf("preserve live terminal pane %s in fanout state: %w", entry.PaneID, recoveryErr)
	}
	return errors.Join(
		writeErr,
		fmt.Errorf("terminal pane %s remains live", entry.PaneID),
		recoveryErr,
	)
}

// lockShellState takes the lock the launch lane needs. The journaled lane
// reads and writes its intent journal under the same lock as the state row, so
// it takes the combined launch lock rather than the plain state lock.
func (l *Launcher) lockShellState(projectRoot string) (*state.LockedStore, error) {
	if l.Backend.MutationModel() == backend.MutationJournaled {
		return state.LockProjectForLaunch(projectRoot)
	}
	return state.LockProject(projectRoot)
}

// cleanupShellPane rolls a recorded terminal pane back through the runtime's
// identity-gated close and reports whether the pane is confirmed gone. A
// runtime without that capability cannot prove the pane stopped, so the caller
// keeps the state row rather than dropping a possibly-live terminal.
func cleanupShellPane(runtimeBackend backend.Backend, relayoutTarget, paneID, expectedWorktreePath, shellKey string) bool {
	closer, ok := runtimeBackend.(backend.OwnedCloser)
	if !ok {
		return false
	}
	result, err := closer.CloseOwned(backend.CloseRequest{
		Ref:          backend.PaneRef{Backend: runtimeBackend.Name(), Pane: paneID},
		WorktreePath: expectedWorktreePath,
		ShellKey:     shellKey,
	})
	if err != nil || result.Status == backend.CloseFailed {
		return false
	}
	if result.Status == backend.CloseConfirmed {
		target := result.ContainerID
		if target == "" {
			target = relayoutTarget
		}
		relayoutShellWindow(runtimeBackend, target)
	}
	return true
}

// NewShellPaneKey generates a random @fanout_shell_key liveness token. The
// historical name is retained, but the token now identifies every fanout pane.
func NewShellPaneKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate pane identity: %w", err)
	}
	return "shell-" + hex.EncodeToString(b[:]), nil
}

func shellPaneSlug(targetPath string, root bool, number int) string {
	base := "root"
	if !root {
		base = SanitizeSessionPart(filepath.Base(targetPath))
	}
	if base == "" {
		base = "terminal"
	}
	n := number
	if n < 0 {
		n = -n
	}
	return fmt.Sprintf("terminal-%s-%d", base, n)
}

func shellPaneTitle(targetPath string, root bool) string {
	if root {
		return "root terminal"
	}
	base := strings.TrimSpace(filepath.Base(targetPath))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "worktree"
	}
	return "terminal " + base
}

// SanitizeSessionPart mirrors cmd/fanout's tmux session-name sanitizer
// (cmd/fanout/tui.go): lowercase [a-z0-9] runs joined by single dashes.
func SanitizeSessionPart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		allowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if allowed {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "repo"
	}
	return out
}
