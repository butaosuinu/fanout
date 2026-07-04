package tui

import (
	"maps"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

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

// TestNewPaneFocusOrderIncludesPlanOnlyInPromptMode guarantees the checkbox is
// offered only for a non-attach prompt form, never for issue mode or attach.
func TestNewPaneFocusOrderIncludesPlanOnlyInPromptMode(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(m *model)
		wantPlan bool
	}{
		{
			name:     "prompt mode offers the plan checkbox",
			setup:    func(m *model) { m.newPane.mode = newPaneModePrompt },
			wantPlan: true,
		},
		{
			name:     "issue mode hides the plan checkbox",
			setup:    func(m *model) { m.newPane.mode = newPaneModeIssue },
			wantPlan: false,
		},
		{
			name:     "attach form hides the plan checkbox",
			setup:    func(m *model) { m.newPane.attach = &AttachTarget{TargetPath: "/repo"} },
			wantPlan: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(Options{
				ListOpenIssues: func() ([]IssueListItem, error) { return nil, nil },
			})
			m.openNewPaneForm()
			tt.setup(&m)
			got := slices.Contains(m.newPaneFocusOrder(), newPaneFieldPlan)
			if got != tt.wantPlan {
				t.Fatalf("newPaneFocusOrder() = %v, plan present = %v, want %v", m.newPaneFocusOrder(), got, tt.wantPlan)
			}
		})
	}
}

// TestNewPaneViewPlanFanoutCheckbox pins the render: the checkbox row appears
// only in a non-attach prompt form.
func TestNewPaneViewPlanFanoutCheckbox(t *testing.T) {
	const checkbox = "decompose via /fanout plan"
	tests := []struct {
		name  string
		setup func(m *model)
		want  bool
	}{
		{name: "prompt mode shows the checkbox", setup: func(*model) {}, want: true},
		{name: "issue mode hides the checkbox", setup: func(m *model) { m.newPane.mode = newPaneModeIssue }, want: false},
		{name: "attach form hides the checkbox", setup: func(m *model) { m.newPane.attach = &AttachTarget{TargetPath: "/repo"} }, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(Options{
				ListOpenIssues: func() ([]IssueListItem, error) { return nil, nil },
			})
			m.openNewPaneForm()
			tt.setup(&m)
			if got := strings.Contains(m.newPaneView(), checkbox); got != tt.want {
				t.Fatalf("newPaneView() shows plan checkbox = %v, want %v", got, tt.want)
			}
		})
	}
}
