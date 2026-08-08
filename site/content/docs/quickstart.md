---
title: Quickstart
linkTitle: Quickstart
description: "Your first fan-out in five minutes — the steps to bring up parallel panes from a parent issue."
weight: 20
kanji: 開
yomi: quickstart
---

## Your first fan-out (five minutes)

Suppose your parent issue has three OPEN sub-issues. Instead of picking them off one at a time and queueing the rest, fanout starts them all at once: one tmux pane per child, each with its own git worktree, so three agents work in parallel without colliding on each other's edits.

{{< diagram "overview" >}}

No issue tree yet? The bundled `fanout-issues` skill turns a plan into a parent issue with linked children — see [Agent Integrations]({{< relref "/docs/agent-integrations" >}}).

Start (or attach) a tmux session:

```bash
tmux new -A -s work
```

Then, inside the session, pick the agent and move to the target repository:

```bash
# Use Claude for child panes, from the repository root
export FANOUT_AGENT=claude
cd path/to/repo
```

Preview the plan first. `--dry-run` prints the git worktree and tmux commands without executing them. It creates no worktrees, panes, state rows, or briefing files:

```bash
# Preview commands without creating worktrees, panes, state, or briefings
fanout 123 --dry-run
```

If the plan looks right, run it for real:

```bash
# Fan out all OPEN sub-issues of #123
fanout 123
```

Each child gets a pane in the current tmux session, an isolated worktree under `.fanout/worktrees/<slug>/`, and the selected agent started with a one-line prompt that points at the per-issue briefing.

> The pane-creation flow needs `gh`, `git`, and `tmux 3.3+` on your `PATH`. fanout checks the dependencies at startup and prints install hints when one is missing — see [Installation]({{< relref "/docs/installation" >}}).

This page assumes the default tmux backend. To run the same fan-out in an owned [herdr backend]({{< relref "/docs/herdr-backend" >}}) session, add `--backend herdr`; no tmux session is required for that run.

## How child issues are declared

fanout enumerates children as the union of GitHub Sub-issues and task-list rows in the parent body (`- [ ] #NUM ...`), and only children with `state == "OPEN"` are processed. Task-list references are same-repo only — `owner/repo#NUM` is skipped. See [Workflow]({{< relref "/docs/workflow" >}}) for details on how to write it.

## Safe to rerun (idempotency)

`.fanout/state.json` records every launched child, so rerunning the same parent skips children that are already recorded and only creates what is missing:

```bash
# Rerun after the first batch — already-recorded children are skipped
fanout 123
```

## Choosing the agent

An agent name must be resolvable: pass `--agent claude`, `--agent codex`, `--agent opencode`, or set `FANOUT_AGENT`.

```bash
fanout 123 --agent claude
fanout 123 --agent codex
fanout 123 --agent opencode
export FANOUT_AGENT=claude   # applies to every following run in this shell
```

The three agents differ in bundled skills, Plan Mode implementation, and messaging; the matrix in [Agent Integrations]({{< relref "/docs/agent-integrations" >}}) compares them.

Unknown agents fail before any pane is created; in live mode, fanout also checks that the selected agent CLI is installed. Run fanout from inside a tmux session — it creates child panes directly with `tmux split-window`, targeting the invoking pane unless `--session` names another session. The one exception is `fanout` with no arguments (the TUI console), which can start from a plain shell — see [Monitoring]({{< relref "/docs/monitoring" >}}).

Next: dig into the flags that shape a run in the [Workflow]({{< relref "/docs/workflow" >}}).
