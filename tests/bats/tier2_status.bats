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

# Copy an in-tree Herdr fixture into the per-test tmpdir and rewrite the
# placeholder repository paths in its state.json to the paths the git shim
# reports for that copy. herdrRepoKey / herdrRepoRoot are compared against
# `git rev-parse` output after symlink resolution, so both must be the
# physical path (BATS_TEST_TMPDIR lives under a symlinked /var on macOS).
materialize_herdr_fixture() {
  local name="$1"
  local source="$TESTS_DIR/fixtures/$name"
  local dir="$BATS_TEST_TMPDIR/fixture"
  local root common

  cp -R "$source" "$dir"
  export FIXTURE_DIR="$dir"
  mkdir -p "$dir/project_root/.fixture-git-common"
  root="$(cd "$dir/project_root" && pwd -P)"
  common="$(cd "$dir/project_root/.fixture-git-common" && pwd -P)"
  sed -e "s|/tmp/herdr-status-repo/.git|$common|" \
      -e "s|/tmp/herdr-status-repo|$root|" \
      -e "s|/tmp/herdr-status-child|$root/child|" \
      "$source/project_root/.fanout/state.json" > "$root/.fanout/state.json"
  export HERDR_SHIM_LOG="$BATS_TEST_TMPDIR/herdr-argv.log"
}

# Assert the herdr shim logged exactly these argv lines, in order. With no
# arguments it asserts that fanout issued no herdr command at all.
assert_herdr_argv() {
  local expected="" actual
  if [[ $# -gt 0 ]]; then
    printf -v expected '%s\n' "$@"
    expected="${expected%$'\n'}"
  fi
  actual="$(cat "$HERDR_SHIM_LOG" 2>/dev/null || true)"
  if [[ "$actual" != "$expected" ]]; then
    printf 'herdr argv log mismatch\n--- want ---\n%s\n--- got ---\n%s\n' \
      "$expected" "$actual" >&2
    return 1
  fi
}

@test "scenario-herdr-status: reporting a Herdr row issues no herdr command" {
  use_fixture scenario-herdr-status
  export HERDR_SHIM_LOG="$BATS_TEST_TMPDIR/herdr-argv.log"
  run_fanout_status 524
  assert_success
  assert_herdr_argv
}

@test "scenario-herdr-cleanup-incomplete: cleanup rejects legacy identity before mutation" {
  use_fixture scenario-herdr-cleanup-incomplete
  cp -R "$FIXTURE_DIR" "$BATS_TEST_TMPDIR/fixture"
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  export HERDR_SHIM_LOG="$BATS_TEST_TMPDIR/herdr-argv.log"
  run_fanout 524 --cleanup
  [ "$status" -eq 1 ]
  assert_golden scenario-herdr-cleanup-incomplete cleanup
  assert_herdr_argv
}

# scenario-herdr-owned-absent carries a complete Herdr identity whose repo key
# and repo root match the checkout, so --cleanup / --close / --merge get past
# every offline identity check and stop only because no fanout-owned Herdr
# server exists. The mutation each command would issue next sits directly
# behind that gate, so a reordering shows up here as a non-empty argv log.

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
# The cases above assert an empty argv log, so they only mean something if a
# non-empty one fails and if the shim can in fact answer a mutation verb.
# These pin both, and cover the verbs no black-box run reaches on its own.

@test "herdr shim: a recorded command fails the empty-argv assertion" {
  export HERDR_SHIM_LOG="$BATS_TEST_TMPDIR/herdr-argv.log"
  printf 'workspace close workspace-528\n' > "$HERDR_SHIM_LOG"
  run assert_herdr_argv
  [ "$status" -ne 0 ]
  [[ "$output" == *"workspace close workspace-528"* ]]
}

@test "herdr shim: a mutation verb answers from its fixture and logs its argv" {
  export FIXTURE_DIR="$BATS_TEST_TMPDIR/fixture"
  export HERDR_SHIM_LOG="$BATS_TEST_TMPDIR/herdr-argv.log"
  mkdir -p "$FIXTURE_DIR"
  printf '{"id":"cli:workspace:create"}\n' > "$FIXTURE_DIR/herdr-workspace-create.json"
  run herdr --session fixture-session workspace create --cwd /repo --label child --no-focus
  assert_success
  [ "$output" = '{"id":"cli:workspace:create"}' ]
  assert_herdr_argv "--session fixture-session workspace create --cwd /repo --label child --no-focus"
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
