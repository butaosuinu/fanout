package dashboard

import "embed"

// staticFS holds the embedded dashboard UI bundle, built from web/ by Vite
// (`make build-web`) into static/. The build output is not committed — only
// static/.gitkeep is tracked, and the `all:` prefix makes that dotfile match
// so a fresh checkout compiles without Node (the server then serves a
// "run make build-web" fallback page instead of the SPA).
//
//go:embed all:static
var staticFS embed.FS
