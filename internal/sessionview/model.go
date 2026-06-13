// Package sessionview turns fanout's persisted pane state, live tmux pane
// liveness, and GitHub issue/PR state into a single read-only Snapshot grouped
// by parent ("Session"). It is the shared data layer the web dashboard renders
// now and the future resident TUI will render later — neither one duplicates
// the aggregation logic. The package is pure plus dependency-injected IO
// (see Collectors) so it can be unit-tested without a live gh/tmux/git.
package sessionview

import (
	"github.com/butaosuinu/fanout/internal/blockers"
	"github.com/butaosuinu/fanout/internal/ghissue"
)

// IssueStateUnknown marks a pane whose issue/PR state could not be fetched
// (gh down, repo unresolved). The dashboard renders it distinctly from
// OPEN/CLOSED rather than implying the issue is open.
const IssueStateUnknown = "UNKNOWN"

// Snapshot is the full dashboard view at one instant. It serializes directly to
// /api/snapshot, so JSON tags are part of the wire contract the frontend reads.
type Snapshot struct {
	Repo        string    `json:"repo"` // "owner/name"; "" when unresolved
	ProjectRoot string    `json:"projectRoot"`
	GeneratedAt string    `json:"generatedAt"` // RFC3339
	Sessions    []Session `json:"sessions"`    // grouped by parent, sorted
	Rollup      Rollup    `json:"rollup"`      // repo-wide totals across all sessions
	Degraded    Degraded  `json:"degraded"`    // which data sources were unavailable
}

// Session is the set of panes that share a Pane.Parent — one parent issue (or
// Project) renders as one card. A repo's state.json may hold several parents.
type Session struct {
	Parent string     `json:"parent"`
	Panes  []PaneView `json:"panes"`
	Rollup Rollup     `json:"rollup"`
}

// WorktreeStat is the small git status summary for one recorded pane worktree.
type WorktreeStat struct {
	DiffSummary string
	DirtyState  string
}

// PaneView is one recorded pane augmented with tmux liveness, gh state, and git
// worktree status.
type PaneView struct {
	IssueNum     int             `json:"issueNum"`
	TaskID       string          `json:"taskId,omitempty"`
	Slug         string          `json:"slug"`
	DisplayName  string          `json:"displayName"`
	Agent        string          `json:"agent"`
	BranchName   string          `json:"branchName"`
	PaneID       string          `json:"paneId"`
	WorktreePath string          `json:"worktreePath"`
	CreatedAt    string          `json:"createdAt"`
	Alive        bool            `json:"alive"`      // PaneID is among the live tmux panes
	IssueState   string          `json:"issueState"` // OPEN / CLOSED / UNKNOWN
	PRs          []ghissue.PRRef `json:"prs"`
	HasMergedPR  bool            `json:"hasMergedPr"`
	DiffSummary  string          `json:"diffSummary"`           // +X/-Y vs merge-base with the base branch (committed + uncommitted)
	DirtyState   string          `json:"dirtyState"`            // dirty / clean / unknown
	WorktreeErr  string          `json:"worktreeErr,omitempty"` // per-row gitstat failure, if any

	TmuxState string `json:"tmuxState"`           // "live" / "stale" / "unknown" / "-"
	TmuxTitle string `json:"tmuxTitle,omitempty"` // live tmux pane title; "" when dead
	// AgentState は "running" / "done" / ""(pane 死亡・不明)。alive な pane は
	// 起動ラッパーが設定する tmux pane user option @fanout_agent_state からの
	// 動的判定、tmux 不通時は state.json の起動時記録値(AgentStatus)への
	// fallback。
	AgentState string `json:"agentState,omitempty"`
	// PlanMode は Codex Plan Mode(--codex-plan-mode)で起動した記録ペインか
	// どうか(state row の CodexPlanMode の passthrough)。ダッシュボードは
	// このフラグで GET /api/plan の対象と Plan セクションの表示を限定する。
	PlanMode  bool              `json:"planMode,omitempty"`
	Prompt    string            `json:"prompt,omitempty"`    // state row's original prompt
	CIStatus  string            `json:"ciStatus,omitempty"`  // primary-PR CI via ghissue.SummarizeCI; lowercase
	Wave      int               `json:"wave,omitempty"`      // DAG depth, 1-based; 0 = unknown
	WaveLabel string            `json:"waveLabel,omitempty"` // state row wave label, else parent-body heading
	Blockers  []blockers.Status `json:"blockers"`            // always non-nil; serializes as []
	Blocked   bool              `json:"blocked"`             // at least one blocker still OPEN
	// NotStarted は state.json に記録 pane が無い「未開始」の子 issue を表す
	// synthetic 行(TUI の synthetic 行の web 移植)。PaneID/Agent/Branch 等の
	// pane 由来フィールドは zero、TmuxState は closed/deferred/queued/unknown。
	NotStarted bool `json:"notStarted,omitempty"`
	// closeUnconfirmed は synthetic な CLOSED 行のうち PR 状態を確認できなかった
	// もの(IssuePRs が miss/失敗し wave graph の状態へ fallback)を示す内部
	// フラグ。一時的な gh 失敗で session が 100%/AllMerged に見えないよう、
	// こうした行は rollup に残す。JSON には出さない(非エクスポート)。
	closeUnconfirmed bool
}

// Rollup is an aggregate count band, mirroring --status's summary plus liveness.
// Total/Merged/Pending/Blocked count synthetic not-started rows too, so the
// progress band reflects session completion, not just launched panes.
type Rollup struct {
	Total      int  `json:"total"`
	Merged     int  `json:"merged"`     // panes with at least one MERGED PR
	Pending    int  `json:"pending"`    // total - merged
	Live       int  `json:"live"`       // panes whose tmux pane is alive
	Running    int  `json:"running"`    // agent が実行中(AgentState=="running")のペイン数
	Blocked    int  `json:"blocked"`    // panes whose blockers still have an OPEN issue
	NotStarted int  `json:"notStarted"` // 未開始子 issue(synthetic 行)の数
	AllMerged  bool `json:"allMerged"`
}

// Degraded records which collectors failed so the UI can show a banner instead
// of silently presenting partial data as complete.
type Degraded struct {
	Tmux   bool   `json:"tmux"`
	GitHub bool   `json:"github"`
	Reason string `json:"reason,omitempty"`
}
