package team

import (
	"database/sql"
	"fmt"

	"github.com/butaosuinu/fanout/internal/infra/state"
)

type peerExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// UpsertPeer records pane p in the peers registry, replacing any existing row
// for the same peer number. A conflict means the pane was fanned out again
// after a --close (the /tmp DB outlives .fanout/state.json), so the row is
// rewritten as the new pane's identity: joined_at moves to now and last_seen
// resets to NULL so a dead pane's activity never shows as recent. now is the
// caller's timestamp (team.Now()) so tests stay deterministic. Issue panes key
// on their issue number; issue-less plan-task panes key on the synthetic
// TaskPeerNum and carry their task id in the task_id column.
func UpsertPeer(db *sql.DB, p state.Pane, now string) error {
	return upsertPeer(db, p, now)
}

// UpsertPeers atomically records a planned cohort. Codex team bridges can
// start as soon as the first pane launches, so every task-id label must become
// visible together before any pane can drain and mark an early message read.
func UpsertPeers(db *sql.DB, panes []state.Pane, now string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin peer registry batch: %w", err)
	}
	defer func() {
		// Rollback is a documented no-op after Commit and keeps every error path
		// atomic without obscuring the original write or commit error.
		_ = tx.Rollback()
	}()
	for _, pane := range panes {
		if err := upsertPeer(tx, pane, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit peer registry batch: %w", err)
	}
	return nil
}

func upsertPeer(db peerExecer, p state.Pane, now string) error {
	_, err := db.Exec(`INSERT INTO peers (issue, pane_id, slug, worktree_path, agent, display_name, joined_at, task_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(issue) DO UPDATE SET
  pane_id=excluded.pane_id, slug=excluded.slug, worktree_path=excluded.worktree_path,
  agent=excluded.agent, display_name=excluded.display_name,
  joined_at=excluded.joined_at, last_seen=NULL, task_id=excluded.task_id`,
		PeerNum(p), p.PaneID, p.Slug, p.WorktreePath, p.Agent, p.DisplayName, now, peerTaskIDArg(p.TaskID))
	if err != nil {
		return fmt.Errorf("upsert peer %s: %w", peerLabel(p), err)
	}
	return nil
}

// DeleteProvisionalTaskPeer removes one plan-task placeholder whose empty pane
// id shows it has not been replaced by live pane metadata. Matching both the
// stable synthetic number and task_id protects an unrelated row if a hash
// collision ever occurs. Numeric issue peers are never eligible for cleanup.
func DeleteProvisionalTaskPeer(db *sql.DB, parentRef, taskID string) error {
	_, err := db.Exec(
		"DELETE FROM peers WHERE issue = ? AND task_id = ? AND COALESCE(pane_id, '') = ''",
		TaskPeerNum(parentRef, taskID), taskID,
	)
	if err != nil {
		return fmt.Errorf("delete task peer %s: %w", taskID, err)
	}
	return nil
}

// PeerNum is the int key under which pane p is stored in the peers/messages
// tables. Plan-task panes (string TaskID, IssueNum 0) map to the stable
// synthetic TaskPeerNum; numeric issue panes use their IssueNum verbatim.
func PeerNum(p state.Pane) int {
	if p.TaskID != "" {
		return TaskPeerNum(p.Parent, p.TaskID)
	}
	return p.IssueNum
}

// peerTaskIDArg binds the task id as NULL for numeric issue panes (no task id)
// and as the task id string for plan-task panes.
func peerTaskIDArg(taskID string) any {
	if taskID == "" {
		return nil
	}
	return taskID
}

// peerLabel is the human label for a pane in registry error messages: the task
// id for plan-task panes, "#<issue>" for numeric issue panes.
func peerLabel(p state.Pane) string {
	if p.TaskID != "" {
		return p.TaskID
	}
	return fmt.Sprintf("#%d", p.IssueNum)
}
