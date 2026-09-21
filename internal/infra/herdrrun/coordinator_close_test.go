package herdrrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"testing"
	"time"

	corebackend "github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func coordinatorCloseFixture(t *testing.T) (*ownedHarness, state.LaunchIntent, *corebackend.PaneProcessInfo) {
	t.Helper()
	h := newOwnedHarness(t)
	target := genericWorkspaceCloseTarget(h)
	h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
		for i := range *snapshot.Panes {
			if (*snapshot.Panes)[i].WorkspaceID == target.Ref.Workspace {
				(*snapshot.Panes)[i].AgentSession = nil
			}
		}
		*snapshot.Agents = []agentJSON{}
	})
	intent := state.LaunchIntent{
		Kind: state.IntentCoordinator, Status: state.IntentRealized, Parent: "plan:alpha", RuntimeParent: "plan:alpha",
		WorkspaceLabel: target.WorkspaceLabel, WorktreePath: target.CurrentPath, Session: target.SessionID, SocketPath: target.SocketPath,
		Resource: state.RuntimeResource{WorkspaceID: target.Ref.Workspace, PaneID: target.Ref.Pane, TerminalID: target.TerminalID, Label: target.WorkspaceLabel, CurrentPath: target.CurrentPath},
	}
	info := &corebackend.PaneProcessInfo{
		PaneID: target.Ref.Pane, ShellPID: 42, ForegroundProcessGroup: 42,
		ForegroundProcesses: []corebackend.PaneProcess{{PID: 42, Argv0: h.session.LauncherPath, Argv: []string{h.session.LauncherPath}, CWD: target.CurrentPath}},
	}
	h.session.processInspector = func(_ context.Context, processes []corebackend.PaneProcess) ([]corebackend.PaneProcess, error) {
		for i := range processes {
			processes[i].Executable = h.session.LauncherPath
			processes[i].ProcessGroup = 42
		}
		return processes, nil
	}
	h.fake.respond = func(args []string) ([]byte, error) {
		if slices.Equal(args, []string{"pane", "process-info", "--pane", target.Ref.Pane}) {
			return json.Marshal(paneProcessInfoEnvelope{ID: "cli:pane:process_info", Result: &paneProcessInfoResult{Type: "pane_process_info", ProcessInfo: *info}})
		}
		if slices.Equal(args, []string{"pane", "send-keys", target.Ref.Pane, "ctrl+d"}) {
			h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
				*snapshot.Workspaces = slices.DeleteFunc(*snapshot.Workspaces, func(w workspaceJSON) bool { return w.WorkspaceID == target.Ref.Workspace })
				*snapshot.Panes = slices.DeleteFunc(*snapshot.Panes, func(p paneJSON) bool { return p.WorkspaceID == target.Ref.Workspace })
			})
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected coordinator command: %v", args)
	}
	return h, intent, info
}

func bindCoordinatorCloser(t *testing.T, h *ownedHarness, intent state.LaunchIntent) (*coordinatorCloser, corebackend.CloseRequest) {
	t.Helper()
	bound, err := h.session.BindOwnedCoordinatorClose(intent)
	if err != nil {
		t.Fatal(err)
	}
	return bound.(*coordinatorCloser), corebackend.CloseRequest{Ref: corebackend.PaneRef{Backend: corebackend.Herdr, Pane: intent.Resource.PaneID}}
}

func TestBindOwnedCoordinatorCloseRejectsNonTokenlessIntent(t *testing.T) {
	for _, change := range []string{"workload", "wrong kind", "pending", "checkout", "resource"} {
		t.Run(change, func(t *testing.T) {
			h, intent, _ := coordinatorCloseFixture(t)
			switch change {
			case "workload":
				intent.Launch = &state.LaunchCapsule{}
			case "wrong kind":
				intent.Kind = state.IntentWorktree
			case "pending":
				intent.Status = state.IntentManualCleanupRequired
			case "checkout":
				intent.Resource.RepoKey = h.commonDir
			case "resource":
				intent.WorktreePath += "-other"
			}
			_, err := h.session.BindOwnedCoordinatorClose(intent)
			if !errors.Is(err, corebackend.ErrOwnedIdentityMismatch) {
				t.Fatalf("bind=%v", err)
			}
			assertCoordinatorEOFCount(t, h, 0)
		})
	}
}

func TestCoordinatorEOFClosesOnlyTarget(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		t.Run(fmt.Sprint(metadata), func(t *testing.T) {
			h, intent, _ := coordinatorCloseFixture(t)
			if metadata {
				h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
					for i := range *snapshot.Workspaces {
						(*snapshot.Workspaces)[i].Worktree = &worktreeInfoJSON{RepoKey: h.commonDir, RepoRoot: intent.WorktreePath, CheckoutPath: intent.WorktreePath}
					}
				})
			}
			bound, req := bindCoordinatorCloser(t, h, intent)
			result, err := bound.CloseOwned(req)
			if err != nil || result.Status != corebackend.CloseConfirmed {
				t.Fatalf("close=%+v, %v", result, err)
			}
			var snapshot snapshotEnvelope
			if err := json.Unmarshal([]byte(h.fake.snapshot), &snapshot); err != nil {
				t.Fatal(err)
			}
			if len(*snapshot.Result.Snapshot.Workspaces) != 1 || (*snapshot.Result.Snapshot.Workspaces)[0].WorkspaceID != "w1" {
				t.Fatalf("unrelated workspace was removed: %s", h.fake.snapshot)
			}
			assertCoordinatorEOFCount(t, h, 1)
		})
	}
}

func TestCoordinatorEOFRejectsUnsafePreflight(t *testing.T) {
	for _, change := range []string{"foreign argv", "replaced executable", "extra process", "wrong cwd", "wrong group", "wrong shell", "process observation", "extra pane", "agent", "empty agent", "terminal", "label", "linked root", "snapshot", "canceled"} {
		t.Run(change, func(t *testing.T) {
			h, intent, info := coordinatorCloseFixture(t)
			bound, req := bindCoordinatorCloser(t, h, intent)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch change {
			case "foreign argv":
				info.ForegroundProcesses[0].Argv = append(info.ForegroundProcesses[0].Argv, "foreign")
			case "replaced executable":
				h.session.processInspector = func(_ context.Context, processes []corebackend.PaneProcess) ([]corebackend.PaneProcess, error) {
					processes[0].ProcessGroup = 42
					processes[0].Executable = "/bin/sh"
					return processes, nil
				}
			case "extra process":
				info.ForegroundProcesses = append(info.ForegroundProcesses, info.ForegroundProcesses[0])
				info.ForegroundProcesses[1].PID = 43
			case "wrong cwd":
				info.ForegroundProcesses[0].CWD = "/foreign"
			case "wrong group":
				info.ForegroundProcessGroup++
			case "wrong shell":
				info.ShellPID++
			case "process observation":
				h.fake.respond = func([]string) ([]byte, error) { return nil, errors.New("process info unavailable") }
			case "snapshot":
				h.fake.errors["snapshot"] = errors.New("snapshot unavailable")
			case "canceled":
				cancel()
			default:
				// Simulate changes during the process inspection, before the final snapshot.
				inspect := h.session.processInspector
				h.session.processInspector = func(ctx context.Context, processes []corebackend.PaneProcess) ([]corebackend.PaneProcess, error) {
					h.fake.snapshot = mutateSnapshot(h.fake.snapshot, func(snapshot *snapshotJSON) {
						switch change {
						case "extra pane":
							extra := (*snapshot.Panes)[1]
							extra.PaneID += "-extra"
							extra.TerminalID += "-extra"
							*snapshot.Panes = append(*snapshot.Panes, extra)
						case "agent", "empty agent":
							pane := (*snapshot.Panes)[1]
							agent := agentJSON{
								PaneID: pane.PaneID, WorkspaceID: pane.WorkspaceID, TerminalID: pane.TerminalID,
								TabID: pane.TabID, Focused: pane.Focused, Revision: pane.Revision, AgentStatus: pane.AgentStatus,
							}
							if change == "agent" {
								agent.Name, agent.Agent = new("foreign"), new("codex")
							}
							*snapshot.Agents = append(*snapshot.Agents, agent)
						case "terminal":
							(*snapshot.Panes)[1].TerminalID += "-replaced"
						case "label":
							(*snapshot.Workspaces)[1].Label += "-replaced"
						case "linked root":
							(*snapshot.Workspaces)[1].Worktree = &worktreeInfoJSON{RepoKey: h.commonDir, RepoRoot: intent.WorktreePath, CheckoutPath: intent.WorktreePath, IsLinked: true}
						}
					})
					return inspect(ctx, processes)
				}
			}
			_, err := bound.close(ctx, req)
			if !errors.Is(err, corebackend.ErrOwnedMutationNotIssued) {
				t.Fatalf("close=%v; want definitely unissued", err)
			}
			assertCoordinatorEOFCount(t, h, 0)
		})
	}
}

func TestCoordinatorEOFDispatchClassification(t *testing.T) {
	for _, failure := range []string{"start", "response loss", "post snapshot", "cancel after dispatch", "delayed exit"} {
		t.Run(failure, func(t *testing.T) {
			h, intent, _ := coordinatorCloseFixture(t)
			bound, req := bindCoordinatorCloser(t, h, intent)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			respond := h.fake.respond
			h.fake.respond = func(args []string) ([]byte, error) {
				if len(args) < 2 || args[1] != "send-keys" {
					return respond(args)
				}
				switch failure {
				case "start":
					return nil, &exec.Error{Name: "herdr", Err: exec.ErrNotFound}
				case "response loss":
					return nil, errors.New("lost response")
				case "post snapshot":
					out, err := respond(args)
					h.fake.errors["snapshot"] = errors.New("verification failed")
					return out, err
				case "cancel after dispatch":
					cancel()
					return nil, ctx.Err()
				case "delayed exit":
					bound.sleep = func(context.Context, time.Duration) error { _, err := respond(args); return err }
					return nil, nil
				}
				return nil, nil
			}
			result, err := bound.close(ctx, req)
			if failure == "delayed exit" {
				if err != nil || result.Status != corebackend.CloseConfirmed {
					t.Fatalf("delayed exit=%+v, %v", result, err)
				}
			} else if err == nil || errors.Is(err, corebackend.ErrOwnedMutationNotIssued) != (failure == "start") {
				t.Fatalf("dispatch classification=%v", err)
			}
			assertCoordinatorEOFCount(t, h, 1)
		})
	}
}

func assertCoordinatorEOFCount(t *testing.T, h *ownedHarness, want int) {
	t.Helper()
	count := 0
	for _, command := range h.fake.commands {
		if len(command.args) < 2 {
			continue
		}
		if command.args[1] == "close" {
			t.Fatalf("unsafe close: %v", command.args)
		}
		if command.args[1] == "send-keys" {
			count++
		}
	}
	if count != want {
		t.Fatalf("EOF calls=%d want=%d", count, want)
	}
}
