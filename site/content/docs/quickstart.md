---
title: Quickstart
linkTitle: Quickstart
description: "Your first fan-out in five minutes — from a parent issue to parallel panes, and what actually happens in between."
weight: 20
kanji: 開
yomi: quickstart
---

## Your first fan-out (five minutes)

fanout fans a GitHub parent issue's OPEN sub-issues out into one tmux pane per child. Each pane gets its own git worktree and an agent CLI launched with a prompt that points at a per-issue briefing file.

Start (or attach) a tmux session in the target repository, then pick the agent used for child panes:

```bash
# Use Claude for child panes
export FANOUT_AGENT=claude
```

Preview the plan first. `--dry-run` prints the git worktree + tmux commands without executing them — it creates no worktrees and no panes:

```bash
# Preview the git worktree + tmux commands without executing them
fanout 123 --dry-run
```

If the plan looks right, run it for real:

```bash
# Fan out all OPEN sub-issues of #123
fanout 123
```

Each child gets a pane in the current tmux session, an isolated worktree under `.fanout/worktrees/<slug>/`, and the selected agent started with a one-line briefing prompt.

> The pane-creation flow needs `gh`, `git`, and `tmux` on your `PATH`. fanout checks the dependencies at startup and prints install hints on failure — see [Installation]({{< relref "/docs/installation" >}}).

## How child issues are declared

fanout enumerates children by taking the union of two sources:

- issues formally linked via the **Sub-issues API** (`gh api repos/{owner}/{repo}/issues/<N>/sub_issues`), and
- **task-list references in the parent body** — any line matching `- [ ] #NUM ...` contributes `#NUM`.

```text
- [ ] #124 Extract the parser
- [ ] #125 Add the --format flag
- [ ] other/repo#126 Upstream fix   <- skipped: same-repo references only
```

Task-list references are same-repo only; `owner/repo#NUM` is skipped. Body-sourced numbers are hydrated via `gh issue view`. Children may be declared via either source or both — the union deduplicates them — and only children whose state is OPEN are processed; closed children never get a pane.

## What happens during a run

A live run walks these steps:

1. Verifies `gh`, `git`, and `tmux` are installed.
2. Resolves the repository root with `git rev-parse --show-toplevel`, the current tmux session and invoking pane, and the agent from `--agent` or `FANOUT_AGENT`.
3. Enumerates children as the union of Sub-issues and parent task-list rows; only OPEN children are processed.
4. Reads `.fanout/state.json` and skips children whose `(parent, issueNum)` pair is already recorded.
5. Writes a briefing for each target to `/tmp/fanout-<repo>-<NUM>.md` with the issue body and a short Requirements checklist.
6. Creates `.fanout/worktrees/<slug>/` from the refreshed base branch, creates the child pane with `tmux split-window` without selecting it, sets the pane title, applies `tmux select-layout tiled`, then sleeps `--sleep` seconds (default 4) before the next child.
7. Prints a summary of created / skipped / deferred / failed counts.

> The pane launch prompt stays short on purpose: the full issue body lives in the briefing file at `/tmp/fanout-<repo>-<NUM>.md`, and the agent is told to read it from there. `deferred` only appears with `--unblocked-only`, which holds back children that still have an OPEN blocker.

## Safe to rerun (idempotency)

`.fanout/state.json` records every launched pane keyed by `(parent, issueNum)`, so rerunning the same parent skips children that already have a recorded fanout pane and only creates what is missing:

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

Unknown agents fail before any pane is created; in live mode, fanout also checks that the selected agent CLI is installed. fanout must be invoked from inside a tmux session — it creates child panes directly with `tmux split-window`, targeting the invoking pane unless `--session` names another session. The one exception is the TUI console (`fanout` with no arguments), which can start from a plain shell — see [Monitoring]({{< relref "/docs/monitoring" >}}).

Next: dig into the flags that shape a run in the [Workflow]({{< relref "/docs/workflow" >}}).
