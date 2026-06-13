#!/usr/bin/env bats
# Tier 1 surface tests for the `fanout msg` subcommand (#70): usage, verb and
# flag validation, the 0/2/4 exit-code contract, and identity-detection
# failure guidance. No goldens here — exact output lives in tier2_msg.bats.

load helpers

# Isolate every msg test from the developer's live environment. helpers'
# setup() exports TMUX_PANE=%1, so without an explicit FANOUT_STATE_PATH the
# pane detection in `fanout msg` could match a real .fanout/state.json row on
# a dev machine; pointing it at a nonexistent file makes detection fail
# deterministically. FANOUT_DB_PATH keeps the SQLite DB (and its WAL/SHM
# sidecars) inside bats' auto-cleaned tmpdir instead of /tmp.
msg_env() {
  export FANOUT_DB_PATH="$BATS_TEST_TMPDIR/team.db"
  export FANOUT_STATE_PATH="$BATS_TEST_TMPDIR/no-state.json"
}

@test "msg without a verb prints usage and exits 2" {
  msg_env
  run_fanout msg
  [ "$status" -eq 2 ]
  [[ "$output" == *"Usage: fanout msg"* ]]
}

@test "msg --help prints usage and exits 0" {
  msg_env
  run_fanout msg --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: fanout msg"* ]]
}

@test "msg -h before a verb prints usage and exits 0" {
  msg_env
  run_fanout msg inbox -h
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: fanout msg"* ]]
}

@test "msg with an unknown verb exits 2" {
  msg_env
  run_fanout msg bogus
  [ "$status" -eq 2 ]
  [[ "$output" == *"unknown verb: bogus"* ]]
}

@test "msg send without detectable pane asks for --self/--parent: exit 2" {
  msg_env
  run_fanout msg send --to 71 hello
  [ "$status" -eq 2 ]
  [[ "$output" == *"pass --self <issue> and --parent <ref>"* ]]
}

@test "msg send without --to exits 2" {
  msg_env
  run_fanout msg send --self 70 --parent 68 hello
  [ "$status" -eq 2 ]
  [[ "$output" == *"--to <issue> is required"* ]]
}

@test "msg send with a non-integer --to exits 2" {
  msg_env
  run_fanout msg send --to abc --self 70 --parent 68 hello
  [ "$status" -eq 2 ]
  [[ "$output" == *"--to must be a non-zero issue number"* ]]
}

@test "msg send without a body exits 2" {
  msg_env
  run_fanout msg send --to 71 --self 70 --parent 68
  [ "$status" -eq 2 ]
  [[ "$output" == *"message body is required"* ]]
}

@test "msg mark-read without --id or --all exits 2" {
  msg_env
  run_fanout msg mark-read --self 70 --parent 68
  [ "$status" -eq 2 ]
  [[ "$output" == *"pass either --id <n> (repeatable) or --all"* ]]
}

@test "msg mark-read with both --id and --all exits 2" {
  msg_env
  run_fanout msg mark-read --id 1 --all --self 70 --parent 68
  [ "$status" -eq 2 ]
  [[ "$output" == *"pass either --id <n> (repeatable) or --all"* ]]
}

@test "msg inbox rejects --dry-run (read verb): exit 2" {
  msg_env
  run_fanout msg inbox --dry-run --self 70 --parent 68
  [ "$status" -eq 2 ]
  [[ "$output" == *"--dry-run is not supported"* ]]
}

@test "msg send rejects --dry-run with --json: exit 2" {
  msg_env
  run_fanout msg send --dry-run --json --to 71 --self 70 --parent 68 hello
  [ "$status" -eq 2 ]
  [[ "$output" == *"--dry-run cannot be combined with --json"* ]]
}

@test "msg peers rejects a verb-foreign flag: exit 2" {
  msg_env
  run_fanout msg peers --to 5 --parent 68
  [ "$status" -eq 2 ]
  [[ "$output" == *"--to is not supported"* ]]
}

@test "msg with an unknown option exits 2" {
  msg_env
  run_fanout msg peers --bogus --parent 68
  [ "$status" -eq 2 ]
  [[ "$output" == *"unknown option: --bogus"* ]]
}

@test "msg with a value-flag missing its value exits 2" {
  msg_env
  run_fanout msg send --self 70 --parent 68 hello --to
  [ "$status" -eq 2 ]
  [[ "$output" == *"--to requires an argument"* ]]
}

@test "msg read verb rejects a stray positional argument: exit 2" {
  msg_env
  run_fanout msg inbox hello --self 70 --parent 68
  [ "$status" -eq 2 ]
  [[ "$output" == *"unexpected argument: hello"* ]]
}

@test "msg exits 4 when the team DB path is unusable (backend)" {
  msg_env
  export FANOUT_DB_PATH="$BATS_TEST_TMPDIR"
  run_fanout msg peers --parent 68
  [ "$status" -eq 4 ]
}

@test "msg send happy path writes the isolated DB and exits 0" {
  msg_env
  run_fanout msg send --to 71 --self 70 --parent 68 hello over there
  assert_success
  [[ "$output" == *"sent #1 to #71"* ]]
  [ -f "$BATS_TEST_TMPDIR/team.db" ]
}

@test "msg dry-run touches no DB file" {
  msg_env
  run_fanout msg send --dry-run --to 71 --self 70 --parent 68 hello
  assert_success
  [[ "$output" == *"# would INSERT INTO messages"* ]]
  [ ! -e "$BATS_TEST_TMPDIR/team.db" ]
}

@test "msg send body may contain -h after the first body word (no silent help)" {
  msg_env
  run_fanout msg send --to 71 --self 70 --parent 68 try -h on the new flag
  assert_success
  [[ "$output" == *"sent #1 to #71"* ]]
}

@test "msg send -- terminator lets the body carry flag-like words" {
  msg_env
  run_fanout msg send --to 71 --self 70 --parent 68 -- --kind is body text
  assert_success
  [[ "$output" == *"sent #1 to #71"* ]]
}

@test "msg nudge without a target exits 2" {
  msg_env
  run_fanout msg nudge --parent 68
  [ "$status" -eq 2 ]
  [[ "$output" == *"target issue <N> is required"* ]]
}

@test "msg nudge with a non-integer target exits 2" {
  msg_env
  run_fanout msg nudge abc --parent 68
  [ "$status" -eq 2 ]
  [[ "$output" == *"target must be a non-zero issue number"* ]]
}

@test "msg nudge with a second target exits 2" {
  msg_env
  run_fanout msg nudge 71 72 --parent 68
  [ "$status" -eq 2 ]
  [[ "$output" == *"takes exactly one target issue"* ]]
}

@test "msg nudge rejects --to (target is positional): exit 2" {
  msg_env
  run_fanout msg nudge --to 71 --parent 68
  [ "$status" -eq 2 ]
  [[ "$output" == *"--to is not supported"* ]]
}

@test "msg nudge for an unrecorded peer is a best-effort no-op success" {
  msg_env
  # A recipient absent from state.json never touches tmux: nudge is best-effort
  # (the message is already persisted by send), so it exits 0 and reports the
  # skip instead of failing. Deterministic without a controllable tmux server.
  printf '%s\n' '{"schemaVersion":1,"panes":[]}' > "$BATS_TEST_TMPDIR/state.json"
  export FANOUT_STATE_PATH="$BATS_TEST_TMPDIR/state.json"
  run_fanout msg nudge 99 --parent 68 --json
  assert_success
  [[ "$output" == *'"nudged": false'* ]]
  [[ "$output" == *"not recorded"* ]]
  [ ! -e "$BATS_TEST_TMPDIR/team.db" ]
}

@test "msg nudge --dry-run prints the would-line and touches no DB" {
  msg_env
  printf '%s\n' '{"schemaVersion":1,"panes":[{"parent":"68","issueNum":70,"slug":"s","branchName":"b","paneId":"%1","agent":"claude","displayName":"d","worktreePath":"","prompt":"[fanout #70 of #68] s","createdAt":"2026-06-13T00:00:00Z"}]}' \
    > "$BATS_TEST_TMPDIR/state.json"
  export FANOUT_STATE_PATH="$BATS_TEST_TMPDIR/state.json"
  run_fanout msg nudge 70 --dry-run --parent 68
  assert_success
  [[ "$output" == *"# would send-keys -t %1 -l "* ]]
  [ ! -e "$BATS_TEST_TMPDIR/team.db" ]
}

@test "msg detects a manual pane (negative synthetic issue under @manual)" {
  msg_env
  printf '%s\n' '{"schemaVersion":1,"panes":[{"parent":"@manual","issueNum":-1,"slug":"manual-1-scratch","branchName":"","paneId":"%1","agent":"claude","displayName":"scratch","worktreePath":"","prompt":"scratch work","createdAt":"2026-06-13T00:00:00Z"}]}' \
    > "$BATS_TEST_TMPDIR/state.json"
  export FANOUT_STATE_PATH="$BATS_TEST_TMPDIR/state.json"
  run_fanout msg inbox --json
  assert_success
  [[ "$output" == *'"self": -1'* ]]
  [[ "$output" == *'"parent": "@manual"'* ]]
}

@test "msg rejects a DB already holding another parent's messages: exit 4" {
  msg_env
  run_fanout msg send --to 71 --self 70 --parent 68 hello
  assert_success
  run_fanout msg send --to 71 --self 70 --parent 99 hello again
  [ "$status" -eq 4 ]
  [[ "$output" == *"one team DB serves one parent"* ]]
}

@test "msg ownership claim covers register-only DBs (no message rows): exit 4" {
  msg_env
  run_fanout msg register --self 70 --parent 68
  assert_success
  run_fanout msg peers --parent 99
  [ "$status" -eq 4 ]
  [[ "$output" == *"owned by parent 68"* ]]
}
