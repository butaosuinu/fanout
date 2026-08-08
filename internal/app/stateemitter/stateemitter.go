// Package stateemitter records launch-bound cooperative agent telemetry.
package stateemitter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/herdrprocess"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/errs"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

const emitterTimeout = 15 * time.Second

// RuntimeTarget is the persisted launch binding used for one current-runtime
// observation. AcceptUnboundSession is true only before final-row persistence.
type RuntimeTarget struct {
	Backend              backend.Name
	Session              string
	SocketPath           string
	RepoKey              string
	WorkspaceID          string
	PaneID               string
	TerminalID           string
	Agent                string
	AgentID              string
	AgentSession         *backend.AgentSessionRef
	AcceptUnboundSession bool
	WorktreePath         string
	Executable           string
	Args                 []string
}

// Observation is one runtime snapshot plus the target pane's current process
// information, collected while the owning state lock is held.
type Observation struct {
	Panes       []backend.LivePane
	ProcessInfo herdrrun.PaneProcessInfo
}

// Observer reads current Herdr state for a persisted launch target.
type Observer interface {
	Observe(context.Context, RuntimeTarget) (Observation, error)
}

// Run handles the hidden CLI verb. Hook commands deliberately ignore a
// non-zero result so telemetry failure cannot affect the agent lifecycle.
func Run(args []string, getenv func(string) string, observer Observer, errw io.Writer) int {
	signal, err := telemetry.ParseSignal(args, getenv)
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), emitterTimeout)
		defer cancel()
		err = Emit(ctx, signal, observer)
	}
	if err == nil {
		return 0
	}
	fmt.Fprintf(errw, "fanout telemetry emitter: %v\n", err)
	return 1
}

// Emit updates exactly one final row or one matching provisional launch. It
// never changes authoritative lifecycle fields.
func Emit(ctx context.Context, signal telemetry.Signal, observer Observer) (err error) {
	defer errs.Wrap(&err, "emit %s telemetry for %s", signal.Backend, signal.RowKey)
	projectRoot, err := projectRootForStatePath(signal.StatePath)
	if err != nil {
		return err
	}
	locked, err := state.LockProjectForLaunch(projectRoot)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, locked.Unlock()) }()

	row, err := uniqueFinalRow(locked.Panes, signal.RowKey)
	if err != nil {
		return err
	}
	if row >= 0 {
		return updateFinalRow(ctx, locked, row, signal, observer)
	}
	return updatePendingIntent(ctx, locked, projectRoot, signal, observer)
}

func projectRootForStatePath(path string) (string, error) {
	root := filepath.Dir(filepath.Dir(path))
	if filepath.Base(filepath.Dir(path)) != ".fanout" || state.Path(root) != path {
		return "", fmt.Errorf("state path must name an owning .fanout/state.json")
	}
	return root, nil
}

func uniqueFinalRow(panes []state.Pane, rowKey string) (int, error) {
	index := -1
	for i := range panes {
		if panes[i].EmitterRowKey != rowKey {
			continue
		}
		if index >= 0 {
			return -1, fmt.Errorf("multiple final rows match emitter row key")
		}
		index = i
	}
	return index, nil
}

func updateFinalRow(
	ctx context.Context,
	locked *state.LockedStore,
	index int,
	signal telemetry.Signal,
	observer Observer,
) error {
	target, err := finalRuntimeTarget(locked.Panes[index], signal)
	if err != nil {
		return err
	}
	if err := verifyCurrentRuntime(ctx, target, observer); err != nil {
		return err
	}
	locked.Panes[index].ReportedState = string(signal.State)
	locked.Panes[index].StateRefinement = true
	return locked.Save()
}

func updatePendingIntent(
	ctx context.Context,
	locked *state.LockedStore,
	projectRoot string,
	signal telemetry.Signal,
	observer Observer,
) error {
	journal, err := locked.HerdrIntents(projectRoot)
	if err != nil {
		return err
	}
	intent, found := journal.FindIntent(signal.RowKey)
	if !found {
		return fmt.Errorf("no final row or provisional intent matches emitter row key")
	}
	target, err := pendingRuntimeTarget(intent, signal)
	if err != nil {
		return err
	}
	if err := verifyCurrentRuntime(ctx, target, observer); err != nil {
		return err
	}
	intent.Launch.PendingReportedState = pendingState(
		intent.Launch.PendingReportedState,
		string(signal.State),
	)
	journal.UpsertIntent(intent)
	return journal.Save()
}

func pendingState(current, next string) string {
	if current == string(backend.AgentDone) {
		return current
	}
	return next
}

func finalRuntimeTarget(pane state.Pane, signal telemetry.Signal) (RuntimeTarget, error) {
	identity := []bool{
		signal.Backend == backend.Herdr,
		pane.Backend == backend.Herdr,
		pane.EmitterRowKey == signal.RowKey,
		pane.LaunchNonce == signal.LaunchNonce,
		pane.EmitterNonce == signal.EmitterNonce,
		pane.HerdrSession == signal.Session,
		pane.HerdrSocketPath == signal.SocketPath,
		pane.HerdrWorkspaceID == signal.WorkspaceID,
		pane.PaneID == signal.PaneID,
		pane.HerdrTerminalID == signal.TerminalID,
		pane.Agent == signal.Agent,
		pane.HerdrAgentID == signal.AgentID,
	}
	if slices.Contains(identity, false) {
		return RuntimeTarget{}, fmt.Errorf("final row does not match emitter launch identity")
	}
	return RuntimeTarget{
		Backend: backend.Herdr, Session: pane.HerdrSession,
		SocketPath: pane.HerdrSocketPath, RepoKey: pane.HerdrRepoKey,
		WorkspaceID: pane.HerdrWorkspaceID, PaneID: pane.PaneID,
		TerminalID: pane.HerdrTerminalID, Agent: pane.Agent,
		AgentID: pane.HerdrAgentID, AgentSession: pane.HerdrAgentSession,
		WorktreePath: pane.WorktreePath, Executable: pane.HerdrLaunchExecutable,
		Args: slices.Clone(pane.HerdrLaunchArgs),
	}, nil
}

func pendingRuntimeTarget(intent state.HerdrIntent, signal telemetry.Signal) (RuntimeTarget, error) {
	launch := intent.Launch
	if intent.Kind != state.HerdrIntentWorktree || intent.Status != state.HerdrIntentRealized || launch == nil {
		return RuntimeTarget{}, fmt.Errorf("matching provisional intent is not an active agent launch")
	}
	if !time.Now().Before(time.UnixMilli(intent.ExpiresUnixMS)) {
		return RuntimeTarget{}, fmt.Errorf("matching provisional intent has expired")
	}
	identity := []bool{
		signal.Backend == backend.Herdr,
		intent.ID == signal.RowKey,
		launch.Nonce == signal.LaunchNonce,
		launch.EmitterNonce == signal.EmitterNonce,
		intent.Session == signal.Session,
		intent.SocketPath == signal.SocketPath,
		intent.Resource.WorkspaceID == signal.WorkspaceID,
		intent.Resource.PaneID == signal.PaneID,
		intent.Resource.TerminalID == signal.TerminalID,
		launch.Agent == signal.Agent,
		launch.AgentName == signal.AgentID,
	}
	if slices.Contains(identity, false) {
		return RuntimeTarget{}, fmt.Errorf("provisional intent does not match emitter launch identity")
	}
	return RuntimeTarget{
		Backend: backend.Herdr, Session: intent.Session,
		SocketPath: intent.SocketPath, RepoKey: intent.Resource.RepoKey,
		WorkspaceID: intent.Resource.WorkspaceID, PaneID: intent.Resource.PaneID,
		TerminalID: intent.Resource.TerminalID, Agent: launch.Agent,
		AgentID: launch.AgentName, AcceptUnboundSession: true,
		WorktreePath: intent.WorktreePath, Executable: launch.Executable,
		Args: slices.Clone(launch.Args),
	}, nil
}

func verifyCurrentRuntime(ctx context.Context, target RuntimeTarget, observer Observer) error {
	if observer == nil {
		return fmt.Errorf("runtime observer is unavailable")
	}
	if err := validateRuntimeTarget(target); err != nil {
		return err
	}
	observation, err := observer.Observe(ctx, target)
	if err != nil {
		return err
	}
	if !hasOneMatchingPane(target, observation.Panes) {
		return fmt.Errorf("saved PaneRef does not match exactly one current runtime pane")
	}
	if observation.ProcessInfo.PaneID != target.PaneID {
		return fmt.Errorf("process observation does not match saved PaneRef")
	}
	return herdrprocess.VerifyAgent(observation.ProcessInfo, herdrprocess.Identity{
		WorktreePath: target.WorktreePath,
		Executable:   target.Executable, Args: target.Args,
	})
}

func validateRuntimeTarget(target RuntimeTarget) error {
	identity := []string{
		target.Session, target.SocketPath, target.RepoKey, target.WorkspaceID,
		target.PaneID, target.TerminalID, target.Agent, target.AgentID,
	}
	if slices.ContainsFunc(identity, invalidIdentityValue) {
		return fmt.Errorf("persisted telemetry runtime identity is incomplete")
	}
	paths := []string{target.SocketPath, target.RepoKey, target.WorktreePath, target.Executable}
	if slices.ContainsFunc(paths, invalidCanonicalPath) {
		return fmt.Errorf("persisted telemetry path identity is invalid")
	}
	if target.Backend != backend.Herdr || target.Agent != "claude" || invalidAgentSession(target) {
		return fmt.Errorf("persisted telemetry backend identity is invalid")
	}
	return nil
}

func invalidAgentSession(target RuntimeTarget) bool {
	ref := target.AgentSession
	return ref != nil && (!ref.Valid() || ref.Agent != target.Agent || ref.Source != "herdr:"+target.Agent)
}

func invalidIdentityValue(value string) bool {
	return value == "" || strings.ContainsRune(value, '\x00')
}

func invalidCanonicalPath(path string) bool {
	return !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, '\x00')
}

func hasOneMatchingPane(target RuntimeTarget, panes []backend.LivePane) bool {
	matches := 0
	for _, pane := range panes {
		if livePaneMatches(target, pane) {
			matches++
		}
	}
	return matches == 1
}

func livePaneMatches(target RuntimeTarget, pane backend.LivePane) bool {
	identity := []bool{
		pane.Ref.Backend == target.Backend,
		pane.Ref.Workspace == target.WorkspaceID,
		pane.Ref.Pane == target.PaneID,
		pane.TerminalID == target.TerminalID,
		pane.SessionID == target.Session,
		pane.SocketPath == target.SocketPath,
		pane.RepoKey == target.RepoKey,
		filepath.Clean(pane.WorktreePath) == filepath.Clean(target.WorktreePath),
		pane.AgentPresent,
		pane.AgentProvider == target.Agent,
		pane.AgentID == target.AgentID,
		agentSessionMatches(target, pane.AgentSession),
	}
	return !slices.Contains(identity, false)
}

func agentSessionMatches(target RuntimeTarget, current *backend.AgentSessionRef) bool {
	if target.AgentSession != nil {
		return current != nil && *current == *target.AgentSession
	}
	if !target.AcceptUnboundSession {
		return current == nil
	}
	return current == nil || current.Valid() &&
		current.Agent == target.Agent && current.Source == "herdr:"+target.Agent
}

// ReportedState returns a sanitized persisted telemetry value for read-only
// status surfaces.
func ReportedState(raw string) string {
	value, ok := backend.ParseAgentState(strings.TrimSpace(raw))
	if !ok {
		return ""
	}
	return string(value)
}
