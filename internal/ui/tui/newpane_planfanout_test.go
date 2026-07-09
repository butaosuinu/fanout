package tui

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// issueModeModel returns a form parked in issue mode with items already loaded
// into the picker, so tests can drive selection-dependent plan fan-out behavior
// without stepping through the mode switch and async load.
func issueModeModel(t *testing.T, items ...IssueListItem) model {
	t.Helper()
	loaded := items
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) { return loaded, nil },
	})
	m.openNewPaneForm()
	m.newPane.mode = newPaneModeIssue
	p := &m.newPane.issuePicker
	p.loaded = true
	p.items = issuePickerItems(items)
	m.recomputePicker(p)
	return m
}

// TestNewPanePlanFanoutCheckboxSubmits drives the prompt-mode plan fan-out
// checkbox: tab onto it, toggle it on, and submit with a single agent.
func TestNewPanePlanFanoutCheckboxSubmits(t *testing.T) {
	m := newModel(Options{})
	m.promptOnly = true
	m.openNewPaneForm()
	m.newPane.prompt.SetValue("Ship the search feature")

	step := func(key tea.KeyMsg) {
		updated, _ := m.Update(key)
		m = updated.(model)
	}
	step(tea.KeyMsg{Type: tea.KeyTab}) // prompt -> plan checkbox
	if m.newPane.focus != newPaneFieldPlan {
		t.Fatalf("focus after tab = %v, want plan checkbox", m.newPane.focus)
	}
	step(tea.KeyMsg{Type: tea.KeySpace}) // toggle on
	step(tea.KeyMsg{Type: tea.KeyEnter}) // submit

	if !m.promptDone || m.promptCanceled {
		t.Fatalf("promptDone = %v promptCanceled = %v, want done", m.promptDone, m.promptCanceled)
	}
	if !m.promptResult.PlanFanout {
		t.Fatal("promptResult.PlanFanout = false, want true")
	}
	if got := m.promptResult.Agents; len(got) != 1 {
		t.Fatalf("promptResult.Agents = %v, want exactly one agent", got)
	}
}

// TestNewPanePlanFanoutRequiresSingleAgent pins the submit guard: plan fan-out
// launches one coordinator, so any agent total other than one is rejected.
func TestNewPanePlanFanoutRequiresSingleAgent(t *testing.T) {
	tests := []struct {
		name   string
		counts map[string]int
	}{
		{name: "two of the same agent", counts: map[string]int{"claude": 2}},
		{name: "one of each agent", counts: map[string]int{"claude": 1, "codex": 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(Options{})
			m.promptOnly = true
			m.openNewPaneForm()
			m.newPane.prompt.SetValue("Ship it")
			m.newPane.planFanout = true
			maps.Copy(m.newPane.agentCount, tt.counts)

			cmd := m.submitNewPane()
			if cmd != nil {
				t.Fatal("submitNewPane() returned a command, want nil on validation failure")
			}
			want := "plan fan-out launches one coordinator agent; select exactly one"
			if m.newPane.err != want {
				t.Fatalf("submitNewPane() err = %q, want %q", m.newPane.err, want)
			}
			if m.newPane.launching || m.promptDone {
				t.Fatalf("submitNewPane() launching = %v promptDone = %v, want both false", m.newPane.launching, m.promptDone)
			}
		})
	}
}

// TestNewPaneFocusOrderPlanRowVisibility guarantees the plan checkbox is offered
// in a non-attach prompt form and in issue mode when the selection can
// decompose, and that issue-mode plan fan-out adds the task-agent row.
func TestNewPaneFocusOrderPlanRowVisibility(t *testing.T) {
	childless := IssueListItem{Number: 42, Title: "Fix UI"}
	parent := IssueListItem{Number: 7, Title: "Epic", HasOpenChildren: true}

	tests := []struct {
		name       string
		setup      func(t *testing.T) model
		wantPlan   bool
		wantWorker bool
	}{
		{
			name: "prompt mode offers the plan checkbox",
			setup: func(t *testing.T) model {
				t.Helper()
				m := newModel(Options{ListOpenIssues: func() ([]IssueListItem, error) { return nil, nil }})
				m.openNewPaneForm()
				return m
			},
			wantPlan: true,
		},
		{
			name: "issue mode with a childless selection offers the checkbox",
			setup: func(t *testing.T) model {
				t.Helper()
				return issueModeModel(t, childless)
			},
			wantPlan: true,
		},
		{
			name: "issue mode plan fan-out adds the task-agent row",
			setup: func(t *testing.T) model {
				t.Helper()
				m := issueModeModel(t, childless)
				m.newPane.issuePlanFanout = true
				return m
			},
			wantPlan:   true,
			wantWorker: true,
		},
		{
			name: "issue mode hides the checkbox for an issue with open children",
			setup: func(t *testing.T) model {
				t.Helper()
				return issueModeModel(t, parent)
			},
			wantPlan: false,
		},
		{
			name: "attach form hides the checkbox",
			setup: func(t *testing.T) model {
				t.Helper()
				m := newModel(Options{ListOpenIssues: func() ([]IssueListItem, error) { return nil, nil }})
				m.openNewPaneForm()
				m.newPane.attach = &AttachTarget{TargetPath: "/repo"}
				return m
			},
			wantPlan: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.setup(t)
			order := m.newPaneFocusOrder()
			if got := slices.Contains(order, newPaneFieldPlan); got != tt.wantPlan {
				t.Fatalf("newPaneFocusOrder() = %v, plan present = %v, want %v", order, got, tt.wantPlan)
			}
			if got := slices.Contains(order, newPaneFieldWorker); got != tt.wantWorker {
				t.Fatalf("newPaneFocusOrder() = %v, worker present = %v, want %v", order, got, tt.wantWorker)
			}
		})
	}
}

// TestNewPaneViewPlanFanoutCheckbox pins the render across modes: prompt shows
// the checkbox, attach hides it, and issue mode renders it — dimmed with a
// reason when the selection has open children, and with the coordinator/task
// agent rows once fan-out is on.
func TestNewPaneViewPlanFanoutCheckbox(t *testing.T) {
	const checkbox = "decompose via /fanout plan"
	childless := IssueListItem{Number: 42, Title: "Fix UI"}
	parent := IssueListItem{Number: 7, Title: "Epic", HasOpenChildren: true}

	t.Run("prompt mode shows the checkbox", func(t *testing.T) {
		m := newModel(Options{ListOpenIssues: func() ([]IssueListItem, error) { return nil, nil }})
		m.openNewPaneForm()
		if view := m.newPaneView(); !strings.Contains(view, checkbox) {
			t.Fatalf("prompt view missing checkbox:\n%s", view)
		}
	})

	t.Run("attach form hides the checkbox", func(t *testing.T) {
		m := newModel(Options{ListOpenIssues: func() ([]IssueListItem, error) { return nil, nil }})
		m.openNewPaneForm()
		m.newPane.attach = &AttachTarget{TargetPath: "/repo"}
		if view := m.newPaneView(); strings.Contains(view, checkbox) {
			t.Fatalf("attach view should hide the checkbox:\n%s", view)
		}
	})

	t.Run("issue mode off renders the plain Agent row", func(t *testing.T) {
		m := issueModeModel(t, childless)
		view := m.newPaneView()
		if !strings.Contains(view, checkbox) {
			t.Fatalf("issue view missing checkbox:\n%s", view)
		}
		if strings.Contains(view, "Coordinator agent") || strings.Contains(view, "Task agent") {
			t.Fatalf("issue view with fan-out off should show a plain Agent row:\n%s", view)
		}
	})

	t.Run("issue mode on renders coordinator and task agent rows", func(t *testing.T) {
		m := issueModeModel(t, childless)
		m.newPane.issuePlanFanout = true
		view := m.newPaneView()
		if !strings.Contains(view, "Coordinator agent") || !strings.Contains(view, "Task agent") {
			t.Fatalf("issue plan view missing coordinator/task labels:\n%s", view)
		}
	})

	t.Run("issue mode with open children dims the checkbox and names the reason", func(t *testing.T) {
		m := issueModeModel(t, parent)
		view := m.newPaneView()
		if !strings.Contains(view, checkbox+" (has open children)") {
			t.Fatalf("disabled checkbox missing reason suffix:\n%s", view)
		}
	})
}

// TestIssuePlanFanoutSyncsWithSelection drives the gray-out via the real update
// path: moving onto an open-children issue clears fan-out and disables the row,
// and moving back onto a childless issue makes it togglable again.
func TestIssuePlanFanoutSyncsWithSelection(t *testing.T) {
	childless := IssueListItem{Number: 42, Title: "Fix UI"}
	parent := IssueListItem{Number: 7, Title: "Epic", HasOpenChildren: true}
	m := issueModeModel(t, childless, parent) // index 0 childless, index 1 open-children
	m.newPane.focus = newPaneFieldMain
	m.newPane.issuePlanFanout = true

	step := func(key tea.KeyMsg) {
		updated, _ := m.Update(key)
		m = updated.(model)
	}

	step(tea.KeyMsg{Type: tea.KeyDown}) // select the open-children issue
	if m.newPane.issuePlanFanout {
		t.Fatal("plan fan-out should clear when the selection has open children")
	}
	if !m.issuePlanFanoutDisabled() {
		t.Fatal("issuePlanFanoutDisabled() = false, want true for an open-children selection")
	}

	step(tea.KeyMsg{Type: tea.KeyUp}) // back onto the childless issue
	if m.issuePlanFanoutDisabled() {
		t.Fatal("issuePlanFanoutDisabled() = true, want false back on the childless issue")
	}
}

// TestNewPaneIssuePlanFanoutSubmitSkipsAssign pins the plan-coordinator submit:
// an issue with fan-out on submits straight to the launch request, never opens
// the per-child assign step, and never enumerates children.
func TestNewPaneIssuePlanFanoutSubmitSkipsAssign(t *testing.T) {
	childrenCalls := 0
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			return []IssueListItem{{Number: 42, Title: "Fix UI"}}, nil
		},
		ListIssueChildren: func(int) ([]ChildTarget, error) {
			childrenCalls++
			return nil, nil
		},
	})
	m.promptOnly = true
	m.openNewPaneForm()
	m.newPane.mode = newPaneModeIssue
	p := &m.newPane.issuePicker
	p.loaded = true
	p.items = issuePickerItems([]IssueListItem{{Number: 42, Title: "Fix UI"}})
	m.recomputePicker(p)
	m.newPane.issuePlanFanout = true
	m.newPane.workerIndex = defaultAgentIndex("codex")

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("enter with fan-out on returned no command")
	}

	if !m.promptDone || m.promptCanceled {
		t.Fatalf("promptDone = %v promptCanceled = %v, want done", m.promptDone, m.promptCanceled)
	}
	want := LaunchRequest{
		Mode:         LaunchModeIssue,
		Issue:        42,
		PlanFanout:   true,
		DefaultAgent: "claude",
		WorkerAgent:  "codex",
	}
	if !reflect.DeepEqual(m.promptResult, want) {
		t.Fatalf("promptResult = %#v, want %#v", m.promptResult, want)
	}
	if m.newPane.step == newPaneStepAssign {
		t.Fatal("plan fan-out submit entered the assign step, want a direct submit")
	}
	if childrenCalls != 0 {
		t.Fatalf("ListIssueChildren calls = %d, want 0 (plan fan-out never enumerates children)", childrenCalls)
	}
}

// TestLaunchNewPaneRequestRoutesIssuePlan pins the dispatch split: a plan
// fan-out issue request calls LaunchIssuePlan, a plain issue request calls
// LaunchIssue.
func TestLaunchNewPaneRequestRoutesIssuePlan(t *testing.T) {
	tests := []struct {
		name          string
		planFanout    bool
		wantPlanCall  bool
		wantIssueCall bool
	}{
		{name: "plan fan-out routes to the plan launcher", planFanout: true, wantPlanCall: true},
		{name: "plain issue routes to the session launcher", planFanout: false, wantIssueCall: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var planCalled, issueCalled bool
			m := newModel(Options{
				LaunchIssue: func(int, string, map[string]string) (string, error) {
					issueCalled = true
					return "", nil
				},
				LaunchIssuePlan: func(int, string, string) (string, error) {
					planCalled = true
					return "", nil
				},
			})
			req := LaunchRequest{
				Mode:         LaunchModeIssue,
				Issue:        42,
				DefaultAgent: "claude",
				WorkerAgent:  "codex",
				PlanFanout:   tt.planFanout,
			}
			cmd := m.launchNewPaneRequest(req)
			if cmd == nil {
				t.Fatal("launchNewPaneRequest() returned nil command")
			}
			cmd() // execute the async launch to record the call
			if planCalled != tt.wantPlanCall {
				t.Fatalf("LaunchIssuePlan called = %v, want %v", planCalled, tt.wantPlanCall)
			}
			if issueCalled != tt.wantIssueCall {
				t.Fatalf("LaunchIssue called = %v, want %v", issueCalled, tt.wantIssueCall)
			}
		})
	}
}

// TestIssuePlanFanoutDoesNotLeakIntoPromptMode pins the mode-scoped toggles:
// turning issue-mode fan-out on and returning to prompt mode leaves the prompt
// checkbox off and the stashed multi-agent counts restored, so what a prompt
// submit launches never changes from peeking at issue mode.
func TestIssuePlanFanoutDoesNotLeakIntoPromptMode(t *testing.T) {
	m := newModel(Options{ListOpenIssues: func() ([]IssueListItem, error) { return nil, nil }})
	m.openNewPaneForm()
	m.newPane.agentCount["claude"] = 2
	m.newPane.agentCount["codex"] = 1

	step := func(key tea.KeyMsg) {
		updated, _ := m.Update(key)
		m = updated.(model)
	}

	m.newPane.focus = newPaneFieldMode
	step(tea.KeyMsg{Type: tea.KeyRight}) // prompt -> issue (stashes the counts)
	p := &m.newPane.issuePicker
	p.loaded = true
	p.items = issuePickerItems([]IssueListItem{{Number: 42, Title: "Fix UI"}})
	m.recomputePicker(p)
	m.newPane.focus = newPaneFieldPlan
	step(tea.KeyMsg{Type: tea.KeySpace}) // issue-mode checkbox on
	if !m.newPane.issuePlanFanout {
		t.Fatal("issue-mode checkbox did not toggle on")
	}

	m.newPane.focus = newPaneFieldMode
	step(tea.KeyMsg{Type: tea.KeyLeft}) // issue -> prompt
	if m.newPane.planFanout {
		t.Fatal("issue-mode fan-out leaked into the prompt-mode checkbox")
	}
	if m.newPane.agentCount["claude"] != 2 || m.newPane.agentCount["codex"] != 1 {
		t.Fatalf("prompt counts = %v, want claude 2 / codex 1 restored", m.newPane.agentCount)
	}
}
