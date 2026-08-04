package herdrrun

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
)

type PaneProcess struct {
	PID     int      `json:"pid"`
	Name    string   `json:"name"`
	Argv    []string `json:"argv"`
	Argv0   string   `json:"argv0"`
	Cmdline string   `json:"cmdline"`
	CWD     string   `json:"cwd"`
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

type agentRenameEnvelope struct {
	ID     string           `json:"id"`
	Result *json.RawMessage `json:"result"`
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
		return fmt.Errorf("Herdr launcher intent has expired")
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
	if strings.TrimSpace(string(out)) != "" {
		return fmt.Errorf("herdr pane run returned unexpected output")
	}
	return nil
}

func (s *OwnedSession) ProcessInfo(ctx context.Context, paneID string) (PaneProcessInfo, error) {
	out, err := s.runOwnedLaunchCommand(ctx, commandTimeout, "pane", "process-info", "--pane", paneID)
	if err != nil {
		return PaneProcessInfo{}, err
	}
	var envelope paneProcessInfoEnvelope
	if err := decodeOne(out, &envelope); err != nil || envelope.ID != "cli:pane:process_info" ||
		envelope.Result == nil || envelope.Result.Type != "pane_process_info" ||
		envelope.Result.ProcessInfo.PaneID != paneID {
		return PaneProcessInfo{}, fmt.Errorf("herdr pane process-info returned an unexpected response")
	}
	return envelope.Result.ProcessInfo, nil
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

func (s *OwnedSession) LivePanes(ctx context.Context) ([]corebackend.LivePane, error) {
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
	return s.backend.snapshot(ctx, commandTimeout, probed)
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
