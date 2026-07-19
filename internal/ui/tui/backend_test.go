package tui

import (
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/lifecycle"
	"github.com/butaosuinu/fanout/internal/core/backend"
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
			if !strings.Contains(message, "herdr backend v1") {
				t.Fatalf("key %q reason = %q, want explicit herdr v1 reason", tt.key, message)
			}
		})
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
