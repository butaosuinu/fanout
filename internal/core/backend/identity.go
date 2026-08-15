package backend

// How a runtime identifies one of its live panes. A read-only aggregator holds
// a persisted row on one side and a live observation on the other, and it has
// to decide whether they are the same pane. The two questions it asks — how far
// a pane identifier reaches, and what evidence an observation carries — are
// properties of the runtime, so they are named here rather than re-derived from
// the runtime's name at every read surface.

// LiveIdentityModel names the evidence a runtime's live observation carries for
// deciding whether a recorded row is still the pane it was launched on.
type LiveIdentityModel int

const (
	// LiveIdentityUnrecognized is the zero value, returned for any runtime
	// fanout does not admit. A row on such a runtime matches nothing: there is
	// no evidence to compare, and adopting a live pane on the strength of a
	// bare identifier would bind the row to a stranger.
	LiveIdentityUnrecognized LiveIdentityModel = iota

	// LiveIdentityForegroundPath is for runtimes whose observation reports only
	// what the pane is doing right now — its foreground working directory and,
	// for a shell pane, the key fanout stamped on it. A row matches when the
	// observed path is its own checkout or a directory beneath it, because an
	// agent may descend into a subdirectory while it works.
	LiveIdentityForegroundPath

	// LiveIdentityRecordedBinding is for runtimes whose observation carries the
	// full launch record — route, workspace label, terminal, agent record and
	// checkout provenance. The row's persisted binding is then compared
	// verbatim: every component must agree, and a component the row never
	// recorded is not evidence and cannot be filled in from the observation.
	LiveIdentityRecordedBinding
)

// LiveIdentityModelOf reports the identity model of the named runtime.
func LiveIdentityModelOf(name Name) LiveIdentityModel {
	switch NormalizeName(name) {
	case Tmux:
		return LiveIdentityForegroundPath
	case Herdr:
		return LiveIdentityRecordedBinding
	default:
		return LiveIdentityUnrecognized
	}
}

// RouteScopedPaneIDs reports whether the named runtime's pane identifiers are
// unique only within one observation route — its workspace, named session and
// socket — rather than across everything fanout can observe at once. Aggregated
// reads span several routes, so an identifier from a route-scoped runtime must
// be qualified by that route before it can key a live-pane index; a runtime
// with a single process-wide route needs no qualifier and carries none.
func RouteScopedPaneIDs(name Name) bool {
	return NormalizeName(name) == Herdr
}
