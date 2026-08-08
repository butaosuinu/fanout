package tui

import (
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

// TestAgentStateGlyph pins the glyph mapping that both the RUN column and the
// future compact switcher render from (docs/tui-compact-switcher.ja.md 状態グリフ).
func TestAgentStateGlyph(t *testing.T) {
	tests := []struct {
		name string
		pane paneView
		want string
	}{
		{name: "running live pane", pane: paneView{TmuxState: "live", AgentState: "running"}, want: "●"},
		{name: "done live pane", pane: paneView{TmuxState: "live", AgentState: "done"}, want: "✓"},
		// 6 値契約(competitive-herdr.ja.md 提案 A + plan)の hook 値。
		{name: "working live pane", pane: paneView{TmuxState: "live", AgentState: "working"}, want: "◐"},
		{name: "idle live pane", pane: paneView{TmuxState: "live", AgentState: "idle"}, want: "○"},
		{name: "blocked live pane", pane: paneView{TmuxState: "live", AgentState: "blocked"}, want: "◆"},
		{name: "plan live pane", pane: paneView{TmuxState: "live", AgentState: "plan"}, want: "◇"},
		{name: "live pane without state", pane: paneView{TmuxState: "live"}, want: "·"},
		{name: "live pane with unknown state value", pane: paneView{TmuxState: "live", AgentState: "garbage"}, want: "·"},
		{name: "stale pane", pane: paneView{TmuxState: "stale"}, want: "✗"},
		// 死んだペインに残った状態値は残骸: stale が勝つ。
		{name: "stale wins over leftover done", pane: paneView{TmuxState: "stale", AgentState: "done"}, want: "✗"},
		{name: "no recorded pane", pane: paneView{TmuxState: "-"}, want: "-"},
		// pane 記録なし + degraded fallback の記録値: pane が無い以上 running とは出さない。
		{name: "no recorded pane ignores leftover state", pane: paneView{TmuxState: "-", AgentState: "running"}, want: "-"},
		// tmux degraded 時は state.json の記録値が fallback で入る。
		{name: "degraded tmux keeps recorded state", pane: paneView{TmuxState: "unknown", AgentState: "running"}, want: "●"},
		{name: "degraded tmux without state", pane: paneView{TmuxState: "unknown"}, want: "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentStateGlyph(tt.pane); got != tt.want {
				t.Errorf("agentStateGlyph(%+v) = %q, want %q", tt.pane, got, tt.want)
			}
		})
	}
}

// columnIndex resolves a table column position by title so tests do not
// hardcode indexes that shift when columns are inserted.
func columnIndex(t *testing.T, title string) int {
	t.Helper()
	for i, c := range columnsForWidth(120) {
		if c.Title == title {
			return i
		}
	}
	t.Fatalf("columnsForWidth(120) has no %q column", title)
	return -1
}

// TestTableRowMatchesColumns guards tableRow and columnsForWidth against
// drifting apart when columns are added or reordered.
func TestTableRowMatchesColumns(t *testing.T) {
	columns := columnsForWidth(120)
	row := paneView{TmuxState: "live", AgentState: "running"}.tableRow()
	if len(row) != len(columns) {
		t.Fatalf("len(tableRow()) = %d, len(columnsForWidth(120)) = %d, want equal", len(row), len(columns))
	}
	if got := row[columnIndex(t, "RUN")]; got != "●" {
		t.Fatalf("RUN cell = %q, want ●", got)
	}
	if idx, agentIdx := columnIndex(t, "RUN"), columnIndex(t, "AGENT"); idx != agentIdx+1 {
		t.Fatalf("RUN column index = %d, want %d (next to AGENT)", idx, agentIdx+1)
	}
}

func TestPaneViewRuntimeActionsRequireSupportedBackend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backend backend.Name
		want    bool
	}{
		{name: "legacy tmux row", want: true},
		{name: "explicit tmux row", backend: backend.Tmux, want: true},
		{name: "herdr row", backend: backend.Herdr, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pane := paneView{Backend: tc.backend, PaneID: "%1", TmuxState: "live"}
			if got := pane.canFocus(); got != tc.want {
				t.Fatalf("canFocus() = %v, want %v", got, tc.want)
			}
			if got := pane.canPeek(); got != tc.want {
				t.Fatalf("canPeek() = %v, want %v", got, tc.want)
			}
		})
	}
}
