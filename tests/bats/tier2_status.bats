#!/usr/bin/env bats
#
# Tier 2 — `./fanout --status <PARENT>` JSON golden tests.
#
# Each @test points the gh / tmux shims at a fixture under
# tests/fixtures/scenario-status-*, runs fanout in --status mode, and diffs
# the captured JSON against tests/golden/scenario-status-*.status.txt.
#
# The fixture contract for --status is the same dmux.config.json /
# tmux-sessions.txt / tmux-show-options.tsv / project_root layout used by
# the dry-run tests, plus per-issue gh-issue-view-<N>.json files that include
# the `closedByPullRequestsReferences` field at the top level.
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

@test "scenario-status-no-fanned-children: total=0, all_merged=false" {
  use_fixture scenario-status-no-fanned-children
  run_fanout_status 999
  assert_success
  assert_status_golden scenario-status-no-fanned-children
}
