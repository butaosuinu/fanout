#!/usr/bin/env bats
#
# We do not source fanout to call write_briefing directly — the script has
# top-level side effects (set -euo pipefail and the main flow at the bottom).
# Instead we run --dry-run, which still writes the briefing to
# /tmp/fanout-<repo_slug>-<NUM>.md, and grep that file before teardown
# wipes it (helpers.bash:55-60).

load helpers

@test "briefing for --agent claude contains Agent Teams hint, /code-review directive, and existing Requirements" {
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100
  assert_success
  local briefing="/tmp/fanout-project_root-101.md"
  grep -q "Optional: Agent Teams" "$briefing"
  grep -q "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1" "$briefing"
  grep -q "run the \`/code-review\` slash command" "$briefing"
  grep -q "Make focused, minimal changes scoped to this single issue" "$briefing"
  grep -q "Closes #101" "$briefing"
}

@test "briefing for --agent codex omits Agent Teams hint and /code-review directive but keeps Requirements" {
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100 --agent codex
  assert_success
  local briefing="/tmp/fanout-project_root-101.md"
  ! grep -q "Agent Teams" "$briefing"
  ! grep -q "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS" "$briefing"
  ! grep -q "/code-review" "$briefing"
  grep -q "Make focused, minimal changes scoped to this single issue" "$briefing"
  grep -q "Closes #101" "$briefing"
}

@test "Go settings: missing config files keep default briefing behavior" {
  skip_unless_fanout_go
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100
  assert_success
  [[ "$output" != *"settings "* ]]
  local briefing="/tmp/fanout-project_root-101.md"
  grep -q "Open a pull request with \"Closes #101\"" "$briefing"
  grep -q "run the \`/code-review\` slash command" "$briefing"
  grep -q "Optional: Agent Teams" "$briefing"
}

@test "Go settings: --no-auto-pr removes PR creation requirement" {
  skip_unless_fanout_go
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100 --no-auto-pr
  assert_success
  local briefing="/tmp/fanout-project_root-101.md"
  ! grep -q "Open a pull request with \"Closes #101\"" "$briefing"
  grep -q "When implementation passes tests, commit and push the branch" "$briefing"
}

@test "Go settings: briefing toggles are last-wins on the CLI" {
  skip_unless_fanout_go
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100 --no-agent-teams-hint --agent-teams-hint
  assert_success
  grep -q "Optional: Agent Teams" /tmp/fanout-project_root-101.md

  run_fanout_dry 100 --agent-teams-hint --no-agent-teams-hint
  assert_success
  ! grep -q "Optional: Agent Teams" /tmp/fanout-project_root-101.md
}

@test "Go settings: invalid env value warns and falls back to default" {
  skip_unless_fanout_go
  use_fixture scenario-sub-issue-only
  export FANOUT_AUTO_PR=maybe
  run_fanout_dry 100
  assert_success
  [[ "$output" == *"settings env FANOUT_AUTO_PR: invalid boolean \"maybe\""* ]]
  grep -q "Open a pull request with \"Closes #101\"" /tmp/fanout-project_root-101.md
}

@test "Go settings: disabled PR review gate adds bypass notice for Claude" {
  skip_unless_fanout_go
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100 --no-pr-review-gate
  assert_success
  local briefing="/tmp/fanout-project_root-101.md"
  grep -q "The PR review gate is disabled for this fanout run" "$briefing"
  grep -q "FANOUT_SKIP_PR_REVIEW=1 gh pr create" "$briefing"
}
