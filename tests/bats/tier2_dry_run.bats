#!/usr/bin/env bats
#
# Tier 2 — `./fanout --dry-run` golden-output tests.
#
# Each @test picks a scenario fixture under tests/fixtures/<name>, runs
# fanout against it with --dry-run (so no tmux or git worktree side effects
# happen), and diffs the captured output against the matching golden file
# under tests/golden/<name>.dry-run.txt.
#
# The fixture directory contract is documented in tests/bin/gh and
# tests/bin/tmux: the shims read gh-sub-issue-list.json, gh-issue-view-<N>.json,
# tmux-sessions.txt, state files, and GitHub fixtures from $FIXTURE_DIR.
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

@test "scenario-cross-parent-shared: state rows do not leak across parents" {
  # Parents #100 and #200 share child #501. The fixture has a
  # .fanout/state.json row for #100/#501, but (parent, issueNum)
  # idempotency must not alter the command plan for parent #200.
  use_fixture scenario-cross-parent-shared
  run_fanout_dry 200
  assert_success
  assert_golden scenario-cross-parent-shared
}

@test "scenario-legacy-weak-signal: body-task-list child gets direct worktree commands" {
  # The fixture still carries a legacy pre-state child, but action mode no
  # longer migrates or consults old pane prompts. The body-task-list child
  # should still produce a fresh direct-runtime command plan.
  use_fixture scenario-legacy-weak-signal
  run_fanout_dry 600
  assert_success
  assert_golden scenario-legacy-weak-signal
}

@test "scenario-idempotency: state entry causes same-parent child to be skipped" {
  # Phase 2 idempotency comes from .fanout/state.json keyed by
  # (parent, issueNum), not from legacy pane prompts or worktree directory names.
  use_fixture scenario-idempotency
  run_fanout_dry 800
  assert_success
  assert_golden scenario-idempotency
}

@test "scenario-sub-issue-only with --name branch override: dry-run uses deterministic slug and branch" {
  # Reuses the scenario-sub-issue-only fixture. Issue #101 gets a 3-segment
  # --name with slug + display + branch; #102 gets a branch-only --name
  # (||branch). The dry-run output must show:
  #   - issue #101: slug/worktree from the slug segment, pane title from
  #     display-name, and branch from the branch segment.
  #   - issue #102: deterministic title slug plus branch override.
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100 \
    --name "101=feat-x|Feature X|feat/sub-issue-101-x" \
    --name "102=||release/v2.0"
  assert_success
  assert_golden scenario-sub-issue-only-branch-override
}

@test "agent-codex variant of scenario-sub-issue-only: briefing has Codex review gate" {
  # Reuses the scenario-sub-issue-only fixture; the only thing under test is
  # that --agent codex (last-wins over the helper's default --agent claude)
  # produces a Codex-specific briefing without the Claude-only Agent Teams
  # section, so the size lines diverge from scenario-sub-issue-only.dry-run.txt.
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100 --agent codex
  assert_success
  assert_golden scenario-sub-issue-only-codex
}

@test "agent-codex plan mode variant of scenario-sub-issue-only: interactive TUI launch" {
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100 --agent codex --codex-plan-mode
  assert_success
  assert_golden scenario-sub-issue-only-codex-plan
}

@test "Go settings disabled variant of scenario-sub-issue-only: briefing size tracks toggles" {
  skip_unless_fanout_go
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100 --no-auto-pr --no-pr-review-gate --no-briefing-code-review --no-agent-teams-hint
  assert_success
  assert_golden scenario-settings-disabled
}

@test "scenario-prviz-disabled: --no-pr-visualization removes structured PR guidance" {
  skip_unless_fanout_go
  use_fixture scenario-prviz-disabled
  run_fanout_dry 100 --no-pr-visualization
  assert_success
  assert_golden scenario-prviz-disabled
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
