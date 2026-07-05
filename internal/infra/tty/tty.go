// Package tty mirrors fanout:123 — only emit ANSI color when the destination
// is a real terminal, TERM is not "dumb", and NO_COLOR is unset.
package tty

import (
	"io"
	"os"
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
