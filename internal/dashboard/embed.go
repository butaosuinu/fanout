package dashboard

import "embed"

// staticFS holds the embedded single-page dashboard UI (no build step, no npm).
// The files are served read-only under "/" by the server.
//
//go:embed static
var staticFS embed.FS
