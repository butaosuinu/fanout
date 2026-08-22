package panelaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type restartRuntimeFake struct {
	t               *testing.T
	route           backend.OwnedLaunchRoute
	waitPanes       []backend.LivePane
	issuePanes      []backend.LivePane
	resumedPanes    []backend.LivePane
	finalPanes      []backend.LivePane
	launcherInfo    backend.PaneProcessInfo
	resumedInfo     backend.PaneProcessInfo
	waitTimeout     time.Duration
	waitDelay       time.Duration
	waitCalls       int
	observeCalls    int
	issueCalls      int
	issueTimeout    time.Duration
	issueErr        error
	onIssue         func()
	preparedEnvPath string
}

func (f *restartRuntimeFake) LaunchRoute() (backend.OwnedLaunchRoute, error) {
	return f.route, nil
}

func (f *restartRuntimeFake) PrepareWorkloadEnvironment(
	nonce string,
	_ []string,
) (string, int, error) {
	f.t.Helper()
	dir := filepath.Join(f.route.RuntimeDir, "workload-env")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	f.preparedEnvPath = filepath.Join(dir, "env-"+nonce+".json")
	if err := os.WriteFile(f.preparedEnvPath, []byte("{}\n"), 0o600); err != nil {
		return "", 0, err
	}
	return f.preparedEnvPath, 1, nil
}

func (f *restartRuntimeFake) WaitRestoredPanes(
	_ context.Context,
	timeout time.Duration,
	match func([]backend.LivePane) bool,
) backend.WaitResult {
	f.waitCalls++
	f.waitTimeout = timeout
	time.Sleep(f.waitDelay)
	status := backend.WaitTimedOut
	if match(f.waitPanes) {
		status = backend.WaitMatched
	}
	return backend.WaitResult{Status: status, Panes: slices.Clone(f.waitPanes)}
}

func (f *restartRuntimeFake) IssueRestartResume(
	_ context.Context,
	_, _ string,
	deadline time.Time,
	preflight func(backend.PaneProcessInfo, []backend.LivePane) error,
	markIssued func() error,
) error {
	f.issueTimeout = time.Until(deadline)
	panes := f.waitPanes
	if f.issuePanes != nil {
		panes = f.issuePanes
	}
	if err := preflight(f.launcherInfo, slices.Clone(panes)); err != nil {
		return err
	}
	if err := markIssued(); err != nil {
		return err
	}
	f.issueCalls++
	if f.onIssue != nil {
		f.onIssue()
	}
	return f.issueErr
}

func (f *restartRuntimeFake) ObserveRestartResume(
	_ context.Context,
	_ string,
) (backend.PaneProcessInfo, []backend.LivePane, error) {
	f.observeCalls++
	panes := f.resumedPanes
	if f.observeCalls > 1 && f.finalPanes != nil {
		panes = f.finalPanes
	}
	return f.resumedInfo, slices.Clone(panes), nil
}

func TestResumeRestartedManagedRowsRebindsExactCodexProcess(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	placeholder.AgentState = backend.AgentIdle
	resumed := resumedCodexPane(placeholder)
	recordRestartStatePane(t, repo, saved)
	locked, journal := lockManagedRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumed)
	runtime.waitDelay = 500 * time.Millisecond

	if err := resumeRestartedManagedRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, found := locked.Find(saved.Parent, saved.IssueNum)
	if !found || got.TerminalID != resumed.TerminalID || got.AgentID != resumed.AgentID ||
		got.ProcessIdentity == nil || got.ProcessIdentity.AgentPID != 10 {
		t.Fatalf("rebound row = (%+v, %t)", got, found)
	}
	if got.ReportedState != "" || got.StateRefinement || got.EmitterNonce == saved.EmitterNonce {
		t.Fatalf("rebound telemetry = (%q, %t, %q)", got.ReportedState, got.StateRefinement, got.EmitterNonce)
	}
	wantArgs := []string{"resume", saved.AgentSession.Value}
	if !slices.Equal(got.LaunchArgs, wantArgs) || runtime.issueCalls != 1 ||
		runtime.waitCalls != 1 || runtime.waitTimeout != 3*time.Second {
		t.Fatalf("resume result args=%q issues=%d wait=%d/%s", got.LaunchArgs, runtime.issueCalls, runtime.waitCalls, runtime.waitTimeout)
	}
	if runtime.issueTimeout <= 0 || runtime.issueTimeout >= 2750*time.Millisecond {
		t.Fatalf("resume issue timeout = %s, want wait time deducted from 3s budget", runtime.issueTimeout)
	}
	if containsManagedResumeIntent(journal.Intents) {
		t.Fatal("successful resume intent remains")
	}
}

func TestResumeRestartedManagedRowsRefreshesShellAndConsoleTerminalIDs(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	shell, shellLive := restartShellFixture(repo, "shell", -1, "")
	console, consoleLive := restartShellFixture(repo, "console", -2, ManagedConsoleRuntimeParent)
	console.RepoKey, console.RepoRoot = "", ""
	consoleLive.RepoKey, consoleLive.ProjectRoot, consoleLive.WorktreePath = "", "", ""
	coordinator, coordinatorLive := restartShellFixture(repo, "coordinator", -3, "plan:test")
	coordinator.Agent = ""
	coordinator.RepoKey, coordinator.RepoRoot = "", ""
	coordinatorLive.RepoKey, coordinatorLive.ProjectRoot, coordinatorLive.WorktreePath = "", "", ""
	for _, pane := range []state.Pane{shell, console, coordinator} {
		recordRestartStatePane(t, repo, pane)
	}
	locked, journal := lockManagedRestartTest(t, repo)
	runtime := &restartRuntimeFake{
		t: t,
		route: backend.OwnedLaunchRoute{
			Session: shell.SessionID, SocketPath: shell.SocketPath,
		},
		waitPanes: []backend.LivePane{shellLive, consoleLive, coordinatorLive},
	}

	if err := resumeRestartedManagedRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		pane       state.Pane
		terminalID string
	}{{shell, shellLive.TerminalID}, {console, consoleLive.TerminalID}, {coordinator, coordinator.TerminalID}} {
		got, found := locked.Find(want.pane.Parent, want.pane.IssueNum)
		if !found || got.TerminalID != want.terminalID {
			t.Fatalf("restart row = (%+v, %t), want terminal %q", got, found, want.terminalID)
		}
	}
}

func TestResumeRestartedManagedRowsKeepsShellTerminalIDOnIdentityMismatch(t *testing.T) {
	tests := map[string]func(*backend.LivePane){
		"workspace label":     func(live *backend.LivePane) { live.WorkspaceLabel = "foreign" },
		"checkout provenance": func(live *backend.LivePane) { live.RepoKey = "foreign" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			repo := newManagedRealizeRepo(t)
			saved, live := restartShellFixture(repo, "shell", -1, "")
			mutate(&live)
			recordRestartStatePane(t, repo, saved)
			locked, journal := lockManagedRestartTest(t, repo)
			runtime := &restartRuntimeFake{
				t: t,
				route: backend.OwnedLaunchRoute{
					Session: saved.SessionID, SocketPath: saved.SocketPath,
				},
				waitPanes: []backend.LivePane{live},
			}

			if err := resumeRestartedManagedRows(
				context.Background(), repo, locked, journal, runtime, 3*time.Second,
			); err != nil {
				t.Fatal(err)
			}
			got, found := locked.Find(saved.Parent, saved.IssueNum)
			if !found || got.TerminalID != saved.TerminalID {
				t.Fatalf("mismatched restart row = (%+v, %t), want terminal %q", got, found, saved.TerminalID)
			}
		})
	}
}

func TestResumeRestartedManagedRowsDoesNotWaitForUnsupportedMissingRoute(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	unsupported := saved
	unsupported.IssueNum, unsupported.PaneID = 533, "w1:p2"
	unsupported.Agent = "claude"
	recordRestartStatePane(t, repo, saved)
	recordRestartStatePane(t, repo, unsupported)
	locked, journal := lockManagedRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))

	if err := resumeRestartedManagedRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	if runtime.waitCalls != 1 || runtime.issueCalls != 1 {
		t.Fatalf("restart calls = (wait=%d, issue=%d), want (1, 1)", runtime.waitCalls, runtime.issueCalls)
	}
	got, found := locked.Find(unsupported.Parent, unsupported.IssueNum)
	if !found || got.DirectAgentLaunch || got.ReportedState != "" || got.StateRefinement {
		t.Fatalf("unsupported missing row = (%+v, %t), want stale", got, found)
	}
}

func TestResumeRestartedManagedRowsDoesNotLetMissingCodexBlockExactCandidate(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	missing := saved
	missing.IssueNum, missing.PaneID = 533, "w1:p2"
	missing.AgentSession = &backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "other-session",
	}
	recordRestartStatePane(t, repo, saved)
	recordRestartStatePane(t, repo, missing)
	locked, journal := lockManagedRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))

	if err := resumeRestartedManagedRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, found := locked.Find(saved.Parent, saved.IssueNum)
	if !found || got.TerminalID != placeholder.TerminalID || runtime.issueCalls != 1 {
		t.Fatalf("exact row/runtime = (%+v, %t, issues=%d)", got, found, runtime.issueCalls)
	}
	stale, found := locked.Find(missing.Parent, missing.IssueNum)
	if !found || stale.DirectAgentLaunch || runtime.waitCalls != 1 {
		t.Fatalf("missing row/runtime = (%+v, %t, waits=%d)", stale, found, runtime.waitCalls)
	}
}

func TestResumeRestartedManagedRowsRejectsDuplicateRefAddedBeforeToken(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	duplicate := placeholder
	duplicate.Ref.Pane = "w1:p2"
	recordRestartStatePane(t, repo, saved)
	locked, journal := lockManagedRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))
	runtime.issuePanes = []backend.LivePane{placeholder, duplicate}

	if err := resumeRestartedManagedRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, found := locked.Find(saved.Parent, saved.IssueNum)
	if !found || got.DirectAgentLaunch || runtime.issueCalls != 0 {
		t.Fatalf("duplicate preflight result = (%+v, %t, issues=%d)", got, found, runtime.issueCalls)
	}
}

func TestResumeRestartedManagedRowsRejectsDuplicateRefBeforeCommit(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	resumed := resumedCodexPane(placeholder)
	duplicate := resumed
	duplicate.Ref.Pane = "w1:p2"
	recordRestartStatePane(t, repo, saved)
	locked, journal := lockManagedRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumed)
	runtime.finalPanes = []backend.LivePane{resumed, duplicate}

	if err := resumeRestartedManagedRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, found := locked.Find(saved.Parent, saved.IssueNum)
	if !found || got.DirectAgentLaunch || got.TerminalID != saved.TerminalID ||
		runtime.issueCalls != 1 {
		t.Fatalf("duplicate final result = (%+v, %t, issues=%d)", got, found, runtime.issueCalls)
	}
}

func TestResumeRestartedManagedRowsLeavesUnsupportedRowsStale(t *testing.T) {
	tests := map[string]func(*state.Pane, *backend.LivePane, *[]backend.LivePane){
		"missing ref": func(saved *state.Pane, _ *backend.LivePane, _ *[]backend.LivePane) {
			saved.AgentSession = nil
		},
		"mismatched ref": func(_ *state.Pane, live *backend.LivePane, _ *[]backend.LivePane) {
			live.AgentSession = &backend.AgentSessionRef{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "other"}
		},
		"duplicate ref": func(_ *state.Pane, live *backend.LivePane, panes *[]backend.LivePane) {
			duplicate := *live
			duplicate.Ref.Pane = "w1:p2"
			*panes = append(*panes, duplicate)
		},
		"unverified provider": func(saved *state.Pane, _ *backend.LivePane, _ *[]backend.LivePane) {
			saved.Agent = "claude"
		},
		"attached row": func(saved *state.Pane, _ *backend.LivePane, _ *[]backend.LivePane) {
			saved.Kind = state.PaneKindAttachedAgent
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			repo := newManagedRealizeRepo(t)
			saved, placeholder := restartCodexFixture()
			runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))
			panes := []backend.LivePane{placeholder}
			mutate(&saved, &placeholder, &panes)
			panes[0] = placeholder
			recordRestartStatePane(t, repo, saved)
			locked, journal := lockManagedRestartTest(t, repo)
			runtime.waitPanes = panes

			if err := resumeRestartedManagedRows(
				context.Background(), repo, locked, journal, runtime, 3*time.Second,
			); err != nil {
				t.Fatal(err)
			}
			got, found := locked.Find(saved.Parent, saved.IssueNum)
			if !found || got.TerminalID != saved.TerminalID ||
				got.ReportedState != "" || got.StateRefinement || runtime.issueCalls != 0 {
				t.Fatalf("stale row/runtime = (%+v, %t, issues=%d)", got, found, runtime.issueCalls)
			}
		})
	}
}

func TestResumeRestartedManagedRowsRejectsUnsupportedUnchangedTerminal(t *testing.T) {
	for _, kind := range []string{"provider", "attached"} {
		t.Run(kind, func(t *testing.T) {
			repo := newManagedRealizeRepo(t)
			saved, placeholder := restartCodexFixture()
			if kind == "provider" {
				saved.Agent = "claude"
			} else {
				saved.Kind = state.PaneKindAttachedAgent
			}
			placeholder.TerminalID = saved.TerminalID
			recordRestartStatePane(t, repo, saved)
			locked, journal := lockManagedRestartTest(t, repo)
			runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))

			err := resumeRestartedManagedRows(
				context.Background(), repo, locked, journal, runtime, 3*time.Second,
			)
			if err == nil || !strings.Contains(err.Error(), "not stale after restart") {
				t.Fatalf("unsupported unchanged terminal error = %v", err)
			}
			if runtime.issueCalls != 0 {
				t.Fatalf("unsupported row resume calls = %d, want 0", runtime.issueCalls)
			}
		})
	}
}

func TestResumeRestartedManagedRowsDoesNotReplayInterruptedIntent(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	recordRestartStatePane(t, repo, saved)
	locked, journal := lockManagedRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))
	candidate := managedRestartCandidate{
		row: managedRestartRow{root: repo, current: true, saved: saved}, live: placeholder,
	}
	intent := newManagedResumeIntent(
		mustManagedResumeID(t, saved), strings.Repeat("a", 32),
		mustResumeEnvironment(t, runtime.route.RuntimeDir, strings.Repeat("a", 32)),
		1, candidate, time.Now().Add(time.Minute),
	)
	intent.Launch.LauncherReady, intent.Launch.TokenIssued = true, true
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}

	if err := resumeRestartedManagedRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, _ := locked.Find(saved.Parent, saved.IssueNum)
	if runtime.issueCalls != 0 || got.TerminalID != saved.TerminalID ||
		containsManagedResumeIntent(journal.Intents) {
		t.Fatalf("interrupted resume was replayed: row=%+v calls=%d", got, runtime.issueCalls)
	}
}

func TestResumeRestartedManagedRowsLeavesLostTokenResponseStale(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	recordRestartStatePane(t, repo, saved)
	locked, journal := lockManagedRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))
	runtime.issueErr = errors.New("pane run response was lost")

	if err := resumeRestartedManagedRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, _ := locked.Find(saved.Parent, saved.IssueNum)
	if runtime.issueCalls != 1 || got.TerminalID != saved.TerminalID ||
		got.DirectAgentLaunch || got.ReportedState != "" || got.StateRefinement ||
		containsManagedResumeIntent(journal.Intents) {
		t.Fatalf("lost token response result: row=%+v calls=%d", got, runtime.issueCalls)
	}
	if err := resumeRestartedManagedRows(context.Background(), repo, locked, journal, runtime, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if runtime.issueCalls != 1 {
		t.Fatalf("lost token response was retried: calls=%d", runtime.issueCalls)
	}
}

func TestResumeRestartedManagedRowsRejectsIdentityChangeBeforeCommit(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	recordRestartStatePane(t, repo, saved)
	locked, journal := lockManagedRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))
	runtime.onIssue = func() {
		pane, found := locked.Find(saved.Parent, saved.IssueNum)
		if !found {
			t.Fatal("saved row disappeared")
		}
		pane.TerminalID = "changed-concurrently"
		locked.Panes[0] = pane
	}

	err := resumeRestartedManagedRows(context.Background(), repo, locked, journal, runtime, 3*time.Second)
	if err == nil || !strings.Contains(err.Error(), "changed before finalization") {
		t.Fatalf("identity change error = %v", err)
	}
}

func TestResumeRestartedManagedRowsRejectsUnchangedTerminal(t *testing.T) {
	repo := newManagedRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	placeholder.TerminalID = saved.TerminalID
	recordRestartStatePane(t, repo, saved)
	locked, journal := lockManagedRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))

	err := resumeRestartedManagedRows(context.Background(), repo, locked, journal, runtime, 3*time.Second)
	if err == nil || !strings.Contains(err.Error(), "not stale after restart") {
		t.Fatalf("unchanged terminal error = %v", err)
	}
}

func newRestartRuntimeFake(
	t *testing.T,
	saved state.Pane,
	placeholder, resumed backend.LivePane,
) *restartRuntimeFake {
	t.Helper()
	runtimeDir := filepath.Join(t.TempDir(), "runtime")
	launcher := filepath.Join(runtimeDir, "launcher", "fanout")
	return &restartRuntimeFake{
		t: t,
		route: backend.OwnedLaunchRoute{
			RuntimeDir: runtimeDir, Session: saved.SessionID, SocketPath: saved.SocketPath,
			LauncherPath: launcher, ControlPath: filepath.Join(runtimeDir, "herdr-intents.json"),
		},
		waitPanes:    []backend.LivePane{placeholder},
		resumedPanes: []backend.LivePane{resumed},
		launcherInfo: restartProcessInfo(launcher, nil, saved.WorktreePath),
		resumedInfo: restartProcessInfo(
			saved.LaunchExecutable, []string{"resume", saved.AgentSession.Value}, saved.WorktreePath,
		),
	}
}

func lockManagedRestartTest(
	t *testing.T,
	repo string,
) (*state.LockedStore, *state.LockedLaunchJournal) {
	t.Helper()
	locked, err := state.LockProjectForLaunch(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	})
	journal, err := locked.LaunchJournal(repo)
	if err != nil {
		t.Fatal(err)
	}
	return locked, journal
}

func restartProcessInfo(executable string, args []string, cwd string) backend.PaneProcessInfo {
	return backend.PaneProcessInfo{
		PaneID: "w1:p1", ShellPID: 10, ForegroundProcessGroup: 10,
		ForegroundProcesses: []backend.PaneProcess{{
			PID: 10, ParentPID: 1, ProcessGroup: 10, Executable: executable,
			Argv0: executable, Argv: args, CWD: cwd,
		}},
	}
}

func restartCodexFixture() (state.Pane, backend.LivePane) {
	ref := &backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "019f-session",
	}
	saved := state.Pane{
		Parent: "524", RuntimeParent: "524", IssueNum: 532, Backend: backend.Herdr,
		PaneID: "w1:p1", WorkspaceID: "w1", WorkspaceLabel: "fanout-workspace-token",
		TerminalID: "term-old", RepoKey: "/repo/.git", RepoRoot: "/repo",
		AgentID: "fanout-codex", AgentSession: ref,
		ProcessIdentity: &backend.ProcessIdentity{ShellPID: 10, ForegroundProcessGroup: 10, AgentPID: 10},
		SessionID:       "fanout-owned", SocketPath: "/runtime/herdr.sock",
		LaunchExecutable: "/opt/codex", LaunchArgs: []string{"prompt"},
		DirectAgentLaunch: true, Agent: "codex", WorktreePath: "/repo/worktree",
		ReportedState: "working", StateRefinement: true, EmitterNonce: strings.Repeat("b", 32),
	}
	live := backend.LivePane{
		Ref:         backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
		CurrentPath: saved.WorktreePath, WorktreePath: saved.WorktreePath,
		WorkspaceLabel: saved.WorkspaceLabel, TerminalID: "term-new",
		AgentSession: ref, RepoKey: saved.RepoKey, ProjectRoot: saved.RepoRoot,
		SessionID: saved.SessionID, SocketPath: saved.SocketPath,
	}
	return saved, live
}

func restartShellFixture(root, name string, issue int, runtimeParent string) (state.Pane, backend.LivePane) {
	saved := state.Pane{
		Parent: ManualParentRef, RuntimeParent: runtimeParent, IssueNum: issue,
		Kind: state.PaneKindShell, Backend: backend.Herdr,
		PaneID: "w-" + name + ":p1", WorkspaceID: "w-" + name,
		WorkspaceLabel: "fanout-" + name + "-token", TerminalID: "term-" + name + "-old",
		RepoKey: root + "/.git", RepoRoot: root,
		SessionID: "fanout-owned", SocketPath: "/runtime/herdr.sock",
		Agent: state.PaneKindShell, WorktreePath: root,
	}
	live := backend.LivePane{
		Ref: backend.PaneRef{
			Backend: backend.Herdr, Workspace: saved.WorkspaceID, Pane: saved.PaneID,
		},
		CurrentPath: saved.WorktreePath, WorktreePath: saved.WorktreePath,
		WorkspaceLabel: saved.WorkspaceLabel, TerminalID: "term-" + name + "-new",
		RepoKey: saved.RepoKey, ProjectRoot: saved.RepoRoot,
		SessionID: saved.SessionID, SocketPath: saved.SocketPath,
	}
	return saved, live
}

func resumedCodexPane(placeholder backend.LivePane) backend.LivePane {
	resumed := placeholder
	resumed.AgentID, resumed.AgentProvider, resumed.AgentPresent = "restored-codex", "codex", true
	return resumed
}

func mustManagedResumeID(t *testing.T, pane state.Pane) string {
	t.Helper()
	id, err := state.ResumeIntentID(
		pane.SessionID, pane.SocketPath, pane.WorkspaceID, pane.PaneID,
	)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustResumeEnvironment(t *testing.T, runtimeDir, nonce string) string {
	t.Helper()
	path := filepath.Join(runtimeDir, "workload-env", "env-"+nonce+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func containsManagedResumeIntent(intents []state.LaunchIntent) bool {
	for _, intent := range intents {
		if intent.Kind == state.IntentResume {
			return true
		}
	}
	return false
}

// WorkloadEnvironment and DiscardWorkloadEnvironment delegate to the real
// implementations so resume tests keep the live capsule contract.
func (f *restartRuntimeFake) WorkloadEnvironment(
	caller []string,
	fanoutPath string,
) ([]string, error) {
	return herdrrun.WorkloadEnvironment(caller, fanoutPath)
}

func (f *restartRuntimeFake) DiscardWorkloadEnvironment(
	runtimeDir string,
	launch *state.LaunchCapsule,
) error {
	return herdrrun.DiscardWorkloadEnvironment(runtimeDir, launch)
}
