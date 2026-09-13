---
description: Start the fanout TUI console, fan out a parent GitHub issue / GitHub Projects v2 board, or route an implementation plan through fanout plan.
argument-hint: "[parent-issue | project-url | dashboard | plan [path]] [--go] [--wait] [extra fanout flags]"
---

Follow the `fanout` skill (`~/.claude/skills/fanout/SKILL.md`). Its
"Invocation" section says how to read the arguments below: an empty argument
list opens the console, `dashboard` and `plan` short-circuit to their own
lanes, lifecycle flags run directly, and everything else is a pane-creation run
whose `--go` (skip confirmation) and `--wait` (wait-and-continue) are wrapper
flags that never reach the CLI.

Arguments: `$ARGUMENTS`
