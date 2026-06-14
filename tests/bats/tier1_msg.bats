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
  [[ "$output" == *"pass --self <issue|task-id> and --parent <ref>"* ]]
}

@test "msg send without --to exits 2" {
  msg_env
  run_fanout msg send --self 70 --parent 68 hello
  [ "$status" -eq 2 ]
  [[ "$output" == *"--to <issue|task-id> is required"* ]]
}

@test "msg send with an invalid --to token exits 2" {
  msg_env
  # Uppercase is neither an issue number nor a lowercase-kebab task id, so it is
  # rejected at parse time.
  run_fanout msg send --to ABC --self 70 --parent 68 hello
  [ "$status" -eq 2 ]
  [[ "$output" == *"--to must be a non-zero issue number or a plan task id"* ]]
}

@test "msg send with a task-id --to under an issue parent exits 2" {
  msg_env
  # A valid task id is only addressable under a plan parent; issue/project peers
  # are addressed by number.
  run_fanout msg send --to api-client --self 70 --parent 68 hello
  [ "$status" -eq 2 ]
  [[ "$output" == *"is a task id, only valid under a plan parent"* ]]
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
  [[ "$output" == *"target <issue|task-id> is required"* ]]
}

@test "msg nudge with an invalid target exits 2" {
  msg_env
  run_fanout msg nudge ABC --parent 68
  [ "$status" -eq 2 ]
  [[ "$output" == *"target must be a non-zero issue number or a plan task id"* ]]
}

@test "msg nudge with a second target exits 2" {
  msg_env
  run_fanout msg nudge 71 72 --parent 68
  [ "$status" -eq 2 ]
  [[ "$output" == *"takes exactly one target"* ]]
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

@test "msg send/inbox round-trip between plan tasks addressed by task id" {
  msg_env
  # Issue-less plan peers are addressed by task id under a plan:<slug> parent.
  # Identity is explicit so the round-trip does not depend on pane detection.
  run_fanout msg send --to api-client --self db-layer --parent plan:launch-plan "schema ready"
  assert_success
  [[ "$output" == *"to api-client"* ]]
  run_fanout msg inbox --self api-client --parent plan:launch-plan --json
  assert_success
  [[ "$output" == *"schema ready"* ]]
  [[ "$output" == *'"parent": "plan:launch-plan"'* ]]
}

@test "msg --to <all-digit task id> targets the task, not numeric peer N" {
  msg_env
  # Task id "123" is a valid plan task id. Under a plan parent it must address
  # the task pane (registered/self-detected as a synthetic number), NOT numeric
  # peer 123. The "123" pane auto-detects its own self from state.json; a sender
  # using --to 123 must reach it.
  printf '%s\n' '{"schemaVersion":1,"panes":[{"parent":"plan:demo","issueNum":0,"taskId":"123","slug":"demo-123","branchName":"b","paneId":"%1","agent":"claude","displayName":"d","worktreePath":"","prompt":"[fanout 123 of plan:demo] demo-123: t"}]}' \
    > "$BATS_TEST_TMPDIR/state.json"
  export FANOUT_STATE_PATH="$BATS_TEST_TMPDIR/state.json"
  run_fanout msg send --to 123 --self db-layer --parent plan:demo "for task 123"
  assert_success
  [[ "$output" == *"to 123"* ]]
  run_fanout msg inbox --json
  assert_success
  [[ "$output" == *"for task 123"* ]]
}

@test "msg auto-detects a plan task pane (parent plan:<slug>)" {
  msg_env
  # A recorded plan row (issueNum 0 + taskId) must self-detect from the pane id
  # and scope messages to the plan parent.
  printf '%s\n' '{"schemaVersion":1,"panes":[{"parent":"plan:launch-plan","issueNum":0,"taskId":"api-client","slug":"launch-plan-api-client","branchName":"b","paneId":"%1","agent":"claude","displayName":"API","worktreePath":"","prompt":"[fanout api-client of plan:launch-plan] launch-plan-api-client: t"}]}' \
    > "$BATS_TEST_TMPDIR/state.json"
  export FANOUT_STATE_PATH="$BATS_TEST_TMPDIR/state.json"
  run_fanout msg inbox --json
  assert_success
  [[ "$output" == *'"parent": "plan:launch-plan"'* ]]
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
