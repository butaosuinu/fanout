#!/usr/bin/env bats
#
# Tier 2 — `./fanout --status <PARENT>` JSON golden tests.
#
# Each @test points the gh / git shims at a fixture under
# tests/fixtures/scenario-status-*, runs fanout in --status mode, and diffs
# the captured JSON against tests/golden/scenario-status-*.status.txt.
#
# The fixture contract for --status is project_root/.fanout/state.json plus
# per-issue gh-issue-view-<N>.json files that include the
# `closedByPullRequestsReferences` field at the top level.
#
# Regenerate goldens after an intentional schema change with:
#   FANOUT_GOLDEN_UPDATE=1 bats tests/bats/tier2_status.bats

load helpers

@test "scenario-status-all-merged: every fanned child has a MERGED PR" {
  use_fixture scenario-status-all-merged
  run_fanout_status 100
  assert_success
  assert_status_golden scenario-status-all-merged
}

@test "scenario-status-mixed: one MERGED, one OPEN-PR, one no-PR child" {
  use_fixture scenario-status-mixed
  run_fanout_status 200
  assert_success
  assert_status_golden scenario-status-mixed
}

@test "scenario-herdr-status: final Herdr child identity is reported" {
  use_fixture scenario-herdr-status
  run_fanout_status 524
  assert_success
  assert_status_golden scenario-herdr-status
}

# --- Herdr lifecycle gates --------------------------------------------------
#
# Every Herdr workspace / worktree / agent mutation sits behind a fanout-owned
# session, whose admission needs a live server socket and a live supervisor
# lock. Black-box runs have neither, so these cases pin the offline half: how
# far each command gets, and that it issues no herdr command on the way.

# materialize_herdr_fixture and assert_herdr_argv live in helpers.bash, which
# also arms HERDR_SHIM_LOG as an empty per-test file.

@test "scenario-herdr-status: reporting a Herdr row issues no herdr command" {
  use_fixture scenario-herdr-status
  run_fanout_status 524
  assert_success
  assert_herdr_argv
}

@test "scenario-herdr-cleanup-incomplete: cleanup rejects legacy identity before mutation" {
  use_fixture scenario-herdr-cleanup-incomplete
  cp -R "$FIXTURE_DIR" "$BATS_TEST_TMPDIR/fixture"
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  run_fanout 524 --cleanup
  [ "$status" -eq 1 ]
  assert_golden scenario-herdr-cleanup-incomplete cleanup
  assert_herdr_argv
}

# scenario-herdr-owned-absent carries a complete Herdr identity whose repo key
# and repo root match the checkout, so --cleanup / --close / --merge get past
# every offline identity check and stop only because no fanout-owned Herdr
# server exists. The golden's owned-session error text is what pins the gate:
# it names the exact check each command stopped at.
#
# The argv log is a narrower guard. Owned-route calls exec the admitted binary
# by absolute path under a hermetic control environment that carries neither
# PATH nor HERDR_SHIM_LOG (internal/infra/herdrrun/herdrrun.go routeEnvironment),
# so they would never reach this shim or this log in the first place. What an
# empty log rules out is a PATH-reachable `herdr` invocation — a probe issued
# before the owned session is opened.

@test "scenario-herdr-owned-absent: cleanup stops at the owned-session gate" {
  materialize_herdr_fixture scenario-herdr-owned-absent
  run_fanout 524 --cleanup
  [ "$status" -eq 1 ]
  assert_golden scenario-herdr-owned-absent cleanup
  assert_herdr_argv
}

@test "scenario-herdr-owned-absent: close stops at the owned-session gate" {
  materialize_herdr_fixture scenario-herdr-owned-absent
  run_fanout 524 --close 528
  [ "$status" -eq 1 ]
  assert_golden scenario-herdr-owned-absent close
  assert_herdr_argv
}

@test "scenario-herdr-owned-absent: merge stops at the owned-session gate" {
  materialize_herdr_fixture scenario-herdr-owned-absent
  run_fanout 524 --merge 528
  [ "$status" -eq 1 ]
  assert_golden scenario-herdr-owned-absent merge
  assert_herdr_argv
}

# --- herdr shim contract ----------------------------------------------------
#
# The cases above assert an empty argv log, so they only mean something if
# every way that assertion could pass by accident fails instead — a recorded
# command, an unarmed log, a blank line from a zero-argument call — and if the
# shim can in fact answer the verbs it claims to. These pin both, and cover the
# verbs and the failure injection no black-box run reaches on its own.

@test "herdr shim: a recorded command fails the empty-argv assertion" {
  printf 'workspace close workspace-528\n' > "$HERDR_SHIM_LOG"
  run assert_herdr_argv
  [ "$status" -ne 0 ]
  [[ "$output" == *"workspace close workspace-528"* ]]
}

@test "herdr shim: an unarmed argv log fails instead of passing vacuously" {
  rm -f "$HERDR_SHIM_LOG"
  run assert_herdr_argv
  [ "$status" -ne 0 ]
  [[ "$output" == *"never armed"* ]]
}

@test "herdr shim: a zero-argument call is not an empty log" {
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  mkdir -p "$FIXTURE_DIR"
  run bash -c 'herdr 2>&1'
  [ "$status" -eq 1 ]
  # The blank argv line the shim logged must not read as "no command issued".
  run assert_herdr_argv
  [ "$status" -ne 0 ]
}

# Mutations carry the session in HERDR_SESSION, so this pins the argv shape
# production actually emits for one.
@test "herdr shim: a mutation verb answers from its fixture and logs its argv" {
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  export HERDR_SESSION=fixture-session
  mkdir -p "$FIXTURE_DIR"
  printf '{"id":"cli:workspace:create"}\n' > "$FIXTURE_DIR/herdr-workspace-create.json"
  run herdr workspace create --cwd /repo --label child --no-focus
  assert_success
  [ "$output" = '{"id":"cli:workspace:create"}' ]
  assert_herdr_argv "workspace create --cwd /repo --label child --no-focus"
}

# The status probe is the one call site that passes the session as a flag
# (internal/infra/herdrrun/herdrrun.go), so the strip has to survive it.
@test "herdr shim: --session is stripped before the status probe dispatches" {
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  mkdir -p "$FIXTURE_DIR"
  printf '{"id":"cli:status"}\n' > "$FIXTURE_DIR/herdr-status.json"
  run herdr --session fixture-session status --json
  assert_success
  [ "$output" = '{"id":"cli:status"}' ]
  assert_herdr_argv "--session fixture-session status --json"
}

@test "herdr shim: --version still answers behind a --session prefix" {
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  mkdir -p "$FIXTURE_DIR"
  printf 'herdr 0.7.5\n' > "$FIXTURE_DIR/herdr-version.txt"
  run herdr --session fixture-session --version
  assert_success
  [ "$output" = "herdr 0.7.5" ]
}

@test "herdr shim: --session without a value dies instead of eating the verb" {
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  mkdir -p "$FIXTURE_DIR"
  run bash -c 'herdr --session 2>&1'
  [ "$status" -eq 1 ]
  [[ "$output" == *"--session needs a value: --session"* ]]
}

# Failure injection reaches the named probes too, not just the verbs whose
# fixture file name is derived from argv.
@test "herdr shim: the .exit override can reject the status probe" {
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  mkdir -p "$FIXTURE_DIR"
  printf '{"error":{"code":"server_unavailable"}}\n' > "$FIXTURE_DIR/herdr-status.json"
  printf 'herdr: server unavailable\n' > "$FIXTURE_DIR/herdr-status.err"
  printf '4\n' > "$FIXTURE_DIR/herdr-status.exit"
  run bash -c 'herdr --session fixture-session status --json 2>&1'
  [ "$status" -eq 4 ]
  [[ "$output" == *"herdr: server unavailable"* ]]
  [[ "$output" == *'"server_unavailable"'* ]]
}

# A .err with no .exit is a half-written injection: the verb would answer 0
# with the rejection text on stderr, which reads as success to the Go side.
@test "herdr shim: an orphan .err fails loudly instead of being ignored" {
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  mkdir -p "$FIXTURE_DIR"
  printf '{"id":"cli:status"}\n' > "$FIXTURE_DIR/herdr-status.json"
  printf 'herdr: server unavailable\n' > "$FIXTURE_DIR/herdr-status.err"
  run bash -c 'herdr --session fixture-session status --json 2>&1'
  [ "$status" -eq 1 ]
  [[ "$output" == *"has no matching herdr-status.exit"* ]]
}

# --version is the one probe with a success-path default, so the injection has
# to win over it. Falling through to `herdr 0.7.5` would admit the binary and
# let the run continue past the failure the scenario asked for.
@test "herdr shim: the .exit override can reject the version probe" {
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  mkdir -p "$FIXTURE_DIR"
  printf 'herdr: fatal: unable to initialize runtime\n' > "$FIXTURE_DIR/herdr-version.err"
  printf '2\n' > "$FIXTURE_DIR/herdr-version.exit"
  run bash -c 'herdr --version 2>&1'
  [ "$status" -eq 2 ]
  [[ "$output" == *"unable to initialize runtime"* ]]
  [[ "$output" != *"herdr 0.7.5"* ]]
}

# The default makes the orphan case worse here than for the other verbs: an
# ignored .err would answer 0 with a healthy version string, which the Go side
# reads as an admitted binary rather than a broken one.
@test "herdr shim: an orphan .err on the version probe fails loudly" {
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  mkdir -p "$FIXTURE_DIR"
  printf 'herdr 0.7.5\n' > "$FIXTURE_DIR/herdr-version.txt"
  printf 'herdr: fatal: unable to initialize runtime\n' > "$FIXTURE_DIR/herdr-version.err"
  run bash -c 'herdr --version 2>&1'
  [ "$status" -eq 1 ]
  [[ "$output" == *"has no matching herdr-version.exit"* ]]
}

@test "herdr shim: the .exit override replays a rejection envelope" {
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  mkdir -p "$FIXTURE_DIR"
  printf '{"error":{"code":"worktree_busy"}}\n' > "$FIXTURE_DIR/herdr-worktree-remove.json"
  printf 'herdr: worktree is busy\n' > "$FIXTURE_DIR/herdr-worktree-remove.err"
  printf '3\n' > "$FIXTURE_DIR/herdr-worktree-remove.exit"
  run bash -c 'herdr worktree remove --workspace workspace-528 --json 2>&1'
  [ "$status" -eq 3 ]
  [[ "$output" == *"herdr: worktree is busy"* ]]
  [[ "$output" == *'"worktree_busy"'* ]]
}

@test "herdr shim: an unlisted verb is an error, not a silent success" {
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  mkdir -p "$FIXTURE_DIR"
  run bash -c 'herdr session teleport 2>&1'
  [ "$status" -eq 1 ]
  [[ "$output" == *"unsupported command: session teleport"* ]]
}

@test "herdr shim: a verb without a fixture names the file it wanted" {
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  mkdir -p "$FIXTURE_DIR"
  run bash -c 'herdr pane read %1 --source visible --format text 2>&1'
  [ "$status" -eq 1 ]
  [[ "$output" == *"missing fixture $FIXTURE_DIR/herdr-pane-read.json"* ]]
}

@test "scenario-status-mixed table: PR diff stats render in a human-readable table" {
  use_fixture scenario-status-mixed
  run_fanout_status 200 --format table
  assert_success
  assert_golden scenario-status-mixed status-table
}

@test "scenario-status-dashboard-post: --post-dashboard creates marker comment" {
  use_fixture scenario-status-dashboard-post
  run_fanout_status 200 --post-dashboard
  assert_success
  assert_golden scenario-status-dashboard-post status-dashboard
}

@test "scenario-status-dashboard-edit: --post-dashboard updates existing marker comment" {
  use_fixture scenario-status-dashboard-edit
  run_fanout_status 200 --post-dashboard
  assert_success
  assert_golden scenario-status-dashboard-edit status-dashboard
}

@test "scenario-status-no-fanned-children: total=0, all_merged=false" {
  use_fixture scenario-status-no-fanned-children
  run_fanout_status 999
  assert_success
  assert_status_golden scenario-status-no-fanned-children
}

# Regression: before the gh-issue-view → gh-api-graphql rewrite, every
# `--status` call died with `jq: Cannot index array with string "nodes"` on
# the first child whose `closedByPullRequestsReferences` was empty — i.e.
# any session in the gap between fan-out and the first PR landing. This
# fixture pins that exact shape (two OPEN children, no PR refs) so the
# fix can't silently regress.
@test "scenario-status-no-prs-yet: empty closedByPullRequestsReferences must not crash" {
  use_fixture scenario-status-no-prs-yet
  run_fanout_status 700
  assert_success
  assert_status_golden scenario-status-no-prs-yet
}

# Pagination: child #901 is closed by two PRs. Page 1 carries an unmerged
# CLOSED ref (#1001) with hasNextPage:true; the MERGED ref (#1002) only
# appears on page 2. If get_issue_with_prs ever stops paginating,
# has_merged_pr collapses to false and summary.all_merged stalls
# wait-and-continue loops forever. Covers the Codex review on PR #51.
@test "scenario-status-paginated: merged PR on page 2 is counted via cursor follow" {
  use_fixture scenario-status-paginated
  run_fanout_status 900
  assert_success
  assert_status_golden scenario-status-paginated
}

# Cross-parent filtering: a state file that fanned both #300 and #400 must not
# leak #400's children into `fanout --status 300` (and vice versa). Old-format
# entries without a parent are excluded because their parent is unknown — we'd
# rather under-report than mix parents and lie about `summary.all_merged`.
@test "scenario-status-multi-parent: --status 300 returns only #300's children" {
  use_fixture scenario-status-multi-parent
  run_fanout_status 300
  assert_success
  assert_status_golden scenario-status-multi-parent-300
}

# Leading-zero normalization: a pane tagged "of #0300" must match
# `--status 300` (and `--status 0300`, since the CLI canonicalizes on
# entry). Ensures wait-and-continue automation doesn't stall when ID
# formats drift between the fanout-time prompt and the polling caller.
@test "scenario-status-leading-zero: --status 300 matches both #300 and #0300 panes" {
  use_fixture scenario-status-leading-zero
  run_fanout_status 300
  assert_success
  assert_status_golden scenario-status-leading-zero-300
}

# Duplicate handling: a stale state file can contain repeated child entries
# under the same parent. `--status` must report one entry per issue and count
# it once in the summary — duplicates would inflate summary arithmetic.
@test "scenario-status-duplicate-panes: --status 500 dedupes #501 across two panes" {
  use_fixture scenario-status-duplicate-panes
  run_fanout_status 500
  assert_success
  assert_status_golden scenario-status-duplicate-panes-500
}

@test "scenario-plan-status: plan --status reports task branches, PRs, and blockers" {
  use_fixture scenario-plan-status
  run_fanout_plan_status launch-plan
  assert_success
  assert_golden scenario-plan-status status
}

@test "scenario-plan-status table: plan --status --format table reuses PR table columns" {
  use_fixture scenario-plan-status
  run_fanout_plan_status "$FIXTURE_DIR/plan.json" --format table
  assert_success
  assert_golden scenario-plan-status status-table
}
