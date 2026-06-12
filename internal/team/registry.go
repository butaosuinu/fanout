package team

import (
	"database/sql"
	"fmt"

	"github.com/butaosuinu/fanout/internal/state"
)

// UpsertPeer records pane p in the peers registry, replacing any existing row
// for the same issue. A conflict means the issue was fanned out again after a
// --close (the /tmp DB outlives .fanout/state.json), so the row is rewritten
// as the new pane's identity: joined_at moves to now and last_seen resets to
// NULL so a dead pane's activity never shows as recent. now is the caller's
// timestamp (team.Now()) so tests stay deterministic.
func UpsertPeer(db *sql.DB, p state.Pane, now string) error {
	_, err := db.Exec(`INSERT INTO peers (issue, pane_id, slug, worktree_path, agent, display_name, joined_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(issue) DO UPDATE SET
  pane_id=excluded.pane_id, slug=excluded.slug, worktree_path=excluded.worktree_path,
  agent=excluded.agent, display_name=excluded.display_name,
  joined_at=excluded.joined_at, last_seen=NULL`,
		p.IssueNum, p.PaneID, p.Slug, p.WorktreePath, p.Agent, p.DisplayName, now)
	if err != nil {
		return fmt.Errorf("upsert peer #%d: %w", p.IssueNum, err)
	}
	return nil
}
