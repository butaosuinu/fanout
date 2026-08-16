package lifecycle

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/panelayout"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

type nopLogger struct{}

func (nopLogger) Info(string, ...any) {}
func (nopLogger) Ok(string, ...any)   {}
func (nopLogger) Warn(string, ...any) {}
func (nopLogger) Err(string, ...any)  {}
func (nopLogger) Stderr() io.Writer   { return io.Discard }

type captureLogger struct {
	errors []string
}

func (*captureLogger) Info(string, ...any) {}
func (*captureLogger) Ok(string, ...any)   {}
func (*captureLogger) Warn(string, ...any) {}
func (l *captureLogger) Err(format string, args ...any) {
	l.errors = append(l.errors, fmt.Sprintf(format, args...))
}
func (*captureLogger) Stderr() io.Writer { return io.Discard }

type paneCloseCall struct {
	paneID       string
	worktreePath string
	shellKey     string
}

var closeTmuxPane = tmuxrun.ClosePaneIfOwned

func stubPaneClose(t *testing.T, fn func(string, string, string) (backend.ClosePaneResult, error)) *[]paneCloseCall {
	t.Helper()
	var calls []paneCloseCall
	orig := closeTmuxPane
	closeTmuxPane = func(paneID, worktreePath, shellKey string) (backend.ClosePaneResult, error) {
		calls = append(calls, paneCloseCall{paneID, worktreePath, shellKey})
		return fn(paneID, worktreePath, shellKey)
	}
	t.Cleanup(func() { closeTmuxPane = orig })
	return &calls
}

type relayoutCall struct {
	id   string
	trig panelayout.Trigger
}

func stubRelayout(t *testing.T) *[]relayoutCall {
	t.Helper()
	var calls []relayoutCall
	orig := relayoutWindow
	relayoutWindow = func(id string, trig panelayout.Trigger) error {
		calls = append(calls, relayoutCall{id, trig})
		return nil
	}
	t.Cleanup(func() { relayoutWindow = orig })
	return &calls
}

// closeAndRelayout mirrors the public entrypoints: capture windows during the
// close, then relayout the accumulated set once.
func closeAndRelayout(panes []state.Pane) {
	windows := map[string]struct{}{}
	closePaneRecordsLocked(fakeRuntimeOptions(), nil, panes, ClosePaneOnly, nopLogger{}, windows)
	relayoutClosedWindows(windows, nopLogger{})
}

func fakeRuntimeOptions() Options {
	return Options{
		Hooks: hooks.EmptyConfig(),
		CloseOwned: func(req backend.CloseRequest) (backend.CloseResult, error) {
			result, err := closeTmuxPane(req.Ref.Pane, req.WorktreePath, req.ShellKey)
			mapped := backend.CloseResult{Status: backend.CloseFailed, ContainerID: result.WindowID}
			switch result.Status {
			case backend.ClosePaneClosed:
				mapped.Status = backend.CloseConfirmed
			case backend.ClosePaneStale:
				mapped.Status = backend.CloseStale
			case backend.ClosePaneFailed:
				mapped.Status = backend.CloseFailed
			}
			return mapped, err
		},
	}
}

func TestClosePaneRecordsRelayoutsAffectedWindowOnce(t *testing.T) {
	stubPaneClose(t, func(string, string, string) (backend.ClosePaneResult, error) {
		return backend.ClosePaneResult{Status: backend.ClosePaneClosed, WindowID: "@1"}, nil
	})
	calls := stubRelayout(t)

	closeAndRelayout([]state.Pane{{PaneID: "%1", IssueNum: 1, WorktreePath: "/wt/shared"}, {PaneID: "%2", IssueNum: 2, WorktreePath: "/wt/shared"}})
	// Both panes share window @1, so it is re-laid-out exactly once.
	if len(*calls) != 1 {
		t.Fatalf("relayout calls = %+v, want one", *calls)
	}
	if (*calls)[0].id != "@1" || (*calls)[0].trig != panelayout.Close {
		t.Fatalf("relayout = %+v, want Close on @1", (*calls)[0])
	}
}

func TestClosePaneRecordsSkipsRelayoutWhenWindowUnknown(t *testing.T) {
	stubPaneClose(t, func(string, string, string) (backend.ClosePaneResult, error) {
		return backend.ClosePaneResult{Status: backend.ClosePaneClosed}, nil
	})
	calls := stubRelayout(t)

	closeAndRelayout([]state.Pane{{PaneID: "%1", IssueNum: 1, WorktreePath: "/wt/one"}})
	if len(*calls) != 0 {
		t.Fatalf("relayout calls = %+v, want none", *calls)
	}
}

func TestClosePaneRecordsCapturesWindowBeforeKill(t *testing.T) {
	// A pane with no recorded id can't be resolved to a window, so no relayout.
	stubPaneClose(t, func(string, string, string) (backend.ClosePaneResult, error) {
		return backend.ClosePaneResult{Status: backend.ClosePaneStale}, nil
	})
	calls := stubRelayout(t)

	closeAndRelayout([]state.Pane{{PaneID: "", IssueNum: 1}})
	if len(*calls) != 0 {
		t.Fatalf("relayout calls = %+v, want none for id-less pane", *calls)
	}
}

func TestCleanupAccumulatesWindowsAcrossPanes(t *testing.T) {
	// Two panes cleaned in separate cleanupPaneRecords calls but sharing one
	// window must relayout that window exactly once (the Cleanup-loop pattern).
	stubPaneClose(t, func(string, string, string) (backend.ClosePaneResult, error) {
		return backend.ClosePaneResult{Status: backend.ClosePaneClosed, WindowID: "@7"}, nil
	})
	calls := stubRelayout(t)

	windows := map[string]struct{}{}
	cleanupPaneRecordsLocked(fakeRuntimeOptions(), nil, []state.Pane{{PaneID: "%1", IssueNum: 1}}, nopLogger{}, windows)
	cleanupPaneRecordsLocked(fakeRuntimeOptions(), nil, []state.Pane{{PaneID: "%2", IssueNum: 2}}, nopLogger{}, windows)
	relayoutClosedWindows(windows, nopLogger{})

	if len(*calls) != 1 || (*calls)[0].id != "@7" {
		t.Fatalf("relayout calls = %+v, want one on @7", *calls)
	}
}

// A keyed attached agent (the plan fan-out coordinator) must take the
// identity-checked kill: when no live pane carries its liveness key, the close
// skips both the kill and the relayout instead of killing by pane id.
func TestClosePaneRecordsKeyVerifiesKeyedAttachedAgent(t *testing.T) {
	closeCalls := stubPaneClose(t, func(string, string, string) (backend.ClosePaneResult, error) {
		return backend.ClosePaneResult{Status: backend.ClosePaneStale}, nil
	})
	calls := stubRelayout(t)

	closeAndRelayout([]state.Pane{{PaneID: "%1", IssueNum: -1, Kind: state.PaneKindAttachedAgent, ShellKey: "shell-coordinator"}})
	if len(*calls) != 0 {
		t.Fatalf("relayout calls = %+v, want none when the liveness key cannot be confirmed", *calls)
	}
	if len(*closeCalls) != 1 || (*closeCalls)[0].shellKey != "shell-coordinator" {
		t.Fatalf("close calls = %+v, want keyed identity", *closeCalls)
	}
}

func TestClosePaneRecordsPreservesLiveLegacyShellWithoutKey(t *testing.T) {
	closeCalls := stubPaneClose(t, func(string, string, string) (backend.ClosePaneResult, error) {
		return backend.ClosePaneResult{Status: backend.ClosePaneFailed}, errors.New("live pane has no liveness key")
	})
	pane := state.Pane{
		PaneID:       "%1",
		IssueNum:     -1,
		Kind:         state.PaneKindShell,
		WorktreePath: "/repo",
	}

	if closePaneRecordsLocked(fakeRuntimeOptions(), nil, []state.Pane{pane}, ClosePaneOnly, nopLogger{}, map[string]struct{}{}) {
		t.Fatal("closePaneRecords() succeeded without a shell liveness key")
	}
	if len(*closeCalls) != 1 || (*closeCalls)[0].shellKey != "" {
		t.Fatalf("close calls = %+v, want one fail-closed legacy check", *closeCalls)
	}
}

func TestClosePaneRecordsPreservesLiveLegacyAttachedAgentWithoutKey(t *testing.T) {
	closeCalls := stubPaneClose(t, func(string, string, string) (backend.ClosePaneResult, error) {
		return backend.ClosePaneResult{Status: backend.ClosePaneFailed}, errors.New("live pane has no liveness key")
	})
	calls := stubRelayout(t)

	if closePaneRecordsLocked(fakeRuntimeOptions(), nil, []state.Pane{{PaneID: "%1", IssueNum: -1, Kind: state.PaneKindAttachedAgent, WorktreePath: "/wt/source"}}, ClosePaneOnly, nopLogger{}, map[string]struct{}{}) {
		t.Fatal("closePaneRecords() succeeded for a live legacy attached pane")
	}
	if len(*calls) != 0 {
		t.Fatalf("relayout calls = %+v, want none for a failed legacy close", *calls)
	}
	if len(*closeCalls) != 1 || (*closeCalls)[0].worktreePath != "/wt/source" || (*closeCalls)[0].shellKey != "" {
		t.Fatalf("close calls = %+v, want one fail-closed legacy check", *closeCalls)
	}
}

func TestClosePaneRecordsPassesOrdinaryPaneLivenessKey(t *testing.T) {
	closeCalls := stubPaneClose(t, func(string, string, string) (backend.ClosePaneResult, error) {
		return backend.ClosePaneResult{Status: backend.ClosePaneClosed, WindowID: "@1"}, nil
	})
	stubRelayout(t)

	closeAndRelayout([]state.Pane{{PaneID: "%1", IssueNum: 1, ShellKey: "shell-child", WorktreePath: "/wt/child"}})
	if len(*closeCalls) != 1 || (*closeCalls)[0].shellKey != "shell-child" {
		t.Fatalf("close calls = %+v, want ordinary pane liveness key", *closeCalls)
	}
}

func TestClosePaneRecordsStopsAllPanesBeforeRemovingWorktrees(t *testing.T) {
	eventsPath := filepath.Join(t.TempDir(), "events")
	appendEvent := func(event string) {
		f, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(event + "\n"); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	stubPaneClose(t, func(paneID, _, _ string) (backend.ClosePaneResult, error) {
		appendEvent("close " + paneID)
		return backend.ClosePaneResult{Status: backend.ClosePaneClosed}, nil
	})

	binDir := t.TempDir()
	gitScript := "#!/bin/sh\nprintf 'git %s\\n' \"$*\" >> \"$LIFECYCLE_EVENTS\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(gitScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LIFECYCLE_EVENTS", eventsPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	projectRoot := t.TempDir()
	wt1 := filepath.Join(t.TempDir(), "one")
	wt2 := filepath.Join(t.TempDir(), "two")
	if err := os.MkdirAll(wt1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wt2, 0o755); err != nil {
		t.Fatal(err)
	}
	panes := []state.Pane{
		{PaneID: "%1", IssueNum: 1, WorktreePath: wt1},
		{PaneID: "%2", IssueNum: 1, WorktreePath: wt2},
	}
	opts := fakeRuntimeOptions()
	opts.ProjectRoot = projectRoot
	if !closePaneRecordsLocked(opts, nil, panes, CloseWorktree, nopLogger{}, map[string]struct{}{}) {
		t.Fatal("closePaneRecords() failed")
	}
	body, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	events := strings.Split(strings.TrimSpace(string(body)), "\n")
	want := []string{
		"close %1",
		"close %2",
		"git -C " + projectRoot + " worktree remove " + wt1 + " --force",
		"git -C " + projectRoot + " worktree remove " + wt2 + " --force",
	}
	if strings.Join(events, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestClosePaneRecordsFailurePreservesEveryWorktree(t *testing.T) {
	closed := 0
	stubPaneClose(t, func(string, string, string) (backend.ClosePaneResult, error) {
		closed++
		if closed == 2 {
			return backend.ClosePaneResult{Status: backend.ClosePaneFailed}, errors.New("tmux unavailable")
		}
		return backend.ClosePaneResult{Status: backend.ClosePaneClosed}, nil
	})
	wt1 := filepath.Join(t.TempDir(), "one")
	wt2 := filepath.Join(t.TempDir(), "two")
	if err := os.MkdirAll(wt1, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wt2, 0o755); err != nil {
		t.Fatal(err)
	}
	panes := []state.Pane{
		{PaneID: "%1", IssueNum: 1, WorktreePath: wt1},
		{PaneID: "%2", IssueNum: 1, WorktreePath: wt2},
	}
	opts := fakeRuntimeOptions()
	opts.ProjectRoot = t.TempDir()
	if closePaneRecordsLocked(opts, nil, panes, CloseWorktree, nopLogger{}, map[string]struct{}{}) {
		t.Fatal("closePaneRecords() succeeded, want failure")
	}
	for _, path := range []string{wt1, wt2} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("worktree %s was removed after pane close failure: %v", path, err)
		}
	}
}

func TestCloseWithModePaneFailurePreservesStateAndWorktree(t *testing.T) {
	stubPaneClose(t, func(string, string, string) (backend.ClosePaneResult, error) {
		return backend.ClosePaneResult{Status: backend.ClosePaneFailed}, errors.New("pane still live")
	})
	projectRoot := t.TempDir()
	if err := exec.Command("git", "init", "-q", projectRoot).Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	worktreePath := filepath.Join(projectRoot, ".fanout", "worktrees", "child")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := state.Path(projectRoot)
	locked, err := state.Lock(statePath)
	if err != nil {
		t.Fatal(err)
	}
	pane := state.Pane{Parent: "81", IssueNum: 82, PaneID: "%5", WorktreePath: worktreePath}
	if recordErr := locked.RecordPane(pane); recordErr != nil {
		_ = locked.Unlock()
		t.Fatal(recordErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}

	opts := fakeRuntimeOptions()
	opts.ProjectRoot = projectRoot
	opts.StatePath = statePath
	code := CloseWithMode(opts, "81", 82, CloseWorktree, nopLogger{})
	if code != exitcode.Env {
		t.Fatalf("CloseWithMode() = %d, want Env", code)
	}
	if _, statErr := os.Stat(worktreePath); statErr != nil {
		t.Fatalf("worktree was removed after pane close failure: %v", statErr)
	}
	store, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Find("81", 82); !ok {
		t.Fatal("state row was removed after pane close failure")
	}
}

func TestClosePaneRecordsRoutesLegacyStateThroughBackendPort(t *testing.T) {
	var closed []backend.CloseRequest
	opts := fakeRuntimeOptions()
	opts.CloseOwned = func(req backend.CloseRequest) (backend.CloseResult, error) {
		closed = append(closed, req)
		return backend.CloseResult{Status: backend.CloseConfirmed}, nil
	}

	ok := closePaneRecordsLocked(opts, nil, []state.Pane{{PaneID: "%9", IssueNum: 9}}, ClosePaneOnly, nopLogger{}, map[string]struct{}{})
	if !ok {
		t.Fatal("closePaneRecords() = false, want true")
	}
	want := []backend.CloseRequest{{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%9"}}}
	if !reflect.DeepEqual(closed, want) {
		t.Fatalf("close requests = %#v, want %#v", closed, want)
	}
}

func TestClosePaneRecordsUsesBackendLivePaneForShellIdentity(t *testing.T) {
	ref := backend.PaneRef{Backend: backend.Tmux, Pane: "%2"}
	var closed []backend.CloseRequest
	opts := fakeRuntimeOptions()
	opts.CloseOwned = func(req backend.CloseRequest) (backend.CloseResult, error) {
		closed = append(closed, req)
		return backend.CloseResult{Status: backend.CloseConfirmed}, nil
	}

	ok := closePaneRecordsLocked(opts, nil, []state.Pane{{PaneID: "%2", IssueNum: -1, Kind: state.PaneKindShell, ShellKey: "shell-2"}}, ClosePaneOnly, nopLogger{}, map[string]struct{}{})
	if !ok {
		t.Fatal("closePaneRecords() = false, want true")
	}
	want := []backend.CloseRequest{{Ref: ref, ShellKey: "shell-2"}}
	if !reflect.DeepEqual(closed, want) {
		t.Fatalf("close requests = %#v, want %#v", closed, want)
	}
}

func TestPaneRefFromStateNormalizesLegacyTmuxAndPreservesHerdrWorkspace(t *testing.T) {
	tests := []struct {
		name string
		pane state.Pane
		want backend.PaneRef
	}{
		{
			name: "legacy tmux",
			pane: state.Pane{PaneID: "%12"},
			want: backend.PaneRef{Backend: backend.Tmux, Pane: "%12"},
		},
		{
			name: "herdr",
			pane: state.Pane{Backend: backend.Herdr, PaneID: "w2:p1", HerdrWorkspaceID: "w2"},
			want: backend.PaneRef{Backend: backend.Herdr, Workspace: "w2", Pane: "w2:p1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := paneRefFromState(tt.pane); got != tt.want {
				t.Fatalf("paneRefFromState() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestLockStateAfterHerdrPrecheckReacquiresCombinedLockWhenHerdrAppears(t *testing.T) {
	projectRoot := t.TempDir()
	runHerdrLifecycleGit(t, projectRoot, "init", "--initial-branch", "main")
	recordLifecyclePane(t, projectRoot, state.Pane{
		Parent: "423", IssueNum: 425, Backend: backend.Herdr,
		PaneID: "w2:p1", HerdrWorkspaceID: "w2",
	})
	opts := Options{ProjectRoot: projectRoot, StatePath: state.Path(projectRoot)}

	locked, err := lockStateAfterHerdrPrecheck(opts, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if unlockErr := locked.Unlock(); unlockErr != nil {
			t.Error(unlockErr)
		}
	}()
	if _, err := locked.HerdrIntents(projectRoot); err != nil {
		t.Fatalf("Herdr row found after precheck without combined lock: %v", err)
	}
}

func TestCloseHerdrFailsBeforeWorktreeAndStateMutation(t *testing.T) {
	projectRoot := t.TempDir()
	runHerdrLifecycleGit(t, projectRoot, "init", "--initial-branch", "main")
	worktreePath := filepath.Join(projectRoot, ".fanout", "worktrees", "child")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := state.Path(projectRoot)
	locked, lockErr := state.Lock(statePath)
	if lockErr != nil {
		t.Fatal(lockErr)
	}
	pane := state.Pane{
		Parent:           "423",
		IssueNum:         425,
		Backend:          backend.Herdr,
		ReportedState:    "done",
		PaneID:           "w2:p1",
		HerdrWorkspaceID: "w2",
		WorktreePath:     worktreePath,
	}
	if recordErr := locked.RecordPane(pane); recordErr != nil {
		_ = locked.Unlock()
		t.Fatal(recordErr)
	}
	if unlockErr := locked.Unlock(); unlockErr != nil {
		t.Fatal(unlockErr)
	}

	closeCalls := 0
	opts := Options{
		ProjectRoot: projectRoot,
		StatePath:   statePath,
		Hooks:       hooks.EmptyConfig(),
		CloseOwned: func(backend.CloseRequest) (backend.CloseResult, error) {
			closeCalls++
			return backend.CloseResult{Status: backend.CloseFailed}, backend.Unsupported(backend.Herdr, "close")
		},
	}
	if got := CloseWithMode(opts, "423", 425, CloseWorktree, nopLogger{}); got != exitcode.Env {
		t.Fatalf("CloseWithMode() = %d, want %d", got, exitcode.Env)
	}
	if closeCalls != 0 {
		t.Fatalf("CloseOwned calls = %d, want 0 for fail-closed herdr v1", closeCalls)
	}
	if _, statErr := os.Stat(worktreePath); statErr != nil {
		t.Fatalf("worktree changed before unsupported close was rejected: %v", statErr)
	}
	store, err := state.Load(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := store.Find("423", 425); !ok || got.Backend != backend.Herdr || got.PaneID != "w2:p1" {
		t.Fatalf("state row changed before unsupported close was rejected: %#v (found=%v)", got, ok)
	}
}

func TestCleanupHerdrFailsBeforeWorktreeAndStateMutation(t *testing.T) {
	projectRoot := t.TempDir()
	runHerdrLifecycleGit(t, projectRoot, "init", "--initial-branch", "main")
	installLifecycleCleanupGH(t)
	worktreePath := filepath.Join(projectRoot, ".fanout", "worktrees", "child")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	pane := state.Pane{
		Parent:        "423",
		IssueNum:      425,
		Backend:       backend.Herdr,
		ReportedState: "done",
		PaneID:        "w2:p1",
		WorktreePath:  worktreePath,
	}
	recordLifecyclePane(t, projectRoot, pane)

	closeCalls := 0
	opts := Options{
		ProjectRoot: projectRoot,
		StatePath:   state.Path(projectRoot),
		Hooks:       hooks.EmptyConfig(),
		CloseOwned: func(backend.CloseRequest) (backend.CloseResult, error) {
			closeCalls++
			return backend.CloseResult{}, nil
		},
	}
	lg := &captureLogger{}
	if got := Cleanup(opts, "423", lg); got != exitcode.Env {
		t.Fatalf("Cleanup() = %d, want %d; errors=%v", got, exitcode.Env, lg.errors)
	}
	assertHerdrCleanupUnchanged(t, projectRoot, worktreePath, "423", 425, "", closeCalls, lg.errors)
}

func TestCleanupPlanHerdrFailsBeforeWorktreeAndStateMutation(t *testing.T) {
	projectRoot := t.TempDir()
	runHerdrLifecycleGit(t, projectRoot, "init", "--initial-branch", "main")
	installLifecycleCleanupGH(t)
	worktreePath := filepath.Join(projectRoot, ".fanout", "worktrees", "task-a")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatal(err)
	}
	pane := state.Pane{
		Parent:        "plan:demo",
		IssueNum:      -1,
		TaskID:        "task-a",
		BranchName:    "fanout/task-a",
		Backend:       backend.Herdr,
		ReportedState: "done",
		PaneID:        "w2:p2",
		WorktreePath:  worktreePath,
	}
	recordLifecyclePane(t, projectRoot, pane)

	closeCalls := 0
	opts := Options{
		ProjectRoot: projectRoot,
		StatePath:   state.Path(projectRoot),
		Hooks:       hooks.EmptyConfig(),
		CloseOwned: func(backend.CloseRequest) (backend.CloseResult, error) {
			closeCalls++
			return backend.CloseResult{}, nil
		},
	}
	lg := &captureLogger{}
	if got := CleanupPlan(opts, "plan:demo", lg); got != exitcode.Env {
		t.Fatalf("CleanupPlan() = %d, want %d; errors=%v", got, exitcode.Env, lg.errors)
	}
	assertHerdrCleanupUnchanged(t, projectRoot, worktreePath, "plan:demo", -1, "task-a", closeCalls, lg.errors)
}

func installLifecycleCleanupGH(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
set -eu
case "${1:-} ${2:-}" in
  "repo view")
    printf '%s\n' 'owner/repo'
    ;;
  "api graphql")
    printf '%s\n' '{"state":"CLOSED","closedByPullRequestsReferences":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}'
    ;;
  "pr list")
    printf '%s\n' '[{"number":1,"state":"MERGED","mergedAt":"2026-07-20T00:00:00Z"}]'
    ;;
  *)
    exit 1
    ;;
esac
`
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func recordLifecyclePane(t *testing.T, projectRoot string, pane state.Pane) {
	t.Helper()
	locked, err := state.Lock(state.Path(projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	if err := locked.RecordPane(pane); err != nil {
		_ = locked.Unlock()
		t.Fatal(err)
	}
	if err := locked.Unlock(); err != nil {
		t.Fatal(err)
	}
}

func assertHerdrCleanupUnchanged(
	t *testing.T,
	projectRoot, worktreePath, parent string,
	issueNum int,
	taskID string,
	closeCalls int,
	errors []string,
) {
	t.Helper()
	if closeCalls != 0 {
		t.Fatalf("CloseOwned calls = %d, want 0", closeCalls)
	}
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("worktree changed before herdr cleanup rejection: %v", err)
	}
	store, err := state.Load(state.Path(projectRoot))
	if err != nil {
		t.Fatal(err)
	}
	var pane state.Pane
	var ok bool
	if taskID != "" {
		pane, ok = store.FindTask(parent, taskID)
	} else {
		pane, ok = store.Find(parent, issueNum)
	}
	if !ok || pane.Backend != backend.Herdr {
		t.Fatalf("state row changed before herdr cleanup rejection: %#v (found=%v)", pane, ok)
	}
	if !strings.Contains(strings.Join(errors, "\n"), "Herdr lifecycle runtime is not configured") {
		t.Fatalf("errors = %v, want explicit missing-runtime rejection", errors)
	}
}
