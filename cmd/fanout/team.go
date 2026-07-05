package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/butaosuinu/fanout/internal/app/briefing"
	"github.com/butaosuinu/fanout/internal/core/planspec"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

// buildTeamContext assembles the per-run --team data exactly once: the DB
// path (so the briefing and the registry seed can never disagree) and the
// sibling roster from this run's plan targets. Pure; it never touches the DB,
// which keeps --team --dry-run free of side effects.
func buildTeamContext(projectRoot, parentRef string, targets []ghissue.Issue) *briefing.TeamContext {
	siblings := make([]briefing.TeamSibling, 0, len(targets))
	for _, issue := range targets {
		siblings = append(siblings, briefing.TeamSibling{Num: issue.Number, Title: issue.Title})
	}
	return &briefing.TeamContext{
		ParentLabel: teamParentLabel(parentRef),
		DBPath:      team.DBPath(projectRoot, parentRef),
		Siblings:    siblings,
	}
}

// buildTaskTeamContext assembles the per-run --team data for an issue-less
// plan: the DB path (so the briefing and the registry seed can never disagree)
// and the sibling roster keyed by task id. Pure; it never touches the DB,
// which keeps `fanout plan --team --dry-run` free of side effects.
func buildTaskTeamContext(projectRoot, parentRef string, targets []planspec.Task) *briefing.TeamContext {
	siblings := make([]briefing.TeamSibling, 0, len(targets))
	for _, task := range targets {
		siblings = append(siblings, briefing.TeamSibling{TaskID: task.ID, Title: task.Title})
	}
	return &briefing.TeamContext{
		ParentLabel: teamParentLabel(parentRef),
		DBPath:      team.DBPath(projectRoot, parentRef),
		Siblings:    siblings,
	}
}

// teamParentLabel mirrors team.ParentDBSlug's issue-number classification
// (strconv-based, leading zeros collapsed) so the briefing identity line and
// the DB path shown next to it always agree on the parent spelling;
// non-numeric refs (Project URLs, @manual) read verbatim.
func teamParentLabel(parentRef string) string {
	if n, err := strconv.Atoi(strings.TrimSpace(parentRef)); err == nil && n >= 0 {
		return fmt.Sprintf("#%d", n)
	}
	return parentRef
}

// seedTeamRegistry upserts the panes created this run into the per-parent
// peers table. Messaging is best-effort by design: every failure is a warning
// and the fan-out result is never affected.
func seedTeamRegistry(lg *log.Logger, dbPath string, st state.Store, parentRef string, created []int) {
	db, err := team.Open(dbPath)
	if err != nil {
		lg.Warn("team: %v", err)
		return
	}
	defer func() {
		if err := db.Close(); err != nil {
			lg.Warn("team: close registry db: %v", err)
		}
	}()
	if err := team.EnsureSchema(db); err != nil {
		lg.Warn("team: %v", err)
		return
	}

	// One timestamp for the run so a cohort launched together joins together.
	now := team.Now()
	seeded := 0
	for _, num := range created {
		pane, ok := st.Find(parentRef, num)
		if !ok {
			lg.Warn("team: #%d: no state row to seed into the peers registry", num)
			continue
		}
		if err := team.UpsertPeer(db, pane, now); err != nil {
			lg.Warn("team: %v", err)
			continue
		}
		seeded++
	}
	if seeded > 0 {
		lg.Ok("team: seeded %d peer(s) -> %s", seeded, dbPath)
	}
}

// seedTaskTeamRegistry is the issue-less plan variant of seedTeamRegistry: it
// upserts the plan-task panes created this run into the per-parent peers table,
// looked up by task id. Best-effort by design: every failure is a warning and
// the fan-out result is never affected.
func seedTaskTeamRegistry(lg *log.Logger, dbPath string, st state.Store, parentRef string, createdIDs []string) {
	db, err := team.Open(dbPath)
	if err != nil {
		lg.Warn("team: %v", err)
		return
	}
	defer func() {
		if err := db.Close(); err != nil {
			lg.Warn("team: close registry db: %v", err)
		}
	}()
	if err := team.EnsureSchema(db); err != nil {
		lg.Warn("team: %v", err)
		return
	}

	// One timestamp for the run so a cohort launched together joins together.
	now := team.Now()
	seeded := 0
	for _, id := range createdIDs {
		pane, ok := st.FindTask(parentRef, id)
		if !ok {
			lg.Warn("team: %s: no state row to seed into the peers registry", id)
			continue
		}
		if err := team.UpsertPeer(db, pane, now); err != nil {
			lg.Warn("team: %v", err)
			continue
		}
		seeded++
	}
	if seeded > 0 {
		lg.Ok("team: seeded %d peer(s) -> %s", seeded, dbPath)
	}
}
