package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/table"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/state"
)

type paneView struct {
	Parent         string
	IssueNum       int
	TaskID         string
	Kind           string
	Name           string
	PaneID         string
	ShellKey       string
	SourceParent   string
	SourceIssueNum int
	SourceTaskID   string
	TmuxState      string
	TmuxTitle      string
	AgentState     string
	IssueState     string
	PRSummary      string
	HasMergedPR    bool
	Wave           int
	WaveLabel      string
	WaveBadge      string
	Blockers       string
	Blocked        bool
	CIStatus       string
	DiffSummary    string
	DirtyState     string
	WorktreeErr    string
	BranchName     string
	WorktreePath   string
	worktreeAbs    string
	// sourceProjectRoot は複数 worktree をまたいで集約した場合に、この pane を
	// 記録した worktree の root。close/merge/cleanup をその所有元 state.json へ
	// 向けるために使う。単一 worktree のときは空(= m.opts.ProjectRoot を使う)。
	sourceProjectRoot string
	// sourceProjectRoots は同一 identity が複数 worktree に記録されていた場合の
	// 全所有 root(通常は [sourceProjectRoot])。close/cleanup が de-duplicate
	// された sibling ストアも漏れなく対象にするために使う。
	sourceProjectRoots []string
	Agent              string
	CreatedAt          string
	Prompt             string
	Derived            sessionview.PaneDerived
}

type hudSummary struct {
	Total   int
	Merged  int
	Pending int
	Blocked int
}

// agentStateGlyphs は @fanout_agent_state の値ごとの表示グリフ。5 値化
// (docs/competitive-herdr.ja.md 提案 A)に備え working / idle / blocked を
// 先行定義しており、5 値化時は sessionview の normalizeAgentState の許可リスト
// 拡張だけで表示側の変更はゼロになる。
var agentStateGlyphs = map[string]string{
	"running": "●",
	"done":    "✓",
	"working": "◐",
	"idle":    "○",
	"blocked": "◆",
}

// agentStateGlyph はグリフの単一情報源。入力は AgentState と TmuxState のみで、
// ペイン内容(capture-pane)からの状態推定はしない。stale ペインと pane 記録の
// ない行の AgentState は残骸なので、map より先に弾く。tmux degraded
// ("unknown")では state.json の記録値が最善情報としてそのまま出る。
func agentStateGlyph(p paneView) string {
	switch p.TmuxState {
	case "stale":
		return "✗"
	case "-":
		return "-"
	}
	if g, ok := agentStateGlyphs[p.AgentState]; ok {
		return g
	}
	if p.TmuxState == "live" {
		return "·"
	}
	return "-"
}

func (p paneView) tableRow() table.Row {
	tmuxState := p.TmuxState
	if tmuxState == "stale" {
		tmuxState = "stale!"
	}
	return table.Row{
		compactParent(p.Parent),
		p.itemLabel(),
		truncate(dash(p.waveCell()), 12),
		truncate(dash(p.Blockers), 22),
		truncate(p.Name, 28),
		dash(p.Agent),
		agentStateGlyph(p),
		tmuxState,
		dash(p.IssueState),
		truncate(dash(p.PRSummary), 12),
		truncate(dash(p.CIStatus), 7),
		dash(p.DiffSummary),
		dash(p.DirtyState),
		truncate(dash(p.BranchName), 18),
		dash(p.PaneID),
	}
}

func (p paneView) canFocus() bool {
	return strings.TrimSpace(p.PaneID) != "" && p.TmuxState != "stale" && p.TmuxState != "-"
}

func (p paneView) canPeek() bool {
	return p.canFocus()
}

func (p paneView) isShell() bool {
	return p.Kind == state.PaneKindShell
}

func (p paneView) isAttachedAgent() bool {
	return p.Kind == state.PaneKindAttachedAgent
}

func (p paneView) isPaneOnly() bool {
	return p.isShell() || p.isAttachedAgent()
}

func (p paneView) absoluteWorktreePath(projectRoot string) string {
	path := strings.TrimSpace(firstNonEmpty(p.worktreeAbs, p.WorktreePath))
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return path
	}
	return filepath.Join(projectRoot, path)
}

func columnsForWidth(width int) []table.Column {
	nameWidth := clampInt(width/7, 12, 20)
	blockerWidth := clampInt(width/8, 10, 16)
	branchWidth := clampInt(width/8, 10, 16)
	return []table.Column{
		{Title: "PARENT", Width: 10},
		{Title: "ISSUE", Width: 7},
		{Title: "WAVE", Width: 12},
		{Title: "BLOCKERS", Width: blockerWidth},
		{Title: "NAME", Width: nameWidth},
		{Title: "AGENT", Width: 7},
		{Title: "RUN", Width: 5},
		{Title: "TMUX", Width: 7},
		{Title: "STATE", Width: 8},
		{Title: "PR", Width: 12},
		{Title: "CI", Width: 7},
		{Title: "DIFF", Width: 8},
		{Title: "DIRTY", Width: 7},
		{Title: "BRANCH", Width: branchWidth},
		{Title: "PANE", Width: 7},
	}
}

func (m model) footerText() string {
	parts := []string{"? help"}
	if m.filterEditing {
		parts = append(parts, "typing")
	}
	if m.filterEditing || strings.TrimSpace(m.filterQuery) != "" {
		parts = append(parts, fmt.Sprintf("filter=%q %d/%d", m.filterQuery, len(m.panes), len(m.allPanes)))
	}
	if watchText := m.watchFooterText(); watchText != "" {
		parts = append(parts, watchText)
	}
	parts = append(parts, "state "+formatClock(m.lastState), "gh "+formatClock(m.lastGH))
	return strings.Join(parts, "  ")
}

func (p paneView) paneCI() string {
	if strings.TrimSpace(p.Derived.CI) != "" {
		return p.Derived.CI
	}
	if p.CIStatus == "-" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(p.CIStatus))
}

func (p paneView) primaryPRState() string {
	if strings.TrimSpace(p.Derived.PrimaryPRState) != "" {
		return p.Derived.PrimaryPRState
	}
	fields := strings.Fields(p.PRSummary)
	if len(fields) >= 2 {
		return strings.ToLower(fields[1])
	}
	return "none"
}

func waveBadge(wave int, blocked bool) string {
	if wave <= 0 {
		return "-"
	}
	state := "ready"
	if blocked {
		state = "blocked"
	}
	return fmt.Sprintf("W%d %s", wave, state)
}

func (p paneView) waveCell() string {
	if strings.TrimSpace(p.WaveLabel) != "" {
		return p.WaveLabel
	}
	return p.WaveBadge
}

func (p paneView) waveText() string {
	parts := nonDashStrings(p.WaveLabel, p.WaveBadge)
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func (p paneView) dependencyWaveText() string {
	if p.Wave <= 0 {
		return ""
	}
	return fmt.Sprintf("wave%d", p.Wave)
}

func (p paneView) isTask() bool {
	return strings.TrimSpace(p.TaskID) != ""
}

func (p paneView) identityLabel() string {
	if p.isTask() {
		return p.TaskID
	}
	if p.isShell() {
		if label := strings.TrimSpace(firstNonEmpty(p.Name, p.Derived.Name, p.TmuxTitle)); label != "" && label != "-" {
			return label
		}
		if slug := strings.TrimSpace(p.Derived.Name); slug != "" && slug != "-" {
			return slug
		}
		return "shell"
	}
	if p.isAttachedAgent() {
		if label := strings.TrimSpace(firstNonEmpty(p.Name, p.Derived.Name, p.SourceTaskID, sourceIssueLabel(p.SourceIssueNum))); label != "" && label != "-" {
			return label
		}
		return "attached agent"
	}
	return "#" + strconv.Itoa(p.IssueNum)
}

func issueTitle(status issueStatus, num int) string {
	if strings.TrimSpace(status.Title) != "" {
		return status.Title
	}
	return "#" + strconv.Itoa(num)
}

func summarizePRs(prs []ghissue.PRRef) string {
	pr, ok := ghissue.PrimaryPR(prs)
	if !ok {
		return "-"
	}
	return "#" + strconv.Itoa(pr.Number) + " " + dash(pr.DisplayState())
}

func (p paneView) itemLabel() string {
	if strings.TrimSpace(p.TaskID) != "" {
		return p.TaskID
	}
	if p.isShell() {
		return "shell"
	}
	if p.isAttachedAgent() {
		if p.SourceTaskID != "" {
			return p.SourceTaskID + "+"
		}
		if p.SourceIssueNum > 0 {
			return "#" + strconv.Itoa(p.SourceIssueNum) + "+"
		}
		return "agent+"
	}
	return "#" + strconv.Itoa(p.IssueNum)
}

func sourceIssueLabel(num int) string {
	if num <= 0 {
		return ""
	}
	return "#" + strconv.Itoa(num)
}

func compactParent(parent string) string {
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return "-"
	}
	if n, ok := strings.CutPrefix(parent, "https://github.com/"); ok {
		parts := strings.Split(strings.Trim(n, "/"), "/")
		if len(parts) >= 4 && parts[2] == "projects" {
			return "proj/" + parts[3]
		}
	}
	return truncate(parent, 10)
}

func summarizeHUD(panes []paneView) hudSummary {
	summary := hudSummary{}
	for _, pane := range panes {
		if pane.isPaneOnly() {
			continue
		}
		summary.Total++
		if pane.HasMergedPR {
			summary.Merged++
		}
		if pane.Blocked {
			summary.Blocked++
		}
	}
	summary.Pending = summary.Total - summary.Merged
	return summary
}

func formatHUD(summary hudSummary) string {
	return fmt.Sprintf("total=%d merged=%d pending=%d blocked=%d", summary.Total, summary.Merged, summary.Pending, summary.Blocked)
}
