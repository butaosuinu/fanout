// Package exitcode centralizes the three CLI exit codes fanout uses.
// Behavior locked in by tests/bats/tier1_flags.bats; see fanout:113-117.
package exitcode

type Code int

const (
	OK         Code = 0
	Env        Code = 1
	Invocation Code = 2
)
