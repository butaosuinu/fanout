package herdrrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

type PaneProcess struct {
	PID          int      `json:"pid"`
	ParentPID    int      `json:"-"`
	ProcessGroup int      `json:"-"`
	Executable   string   `json:"-"`
	Name         string   `json:"name"`
	Argv         []string `json:"argv"`
	Argv0        string   `json:"argv0"`
	Cmdline      string   `json:"cmdline"`
	CWD          string   `json:"cwd"`
}

type PaneProcessInfo struct {
	PaneID                 string        `json:"pane_id"`
	ShellPID               int           `json:"shell_pid"`
	ForegroundProcessGroup int           `json:"foreground_process_group_id"`
	ForegroundProcesses    []PaneProcess `json:"foreground_processes"`
}

type OwnedLaunchRoute struct {
	GitCommonDir string
	RuntimeDir   string
	Session      string
	SocketPath   string
	LauncherPath string
	ControlPath  string
}

type paneProcessInfoEnvelope struct {
	ID     string                 `json:"id"`
	Result *paneProcessInfoResult `json:"result"`
}

type paneProcessInfoResult struct {
	Type        string          `json:"type"`
	ProcessInfo PaneProcessInfo `json:"process_info"`
}

type waitOutputEnvelope struct {
	ID     string            `json:"id"`
	Result *waitOutputResult `json:"result"`
}

type waitOutputResult struct {
	Type        string `json:"type"`
	PaneID      string `json:"pane_id"`
	MatchedLine string `json:"matched_line"`
}

type paneRunEnvelope struct {
	ID     string         `json:"id"`
	Result *paneRunResult `json:"result"`
}

type paneRunResult struct {
	Type string `json:"type"`
}

type agentRenameEnvelope struct {
	ID     string           `json:"id"`
	Result *json.RawMessage `json:"result"`
}

type worktreeRemoveEnvelope struct {
	ID     string                `json:"id"`
	Result *worktreeRemoveResult `json:"result"`
}

type worktreeRemoveResult struct {
	Type        string `json:"type"`
	WorkspaceID string `json:"workspace_id"`
	Path        string `json:"path"`
	Forced      *bool  `json:"forced"`
}

// VerifyOwned revalidates the immutable marker, supervisor, sockets, pinned
// binaries, and exact route for one long-lived launch caller.
func (s *OwnedSession) VerifyOwned(ctx context.Context) error {
	if s == nil || s.backend == nil {
		return fmt.Errorf("herdr owned session is nil")
	}
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return err
	}
	defer unlockPrivateFile(lock)
	_, err = s.backend.probeOwned(ctx, admission)
	return err
}

func (s *OwnedSession) LaunchRoute() (OwnedLaunchRoute, error) {
	if s == nil || s.backend == nil {
		return OwnedLaunchRoute{}, fmt.Errorf("herdr owned session is nil")
	}
	return OwnedLaunchRoute{
		GitCommonDir: s.GitCommonDir, RuntimeDir: s.RuntimeDir, Session: s.Session,
		SocketPath: s.SocketPath, LauncherPath: s.LauncherPath, ControlPath: s.ControlPath,
	}, nil
}

func (s *OwnedSession) WaitForLauncher(
	ctx context.Context,
	paneID, nonce string,
	totalTimeout time.Duration,
) error {
	if totalTimeout <= 0 {
		return fmt.Errorf("herdr launcher intent has expired")
	}
	marker := launcherReadyMarker(nonce)
	out, err := s.runOwnedLaunchCommand(ctx, totalTimeout, "pane", "wait-output", paneID,
		"--match", marker, "--source", "recent-unwrapped", "--timeout", strconv.FormatInt(totalTimeout.Milliseconds(), 10))
	if err != nil {
		return err
	}
	var envelope waitOutputEnvelope
	if err := decodeOne(out, &envelope); err != nil || envelope.ID != "cli:pane:wait-output" ||
		envelope.Result == nil || envelope.Result.Type != "output_matched" ||
		envelope.Result.PaneID != paneID || !strings.Contains(envelope.Result.MatchedLine, marker) {
		return fmt.Errorf("herdr pane wait-output returned an unexpected launcher marker response")
	}
	return nil
}

func (s *OwnedSession) SendLaunchToken(ctx context.Context, paneID, nonce string) error {
	out, err := s.runOwnedLaunchCommand(ctx, commandTimeout, "pane", "run", paneID, launcherStartToken(nonce))
	if err != nil {
		return err
	}
	return validatePaneRunResponse(out)
}

func validatePaneRunResponse(out []byte) error {
	var envelope paneRunEnvelope
	if err := decodeOne(out, &envelope); err != nil || envelope.ID != "cli:pane:run" ||
		envelope.Result == nil || envelope.Result.Type != "ok" {
		return fmt.Errorf("herdr pane run returned an unexpected response")
	}
	return nil
}

func (s *OwnedSession) ProcessInfo(ctx context.Context, paneID string) (PaneProcessInfo, error) {
	out, err := s.runOwnedLaunchCommand(ctx, commandTimeout, "pane", "process-info", "--pane", paneID)
	if err != nil {
		return PaneProcessInfo{}, observationCommandError("herdr pane process-info", err)
	}
	var envelope paneProcessInfoEnvelope
	if decodeErr := decodeOne(out, &envelope); decodeErr != nil || envelope.ID != "cli:pane:process_info" ||
		envelope.Result == nil || envelope.Result.Type != "pane_process_info" ||
		envelope.Result.ProcessInfo.PaneID != paneID {
		return PaneProcessInfo{}, fmt.Errorf("herdr pane process-info returned an unexpected response")
	}
	processInfo := envelope.Result.ProcessInfo
	processes, err := s.inspectPaneProcesses(ctx, processInfo.ForegroundProcesses)
	if err != nil {
		wrapped := fmt.Errorf("inspect herdr pane process ancestry: %w", err)
		if errors.Is(err, errPaneProcessChanged) || retryableCommandError(err) {
			return PaneProcessInfo{}, retryableObservationError{err: wrapped}
		}
		return PaneProcessInfo{}, wrapped
	}
	processInfo.ForegroundProcesses = processes
	return processInfo, nil
}

func (s *OwnedSession) RenameAgent(ctx context.Context, paneID, name string) error {
	out, err := s.runOwnedLaunchCommand(ctx, commandTimeout, "agent", "rename", paneID, name)
	if err != nil {
		return err
	}
	var envelope agentRenameEnvelope
	if err := decodeOne(out, &envelope); err != nil || envelope.ID != "cli:agent:rename" || envelope.Result == nil {
		return fmt.Errorf("herdr agent rename returned an unexpected response")
	}
	return nil
}

// RemoveWorktree issues the non-force rollback mutation for one identity-
// fenced child workspace. The caller verifies absence before Git cleanup.
func (s *OwnedSession) RemoveWorktree(ctx context.Context, workspaceID, path string) error {
	out, err := s.runOwnedLaunchCommand(ctx, commandTimeout,
		"worktree", "remove", "--workspace", workspaceID, "--json")
	if err != nil {
		return err
	}
	return validateWorktreeRemoveResponse(out, workspaceID, path)
}

func validateWorktreeRemoveResponse(out []byte, workspaceID, path string) error {
	var envelope worktreeRemoveEnvelope
	if err := decodeOne(out, &envelope); err != nil || envelope.ID != "cli:worktree:remove" ||
		envelope.Result == nil || envelope.Result.Type != "worktree_removed" ||
		envelope.Result.WorkspaceID != workspaceID || envelope.Result.Path != path ||
		envelope.Result.Forced == nil || *envelope.Result.Forced {
		return fmt.Errorf("herdr worktree remove returned an unexpected response")
	}
	return nil
}

func (s *OwnedSession) LivePanes(ctx context.Context) ([]corebackend.LivePane, error) {
	if s == nil || s.backend == nil {
		return nil, fmt.Errorf("herdr owned session is nil")
	}
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return nil, observationCommandError("acquire Herdr observation", err)
	}
	defer unlockPrivateFile(lock)
	probed, err := s.backend.probeOwned(ctx, admission)
	if err != nil {
		return nil, observationCommandError("verify Herdr observation route", err)
	}
	return s.backend.snapshot(ctx, commandTimeout, probed)
}

func observationCommandError(operation string, err error) error {
	wrapped := fmt.Errorf("%s: %w", operation, err)
	if retryableCommandError(err) {
		return retryableObservationError{err: wrapped}
	}
	return wrapped
}

func (s *OwnedSession) runOwnedLaunchCommand(
	ctx context.Context,
	timeout time.Duration,
	args ...string,
) ([]byte, error) {
	if s == nil || s.backend == nil {
		return nil, fmt.Errorf("herdr owned session is nil")
	}
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer unlockPrivateFile(lock)
	probed, err := s.backend.probeOwned(ctx, admission)
	if err != nil {
		return nil, err
	}
	return s.backend.runContext(ctx, timeout, probed.binary, probed.route, args...)
}
