package team

import (
	"hash/fnv"
	"regexp"
	"strings"
)

// TaskIDRE is the plan task id shape: lowercase kebab-case starting with an
// alphanumeric. It is the single definition shared by the `fanout plan` /
// `fanout msg` CLI parsers (cmd/fanout) and the msg execution layer
// (internal/app/peermsg), which must agree on what counts as a task id.
var TaskIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// planParentPrefix marks a parent ref scoped to an issue-less `fanout plan`
// run (cmd/fanout planParentRef builds "plan:<slug>"). Plan tasks are
// identified by a string task id, not a numeric issue, so the messaging layer
// — which is keyed by int — addresses them through a stable synthetic number
// derived from the parent-scoped task id.
const planParentPrefix = "plan:"

// IsPlanParent reports whether parentRef names a `fanout plan` run.
func IsPlanParent(parentRef string) bool {
	return strings.HasPrefix(parentRef, planParentPrefix)
}

// TaskPeerNum maps a plan task to a stable, negative synthetic peer number so
// it participates in the int-keyed peers/messages tables alongside numeric
// issue panes. It is deterministic in (parentRef, taskID): the registry seed
// (registry.go), pane self-detection (detect.go), and the
// `fanout msg --to <task-id>` translation (cmd/fanout/msg.go) all derive the
// same number for a given task. The value is always < 0 so it never collides
// with the 0 "unknown" sentinel the msg layer rejects. The 63-bit fnv64a fold
// over parent + NUL + task id makes collisions between two task ids in one
// plan astronomically unlikely (a 31-bit fold could collide within a single
// plan's task set, which would alias their peers/inbox rows). int is 64-bit on
// every platform fanout targets, so a 63-bit magnitude fits without overflow.
func TaskPeerNum(parentRef, taskID string) int {
	h := fnv.New64a()
	_, _ = h.Write([]byte(parentRef))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(taskID))
	return -int(h.Sum64()&0x7fffffffffffffff) - 1
}
