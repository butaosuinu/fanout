---
title: Watcher
linkTitle: Watcher
description: "The opt-in, TUI-resident launcher that turns a `fanout:auto` label into an unattended session: enabling it, the label lifecycle, the session budget, and cleanup."
weight: 50
kanji: 巡
yomi: watcher
---

The watcher is an opt-in launcher that turns a `fanout:auto` label into a one-shot fanout session, without anyone running the CLI by hand. It runs only while the no-argument TUI console is open — leave the console and the watcher stops with it. It is not a cron or webhook service: it discovers labeled issues across the repository and launches each one once, rather than continuously revisiting a parent's children.

Reach for it in place of manually rerunning `fanout <parent> --unblocked-only` each wave (see [Workflow]({{< relref "/docs/workflow" >}})): label a trusted issue once, and the watcher picks it up on its own.

## Enable it

Only user config or the `FANOUT_WATCHER` environment variable can turn the watcher on. Repo config cannot enable it — a checked-out repo must never start launching sessions on its own — though repo config can still set `watcherTriggerLabel`, `watcherRunningLabel`, `watcherIntervalSeconds`, `watcherAgent`, and `watcherMaxSessions`. None of these settings has a CLI flag. See [Settings]({{< relref "/docs/settings" >}}) for the full key table and config file locations.

```bash
export FANOUT_WATCHER=1
export FANOUT_WATCHER_AGENT=codex
fanout
```

The watcher runs only while this no-argument TUI console stays open. Exit the console and it stops.

## The label lifecycle

Add the trigger label — `fanout:auto` by default — to a trusted issue. On its next cycle the watcher swaps the label to the running label (`fanout:running` by default) before launching a session for that issue. If the label swap fails, the watcher does not launch: it fails closed rather than risk a session running with no recorded running state.

The watcher polls every `watcherIntervalSeconds`, 60 seconds by default and never less than 20.

## What it launches

What launches depends on the issue's children:

- An issue with OPEN children fans out the equivalent of `--unblocked-only` — a parent fan-out that creates one pane per unblocked child.
- An issue with no OPEN children — including one whose children are all CLOSED — launches as a standalone pane under the reserved `@watch` parent.

Watcher launches follow `childPlanMode`, off by default. Turning it on stalls every launched Session at the agent's plan approval prompt — the watcher is unattended, so leave the key off unless someone is around to approve the plans. See [Settings]({{< relref "/docs/settings" >}}).

## Session budget

`watcherMaxSessions` caps live panes, not launches. Each cycle the watcher counts every live (non-shell) fanout pane in the repository, regardless of who launched it, and launches only while that count stays below the cap. The default is 4; `watcherMaxSessions=0` means unlimited.

A parent fan-out takes one slot per launched child, and a slot frees up when its pane closes. When blocked children or the session cap leave work outstanding, the watcher reverts the label from `fanout:running` back to `fanout:auto`, so that issue is retried automatically on a later cycle.

## Watch it in the TUI

The watcher has no dedicated pane or screen. Instead, the monitor screen footer shows a one-line status:

```text
watch: on label=fanout:auto running last=12:03:45 launched=2 err=-
```

The line is absent only when the watcher is not enabled. Panes it launches appear and behave like any other row in the monitor table — see [Monitoring]({{< relref "/docs/monitoring" >}}).

Press `s` to open the settings popup and change `watcher`, the interval, or the label without leaving the console; the next cycle picks up the change.

## Clean up and requeue

For a parent fan-out, `fanout <parent> --merge <child>`, `--close`, and `--cleanup` remove `fanout:running` on a best-effort basis, the same as for any other fanned-out parent.

A standalone `@watch` pane has no parent argument to run those commands against — the public CLI does not accept rows for the reserved `@watch` parent — so fold it away with the TUI lifecycle keys instead: `c` or `x` closes the pane (optionally dropping its worktree and branch), `m` merges its branch, and `X` cleans up merged or closed panes.

To hand an issue back to the watcher, fold its panes away first — using the commands or keys above — and then re-add `fanout:auto`. While an issue still has recorded panes, the watcher treats it as already running and skips it, so re-adding the label before you fold the panes launches nothing.

## The trigger label is a prompt-injection boundary

> **Security.** The labeled issue's body and the body of every OPEN child it launches become an agent's briefing verbatim. Treat `fanout:auto` as an execution request, and apply it only to an issue — and its launchable children — that you trust.
