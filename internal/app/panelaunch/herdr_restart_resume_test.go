package panelaunch

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func (f *fakeHerdrLaunchRuntime) WaitRestoredPanes(
	_ context.Context,
	timeout time.Duration,
	match func([]backend.LivePane) bool,
) herdrrun.WaitResult {
	f.restartWaitCalls++
	f.restartWaitTimeout = timeout
	panes := append([]backend.LivePane(nil), f.live...)
	status := herdrrun.WaitTimedOut
	if match(panes) {
		status = herdrrun.WaitMatched
	}
	return herdrrun.WaitResult{Status: status, Panes: panes}
}

func (f *fakeHerdrLaunchRuntime) SendRestartResumeToken(context.Context, string, string) error {
	f.tokenCalls++
	return f.restartTokenErr
}

func TestResumeRestartedHerdrRowsRebindsExactCodexProcess(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	resumed := placeholder
	resumed.AgentID, resumed.AgentProvider, resumed.AgentPresent = "restored-codex", "codex", true
	recordRestartStatePane(t, repo, saved)
	runtime := &fakeHerdrLaunchRuntime{
		live: []backend.LivePane{placeholder},
		launchRoute: herdrrun.OwnedLaunchRoute{
			RuntimeDir: "/runtime", Session: saved.HerdrSession, SocketPath: saved.HerdrSocketPath,
			LauncherPath: "/runtime/launcher/fanout", ControlPath: "/repo/.git/fanout/herdr-intents.json",
		},
	}
	runtime.listLive = func(context.Context) ([]backend.LivePane, error) {
		if runtime.tokenCalls == 0 {
			return []backend.LivePane{placeholder}, nil
		}
		return []backend.LivePane{resumed}, nil
	}
	runtime.process = func(_ context.Context, _ string) (herdrrun.PaneProcessInfo, error) {
		if runtime.tokenCalls == 0 {
			return restartProcessInfo(runtime.launchRoute.LauncherPath, nil, saved.WorktreePath), nil
		}
		return restartProcessInfo(
			saved.HerdrLaunchExecutable,
			[]string{"resume", saved.HerdrAgentSession.Value},
			saved.WorktreePath,
		), nil
	}
	locked, journal := lockHerdrRestartTest(t, repo)

	if err := resumeRestartedHerdrRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, found := locked.Find(saved.Parent, saved.IssueNum)
	if !found || got.HerdrTerminalID != resumed.TerminalID || got.HerdrProcessIdentity == nil ||
		got.HerdrProcessIdentity.AgentPID != 10 || got.HerdrAgentID != resumed.AgentID || runtime.tokenCalls != 1 ||
		runtime.restartWaitCalls != 1 || runtime.restartWaitTimeout != 3*time.Second {
		t.Fatalf("rebound row/runtime = (%+v, %t, tokens=%d)", got, found, runtime.tokenCalls)
	}
	if len(got.HerdrLaunchArgs) != 2 || got.HerdrLaunchArgs[0] != "resume" ||
		got.HerdrLaunchArgs[1] != saved.HerdrAgentSession.Value {
		t.Fatalf("rebound argv = %#v", got.HerdrLaunchArgs)
	}
	if !containsHerdrResumeIntent(journal.Intents) {
		t.Fatal("successful resume intent was removed before server lifecycle completion")
	}
	completeRestartLifecycleForTest(t, locked, journal)
	if containsHerdrResumeIntent(journal.Intents) {
		t.Fatal("successful resume intent remained after server lifecycle completion")
	}
}

func TestResumeRestartedHerdrRowsDoesNotPartiallyResumeAfterWaitTimeout(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	missing := saved
	missing.IssueNum, missing.PaneID, missing.HerdrWorkspaceID = 533, "w2:p1", "w2"
	missing.HerdrTerminalID, missing.WorktreePath = "term-missing", "/repo/missing"
	missing.HerdrAgentSession = cloneAgentSession(saved.HerdrAgentSession)
	missing.HerdrAgentSession.Value = "019f-missing"
	recordRestartStatePane(t, repo, saved)
	recordRestartStatePane(t, repo, missing)
	runtime := &fakeHerdrLaunchRuntime{
		live: []backend.LivePane{placeholder},
		launchRoute: herdrrun.OwnedLaunchRoute{
			RuntimeDir: "/runtime", Session: saved.HerdrSession, SocketPath: saved.HerdrSocketPath,
			LauncherPath: "/runtime/launcher/fanout", ControlPath: "/repo/.git/fanout/herdr-intents.json",
		},
	}
	locked, journal := lockHerdrRestartTest(t, repo)

	if err := resumeRestartedHerdrRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, found := locked.Find(saved.Parent, saved.IssueNum)
	if !found || got.HerdrTerminalID != saved.HerdrTerminalID || got.ReportedState != "" ||
		got.StateRefinement || runtime.tokenCalls != 0 {
		t.Fatalf("timed-out observed row = (%+v, %t, tokens=%d)", got, found, runtime.tokenCalls)
	}
}

func TestResumeRestartedHerdrRowsDoesNotRetryFailedToken(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	recordRestartStatePane(t, repo, saved)
	runtime := &fakeHerdrLaunchRuntime{
		live: []backend.LivePane{placeholder}, restartTokenErr: errors.New("token failed"),
		launchRoute: herdrrun.OwnedLaunchRoute{
			RuntimeDir: "/runtime", Session: saved.HerdrSession, SocketPath: saved.HerdrSocketPath,
			LauncherPath: "/runtime/launcher/fanout", ControlPath: "/repo/.git/fanout/herdr-intents.json",
		},
	}
	runtime.process = func(context.Context, string) (herdrrun.PaneProcessInfo, error) {
		return restartProcessInfo(runtime.launchRoute.LauncherPath, nil, saved.WorktreePath), nil
	}
	locked, journal := lockHerdrRestartTest(t, repo)

	if err := resumeRestartedHerdrRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, found := locked.Find(saved.Parent, saved.IssueNum)
	if !found || got.HerdrTerminalID != saved.HerdrTerminalID || got.ReportedState != "" ||
		got.StateRefinement || got.EmitterNonce == "" || got.EmitterNonce == saved.EmitterNonce ||
		runtime.tokenCalls != 1 {
		t.Fatalf("failed resume state/runtime = (%+v, %t, tokens=%d)", got, found, runtime.tokenCalls)
	}
	for _, intent := range journal.Intents {
		if intent.Kind == state.HerdrIntentResume {
			t.Fatalf("failed resume intent remained replayable: %+v", intent)
		}
	}
}

func TestResumeRestartedHerdrRowsRecoversIssuedProcessWithoutResendingToken(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	resumed := placeholder
	resumed.AgentID, resumed.AgentProvider, resumed.AgentPresent = "restored-codex", "codex", true
	recordRestartStatePane(t, repo, saved)
	runtime := &fakeHerdrLaunchRuntime{live: []backend.LivePane{resumed}}
	runtime.process = func(context.Context, string) (herdrrun.PaneProcessInfo, error) {
		return restartProcessInfo(
			saved.HerdrLaunchExecutable,
			[]string{"resume", saved.HerdrAgentSession.Value},
			saved.WorktreePath,
		), nil
	}
	locked, journal := lockHerdrRestartTest(t, repo)
	recordIssuedHerdrResumeIntent(t, journal, saved, placeholder)

	if err := resumeRestartedHerdrRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, found := locked.Find(saved.Parent, saved.IssueNum)
	if !found || got.HerdrTerminalID != resumed.TerminalID || got.HerdrAgentID != resumed.AgentID ||
		runtime.tokenCalls != 0 || !containsHerdrResumeIntent(journal.Intents) {
		t.Fatalf("recovered resume = (%+v, %t, tokens=%d)", got, found, runtime.tokenCalls)
	}
	completeRestartLifecycleForTest(t, locked, journal)
	if containsHerdrResumeIntent(journal.Intents) {
		t.Fatal("recovered resume intent remained after server lifecycle completion")
	}
}

func TestResumeRestartedHerdrRowsCompletesAlreadySavedIssuedResume(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, live := restartCodexFixture()
	live.AgentID, live.AgentProvider, live.AgentPresent = "restored-codex", "codex", true
	process := backend.ProcessIdentity{ShellPID: 10, ForegroundProcessGroup: 10, AgentPID: 10}
	saved.HerdrTerminalID, saved.HerdrAgentID = live.TerminalID, live.AgentID
	saved.HerdrProcessIdentity = &process
	saved.HerdrLaunchArgs = []string{"resume", saved.HerdrAgentSession.Value}
	saved.ReportedState, saved.StateRefinement = "", false
	saved.EmitterRowKey, saved.LaunchNonce = "", ""
	saved.EmitterNonce = strings.Repeat("c", 32)
	recordRestartStatePane(t, repo, saved)
	runtime := &fakeHerdrLaunchRuntime{live: []backend.LivePane{live}}
	runtime.process = func(context.Context, string) (herdrrun.PaneProcessInfo, error) {
		return restartProcessInfo(saved.HerdrLaunchExecutable, saved.HerdrLaunchArgs, saved.WorktreePath), nil
	}
	locked, journal := lockHerdrRestartTest(t, repo)
	recordIssuedHerdrResumeIntent(t, journal, saved, live)

	if err := resumeRestartedHerdrRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, found := locked.Find(saved.Parent, saved.IssueNum)
	if !found || got.EmitterNonce != saved.EmitterNonce || runtime.tokenCalls != 0 ||
		!containsHerdrResumeIntent(journal.Intents) {
		t.Fatalf("completed saved resume = (%+v, %t, tokens=%d)", got, found, runtime.tokenCalls)
	}
	completeRestartLifecycleForTest(t, locked, journal)
	if containsHerdrResumeIntent(journal.Intents) {
		t.Fatal("saved resume intent remained after server lifecycle completion")
	}
}

func TestResumeRestartedHerdrRowsLeavesClaudeStale(t *testing.T) {
	repo := newHerdrRealizeRepo(t)
	saved, placeholder := restartCodexFixture()
	saved.Agent = "claude"
	saved.HerdrAgentSession.Source, saved.HerdrAgentSession.Agent = "herdr:claude", "claude"
	placeholder.AgentSession = cloneAgentSession(saved.HerdrAgentSession)
	recordRestartStatePane(t, repo, saved)
	runtime := &fakeHerdrLaunchRuntime{
		live: []backend.LivePane{placeholder},
		launchRoute: herdrrun.OwnedLaunchRoute{
			RuntimeDir: "/runtime", Session: saved.HerdrSession, SocketPath: saved.HerdrSocketPath,
			LauncherPath: "/runtime/launcher/fanout", ControlPath: "/repo/.git/fanout/herdr-intents.json",
		},
	}
	locked, journal := lockHerdrRestartTest(t, repo)

	if err := resumeRestartedHerdrRows(
		context.Background(), repo, locked, journal, runtime, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}
	got, found := locked.Find(saved.Parent, saved.IssueNum)
	if !found || got.HerdrTerminalID != saved.HerdrTerminalID || got.ReportedState != "" ||
		got.StateRefinement || runtime.tokenCalls != 0 {
		t.Fatalf("Claude restart state/runtime = (%+v, %t, tokens=%d)", got, found, runtime.tokenCalls)
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
	t.Cleanup(func() { _ = locked.Unlock() })
	journal, err := locked.HerdrIntents(repo)
	if err != nil {
		t.Fatal(err)
	}
	server, err := newHerdrServerIntent(state.HerdrIntentRestart, testHerdrServerIdentity())
	if err != nil {
		t.Fatal(err)
	}
	journal.UpsertIntent(server)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
	return locked, journal
}

func restartProcessInfo(executable string, args []string, cwd string) herdrrun.PaneProcessInfo {
	return herdrrun.PaneProcessInfo{
		ShellPID: 10, ForegroundProcessGroup: 10,
		ForegroundProcesses: []herdrrun.PaneProcess{{
			PID: 10, ParentPID: 1, ProcessGroup: 10, Executable: executable,
			Argv0: executable, Argv: args, CWD: cwd,
		}},
	}
}

func recordIssuedHerdrResumeIntent(
	t *testing.T,
	journal *state.LockedHerdrIntents,
	saved state.Pane,
	live backend.LivePane,
) {
	t.Helper()
	id, err := state.HerdrResumeIntentID(
		saved.HerdrSession, saved.HerdrSocketPath, saved.HerdrWorkspaceID, saved.PaneID,
	)
	if err != nil {
		t.Fatal(err)
	}
	intent := newHerdrResumeIntent(
		id, strings.Repeat("d", 32), "/runtime/env.json", 3,
		herdrRestartCandidate{row: herdrRestartRow{root: "/repo", saved: saved}, live: live},
		testFutureDeadline(),
	)
	intent.Launch.LauncherReady, intent.Launch.TokenIssued = true, true
	journal.UpsertIntent(intent)
	if err := journal.Save(); err != nil {
		t.Fatal(err)
	}
}

func containsHerdrResumeIntent(intents []state.HerdrIntent) bool {
	return slices.ContainsFunc(intents, func(intent state.HerdrIntent) bool {
		return intent.Kind == state.HerdrIntentResume
	})
}

func completeRestartLifecycleForTest(
	t *testing.T,
	locked *state.LockedStore,
	journal *state.LockedHerdrIntents,
) {
	t.Helper()
	id, err := state.HerdrServerIntentID(state.HerdrIntentRestart)
	if err != nil {
		t.Fatal(err)
	}
	if err := completeHerdrServerLifecycle(locked, journal, id); err != nil {
		t.Fatal(err)
	}
}

func TestExactRestartedCodexPlaceholderAcceptsIdleWithoutTreatingItAsDone(t *testing.T) {
	saved, live := restartCodexFixture()
	live.AgentState = backend.AgentIdle

	got, ok := exactRestartedCodexPlaceholder(saved, []backend.LivePane{live})
	if !ok || got.AgentState != backend.AgentIdle {
		t.Fatalf("placeholder = (%+v, %t), want exact idle candidate", got, ok)
	}
}

func TestExactHerdrResumePlaceholderRejectsMissingAgentSession(t *testing.T) {
	saved, live := restartCodexFixture()
	intent := state.HerdrIntent{
		Session: saved.HerdrSession, SocketPath: saved.HerdrSocketPath,
		Resource: state.HerdrResource{
			WorkspaceID: live.Ref.Workspace, PaneID: live.Ref.Pane,
			TerminalID: live.TerminalID, Label: live.WorkspaceLabel,
			CurrentPath: live.CurrentPath, RepoKey: live.RepoKey, RepoRoot: live.ProjectRoot,
		},
		ResumeAgentSession: saved.HerdrAgentSession,
	}
	live.AgentSession = nil

	if exactHerdrResumePlaceholder(intent, live) {
		t.Fatal("placeholder without an agent session matched")
	}
}

func TestExactRestartedCodexPlaceholderFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*state.Pane, *[]backend.LivePane)
	}{
		{name: "missing saved ref", mutate: func(saved *state.Pane, _ *[]backend.LivePane) {
			saved.HerdrAgentSession = nil
		}},
		{name: "duplicate current ref", mutate: func(_ *state.Pane, live *[]backend.LivePane) {
			duplicate := (*live)[0]
			duplicate.Ref.Pane = "w1:p2"
			*live = append(*live, duplicate)
		}},
		{name: "provider mismatch", mutate: func(_ *state.Pane, live *[]backend.LivePane) {
			(*live)[0].AgentProvider = "claude"
		}},
		{name: "unverified saved process", mutate: func(saved *state.Pane, _ *[]backend.LivePane) {
			saved.HerdrProcessIdentity = nil
		}},
		{name: "claude row", mutate: func(saved *state.Pane, _ *[]backend.LivePane) {
			saved.Agent = "claude"
		}},
		{name: "controller launch", mutate: func(saved *state.Pane, _ *[]backend.LivePane) {
			saved.HerdrDirectAgentLaunch = false
		}},
		{name: "worktree cwd mismatch", mutate: func(_ *state.Pane, live *[]backend.LivePane) {
			(*live)[0].CurrentPath = "/repo/other"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			saved, current := restartCodexFixture()
			live := []backend.LivePane{current}
			test.mutate(&saved, &live)
			if _, ok := exactRestartedCodexPlaceholder(saved, live); ok {
				t.Fatal("exactRestartedCodexPlaceholder() accepted an unsafe candidate")
			}
		})
	}
}

func TestNewHerdrResumeIntentPinsExactCodexArgv(t *testing.T) {
	saved, live := restartCodexFixture()
	live.AgentID = "restored-codex"
	intent := newHerdrResumeIntent(
		"resume:"+strings.Repeat("a", 64), strings.Repeat("b", 32), "/runtime/env.json", 3,
		herdrRestartCandidate{row: herdrRestartRow{root: "/repo", saved: saved}, live: live},
		testFutureDeadline(),
	)
	if intent.Launch.Executable != saved.HerdrLaunchExecutable ||
		intent.Launch.AgentName != "" ||
		!slicesEqual(intent.Launch.Args, []string{"resume", saved.HerdrAgentSession.Value}) {
		t.Fatalf("resume launch = %+v", intent.Launch)
	}
}

func TestApplyHerdrRestartRowBindsTerminalAndProcessInOneMutation(t *testing.T) {
	saved, live := restartCodexFixture()
	live.AgentID, live.AgentProvider, live.AgentPresent = "restored-codex", "codex", true
	store := state.Store{SchemaVersion: state.SchemaVersion, Panes: []state.Pane{saved}}
	process := backend.ProcessIdentity{ShellPID: 100, ForegroundProcessGroup: 100, AgentPID: 101}
	launch := state.HerdrLaunch{
		Executable: saved.HerdrLaunchExecutable, Args: []string{"resume", saved.HerdrAgentSession.Value},
	}

	if err := applyHerdrRestartRow(&store, saved, &live, &process, &launch); err != nil {
		t.Fatal(err)
	}
	got := store.Panes[0]
	if got.HerdrTerminalID != live.TerminalID || got.HerdrProcessIdentity == nil ||
		*got.HerdrProcessIdentity != process || got.ReportedState != "" || got.StateRefinement {
		t.Fatalf("rebound row = %+v", got)
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
		ReportedState: "working", StateRefinement: true,
	}
	live := backend.LivePane{
		Ref:         backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
		CurrentPath: "/repo/worktree", WorktreePath: "/repo/worktree",
		WorkspaceLabel: saved.HerdrWorkspaceLabel, TerminalID: "term-new",
		AgentSession: ref,
		RepoKey:      saved.HerdrRepoKey, ProjectRoot: saved.HerdrRepoRoot,
		SessionID: saved.HerdrSession, SocketPath: saved.HerdrSocketPath,
	}
	return saved, live
}

func slicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func testFutureDeadline() time.Time {
	return time.Now().Add(5 * time.Minute)
}
