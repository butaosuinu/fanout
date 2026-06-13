#!/usr/bin/env bats
# Tier 2 goldens for `fanout msg` (#70): exact dry-run output for the write
# verbs, and the read-path JSON/table views against a real SQLite DB. The DB
# fixture is self-hosting — seeded through fanout's own write verbs instead
# of a sqlite3 CLI (which is deliberately not a test dependency) — so the
# write path is exercised by every read-path golden. FANOUT_FAKE_NOW freezes
# created_at/read_at; ids are deterministic because each test starts from a
# fresh DB (AUTOINCREMENT yields 1, 2, 3, ...).

load helpers

# Same isolation rationale as tier1_msg.bats: detection must never see a dev
# machine's live state.json, and the DB must live in bats' tmpdir.
msg_env() {
  export FANOUT_DB_PATH="$BATS_TEST_TMPDIR/team.db"
  export FANOUT_STATE_PATH="$BATS_TEST_TMPDIR/no-state.json"
  export FANOUT_FAKE_NOW="2026-06-13T00:00:00Z"
}

# Canonical conversation: peers 70/71 registered, two 1:1 messages 71->70,
# one board post by 71, one board post by 70 (own posts must not show up as
# unread for 70), one 1:1 message 70->71 (must not show up in 70's inbox).
seed_msg_db() {
  run_fanout msg register --self 70 --parent 68
  assert_success
  run_fanout msg register --self 71 --parent 68
  assert_success
  run_fanout msg send --to 70 --self 71 --parent 68 first note to 70
  assert_success
  run_fanout msg send --to 70 --self 71 --parent 68 --kind blocker waiting on your schema
  assert_success
  run_fanout msg post --self 71 --parent 68 board update from 71
  assert_success
  run_fanout msg post --self 70 --parent 68 board update from 70
  assert_success
  run_fanout msg send --to 71 --self 70 --parent 68 reply meant for 71
  assert_success
}

@test "msg send --dry-run golden" {
  msg_env
  run_fanout msg send --dry-run --to 71 --self 70 --parent 68 hello world
  assert_success
  assert_golden msg-send
}

@test "msg post --dry-run --kind golden" {
  msg_env
  run_fanout msg post --dry-run --kind blocker --self 70 --parent 68 schema is not ready
  assert_success
  assert_golden msg-post-kind
}

@test "msg mark-read --dry-run --id golden" {
  msg_env
  run_fanout msg mark-read --dry-run --id 3 --id 5 --self 70 --parent 68
  assert_success
  assert_golden msg-mark-read-ids
}

@test "msg mark-read --dry-run --all golden" {
  msg_env
  run_fanout msg mark-read --dry-run --all --self 70 --parent 68
  assert_success
  assert_golden msg-mark-read-all
}

@test "msg register --dry-run with explicit identity golden (no pane fields)" {
  msg_env
  run_fanout msg register --dry-run --self 70 --parent 68
  assert_success
  assert_golden msg-register-bare
}

@test "msg nudge --dry-run golden (pane id resolved from state.json)" {
  msg_env
  # nudge resolves the recipient pane id from state.json (never the DB), so the
  # dry-run line is deterministic given a fixed FANOUT_STATE_PATH. The agent
  # state is read live only on the real path, never in dry-run.
  printf '%s\n' '{"schemaVersion":1,"panes":[{"parent":"68","issueNum":70,"slug":"msg-cli-surface-70","branchName":"fanout/msg-cli-surface-70","paneId":"%1","agent":"claude","displayName":"msg cli surface","worktreePath":"","prompt":"[fanout #70 of #68] msg-cli-surface-70: msg CLI.","createdAt":"2026-06-13T00:00:00Z"}]}' \
    > "$BATS_TEST_TMPDIR/state.json"
  export FANOUT_STATE_PATH="$BATS_TEST_TMPDIR/state.json"
  run_fanout msg nudge 70 --dry-run --parent 68
  assert_success
  assert_golden msg-nudge
}

@test "msg inbox --json golden: unread 1:1 + board union" {
  msg_env
  seed_msg_db
  run_fanout msg inbox --self 70 --parent 68 --json
  assert_success
  assert_golden msg-inbox json
}

@test "msg inbox --mark-read --json golden, then a drained second read" {
  msg_env
  seed_msg_db
  run_fanout msg inbox --mark-read --self 70 --parent 68 --json
  assert_success
  assert_golden msg-inbox-mark-read json

  run_fanout msg inbox --self 70 --parent 68 --json
  assert_success
  assert_golden msg-inbox-drained json
}

@test "msg board --json golden: own posts excluded from unread" {
  msg_env
  seed_msg_db
  run_fanout msg board --self 71 --parent 68 --json
  assert_success
  assert_golden msg-board-own-excluded json
}

@test "msg board --all --json golden" {
  msg_env
  seed_msg_db
  run_fanout msg board --all --self 71 --parent 68 --json
  assert_success
  assert_golden msg-board-all json
}

@test "msg peers --json golden" {
  msg_env
  seed_msg_db
  run_fanout msg peers --parent 68 --json
  assert_success
  assert_golden msg-peers json
}

@test "msg inbox human table golden (TERM=dumb, no color)" {
  msg_env
  seed_msg_db
  run_fanout msg inbox --self 70 --parent 68
  assert_success
  assert_golden msg-inbox-table table
}

@test "msg send --json golden: inserted message echo" {
  msg_env
  seed_msg_db
  run_fanout msg send --to 71 --self 70 --parent 68 --json one more for 71
  assert_success
  assert_golden msg-send-echo json
}

@test "msg with zero identity flags detects the pane from state.json" {
  msg_env
  # worktreePath stays "" on purpose: IdentifyPane skips rows whose recorded
  # worktree conflicts with the live one, and the bats cwd varies by machine.
  printf '%s\n' '{"schemaVersion":1,"panes":[{"parent":"68","issueNum":70,"slug":"msg-cli-surface-70","branchName":"fanout/msg-cli-surface-70","paneId":"%1","agent":"claude","displayName":"msg cli surface","worktreePath":"","prompt":"[fanout #70 of #68] msg-cli-surface-70: msg CLI.","createdAt":"2026-06-13T00:00:00Z"}]}' \
    > "$BATS_TEST_TMPDIR/state.json"
  export FANOUT_STATE_PATH="$BATS_TEST_TMPDIR/state.json"

  run_fanout msg register --json
  assert_success
  assert_golden msg-register-detected json

  run_fanout msg inbox --json
  assert_success
  assert_golden msg-inbox-detected json
}
