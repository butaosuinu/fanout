#!/usr/bin/env bats
#
# Tier 1 — flag validation and prerequisite checks.
#
# Locks in the CLI surface identified as Invariant #5 in issue #20: every
# flag's error message and exit code is frozen here so the future Go
# rewrite can target the same contract. No live dmux, no GitHub network —
# every case fails before fanout reaches its external collaborators.
#
# Exit code convention (matches fanout:140-143, 338-342, 356-359):
#   0 = success (help)
#   1 = prerequisite / invocation problem routed through `die` (includes
#       --limit / --sleep / --popup-timeout / --only|--skip validation)
#   2 = positional missing, unknown option, unexpected positional
#
# Issue #20 narrative says "exit 2" for --limit abc et al., but the
# implementation uses `die` (exit 1). Per the invariant "CLI surface is 1:1
# frozen", we keep the implementation's behavior as truth; the issue text
# is an unintentional inconsistency.

load helpers

# --- Help & usage -----------------------------------------------------------

@test "-h prints usage and exits 0" {
  run_fanout -h
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: fanout"* ]]
}

@test "--help prints usage and exits 0" {
  run_fanout --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: fanout"* ]]
}

@test "no positional argument: usage + exit 2" {
  run_fanout
  [ "$status" -eq 2 ]
  [[ "$output" == *"Usage: fanout"* ]]
}

@test "unknown long option: error + usage + exit 2" {
  run_fanout --not-a-flag
  [ "$status" -eq 2 ]
  [[ "$output" == *"unknown option: --not-a-flag"* ]]
  [[ "$output" == *"Usage: fanout"* ]]
}

@test "unexpected positional after parent: error + usage + exit 2" {
  run_fanout 20 extra-word
  [ "$status" -eq 2 ]
  [[ "$output" == *"unexpected positional argument: extra-word"* ]]
}

# --- Positional / required-argument validation -----------------------------

@test "parent must be an integer: exit 1" {
  run_fanout abc
  [ "$status" -eq 1 ]
  [[ "$output" == *"parent issue must be an integer, got: abc"* ]]
}

@test "--agent missing value: exit 1" {
  run_fanout 20 --agent
  [ "$status" -eq 1 ]
  [[ "$output" == *"--agent requires an argument"* ]]
}

@test "--limit missing value: exit 1" {
  run_fanout 20 --limit
  [ "$status" -eq 1 ]
  [[ "$output" == *"--limit requires an argument"* ]]
}

# --- Numeric / regex validation --------------------------------------------

@test "--limit abc: exit 1" {
  run_fanout 20 --limit abc
  [ "$status" -eq 1 ]
  [[ "$output" == *"--limit must be a positive integer, got: abc"* ]]
}

@test "--limit 0 (positive-integer regex rejects zero): exit 1" {
  run_fanout 20 --limit 0
  [ "$status" -eq 1 ]
  [[ "$output" == *"--limit must be a positive integer, got: 0"* ]]
}

@test "--sleep non-numeric: exit 1" {
  run_fanout 20 --sleep X
  [ "$status" -eq 1 ]
  [[ "$output" == *"--sleep must be a number, got: X"* ]]
}

@test "--popup-timeout 0 (positive-integer regex rejects zero): exit 1" {
  run_fanout 20 --popup-timeout 0
  [ "$status" -eq 1 ]
  [[ "$output" == *"--popup-timeout must be a positive integer (seconds), got: 0"* ]]
}

# --- --only / --skip / --include CSV parsing -------------------------------

@test "--only and --skip are mutually exclusive: exit 1" {
  run_fanout 20 --only 4 --skip 5
  [ "$status" -eq 1 ]
  [[ "$output" == *"--only and --skip are mutually exclusive"* ]]
}

@test "--only with non-integer entry: exit 1" {
  run_fanout 20 --only 4,abc
  [ "$status" -eq 1 ]
  [[ "$output" == *"--only: invalid entry 'abc'"* ]]
}

@test "--only with empty entry (double comma): exit 1" {
  run_fanout 20 --only 4,,5
  [ "$status" -eq 1 ]
  [[ "$output" == *"--only: invalid entry ''"* ]]
}

@test "--skip with non-integer entry: exit 1" {
  run_fanout 20 --skip foo
  [ "$status" -eq 1 ]
  [[ "$output" == *"--skip: invalid entry 'foo'"* ]]
}

@test "--include with non-integer entry: exit 1" {
  run_fanout 20 --include bar
  [ "$status" -eq 1 ]
  [[ "$output" == *"--include: invalid entry 'bar'"* ]]
}

# --- --name NUM=<slug-hint>[|<display-name>] --------------------------------

@test "--name: NUM must be a positive integer: exit 1" {
  run_fanout 20 --name foo=bar
  [ "$status" -eq 1 ]
  [[ "$output" == *"--name: <NUM> must be a positive integer"* ]]
}

@test "--name: slug-hint with uppercase rejected: exit 1" {
  run_fanout 20 --name 4=aBc
  [ "$status" -eq 1 ]
  [[ "$output" == *"--name #4: slug-hint must be lowercase kebab-case"* ]]
}

# --- Prerequisite detection (missing_deps[]) --------------------------------
# force_missing rebuilds PATH to exclude the named command(s) while keeping
# jq / awk / grep / ... reachable so fanout can still run its prereq loop.

@test "missing gh: reports gh + gh-sub-issue extension, exit 1" {
  force_missing gh
  run_fanout 20 --agent claude
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing dependencies"* ]]
  [[ "$output" == *"gh (brew install gh)"* ]]
  [[ "$output" == *"gh-sub-issue extension"* ]]
}

@test "missing jq: exit 1" {
  force_missing jq
  run_fanout 20 --agent claude
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing dependencies"* ]]
  [[ "$output" == *"jq (brew install jq)"* ]]
}

@test "missing tmux: exit 1" {
  force_missing tmux
  run_fanout 20 --agent claude
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing dependencies"* ]]
  [[ "$output" == *"tmux (brew install tmux)"* ]]
}

@test "missing pgrep: exit 1" {
  force_missing pgrep
  run_fanout 20 --agent claude
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing dependencies"* ]]
  [[ "$output" == *"pgrep"* ]]
}
