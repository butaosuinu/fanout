// Package log mirrors the fanout:121-144 helpers. Exact prefixes and
// stdout/stderr routing are locked in by Tier 1/2 tests.
package log

import (
	"fmt"
	"io"
	"os"

	"github.com/butaosuinu/fanout/internal/tty"
)

// Logger captures the destinations and color state once at startup so we don't
// reprobe TTY state on every call.
type Logger struct {
	out    io.Writer
	err    io.Writer
	color  bool
	debug  bool
	cInfo  string
	cOk    string
	cWarn  string
	cErr   string
	cDim   string
	cReset string
}

// New constructs a Logger that writes to stdout/stderr and detects color.
func New(debug bool) *Logger {
	return NewWith(os.Stdout, os.Stderr, debug)
}

// NewWith constructs a Logger with explicit destinations (for tests).
func NewWith(out, err io.Writer, debug bool) *Logger {
	l := &Logger{out: out, err: err, color: tty.IsColorCapable(out), debug: debug}
	if l.color {
		// Match bash tput colors: blue, green, yellow, red, dim.
		l.cInfo = "\x1b[34m"
		l.cOk = "\x1b[32m"
		l.cWarn = "\x1b[33m"
		l.cErr = "\x1b[31m"
		l.cDim = "\x1b[2m"
		l.cReset = "\x1b[0m"
	}
	return l
}

func (l *Logger) Info(format string, a ...any) {
	fmt.Fprintf(l.out, "%s[info]%s %s\n", l.cInfo, l.cReset, fmt.Sprintf(format, a...))
}

func (l *Logger) Ok(format string, a ...any) {
	fmt.Fprintf(l.out, "%s[ ok ]%s %s\n", l.cOk, l.cReset, fmt.Sprintf(format, a...))
}

func (l *Logger) Warn(format string, a ...any) {
	fmt.Fprintf(l.err, "%s[warn]%s %s\n", l.cWarn, l.cReset, fmt.Sprintf(format, a...))
}

func (l *Logger) Err(format string, a ...any) {
	fmt.Fprintf(l.err, "%s[err ]%s %s\n", l.cErr, l.cReset, fmt.Sprintf(format, a...))
}

// Dim writes a dim-styled line to stdout (no level prefix).
func (l *Logger) Dim(format string, a ...any) {
	fmt.Fprintf(l.out, "%s%s%s\n", l.cDim, fmt.Sprintf(format, a...), l.cReset)
}

func (l *Logger) Debug(format string, a ...any) {
	if !l.debug {
		return
	}
	fmt.Fprintf(l.err, "%s[dbg ]%s %s\n", l.cDim, l.cReset, fmt.Sprintf(format, a...))
}

// Die writes the message via Err and exits 1.
func (l *Logger) Die(format string, a ...any) {
	l.Err(format, a...)
	os.Exit(1)
}

// Stdout / Stderr expose the writers for ad-hoc fmt.Fprintf calls (used by
// dry-run printer to render escape-sequence-aware indented blocks).
func (l *Logger) Stdout() io.Writer { return l.out }
func (l *Logger) Stderr() io.Writer { return l.err }

// Colors returns the active color escape codes plus reset, in the order:
// info, ok, warn, err, dim, reset. Empty strings when color is off.
func (l *Logger) Colors() (info, ok, warn, errc, dim, reset string) {
	return l.cInfo, l.cOk, l.cWarn, l.cErr, l.cDim, l.cReset
}
