// Package exitcode centralizes the CLI exit codes fanout uses.
// Behavior locked in by tests/bats/tier1_flags.bats; see fanout:113-117.
// Backend is the `fanout msg` contract (tests/bats/tier1_msg.bats): the
// team SQLite DB could not be opened, migrated, or queried.
package exitcode

type Code int

const (
	OK         Code = 0
	Env        Code = 1
	Invocation Code = 2
	GitHub     Code = 3
	Backend    Code = 4
)
