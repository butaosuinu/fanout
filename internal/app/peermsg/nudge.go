package peermsg

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/core/agent"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

// nudgeText is the hint `msg nudge` types into a peer pane. It carries no
// message body on purpose — only a pointer to the inbox — so the actual
// content stays in the SQLite DB and the pane never displays a peer's words
// out of context. The em dash is intentional; it is a stable UTF-8 literal.
const nudgeText = "[fanout] peer message in your inbox — run: fanout msg inbox"

// msgNudgeReport is the --json encoding of a nudge attempt. Nudged is true only
// when the runtime send actually went out; Reason explains every other outcome so
// automation can tell a delivered push from a best-effort no-op.
type msgNudgeReport struct {
	Target     int    `json:"target"`
	TargetTask string `json:"targetTask,omitempty"`
	PaneID     string `json:"pane_id"`
	AgentState string `json:"agent_state"`
	Nudged     bool   `json:"nudged"`
	Reason     string `json:"reason"`
}

// shouldNudge reports whether a pane in the given refined agent state should
// receive a nudge. Every state in which the agent can safely take
// queued input qualifies: "idle" (at its prompt, the ideal moment), "running"
// (wrapper state, granularity unknown), and the hook states "working" and
// "plan" — the supported agents (claude, codex) queue typed input during a
// turn rather than aborting it, so a mid-turn hint is picked up at the next
// checkpoint. With hooks the agent spends most of a turn in "working", so
// excluding it would make nudge a near no-op in practice. "blocked" is the
// deliberate exception: the pane is showing a permission dialog and the
// nudge's trailing Enter could press the focused button. "done" (agent
// exited, bare shell), "" (unset: legacy or non-fanout pane), and any other
// value are no-op successes so a hint never lands in a shell or an unrelated
// pane. The raw option value is trimmed with the same tolerance as
// sessionview's normalizeAgentState, so the nudge gate and the dashboards
// never disagree about a padded hook value.
func shouldNudge(agentState string) bool {
	switch strings.TrimSpace(agentState) {
	case "running", "working", "plan", "idle":
		return true
	default:
		return false
	}
}

// runMsgNudge resolves peer req.To's recorded pane from state.json and pushes
// the inbox hint when its agent can take queued input (running / working /
// plan / idle). Every operational miss (recipient absent, no pane id, pane
// gone, agent not nudgeable, runtime send failure) is a no-op SUCCESS with a
// warning/reason: the message is already persisted by send, so a failed nudge
// must never break messaging. Only invocation errors (handled earlier) exit
// non-zero.
func runMsgNudge(req *Request, parent string, deps Deps, lg *log.Logger) exitcode.Code {
	st, err := deps.LoadState()
	if err != nil {
		lg.Err("msg nudge: %v", err)
		return exitcode.Invocation
	}
	// Plan recipients are addressed by task id (state rows carry IssueNum 0),
	// while issue/Project recipients use the numeric key. A duplicate logical
	// recipient is ambiguous even when one row has a unique runtime binding.
	pane, matches := uniqueNudgeRecipient(st, parent, req.To, req.ToRaw)
	report := msgNudgeReport{Target: req.To}
	if team.IsPlanParent(parent) {
		report.TargetTask = req.ToRaw
	}
	switch {
	case matches == 0:
		report.Reason = "recipient is not recorded in fanout state"
	case matches != 1:
		report.Reason = "recipient identity is ambiguous in fanout state"
	case pane.PaneID == "":
		report.Reason = "recipient has no recorded pane"
	default:
		report.PaneID = pane.PaneID
		report.AgentState, report.Reason, report.Nudged = deliverRuntimeNudge(pane, deps)
	}
	return writeMsgNudgeResult(req, parent, report, lg)
}

func uniqueNudgeRecipient(store state.Store, parent string, issueNum int, taskID string) (state.Pane, int) {
	var matched state.Pane
	count := 0
	for _, pane := range store.PanesForParent(parent) {
		isMatch := pane.IssueNum == issueNum
		if team.IsPlanParent(parent) {
			isMatch = taskID != "" && pane.TaskID == taskID
		}
		if isMatch {
			matched = pane
			count++
		}
	}
	return matched, count
}

func deliverRuntimeNudge(pane state.Pane, deps Deps) (agentState, reason string, nudged bool) {
	if backend.NormalizeName(pane.Backend) == backend.Herdr {
		if !agent.PaneStateRefined(pane.Agent) {
			return "", fmt.Sprintf("agent %q has no agent-state refinement; nudge is disabled for its panes", pane.Agent), false
		}
		return deliverHerdrNudge(pane, deps)
	}
	return deliverNudge(pane, deps)
}

// deliverNudge re-reads the live tmux panes immediately before sending (TOCTOU),
// confirms the recorded pane id STILL belongs to the recipient, and pushes the
// hint only when its agent can take queued input. The recorded id alone is not enough:
// tmux reuses %N ids after a server restart, so an id-only check could nudge an
// unrelated pane that inherited the id — exactly the interruption accident the
// gate must avoid. matchLivePane applies the same id+worktree liveness check the
// dashboard uses (sessionview.paneAlive). It returns the observed agent state, a
// reason when nothing was sent, and whether the push went out; every miss
// (tmux down, pane gone/reused, agent not nudgeable, send failure) is a
// best-effort no-op because the message is already persisted.
func deliverNudge(pane state.Pane, deps Deps) (agentState, reason string, nudged bool) {
	ref := paneRef(pane)
	if backend.NormalizeName(ref.Backend) != backend.Tmux {
		return "", fmt.Sprintf("automatic nudge is unavailable for %s backend", backend.NormalizeName(ref.Backend)), false
	}
	// An agent without @fanout_agent_state refinement (opencode, future
	// agents) stays "running" even while a permission dialog is focused, and
	// the nudge's trailing Enter could press the dialog's button. Exclude its
	// panes; the message stays readable via inbox/board regardless.
	if !agent.PaneStateRefined(pane.Agent) {
		return "", fmt.Sprintf("agent %q has no agent-state refinement; nudge is disabled for its panes", pane.Agent), false
	}
	if deps.ListLive == nil || deps.SendLine == nil {
		return "", "tmux is unavailable", false
	}
	panes, err := deps.ListLive()
	if err != nil {
		return "", "tmux is unavailable", false
	}
	lp, ok := matchLivePane(panes, ref, pane.WorktreePath, pane.ShellKey)
	if !ok {
		return "", "recipient pane is gone or its id was reused", false
	}
	observedState := string(lp.AgentState)
	if !shouldNudge(observedState) {
		// blocked の agent は生きている — "not running" とは言わず、nudge を
		// 意図的に見送ったことだけを述べる。
		return observedState, fmt.Sprintf("agent is not nudgeable (state %q)", observedState), false
	}
	if err := deps.SendLine(lp.Ref, nudgeText); err != nil {
		return observedState, fmt.Sprintf("send-keys failed: %v", err), false
	}
	return observedState, "", true
}

// matchLivePane returns the live pane for the backend-native reference only
// when it is still the recipient's pane. Keyed tmux rows require the per-pane
// liveness token; legacy rows fall back to their recorded worktree or id-only.
// This mirrors sessionview's liveness convention so pane-id reuse is handled
// identically before nudge sends terminal input.
func matchLivePane(panes []backend.LivePane, ref backend.PaneRef, worktree, livenessKey string) (backend.LivePane, bool) {
	if ref.Pane == "" {
		return backend.LivePane{}, false
	}
	name := backend.NormalizeName(ref.Backend)
	for _, lp := range panes {
		if backend.NormalizeName(lp.Ref.Backend) != name || lp.Ref.Pane != ref.Pane {
			continue
		}
		if ref.Workspace != "" && lp.Ref.Workspace != ref.Workspace {
			return backend.LivePane{}, false
		}
		if livenessKey = strings.TrimSpace(livenessKey); livenessKey != "" {
			if lp.ShellKey == livenessKey {
				return lp, true
			}
			return backend.LivePane{}, false
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
		return backend.LivePane{}, false
	}
	return backend.LivePane{}, false
}

func paneRef(pane state.Pane) backend.PaneRef {
	name := backend.NormalizeName(pane.Backend)
	ref := backend.PaneRef{Backend: name, Pane: pane.PaneID}
	if name == backend.Herdr {
		ref.Workspace = pane.WorkspaceID
	}
	return ref
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
