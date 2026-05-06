#!/usr/bin/env bats
#
# Tier 2 — `./fanout --status` JSON tests.
#
# These scenarios exercise the read-only status path against fixture
# dmux.config.json files. The gh shim returns fixed child issue objects with
# closedByPullRequestsReferences so no live GitHub calls are made.

load helpers

@test "scenario-status-all-merged: every fanned child has a merged PR" {
  use_fixture scenario-status-all-merged
  run_fanout_status 900
  assert_success
  assert_json_golden scenario-status-all-merged
}

@test "scenario-status-partial: open PR keeps all_merged false" {
  use_fixture scenario-status-partial
  run_fanout_status 910
  assert_success
  assert_json_golden scenario-status-partial
}

@test "scenario-status-no-pr: child without PR is pending" {
  use_fixture scenario-status-no-pr
  run_fanout_status 920
  assert_success
  assert_json_golden scenario-status-no-pr
}

@test "scenario-status-empty: no fanned panes is not all_merged" {
  use_fixture scenario-status-empty
  run_fanout_status 930
  assert_success
  assert_json_golden scenario-status-empty
}

@test "--status cannot be combined with pane-creation flags" {
  use_fixture scenario-status-empty
  run_fanout_status 930 --dry-run
  [ "$status" -eq 2 ]
  [[ "$output" == *"--status cannot be combined with pane-creation flags"* ]]
}

@test "scenario-status-gh-error: child lookup failure exits 3" {
  use_fixture scenario-status-gh-error
  run_fanout_status 940
  [ "$status" -eq 3 ]
  [[ "$output" == *"gh issue view failed for #941"* ]]
}

@test "scenario-status-missing-config: missing dmux config exits 2" {
  use_fixture scenario-status-missing-config
  run_fanout_status 950
  [ "$status" -eq 2 ]
  [[ "$output" == *"dmux config not found"* ]]
}
