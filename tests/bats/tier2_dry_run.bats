#!/usr/bin/env bats
#
# Tier 2 — `./fanout --dry-run` golden-output tests.
#
# Each @test picks a scenario fixture under tests/fixtures/<name>, runs
# fanout against it with --dry-run (so no tmux I/O and no popup intercept
# happen), and diffs the captured output against the matching golden file
# under tests/golden/<name>.dry-run.txt.
#
# The fixture directory contract is documented in tests/bin/gh and
# tests/bin/tmux: the shims read gh-sub-issue-list.json, gh-issue-view-<N>.json,
# tmux-sessions.txt, tmux-show-options.tsv, and dmux.config.json from
# $FIXTURE_DIR.
#
# Regenerating goldens after an intentional output change:
#   FANOUT_GOLDEN_UPDATE=1 bats tests/bats/tier2_dry_run.bats
# Review the diff in git and commit if the new output is correct.

load helpers

@test "scenario-sub-issue-only: two OPEN children from Sub-issues API" {
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100
  assert_success
  assert_golden scenario-sub-issue-only
}

@test "scenario-body-task-list: children come only from parent body task-list" {
  use_fixture scenario-body-task-list
  run_fanout_dry 200
  assert_success
  assert_golden scenario-body-task-list
}

@test "scenario-union: Sub-issues API + body task-list dedupe into one set" {
  use_fixture scenario-union
  run_fanout_dry 300
  assert_success
  assert_golden scenario-union
}

@test "scenario-include: --include force-adds numbers not reachable by the union" {
  use_fixture scenario-include
  run_fanout_dry 400 --include 401,402
  assert_success
  assert_golden scenario-include
}

@test "scenario-only: --only narrows the set and prints filtered-out section" {
  use_fixture scenario-only
  run_fanout_dry 500 --only 501,503
  assert_success
  assert_golden scenario-only
}

@test "scenario-skip: --skip excludes and prints filtered-out section" {
  use_fixture scenario-skip
  run_fanout_dry 600 --skip 602
  assert_success
  assert_golden scenario-skip
}

@test "scenario-limit: --limit caps the run and prints deferred rerun command" {
  use_fixture scenario-limit
  run_fanout_dry 700 --limit 2
  assert_success
  assert_golden scenario-limit
}

@test "scenario-idempotency: existing [fanout #N] pane causes N to be skipped" {
  use_fixture scenario-idempotency
  run_fanout_dry 800
  assert_success
  assert_golden scenario-idempotency
}
