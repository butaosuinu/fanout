package peermsg

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/core/naming"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestShouldNudge(t *testing.T) {
	tests := []struct {
		name  string
		state string
		want  bool
	}{
		{name: "running is nudged (granularity-unknown wrapper state)", state: "running", want: true},
		{name: "idle is nudged (agent at its prompt)", state: "idle", want: true},
		{name: "working is nudged (turns queue typed input)", state: "working", want: true},
		{name: "plan is nudged (Plan Mode composer queues input too)", state: "plan", want: true},
		{name: "padded hook value is trimmed like the display path", state: " idle ", want: true},
		{name: "blocked is a no-op (never type into a permission dialog)", state: "blocked", want: false},
		{name: "done is a no-op (bare shell)", state: "done", want: false},
		{name: "unset state is a no-op", state: "", want: false},
		{name: "unknown value is a no-op", state: "garbage", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldNudge(tt.state); got != tt.want {
				t.Errorf("shouldNudge(%q) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

func TestRunMsgNudge(t *testing.T) {
	// withPane is a legacy row (no recorded worktree) so it falls back to an
	// id-only liveness match; withWorktree carries a worktree, so the live pane
	// must also sit at/under it (the reused-%N defense).
	withPane := state.Store{SchemaVersion: 1, Panes: []state.Pane{{Parent: "68", IssueNum: 71, PaneID: "%5", Agent: "claude"}}}
	withWorktree := state.Store{SchemaVersion: 1, Panes: []state.Pane{{Parent: "68", IssueNum: 71, PaneID: "%5", Agent: "claude", WorktreePath: "/wt/recipient"}}}
	withKey := state.Store{SchemaVersion: 1, Panes: []state.Pane{{Parent: "68", IssueNum: 71, PaneID: "%5", Agent: "claude", ShellKey: "key-five", WorktreePath: "/wt/recipient"}}}
	withOpencode := state.Store{SchemaVersion: 1, Panes: []state.Pane{{Parent: "68", IssueNum: 71, PaneID: "%5", Agent: "opencode", WorktreePath: "/wt/recipient"}}}
	duplicateRecipient := state.Store{SchemaVersion: 1, Panes: []state.Pane{
		{Parent: "68", IssueNum: 71, PaneID: "%5", Agent: "claude", WorktreePath: "/wt/recipient"},
		{Parent: "0068", IssueNum: 71, PaneID: "%6", Agent: "claude", WorktreePath: "/wt/stale"},
	}}
	noPaneID := state.Store{SchemaVersion: 1, Panes: []state.Pane{{Parent: "68", IssueNum: 72, PaneID: ""}}}
	lp := func(id, path, agentState string) backend.LivePane {
		state, _ := backend.ParseAgentState(agentState)
		return backend.LivePane{
			Ref:         backend.PaneRef{Backend: backend.Tmux, Pane: id},
			CurrentPath: path,
			AgentState:  state,
		}
	}

	for _, tc := range []struct {
		name           string
		req            Request
		store          state.Store
		storeErr       error
		live           []backend.LivePane
		listErr        error
		sendErr        error
		wantCode       exitcode.Code
		wantListed     bool // ListLive consulted
		wantSendCalled bool // SendLine invoked
		wantStdout     string
		wantStderr     string
	}{
		{
			name: "running pane is nudged (legacy id-only match)", req: Request{Verb: "nudge", To: 71}, store: withPane,
			live: []backend.LivePane{lp("%5", "/anywhere", "running")}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStdout: "nudged #71",
		},
		{
			name: "running pane at the recorded worktree is nudged", req: Request{Verb: "nudge", To: 71}, store: withWorktree,
			live: []backend.LivePane{lp("%5", "/wt/recipient", "running")}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStdout: "nudged #71",
		},
		{
			name: "running pane under the recorded worktree is nudged", req: Request{Verb: "nudge", To: 71}, store: withWorktree,
			live: []backend.LivePane{lp("%5", "/wt/recipient/nested", "running")}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStdout: "nudged #71",
		},
		{
			name: "matching liveness key is nudged despite changed cwd", req: Request{Verb: "nudge", To: 71}, store: withKey,
			live: []backend.LivePane{{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%5"}, CurrentPath: "/tmp/changed", ShellKey: "key-five", AgentState: backend.AgentRunning}}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStdout: "nudged #71",
		},
		{
			name: "mismatched liveness key is not nudged on shared worktree", req: Request{Verb: "nudge", To: 71}, store: withKey,
			live: []backend.LivePane{{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%5"}, CurrentPath: "/wt/recipient", ShellKey: "other-key", AgentState: backend.AgentRunning}}, wantCode: exitcode.OK, wantListed: true, wantStderr: "gone or its id was reused",
		},
		{
			name: "plan-ready pane at the recorded worktree is nudged", req: Request{Verb: "nudge", To: 71}, store: withWorktree,
			live: []backend.LivePane{lp("%5", "/wt/recipient", "plan")}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStdout: "nudged #71",
		},
		{
			// The core Codex P2: tmux reused %5 for a pane sitting elsewhere.
			// It must NOT be nudged even though it reports "running".
			name: "reused id off the recorded worktree is not nudged", req: Request{Verb: "nudge", To: 71}, store: withWorktree,
			live: []backend.LivePane{lp("%5", "/wt/someone-else", "running")}, wantCode: exitcode.OK, wantListed: true, wantStderr: "gone or its id was reused",
		},
		{
			name: "pane absent from the live set is a no-op success", req: Request{Verb: "nudge", To: 71}, store: withWorktree,
			live: []backend.LivePane{lp("%9", "/wt/recipient", "running")}, wantCode: exitcode.OK, wantListed: true, wantStderr: "gone or its id was reused",
		},
		{
			// parent "0068" must still resolve the stored "68" pane via Find's
			// numeric canonicalization (parentMatches).
			name: "leading-zero parent still resolves the recipient", req: Request{Verb: "nudge", To: 71, Parent: "0068"}, store: withWorktree,
			live: []backend.LivePane{lp("%5", "/wt/recipient", "running")}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStdout: "nudged #71",
		},
		{
			name: "idle pane is nudged", req: Request{Verb: "nudge", To: 71}, store: withWorktree,
			live: []backend.LivePane{lp("%5", "/wt/recipient", "idle")}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStdout: "nudged #71",
		},
		{
			name: "working pane is nudged (typed input queues mid-turn)", req: Request{Verb: "nudge", To: 71}, store: withWorktree,
			live: []backend.LivePane{lp("%5", "/wt/recipient", "working")}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStdout: "nudged #71",
		},
		{
			name: "blocked pane is a no-op success (permission dialog)", req: Request{Verb: "nudge", To: 71}, store: withWorktree,
			live: []backend.LivePane{lp("%5", "/wt/recipient", "blocked")}, wantCode: exitcode.OK, wantListed: true, wantStderr: "agent is not nudgeable",
		},
		{
			// opencode never refines the wrapper's "running" state, so a nudge
			// could land while its permission dialog is focused. The pane is
			// excluded before any tmux IO; the message stays in the inbox.
			name: "opencode pane is excluded before tmux IO (no state refinement)", req: Request{Verb: "nudge", To: 71}, store: withOpencode,
			wantCode: exitcode.OK, wantStderr: "no agent-state refinement",
		},
		{
			name: "done pane is a no-op success", req: Request{Verb: "nudge", To: 71}, store: withWorktree,
			live: []backend.LivePane{lp("%5", "/wt/recipient", "done")}, wantCode: exitcode.OK, wantListed: true, wantStderr: "agent is not nudgeable",
		},
		{
			name: "unset state is a no-op success", req: Request{Verb: "nudge", To: 71}, store: withWorktree,
			live: []backend.LivePane{lp("%5", "/wt/recipient", "")}, wantCode: exitcode.OK, wantListed: true, wantStderr: "agent is not nudgeable",
		},
		{
			name: "tmux unavailable is a no-op success", req: Request{Verb: "nudge", To: 71}, store: withWorktree,
			listErr: errors.New("tmux down"), wantCode: exitcode.OK, wantListed: true, wantStderr: "tmux is unavailable",
		},
		{
			name: "send-keys failure stays a best-effort success", req: Request{Verb: "nudge", To: 71}, store: withWorktree,
			live: []backend.LivePane{lp("%5", "/wt/recipient", "running")}, sendErr: errors.New("boom"), wantCode: exitcode.OK, wantListed: true, wantSendCalled: true, wantStderr: "send-keys failed",
		},
		{
			name: "recipient absent from state is a no-op success", req: Request{Verb: "nudge", To: 99}, store: withWorktree,
			wantCode: exitcode.OK, wantStderr: "not recorded",
		},
		{
			name: "duplicate logical recipient is a no-op success", req: Request{Verb: "nudge", To: 71}, store: duplicateRecipient,
			wantCode: exitcode.OK, wantStderr: "identity is ambiguous",
		},
		{
			name: "recipient without a recorded pane is a no-op success", req: Request{Verb: "nudge", To: 72}, store: noPaneID,
			wantCode: exitcode.OK, wantStderr: "no recorded pane",
		},
		{
			name: "state load failure is an invocation error", req: Request{Verb: "nudge", To: 71}, storeErr: errors.New("bad path"),
			wantCode: exitcode.Invocation,
		},
		{
			name: "json reports a delivered nudge", req: Request{Verb: "nudge", To: 71, JSON: true}, store: withWorktree,
			live: []backend.LivePane{lp("%5", "/wt/recipient", "running")}, wantCode: exitcode.OK, wantListed: true, wantSendCalled: true,
			wantStdout: `"nudged": true`,
		},
		{
			name: "json reports a skipped nudge with a reason", req: Request{Verb: "nudge", To: 71, JSON: true}, store: withWorktree,
			live: []backend.LivePane{lp("%5", "/wt/recipient", "done")}, wantCode: exitcode.OK, wantListed: true, wantStdout: `"nudged": false`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listed := false
			sent := false
			var sentPane backend.PaneRef
			var sentText string
			deps := Deps{
				LoadState: func() (state.Store, error) { return tc.store, tc.storeErr },
				ListLive: func() ([]backend.LivePane, error) {
					listed = true
					return tc.live, tc.listErr
				},
				SendLine: func(ref backend.PaneRef, text string) error {
					sent = true
					sentPane, sentText = ref, text
					return tc.sendErr
				},
			}

			var out, errb strings.Builder
			lg := log.NewWith(&out, &errb, false)
			// resolveMsgIdentity supplies parent in the real flow; mirror that
			// here, defaulting to "68" so existing cases need no parent field.
			parent := tc.req.Parent
			if parent == "" {
				parent = "68"
			}
			code := runMsgNudge(&tc.req, parent, deps, lg)
			if code != tc.wantCode {
				t.Fatalf("runMsgNudge() code = %d, want %d (stderr: %q)", code, tc.wantCode, errb.String())
			}
			if tc.wantCode != exitcode.OK {
				return
			}
			if listed != tc.wantListed {
				t.Errorf("ListLive consulted = %v, want %v", listed, tc.wantListed)
			}
			if sent != tc.wantSendCalled {
				t.Errorf("SendLine called = %v, want %v", sent, tc.wantSendCalled)
			}
			if tc.wantSendCalled {
				if sentPane != (backend.PaneRef{Backend: backend.Tmux, Pane: "%5"}) {
					t.Errorf("send pane = %+v, want tmux %%5", sentPane)
				}
				if sentText != nudgeText {
					t.Errorf("send text = %q, want nudgeText", sentText)
				}
			}
			if tc.wantStdout != "" && !strings.Contains(out.String(), tc.wantStdout) {
				t.Errorf("stdout = %q, want substring %q", out.String(), tc.wantStdout)
			}
			if tc.wantStderr != "" && !strings.Contains(errb.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", errb.String(), tc.wantStderr)
			}
		})
	}
}

type fakeHerdrNudger struct {
	panes      []backend.LivePane
	panesErr   error
	beforeLive func()
	process    herdrrun.PaneProcessInfo
	processErr error
	nudgeErr   error
	nudged     bool
	nudgeCalls int
	target     herdrrun.NudgeTarget
	text       string
}

func (f *fakeHerdrNudger) LivePanes(context.Context) ([]backend.LivePane, error) {
	if f.beforeLive != nil {
		f.beforeLive()
	}
	return f.panes, f.panesErr
}

func (f *fakeHerdrNudger) ProcessInfo(context.Context, string) (herdrrun.PaneProcessInfo, error) {
	return f.process, f.processErr
}

func (f *fakeHerdrNudger) Nudge(_ context.Context, target herdrrun.NudgeTarget, text string) error {
	f.nudged, f.target, f.text = true, target, text
	f.nudgeCalls++
	return f.nudgeErr
}

func TestRunMsgNudgeHerdrUsesTheTmuxStateAllowlist(t *testing.T) {
	for _, test := range []struct {
		state string
		want  bool
	}{
		{state: "running", want: true},
		{state: "working", want: true},
		{state: "plan", want: true},
		{state: "idle", want: true},
		{state: "blocked"},
		{state: "done"},
		{state: ""},
	} {
		t.Run("state_"+test.state, func(t *testing.T) {
			store, runtime := herdrNudgeFixture(test.state, true)
			opened := false
			deps := herdrNudgeDeps(store, store, runtime)
			deps.OpenHerdr = func(context.Context) (HerdrNudger, error) {
				opened = true
				return runtime, nil
			}
			var out, errb strings.Builder
			code := runMsgNudge(&Request{Verb: "nudge", To: 71}, "68", deps, log.NewWith(&out, &errb, false))
			if code != exitcode.OK || runtime.nudged != test.want {
				t.Fatalf("state %q: code=%d nudged=%v stderr=%q", test.state, code, runtime.nudged, errb.String())
			}
			if opened != test.want {
				t.Errorf("state %q: runtime opened=%v, want %v", test.state, opened, test.want)
			}
			if test.want && (runtime.text != nudgeText || runtime.target.Ref.Pane != "w1:p1") {
				t.Errorf("nudge = target %+v text %q", runtime.target, runtime.text)
			}
		})
	}
}

func TestRunMsgNudgeHerdrAllowsUnreportedAgentSession(t *testing.T) {
	store, runtime := herdrNudgeFixture("idle", true)
	store.Panes[0].HerdrAgentSession = nil
	runtime.panes[0].AgentSession = nil
	deps := herdrNudgeDeps(store, store, runtime)
	var out, errb strings.Builder
	code := runMsgNudge(&Request{Verb: "nudge", To: 71}, "68", deps, log.NewWith(&out, &errb, false))
	if code != exitcode.OK || runtime.nudgeCalls != 1 || errb.Len() != 0 {
		t.Fatalf("code=%d calls=%d stderr=%q", code, runtime.nudgeCalls, errb.String())
	}
}

func TestRunMsgNudgeHerdrRejectsInvalidLaunchGenerationBeforeRuntimeIO(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*state.Pane)
	}{
		{name: "agent name", mutate: func(p *state.Pane) { p.HerdrAgentID = "fanout-corrupt" }},
		{name: "launch nonce", mutate: func(p *state.Pane) {
			p.LaunchNonce = "invalid"
			p.HerdrAgentID = naming.HerdrAgentName(p.HerdrRepoKey, p.EmitterRowKey, p.LaunchNonce)
		}},
		{name: "emitter nonce", mutate: func(p *state.Pane) { p.EmitterNonce = "invalid" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, runtime := herdrNudgeFixture("working", true)
			test.mutate(&store.Panes[0])
			runtime.panes[0].AgentID = store.Panes[0].HerdrAgentID
			runtime.beforeLive = func() { t.Fatal("invalid generation reached runtime IO") }
			deps := herdrNudgeDeps(store, store, runtime)
			var out, errb strings.Builder
			code := runMsgNudge(&Request{Verb: "nudge", To: 71}, "68", deps, log.NewWith(&out, &errb, false))
			if code != exitcode.OK || runtime.nudgeCalls != 0 || !strings.Contains(errb.String(), "binding changed") {
				t.Fatalf("code=%d calls=%d stderr=%q", code, runtime.nudgeCalls, errb.String())
			}
		})
	}
}

func TestRunMsgNudgeHerdrRequiresFreshRefinedState(t *testing.T) {
	store, runtime := herdrNudgeFixture("running", false)
	deps := herdrNudgeDeps(store, store, runtime)
	deps.OpenHerdr = func(context.Context) (HerdrNudger, error) {
		t.Fatal("unrefined state opened the Herdr runtime")
		return nil, nil
	}
	var out, errb strings.Builder
	code := runMsgNudge(&Request{Verb: "nudge", To: 71}, "68", deps, log.NewWith(&out, &errb, false))
	if code != exitcode.OK || runtime.nudged || !strings.Contains(errb.String(), "not refined") {
		t.Fatalf("unrefined nudge: code=%d nudged=%v stderr=%q", code, runtime.nudged, errb.String())
	}
}

func TestRunMsgNudgeHerdrFailsClosedBeforePrompt(t *testing.T) {
	for _, test := range []struct {
		name        string
		mutateLive  func(*backend.LivePane)
		mutateProc  func(*herdrrun.PaneProcessInfo)
		mutateRow   func(*state.Pane)
		mutateStore func(*state.Store)
		lockErr     error
		want        string
	}{
		{name: "worktree changed", mutateLive: func(p *backend.LivePane) { p.WorktreePath = "/repo/other" }, want: "provenance changed"},
		{name: "terminal changed", mutateLive: func(p *backend.LivePane) { p.TerminalID = "term-new" }, want: "identity or worktree"},
		{name: "process changed", mutateProc: func(p *herdrrun.PaneProcessInfo) { p.ForegroundProcesses[0].Argv = []string{"other"} }, want: "process identity"},
		{name: "emitter generation changed", mutateRow: func(p *state.Pane) { p.EmitterNonce = strings.Repeat("c", 32) }, want: "launch binding changed"},
		{name: "recipient duplicated", mutateStore: appendDuplicateNudgeRecipient, want: "launch binding changed"},
		{name: "latest state blocked", mutateRow: func(p *state.Pane) { p.ReportedState = "blocked" }, want: "not nudgeable"},
		{name: "state lock failed", lockErr: errors.New("lock failed"), want: "lock failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			initial, runtime := herdrNudgeFixture("working", true)
			locked := cloneNudgeStore(initial)
			if test.mutateLive != nil {
				test.mutateLive(&runtime.panes[0])
			}
			if test.mutateProc != nil {
				test.mutateProc(&runtime.process)
			}
			if test.mutateRow != nil {
				test.mutateRow(&locked.Panes[0])
			}
			if test.mutateStore != nil {
				test.mutateStore(&locked)
			}
			deps := herdrNudgeDeps(initial, locked, runtime)
			if test.lockErr != nil {
				deps.ReadLockedState = func(context.Context, func(state.Store) error) error { return test.lockErr }
			}
			var out, errb strings.Builder
			code := runMsgNudge(&Request{Verb: "nudge", To: 71}, "68", deps, log.NewWith(&out, &errb, false))
			if code != exitcode.OK || runtime.nudged || !strings.Contains(errb.String(), test.want) {
				t.Fatalf("code=%d nudged=%v stderr=%q, want %q", code, runtime.nudged, errb.String(), test.want)
			}
		})
	}
}

func TestRecheckHerdrNudgeStateHonorsContext(t *testing.T) {
	store, runtime := herdrNudgeFixture("working", true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	deps := herdrNudgeDeps(store, store, runtime)
	deps.ReadLockedState = func(ctx context.Context, _ func(state.Store) error) error {
		<-ctx.Done()
		return ctx.Err()
	}
	if _, _, err := recheckHerdrNudgeState(ctx, store.Panes[0], deps); !errors.Is(err, context.Canceled) {
		t.Fatalf("recheckHerdrNudgeState() error = %v, want context canceled", err)
	}
}

func TestRunMsgNudgeHerdrRechecksRuntimeAfterStateLockWait(t *testing.T) {
	store, runtime := herdrNudgeFixture("working", true)
	deps := herdrNudgeDeps(store, store, runtime)
	deps.ReadLockedState = func(_ context.Context, read func(state.Store) error) error {
		runtime.panes[0].WorktreePath = "/repo/reused"
		return read(store)
	}
	var out, errb strings.Builder
	code := runMsgNudge(&Request{Verb: "nudge", To: 71}, "68", deps, log.NewWith(&out, &errb, false))
	if code != exitcode.OK || runtime.nudged || !strings.Contains(errb.String(), "provenance changed") {
		t.Fatalf("code=%d nudged=%v stderr=%q", code, runtime.nudged, errb.String())
	}
}

func TestRunMsgNudgeHerdrRechecksStateAfterRuntimeVerification(t *testing.T) {
	initial, runtime := herdrNudgeFixture("working", true)
	locked := cloneNudgeStore(initial)
	runtime.beforeLive = func() { locked.Panes[0].ReportedState = "blocked" }
	deps := herdrNudgeDeps(initial, locked, runtime)
	var out, errb strings.Builder
	code := runMsgNudge(&Request{Verb: "nudge", To: 71}, "68", deps, log.NewWith(&out, &errb, false))
	if code != exitcode.OK || runtime.nudgeCalls != 0 || !strings.Contains(errb.String(), "not nudgeable") {
		t.Fatalf("code=%d calls=%d stderr=%q", code, runtime.nudgeCalls, errb.String())
	}
}

func appendDuplicateNudgeRecipient(store *state.Store) {
	duplicate := store.Panes[0]
	duplicate.PaneID = "w1:p2"
	duplicate.HerdrTerminalID = "term-duplicate"
	duplicate.EmitterRowKey = "issue:68:71:duplicate"
	store.Panes = append(store.Panes, duplicate)
}

func TestUniqueNudgeRecipientRejectsDuplicatePlanTask(t *testing.T) {
	store := state.Store{SchemaVersion: 1, Panes: []state.Pane{
		{Parent: "plan:demo", TaskID: "task-a", PaneID: "w1:p1"},
		{Parent: "plan:demo", TaskID: "task-a", PaneID: "w1:p2"},
	}}
	if _, matches := uniqueNudgeRecipient(store, "plan:demo", 0, "task-a"); matches != 2 {
		t.Fatalf("uniqueNudgeRecipient() matches = %d, want 2", matches)
	}
}

func TestRunMsgNudgeHerdrDoesNotRetryAnAmbiguousPromptFailure(t *testing.T) {
	store, runtime := herdrNudgeFixture("idle", true)
	runtime.nudgeErr = errors.New("response lost")
	deps := herdrNudgeDeps(store, store, runtime)
	var out, errb strings.Builder
	code := runMsgNudge(&Request{Verb: "nudge", To: 71}, "68", deps, log.NewWith(&out, &errb, false))
	if code != exitcode.OK || runtime.nudgeCalls != 1 || !strings.Contains(errb.String(), "agent prompt failed") {
		t.Fatalf("code=%d calls=%d stderr=%q", code, runtime.nudgeCalls, errb.String())
	}
}

func herdrNudgeFixture(reportedState string, refined bool) (state.Store, *fakeHerdrNudger) {
	worktree := "/repo/.fanout/worktrees/child"
	args := []string{"--permission-mode", "auto", "prompt"}
	repoKey := "/repo/.git"
	rowKey := "issue:68:71"
	launchNonce := strings.Repeat("a", 32)
	session := &backend.AgentSessionRef{Source: "herdr:claude", Agent: "claude", Kind: "id", Value: "session-71"}
	pane := state.Pane{
		Parent: "68", IssueNum: 71, Backend: backend.Herdr, PaneID: "w1:p1", Agent: "claude",
		HerdrWorkspaceID: "w1", HerdrTerminalID: "term-71", HerdrRepoKey: repoKey,
		HerdrAgentID: naming.HerdrAgentName(repoKey, rowKey, launchNonce), HerdrAgentSession: session,
		HerdrSession: "fanout-owned", HerdrSocketPath: "/tmp/fanout-owned/herdr.sock",
		WorktreePath: worktree, ReportedState: reportedState, StateRefinement: refined,
		EmitterRowKey: rowKey, LaunchNonce: launchNonce,
		EmitterNonce: strings.Repeat("b", 32), HerdrLaunchExecutable: "/usr/bin/claude",
		HerdrLaunchArgs: args,
	}
	live := backend.LivePane{
		Ref: paneRef(pane), TerminalID: pane.HerdrTerminalID, SessionID: pane.HerdrSession,
		SocketPath: pane.HerdrSocketPath, RepoKey: pane.HerdrRepoKey,
		ProjectRoot: "/repo", WorktreePath: worktree, CurrentPath: worktree, AgentPresent: true,
		AgentProvider: pane.Agent, AgentID: pane.HerdrAgentID, AgentSession: session,
	}
	process := herdrrun.PaneProcessInfo{
		PaneID: pane.PaneID, ShellPID: 101, ForegroundProcessGroup: 101,
		ForegroundProcesses: []herdrrun.PaneProcess{{
			PID: 101, ParentPID: 1, ProcessGroup: 101, Executable: "/usr/bin/claude",
			Argv0: "/usr/bin/claude", Argv: args, CWD: worktree,
		}},
	}
	return state.Store{SchemaVersion: 1, Panes: []state.Pane{pane}}, &fakeHerdrNudger{panes: []backend.LivePane{live}, process: process}
}

func herdrNudgeDeps(initial, locked state.Store, runtime *fakeHerdrNudger) Deps {
	return Deps{
		LoadState:       func() (state.Store, error) { return initial, nil },
		OpenHerdr:       func(context.Context) (HerdrNudger, error) { return runtime, nil },
		ReadLockedState: func(_ context.Context, read func(state.Store) error) error { return read(locked) },
	}
}

func cloneNudgeStore(store state.Store) state.Store {
	clone := store
	clone.Panes = append([]state.Pane(nil), store.Panes...)
	clone.Panes[0].HerdrLaunchArgs = append([]string(nil), store.Panes[0].HerdrLaunchArgs...)
	return clone
}

func TestMatchLivePane(t *testing.T) {
	panes := []backend.LivePane{
		{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%5"}, CurrentPath: "/wt/recipient", AgentState: backend.AgentRunning, ShellKey: "key-five"},
		{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%6"}, CurrentPath: "/wt/recipient/nested/deep", AgentState: backend.AgentDone},
		{Ref: backend.PaneRef{Backend: backend.Tmux, Pane: "%7"}, CurrentPath: "/wt/other", AgentState: backend.AgentRunning},
	}
	for _, tc := range []struct {
		name     string
		paneID   string
		worktree string
		key      string
		wantOK   bool
		wantID   string // matched pane id when wantOK
	}{
		{name: "id + exact worktree", paneID: "%5", worktree: "/wt/recipient", wantOK: true, wantID: "%5"},
		{name: "id + path under worktree", paneID: "%6", worktree: "/wt/recipient", wantOK: true, wantID: "%6"},
		{name: "trailing slash on worktree still matches", paneID: "%5", worktree: "/wt/recipient/", wantOK: true, wantID: "%5"},
		{name: "reused id off the worktree is rejected", paneID: "%7", worktree: "/wt/recipient", wantOK: false},
		{name: "sibling-prefix path is not under the worktree", paneID: "%5", worktree: "/wt/recip", wantOK: false},
		{name: "id absent from the live set", paneID: "%9", worktree: "/wt/recipient", wantOK: false},
		{name: "empty worktree falls back to id-only", paneID: "%7", worktree: "", wantOK: true, wantID: "%7"},
		{name: "matching liveness key wins over changed cwd", paneID: "%5", worktree: "/wt/other", key: "key-five", wantOK: true, wantID: "%5"},
		{name: "mismatched liveness key rejects shared worktree", paneID: "%5", worktree: "/wt/recipient", key: "other-key", wantOK: false},
		{name: "empty pane id never matches", paneID: "", worktree: "/wt/recipient", wantOK: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := backend.PaneRef{Backend: backend.Tmux, Pane: tc.paneID}
			got, ok := matchLivePane(panes, ref, tc.worktree, tc.key)
			if ok != tc.wantOK {
				t.Fatalf("matchLivePane(%q, %q, %q) ok = %v, want %v", tc.paneID, tc.worktree, tc.key, ok, tc.wantOK)
			}
			if ok && got.Ref.Pane != tc.wantID {
				t.Errorf("matched pane id = %q, want %q", got.Ref.Pane, tc.wantID)
			}
		})
	}
}

func TestMatchLivePaneUsesBackendNativeReference(t *testing.T) {
	panes := []backend.LivePane{{
		Ref:         backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
		CurrentPath: "/wt/recipient",
	}}
	for _, tc := range []struct {
		name string
		ref  backend.PaneRef
		want bool
	}{
		{name: "exact herdr reference", ref: backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"}, want: true},
		{name: "backend mismatch", ref: backend.PaneRef{Backend: backend.Tmux, Pane: "w1:p1"}},
		{name: "workspace mismatch", ref: backend.PaneRef{Backend: backend.Herdr, Workspace: "w2", Pane: "w1:p1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := matchLivePane(panes, tc.ref, "/wt/recipient", "")
			if ok != tc.want {
				t.Fatalf("matchLivePane(%+v) = %v, want %v", tc.ref, ok, tc.want)
			}
		})
	}
}
