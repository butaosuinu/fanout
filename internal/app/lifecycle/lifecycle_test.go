package lifecycle

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/panelayout"
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

type paneCloseCall struct {
	paneID       string
	worktreePath string
	shellKey     string
}

func stubPaneClose(t *testing.T, fn func(string, string, string) (tmuxrun.ClosePaneResult, error)) *[]paneCloseCall {
	t.Helper()
	var calls []paneCloseCall
	orig := closeTmuxPane
	closeTmuxPane = func(paneID, worktreePath, shellKey string) (tmuxrun.ClosePaneResult, error) {
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
	closePaneRecords(Options{Hooks: hooks.EmptyConfig()}, panes, ClosePaneOnly, nopLogger{}, windows)
	relayoutClosedWindows(windows, nopLogger{})
}

func TestClosePaneRecordsRelayoutsAffectedWindowOnce(t *testing.T) {
	stubPaneClose(t, func(string, string, string) (tmuxrun.ClosePaneResult, error) {
		return tmuxrun.ClosePaneResult{Status: tmuxrun.ClosePaneClosed, WindowID: "@1"}, nil
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
	stubPaneClose(t, func(string, string, string) (tmuxrun.ClosePaneResult, error) {
		return tmuxrun.ClosePaneResult{Status: tmuxrun.ClosePaneClosed}, nil
	})
	calls := stubRelayout(t)

	closeAndRelayout([]state.Pane{{PaneID: "%1", IssueNum: 1, WorktreePath: "/wt/one"}})
	if len(*calls) != 0 {
		t.Fatalf("relayout calls = %+v, want none", *calls)
	}
}

func TestClosePaneRecordsCapturesWindowBeforeKill(t *testing.T) {
	// A pane with no recorded id can't be resolved to a window, so no relayout.
	stubPaneClose(t, func(string, string, string) (tmuxrun.ClosePaneResult, error) {
		return tmuxrun.ClosePaneResult{Status: tmuxrun.ClosePaneStale}, nil
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
	stubPaneClose(t, func(string, string, string) (tmuxrun.ClosePaneResult, error) {
		return tmuxrun.ClosePaneResult{Status: tmuxrun.ClosePaneClosed, WindowID: "@7"}, nil
	})
	calls := stubRelayout(t)

	windows := map[string]struct{}{}
	cleanupPaneRecords(Options{Hooks: hooks.EmptyConfig()}, []state.Pane{{PaneID: "%1", IssueNum: 1, WorktreePath: "/missing/one"}}, nopLogger{}, windows)
	cleanupPaneRecords(Options{Hooks: hooks.EmptyConfig()}, []state.Pane{{PaneID: "%2", IssueNum: 2, WorktreePath: "/missing/two"}}, nopLogger{}, windows)
	relayoutClosedWindows(windows, nopLogger{})

	if len(*calls) != 1 || (*calls)[0].id != "@7" {
		t.Fatalf("relayout calls = %+v, want one on @7", *calls)
	}
}

// A keyed attached agent (the plan fan-out coordinator) must take the
// identity-checked kill: when no live pane carries its liveness key, the close
// skips both the kill and the relayout instead of killing by pane id.
func TestClosePaneRecordsKeyVerifiesKeyedAttachedAgent(t *testing.T) {
	closeCalls := stubPaneClose(t, func(string, string, string) (tmuxrun.ClosePaneResult, error) {
		return tmuxrun.ClosePaneResult{Status: tmuxrun.ClosePaneStale}, nil
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

func TestClosePaneRecordsPreservesLegacyShellWithoutKey(t *testing.T) {
	closeCalls := stubPaneClose(t, func(string, string, string) (tmuxrun.ClosePaneResult, error) {
		return tmuxrun.ClosePaneResult{Status: tmuxrun.ClosePaneClosed}, nil
	})
	pane := state.Pane{
		PaneID:       "%1",
		IssueNum:     -1,
		Kind:         state.PaneKindShell,
		WorktreePath: "/repo",
	}

	if closePaneRecords(Options{Hooks: hooks.EmptyConfig()}, []state.Pane{pane}, ClosePaneOnly, nopLogger{}, map[string]struct{}{}) {
		t.Fatal("closePaneRecords() succeeded without a shell liveness key")
	}
	if len(*closeCalls) != 0 {
		t.Fatalf("close calls = %+v, want none for an unverified legacy shell", *closeCalls)
	}
}

// An attached agent without a liveness key uses the same recorded-worktree
// identity as an ordinary agent pane.
func TestClosePaneRecordsVerifiesUnkeyedAttachedAgentWorktree(t *testing.T) {
	closeCalls := stubPaneClose(t, func(string, string, string) (tmuxrun.ClosePaneResult, error) {
		return tmuxrun.ClosePaneResult{Status: tmuxrun.ClosePaneClosed, WindowID: "@1"}, nil
	})
	calls := stubRelayout(t)

	closeAndRelayout([]state.Pane{{PaneID: "%1", IssueNum: -1, Kind: state.PaneKindAttachedAgent, WorktreePath: "/wt/source"}})
	if len(*calls) != 1 {
		t.Fatalf("relayout calls = %+v, want one for an unkeyed attached agent", *calls)
	}
	if len(*closeCalls) != 1 || (*closeCalls)[0].worktreePath != "/wt/source" || (*closeCalls)[0].shellKey != "" {
		t.Fatalf("close calls = %+v, want worktree identity", *closeCalls)
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
	stubPaneClose(t, func(paneID, _, _ string) (tmuxrun.ClosePaneResult, error) {
		appendEvent("close " + paneID)
		return tmuxrun.ClosePaneResult{Status: tmuxrun.ClosePaneClosed}, nil
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
	if !closePaneRecords(Options{ProjectRoot: projectRoot, Hooks: hooks.EmptyConfig()}, panes, CloseWorktree, nopLogger{}, map[string]struct{}{}) {
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
	stubPaneClose(t, func(string, string, string) (tmuxrun.ClosePaneResult, error) {
		closed++
		if closed == 2 {
			return tmuxrun.ClosePaneResult{Status: tmuxrun.ClosePaneFailed}, errors.New("tmux unavailable")
		}
		return tmuxrun.ClosePaneResult{Status: tmuxrun.ClosePaneClosed}, nil
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
	if closePaneRecords(Options{ProjectRoot: t.TempDir(), Hooks: hooks.EmptyConfig()}, panes, CloseWorktree, nopLogger{}, map[string]struct{}{}) {
		t.Fatal("closePaneRecords() succeeded, want failure")
	}
	for _, path := range []string{wt1, wt2} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("worktree %s was removed after pane close failure: %v", path, err)
		}
	}
}

func TestCloseWithModePaneFailurePreservesStateAndWorktree(t *testing.T) {
	stubPaneClose(t, func(string, string, string) (tmuxrun.ClosePaneResult, error) {
		return tmuxrun.ClosePaneResult{Status: tmuxrun.ClosePaneFailed}, errors.New("pane still live")
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

	code := CloseWithMode(Options{ProjectRoot: projectRoot, StatePath: statePath, Hooks: hooks.EmptyConfig()}, "81", 82, CloseWorktree, nopLogger{})
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
