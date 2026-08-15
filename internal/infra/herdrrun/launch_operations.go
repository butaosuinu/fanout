package herdrrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type (
	PaneProcess      = corebackend.PaneProcess
	PaneProcessInfo  = corebackend.PaneProcessInfo
	OwnedLaunchRoute = corebackend.OwnedLaunchRoute
)

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
		SocketPath: s.SocketPath, LauncherPath: s.LauncherPath,
		EmitterPath: s.EmitterPath, ControlPath: s.ControlPath,
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
	out, err := s.runOwnedLaunchMutationCommand(ctx, commandTimeout, "pane", "run", paneID, launcherStartToken(nonce))
	if err != nil {
		return err
	}
	return validatePaneRunResponse(out)
}

// IssueRestartResume keeps one owned admission and probe from launcher
// preflight through token issuance. The caller holds the repository state and
// intent lock and persists markIssued immediately before the pane mutation.
func (s *OwnedSession) IssueRestartResume(
	ctx context.Context,
	paneID, nonce string,
	deadline time.Time,
	preflight func(PaneProcessInfo, []corebackend.LivePane) error,
	markIssued func() error,
) error {
	if preflight == nil || markIssued == nil {
		return fmt.Errorf("invalid Herdr restart resume request")
	}
	if _, err := remainingRestartResumeTime(deadline, time.Now()); err != nil {
		return err
	}
	if err := s.requireRestartResumeToken(paneID, nonce); err != nil {
		return err
	}
	probed, lock, err := s.admitRestartResume(
		ctx, paneID, nonce, deadline, preflight,
	)
	if err != nil {
		return err
	}
	defer unlockPrivateFile(lock)
	return s.issueRestartResumeToken(ctx, probed, paneID, nonce, deadline, markIssued)
}

func (s *OwnedSession) issueRestartResumeToken(
	ctx context.Context,
	probed probeResult,
	paneID, nonce string,
	deadline time.Time,
	markIssued func() error,
) error {
	if _, err := remainingRestartResumeTime(deadline, s.backend.now()); err != nil {
		return err
	}
	if markErr := markIssued(); markErr != nil {
		return fmt.Errorf("persist issued Herdr restart resume token: %w", markErr)
	}
	remaining, err := remainingRestartResumeTime(deadline, s.backend.now())
	if err != nil {
		return err
	}
	callTimeout := min(commandTimeout, remaining)
	out, err := s.backend.runContext(
		ctx, callTimeout, probed.binary, probed.route,
		"pane", "run", paneID, launcherStartToken(nonce),
	)
	if err != nil {
		return err
	}
	return validateRestartResumeResponse(out)
}

func (s *OwnedSession) admitRestartResume(
	ctx context.Context,
	paneID, nonce string,
	deadline time.Time,
	preflight func(PaneProcessInfo, []corebackend.LivePane) error,
) (probeResult, *os.File, error) {
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return probeResult{}, nil, err
	}
	probed, err := s.backend.probeOwned(ctx, admission)
	if err != nil {
		unlockPrivateFile(lock)
		return probeResult{}, nil, err
	}
	remaining, err := remainingRestartResumeTime(deadline, s.backend.now())
	if err != nil {
		unlockPrivateFile(lock)
		return probeResult{}, nil, err
	}
	if waitErr := s.waitForLauncherProbed(ctx, probed, paneID, nonce, remaining); waitErr != nil {
		unlockPrivateFile(lock)
		return probeResult{}, nil, waitErr
	}
	info, panes, err := s.observeRestartResumeProbed(ctx, probed, paneID, deadline)
	if err == nil {
		err = preflight(info, panes)
	}
	if err != nil {
		unlockPrivateFile(lock)
		return probeResult{}, nil, err
	}
	return probed, lock, nil
}

func (s *OwnedSession) waitForLauncherProbed(
	ctx context.Context,
	probed probeResult,
	paneID, nonce string,
	totalTimeout time.Duration,
) error {
	marker := launcherReadyMarker(nonce)
	out, err := s.backend.runContext(ctx, totalTimeout, probed.binary, probed.route,
		"pane", "wait-output", paneID, "--match", marker,
		"--source", "recent-unwrapped", "--timeout", strconv.FormatInt(totalTimeout.Milliseconds(), 10))
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

func remainingRestartResumeTime(deadline, now time.Time) (time.Duration, error) {
	remaining := deadline.Sub(now)
	if deadline.IsZero() || remaining <= 0 {
		return 0, fmt.Errorf("herdr restart resume intent expired")
	}
	return remaining, nil
}

func restartResumeCallTimeout(deadline time.Time) time.Duration {
	return min(commandTimeout, time.Until(deadline))
}

func (s *OwnedSession) requireRestartResumeToken(paneID, nonce string) error {
	validRequest := !slices.Contains([]bool{
		s != nil, s != nil && s.backend != nil, s != nil && s.ControlPath != "",
		paneID != "", workloadLaunchNonce.MatchString(nonce),
	}, false)
	if !validRequest {
		return fmt.Errorf("invalid Herdr restart resume token request")
	}
	journal, err := state.LoadHerdrIntentsPath(s.ControlPath)
	if err != nil {
		return err
	}
	server, found, err := journal.ServerLifecycleIntent()
	validLifecycle := !slices.Contains([]bool{
		err == nil, found, server.Kind == state.HerdrIntentRestart,
		server.Server != nil, serverRestartTokenMatches(server.Server, s),
	}, false)
	if !validLifecycle {
		return fmt.Errorf("herdr restart resume token requires an active server restart intent")
	}
	matches := 0
	for _, intent := range journal.Intents {
		if exactRestartResumeTokenIntent(intent, s.Session, s.SocketPath, paneID, nonce) {
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("herdr restart resume token has %d exact intents", matches)
	}
	return nil
}

func serverRestartTokenMatches(server *state.HerdrServerIdentity, session *OwnedSession) bool {
	return server != nil && session != nil && server.GitCommonDir == session.GitCommonDir &&
		server.RuntimeDir == session.RuntimeDir && server.Session == session.Session &&
		server.SocketPath == session.SocketPath && server.ClientSocketPath == session.ClientSocketPath
}

func exactRestartResumeTokenIntent(intent state.HerdrIntent, session, socketPath, paneID, nonce string) bool {
	return intent.Kind == state.HerdrIntentResume && intent.Status == state.HerdrIntentRealized &&
		intent.Session == session && intent.SocketPath == socketPath &&
		intent.Resource.PaneID == paneID && intent.Launch != nil && intent.Launch.Nonce == nonce &&
		!intent.Launch.TokenIssued
}

func validatePaneRunResponse(out []byte) error {
	var envelope paneRunEnvelope
	if err := decodeOne(out, &envelope); err != nil || envelope.ID != "cli:pane:run" ||
		envelope.Result == nil || envelope.Result.Type != "ok" {
		return fmt.Errorf("herdr pane run returned an unexpected response")
	}
	return nil
}

func validateRestartResumeResponse(out []byte) error {
	if len(out) == 0 {
		return nil
	}
	return validatePaneRunResponse(out)
}

func (s *OwnedSession) ProcessInfo(ctx context.Context, paneID string) (PaneProcessInfo, error) {
	if s == nil || s.backend == nil {
		return PaneProcessInfo{}, fmt.Errorf("herdr owned session is nil")
	}
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return PaneProcessInfo{}, err
	}
	defer unlockPrivateFile(lock)
	probed, err := s.backend.probeOwned(ctx, admission)
	if err != nil {
		return PaneProcessInfo{}, err
	}
	return s.processInfoProbed(ctx, probed, paneID, commandTimeout)
}

func (s *OwnedSession) processInfoProbed(
	ctx context.Context,
	probed probeResult,
	paneID string,
	timeout time.Duration,
) (PaneProcessInfo, error) {
	if timeout <= 0 {
		return PaneProcessInfo{}, context.DeadlineExceeded
	}
	out, err := s.backend.runContext(ctx, timeout, probed.binary, probed.route,
		"pane", "process-info", "--pane", paneID)
	if err != nil {
		return PaneProcessInfo{}, observationCommandError("herdr pane process-info", err)
	}
	processInfo, err := decodePaneProcessInfo(out, paneID)
	if err != nil {
		return PaneProcessInfo{}, err
	}
	processes, err := s.inspectNormalizedPaneProcesses(ctx, processInfo.ForegroundProcesses)
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

func decodePaneProcessInfo(out []byte, paneID string) (PaneProcessInfo, error) {
	var envelope paneProcessInfoEnvelope
	if decodeErr := decodeOne(out, &envelope); decodeErr != nil || envelope.ID != "cli:pane:process_info" ||
		envelope.Result == nil || envelope.Result.Type != "pane_process_info" ||
		envelope.Result.ProcessInfo.PaneID != paneID {
		return PaneProcessInfo{}, fmt.Errorf("herdr pane process-info returned an unexpected response")
	}
	return envelope.Result.ProcessInfo, nil
}

// ObserveRestartResume returns process and pane identity from one owned probe.
func (s *OwnedSession) ObserveRestartResume(
	ctx context.Context,
	paneID string,
) (PaneProcessInfo, []corebackend.LivePane, error) {
	if s == nil || s.backend == nil {
		return PaneProcessInfo{}, nil, fmt.Errorf("herdr owned session is nil")
	}
	admission, lock, err := s.backend.acquireOwnedOperation(ctx)
	if err != nil {
		return PaneProcessInfo{}, nil, err
	}
	defer unlockPrivateFile(lock)
	probed, err := s.backend.probeOwned(ctx, admission)
	if err != nil {
		return PaneProcessInfo{}, nil, err
	}
	return s.observeRestartResumeProbed(ctx, probed, paneID, time.Now().Add(commandTimeout))
}

func (s *OwnedSession) observeRestartResumeProbed(
	ctx context.Context,
	probed probeResult,
	paneID string,
	deadline time.Time,
) (PaneProcessInfo, []corebackend.LivePane, error) {
	info, err := s.processInfoProbed(ctx, probed, paneID, restartResumeCallTimeout(deadline))
	if err != nil {
		return PaneProcessInfo{}, nil, err
	}
	panes, err := s.backend.snapshot(ctx, restartResumeCallTimeout(deadline), probed)
	return info, panes, err
}

func (s *OwnedSession) RenameAgent(ctx context.Context, paneID, name string) error {
	out, err := s.runOwnedLaunchMutationCommand(ctx, commandTimeout, "agent", "rename", paneID, name)
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
	if strings.TrimSpace(workspaceID) == "" || path == "" {
		return mutationNotIssued(fmt.Errorf("herdr worktree remove identity is incomplete"))
	}
	out, err := s.runOwnedMutationCommand(ctx, commandTimeout,
		"worktree", "remove", "--workspace", workspaceID, "--json")
	if err != nil {
		if rejected, ok := decodeMutationRejection(out, "cli:worktree:remove"); ok {
			return rejected
		}
		return err
	}
	return validateWorktreeRemoveResponse(out, workspaceID, path)
}

// CloseWorkspace removes a residual child workspace after its checkout is
// already absent. The caller verifies the workspace identity and postcondition.
func (s *OwnedSession) CloseWorkspace(ctx context.Context, workspaceID string) error {
	if strings.TrimSpace(workspaceID) == "" {
		return mutationNotIssued(fmt.Errorf("herdr workspace close requires a workspace id"))
	}
	out, err := s.runOwnedMutationCommand(ctx, commandTimeout, "workspace", "close", workspaceID)
	if err != nil {
		if rejected, ok := decodeMutationRejection(out, "cli:workspace:close"); ok {
			return rejected
		}
	}
	return err
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

// WaitRestoredPanes applies Backend.Wait's single total budget to the exact
// owned route after a server restart.
func (s *OwnedSession) WaitRestoredPanes(
	ctx context.Context,
	totalTimeout time.Duration,
	match func([]corebackend.LivePane) bool,
) WaitResult {
	if s == nil || s.backend == nil {
		return failedWait(fmt.Errorf("herdr owned session is nil"))
	}
	return s.backend.Wait(ctx, totalTimeout, match)
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
	return s.runOwnedCommand(ctx, timeout, (*Backend).acquireOwnedOperation, args...)
}

func (s *OwnedSession) runOwnedLaunchMutationCommand(
	ctx context.Context,
	timeout time.Duration,
	args ...string,
) ([]byte, error) {
	return s.runOwnedCommand(ctx, timeout, (*Backend).acquireOwnedMutation, args...)
}

func (s *OwnedSession) runOwnedCommand(
	ctx context.Context,
	timeout time.Duration,
	acquire func(*Backend, context.Context) (ownedAdmission, *os.File, error),
	args ...string,
) ([]byte, error) {
	if s == nil || s.backend == nil {
		return nil, fmt.Errorf("herdr owned session is nil")
	}
	admission, lock, err := acquire(s.backend, ctx)
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

func (s *OwnedSession) runOwnedMutationCommand(
	ctx context.Context,
	timeout time.Duration,
	args ...string,
) ([]byte, error) {
	if s == nil || s.backend == nil {
		return nil, mutationNotIssued(fmt.Errorf("herdr owned session is nil"))
	}
	admission, lock, err := s.backend.acquireOwnedMutation(ctx)
	if err != nil {
		return nil, mutationNotIssued(err)
	}
	defer unlockPrivateFile(lock)
	probed, err := s.backend.probeOwned(ctx, admission)
	if err != nil {
		return nil, mutationNotIssued(err)
	}
	if timeout <= 0 {
		return nil, mutationNotIssued(context.DeadlineExceeded)
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.backend.runWorktreeMutation(callCtx, probed.binary, probed.route, args...)
}
