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

@test "scenario-cross-parent-shared: pane fanned for #100 does not block fanout 200 for the shared child" {
  # Parents #100 and #200 share child #501. The fixture's only pane is
  # `[fanout #501 of #100]`. Idempotency must scope to the requested
  # parent: when running `fanout 200`, both #501 (shared) and #502 (B-only)
  # appear as targets and would each get their own `of #200` pane. Without
  # parent-scoped idempotency, #501 would be silently skipped and a later
  # `fanout --status 200` would lie about all_merged.
  use_fixture scenario-cross-parent-shared
  run_fanout_dry 200
  assert_success
  assert_golden scenario-cross-parent-shared
}

@test "scenario-legacy-weak-signal: weak-signal legacy pane is left alone AND a fresh parent-annotated pane is created" {
  # The pane carries the legacy `[fanout #601]` form (no parent annotation).
  # #601 is in parent #600's set only via body-task-list scan — the
  # Sub-issues API returns []. Two invariants must hold:
  #   (a) The migration step doesn't relabel the legacy pane — body-task-list
  #       refs aren't strong enough to claim ownership. No "would migrate"
  #       line appears.
  #   (b) The lenient-idempotency "claim" set is the strong-signal CSV
  #       (empty here), so #601 is NOT considered already-fanned. A fresh
  #       `[fanout #601 of #600]` pane is created, surfacing the child in
  #       a later `fanout --status 600`. The legacy pane is left for the
  #       user to delete in the dmux TUI.
  use_fixture scenario-legacy-weak-signal
  run_fanout_dry 600
  assert_success
  assert_golden scenario-legacy-weak-signal
}

@test "scenario-idempotency: existing [fanout #N] pane causes N to be skipped and migration is announced" {
  # The fixture's pane uses the legacy `[fanout #N]` form (no parent
  # annotation), which exercises both invariants in one run: idempotency
  # still treats #801 as already-fanned, AND the new legacy-tag migration
  # path emits its "would migrate ..." line under --dry-run.
  use_fixture scenario-idempotency
  run_fanout_dry 800
  assert_success
  assert_golden scenario-idempotency
}

@test "scenario-sub-issue-only with --name branch override: dry-run payload becomes structured object" {
  # Reuses the scenario-sub-issue-only fixture. Issue #101 gets a 3-segment
  # --name with slug + display + branch; #102 gets a branch-only --name
  # (||branch). The dry-run output must show:
  #   - issue #101: newPanePopup payload as object {prompt, branchName} and
  #     "branch-name -> feat/sub-issue-101-x" trace line
  #   - issue #102: same object shape with branchName only (no slug-hint or
  #     display-name lines)
  # If dmux ever changes how PopupManager.normalizeNewPaneInput interprets
  # the object, this golden will catch the divergence before live use.
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100 \
    --name "101=feat-x|Feature X|feat/sub-issue-101-x" \
    --name "102=||release/v2.0"
  assert_success
  assert_golden scenario-sub-issue-only-branch-override
}

@test "agent-codex variant of scenario-sub-issue-only: briefing omits Agent Teams hint" {
  # Reuses the scenario-sub-issue-only fixture; the only thing under test is
  # that --agent codex (last-wins over the helper's default --agent claude)
  # produces a briefing without the Agent Teams section, so the size lines
  # diverge from scenario-sub-issue-only.dry-run.txt.
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100 --agent codex
  assert_success
  assert_golden scenario-sub-issue-only-codex
}

@test "Go settings disabled variant of scenario-sub-issue-only: briefing size tracks toggles" {
  skip_unless_fanout_go
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100 --no-auto-pr --no-pr-review-gate --no-briefing-code-review --no-agent-teams-hint
  assert_success
  assert_golden scenario-settings-disabled
}

@test "scenario-project-basic: Projects v2 URL with Todo column produces panes" {
  use_fixture scenario-project-basic
  run_fanout_dry 'https://github.com/users/butaosuinu/projects/3'
  assert_success
  assert_golden scenario-project-basic
}

@test "scenario-project-status-all: --project-status all bypasses Status filter" {
  use_fixture scenario-project-status-all
  run_fanout_dry 'https://github.com/users/butaosuinu/projects/3' --project-status all
  assert_success
  assert_golden scenario-project-status-all
}

@test "scenario-project-cross-repo: cross-repo items warn and are dropped" {
  use_fixture scenario-project-cross-repo
  run_fanout_dry 'https://github.com/users/butaosuinu/projects/3'
  assert_success
  assert_golden scenario-project-cross-repo
}
