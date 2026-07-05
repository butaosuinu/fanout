// Package cliview holds pure text-formatting primitives for CLI table
// output: column padding, table line assembly, and ANSI color wrapping.
// Color codes arrive as raw start/reset strings so the package carries no
// palette or terminal dependency; widths are measured in bytes, matching
// the ASCII-only status table output.
package cliview

import "strings"

// TableLine joins cols with two-space gaps, padding every column but the
// last to its width.
func TableLine(cols []string, widths []int) string {
	var b strings.Builder
	for i, col := range cols {
		if i > 0 {
			b.WriteString("  ")
		}
		if i == len(cols)-1 {
			b.WriteString(col)
			continue
		}
		b.WriteString(PadRight(col, widths[i]))
	}
	return b.String()
}

// PadRight pads s with trailing spaces to width; s longer than width
// passes through unchanged.
func PadRight(s string, width int) string {
	return s + strings.Repeat(" ", max(0, width-len(s)))
}

// ColorWrap wraps s in the start/reset sequences. An empty color or an
// empty string passes through unchanged so padding math stays exact.
func ColorWrap(start, reset, s string) string {
	if start == "" || s == "" {
		return s
	}
	return start + s + reset
}

// ColorPad color-wraps s and pads to width, excluding the escape
// sequences from the width measurement.
func ColorPad(start, reset, s string, width int) string {
	return ColorWrap(start, reset, s) + strings.Repeat(" ", max(0, width-len(s)))
}

// DashIfEmpty substitutes "-" for an empty cell value.
func DashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
