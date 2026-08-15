package backend

// The capabilities a process uses about the pane it is itself running in.
// fanout's self-exec controllers — the Codex Plan Mode controller and the Codex
// team bridge — are already inside a pane when they need these, so they resolve
// the runtime that created that pane rather than the ambient launch selection:
// no other runtime can answer for it.

// AgentStateReporter is an optional capability for runtimes that let the
// process inside a pane publish that pane's agent state on the pane itself. It
// is the self-reported half of the agent-state contract: the launch wrapper and
// the launch-time hooks write the same value from outside, while a controller
// that owns the pane's foreground process writes it from within.
//
// state is the raw value rather than AgentState because it is written verbatim:
// this is display telemetry, and normalizing (or dropping) an unrecognized
// value belongs to the reader, not to the pane publishing it.
type AgentStateReporter interface {
	SetPaneAgentState(paneID, state string) error
}

// PlanCapture is an optional capability for runtimes that return everything a
// pane rendered as one machine-readable transcript. It is deliberately separate
// from Backend.Read: Read answers "what is on this pane's screen now" for a
// human peek, while this answers "everything this pane rendered, unwrapped and
// including the alternate screen" for a parser looking for a plan block — the
// two capture differently, and collapsing them would silently degrade one.
type PlanCapture interface {
	CapturePlanSource(paneID string, lines int) (string, error)
}

// AsAgentStateReporter resolves b's self-reported agent-state capability.
// ok=false means the runtime accepts no state from inside a pane, so callers
// drop the telemetry instead of failing: it is display-only.
func AsAgentStateReporter(b Backend) (AgentStateReporter, bool) {
	reporter, ok := b.(AgentStateReporter)
	return reporter, ok
}

// AsPlanCapture resolves b's plan-transcript capability. ok=false means the
// runtime cannot hand back what a pane rendered, so callers fall back to the
// route their own launch recorded rather than reading through this process.
func AsPlanCapture(b Backend) (PlanCapture, bool) {
	capture, ok := b.(PlanCapture)
	return capture, ok
}
