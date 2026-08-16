package backend

// Display-only sidebar metadata for the fanout-owned Herdr session. Tokens are
// presentation data: they never carry backend state, liveness, nudge authority,
// or completion. The bracketed report that publishes them lives in infra.

// MaxMetadataTokenValue is Herdr's per-value character limit after trimming and
// control-character removal. Herdr truncates silently, so callers shorten values
// themselves and keep the reported value and the sidebar identical.
const MaxMetadataTokenValue = 80

// MetadataToken is one entry of a fanout-owned token patch. An empty Value
// clears the token: a report always writes fanout's complete token set for a
// resource, so a reused workspace or pane never keeps a stale fanout value.
type MetadataToken struct {
	Name  string
	Value string
}

// MetadataTarget is the exact workspace and pane identity that must be live
// immediately before and after a report.
type MetadataTarget struct {
	WorkspaceID  string
	Label        string
	RepoKey      string
	RepoRoot     string
	CheckoutPath string
	PaneID       string
	TerminalID   string
}

// MetadataReport is one target's token patch. Either patch may be empty, which
// skips that resource; a report with no tokens at all is rejected.
type MetadataReport struct {
	Target          MetadataTarget
	WorkspaceTokens []MetadataToken
	PaneTokens      []MetadataToken
}
