---
description: Watch the current branch's PR and autonomously handle merge conflicts, failing CI, and review comments until it is mergeable / approved / green.
argument-hint: "[pr-number | pr-url]"
---

Invoke the `pr-watch` skill to monitor and auto-resolve a pull request after it
has been created. See the `pr-watch` skill (`~/.claude/skills/pr-watch/SKILL.md`)
for the full workflow, safety guardrails, and termination conditions.

Arguments: `$ARGUMENTS`

- If `$ARGUMENTS` is empty, target the PR attached to the **current git
  branch** (`gh pr view`). If there is no PR for this branch, tell the user to
  create one first and stop — this command does not create PRs.
- If `$ARGUMENTS` is a PR number (`^#?\d+$`, strip a leading `#`) or a PR URL,
  target that PR instead.

This runs **one monitoring pass** (state → conflicts → CI → review comments →
push), then reports. Conflicts/CI/review are time-delayed and recur, so for
continuous watching wrap it in the dynamic loop: **`/loop /pr-watch`** (no
interval — the skill self-paces with `ScheduleWakeup`: short while CI is
running, long while idle). The loop ends when the PR is merged/closed, reaches
approved + green + no unresolved threads, you stop it, or the skill detects it
is stuck on the same problem (oscillation safety).

Behavior is fully autonomous within guardrails: rebase + auto-resolve confident
conflicts then `git push --force-with-lease`, read failing CI logs and fix the
cause, address review comments in code and reply after pushing. It never
force-pushes others' PRs or protected branches, never auto-merges the PR unless
explicitly told to, and escalates conflicts/CI/review that need human judgment.
