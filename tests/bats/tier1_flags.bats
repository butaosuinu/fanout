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

@test "parent must be an integer or Projects v2 URL: exit 1" {
  run_fanout abc
  [ "$status" -eq 1 ]
  [[ "$output" == *"parent must be an integer issue number or Projects v2 URL, got: abc"* ]]
}

@test "parent URL with wrong host rejected: exit 1" {
  run_fanout https://example.com/users/foo/projects/3
  [ "$status" -eq 1 ]
  [[ "$output" == *"parent must be an integer issue number or Projects v2 URL"* ]]
}

@test "parent URL missing project number rejected: exit 1" {
  run_fanout https://github.com/users/foo/projects/
  [ "$status" -eq 1 ]
  [[ "$output" == *"parent must be an integer issue number or Projects v2 URL"* ]]
}

@test "--project-status missing value: exit 1" {
  run_fanout 20 --project-status
  [ "$status" -eq 1 ]
  [[ "$output" == *"--project-status requires an argument"* ]]
}

# Project URL parser must accept the canonical links users copy from GitHub
# Projects: the bare /projects/<n>, /projects/<n>/views/<id>, and either
# form with a ?query suffix. We only assert the parser does not reject
# them — downstream stages (tmux/gh) fail in the test env but that's not
# the parser's concern.
@test "Projects URL bare form is accepted by parser" {
  run_fanout 'https://github.com/users/foo/projects/3'
  [[ "$output" != *"parent must be"* ]]
}

@test "Projects URL with /views/<id> is accepted by parser" {
  run_fanout 'https://github.com/orgs/bar/projects/7/views/1'
  [[ "$output" != *"parent must be"* ]]
}

@test "Projects URL with query string is accepted by parser" {
  run_fanout 'https://github.com/users/foo/projects/3?filterQuery=is%3Aopen'
  [[ "$output" != *"parent must be"* ]]
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

# --- --name NUM=<slug-hint>[|<display-name>[|<branch-name>]] ---------------

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

@test "--name: all three segments empty (NUM=||) rejected: exit 1" {
  run_fanout 20 --name 4=||
  [ "$status" -eq 1 ]
  [[ "$output" == *"--name #4: slug-hint, display-name, and branch-name are all empty"* ]]
}

@test "--name: more than 3 '|'-separated segments rejected: exit 1" {
  run_fanout 20 --name 4=slug\|disp\|branch\|extra
  [ "$status" -eq 1 ]
  [[ "$output" == *"too many '|'-separated segments"* ]]
}

@test "--name: branch-name containing whitespace rejected: exit 1" {
  run_fanout 20 --name "4=slug|disp|bad branch"
  [ "$status" -eq 1 ]
  [[ "$output" == *"--name #4: branch-name must not contain whitespace"* ]]
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

# --- --status CLI surface ---------------------------------------------------
# --status uses its own exit-code lane (0/2/3) per issue #35: 0=JSON emitted,
# 2=cannot enumerate (bad invocation, missing config, no dmux session),
# 3=gh API failure. The cases below cover the CLI surface only — the body /
# JSON shape lives in tier2_status.bats against fixtures.

@test "--status without parent: exit 2" {
  run_fanout --status
  [ "$status" -eq 2 ]
  [[ "$output" == *"Usage: fanout"* ]]
}

@test "--status non-integer parent: exit 2" {
  run_fanout --status abc
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status: parent must be an integer issue number or Projects v2 URL"* ]]
}

@test "--status with Projects v2 URL parent: exit 2" {
  # --status only makes sense for integer-issue parents (panes carry
  # `[fanout #N of #<issue-number>]`). Projects URLs are not addressable
  # via that prefix, so the combination is rejected up-front.
  run_fanout --status 'https://github.com/users/foo/projects/3'
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with Projects v2 URLs as parent"* ]]
}

@test "--status conflicts with --agent: exit 2" {
  run_fanout --status 1 --agent claude
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --agent"* ]]
}

@test "--status conflicts with --limit: exit 2" {
  run_fanout --status 1 --limit 3
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --limit"* ]]
}

@test "--status conflicts with --only: exit 2" {
  run_fanout --status 1 --only 4
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --only"* ]]
}

@test "--status conflicts with --skip: exit 2" {
  run_fanout --status 1 --skip 4
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --skip"* ]]
}

@test "--status conflicts with --include: exit 2" {
  run_fanout --status 1 --include 4
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --include"* ]]
}

@test "--status conflicts with --dry-run: exit 2" {
  run_fanout --status 1 --dry-run
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --dry-run"* ]]
}

@test "--status conflicts with --unblocked-only: exit 2" {
  run_fanout --status 1 --unblocked-only
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --unblocked-only"* ]]
}

@test "--status conflicts with --name: exit 2" {
  run_fanout --status 1 --name 4=foo
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --name"* ]]
}

@test "--status conflicts with --sleep even at default value: exit 2" {
  # Wrappers that always pass `--sleep 4` (the default) must still trigger
  # the exclusivity error. Detection is by flag presence, not value diff.
  run_fanout --status 1 --sleep 4
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --sleep"* ]]
}

@test "--status conflicts with --popup-timeout even at default value: exit 2" {
  run_fanout --status 1 --popup-timeout 20
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --popup-timeout"* ]]
}

@test "--status with bogus --sleep value: exclusivity wins (exit 2), not numeric die (exit 1)" {
  # Callers branch on --status's dedicated exit codes (0/2/3). The
  # exclusivity check must run before the `--sleep must be a number` die,
  # otherwise an invalid combination leaks through as a main-flow exit 1.
  run_fanout --status 1 --sleep nope
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --sleep"* ]]
}

@test "--status with bogus --popup-timeout value: exclusivity wins (exit 2)" {
  run_fanout --status 1 --popup-timeout zero
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --popup-timeout"* ]]
}

@test "--status with bogus --limit value: exclusivity wins (exit 2)" {
  run_fanout --status 1 --limit abc
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --limit"* ]]
}

@test "--status with bogus --name value: exclusivity wins (exit 2), not parse_name_arg die (exit 1)" {
  # --name normally die's inside parse_name_arg if the slug-hint/NUM is
  # malformed. Combined with --status, the conflict must surface first so
  # callers see the documented --status exit code 2.
  run_fanout --status 1 --name bad
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --name"* ]]
}

@test "--status with bogus --name slug-hint: exclusivity wins (exit 2)" {
  # Same as above but the malformed part is the slug-hint (would normally
  # die with "slug-hint must be lowercase kebab-case").
  run_fanout --status 1 --name 4=BAD
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with --name"* ]]
}

@test "--status with DMUX_CONFIG_PATH does not require tmux: exit 0" {
  # Offline-mode contract: an empty panes config plus DMUX_CONFIG_PATH must
  # let `--status` complete without tmux installed (CI / post-session
  # introspection). With no fanned children, cmd_status emits a zero summary
  # before reaching any gh call.
  force_missing tmux
  local cfg="$BATS_TEST_TMPDIR/dmux.config.json"
  printf '{"panes":[]}\n' > "$cfg"
  export DMUX_CONFIG_PATH="$cfg"
  run_fanout --status 1
  [ "$status" -eq 0 ]
  [[ "$output" == *'"all_merged": false'* ]]
  [[ "$output" == *'"total": 0'* ]]
}

@test "--status without DMUX_CONFIG_PATH still requires tmux: exit 1" {
  # Regression guard: the offline-mode escape hatch above must not
  # accidentally mask the missing-tmux error for the live-discovery path.
  force_missing tmux
  run_fanout --status 1
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing dependencies"* ]]
  [[ "$output" == *"tmux"* ]]
}

@test "--status with malformed JSON config: exit 2" {
  # Contract: an unparseable dmux.config.json must surface as exit 2 with a
  # clear message. Without this, jq's raw exit code (5) or a silent
  # "total: 0" misreport would slip through and break wait-and-continue
  # automation that polls --status.
  force_missing tmux
  local cfg="$BATS_TEST_TMPDIR/dmux.config.json"
  printf '{"panes": [bogus]\n' > "$cfg"
  export DMUX_CONFIG_PATH="$cfg"
  run_fanout --status 1
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status: dmux config at"* ]]
  [[ "$output" == *"not valid JSON or .panes is not an array"* ]]
}

@test "--status with non-array .panes: exit 2" {
  # JSON is valid but the schema is wrong (.panes is a string, not an array).
  # Catch this up-front so enumerate_fanned_children's lenient mode can't turn
  # it into a silent "no fanned children" report.
  force_missing tmux
  local cfg="$BATS_TEST_TMPDIR/dmux.config.json"
  printf '{"panes":"oops"}\n' > "$cfg"
  export DMUX_CONFIG_PATH="$cfg"
  run_fanout --status 1
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status: dmux config at"* ]]
  [[ "$output" == *"not valid JSON or .panes is not an array"* ]]
}

@test "--status with valid JSON missing .panes field: exit 0 with empty children" {
  # `.panes` may be absent on a freshly-initialized config; that's a valid
  # state, not an error. Treat as zero fanned children.
  force_missing tmux
  local cfg="$BATS_TEST_TMPDIR/dmux.config.json"
  printf '{}\n' > "$cfg"
  export DMUX_CONFIG_PATH="$cfg"
  run_fanout --status 1
  [ "$status" -eq 0 ]
  [[ "$output" == *'"total": 0'* ]]
  [[ "$output" == *'"all_merged": false'* ]]
}

@test "--status with DMUX_CONFIG_PATH whose projectRoot is missing: exit 2 (not 3)" {
  # Stale / wrong project_root in the config must be reported as an
  # enumeration problem (exit 2), not deferred until each gh_in_root cd
  # fails and reports exit 3 per child. The --status contract documents
  # 2 = "cannot enumerate / unusable config".
  force_missing tmux
  local cfg="$BATS_TEST_TMPDIR/dmux.config.json"
  printf '{"panes":[],"projectRoot":"/does/not/exist/anywhere"}\n' > "$cfg"
  export DMUX_CONFIG_PATH="$cfg"
  run_fanout --status 1
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status: project_root is not a directory"* ]]
}

@test "--status normalizes leading zeros in parent argument" {
  # Wrappers that pass IDs from external systems may forward "0300" instead
  # of "300". Without canonicalization, the parent field in the emitted JSON
  # would carry the leading zero, and pane-prompt filtering would match
  # against that string (missing legacy panes tagged plainly "of #300").
  force_missing tmux
  local cfg="$BATS_TEST_TMPDIR/dmux.config.json"
  printf '{"panes":[]}\n' > "$cfg"
  export DMUX_CONFIG_PATH="$cfg"
  run_fanout --status 0300
  [ "$status" -eq 0 ]
  [[ "$output" == *'"parent": 300'* ]]
}
