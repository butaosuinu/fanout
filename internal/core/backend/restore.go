package backend

import "time"

// RestoreOps is the optional observation set the console's pane restore needs
// to prove that a recorded pane is still the pane its state row launched, after
// the runtime's process tree may have been replaced wholesale (a killed server,
// a reboot). Restore rebinds, recreates, and rewrites durable state rows, so
// every member is an evidence source rather than a convenience: without them a
// reused pane id cannot be told apart from the original, and fanout must not
// guess.
//
// Absence is the contract, not an omission. A runtime that persists and
// rearranges its own sessions has nothing for restore to repair, so it offers
// no RestoreOps and the console leaves the whole lane unwired instead of
// rebinding rows on evidence it does not have.
type RestoreOps interface {
	// ListLiveForIdentity is the strict pane sweep restore decides on. Unlike
	// Backend.ListLive it must fail rather than degrade a missing or ambiguous
	// identity field to an empty value: a degraded sweep can make a live pane
	// look dead, and restore would then launch a duplicate beside a running
	// agent it can no longer see.
	ListLiveForIdentity() ([]LivePane, error)
	// ListPanes reports one runtime-native target's panes with the display
	// metadata title rebinding matches on. It is target-scoped where
	// ListLiveForIdentity sweeps every session.
	ListPanes(target string) ([]PaneInfo, error)
	// ServerStartTime is the instant the runtime's current process generation
	// started. Pane ids are unique within one generation, so a row recorded at
	// or after this instant still owns the pane id it recorded.
	ServerStartTime() (time.Time, error)
	// PaneStartTime is the instant paneID's root process started: the per-pane
	// half of the same proof. A pane id coincidence across generations cannot
	// also fake a process age matching the row's creation time.
	PaneStartTime(paneID string) (time.Time, error)
	// CanonicalPaneLabel returns the form the runtime stores a border label in.
	// Adoption compares a row's expected label against the one stamped on the
	// live pane, so the caller has to canonicalize its own label exactly the way
	// the runtime did when PaneDecorator.SetPaneLabel wrote it.
	CanonicalPaneLabel(label string) string
}

// AsRestoreOps resolves b's restore-observation capability. ok=false means the
// runtime cannot prove a recorded pane's identity across a restart, so callers
// leave restore unwired rather than acting on unprovable rows.
func AsRestoreOps(b Backend) (RestoreOps, bool) {
	ops, ok := b.(RestoreOps)
	return ops, ok
}
