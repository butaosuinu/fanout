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
  [[ "$output" == *"--to must be a positive issue number"* ]]
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
