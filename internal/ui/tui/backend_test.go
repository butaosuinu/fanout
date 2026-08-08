package tui

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/lifecycle"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

func TestViewAlwaysShowsBackendSelectionAndReason(t *testing.T) {
	for _, width := range []int{120, 40} {
		m := newModel(Options{
			ProjectRoot:      "/repo",
			BackendSelection: backend.Selection{Name: backend.Herdr, Reason: backend.ReasonHerdrContext},
		})
		m.width = width
		m.height = 24
		m.allPanes = []paneView{{IssueNum: 1, Backend: backend.Herdr, PaneID: "w1:p1", TmuxState: "live"}}
		m.resize()
		if got := m.View(); !strings.Contains(got, "backend: herdr (HERDR_ENV)") {
			t.Fatalf("View(width=%d) missing backend selection:\n%s", width, got)
		}
	}
}

func TestHelpDimsHerdrActionsAndShowsSharedReason(t *testing.T) {
	m := newModel(Options{
		ProjectRoot:      "/repo",
		BackendSelection: backend.Selection{Name: backend.Herdr, Reason: backend.ReasonHerdrContext},
	})
	m.width = 80
	m.allPanes = []paneView{{IssueNum: 1, Backend: backend.Herdr, PaneID: "w1:p1", TmuxState: "live"}}
	m.refreshRows()

	got := m.helpView()
	if !strings.Contains(got, "New agent pane") || !strings.Contains(got, "[disabled]") {
		t.Fatalf("helpView() missing disabled launch row:\n%s", got)
	}
	if !strings.Contains(got, "disabled: "+backend.HerdrObservationOnlyReason) {
		t.Fatalf("helpView() missing shared herdr reason:\n%s", got)
	}
}

func TestHelpUsesInlineViewForSelectedHerdrRow(t *testing.T) {
	popupCalls := 0
	m := newModel(Options{
		ProjectRoot:      "/repo",
		BackendSelection: backend.Selection{Name: backend.Tmux},
		HelpPopup: func() error {
			popupCalls++
			return nil
		},
	})
	m.allPanes = []paneView{{IssueNum: 1, Backend: backend.Herdr, PaneID: "w1:p1", TmuxState: "live"}}
	m.refreshRows()

	if cmd := m.openHelpPopupCmd(); cmd != nil {
		t.Fatal("openHelpPopupCmd() returned a command, want inline help")
	}
	if popupCalls != 0 {
		t.Fatalf("external popup called %d time(s), want 0", popupCalls)
	}
	if m.mode != modeHelp {
		t.Fatalf("mode = %v, want modeHelp", m.mode)
	}
}

func TestFilterPaneViewsBackendPredicateNormalizesLegacyTmux(t *testing.T) {
	panes := []paneView{
		{IssueNum: 1, Backend: ""},
		{IssueNum: 2, Backend: backend.Herdr},
	}
	if got := filterPaneViews(panes, "backend:tmux"); len(got) != 1 || got[0].IssueNum != 1 {
		t.Fatalf("backend:tmux = %+v, want legacy tmux row", got)
	}
	if got := filterPaneViews(panes, "backend:herdr"); len(got) != 1 || got[0].IssueNum != 2 {
		t.Fatalf("backend:herdr = %+v, want herdr row", got)
	}
}

func TestSyntheticPaneDoesNotInventTmuxBackend(t *testing.T) {
	panes := applyIssueStatuses("/repo", nil, map[issueKey]issueStatus{
		{Parent: "100", Num: 2}: {Title: "queued child", State: "OPEN"},
	})
	if len(panes) != 1 {
		t.Fatalf("applyIssueStatuses() returned %d rows, want 1", len(panes))
	}
	pane := panes[0]
	if !pane.NotStarted {
		t.Fatal("synthetic row NotStarted = false, want true")
	}
	if got := pane.backendLabel(); got != "" {
		t.Fatalf("backendLabel() = %q, want empty for synthetic row", got)
	}
	if got := pane.tableRow()[columnIndex(t, "BACKEND")]; got != "-" {
		t.Fatalf("BACKEND cell = %q, want - for synthetic row", got)
	}
	if got := filterPaneViews([]paneView{pane}, "backend:tmux"); len(got) != 0 {
		t.Fatalf("backend:tmux matched synthetic row: %+v", got)
	}
	m := newModel(Options{})
	m.allPanes = panes
	m.refreshRows()
	if got := m.detailContent(); !strings.Contains(got, "backend=- pane=-") {
		t.Fatalf("detailContent() invented runtime ownership:\n%s", got)
	}
}

func TestHerdrRowRuntimeActionsAreDisabledBeforePorts(t *testing.T) {
	tests := []struct {
		name string
		key  string
	}{
		{name: "focus", key: "enter"},
		{name: "peek", key: "p"},
		{name: "attach", key: "a"},
		{name: "worktree terminal", key: "A"},
		{name: "close", key: "c"},
		{name: "cleanup", key: "X"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			m := newModel(Options{
				ProjectRoot:      "/repo",
				BackendSelection: backend.Selection{Name: backend.Tmux},
				FocusPane:        func(string) error { calls++; return nil },
				PaneAlive:        func(string) bool { calls++; return true },
				CapturePaneOutput: func(string, int) (string, error) {
					calls++
					return "", nil
				},
				LaunchAttach: func(AttachLaunchRequest) (string, error) { calls++; return "", nil },
				LaunchShell:  func(ShellLaunchRequest) error { calls++; return nil },
				CloseChoicePopup: func(CloseChoiceRequest) (lifecycle.CloseMode, bool, error) {
					calls++
					return lifecycle.ClosePaneOnly, false, nil
				},
			})
			m.allPanes = []paneView{
				{IssueNum: 1, Backend: backend.Herdr, PaneID: "w1:p1", TmuxState: "live", WorktreePath: "/repo/wt"},
			}
			m.refreshRows()

			updated, cmd := m.Update(keyRunes(tt.key))
			m = updated.(model)
			if cmd != nil {
				t.Fatalf("key %q returned a command, want disabled no-op", tt.key)
			}
			if calls != 0 {
				t.Fatalf("key %q called runtime ports %d time(s)", tt.key, calls)
			}
			message := strings.Join([]string{m.notice, m.actionMessage, m.peek.Err}, " ")
			wantReason := backend.HerdrObservationOnlyReason
			if !strings.Contains(message, wantReason) {
				t.Fatalf("key %q reason = %q, want explicit herdr action reason", tt.key, message)
			}
		})
	}
}

func TestOwnedHerdrPaneFocusAndPeekUsePersistedIdentityPorts(t *testing.T) {
	saved := state.Pane{
		Backend: backend.Herdr, PaneID: "p1", HerdrWorkspaceID: "w1",
		HerdrWorkspaceLabel: "owned-label", HerdrTerminalID: "t1",
	}
	focused, captured := false, false
	m := newModel(Options{
		BackendSelection: backend.Selection{Name: backend.Herdr},
		HerdrActionDisabled: func(got state.Pane) string {
			if got.PaneID != saved.PaneID {
				return "wrong identity"
			}
			return ""
		},
		FocusHerdrPane: func(got state.Pane) error {
			focused = got.PaneID == saved.PaneID
			return nil
		},
		CaptureHerdrPane: func(got state.Pane, lines int) (string, error) {
			captured = got.PaneID == saved.PaneID && lines == peekLines
			return "owned output", nil
		},
	})
	m.allPanes = []paneView{
		{Backend: backend.Herdr, PaneID: "p1", TmuxState: "live", savedPane: saved},
	}
	m.refreshRows()
	if cmd := m.focusSelectedCmd(); cmd == nil {
		t.Fatal("focusSelectedCmd() = nil, want owned Herdr focus")
	} else {
		_ = cmd()
	}
	if cmd := m.peekSelectedCmd(true); cmd == nil {
		t.Fatal("peekSelectedCmd() = nil, want owned Herdr read")
	} else {
		_ = cmd()
	}
	if !focused || !captured {
		t.Fatalf("owned ports called = focus:%t peek:%t, want both true", focused, captured)
	}
}

func TestOwnedHerdrPaneLifecycleStaysDisabled(t *testing.T) {
	saved := state.Pane{Backend: backend.Herdr, PaneID: "p1", HerdrWorkspaceID: "w1"}
	m := newModel(Options{
		BackendSelection: backend.Selection{Name: backend.Herdr},
		HerdrActionDisabled: func(state.Pane) string {
			return ""
		},
	})
	m.allPanes = []paneView{{
		Backend: backend.Herdr, PaneID: "p1", TmuxState: "live", savedPane: saved,
	}}
	m.refreshRows()

	for _, action := range []lifecycleAction{actionClose, actionMerge, actionCleanup} {
		updated, cmd := m.startPendingAction(action)
		m = updated.(model)
		if cmd != nil || m.pendingAction != nil {
			t.Fatalf("%s enabled an owned Herdr lifecycle action", action)
		}
		if !strings.Contains(m.actionMessage, backend.HerdrObservationOnlyReason) {
			t.Fatalf("%s reason = %q, want explicit deferred lifecycle reason", action, m.actionMessage)
		}
	}
}

func TestAutomaticHerdrFocusReloadsPersistedPaneIdentity(t *testing.T) {
	root := t.TempDir()
	session := backend.AgentSessionRef{Source: "herdr:codex", Agent: "codex", Kind: "id", Value: "thread-1"}
	row := state.Pane{
		Parent: "524", IssueNum: 530, Backend: backend.Herdr, PaneID: "w1:p1",
		Agent: "codex", HerdrAgentID: "agent-1", HerdrAgentSession: &session,
		HerdrWorkspaceID: "w1", HerdrWorkspaceLabel: "owned-label", HerdrTerminalID: "term-1",
		HerdrRepoKey: "/repo/.git", HerdrSession: "owned-session", HerdrSocketPath: "/tmp/owned.sock",
		WorktreePath: root + "/child",
	}
	locked, err := state.LockProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = locked.RecordPane(row); err != nil {
		t.Fatal(err)
	}
	if err = locked.Unlock(); err != nil {
		t.Fatal(err)
	}
	live := backend.LivePane{
		Ref:            backend.PaneRef{Backend: backend.Herdr, Workspace: "w1", Pane: "w1:p1"},
		WorkspaceLabel: "owned-label", TerminalID: "term-1", AgentID: "agent-1",
		AgentProvider: "codex", AgentSession: &session, AgentPresent: true,
		RepoKey: "/repo/.git", ProjectRoot: root, WorktreePath: root + "/child",
		SessionID: "owned-session", SocketPath: "/tmp/owned.sock",
	}
	var focused state.Pane
	m := newModel(Options{
		ProjectRoot: root, BackendSelection: backend.Selection{Name: backend.Herdr},
		ListLive:            func() ([]backend.LivePane, error) { return []backend.LivePane{live}, nil },
		HerdrActionDisabled: func(state.Pane) string { return "" },
		FocusHerdrPane:      func(pane state.Pane) error { focused = pane; return nil },
		FocusPane:           func(string) error { t.Fatal("automatic Herdr focus routed through tmux"); return nil },
	})
	msg := m.focusPaneIDCmd("w1:p1", "launched")()
	focusedMsg, ok := msg.(paneFocusedMsg)
	if !ok || focusedMsg.err != nil || focused.HerdrWorkspaceLabel != "owned-label" || focused.HerdrTerminalID != "term-1" {
		t.Fatalf("automatic Herdr focus = msg:%#v pane:%+v", msg, focused)
	}
}

func TestHerdrConsoleLaunchesAreDisabledBeforePorts(t *testing.T) {
	launchCalls := 0
	m := newModel(Options{
		ProjectRoot:      "/repo",
		BackendSelection: backend.Selection{Name: backend.Herdr, Reason: backend.ReasonHerdrContext},
		NewPanePrompt: func(NewPanePromptRequest) (LaunchRequest, bool, error) {
			launchCalls++
			return LaunchRequest{}, false, nil
		},
		LaunchPane: func(LaunchRequest) (LaunchResult, error) {
			launchCalls++
			return LaunchResult{}, nil
		},
		LaunchIssue: func(int, string, map[string]string) (LaunchResult, error) {
			launchCalls++
			return LaunchResult{}, nil
		},
		LaunchIssuePlan: func(int, string, string) (LaunchResult, error) {
			launchCalls++
			return LaunchResult{}, nil
		},
		LaunchShell: func(ShellLaunchRequest) error { launchCalls++; return nil },
	})
	m.allPanes = []paneView{{IssueNum: 1, Backend: backend.Tmux, PaneID: "%1", TmuxState: "live"}}
	m.refreshRows()

	for _, key := range []string{"n", "t"} {
		updated, cmd := m.Update(keyRunes(key))
		m = updated.(model)
		if cmd != nil {
			t.Fatalf("key %q returned a command, want disabled no-op", key)
		}
		if !strings.Contains(m.notice, backend.HerdrObservationOnlyReason) {
			t.Fatalf("key %q notice = %q, want herdr reason", key, m.notice)
		}
	}

	requests := []LaunchRequest{
		{Prompt: "prompt", Agents: []string{"codex"}},
		{Mode: LaunchModeIssue, Issue: 42, DefaultAgent: "codex"},
		{Mode: LaunchModeIssue, Issue: 42, PlanFanout: true, DefaultAgent: "codex", WorkerAgent: "codex"},
	}
	for _, req := range requests {
		if cmd := m.launchNewPaneRequest(req); cmd != nil {
			t.Fatalf("launchNewPaneRequest(%+v) returned a command", req)
		}
	}
	if launchCalls != 0 {
		t.Fatalf("launch ports called %d time(s), want 0", launchCalls)
	}
}
