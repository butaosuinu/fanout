package tui

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRankPickerItems(t *testing.T) {
	items := []pickerItem{
		{key: "#41", title: "Add API client", labels: []string{"backend"}},
		{key: "#42", title: "Fix UI overflow", labels: []string{"frontend", "bug"}},
		{key: "#420", title: "Retro notes"},
	}

	tests := []struct {
		name  string
		query string
		want  []int
	}{
		{name: "empty query keeps source order", query: "", want: []int{0, 1, 2}},
		{name: "number prefix outranks title match", query: "42", want: []int{1, 2}},
		{name: "leading hash is ignored", query: "#41", want: []int{0}},
		{name: "title substring matches case-insensitively", query: "ui over", want: []int{1}},
		{name: "label substring ranks last", query: "backend", want: []int{0}},
		{name: "no match yields empty results", query: "zzz", want: []int{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rankPickerItems(items, tt.query)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("rankPickerItems(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}

// TestNewPaneModeSwitchLoadsListOnce guarantees the issue list loads on first
// entry, retries after a failure, and is not re-fetched once loaded.
func TestNewPaneModeSwitchLoadsListOnce(t *testing.T) {
	calls := 0
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			calls++
			if calls == 1 {
				return nil, errors.New("gh boom")
			}
			return []IssueListItem{{Number: 42, Title: "Fix UI"}}, nil
		},
	})
	m.openNewPaneForm()
	m.newPane.focus = newPaneFieldMode

	step := func(key tea.KeyMsg) tea.Cmd {
		updated, cmd := m.Update(key)
		m = updated.(model)
		return cmd
	}
	deliver := func(cmd tea.Cmd) {
		t.Helper()
		if cmd == nil {
			t.Fatal("expected a load command")
		}
		updated, _ := m.Update(cmd())
		m = updated.(model)
	}

	// prompt -> issue: first load fails and must leave the mode retriable.
	deliver(step(tea.KeyMsg{Type: tea.KeyRight}))
	if m.newPane.issuePicker.err != "gh boom" || m.newPane.issuePicker.loaded {
		t.Fatalf("after failed load: err = %q loaded = %v, want gh boom / false", m.newPane.issuePicker.err, m.newPane.issuePicker.loaded)
	}

	// issue -> prompt -> issue: re-entering retries the fetch.
	if cmd := step(tea.KeyMsg{Type: tea.KeyRight}); cmd != nil {
		t.Fatal("switching to prompt mode issued a load command")
	}
	deliver(step(tea.KeyMsg{Type: tea.KeyRight}))
	if !m.newPane.issuePicker.loaded || m.newPane.issuePicker.err != "" {
		t.Fatalf("after retry: loaded = %v err = %q, want true / empty", m.newPane.issuePicker.loaded, m.newPane.issuePicker.err)
	}

	// A loaded list is served from cache on re-entry.
	step(tea.KeyMsg{Type: tea.KeyRight})
	if cmd := step(tea.KeyMsg{Type: tea.KeyRight}); cmd != nil {
		t.Fatal("re-entering a loaded mode issued a load command")
	}
	if calls != 2 {
		t.Fatalf("ListOpenIssues calls = %d, want 2", calls)
	}
}

// TestNewPaneIssuePickerToAssignFlow drives the whole promptOnly wizard:
// filter the issue list, open the assignment step, flip one child to codex,
// and submit a diff-only override map.
func TestNewPaneIssuePickerToAssignFlow(t *testing.T) {
	m := newModel(Options{
		DefaultAgent: "claude",
		ListOpenIssues: func() ([]IssueListItem, error) {
			return []IssueListItem{
				{Number: 41, Title: "Add API client"},
				{Number: 42, Title: "Fix UI overflow"},
			}, nil
		},
		ListIssueChildren: func(parent int) ([]ChildTarget, error) {
			if parent != 42 {
				return nil, errors.New("unexpected parent")
			}
			return []ChildTarget{
				{Number: 43, Title: "Frontend", Wave: "1"},
				{Number: 44, Title: "Backend", Wave: "1"},
			}, nil
		},
	})
	m.promptOnly = true
	m.openNewPaneForm()
	m.newPane.focus = newPaneFieldMode

	step := func(key tea.KeyMsg) tea.Cmd {
		updated, cmd := m.Update(key)
		m = updated.(model)
		return cmd
	}
	deliver := func(cmd tea.Cmd) {
		t.Helper()
		if cmd == nil {
			t.Fatal("expected a command")
		}
		updated, _ := m.Update(cmd())
		m = updated.(model)
	}

	deliver(step(tea.KeyMsg{Type: tea.KeyRight})) // switch to issue mode + load
	step(tea.KeyMsg{Type: tea.KeyTab})            // focus the picker
	step(keyRunes("42"))                          // narrow to #42
	if len(m.newPane.issuePicker.results) != 1 {
		t.Fatalf("filtered results = %v, want one", m.newPane.issuePicker.results)
	}

	deliver(step(tea.KeyMsg{Type: tea.KeyEnter})) // open assign + load children
	if m.newPane.step != newPaneStepAssign {
		t.Fatalf("step = %v, want assign", m.newPane.step)
	}
	if len(m.newPane.assign.rows) != 2 {
		t.Fatalf("assign rows = %#v, want two children", m.newPane.assign.rows)
	}

	step(tea.KeyMsg{Type: tea.KeyDown})  // select #44
	step(tea.KeyMsg{Type: tea.KeyRight}) // claude -> codex
	step(tea.KeyMsg{Type: tea.KeyEnter}) // submit

	if !m.promptDone || m.promptCanceled {
		t.Fatalf("promptDone = %v promptCanceled = %v, want done", m.promptDone, m.promptCanceled)
	}
	want := LaunchRequest{
		Mode:           LaunchModeIssue,
		Issue:          42,
		DefaultAgent:   "claude",
		AgentOverrides: map[string]string{"44": "codex"},
	}
	if !reflect.DeepEqual(m.promptResult, want) {
		t.Fatalf("promptResult = %#v, want %#v", m.promptResult, want)
	}
}

// TestNewPaneIssueWithoutChildrenSkipsAssign guarantees a childless issue
// submits straight from the picker as a single-pane session.
func TestNewPaneIssueWithoutChildrenSkipsAssign(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			return []IssueListItem{{Number: 7, Title: "Standalone"}}, nil
		},
		ListIssueChildren: func(int) ([]ChildTarget, error) { return nil, nil },
	})
	m.promptOnly = true
	m.openNewPaneForm()
	m.newPane.focus = newPaneFieldMode

	step := func(key tea.KeyMsg) tea.Cmd {
		updated, cmd := m.Update(key)
		m = updated.(model)
		return cmd
	}

	loadCmd := step(tea.KeyMsg{Type: tea.KeyRight})
	updated, _ := m.Update(loadCmd())
	m = updated.(model)
	assignCmd := step(tea.KeyMsg{Type: tea.KeyEnter})
	updated, quitCmd := m.Update(assignCmd())
	m = updated.(model)

	if quitCmd == nil {
		t.Fatal("empty child list did not finalize the submission")
	}
	if !m.promptDone {
		t.Fatal("promptDone = false, want direct submit without assign step")
	}
	want := LaunchRequest{Mode: LaunchModeIssue, Issue: 7, DefaultAgent: "claude"}
	if !reflect.DeepEqual(m.promptResult, want) {
		t.Fatalf("promptResult = %#v, want %#v", m.promptResult, want)
	}
}

func TestNewPanePlanAssignSubmit(t *testing.T) {
	m := newModel(Options{
		ListPlanSlugs: func() ([]string, error) { return []string{"alpha", "beta"}, nil },
		ListPlanTasks: func(slug string) ([]PlanTaskItem, error) {
			if slug != "beta" {
				return nil, errors.New("unexpected slug")
			}
			return []PlanTaskItem{
				{ID: "front", Title: "Frontend", Wave: "1"},
				{ID: "back", Title: "Backend", Wave: "2"},
			}, nil
		},
	})
	m.promptOnly = true
	m.openNewPaneForm()
	m.newPane.focus = newPaneFieldMode

	step := func(key tea.KeyMsg) tea.Cmd {
		updated, cmd := m.Update(key)
		m = updated.(model)
		return cmd
	}
	deliver := func(cmd tea.Cmd) {
		t.Helper()
		if cmd == nil {
			t.Fatal("expected a command")
		}
		updated, _ := m.Update(cmd())
		m = updated.(model)
	}

	deliver(step(tea.KeyMsg{Type: tea.KeyRight})) // prompt -> plan (issue mode not wired)
	step(tea.KeyMsg{Type: tea.KeyTab})
	step(keyRunes("bet"))
	deliver(step(tea.KeyMsg{Type: tea.KeyEnter}))
	step(tea.KeyMsg{Type: tea.KeyRight}) // first task claude -> codex
	step(tea.KeyMsg{Type: tea.KeyEnter})

	want := LaunchRequest{
		Mode:           LaunchModePlan,
		Plan:           "beta",
		DefaultAgent:   "claude",
		AgentOverrides: map[string]string{"front": "codex"},
	}
	if !reflect.DeepEqual(m.promptResult, want) {
		t.Fatalf("promptResult = %#v, want %#v", m.promptResult, want)
	}
}

func TestNewPaneAssignEscReturnsToPicker(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			return []IssueListItem{{Number: 42, Title: "Fix UI"}}, nil
		},
		ListIssueChildren: func(int) ([]ChildTarget, error) {
			return []ChildTarget{{Number: 43, Title: "Child"}}, nil
		},
	})
	m.promptOnly = true
	m.openNewPaneForm()
	m.newPane.focus = newPaneFieldMode

	step := func(key tea.KeyMsg) tea.Cmd {
		updated, cmd := m.Update(key)
		m = updated.(model)
		return cmd
	}

	loadCmd := step(tea.KeyMsg{Type: tea.KeyRight})
	updated, _ := m.Update(loadCmd())
	m = updated.(model)
	assignCmd := step(tea.KeyMsg{Type: tea.KeyEnter})
	updated, _ = m.Update(assignCmd())
	m = updated.(model)
	if m.newPane.step != newPaneStepAssign {
		t.Fatalf("step = %v, want assign", m.newPane.step)
	}

	step(tea.KeyMsg{Type: tea.KeyEsc})
	if m.newPane.step != newPaneStepForm {
		t.Fatalf("step after esc = %v, want form", m.newPane.step)
	}
	if m.promptDone || m.promptCanceled {
		t.Fatal("esc from assign must return to the picker, not close the prompt")
	}
	if !m.newPane.issuePicker.loaded {
		t.Fatal("picker state was lost when returning from assign")
	}
}

func TestNewPaneIssueSubmitWhileLoadingSetsError(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) { return nil, nil },
	})
	m.openNewPaneForm()
	m.newPane.mode = newPaneModeIssue
	m.newPane.issuePicker.loading = true

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(model)
	if cmd != nil {
		t.Fatal("enter while loading returned a command")
	}
	if m.newPane.err != "list is still loading" {
		t.Fatalf("err = %q, want list is still loading", m.newPane.err)
	}
}

// TestLaunchRequestDispatchesByMode guarantees the popup-result dispatch
// routes each mode to its launcher and reports unwired launchers as notices.
func TestLaunchRequestDispatchesByMode(t *testing.T) {
	type call struct {
		kind      string
		target    string
		agent     string
		overrides map[string]string
	}

	tests := []struct {
		name       string
		req        LaunchRequest
		wired      bool
		wantCall   *call
		wantNotice string
	}{
		{
			name:     "issue mode calls LaunchIssue",
			req:      LaunchRequest{Mode: LaunchModeIssue, Issue: 42, DefaultAgent: "claude", AgentOverrides: map[string]string{"43": "codex"}},
			wired:    true,
			wantCall: &call{kind: "issue", target: "42", agent: "claude", overrides: map[string]string{"43": "codex"}},
		},
		{
			name:     "plan mode calls LaunchPlan",
			req:      LaunchRequest{Mode: LaunchModePlan, Plan: "beta", DefaultAgent: "codex"},
			wired:    true,
			wantCall: &call{kind: "plan", target: "beta", agent: "codex"},
		},
		{
			name:     "prompt mode keeps calling LaunchPane",
			req:      LaunchRequest{Prompt: "do it", Agents: []string{"claude"}},
			wired:    true,
			wantCall: &call{kind: "prompt", target: "do it", agent: "claude"},
		},
		{
			name:       "unwired issue launcher surfaces a notice",
			req:        LaunchRequest{Mode: LaunchModeIssue, Issue: 42, DefaultAgent: "claude"},
			wantNotice: "new session: issue launcher is not configured",
		},
		{
			name:       "unwired plan launcher surfaces a notice",
			req:        LaunchRequest{Mode: LaunchModePlan, Plan: "beta", DefaultAgent: "claude"},
			wantNotice: "new session: plan launcher is not configured",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got *call
			opts := Options{}
			if tt.wired {
				opts.LaunchIssue = func(num int, agent string, overrides map[string]string) (string, error) {
					got = &call{kind: "issue", target: strconv.Itoa(num), agent: agent, overrides: overrides}
					return "", nil
				}
				opts.LaunchPlan = func(slug, agent string, overrides map[string]string) (string, error) {
					got = &call{kind: "plan", target: slug, agent: agent, overrides: overrides}
					return "", nil
				}
				opts.LaunchPane = func(req LaunchRequest) (string, error) {
					got = &call{kind: "prompt", target: req.Prompt, agent: req.Agents[0]}
					return "", nil
				}
			}
			m := newModel(opts)

			cmd := m.launchNewPaneRequest(tt.req)
			if tt.wantNotice != "" {
				if cmd != nil {
					t.Fatal("unwired launcher returned a command")
				}
				if m.notice != tt.wantNotice {
					t.Fatalf("notice = %q, want %q", m.notice, tt.wantNotice)
				}
				return
			}
			if cmd == nil {
				t.Fatal("wired launcher returned nil command")
			}
			if msg, ok := cmd().(launchPaneMsg); !ok || msg.err != nil {
				t.Fatalf("cmd() = %#v, want launchPaneMsg without error", msg)
			}
			if !reflect.DeepEqual(got, tt.wantCall) {
				t.Fatalf("launcher call = %#v, want %#v", got, tt.wantCall)
			}
		})
	}
}

func TestSingleAgentSelectorCycles(t *testing.T) {
	m := newModel(Options{})
	m.openNewPaneForm()

	m.cycleNewPaneAgentChoice("right")
	if got := launchAgents[m.newPane.agentChoice]; got != "codex" {
		t.Fatalf("after right agent = %q, want codex", got)
	}
	m.cycleNewPaneAgentChoice("right")
	if got := launchAgents[m.newPane.agentChoice]; got != "claude" {
		t.Fatalf("after wrap agent = %q, want claude", got)
	}
	m.cycleNewPaneAgentChoice("left")
	if got := launchAgents[m.newPane.agentChoice]; got != "codex" {
		t.Fatalf("after left agent = %q, want codex", got)
	}
}

func TestNewPaneViewRendersPickerStates(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) { return nil, nil },
		ListPlanSlugs:  func() ([]string, error) { return nil, nil },
	})
	m.openNewPaneForm()
	m.newPane.mode = newPaneModeIssue

	m.newPane.issuePicker.loading = true
	if view := m.newPaneView(); !strings.Contains(view, "loading…") {
		t.Fatalf("loading view missing spinner text:\n%s", view)
	}

	m.newPane.issuePicker.loading = false
	m.newPane.issuePicker.err = "gh boom"
	if view := m.newPaneView(); !strings.Contains(view, "list failed: gh boom") {
		t.Fatalf("error view missing failure text:\n%s", view)
	}

	m.newPane.issuePicker.err = ""
	m.newPane.issuePicker.loaded = true
	m.newPane.issuePicker.items = issuePickerItems([]IssueListItem{
		{Number: 42, Title: "Fix UI", HasSession: true},
	})
	m.recomputePicker(&m.newPane.issuePicker)
	view := m.newPaneView()
	for _, want := range []string{"#42 Fix UI", "(has session)", "[Issue]"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker view missing %q:\n%s", want, view)
		}
	}
}

// TestNewPaneAssignDropsStaleSameTargetLoad pins the esc-while-loading race:
// re-entering the same issue must ignore the first, superseded load even
// though both carry the same target.
func TestNewPaneAssignDropsStaleSameTargetLoad(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			return []IssueListItem{{Number: 42, Title: "Fix UI"}}, nil
		},
		ListIssueChildren: func(int) ([]ChildTarget, error) {
			return []ChildTarget{{Number: 43, Title: "Child"}}, nil
		},
	})
	m.promptOnly = true
	m.openNewPaneForm()
	m.newPane.focus = newPaneFieldMode

	step := func(key tea.KeyMsg) tea.Cmd {
		updated, cmd := m.Update(key)
		m = updated.(model)
		return cmd
	}

	loadCmd := step(tea.KeyMsg{Type: tea.KeyRight})
	updated, _ := m.Update(loadCmd())
	m = updated.(model)

	staleCmd := step(tea.KeyMsg{Type: tea.KeyEnter}) // first attempt: load in flight
	staleMsg := staleCmd()
	step(tea.KeyMsg{Type: tea.KeyEsc})               // back to the picker mid-load
	freshCmd := step(tea.KeyMsg{Type: tea.KeyEnter}) // second attempt, same target
	updated, finalize := m.Update(staleMsg)          // stale load resolves late
	m = updated.(model)
	if finalize != nil || len(m.newPane.assign.rows) != 0 || !m.newPane.assign.loading {
		t.Fatalf("stale load accepted: rows = %#v loading = %v", m.newPane.assign.rows, m.newPane.assign.loading)
	}

	updated, _ = m.Update(freshCmd())
	m = updated.(model)
	if len(m.newPane.assign.rows) != 1 {
		t.Fatalf("fresh load rows = %#v, want one child", m.newPane.assign.rows)
	}
}

// TestNewPanePickerFilterAcceptsSpaces pins multi-word title queries: a lone
// space arrives as tea.KeySpace (not KeyRunes) and must extend the filter.
func TestNewPanePickerFilterAcceptsSpaces(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) {
			return []IssueListItem{
				{Number: 41, Title: "Add API client"},
				{Number: 42, Title: "Fix UI overflow"},
			}, nil
		},
	})
	m.openNewPaneForm()
	m.newPane.mode = newPaneModeIssue

	updated, _ := m.Update(newPaneIssuesLoadedMsg{items: []IssueListItem{
		{Number: 41, Title: "Add API client"},
		{Number: 42, Title: "Fix UI overflow"},
	}})
	m = updated.(model)
	for _, key := range []tea.KeyMsg{keyRunes("ui"), {Type: tea.KeySpace}, keyRunes("over")} {
		updated, _ = m.Update(key)
		m = updated.(model)
	}

	if got := m.newPane.issuePicker.query; got != "ui over" {
		t.Fatalf("filter query = %q, want %q", got, "ui over")
	}
	if results := m.newPane.issuePicker.results; len(results) != 1 || m.newPane.issuePicker.items[results[0]].number != 42 {
		t.Fatalf("filtered results = %v, want only #42", results)
	}
}

// TestLaunchingGateQueuesQuit pins the fan-out escape hatch: while a launch
// runs, q queues a quit instead of being silently swallowed.
func TestLaunchingGateQueuesQuit(t *testing.T) {
	m := newModel(Options{})
	m.newPane.launching = true

	updated, cmd := m.Update(keyRunes("q"))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("q during launch returned a command, want deferred quit")
	}
	if !m.quitAfterLaunch {
		t.Fatal("quitAfterLaunch = false, want queued quit")
	}

	updated, cmd = m.Update(launchPaneMsg{notice: "done", count: 1})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("launchPaneMsg with queued quit returned nil command, want quit")
	}
	if msg := cmd(); msg != (tea.QuitMsg{}) {
		t.Fatalf("cmd() = %#v, want tea.QuitMsg", msg)
	}
}

// TestAssignRowWindowFollowsSelection pins the sliding window: a long target
// list never renders taller than the popup and the cursor stays visible.
func TestAssignRowWindowFollowsSelection(t *testing.T) {
	m := newModel(Options{})
	m.openNewPaneForm()
	m.height = 18 // minimum popup pty: 18-8 overhead = 10 -> capped at 8
	rows := make([]assignRow, 15)
	for i := range rows {
		rows[i] = assignRow{target: strconv.Itoa(i), label: "row"}
	}
	m.newPane.assign.rows = rows

	if start, end := m.assignRowWindow(); start != 0 || end != 8 {
		t.Fatalf("window at top = [%d,%d), want [0,8)", start, end)
	}
	m.newPane.assign.index = 12
	start, end := m.assignRowWindow()
	if m.newPane.assign.index < start || m.newPane.assign.index >= end {
		t.Fatalf("window [%d,%d) does not contain selected row %d", start, end, m.newPane.assign.index)
	}

	m.newPane.mode = newPaneModeIssue
	m.newPane.step = newPaneStepAssign
	view := m.newPaneAssignView()
	if !strings.Contains(view, "more") {
		t.Fatalf("windowed assign view missing scroll marker:\n%s", view)
	}
}

// TestPickerRowWindowFollowsSelection pins the picker's sliding window: an
// uncapped result list never renders taller than the popup, the cursor stays
// visible, and every match (including the last) is reachable without filtering.
func TestPickerRowWindowFollowsSelection(t *testing.T) {
	m := newModel(Options{
		ListOpenIssues: func() ([]IssueListItem, error) { return nil, nil },
	})
	m.openNewPaneForm()
	m.newPane.mode = newPaneModeIssue
	m.height = 24 // 24-16 overhead = 8 visible rows

	items := make([]IssueListItem, 15)
	for i := range items {
		items[i] = IssueListItem{Number: i + 1, Title: "row"}
	}
	p := &m.newPane.issuePicker
	p.loaded = true
	p.items = issuePickerItems(items)
	m.recomputePicker(p)

	if len(p.results) != 15 {
		t.Fatalf("uncapped results = %d, want all 15", len(p.results))
	}
	if start, end := m.pickerRowWindow(*p); start != 0 || end != 8 {
		t.Fatalf("window at top = [%d,%d), want [0,8)", start, end)
	}

	p.index = 12
	start, end := m.pickerRowWindow(*p)
	if p.index < start || p.index >= end {
		t.Fatalf("window [%d,%d) does not contain selected row %d", start, end, p.index)
	}
	if view := m.newPaneView(); !strings.Contains(view, "more") {
		t.Fatalf("windowed picker view missing scroll marker:\n%s", view)
	}

	// The last row stays reachable with no filter applied.
	p.index = 14
	start, end = m.pickerRowWindow(*p)
	if p.index < start || p.index >= end {
		t.Fatalf("window [%d,%d) does not contain last row %d", start, end, p.index)
	}
}

// with no list providers wired the form keeps its classic prompt-only shape.
func TestNewPaneViewHidesModeRowWithoutProviders(t *testing.T) {
	m := newModel(Options{})
	m.openNewPaneForm()

	if view := m.newPaneView(); strings.Contains(view, "Mode") {
		t.Fatalf("mode row rendered without providers:\n%s", view)
	}
	if order := m.newPaneFocusOrder(); len(order) != 2 {
		t.Fatalf("focus order = %v, want two fields", order)
	}
}
