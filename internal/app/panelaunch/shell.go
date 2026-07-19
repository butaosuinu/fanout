package panelaunch

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/panelayout"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

// ShellRequest describes one plain terminal pane to open.
type ShellRequest struct {
	// TargetPath is the directory the shell starts in.
	TargetPath string
	// Root marks the project-root terminal (names the pane "root terminal").
	Root bool
}

var (
	closePaneForCleanup = tmuxrun.ClosePaneIfOwned
	closeFreshPane      = tmuxrun.CloseFreshPane
)

// Shell opens a plain tmux shell pane at req.TargetPath and records it as a
// keyed @manual shell row in l.Info.ProjectRoot's state. Plain TUI terminals
// are part of tmux session management, not backend agent launch, so this path
// deliberately stays outside backend.Backend. It locks that project's state
// itself; only l.Info is used.
func (l *Launcher) Shell(req ShellRequest) error {
	projectRoot := l.Info.ProjectRoot
	target := l.Info.Target
	rawPath := strings.TrimSpace(req.TargetPath)
	if rawPath == "" {
		return fmt.Errorf("terminal path is required")
	}
	targetPath, err := filepath.Abs(rawPath)
	if err != nil {
		return fmt.Errorf("resolve terminal path: %w", err)
	}
	st, statErr := os.Stat(targetPath)
	if statErr != nil {
		return fmt.Errorf("terminal path: %w", statErr)
	} else if !st.IsDir() {
		return fmt.Errorf("terminal path is not a directory: %s", targetPath)
	}

	if excludeErr := worktree.EnsureLocalExclude(projectRoot); excludeErr != nil {
		return fmt.Errorf("prepare local git exclude: %w", excludeErr)
	}

	recorder, err := state.LockProject(projectRoot)
	if err != nil {
		return err
	}
	defer func() {
		_ = recorder.Unlock()
	}()

	number := NextSyntheticPaneNumber(recorder.Store, ManualParentRef)
	slug := shellPaneSlug(targetPath, req.Root, number)
	title := shellPaneTitle(targetPath, req.Root)
	shellKey, err := NewShellPaneKey()
	if err != nil {
		return err
	}
	paneID, err := tmuxrun.SplitPane(target, targetPath)
	if err != nil {
		return err
	}
	entry := state.Pane{
		Parent:       ManualParentRef,
		IssueNum:     number,
		Kind:         state.PaneKindShell,
		Slug:         slug,
		PaneID:       paneID,
		ShellKey:     shellKey,
		Agent:        state.PaneKindShell,
		DisplayName:  title,
		WorktreePath: targetPath,
		CreatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	if err := setPaneLivenessKey(paneID, shellKey); err != nil {
		stampErr := fmt.Errorf("set terminal pane liveness key: %w", err)
		if cleanupErr := cleanupFreshShellPane(target, paneID); cleanupErr != nil {
			recoveryErr := recorder.RecordPane(entry)
			if recoveryErr != nil {
				recoveryErr = fmt.Errorf("preserve live terminal pane %s in fanout state: %w", paneID, recoveryErr)
			}
			return errors.Join(stampErr, fmt.Errorf("stop unstamped terminal pane %s: %w", paneID, cleanupErr), recoveryErr)
		}
		return stampErr
	}
	// Shell pane ergonomics are best-effort; the recorded pane id is enough to
	// keep the terminal usable when tmux metadata/layout updates fail.
	_ = tmuxrun.SetPaneTitle(paneID, title)
	_ = tmuxrun.SetPaneLabel(paneID, BorderLabel(ManualParentRef, title))
	_ = tmuxrun.EnablePaneBorderTitles(paneID)
	_ = tmuxrun.SetPaneProjectRoot(paneID, projectRoot)
	_ = tmuxrun.SetPaneWorktreePath(paneID, targetPath)
	if err := recorder.RecordPane(entry); err != nil {
		writeErr := fmt.Errorf("write fanout state: %w", err)
		if cleanupShellPane(target, paneID, targetPath, shellKey) {
			removeErr := recorder.RemovePane(ManualParentRef, number)
			if removeErr != nil {
				removeErr = fmt.Errorf("remove stopped terminal pane from fanout state: %w", removeErr)
			}
			return errors.Join(writeErr, removeErr)
		}
		recoveryErr := recorder.RecordPane(entry)
		if recoveryErr != nil {
			recoveryErr = fmt.Errorf("preserve live terminal pane %s in fanout state: %w", paneID, recoveryErr)
		}
		return errors.Join(writeErr, fmt.Errorf("terminal pane %s remains live", paneID), recoveryErr)
	}
	// Re-layout only after the pane is recorded, so a failed/rolled-back launch
	// never leaves the window arranged around a pane that no longer exists or an
	// orphaned spacer behind.
	_ = panelayout.Apply(target, panelayout.Create)
	return nil
}

func cleanupFreshShellPane(relayoutTarget, paneID string) error {
	if err := closeFreshPane(paneID); err != nil {
		return err
	}
	_ = panelayout.Apply(relayoutTarget, panelayout.Close)
	return nil
}

func cleanupShellPane(relayoutTarget, paneID, expectedWorktreePath, shellKey string) bool {
	result, err := closePaneForCleanup(paneID, expectedWorktreePath, shellKey)
	if err != nil || result.Status == tmuxrun.ClosePaneFailed {
		return false
	}
	if result.Status == tmuxrun.ClosePaneClosed {
		target := result.WindowID
		if target == "" {
			target = relayoutTarget
		}
		_ = panelayout.Apply(target, panelayout.Close)
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
