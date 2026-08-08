package run

import (
	"database/sql"
	"fmt"

	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/msgstore"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

// planHasCodexTeamBridge reports whether this cohort can launch the hidden
// app-server bridge. A mixed cohort counts: every task label must be present
// before the first Codex watcher can drain a message from any sibling.
func planHasCodexTeamBridge(cfg *cliflags.Config, tasks []planspec.Task) bool {
	for _, task := range tasks {
		if cfg.EffectiveAgent(task.ID) == "codex" {
			return true
		}
	}
	return false
}

// preseedTaskTeamRegistry publishes the full planned cohort atomically before
// executeTaskPlan starts its first pane. The later normal seeder replaces each
// placeholder with its real pane metadata.
func preseedTaskTeamRegistry(dbPath, parentRef string, tasks []planspec.Task, cfg *cliflags.Config) (retErr error) {
	db, err := openOwnedTeamRegistry(dbPath, parentRef)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close task team registry: %w", closeErr)
		}
	}()

	panes := make([]state.Pane, 0, len(tasks))
	for _, task := range tasks {
		displayName := task.DisplayName
		if displayName == "" {
			displayName = task.Title
		}
		panes = append(panes, state.Pane{
			Parent:      parentRef,
			TaskID:      task.ID,
			Agent:       cfg.EffectiveAgent(task.ID),
			DisplayName: displayName,
		})
	}
	if err := team.UpsertPeers(db, panes, team.Now()); err != nil {
		return fmt.Errorf("preseed task team registry: %w", err)
	}
	return nil
}

// cleanupUncreatedTaskPeers removes placeholders for tasks the fail-fast loop
// never created. Messages stay intact and become addressable again on a retry.
// DeleteProvisionalTaskPeer's empty-pane guard preserves rows already replaced
// by a live pane even if the caller's created-id bookkeeping is stale.
func cleanupUncreatedTaskPeers(dbPath, parentRef string, planned []planspec.Task, createdIDs []string) (retErr error) {
	db, err := openOwnedTeamRegistry(dbPath, parentRef)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close task team registry: %w", closeErr)
		}
	}()

	created := make(map[string]struct{}, len(createdIDs))
	for _, taskID := range createdIDs {
		created[taskID] = struct{}{}
	}
	for _, task := range planned {
		if _, ok := created[task.ID]; ok {
			continue
		}
		if err := team.DeleteProvisionalTaskPeer(db, parentRef, task.ID); err != nil {
			return err
		}
	}
	return nil
}

func openOwnedTeamRegistry(dbPath, parentRef string) (*sql.DB, error) {
	db, err := team.Open(dbPath)
	if err != nil {
		return nil, err
	}
	if err := team.EnsureSchema(db); err != nil {
		// The schema error is authoritative; closing only releases this failed
		// setup handle and cannot make the registry usable.
		_ = db.Close()
		return nil, err
	}
	if _, err := msgstore.New(db, parentRef); err != nil {
		// The owner mismatch is authoritative and no registry write has run.
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
