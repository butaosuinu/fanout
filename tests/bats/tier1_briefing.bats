#!/usr/bin/env bats
#
# We do not source fanout to call write_briefing directly — the script has
# top-level side effects (set -euo pipefail and the main flow at the bottom).
# Instead we run --dry-run, which still writes the briefing to
# /tmp/fanout-<repo_slug>-<NUM>.md, and grep that file before teardown
# wipes it (helpers.bash:55-60).

load helpers

@test "briefing for --agent claude contains Agent Teams hint, /simplify directive, and existing Requirements" {
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100
  assert_success
  local briefing="/tmp/fanout-project_root-101.md"
  grep -q "Optional: Agent Teams" "$briefing"
  grep -q "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1" "$briefing"
  grep -q "run the \`/simplify\` slash command" "$briefing"
  grep -q "Make focused, minimal changes scoped to this single issue" "$briefing"
  grep -q "Closes #101" "$briefing"
}

@test "briefing for --agent codex omits Agent Teams hint and /simplify directive but keeps Requirements" {
  use_fixture scenario-sub-issue-only
  run_fanout_dry 100 --agent codex
  assert_success
  local briefing="/tmp/fanout-project_root-101.md"
  ! grep -q "Agent Teams" "$briefing"
  ! grep -q "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS" "$briefing"
  ! grep -q "/simplify" "$briefing"
  grep -q "Make focused, minimal changes scoped to this single issue" "$briefing"
  grep -q "Closes #101" "$briefing"
}
