// Package log mirrors the fanout:121-144 helpers. Exact prefixes and
// stdout/stderr routing are locked in by Tier 1/2 tests.
package log

import (
	"fmt"
	"io"
	"os"

	"github.com/butaosuinu/fanout/internal/tty"
)

type Logger struct {
	out     io.Writer
	err     io.Writer
	color   bool
	debug   bool
	palette Palette
}

// Palette holds the active ANSI escape codes (or empty strings when color is
// off). Returned by Logger.Colors so callers can render compound output (the
// dry-run printer needs raw escapes around adjacent unstyled text).
type Palette struct {
	Info, Ok, Warn, Err, Dim, Reset string
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
		l.palette = Palette{
			Info:  "\x1b[34m",
			Ok:    "\x1b[32m",
			Warn:  "\x1b[33m",
			Err:   "\x1b[31m",
			Dim:   "\x1b[2m",
			Reset: "\x1b[0m",
		}
	}
	return l
}

func (l *Logger) Info(format string, a ...any) {
	fmt.Fprintf(l.out, "%s[info]%s %s\n", l.palette.Info, l.palette.Reset, fmt.Sprintf(format, a...))
}

func (l *Logger) Ok(format string, a ...any) {
	fmt.Fprintf(l.out, "%s[ ok ]%s %s\n", l.palette.Ok, l.palette.Reset, fmt.Sprintf(format, a...))
}

func (l *Logger) Warn(format string, a ...any) {
	fmt.Fprintf(l.err, "%s[warn]%s %s\n", l.palette.Warn, l.palette.Reset, fmt.Sprintf(format, a...))
}

func (l *Logger) Err(format string, a ...any) {
	fmt.Fprintf(l.err, "%s[err ]%s %s\n", l.palette.Err, l.palette.Reset, fmt.Sprintf(format, a...))
}

// Dim writes a dim-styled line to stdout (no level prefix).
func (l *Logger) Dim(format string, a ...any) {
	fmt.Fprintf(l.out, "%s%s%s\n", l.palette.Dim, fmt.Sprintf(format, a...), l.palette.Reset)
}

func (l *Logger) Debug(format string, a ...any) {
	if !l.debug {
		return
	}
	fmt.Fprintf(l.err, "%s[dbg ]%s %s\n", l.palette.Dim, l.palette.Reset, fmt.Sprintf(format, a...))
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

// Colors returns the active palette. Empty strings when color is off.
func (l *Logger) Colors() Palette { return l.palette }
