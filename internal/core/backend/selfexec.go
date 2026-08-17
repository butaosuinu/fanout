package backend

import "io"

// SelfExecEntry is one hidden subcommand the fanout binary must answer when a
// runtime backend re-executes it — a Herdr pane shell or an owned-server
// supervisor. The runtime bakes the invocation into a process it spawns, so the
// composition root cannot know the tokens: it only iterates the registry the
// runtime layer publishes and keeps the first match wins.
type SelfExecEntry struct {
	// Name identifies the entry in tests and diagnostics. It is not the
	// dispatch token; Match owns recognition because some entries are selected
	// by an inherited environment variable rather than by argv.
	Name string
	// Match receives argv without the program name, exactly as the surrounding
	// dispatch table sees it.
	Match func(args []string) bool
	// Run receives the process streams plus argv after the subcommand token and
	// returns the process exit status.
	Run func(in io.Reader, out, errw io.Writer, args []string) int
}
