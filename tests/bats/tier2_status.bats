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

# Cross-parent filtering: a session that fanned both #300 and #400 must not
# leak #400's children into `fanout --status 300` (and vice versa). Old-format
# panes (`[fanout #N]` without parent annotation) are excluded because their
# parent is unknown — we'd rather under-report than mix parents and lie about
# `summary.all_merged`.
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

# Duplicate pane handling: a child can legitimately have more than one pane
# under the same parent (stale pane from a manual cleanup lag, a re-fan
# after a failed dmux popup, etc.). `--status` must report one entry per
# issue and count it once in the summary — duplicates would inflate
# `summary.total/merged/pending` and break wait-and-continue arithmetic.
@test "scenario-status-duplicate-panes: --status 500 dedupes #501 across two panes" {
  use_fixture scenario-status-duplicate-panes
  run_fanout_status 500
  assert_success
  assert_status_golden scenario-status-duplicate-panes-500
}
