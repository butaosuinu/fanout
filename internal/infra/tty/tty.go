// Package tty mirrors fanout:123 — only emit ANSI color when the destination
// is a real terminal, TERM is not "dumb", and NO_COLOR is unset.
package tty

import (
	"io"
	"os"

	"github.com/charmbracelet/x/term"
)

// IsColorCapable reports whether colored output is appropriate for w.
func IsColorCapable(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if t := os.Getenv("TERM"); t == "" || t == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// IsTerminal reports whether f is an interactive terminal. It asks the
// device itself (isatty) rather than testing the char-device mode bit, which
// /dev/null also carries — a redirected /dev/null must never count as a
// terminal a full-screen client can take over.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(f.Fd())
}
