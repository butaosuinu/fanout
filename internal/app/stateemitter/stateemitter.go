// Package stateemitter records launch-bound cooperative agent telemetry.
package stateemitter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/app/agentprocess"
	"github.com/butaosuinu/fanout/internal/app/sessionbinding"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/errs"
	"github.com/butaosuinu/fanout/internal/core/telemetry"
	"github.com/butaosuinu/fanout/internal/infra/codexapp"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const emitterTimeout = telemetry.EmitterTimeoutSeconds * time.Second

var errRuntimeIdentityChanged = errors.New("current Herdr runtime identity changed")

// RuntimeTarget is the persisted launch binding used for one current-runtime
// observation. AcceptUnboundSession is true only before final-row persistence.
type RuntimeTarget struct {
	Backend              backend.Name
	Session              string
	SocketPath           string
	RepoKey              string
	GitCommonDir         string
	WorkspaceID          string
	WorkspaceLabel       string
	PaneID               string
	TerminalID           string
	Agent                string
	AgentID              string
	PlanMode             bool
	AgentSession         *backend.AgentSessionRef
	AcceptUnboundSession bool
	WorktreePath         string
	Executable           string
	Args                 []string
	GenericWorkspace     bool
}

// Observation is one runtime snapshot plus the target pane's current process
// information. ProcessError is kept separate so terminal replacement can
// invalidate telemetry from the pane snapshot even while the process tree is
// changing.
type Observation struct {
	Panes        []backend.LivePane
	ProcessInfo  backend.PaneProcessInfo
	ProcessError error
}

// Observer reads the current runtime state for a persisted launch target.
type Observer interface {
	Observe(context.Context, RuntimeTarget) (Observation, error)
}

// Run handles the hidden CLI verb. Hook commands deliberately ignore a
// non-zero result so telemetry failure cannot affect the agent lifecycle.
func Run(args []string, getenv func(string) string, observer Observer, errw io.Writer) int {
	signal, err := telemetry.ParseSignal(args, getenv)
	if err == nil {
		err = runSignal(signal, observer)
	}
	if err == nil {
		return 0
	}
	fmt.Fprintf(errw, "fanout telemetry emitter: %v\n", err)
	return 1
}

func runSignal(signal telemetry.Signal, observer Observer) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), emitterTimeout)
	defer cancel()
	return Emit(ctx, signal, observer)
}

// Emit updates exactly one final row or one matching provisional launch. It
// never changes authoritative lifecycle fields.
func Emit(ctx context.Context, signal telemetry.Signal, observer Observer) (err error) {
	defer errs.Wrap(&err, "emit %s telemetry for %s", signal.Backend, signal.RowKey)
	if signal.Agent == "claude" && signal.Sequence == 0 || signal.Agent != "claude" && signal.Sequence != 0 {
		return fmt.Errorf("telemetry sequence does not match provider")
	}
	projectRoot, err := projectRootForStatePath(signal.StatePath)
	if err != nil {
		return err
	}
	target, err := loadRuntimeTarget(ctx, projectRoot, signal)
	if err != nil {
		return err
	}
	observation, err := observeRuntime(ctx, target, observer)
	if err != nil {
		return err
	}
	locked, err := state.LockProjectForLaunchContext(ctx, projectRoot)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, locked.Unlock()) }()
	return applyObservedSignal(locked, projectRoot, target.GitCommonDir, signal, observation)
}

func loadRuntimeTarget(
	ctx context.Context,
	projectRoot string,
	signal telemetry.Signal,
) (target RuntimeTarget, err error) {
	owner, err := worktree.ResolveRepoIdentity(ctx, projectRoot)
	if err != nil {
		return RuntimeTarget{}, err
	}
	locked, err := state.LockProjectForLaunchContext(ctx, projectRoot)
	if err != nil {
		return RuntimeTarget{}, err
	}
	defer func() { err = errors.Join(err, locked.Unlock()) }()
	return runtimeTargetForSignal(locked, projectRoot, owner.RepoKey, signal)
}

func runtimeTargetForSignal(
	locked *state.LockedStore,
	projectRoot string,
	gitCommonDir string,
	signal telemetry.Signal,
) (RuntimeTarget, error) {
	row, err := uniqueFinalRow(locked.Panes, signal.RowKey)
	if err != nil {
		return RuntimeTarget{}, err
	}
	if row >= 0 {
		return finalRuntimeTarget(locked.Panes[row], gitCommonDir, signal)
	}
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return RuntimeTarget{}, err
	}
	intent, found := journal.FindIntent(signal.RowKey)
	if !found {
		return RuntimeTarget{}, fmt.Errorf("no final row or provisional intent matches emitter row key")
	}
	return pendingRuntimeTarget(intent, gitCommonDir, signal)
}

func applyObservedSignal(
	locked *state.LockedStore,
	projectRoot string,
	gitCommonDir string,
	signal telemetry.Signal,
	observation Observation,
) error {
	row, err := uniqueFinalRow(locked.Panes, signal.RowKey)
	if err != nil {
		return err
	}
	if row >= 0 {
		return updateFinalRow(locked, row, gitCommonDir, signal, observation)
	}
	return updatePendingIntent(locked, projectRoot, gitCommonDir, signal, observation)
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
	locked *state.LockedStore,
	index int,
	gitCommonDir string,
	signal telemetry.Signal,
	observation Observation,
) error {
	target, err := finalRuntimeTarget(locked.Panes[index], gitCommonDir, signal)
	if err != nil {
		return err
	}
	if staleSignal(signal, locked.Panes[index].ReportedStateSeq) {
		return nil
	}
	current, err := verifyRuntimeObservation(target, observation)
	if err != nil {
		if errors.Is(err, errRuntimeIdentityChanged) {
			return invalidateFinalRowTelemetry(locked, index)
		}
		return err
	}
	if err := bindAgentSession(locked.Panes, index, current); err != nil {
		return err
	}
	locked.Panes[index].ReportedState = nextReportedState(
		locked.Panes[index].ReportedState,
		string(signal.State),
	)
	locked.Panes[index].ReportedStateSeq = signal.Sequence
	locked.Panes[index].StateRefinement = true
	return locked.Save()
}

// bindAgentSession records the conversation the pane currently reports. It
// covers both the first bind, where the row carries none yet, and the rebind
// after the provider replaced its conversation in a pane this launch still
// owns. Recording the replacement is what keeps the row truthful for the one
// consumer that reads the value rather than comparing it — resume, which
// restores the conversation the pane is actually on.
func bindAgentSession(panes []state.Pane, index int, current backend.LivePane) error {
	recorded := panes[index].AgentSession
	if current.AgentSession == nil || backend.SameAgentSession(recorded, current.AgentSession) {
		return nil
	}
	if recorded == nil {
		return bindFirstAgentSession(panes, index, current)
	}
	// Reaching here means agentSessionMatches already admitted the observation
	// against the recorded provider, so the replacement is proven, not assumed.
	ref := *current.AgentSession
	panes[index].AgentSession = &ref
	return nil
}

func bindFirstAgentSession(panes []state.Pane, index int, current backend.LivePane) error {
	ref, ok := sessionbinding.UniqueSessionBinding(panes, index, []backend.LivePane{current})
	if !ok {
		return fmt.Errorf("late Herdr agent session does not match exactly one state row")
	}
	panes[index].AgentSession = ref
	return nil
}

func invalidateFinalRowTelemetry(locked *state.LockedStore, index int) error {
	nonce, err := newEmitterNonce()
	if err != nil {
		return err
	}
	locked.Panes[index].ReportedState = ""
	locked.Panes[index].ReportedStateSeq = 0
	locked.Panes[index].StateRefinement = false
	locked.Panes[index].EmitterNonce = nonce
	return locked.Save()
}

func newEmitterNonce() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("rotate telemetry emitter nonce: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func updatePendingIntent(
	locked *state.LockedStore,
	projectRoot string,
	gitCommonDir string,
	signal telemetry.Signal,
	observation Observation,
) error {
	journal, err := locked.LaunchJournal(projectRoot)
	if err != nil {
		return err
	}
	intent, found := journal.FindIntent(signal.RowKey)
	if !found {
		return fmt.Errorf("no final row or provisional intent matches emitter row key")
	}
	target, err := pendingRuntimeTarget(intent, gitCommonDir, signal)
	if err != nil {
		return err
	}
	if staleSignal(signal, intent.Launch.PendingReportedSeq) {
		return nil
	}
	current, err := verifyRuntimeObservation(target, observation)
	if err != nil {
		return err
	}
	if err := recordPendingSignal(&intent, signal, current); err != nil {
		return err
	}
	journal.UpsertIntent(intent)
	return journal.Save()
}

func recordPendingSignal(intent *state.LaunchIntent, signal telemetry.Signal, current backend.LivePane) error {
	if current.AgentSession == nil {
		return fmt.Errorf("pending telemetry requires a current agent session")
	}
	if intent.Launch.PendingReportedState != string(backend.AgentDone) {
		session := *current.AgentSession
		intent.Launch.PendingAgentSession = &session
	}
	intent.Launch.PendingReportedState = nextReportedState(intent.Launch.PendingReportedState, string(signal.State))
	intent.Launch.PendingReportedSeq = signal.Sequence
	return nil
}

func staleSignal(signal telemetry.Signal, applied uint64) bool {
	return signal.Agent == "claude" && signal.Sequence <= applied
}

func nextReportedState(current, next string) string {
	if current == string(backend.AgentDone) {
		return current
	}
	return next
}

// finalRuntimeTarget builds the observation target out of the row's own durable
// binding projection, so the identity the emitter compares and the identity it
// observes against come from one place. PlanMode and the generic-workspace flag
// stay read from the row: they describe the launch mode and the row kind, not
// the pane identity the projection carries.
func finalRuntimeTarget(pane state.Pane, gitCommonDir string, signal telemetry.Signal) (RuntimeTarget, error) {
	binding := pane.RuntimeBinding()
	identity := []bool{
		signal.Backend == backend.Herdr,
		binding.Ref.Backend == backend.Herdr,
		binding.Launch.RowKey == signal.RowKey,
		binding.Launch.Nonce == signal.LaunchNonce,
		binding.Launch.EmitterNonce == signal.EmitterNonce,
		binding.SessionID == signal.Session,
		binding.SocketPath == signal.SocketPath,
		binding.Ref.Workspace == signal.WorkspaceID,
		binding.Ref.Pane == signal.PaneID,
		binding.TerminalID == signal.TerminalID,
		binding.Agent == signal.Agent,
		binding.AgentID == signal.AgentID,
	}
	if slices.Contains(identity, false) {
		return RuntimeTarget{}, fmt.Errorf("final row does not match emitter launch identity")
	}
	return RuntimeTarget{
		Backend: binding.Ref.Backend, Session: binding.SessionID,
		SocketPath: binding.SocketPath, RepoKey: binding.RepoKey, GitCommonDir: gitCommonDir,
		WorkspaceID: binding.Ref.Workspace, WorkspaceLabel: binding.WorkspaceLabel,
		PaneID:     binding.Ref.Pane,
		TerminalID: binding.TerminalID, Agent: binding.Agent,
		AgentID: binding.AgentID, PlanMode: pane.PlanMode, AgentSession: binding.AgentSession,
		AcceptUnboundSession: binding.AgentSession == nil,
		WorktreePath:         binding.WorktreePath, Executable: binding.Launch.Executable,
		Args:             slices.Clone(binding.Launch.Args),
		GenericWorkspace: pane.Kind == state.PaneKindAttachedAgent && binding.RepoKey == "",
	}, nil
}

func pendingRuntimeTarget(
	intent state.LaunchIntent,
	gitCommonDir string,
	signal telemetry.Signal,
) (RuntimeTarget, error) {
	launch := intent.Launch
	generic := intent.Kind == state.IntentCoordinator && intent.Resource.RepoKey == ""
	if intent.Status != state.IntentRealized || launch == nil ||
		(intent.Kind != state.IntentWorktree && !generic) {
		return RuntimeTarget{}, fmt.Errorf("matching provisional intent is not an active agent launch")
	}
	if !time.Now().Before(time.UnixMilli(intent.ExpiresUnixMS)) {
		return RuntimeTarget{}, fmt.Errorf("matching provisional intent has expired")
	}
	if !pendingSignalMatches(intent, signal) {
		return RuntimeTarget{}, fmt.Errorf("provisional intent does not match emitter launch identity")
	}
	return RuntimeTarget{
		Backend: backend.Herdr, Session: intent.Session,
		SocketPath: intent.SocketPath, RepoKey: intent.Resource.RepoKey, GitCommonDir: gitCommonDir,
		WorkspaceID: intent.Resource.WorkspaceID, WorkspaceLabel: intent.Resource.Label,
		PaneID:     intent.Resource.PaneID,
		TerminalID: intent.Resource.TerminalID, Agent: launch.Agent,
		AgentID: launch.AgentName, PlanMode: launch.CodexPlanStatusPath != "", AcceptUnboundSession: true,
		WorktreePath: intent.WorktreePath, Executable: launch.Executable,
		Args:             slices.Clone(launch.Args),
		GenericWorkspace: generic,
	}, nil
}

func pendingSignalMatches(intent state.LaunchIntent, signal telemetry.Signal) bool {
	launch := intent.Launch
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
	return !slices.Contains(identity, false)
}

func observeRuntime(
	ctx context.Context,
	target RuntimeTarget,
	observer Observer,
) (Observation, error) {
	if observer == nil {
		return Observation{}, fmt.Errorf("runtime observer is unavailable")
	}
	if err := validateRuntimeTarget(target); err != nil {
		return Observation{}, err
	}
	observation, err := observer.Observe(ctx, target)
	if err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func verifyRuntimeObservation(target RuntimeTarget, observation Observation) (backend.LivePane, error) {
	if err := validateRuntimeTarget(target); err != nil {
		return backend.LivePane{}, err
	}
	if currentTerminalChanged(target, observation.Panes) {
		return backend.LivePane{}, errRuntimeIdentityChanged
	}
	current, ok := uniqueMatchingPane(target, observation.Panes)
	if !ok {
		return backend.LivePane{}, fmt.Errorf(
			"%w: saved PaneRef does not match exactly one current runtime pane",
			errRuntimeIdentityChanged,
		)
	}
	if observation.ProcessError != nil {
		return backend.LivePane{}, observation.ProcessError
	}
	if observation.ProcessInfo.PaneID != target.PaneID {
		return backend.LivePane{}, fmt.Errorf(
			"%w: process observation does not match saved PaneRef",
			errRuntimeIdentityChanged,
		)
	}
	err := agentprocess.VerifyAgent(observation.ProcessInfo, agentprocess.Identity{
		WorktreePath: target.WorktreePath,
		Executable:   target.Executable, Args: target.Args, Agent: target.Agent,
	})
	if err != nil {
		return backend.LivePane{}, fmt.Errorf("%w: %w", errRuntimeIdentityChanged, err)
	}
	return current, err
}

func currentTerminalChanged(target RuntimeTarget, panes []backend.LivePane) bool {
	matches := 0
	changed := false
	for _, pane := range panes {
		if !sameLivePaneWithoutTerminal(target, pane) {
			continue
		}
		matches++
		changed = pane.TerminalID != "" && pane.TerminalID != target.TerminalID
	}
	return matches == 1 && changed
}

func sameLivePaneWithoutTerminal(target RuntimeTarget, pane backend.LivePane) bool {
	identity := []bool{
		pane.Ref.Backend == target.Backend,
		pane.Ref.Workspace == target.WorkspaceID,
		pane.WorkspaceLabel == target.WorkspaceLabel,
		pane.Ref.Pane == target.PaneID,
		pane.SessionID == target.Session,
		pane.SocketPath == target.SocketPath,
		pane.RepoKey == target.RepoKey,
		runtimePathMatches(target, pane),
	}
	return !slices.Contains(identity, false)
}

func runtimePathMatches(target RuntimeTarget, pane backend.LivePane) bool {
	if target.GenericWorkspace {
		return pane.CurrentPath == target.WorktreePath
	}
	return filepath.Clean(pane.WorktreePath) == filepath.Clean(target.WorktreePath)
}

func validateRuntimeTarget(target RuntimeTarget) error {
	identity := []string{
		target.Session, target.SocketPath, target.WorkspaceID,
		target.WorkspaceLabel, target.PaneID, target.TerminalID, target.Agent, target.AgentID,
	}
	if slices.ContainsFunc(identity, invalidIdentityValue) {
		return fmt.Errorf("persisted telemetry runtime identity is incomplete")
	}
	paths := []string{target.SocketPath, target.GitCommonDir, target.WorktreePath, target.Executable}
	if slices.ContainsFunc(paths, invalidCanonicalPath) {
		return fmt.Errorf("persisted telemetry path identity is invalid")
	}
	if target.GenericWorkspace != (target.RepoKey == "") ||
		target.RepoKey != "" && (invalidCanonicalPath(target.RepoKey) || target.RepoKey != target.GitCommonDir) {
		return fmt.Errorf("persisted telemetry repository identity is invalid")
	}
	if target.Backend != backend.Herdr || !validTelemetryAgent(target) || invalidAgentSession(target) {
		return fmt.Errorf("persisted telemetry backend identity is invalid")
	}
	return nil
}

func validTelemetryAgent(target RuntimeTarget) bool {
	if target.Agent == "claude" {
		return true
	}
	return target.Agent == "codex" && target.PlanMode &&
		len(target.Args) > 0 && target.Args[0] == codexapp.PlanTUICommand
}

func invalidAgentSession(target RuntimeTarget) bool {
	ref := target.AgentSession
	return ref != nil && !backend.ExpectedAgentSession(ref, target.Agent)
}

func invalidIdentityValue(value string) bool {
	return value == "" || strings.ContainsRune(value, '\x00')
}

func invalidCanonicalPath(path string) bool {
	return !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, '\x00')
}

func uniqueMatchingPane(target RuntimeTarget, panes []backend.LivePane) (backend.LivePane, bool) {
	var matched backend.LivePane
	count := 0
	for _, pane := range panes {
		if livePaneMatches(target, pane) {
			matched = pane
			count++
		}
	}
	return matched, count == 1
}

func livePaneMatches(target RuntimeTarget, pane backend.LivePane) bool {
	identity := []bool{
		pane.Ref.Backend == target.Backend,
		pane.Ref.Workspace == target.WorkspaceID,
		pane.WorkspaceLabel == target.WorkspaceLabel,
		pane.Ref.Pane == target.PaneID,
		pane.TerminalID == target.TerminalID,
		pane.SessionID == target.Session,
		pane.SocketPath == target.SocketPath,
		pane.RepoKey == target.RepoKey,
		runtimePathMatches(target, pane),
		pane.AgentPresent,
		pane.AgentProvider == target.Agent,
		backend.AgentRecordMatches(pane.AgentID, pane.AgentNamed, target.AgentID),
		agentSessionMatches(target, pane.AgentSession),
	}
	return !slices.Contains(identity, false)
}

func agentSessionMatches(target RuntimeTarget, current *backend.AgentSessionRef) bool {
	if target.AgentSession != nil {
		// A recorded conversation admits the provider's current one, so a row
		// whose agent started a new conversation keeps reporting state instead
		// of being invalidated into a rotated emitter nonce it can never match.
		return backend.SameAgentSession(current, target.AgentSession) ||
			backend.ExpectedAgentSession(current, target.Agent)
	}
	if !target.AcceptUnboundSession {
		return current == nil
	}
	return current == nil || backend.ExpectedAgentSession(current, target.Agent)
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
