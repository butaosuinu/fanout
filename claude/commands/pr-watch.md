---
description: Watch the current branch's PR and autonomously handle merge conflicts, failing CI, and review comments until it is mergeable / approved / green.
argument-hint: "[pr-number | pr-url]"
---

Follow the `pr-watch` skill (`~/.claude/skills/pr-watch/SKILL.md`). With no
arguments it targets the PR of the current branch; a PR number (`^#?\d+$`, strip
a leading `#`) or URL targets that PR instead. One invocation is one pass
(state → conflicts → CI → review comments → push, then a report); for
continuous watching wrap it as `/loop /pr-watch` and the skill self-paces with
`ScheduleWakeup`.

Arguments: `$ARGUMENTS`
