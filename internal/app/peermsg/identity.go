package peermsg

import (
	"strconv"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/msgstore"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

// resolveMsgIdentity resolves who is speaking (self) and which team DB to use
// (parent). Explicit --self/--parent win per field; missing fields fall back
// to pane detection. peers needs only parent.
func resolveMsgIdentity(req *Request, deps Deps, lg *log.Logger) (int, string, msgstore.Peer, exitcode.Code) {
	self, parent := req.Self, req.Parent
	pane := msgstore.Peer{Issue: self}
	// nudge, like peers, only needs the parent (to scope state.json) — it
	// targets a peer, never speaks as self — so it does not require self to be
	// detectable.
	needSelf := req.Verb != "peers" && req.Verb != "nudge"
	// An explicit --self task id (SelfRaw) is "self given" too; it is resolved
	// to a synthetic number below once the parent is known.
	haveSelf := req.Self != 0 || req.SelfRaw != ""
	if (needSelf && !haveSelf) || parent == "" {
		id, err := deps.DetectIdentity()
		if err != nil {
			lg.Err("msg %s: could not detect this pane (%v); pass %s", req.Verb, err, msgMissingIdentityFlags(needSelf && !haveSelf, parent == ""))
			return 0, "", msgstore.Peer{}, exitcode.Invocation
		}
		if !haveSelf {
			self = id.Issue
			pane = msgstore.Peer{
				Issue:        id.Issue,
				TaskID:       id.TaskID,
				PaneID:       id.Pane.PaneID,
				Slug:         id.Pane.Slug,
				WorktreePath: id.Pane.WorktreePath,
				Agent:        id.Pane.Agent,
				DisplayName:  id.Pane.DisplayName,
			}
		}
		if parent == "" {
			parent = id.Parent
		}
	}
	if parent == "" {
		lg.Err("msg %s: parent is unknown; pass --parent <ref>", req.Verb)
		return 0, "", msgstore.Peer{}, exitcode.Invocation
	}
	// An explicit --self resolves against the now-known parent: a task id under
	// a plan parent, a non-zero issue number otherwise.
	if req.SelfRaw != "" {
		n, code := resolveMemberNum(req.Verb, "--self", req.SelfRaw, req.Self, parent, lg)
		if code != exitcode.OK {
			return 0, "", msgstore.Peer{}, code
		}
		self, pane.Issue = n, n
		if team.IsPlanParent(parent) {
			pane.TaskID = req.SelfRaw
		} else {
			pane.TaskID = ""
		}
	}
	// Negative self is legitimate: manual panes carry synthetic numbers
	// (-1, -2, ...) under the @manual parent, and plan tasks carry synthetic
	// negatives under plan:<slug>. Only zero means unknown.
	if needSelf && self == 0 {
		lg.Err("msg %s: self issue is unknown; pass --self <issue|task-id>", req.Verb)
		return 0, "", msgstore.Peer{}, exitcode.Invocation
	}
	// Resolve the --to/nudge recipient now that the parent is known: an
	// all-digit token routes to a task id under a plan parent, an issue number
	// otherwise.
	if req.ToRaw != "" {
		n, code := resolveMemberNum(req.Verb, recipientFlagLabel(req.Verb), req.ToRaw, req.To, parent, lg)
		if code != exitcode.OK {
			return 0, "", msgstore.Peer{}, code
		}
		req.To = n
	}
	return self, parent, pane, exitcode.OK
}

// msgMissingIdentityFlags names only the flags the user actually still owes,
// so `--self 70` without --parent is told about --parent alone.
func msgMissingIdentityFlags(needSelf, needParent bool) string {
	switch {
	case needSelf && needParent:
		return "--self <issue|task-id> and --parent <ref>"
	case needSelf:
		return "--self <issue|task-id>"
	default:
		return "--parent <ref>"
	}
}

// resolveMemberNum resolves a member token (raw + its parsed int) to the peer
// number for the now-known parent. Under a plan parent the token is a task id
// (even an all-digit one), mapped through TaskPeerNum; under an issue/Project
// parent it must be a non-zero integer. This is the single seam where the
// int-vs-task-id ambiguity of an all-digit token is decided.
func resolveMemberNum(verb, flag, raw string, intVal int, parent string, lg *log.Logger) (int, exitcode.Code) {
	if team.IsPlanParent(parent) {
		if !team.TaskIDRE.MatchString(raw) {
			lg.Err("msg %s: under a plan parent, %s must be a task id (lowercase kebab-case), got: %s", verb, flag, raw)
			return 0, exitcode.Invocation
		}
		return team.TaskPeerNum(parent, raw), exitcode.OK
	}
	if intVal == 0 {
		// The CLI parser only yields intVal 0 for a task-id-shaped token, so a
		// 0 here means a task id was passed under a numeric parent.
		lg.Err("msg %s: %s %q is a task id, only valid under a plan parent; issue/project peers are addressed by number", verb, flag, raw)
		return 0, exitcode.Invocation
	}
	return intVal, exitcode.OK
}

// recipientLabel renders the recipient as the user addressed it: the task id
// under a plan parent, "#<issue>" otherwise. req.To must already be resolved.
func recipientLabel(req *Request, parent string) string {
	if team.IsPlanParent(parent) {
		return req.ToRaw
	}
	return "#" + strconv.Itoa(req.To)
}

// recipientFlagLabel names the recipient flag for error messages: nudge's
// positional "target", send's "--to".
func recipientFlagLabel(verb string) string {
	if verb == "nudge" {
		return "target"
	}
	return "--to"
}
