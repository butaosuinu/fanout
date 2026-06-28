---
title: Quickstart
linkTitle: Quickstart
description: "Your first fan-out in five minutes — bring up parallel panes from a parent issue, and see what happens in between."
weight: 20
kanji: 開
yomi: quickstart
---

## Your first fan-out (five minutes)

Suppose your parent issue has five OPEN sub-issues. Instead of picking them off one at a time and queueing the rest, fanout starts them all at once: one tmux pane per child, each with its own git worktree, so five agents work in parallel without colliding on each other's edits.

Pick the agent used for child panes, then start (or attach) a tmux session in the target repository:

```bash
# Use Claude for child panes
export FANOUT_AGENT=claude
```

Preview the plan first. `--dry-run` prints the git worktree + tmux commands without executing them. It creates no worktrees, panes, state rows, or briefing files:

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

> The pane-creation flow needs `gh`, `git`, and `tmux` on your `PATH`. fanout checks the dependencies at startup and prints install hints when one is missing — see [Installation]({{< relref "/docs/installation" >}}).

## How child issues are declared

Here is how to write your issue tree so fanout picks it up. fanout enumerates children by taking the union of two sources:

- issues formally linked to the parent as **Sub-issues**, and
- **task-list references in the parent body** — any line matching `- [ ] #NUM ...` contributes `#NUM`.

```text
- [ ] #124 Extract the parser
- [ ] #125 Add the --format flag
- [ ] other/repo#126 Upstream fix   <- skipped: same-repo references only
```

Task-list references are same-repo only; `owner/repo#NUM` is skipped. Children may be declared via either source or both — the union deduplicates them. Only children whose state is OPEN are processed; closed children never get a pane.

So you can hang five sub-issues off the parent, write a five-line task list in the parent body, or mix both. Either way, every OPEN child becomes a parallel pane.

## What happens during a run

A live run walks these steps:

1. Verifies `gh`, `git`, and `tmux` are installed.
2. Resolves the repository root, the current tmux session and invoking pane, and the agent from `--agent` or `FANOUT_AGENT`.
3. Enumerates children as the union of Sub-issues and parent task-list rows; only OPEN children are processed.
4. Reads `.fanout/state.json` and skips children whose `(parent, issueNum)` pair is already recorded.
5. Writes a briefing for each target with the issue body and a short Requirements checklist.
6. Creates `.fanout/worktrees/<slug>/` from the refreshed base branch, creates the child pane with `tmux split-window` without selecting it, sets the pane title, re-lays out the window into a comfortable-width grid (falling back to `main-vertical` then `tiled`), then sleeps `--sleep` seconds (default 4) before the next child.
7. Prints a summary of created / skipped / deferred / failed counts.

> The pane launch prompt stays short on purpose: the full issue body lives in the briefing file, and the agent is told to read it from there. `deferred` only appears with `--unblocked-only`, which holds back children that still have an OPEN blocker.

> In `--dry-run`, fanout prints the future briefing path and size for review, but writes the file only during the live run.

## Safe to rerun (idempotency)

Idempotent means running the same operation many times leaves the same result as running it once. `.fanout/state.json` records every launched pane keyed by `(parent, issueNum)`, so rerunning the same parent skips children that already have a recorded fanout pane and only creates what is missing:

```bash
# Rerun after the first batch — already-recorded children are skipped
fanout 123
```

An existing `.fanout/worktrees/<slug>/` directory without a state row is also treated as already fanned — a migration fallback for pre-state runs or interrupted launches.

## Choosing the agent

An agent name must be resolvable: pass `--agent claude`, `--agent codex`, or set `FANOUT_AGENT`.

```bash
fanout 123 --agent claude
fanout 123 --agent codex
export FANOUT_AGENT=claude   # applies to every following run in this shell
```

Unknown agents fail before any pane is created; in live mode, fanout also checks that the selected agent CLI is installed. Run fanout from inside a tmux session — it creates child panes directly with `tmux split-window`, targeting the invoking pane unless `--session` names another session. The one exception is `fanout` with no arguments (the TUI console), which can start from a plain shell — see [Monitoring]({{< relref "/docs/monitoring" >}}).

Next: dig into the flags that shape a run in the [Workflow]({{< relref "/docs/workflow" >}}).
