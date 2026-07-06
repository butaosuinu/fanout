// Package parentref canonicalizes fanout parent references. Numeric parents
// are normalized by an Atoi round-trip so "0100" and "100" identify the same
// parent; every non-numeric parent (Projects URL, plan:<slug>, @manual) passes
// through unchanged. state, sessionview, the TUI, and cliflags all key rows by
// this rule — if it splits, alias parents produce duplicate or missing rows.
package parentref

import "strconv"

// Canon returns the canonical form of parent: numeric strings lose leading
// zeros via an Atoi round-trip, anything Atoi rejects (including out-of-range
// digit strings) is returned unchanged.
func Canon(parent string) string {
	if n, err := strconv.Atoi(parent); err == nil {
		return strconv.Itoa(n)
	}
	return parent
}
