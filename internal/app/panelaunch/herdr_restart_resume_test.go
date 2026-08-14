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
	route           herdrrun.OwnedLaunchRoute
	waitPanes       []backend.LivePane
	issuePanes      []backend.LivePane
	resumedPanes    []backend.LivePane
	launcherInfo    herdrrun.PaneProcessInfo
	resumedInfo     herdrrun.PaneProcessInfo
	waitTimeout     time.Duration
	waitDelay       time.Duration
	waitCalls       int
	issueCalls      int
	issueTimeout    time.Duration
	issueErr        error
	onIssue         func()
	preparedEnvPath string
}

func (f *restartRuntimeFake) LaunchRoute() (herdrrun.OwnedLaunchRoute, error) {
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
) herdrrun.WaitResult {
	f.waitCalls++
	f.waitTimeout = timeout
	time.Sleep(f.waitDelay)
	status := herdrrun.WaitTimedOut
	if match(f.waitPanes) {
		status = herdrrun.WaitMatched
	}
	return herdrrun.WaitResult{Status: status, Panes: slices.Clone(f.waitPanes)}
}

func (f *restartRuntimeFake) IssueRestartResume(
	_ context.Context,
	_, _ string,
	timeout time.Duration,
	preflight func(herdrrun.PaneProcessInfo, []backend.LivePane) error,
	markIssued func() error,
) error {
	f.issueTimeout = timeout
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
) (herdrrun.PaneProcessInfo, []backend.LivePane, error) {
	return f.resumedInfo, slices.Clone(f.resumedPanes), nil
}

func TestResumeRestartedHerdrRowsRebindsExactCodexProcess(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	placeholder.AgentState = backend.AgentIdle
	resumed := resumedCodexPane(placeholder)
	recordRestartStatePane(t, repo, saved)
	locked, journal := lockHerdrRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumed)
	runtime.waitDelay = 500 * time.Millisecond

	if err := resumeRestartedHerdrRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, found := locked.Find(saved.Parent, saved.IssueNum)
	if !found || got.HerdrTerminalID != resumed.TerminalID || got.HerdrAgentID != resumed.AgentID ||
		got.HerdrProcessIdentity == nil || got.HerdrProcessIdentity.AgentPID != 10 {
		t.Fatalf("rebound row = (%+v, %t)", got, found)
	}
	if got.ReportedState != "" || got.StateRefinement || got.EmitterNonce == saved.EmitterNonce {
		t.Fatalf("rebound telemetry = (%q, %t, %q)", got.ReportedState, got.StateRefinement, got.EmitterNonce)
	}
	wantArgs := []string{"resume", saved.HerdrAgentSession.Value}
	if !slices.Equal(got.HerdrLaunchArgs, wantArgs) || runtime.issueCalls != 1 ||
		runtime.waitCalls != 1 || runtime.waitTimeout != 3*time.Second {
		t.Fatalf("resume result args=%q issues=%d wait=%d/%s", got.HerdrLaunchArgs, runtime.issueCalls, runtime.waitCalls, runtime.waitTimeout)
	}
	if runtime.issueTimeout <= 0 || runtime.issueTimeout >= 2750*time.Millisecond {
		t.Fatalf("resume issue timeout = %s, want wait time deducted from 3s budget", runtime.issueTimeout)
	}
	if containsHerdrResumeIntent(journal.Intents) {
		t.Fatal("successful resume intent remains")
	}
}

func TestResumeRestartedHerdrRowsDoesNotWaitForUnsupportedMissingRoute(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	unsupported := saved
	unsupported.IssueNum, unsupported.PaneID = 533, "w1:p2"
	unsupported.Agent = "claude"
	recordRestartStatePane(t, repo, saved)
	recordRestartStatePane(t, repo, unsupported)
	locked, journal := lockHerdrRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))

	if err := resumeRestartedHerdrRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	if runtime.waitCalls != 1 || runtime.issueCalls != 1 {
		t.Fatalf("restart calls = (wait=%d, issue=%d), want (1, 1)", runtime.waitCalls, runtime.issueCalls)
	}
	got, found := locked.Find(unsupported.Parent, unsupported.IssueNum)
	if !found || got.HerdrDirectAgentLaunch || got.ReportedState != "" || got.StateRefinement {
		t.Fatalf("unsupported missing row = (%+v, %t), want stale", got, found)
	}
}

func TestResumeRestartedHerdrRowsDoesNotLetMissingCodexBlockExactCandidate(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	missing := saved
	missing.IssueNum, missing.PaneID = 533, "w1:p2"
	missing.HerdrAgentSession = &backend.AgentSessionRef{
		Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "other-session",
	}
	recordRestartStatePane(t, repo, saved)
	recordRestartStatePane(t, repo, missing)
	locked, journal := lockHerdrRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))

	if err := resumeRestartedHerdrRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, found := locked.Find(saved.Parent, saved.IssueNum)
	if !found || got.HerdrTerminalID != placeholder.TerminalID || runtime.issueCalls != 1 {
		t.Fatalf("exact row/runtime = (%+v, %t, issues=%d)", got, found, runtime.issueCalls)
	}
	stale, found := locked.Find(missing.Parent, missing.IssueNum)
	if !found || stale.HerdrDirectAgentLaunch || runtime.waitCalls != 1 {
		t.Fatalf("missing row/runtime = (%+v, %t, waits=%d)", stale, found, runtime.waitCalls)
	}
}

func TestResumeRestartedHerdrRowsRejectsDuplicateRefAddedBeforeToken(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	duplicate := placeholder
	duplicate.Ref.Pane = "w1:p2"
	recordRestartStatePane(t, repo, saved)
	locked, journal := lockHerdrRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))
	runtime.issuePanes = []backend.LivePane{placeholder, duplicate}

	if err := resumeRestartedHerdrRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, found := locked.Find(saved.Parent, saved.IssueNum)
	if !found || got.HerdrDirectAgentLaunch || runtime.issueCalls != 0 {
		t.Fatalf("duplicate preflight result = (%+v, %t, issues=%d)", got, found, runtime.issueCalls)
	}
}

func TestResumeRestartedHerdrRowsLeavesUnsupportedRowsStale(t *testing.T) {
	tests := map[string]func(*state.Pane, *backend.LivePane, *[]backend.LivePane){
		"missing ref": func(saved *state.Pane, _ *backend.LivePane, _ *[]backend.LivePane) {
			saved.HerdrAgentSession = nil
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
			repo := newHerdrRealizeRepo(t)
			saved, placeholder := restartCodexFixture()
			runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))
			panes := []backend.LivePane{placeholder}
			mutate(&saved, &placeholder, &panes)
			panes[0] = placeholder
			recordRestartStatePane(t, repo, saved)
			locked, journal := lockHerdrRestartTest(t, repo)
			runtime.waitPanes = panes

			if err := resumeRestartedHerdrRows(
				context.Background(), repo, locked, journal, runtime, 3*time.Second,
			); err != nil {
				t.Fatal(err)
			}
			got, found := locked.Find(saved.Parent, saved.IssueNum)
			if !found || got.HerdrTerminalID != saved.HerdrTerminalID ||
				got.ReportedState != "" || got.StateRefinement || runtime.issueCalls != 0 {
				t.Fatalf("stale row/runtime = (%+v, %t, issues=%d)", got, found, runtime.issueCalls)
			}
		})
	}
}

func TestResumeRestartedHerdrRowsRejectsUnsupportedUnchangedTerminal(t *testing.T) {
	for _, kind := range []string{"provider", "attached"} {
		t.Run(kind, func(t *testing.T) {
			repo := newHerdrRealizeRepo(t)
			saved, placeholder := restartCodexFixture()
			if kind == "provider" {
				saved.Agent = "claude"
			} else {
				saved.Kind = state.PaneKindAttachedAgent
			}
			placeholder.TerminalID = saved.HerdrTerminalID
			recordRestartStatePane(t, repo, saved)
			locked, journal := lockHerdrRestartTest(t, repo)
			runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))

			err := resumeRestartedHerdrRows(
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

func TestResumeRestartedHerdrRowsDoesNotReplayInterruptedIntent(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	recordRestartStatePane(t, repo, saved)
	locked, journal := lockHerdrRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))
	candidate := herdrRestartCandidate{
		row: herdrRestartRow{root: repo, current: true, saved: saved}, live: placeholder,
	}
	intent := newHerdrResumeIntent(
		mustHerdrResumeID(t, saved), strings.Repeat("a", 32),
		mustResumeEnvironment(t, runtime.route.RuntimeDir, strings.Repeat("a", 32)),
		1, candidate, time.Now().Add(time.Minute),
	)
	if intent.Launch.AgentSessionStatePath != state.Path(repo) {
		t.Fatalf("resume relay state path = %q, want %q", intent.Launch.AgentSessionStatePath, state.Path(repo))
	}
	intent.Launch.LauncherReady, intent.Launch.TokenIssued = true, true
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}

	if err := resumeRestartedHerdrRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, _ := locked.Find(saved.Parent, saved.IssueNum)
	if runtime.issueCalls != 0 || got.HerdrTerminalID != saved.HerdrTerminalID ||
		containsHerdrResumeIntent(journal.Intents) {
		t.Fatalf("interrupted resume was replayed: row=%+v calls=%d", got, runtime.issueCalls)
	}
}

func TestResumeRestartedHerdrRowsLeavesLostTokenResponseStale(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	recordRestartStatePane(t, repo, saved)
	locked, journal := lockHerdrRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))
	runtime.issueErr = errors.New("pane run response was lost")

	if err := resumeRestartedHerdrRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, _ := locked.Find(saved.Parent, saved.IssueNum)
	if runtime.issueCalls != 1 || got.HerdrTerminalID != saved.HerdrTerminalID ||
		got.HerdrDirectAgentLaunch || got.ReportedState != "" || got.StateRefinement ||
		containsHerdrResumeIntent(journal.Intents) {
		t.Fatalf("lost token response result: row=%+v calls=%d", got, runtime.issueCalls)
	}
	if err := resumeRestartedHerdrRows(context.Background(), repo, locked, journal, runtime, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if runtime.issueCalls != 1 {
		t.Fatalf("lost token response was retried: calls=%d", runtime.issueCalls)
	}
}

func TestResumeRestartedHerdrRowsRejectsIdentityChangeBeforeCommit(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	recordRestartStatePane(t, repo, saved)
	locked, journal := lockHerdrRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))
	runtime.onIssue = func() {
		pane, found := locked.Find(saved.Parent, saved.IssueNum)
		if !found {
			t.Fatal("saved row disappeared")
		}
		pane.HerdrTerminalID = "changed-concurrently"
		locked.Panes[0] = pane
	}

	err := resumeRestartedHerdrRows(context.Background(), repo, locked, journal, runtime, 3*time.Second)
	if err == nil || !strings.Contains(err.Error(), "changed before finalization") {
		t.Fatalf("identity change error = %v", err)
	}
}

func TestResumeRestartedHerdrRowsRejectsUnchangedTerminal(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	placeholder.TerminalID = saved.HerdrTerminalID
	recordRestartStatePane(t, repo, saved)
	locked, journal := lockHerdrRestartTest(t, repo)
	runtime := newRestartRuntimeFake(t, saved, placeholder, resumedCodexPane(placeholder))

	err := resumeRestartedHerdrRows(context.Background(), repo, locked, journal, runtime, 3*time.Second)
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
		route: herdrrun.OwnedLaunchRoute{
			RuntimeDir: runtimeDir, Session: saved.HerdrSession, SocketPath: saved.HerdrSocketPath,
			LauncherPath: launcher, ControlPath: filepath.Join(runtimeDir, "herdr-intents.json"),
		},
		waitPanes:    []backend.LivePane{placeholder},
		resumedPanes: []backend.LivePane{resumed},
		launcherInfo: restartProcessInfo(launcher, nil, saved.WorktreePath),
		resumedInfo: restartProcessInfo(
			saved.HerdrLaunchExecutable, []string{"resume", saved.HerdrAgentSession.Value}, saved.WorktreePath,
		),
	}
}

func lockHerdrRestartTest(
	t *testing.T,
	repo string,
) (*state.LockedStore, *state.LockedHerdrIntents) {
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
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	return locked, journal
}

func restartProcessInfo(executable string, args []string, cwd string) herdrrun.PaneProcessInfo {
	return herdrrun.PaneProcessInfo{
		PaneID: "w1:p1", ShellPID: 10, ForegroundProcessGroup: 10,
		ForegroundProcesses: []herdrrun.PaneProcess{{
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
		PaneID: "w1:p1", HerdrWorkspaceID: "w1", HerdrWorkspaceLabel: "fanout-workspace-token",
		HerdrTerminalID: "term-old", HerdrRepoKey: "/repo/.git", HerdrRepoRoot: "/repo",
		HerdrAgentID: "fanout-codex", HerdrAgentSession: ref,
		HerdrProcessIdentity: &backend.ProcessIdentity{ShellPID: 10, ForegroundProcessGroup: 10, AgentPID: 10},
		HerdrSession:         "fanout-owned", HerdrSocketPath: "/runtime/herdr.sock",
		HerdrLaunchExecutable: "/opt/codex", HerdrLaunchArgs: []string{"prompt"},
		HerdrDirectAgentLaunch: true, Agent: "codex", WorktreePath: "/repo/worktree",
		ReportedState: "working", StateRefinement: true, EmitterNonce: strings.Repeat("b", 32),
	}
	live := backend.LivePane{
		Ref:         backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
		CurrentPath: saved.WorktreePath, WorktreePath: saved.WorktreePath,
		WorkspaceLabel: saved.HerdrWorkspaceLabel, TerminalID: "term-new",
		AgentSession: ref, RepoKey: saved.HerdrRepoKey, ProjectRoot: saved.HerdrRepoRoot,
		SessionID: saved.HerdrSession, SocketPath: saved.HerdrSocketPath,
	}
	return saved, live
}

func resumedCodexPane(placeholder backend.LivePane) backend.LivePane {
	resumed := placeholder
	resumed.AgentID, resumed.AgentProvider, resumed.AgentPresent = "restored-codex", "codex", true
	return resumed
}

func mustHerdrResumeID(t *testing.T, pane state.Pane) string {
	t.Helper()
	id, err := state.HerdrResumeIntentID(
		pane.HerdrSession, pane.HerdrSocketPath, pane.HerdrWorkspaceID, pane.PaneID,
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

func containsHerdrResumeIntent(intents []state.HerdrIntent) bool {
	for _, intent := range intents {
		if intent.Kind == state.HerdrIntentResume {
			return true
		}
	}
	return false
}
