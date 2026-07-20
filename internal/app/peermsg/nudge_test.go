package peermsg

import (
	"errors"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
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
	withHerdr := state.Store{SchemaVersion: 1, Panes: []state.Pane{{
		Parent: "68", IssueNum: 71, Backend: backend.Herdr, PaneID: "w1:p1", HerdrWorkspaceID: "w1",
	}}}
	withOpencode := state.Store{SchemaVersion: 1, Panes: []state.Pane{{Parent: "68", IssueNum: 71, PaneID: "%5", Agent: "opencode", WorktreePath: "/wt/recipient"}}}
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
			name: "herdr automatic nudge is disabled before runtime IO", req: Request{Verb: "nudge", To: 71}, store: withHerdr,
			wantCode: exitcode.OK, wantStderr: "automatic nudge is unavailable for herdr backend",
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
