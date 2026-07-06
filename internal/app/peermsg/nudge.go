package peermsg

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/team"
	"github.com/butaosuinu/fanout/internal/infra/tmuxrun"
)

// nudgeText is the hint `msg nudge` types into a peer pane. It carries no
// message body on purpose — only a pointer to the inbox — so the actual
// content stays in the SQLite DB and the pane never displays a peer's words
// out of context. The em dash is intentional; it is a stable UTF-8 literal.
const nudgeText = "[fanout] peer message in your inbox — run: fanout msg inbox"

// msgNudgeReport is the --json encoding of a nudge attempt. Nudged is true only
// when the send-keys actually went out; Reason explains every other outcome so
// automation can tell a delivered push from a best-effort no-op.
type msgNudgeReport struct {
	Target     int    `json:"target"`
	TargetTask string `json:"targetTask,omitempty"`
	PaneID     string `json:"pane_id"`
	AgentState string `json:"agent_state"`
	Nudged     bool   `json:"nudged"`
	Reason     string `json:"reason"`
}

// shouldNudge reports whether a pane in the given @fanout_agent_state should
// receive a send-keys nudge. Active/waiting agent states qualify: "running"
// from the launch wrapper and the Codex Plan Mode controller's "working",
// "plan", and "idle". "blocked" is excluded because it may be sitting at an
// approval/input prompt; "done" (agent exited, bare shell), "" (unset: legacy
// or non-fanout pane), and any other value are no-op successes so a hint never
// lands in a shell or an unrelated pane.
//
// This is the deliberate re-design of the original "agentStatus == idle" gate:
// the dmux idle/analyzing/waiting/working enum no longer exists, and the only
// live signal (@fanout_agent_state) only tells us that a supported agent or the
// Plan Mode controller owns the pane. These states are safe in practice because
// the supported agents (claude, codex) queue typed input during a turn rather
// than aborting it, so a nudge to a busy agent is picked up at its next
// checkpoint — the same pull the message already guarantees, only sooner.
func shouldNudge(agentState string) bool {
	switch agentState {
	case "running", "working", "plan", "idle":
		return true
	default:
		return false
	}
}

// runMsgNudge resolves peer req.To's recorded pane from state.json and pushes
// the inbox hint when its agent is running. Every operational miss (recipient
// absent, no pane id, pane gone, agent not running, send-keys failure) is a
// no-op SUCCESS with a warning/reason: the message is already persisted by
// send, so a failed nudge must never break messaging. Only invocation errors
// (handled earlier) exit non-zero.
func runMsgNudge(req *Request, parent string, deps Deps, lg *log.Logger) exitcode.Code {
	st, err := deps.LoadState()
	if err != nil {
		lg.Err("msg nudge: %v", err)
		return exitcode.Invocation
	}
	// Plan recipients are addressed by task id (state rows carry IssueNum 0),
	// so look them up by task id under a plan parent; issue/Project recipients
	// keep the numeric lookup.
	var pane state.Pane
	var found bool
	if team.IsPlanParent(parent) {
		pane, found = st.FindTask(parent, req.ToRaw)
	} else {
		pane, found = st.Find(parent, req.To)
	}

	report := msgNudgeReport{Target: req.To, PaneID: pane.PaneID}
	if team.IsPlanParent(parent) {
		report.TargetTask = req.ToRaw
	}
	switch {
	case !found:
		report.Reason = "recipient is not recorded in fanout state"
	case pane.PaneID == "":
		report.Reason = "recipient has no recorded pane"
	default:
		report.AgentState, report.Reason, report.Nudged = deliverNudge(pane, deps)
	}
	return writeMsgNudgeResult(req, parent, report, lg)
}

// deliverNudge re-reads the live tmux panes immediately before sending (TOCTOU),
// confirms the recorded pane id STILL belongs to the recipient, and pushes the
// hint only when its agent is running. The recorded id alone is not enough:
// tmux reuses %N ids after a server restart, so an id-only check could nudge an
// unrelated pane that inherited the id — exactly the interruption accident the
// gate must avoid. matchLivePane applies the same id+worktree liveness check the
// dashboard uses (sessionview.paneAlive). It returns the observed agent state, a
// reason when nothing was sent, and whether the push went out; every miss
// (tmux down, pane gone/reused, agent not running, send failure) is a
// best-effort no-op because the message is already persisted.
func deliverNudge(pane state.Pane, deps Deps) (agentState, reason string, nudged bool) {
	panes, err := deps.ListLivePanes()
	if err != nil {
		return "", "tmux is unavailable", false
	}
	lp, ok := matchLivePane(panes, pane.PaneID, pane.WorktreePath)
	if !ok {
		return "", "recipient pane is gone or its id was reused", false
	}
	if !shouldNudge(lp.AgentState) {
		return lp.AgentState, fmt.Sprintf("agent is not running (state %q)", lp.AgentState), false
	}
	if err := deps.SendLine(pane.PaneID, nudgeText); err != nil {
		return lp.AgentState, fmt.Sprintf("send-keys failed: %v", err), false
	}
	return lp.AgentState, "", true
}

// matchLivePane returns the live pane recorded as paneID only when it is still
// the recipient's pane: its id must be live AND it must sit at/under the
// recorded worktree, because tmux reuses %N ids across server restarts. A row
// without a recorded worktree (legacy) falls back to an id-only match. This
// mirrors sessionview.paneAlive — the dashboard's liveness convention — so a
// reused id is treated identically on both surfaces.
func matchLivePane(panes []tmuxrun.LivePane, paneID, worktree string) (tmuxrun.LivePane, bool) {
	if paneID == "" {
		return tmuxrun.LivePane{}, false
	}
	for _, lp := range panes {
		if lp.ID != paneID {
			continue
		}
		if strings.TrimSpace(worktree) == "" {
			return lp, true
		}
		wt := filepath.Clean(worktree)
		cp := filepath.Clean(lp.CurrentPath)
		if cp == wt || strings.HasPrefix(cp, wt+string(filepath.Separator)) {
			return lp, true
		}
		// id matched but the live pane moved off the recorded worktree: tmux
		// handed %N to an unrelated pane. Treat it as gone, never nudge it.
		return tmuxrun.LivePane{}, false
	}
	return tmuxrun.LivePane{}, false
}

func writeMsgNudgeResult(req *Request, parent string, report msgNudgeReport, lg *log.Logger) exitcode.Code {
	if req.JSON {
		return writeMsgJSON(report, lg)
	}
	// Show the recipient as the user addressed it: "#<issue>" for numeric
	// peers, the task id for plan peers (report.Target holds the synthetic
	// number, which is not user-facing).
	label := recipientLabel(req, parent)
	if report.Nudged {
		lg.Ok("nudged %s", label)
	} else {
		lg.Warn("did not nudge %s: %s", label, report.Reason)
	}
	return exitcode.OK
}
