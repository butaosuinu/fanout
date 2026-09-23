package panelaunch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type consoleRuntimeFake struct {
	fakeManagedLaunchRuntime
	t        *testing.T
	root     string
	onToken  func()
	tokenErr error
	intent   state.LaunchIntent
}

func newConsoleRuntimeFake(t *testing.T, root string) *consoleRuntimeFake {
	t.Helper()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	f := &consoleRuntimeFake{t: t, root: root}
	installSuccessfulManagedMutations(t, root, &f.fakeManagedRealizeRuntime)
	runtimeDir := t.TempDir()
	f.launchRoute = backend.OwnedLaunchRoute{
		Session: f.route.Session, SocketPath: f.route.SocketPath, RuntimeDir: runtimeDir,
		LauncherPath: filepath.Join(runtimeDir, "launcher", "fanout"),
	}
	f.processInfo = restartProcessInfo(f.launchRoute.LauncherPath, nil, root)
	f.listLive = func(ctx context.Context) ([]backend.LivePane, error) {
		panes := append([]backend.LivePane(nil), f.live...)
		for _, w := range f.workspaces {
			panes = append(panes, backend.LivePane{
				Ref: w.Pane, TerminalID: w.TerminalID, WorkspaceLabel: w.Label,
				CurrentPath: w.CWD, SessionID: f.route.Session, SocketPath: f.route.SocketPath,
			})
		}
		return panes, ctx.Err()
	}
	return f
}

func (f *consoleRuntimeFake) AttachForms([]string) (string, backend.AttachExec, error) {
	return "attach", backend.AttachExec{}, nil
}

func (f *consoleRuntimeFake) VerifyOwnedTarget(target backend.OwnedPaneIdentity) error {
	panes, err := f.LivePanes(context.Background())
	if err != nil {
		return err
	}
	for _, pane := range panes {
		if pane.Ref == target.Ref && pane.TerminalID == target.TerminalID &&
			pane.WorkspaceLabel == target.WorkspaceLabel && pane.CurrentPath == target.CurrentPath &&
			pane.SessionID == target.SessionID && pane.SocketPath == target.SocketPath {
			return nil
		}
	}
	return backend.ErrOwnedIdentityMismatch
}

func (f *consoleRuntimeFake) BindOwnedWorkspaceClose(backend.OwnedPaneIdentity) (backend.OwnedClosingBackend, error) {
	f.t.Fatal("console recovery must not close an existing workspace")
	return nil, errors.New("unexpected close")
}

func (f *consoleRuntimeFake) PrepareWorkloadEnvironment(nonce string, env []string) (string, int, error) {
	return (&herdrrun.OwnedSession{RuntimeDir: f.launchRoute.RuntimeDir}).PrepareWorkloadEnvironment(nonce, env)
}

func (f *consoleRuntimeFake) SendLaunchToken(_ context.Context, paneID, nonce string) error {
	f.tokenCalls++
	journal, err := state.LoadLaunchJournal(f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, intent := range journal.Intents {
		if intent.Resource.PaneID == paneID && intent.Launch != nil && intent.Launch.Nonce == nonce {
			f.intent = intent
		}
	}
	if f.intent.Launch == nil || !f.intent.Launch.TokenIssued {
		f.t.Fatal("token sent without durable launch intent")
	}
	if err := os.Remove(f.intent.Launch.EnvFilePath); err != nil {
		f.t.Fatal(err)
	}
	f.processInfo = restartProcessInfo(f.launchRoute.LauncherPath, []string{ManagedConsoleWorkloadArg}, f.intent.WorktreePath)
	if f.onToken != nil {
		f.onToken()
	}
	return f.tokenErr
}

func (f *consoleRuntimeFake) bootstrap(ctx context.Context, root string) (ManagedConsoleResult, error) {
	return EnsureManagedConsole(ctx, root, f, []string{"PATH=/usr/bin", "TERM=xterm"}, "/bin/sh")
}

func (f *consoleRuntimeFake) restore(t *testing.T, pane state.Pane) state.Pane {
	t.Helper()
	oldNonce := f.intent.Launch.Nonce
	f.workspaces[0].TerminalID = "restored-terminal"
	f.processInfo = restartProcessInfo(f.launchRoute.LauncherPath, nil, pane.WorktreePath)
	live, err := f.LivePanes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restarted := &restartRuntimeFake{t: t, route: f.launchRoute, waitPanes: live}
	locked, err := state.LockProjectForLaunch(f.root)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := locked.LaunchJournal(f.root)
	if err == nil {
		err = resumeRestartedManagedRows(context.Background(), f.root, locked, journal, restarted, 3*time.Second)
	}
	got, _ := locked.Find(pane.Parent, pane.IssueNum)
	err = errors.Join(err, locked.Unlock())
	if err != nil {
		t.Fatal(err)
	}
	if got.TerminalID != "restored-terminal" || restarted.issueCalls != 0 || f.intent.Launch.Nonce != oldNonce {
		t.Fatalf("restart must only refresh console binding: %+v", got)
	}
	return got
}

func TestManagedConsoleColdRestartReadinessAndSharedReuse(t *testing.T) {
	root, linked := managedConsoleTestWorktrees(t)
	f := newConsoleRuntimeFake(t, root)
	first, err := f.bootstrap(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	oldNonce, oldExpiry := f.intent.Launch.Nonce, f.intent.ExpiresUnixMS
	restored := f.restore(t, first.Pane)

	// Both linked roots contend on the common launch lock. A busy caller can
	// retry; only one may publish a capsule or issue a token.
	var wg sync.WaitGroup
	for _, cwd := range []string{root, linked} {
		wg.Go(func() {
			_, bootErr := f.bootstrap(context.Background(), cwd)
			if bootErr != nil && !strings.Contains(bootErr.Error(), "locked") {
				t.Errorf("concurrent bootstrap: %v", bootErr)
			}
		})
	}
	wg.Wait()
	for _, cwd := range []string{linked, root} {
		result, bootErr := f.bootstrap(context.Background(), cwd)
		if bootErr != nil || result.Pane.PaneID != first.Pane.PaneID ||
			result.Pane.IssueNum != first.Pane.IssueNum || result.Pane.SourceProjectRoot != root {
			t.Fatalf("shared console = %+v, %v", result, bootErr)
		}
	}
	if f.tokenCalls != 2 || len(f.mutations) != 1 || f.intent.Launch.Nonce == oldNonce ||
		f.intent.ExpiresUnixMS <= oldExpiry || !reflect.DeepEqual(f.processInfo.ForegroundProcesses[0].Argv, []string{ManagedConsoleWorkloadArg}) {
		t.Fatalf("restored readiness: tokens=%d mutations=%d intent=%+v", f.tokenCalls, len(f.mutations), f.intent)
	}
	store, err := state.LoadProject(root)
	if err != nil || len(store.Panes) != 1 || !reflect.DeepEqual(store.Panes[0], restored) {
		t.Fatalf("restored row changed: %+v, %v", store.Panes, err)
	}
	// Quitting the TUI hands off to the operator shell; reuse must not launch a
	// second TUI or send a start token to that shell.
	shell, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	f.processInfo = restartProcessInfo(shell, nil, root)
	if _, err := f.bootstrap(context.Background(), linked); err != nil || f.tokenCalls != 2 {
		t.Fatalf("shell handoff reuse: %v, tokens=%d", err, f.tokenCalls)
	}
	f.processInfo = restartProcessInfo(f.launchRoute.LauncherPath, nil, root)
	f.processInfo.ShellPID = 5
	f.processInfo.ForegroundProcesses[0].ParentPID = 5
	if _, err := f.bootstrap(context.Background(), linked); err != nil || f.tokenCalls != 2 {
		t.Fatalf("reopened TUI reuse: %v, tokens=%d", err, f.tokenCalls)
	}
}

func TestManagedConsoleReopenedProcessRequiresExactForegroundChild(t *testing.T) {
	for _, scenario := range []string{"reopened", "wrong parent", "wrong group", "foreign", "different cwd", "arguments", "duplicate", "bare launcher"} {
		t.Run(scenario, func(t *testing.T) {
			route := backend.OwnedLaunchRoute{LauncherPath: "/owned/launcher/fanout"}
			intent := managedConsoleIntentForPane("console", state.Pane{WorktreePath: "/repo"}, route)
			process := restartProcessInfo(route.LauncherPath, nil, "/repo")
			process.ShellPID = 5
			child := &process.ForegroundProcesses[0]
			child.ParentPID = 5
			switch scenario {
			case "wrong parent":
				child.ParentPID = 9
			case "wrong group":
				child.ProcessGroup = 9
			case "foreign":
				child.Executable = "/usr/bin/vim"
			case "different cwd":
				child.CWD = "/other"
			case "arguments":
				child.Argv = []string{"herdr", "restart"}
			case "duplicate":
				duplicate := *child
				duplicate.PID = 11
				process.ForegroundProcesses = append(process.ForegroundProcesses, duplicate)
			case "bare launcher":
				process.ShellPID = child.PID
			}
			err := classifyManagedConsoleProcess(process, intent, route, "/bin/sh")
			if (err == nil) != (scenario == "reopened") {
				t.Fatalf("classify reopened TUI = %v", err)
			}
			if scenario == "bare launcher" && !errors.Is(err, managedLaunchTransitionPending{}) {
				t.Fatalf("bare launcher = %v, want pending", err)
			}
		})
	}
}

func TestManagedConsoleRestoreRejectsUnsafeTargets(t *testing.T) {
	for _, scenario := range []string{"foreign", "extra pane", "pending restart", "old terminal", "old intent", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			root := newManagedRealizeRepo(t)
			f := newConsoleRuntimeFake(t, root)
			first, err := f.bootstrap(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			f.restore(t, first.Pane)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "foreign":
				f.processInfo = restartProcessInfo("/usr/bin/vim", nil, root)
			case "extra pane":
				f.live = []backend.LivePane{{Ref: backend.PaneRef{Workspace: "w1", Pane: "w1:p2"}}}
			case "pending restart":
				locked, journal := lockManagedRestartTest(t, root)
				intent, intentErr := newManagedServerIntent(state.IntentRestart, testManagedServerIdentity())
				if intentErr != nil {
					t.Fatal(intentErr)
				}
				journal.UpsertIntent(intent)
				if err := errors.Join(journal.Save(), locked.Unlock()); err != nil {
					t.Fatal(err)
				}
			case "old terminal":
				f.workspaces[0].TerminalID = first.Pane.TerminalID
			case "old intent":
				locked, journal := lockManagedRestartTest(t, root)
				journal.UpsertIntent(f.intent)
				if err := errors.Join(journal.Save(), locked.Unlock()); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			}
			if _, err := f.bootstrap(ctx, root); err == nil || f.tokenCalls != 1 || len(f.mutations) != 1 {
				t.Fatalf("unsafe restore = %v, tokens=%d mutations=%d", err, f.tokenCalls, len(f.mutations))
			}
		})
	}
}

func TestManagedConsoleRestoreRetryAndExpiredCapsule(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "expired"}[expired], func(t *testing.T) {
			root := newManagedRealizeRepo(t)
			f := newConsoleRuntimeFake(t, root)
			first, err := f.bootstrap(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			f.restore(t, first.Pane)
			f.wait = func(context.Context, string, string, time.Duration) error {
				return errors.New("wait failed before token")
			}
			if _, bootErr := f.bootstrap(context.Background(), root); bootErr == nil || f.tokenCalls != 1 {
				t.Fatalf("failed wait: %v tokens=%d", bootErr, f.tokenCalls)
			}
			journal, err := state.LoadLaunchJournal(root)
			if err != nil || len(journal.Intents) != 1 {
				t.Fatalf("saved unissued intent: %+v, %v", journal, err)
			}
			intent := journal.Intents[0]
			if expired {
				intent.ExpiresUnixMS = time.Now().Add(-time.Second).UnixMilli()
				locked, saved := lockManagedRestartTest(t, root)
				saved.UpsertIntent(intent)
				if saveErr := errors.Join(saved.Save(), locked.Unlock()); saveErr != nil {
					t.Fatal(saveErr)
				}
			}
			f.wait = nil
			_, err = f.bootstrap(context.Background(), root)
			if expired {
				if err == nil || f.tokenCalls != 1 {
					t.Fatalf("expired intent was replayed: %v tokens=%d", err, f.tokenCalls)
				}
			} else if err != nil || f.tokenCalls != 2 || f.intent.Launch.Nonce != intent.Launch.Nonce {
				t.Fatalf("unissued retry = %v tokens=%d", err, f.tokenCalls)
			}
		})
	}
}

func TestManagedConsoleIssuedRecoveryObservesWithoutReplay(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "restored"}[restored], func(t *testing.T) {
			root := newManagedRealizeRepo(t)
			f := newConsoleRuntimeFake(t, root)
			if restored {
				first, err := f.bootstrap(context.Background(), root)
				if err != nil {
					t.Fatal(err)
				}
				f.restore(t, first.Pane)
			}
			f.tokenErr = errors.New("token response lost")
			if _, err := f.bootstrap(context.Background(), root); err == nil {
				t.Fatal("lost token response reported success")
			}
			calls := f.tokenCalls
			locked, journal := lockManagedRestartTest(t, root)
			intent, found := journal.FindIntent(f.intent.ID)
			if !found || !intent.Launch.TokenIssued {
				t.Fatal("lost response did not preserve issued intent")
			}
			intent.ExpiresUnixMS = time.Now().Add(-time.Second).UnixMilli()
			journal.UpsertIntent(intent)
			if err := errors.Join(journal.Save(), locked.Unlock()); err != nil {
				t.Fatal(err)
			}
			if _, err := f.bootstrap(context.Background(), root); err != nil || f.tokenCalls != calls {
				t.Fatalf("observe issued workload: %v tokens=%d want=%d", err, f.tokenCalls, calls)
			}
			if _, err := f.bootstrap(context.Background(), root); err != nil || f.tokenCalls != calls {
				t.Fatalf("completed replay: %v tokens=%d", err, f.tokenCalls)
			}
		})
	}
}

func TestManagedConsoleIssuedRecoveryRequiresStartedProcessAndConsumedCapsule(t *testing.T) {
	for _, scenario := range []string{"waiting launcher", "foreign process", "unconsumed capsule"} {
		t.Run(scenario, func(t *testing.T) {
			root := newManagedRealizeRepo(t)
			f := newConsoleRuntimeFake(t, root)
			first, err := f.bootstrap(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			f.restore(t, first.Pane)
			f.tokenErr = errors.New("lost response")
			if _, err := f.bootstrap(context.Background(), root); err == nil {
				t.Fatal("lost response reported success")
			}
			switch scenario {
			case "waiting launcher":
				f.processInfo.ForegroundProcesses[0].Argv = nil
			case "foreign process":
				f.processInfo.ForegroundProcesses[0].Executable = "/usr/bin/vim"
			case "unconsumed capsule":
				if err := os.WriteFile(f.intent.Launch.EnvFilePath, []byte("unconsumed"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if _, err := f.bootstrap(ctx, root); err == nil || f.tokenCalls != 2 || len(f.mutations) != 1 {
				t.Fatalf("unsafe issued recovery: %v tokens=%d mutations=%d", err, f.tokenCalls, len(f.mutations))
			}
		})
	}
}

func TestManagedConsoleRecoversFinalizationSaveFailures(t *testing.T) {
	for _, failure := range []string{"row", "journal"} {
		t.Run(failure, func(t *testing.T) {
			root := newManagedRealizeRepo(t)
			f := newConsoleRuntimeFake(t, root)
			journalPath, err := state.LaunchJournalPath(root)
			if err != nil {
				t.Fatal(err)
			}
			f.onToken = func() {
				if failure == "row" {
					if err := os.Mkdir(state.Path(root), 0o700); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Chmod(filepath.Dir(journalPath), 0o500); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.bootstrap(context.Background(), root); err == nil || f.tokenCalls != 1 {
				t.Fatalf("save failure: %v tokens=%d", err, f.tokenCalls)
			}
			if failure == "row" {
				if err := os.Remove(state.Path(root)); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Chmod(filepath.Dir(journalPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := f.bootstrap(context.Background(), root); err != nil || f.tokenCalls != 1 || len(f.mutations) != 1 {
				t.Fatalf("recover save failure: %v tokens=%d mutations=%d", err, f.tokenCalls, len(f.mutations))
			}
		})
	}
}

func TestManagedConsoleReplacesAbsentLauncherWithoutClosingWorkspace(t *testing.T) {
	root := newManagedRealizeRepo(t)
	f := newConsoleRuntimeFake(t, root)
	first, err := f.bootstrap(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	f.restore(t, first.Pane)
	f.workspaces = nil // Herdr removes the pane/workspace when its launcher exits.
	if _, err := f.bootstrap(context.Background(), root); err != nil || f.tokenCalls != 2 || len(f.mutations) != 2 {
		t.Fatalf("absent console recovery: %v tokens=%d mutations=%d", err, f.tokenCalls, len(f.mutations))
	}
}

func TestManagedConsoleRestorePreservesUnrelatedPlan(t *testing.T) {
	root := newManagedRealizeRepo(t)
	f := newConsoleRuntimeFake(t, root)
	first, err := f.bootstrap(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	f.restore(t, first.Pane)
	other := f.workspaces[0]
	other.WorkspaceID, other.Label = "w99", "fanout-coordinator-other"
	other.Pane = backend.PaneRef{Backend: backend.Herdr, Workspace: "w99", Pane: "w99:p1"}
	other.TerminalID = "other-terminal"
	f.workspaces = append(f.workspaces, other)
	intent := f.intent
	intent.ID, err = state.CoordinatorIntentID("plan:other", f.root, 0)
	if err != nil {
		t.Fatal(err)
	}
	intent.Parent, intent.RuntimeParent, intent.WorkspaceLabel = "plan:other", "plan:other", other.Label
	intent.OwnerProjectRoot = f.root
	intent.Resource, intent.Launch = state.RuntimeResourceFromObservation(other), nil
	locked, journal := lockManagedRestartTest(t, root)
	journal.UpsertIntent(intent)
	if saveErr := errors.Join(journal.Save(), locked.Unlock()); saveErr != nil {
		t.Fatal(saveErr)
	}
	sentinel := filepath.Join(root, "unrelated-file")
	if writeErr := os.WriteFile(sentinel, []byte("keep"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, bootErr := f.bootstrap(context.Background(), root); bootErr != nil {
		t.Fatal(bootErr)
	}
	saved, err := state.LoadLaunchJournal(root)
	if err != nil || !reflect.DeepEqual(saved.Intents, []state.LaunchIntent{intent}) ||
		len(f.workspaces) != 2 || !reflect.DeepEqual(f.workspaces[1], other) {
		t.Fatalf("unrelated plan changed: %+v, %v", saved.Intents, err)
	}
	content, err := os.ReadFile(sentinel)
	if err != nil || string(content) != "keep" {
		t.Fatalf("unrelated file = %q, %v", content, err)
	}
}
