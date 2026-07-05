package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/butaosuinu/fanout/internal/state"
)

// Pins the compactWidthAt breakpoint (80) and both override directions of the
// v cycle: compact below 80 in auto, table forced by full, switcher forced by
// compact even on wide terminals.
func TestCompactLayoutThreshold(t *testing.T) {
	tests := []struct {
		name        string
		width       int
		override    viewOverride
		wantCompact bool
		wantSidebar bool
		wantStrip   bool
	}{
		{name: "width 79 auto switches to compact", width: 79, wantCompact: true},
		{name: "width 80 auto keeps the top strip", width: 80, wantStrip: true},
		{name: "width 119 auto keeps the top strip", width: 119, wantStrip: true},
		{name: "width 120 auto uses the sidebar", width: 120, wantSidebar: true},
		{name: "override compact wins on a wide terminal", width: 150, override: overrideCompact, wantCompact: true},
		{name: "override full forces the table when narrow", width: 40, override: overrideFull, wantStrip: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(Options{})
			m.width = tt.width
			m.height = 30
			m.viewOverride = tt.override

			layout := m.monitorLayout()

			if layout.Compact != tt.wantCompact {
				t.Fatalf("monitorLayout().Compact = %v, want %v", layout.Compact, tt.wantCompact)
			}
			if layout.Sidebar != tt.wantSidebar {
				t.Fatalf("monitorLayout().Sidebar = %v, want %v", layout.Sidebar, tt.wantSidebar)
			}
			if got := layout.TopStripHeight > 0; got != tt.wantStrip {
				t.Fatalf("monitorLayout().TopStripHeight = %d, want strip %v", layout.TopStripHeight, tt.wantStrip)
			}
			if tt.wantCompact {
				if want := max(m.height-4, 4); layout.TableRows != want {
					t.Fatalf("compact TableRows = %d, want %d", layout.TableRows, want)
				}
			}
		})
	}
}

// Pins the v key's three-state cycle auto → compact → full → auto and that it
// returns no command.
func TestViewOverrideCycleKey(t *testing.T) {
	m := newModel(Options{})
	m.width = 100
	m.height = 30
	m.resize()

	// The steps are order-dependent (each press mutates the model), so they
	// run in sequence with names instead of independent subtests.
	steps := []struct {
		name         string
		wantOverride viewOverride
		wantCompact  bool
		wantNotice   string
	}{
		{name: "first press forces compact", wantOverride: overrideCompact, wantCompact: true, wantNotice: "view=compact"},
		{name: "second press forces full", wantOverride: overrideFull, wantCompact: false, wantNotice: "view=full"},
		{name: "third press wraps to auto", wantOverride: overrideAuto, wantCompact: false, wantNotice: "view=auto"},
	}
	for _, step := range steps {
		updated, cmd := m.Update(keyRunes("v"))
		m = updated.(model)
		if cmd != nil {
			t.Fatalf("%s: v returned a command, want nil", step.name)
		}
		if m.viewOverride != step.wantOverride {
			t.Fatalf("%s: viewOverride = %v, want %v", step.name, m.viewOverride, step.wantOverride)
		}
		if got := m.monitorLayout().Compact; got != step.wantCompact {
			t.Fatalf("%s: monitorLayout().Compact = %v, want %v", step.name, got, step.wantCompact)
		}
		if m.notice != step.wantNotice {
			t.Fatalf("%s: notice = %q, want %q", step.name, m.notice, step.wantNotice)
		}
	}
}

// Guards that the view toggle never touches tmux: no relayout is scheduled
// and the injected Relayout callback is never invoked.
func TestViewOverrideKeySchedulesNoRelayout(t *testing.T) {
	called := 0
	m := newModel(Options{Relayout: func() error { called++; return nil }})
	m.width = 40
	m.height = 20

	for range 3 {
		updated, cmd := m.Update(keyRunes("v"))
		m = updated.(model)
		if cmd != nil {
			t.Fatal("v returned a command, want nil")
		}
	}
	if m.relayoutGen != 0 {
		t.Fatalf("relayoutGen = %d, want 0", m.relayoutGen)
	}
	if called != 0 {
		t.Fatalf("Relayout called %d times, want 0", called)
	}
}

// Guards that the v toggle re-sizes the stored table for the new mode: a
// stale compact-sized table would scroll the selection out of the visible
// window in a v-forced full view on a narrow terminal.
func TestViewOverrideResizesStoredTable(t *testing.T) {
	m := newModel(Options{})
	m.width = 40
	m.height = 20
	m.resize()

	compactRows := m.monitorLayout().TableRows
	before := m.table.Height()

	for range 2 { // auto → compact → full
		updated, _ := m.Update(keyRunes("v"))
		m = updated.(model)
	}

	fullRows := m.monitorLayout().TableRows
	if fullRows == compactRows {
		t.Fatalf("full TableRows = compact TableRows = %d, want them to differ at 40x20", fullRows)
	}
	// table.Height() excludes the bubbles header row, so compare the delta
	// instead of the absolute values.
	if after := m.table.Height(); before-after != compactRows-fullRows {
		t.Fatalf("table height %d -> %d after v to full, want a %d-row shrink", before, after, compactRows-fullRows)
	}
}

// Pins the exact 40-column switcher row format: marker, ordinal, glyph, item
// label, name, right-aligned pane ID; only Name shrinks when width is short.
func TestCompactPaneLineFormat(t *testing.T) {
	tests := []struct {
		name     string
		pane     paneView
		ordinal  int
		selected bool
		width    int
		want     string
	}{
		{
			name:    "done pane fills 40 columns",
			pane:    paneView{IssueNum: 101, Name: "rate-limiter-core", PaneID: "%5", TmuxState: "live", AgentState: "done"},
			ordinal: 1,
			width:   40,
			want:    " 1✓ #101 rate-limiter-core            %5",
		},
		{
			name:     "selected row carries the > marker",
			pane:     paneView{IssueNum: 102, Name: "api-cache", PaneID: "%8", TmuxState: "live", AgentState: "running"},
			ordinal:  2,
			selected: true,
			width:    40,
			want:     ">2● #102 api-cache                    %8",
		},
		{
			name:    "only the name shrinks on overflow",
			pane:    paneView{IssueNum: 103, Name: "very-long-name-that-overflows-the-line-badly", PaneID: "%12", TmuxState: "live", AgentState: "running"},
			ordinal: 3,
			width:   40,
			want:    " 3● #103 very-long-name-that-over... %12",
		},
		{
			name:    "ordinal past 9 renders blank",
			pane:    paneView{IssueNum: 110, Name: "ten", PaneID: "%10", TmuxState: "live"},
			ordinal: 10,
			width:   40,
			want:    "  · #110 ten                         %10",
		},
		{
			name:    "stale pane shows the stale glyph",
			pane:    paneView{IssueNum: 104, Name: "gone", PaneID: "%9", TmuxState: "stale", AgentState: "running"},
			ordinal: 4,
			width:   40,
			want:    " 4✗ #104 gone                         %9",
		},
		{
			name:    "shell row uses the shell label and dashes a missing pane id",
			pane:    paneView{Kind: state.PaneKindShell, Name: "scratch", TmuxState: "-"},
			ordinal: 5,
			width:   40,
			want:    " 5- shell scratch                      -",
		},
		{
			name:    "plan task row uses the task id label",
			pane:    paneView{TaskID: "T1", Name: "fix-flaky-test", PaneID: "%21", TmuxState: "live", AgentState: "running"},
			ordinal: 4,
			width:   40,
			want:    " 4● T1 fix-flaky-test                %21",
		},
		{
			name:    "working pane shows the working glyph",
			pane:    paneView{IssueNum: 106, Name: "hook-emitter", PaneID: "%6", TmuxState: "live", AgentState: "working"},
			ordinal: 7,
			width:   40,
			want:    " 7◐ #106 hook-emitter                 %6",
		},
		{
			name:    "plan-state pane shows the plan glyph",
			pane:    paneView{TaskID: "T2", Name: "notify-sounds", PaneID: "%22", TmuxState: "live", AgentState: "plan"},
			ordinal: 8,
			width:   40,
			want:    " 8◇ T2 notify-sounds                 %22",
		},
		{
			name:    "narrow width keeps label and pane id intact",
			pane:    paneView{IssueNum: 101, Name: "rate-limiter-core", PaneID: "%5", TmuxState: "live", AgentState: "done"},
			ordinal: 1,
			width:   20,
			want:    " 1✓ #101 rate-... %5",
		},
		{
			name:    "double-width name is measured in display cells",
			pane:    paneView{IssueNum: 105, Name: "認証キャッシュ改善", PaneID: "%7", TmuxState: "live", AgentState: "running"},
			ordinal: 6,
			width:   40,
			want:    " 6● #105 認証キャッシュ改善           %7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compactPaneLine(tt.pane, tt.ordinal, tt.selected, tt.width)
			if got != tt.want {
				t.Fatalf("compactPaneLine() = %q, want %q", got, tt.want)
			}
			if cells := cellWidth(got); cells != tt.width {
				t.Fatalf("compactPaneLine() width = %d cells, want %d", cells, tt.width)
			}
		})
	}
}

// Pins the session header format: ▏ + the sidebar counter text, no > marker
// even for the active session.
func TestCompactSessionHeaderLine(t *testing.T) {
	tests := []struct {
		name    string
		session sessionSummary
		width   int
		want    string
	}{
		{
			name:    "issue parent with counters",
			session: sessionSummary{Parent: "100", Total: 3, Merged: 1, Pending: 2, Blocked: 1, Live: 3},
			width:   40,
			want:    "▏100 t3 m1 p2 b1 l3",
		},
		{
			name:    "project parent uses the proj short form",
			session: sessionSummary{Parent: "https://github.com/o/r/projects/7", Total: 1, Pending: 1, Live: 1},
			width:   40,
			want:    "▏proj/7 t1 m0 p1 b0 l1",
		},
		{
			name:    "active session gets no > marker",
			session: sessionSummary{Parent: "100", Total: 1, Pending: 1, Active: true},
			width:   40,
			want:    "▏100 t1 m0 p1 b0 l0",
		},
		{
			name:    "header truncates to width",
			session: sessionSummary{Parent: "100", Total: 3, Merged: 1, Pending: 2, Blocked: 1, Live: 3},
			width:   10,
			want:    "▏100 t3...",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compactSessionHeaderLine(tt.session, tt.width)
			if got != tt.want {
				t.Fatalf("compactSessionHeaderLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Pins the selected row's expansion: branch + PR, ci/wave/blockers/dirty, and
// the last peek line, with empty lines dropped.
func TestCompactExpansionLines(t *testing.T) {
	full := paneView{
		PaneID:     "%8",
		TmuxState:  "live",
		BranchName: "fanout/issue-102",
		PRSummary:  "#77 open",
		CIStatus:   "fail",
		Wave:       2,
		Blockers:   "#99",
		DirtyState: "dirty",
	}
	tests := []struct {
		name string
		pane paneView
		peek panePeek
		want []string
	}{
		{
			name: "full data renders all three lines",
			pane: full,
			peek: panePeek{PaneID: "%8", Output: "first\n$ make test ok\n"},
			want: []string{
				"   ⎇ fanout/issue-102 PR#77 open",
				"   ci:fail W2 blk:#99 dirty",
				"   $ make test ok",
			},
		},
		{
			name: "no branch or PR drops the first line",
			pane: paneView{PaneID: "%8", TmuxState: "live", CIStatus: "fail", Wave: 2},
			want: []string{"   ci:fail W2"},
		},
		{
			name: "clean pane drops the status line",
			pane: paneView{PaneID: "%8", TmuxState: "live", BranchName: "fanout/x", CIStatus: "-", DirtyState: "clean"},
			want: []string{"   ⎇ fanout/x"},
		},
		{
			name: "wave label wins over the wave number",
			pane: paneView{PaneID: "%8", TmuxState: "live", WaveLabel: "wave5"},
			want: []string{"   wave5"},
		},
		{
			name: "worktree error renders its own line",
			pane: paneView{PaneID: "%8", TmuxState: "stale", WorktreeErr: "worktree missing"},
			want: []string{"   worktree_error=worktree missing"},
		},
		{
			name: "multi-line worktree error flattens to one line",
			pane: paneView{PaneID: "%8", TmuxState: "stale", WorktreeErr: "fatal: bad\nhint: try\nhint: again"},
			want: []string{"   worktree_error=fatal: bad hint: tr..."},
		},
		{
			name: "loading peek shows its state",
			pane: full,
			peek: panePeek{PaneID: "%8", Loading: true, Output: "stale"},
			want: []string{
				"   ⎇ fanout/issue-102 PR#77 open",
				"   ci:fail W2 blk:#99 dirty",
				"   peek: loading...",
			},
		},
		{
			name: "peek error shows its state",
			pane: full,
			peek: panePeek{PaneID: "%8", Err: "boom", Output: "stale"},
			want: []string{
				"   ⎇ fanout/issue-102 PR#77 open",
				"   ci:fail W2 blk:#99 dirty",
				"   peek: boom",
			},
		},
		{
			name: "peek for another pane is omitted",
			pane: full,
			peek: panePeek{PaneID: "%9", Output: "other"},
			want: []string{
				"   ⎇ fanout/issue-102 PR#77 open",
				"   ci:fail W2 blk:#99 dirty",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(Options{})
			m.peek = tt.peek
			got := m.compactExpansionLines(tt.pane, 40)
			if len(got) != len(tt.want) {
				t.Fatalf("compactExpansionLines() = %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("compactExpansionLines()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// Pins the active-centered sliding window with ... markers, height-aware for
// the multi-line selected entry.
func TestCompactWindowCentersActive(t *testing.T) {
	entry := func(text string, extra ...string) compactEntry {
		return compactEntry{lines: append([]string{text}, extra...)}
	}
	tenRows := func() []compactEntry {
		entries := make([]compactEntry, 0, 10)
		for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
			entries = append(entries, entry(id))
		}
		return entries
	}

	tests := []struct {
		name      string
		entries   []compactEntry
		activeIdx int
		budget    int
		want      []string
	}{
		{
			name:      "everything fits without markers",
			entries:   tenRows()[:4],
			activeIdx: 2,
			budget:    10,
			want:      []string{"a", "b", "c", "d"},
		},
		{
			name:      "active centered with markers on both sides",
			entries:   tenRows(),
			activeIdx: 5,
			budget:    5,
			want:      []string{"...", "e", "f", "g", "..."},
		},
		{
			name:      "active first keeps only the trailing marker",
			entries:   tenRows(),
			activeIdx: 0,
			budget:    4,
			want:      []string{"a", "b", "c", "..."},
		},
		{
			name:      "active last keeps only the leading marker",
			entries:   tenRows(),
			activeIdx: 9,
			budget:    4,
			want:      []string{"...", "h", "i", "j"},
		},
		{
			name:      "multi-line active entry stays whole",
			entries:   []compactEntry{entry("a"), entry("b"), {lines: []string{"c", "c1", "c2"}, active: true}, entry("d"), entry("e")},
			activeIdx: 2,
			budget:    5,
			want:      []string{"...", "c", "c1", "c2", "..."},
		},
		{
			name:      "active entry alone over budget is clipped",
			entries:   []compactEntry{{lines: []string{"c", "c1", "c2", "c3", "c4", "c5"}, active: true}},
			activeIdx: 0,
			budget:    4,
			want:      []string{"c", "c1", "c2", "c3"},
		},
		{
			name:      "over-budget active keeps both markers and trims its tail",
			entries:   []compactEntry{entry("a"), {lines: []string{"c", "c1", "c2", "c3"}, active: true}, entry("d")},
			activeIdx: 1,
			budget:    5,
			want:      []string{"...", "c", "c1", "c2", "..."},
		},
		{
			name:      "tiny budget favors the active row over markers",
			entries:   []compactEntry{entry("a"), {lines: []string{"c", "c1"}, active: true}, entry("d")},
			activeIdx: 1,
			budget:    1,
			want:      []string{"c"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compactWindowLines(tt.entries, tt.activeIdx, tt.budget)
			if len(got) > tt.budget {
				t.Fatalf("compactWindowLines() = %d lines, want <= %d", len(got), tt.budget)
			}
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("compactWindowLines() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Guards that the compact view replaces the table + detail panel: session
// headers and switcher rows render, the table header and detail fields do not.
func TestCompactViewRendersSwitcher(t *testing.T) {
	m := newModel(Options{ProjectRoot: "/repo", Session: "fanout-demo"})
	m.width = 40
	m.height = 20
	m.allPanes = []paneView{
		{Parent: "100", IssueNum: 101, Name: "one", PaneID: "%1", TmuxState: "live", AgentState: "running", BranchName: "fanout/one"},
		{Parent: "100", IssueNum: 102, Name: "two", PaneID: "%2", TmuxState: "live", AgentState: "done"},
		{Parent: "200", IssueNum: 201, Name: "three", PaneID: "%3", TmuxState: "live"},
	}
	m.resize()

	view := m.View()
	for _, want := range []string{
		"fanout repo", // title + project-root basename, not the full header
		"▏100 t2 m0 p2 b0 l2",
		"▏200 t1 m0 p1 b0 l1",
		">1● #101 one",
		"   ⎇ fanout/one",
		" 2✓ #102 two",
		" 3· #201 three",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("compact view missing %q:\n%s", want, view)
		}
	}
	for _, banned := range []string{"PARENT", "pane=", "session=", "/repo"} {
		if strings.Contains(view, banned) {
			t.Fatalf("compact view unexpectedly contains %q:\n%s", banned, view)
		}
	}
}

// Guards that a long repository basename cannot wrap the one-line compact
// header monitorLayout budgets for.
func TestCompactHeaderTruncatesLongRepoBasename(t *testing.T) {
	m := newModel(Options{ProjectRoot: "/tmp/very-long-repository-basename-that-overflows-forty-columns"})
	m.width = 40
	m.height = 20
	m.allPanes = []paneView{{Parent: "100", IssueNum: 101, Name: "one", PaneID: "%1", TmuxState: "live"}}
	m.resize()

	view := m.View()
	headerLine, _, _ := strings.Cut(view, "\n")
	if got := cellWidth(headerLine); got > 40 {
		t.Fatalf("compact header width = %d cells, want <= 40:\n%q", got, headerLine)
	}
	if !strings.Contains(headerLine, "very-long-repository-basename-...") {
		t.Fatalf("compact header did not truncate the basename:\n%q", headerLine)
	}
}

// Guards that cycling to full restores the table even below compactWidthAt.
func TestViewOverrideFullForcesTableWhenNarrow(t *testing.T) {
	m := newModel(Options{})
	m.width = 40
	m.height = 20
	m.allPanes = []paneView{{Parent: "100", IssueNum: 101, Name: "one", PaneID: "%1", TmuxState: "live"}}
	m.resize()

	for range 2 { // auto → compact → full
		updated, _ := m.Update(keyRunes("v"))
		m = updated.(model)
	}

	if view := m.View(); !strings.Contains(view, "PARENT") {
		t.Fatalf("full-override view missing table header:\n%s", view)
	}
}

// Guards acceptance criterion 2: while compact renders, every selection-based
// key still acts on selectedPane() via the table cursor.
func TestCompactKeysUseSelectedPane(t *testing.T) {
	newCompactModel := func(focused *string) model {
		opts := Options{
			PaneAlive:         func(string) bool { return true },
			CapturePaneOutput: func(string, int) (string, error) { return "", nil },
		}
		if focused != nil {
			opts.FocusPane = func(paneID string) error {
				*focused = paneID
				return nil
			}
		}
		m := newModel(opts)
		m.width = 40
		m.height = 20
		m.allPanes = []paneView{
			{Parent: "100", IssueNum: 101, Name: "one", PaneID: "%1", TmuxState: "live"},
			{Parent: "100", IssueNum: 102, Name: "two", PaneID: "%2", TmuxState: "live"},
			{Parent: "200", IssueNum: 201, Name: "three", PaneID: "%3", TmuxState: "live"},
		}
		m.resize()
		if !m.monitorLayout().Compact {
			t.Fatal("test harness expected the compact layout at width 40")
		}
		return m
	}

	t.Run("enter focuses the cursor pane", func(t *testing.T) {
		var focused string
		m := newCompactModel(&focused)
		m.moveTableCursorTo(1)

		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("enter returned nil command, want focus command")
		}
		if msg := findPaneFocusedMsg(t, runCmd(cmd)); msg.err != nil {
			t.Fatalf("focus msg err = %v, want nil", msg.err)
		}
		if focused != "%2" {
			t.Fatalf("focused pane = %q, want %%2", focused)
		}
	})

	t.Run("numeric jump moves the cursor and focuses", func(t *testing.T) {
		var focused string
		m := newCompactModel(&focused)

		updated, cmd := m.Update(keyRunes("3"))
		m = updated.(model)
		if got := m.table.Cursor(); got != 2 {
			t.Fatalf("cursor after 3 = %d, want 2", got)
		}
		if msg := findPaneFocusedMsg(t, runCmd(cmd)); msg.err != nil {
			t.Fatalf("focus msg err = %v, want nil", msg.err)
		}
		if focused != "%3" {
			t.Fatalf("focused pane = %q, want %%3", focused)
		}
	})

	t.Run("session jump brackets move between parents", func(t *testing.T) {
		m := newCompactModel(nil)

		updated, _ := m.Update(keyRunes("]"))
		m = updated.(model)
		if got := m.table.Cursor(); got != 2 {
			t.Fatalf("cursor after ] = %d, want 2 (parent 200 start)", got)
		}
		updated, _ = m.Update(keyRunes("["))
		m = updated.(model)
		if got := m.table.Cursor(); got != 0 {
			t.Fatalf("cursor after [ = %d, want 0 (parent 100 start)", got)
		}
	})

	lifecycleKeys := []struct {
		key  string
		want lifecycleAction
	}{
		{"c", actionClose},
		{"x", actionClose},
		{"m", actionMerge},
		{"X", actionCleanup},
	}
	for _, tt := range lifecycleKeys {
		t.Run(tt.key+" targets the cursor pane", func(t *testing.T) {
			m := newCompactModel(nil)
			m.moveTableCursorTo(1)

			updated, _ := m.Update(keyRunes(tt.key))
			m = updated.(model)
			if m.pendingAction == nil {
				t.Fatalf("%s did not open a pending action", tt.key)
			}
			if m.pendingAction.action != tt.want {
				t.Fatalf("pending action = %v, want %v", m.pendingAction.action, tt.want)
			}
			if got := m.pendingAction.pane.PaneID; got != "%2" {
				t.Fatalf("pending action pane = %q, want %%2", got)
			}
		})
	}

	t.Run("filter narrows the visible list", func(t *testing.T) {
		m := newCompactModel(nil)

		updated, _ := m.Update(keyRunes("/"))
		m = updated.(model)
		updated, _ = m.Update(keyRunes("two"))
		m = updated.(model)
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(model)

		if len(m.panes) != 1 {
			t.Fatalf("filtered panes = %d, want 1", len(m.panes))
		}
		pane, ok := m.selectedPane()
		if !ok || pane.Name != "two" {
			t.Fatalf("selectedPane() = %#v ok=%v, want the filtered pane", pane, ok)
		}
		if view := m.View(); !strings.Contains(view, "#102") || strings.Contains(view, "#101") {
			t.Fatalf("compact view after filter should show only #102:\n%s", view)
		}
	})
}
