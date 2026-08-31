package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/sessionbinding"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

func TestIsHerdrLifecycleRequest(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{
		{args: []string{"herdr", "restart"}, want: true},
		{args: []string{"herdr", "shutdown"}, want: true},
		{args: []string{"restart"}},
		{args: nil},
	} {
		if got := isHerdrLifecycleRequest(test.args); got != test.want {
			t.Errorf("isHerdrLifecycleRequest(%q) = %t, want %t", test.args, got, test.want)
		}
	}
}

func TestHerdrLifecycleTimeoutLeavesFinalizationBudget(t *testing.T) {
	if herdrLifecycleTimeout <= backend.DefaultWaitTimeout {
		t.Fatalf(
			"herdr lifecycle timeout = %s, want greater than restart wait %s",
			herdrLifecycleTimeout,
			backend.DefaultWaitTimeout,
		)
	}
}

func TestRunHerdrLifecycleRequiresExplicitAction(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"bogus"}, {"restart", "shutdown"}} {
		var out, errOut bytes.Buffer
		called := false
		deps := herdrLifecycleDeps{projectRoot: func() (string, error) { called = true; return "/repo", nil }}
		code := runHerdrLifecycle(args, log.NewWith(&out, &errOut, false), deps)
		if code != exitcode.Invocation || called || !strings.Contains(errOut.String(), herdrLifecycleUsage) {
			t.Fatalf("runHerdrLifecycle(%q) = %d called=%t stderr=%q", args, code, called, errOut.String())
		}
	}
}

func TestRunHerdrLifecycleDispatchesOnlySelectedAction(t *testing.T) {
	for _, action := range []string{"restart", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			var out, errOut bytes.Buffer
			refreshes, restarts, shutdowns := 0, 0, 0
			deps := herdrLifecycleDeps{
				projectRoot: func() (string, error) { return "/repo", nil },
				repoIdentity: func(_ context.Context, root string) (worktree.RepoIdentity, error) {
					if root != "/repo" {
						return worktree.RepoIdentity{}, errors.New("wrong root")
					}
					return worktree.RepoIdentity{RepoKey: "/repo/.git", RepoRoot: root}, nil
				},
				refreshSessions: func(root string) error {
					refreshes++
					return nil
				},
				restart: func(_ context.Context, root, repoKey string) (string, error) {
					restarts++
					assertHerdrLifecycleInputs(t, root, repoKey)
					return "fanout-owned", nil
				},
				shutdown: func(_ context.Context, root, repoKey string) error {
					shutdowns++
					assertHerdrLifecycleInputs(t, root, repoKey)
					return nil
				},
			}
			code := runHerdrLifecycle([]string{action}, log.NewWith(&out, &errOut, false), deps)
			if code != exitcode.OK || errOut.Len() != 0 {
				t.Fatalf("runHerdrLifecycle(%q) = %d stderr=%q", action, code, errOut.String())
			}
			wantRefresh, wantRestart, wantShutdown := 0, 0, 1
			if action == "restart" {
				wantRefresh, wantRestart, wantShutdown = 1, 1, 0
			}
			if refreshes != wantRefresh || restarts != wantRestart || shutdowns != wantShutdown {
				t.Fatalf("calls refresh=%d restart=%d shutdown=%d", refreshes, restarts, shutdowns)
			}
		})
	}
}

func TestServerRestartRefreshesSessionOnlyWhenObservable(t *testing.T) {
	for _, test := range []struct {
		name        string
		unavailable bool
	}{
		{name: "live replacement is persisted before resume"},
		{name: "unobservable replacement stays stale", unavailable: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			row, live, next := lifecycleSessionFixture(root)
			want := *row.AgentSession
			if !test.unavailable {
				want = next
			}
			recordLifecycleSessionPane(t, root, row)
			listLive := func() ([]backend.LivePane, error) {
				if test.unavailable {
					return nil, errors.New("owned server is unavailable")
				}
				return []backend.LivePane{live}, nil
			}
			restarted := false
			deps := herdrLifecycleDeps{
				refreshSessions: func(root string) error {
					_, err := sessionbinding.StateLoader(root, listLive)()
					return err
				},
				restart: func(_ context.Context, projectRoot, _ string) (string, error) {
					store, err := state.LoadProject(projectRoot)
					if err != nil {
						return "", err
					}
					pane, ok := store.Find("733", 734)
					if !ok || pane.AgentSession == nil || *pane.AgentSession != want {
						return "", errors.New("restart saw an unexpected session")
					}
					restarted = true
					return "fanout-owned", nil
				},
			}
			var out, errOut bytes.Buffer
			code := executeServerRestart(
				context.Background(), root, "/repo/.git", log.NewWith(&out, &errOut, false),
				deps.refreshSessions, deps.restart,
			)
			if code != exitcode.OK || !restarted || errOut.Len() != 0 {
				t.Fatalf("restart code=%d restarted=%t stderr=%q", code, restarted, errOut.String())
			}
		})
	}
}

func lifecycleSessionFixture(root string) (state.Pane, backend.LivePane, backend.AgentSessionRef) {
	first := backend.AgentSessionRef{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "session-first"}
	next := first
	next.Value = "session-next"
	row := state.Pane{
		Parent: "733", IssueNum: 734, Backend: backend.Herdr,
		PaneID: "workspace-a:p1", WorkspaceID: "workspace-a", WorkspaceLabel: "owned-a",
		TerminalID: "terminal-a", Agent: "codex", AgentID: "agent-a", AgentSession: &first,
		SessionID: "session-a", SocketPath: "/tmp/herdr-a.sock",
		RepoKey: "/repo/.git", WorktreePath: filepath.Join(root, "child"),
	}
	live := backend.LivePane{
		Ref:            backend.PaneRef{Backend: backend.Herdr, Workspace: row.WorkspaceID, Pane: row.PaneID},
		WorkspaceLabel: row.WorkspaceLabel, TerminalID: row.TerminalID,
		AgentProvider: row.Agent, AgentID: row.AgentID, AgentSession: &next, AgentPresent: true,
		SessionID: row.SessionID, SocketPath: row.SocketPath,
		RepoKey: row.RepoKey, ProjectRoot: root, WorktreePath: row.WorktreePath,
	}
	return row, live, next
}

func recordLifecycleSessionPane(t *testing.T, root string, pane state.Pane) {
	t.Helper()
	locked, err := state.LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(pane); err == nil {
		err = locked.Unlock()
	}
	if err != nil {
		t.Fatal(err)
	}
}

func assertHerdrLifecycleInputs(t *testing.T, root, repoKey string) {
	t.Helper()
	if root != "/repo" || repoKey != "/repo/.git" {
		t.Fatalf("lifecycle inputs root=%q repoKey=%q", root, repoKey)
	}
}
