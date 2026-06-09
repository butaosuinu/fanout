// Package sessionview turns fanout's persisted pane state, live tmux pane
// liveness, and GitHub issue/PR state into a single read-only Snapshot grouped
// by parent ("Session"). It is the shared data layer the web dashboard renders
// now and the future resident TUI will render later — neither one duplicates
// the aggregation logic. The package is pure plus dependency-injected IO
// (see Collectors) so it can be unit-tested without a live gh/tmux/git.
package sessionview

import "github.com/butaosuinu/fanout/internal/ghissue"

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
	DiffSummary  string          `json:"diffSummary"`           // +X/-Y from git diff --shortstat HEAD
	DirtyState   string          `json:"dirtyState"`            // dirty / clean / unknown
	WorktreeErr  string          `json:"worktreeErr,omitempty"` // per-row gitstat failure, if any
}

// Rollup is an aggregate count band, mirroring --status's summary plus liveness.
type Rollup struct {
	Total     int  `json:"total"`
	Merged    int  `json:"merged"`  // panes with at least one MERGED PR
	Pending   int  `json:"pending"` // total - merged
	Live      int  `json:"live"`    // panes whose tmux pane is alive
	Blocked   int  `json:"blocked"` // reserved; always 0 in the MVP (see aggregate.go)
	AllMerged bool `json:"allMerged"`
}

// Degraded records which collectors failed so the UI can show a banner instead
// of silently presenting partial data as complete.
type Degraded struct {
	Tmux   bool   `json:"tmux"`
	GitHub bool   `json:"github"`
	Reason string `json:"reason,omitempty"`
}
