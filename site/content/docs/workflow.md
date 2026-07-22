---
title: Workflow
linkTitle: Workflow
description: "The wave-driven loop — grow an issue tree, fan it out, and merge children as their blockers clear before moving to the next wave."
weight: 30
kanji: 流
yomi: workflow
---

## The loop at a glance

fanout's day-to-day shape is a loop, not a one-shot command. You grow a parent issue with OPEN children, fan them out into parallel panes, watch the panes work, fold the finished ones away, and rerun for the next batch. Rather than finishing everything at once, you advance the children whose blockers have cleared, batch by batch:

1. **Grow the issue tree.** Create a parent issue plus linked child issues. The bundled `fanout-issues` skill turns a plan into this fanout-ready shape for you — see [Agent Integrations]({{< relref "/docs/agent-integrations" >}}).
2. **Fan out.** `fanout <parent>` creates one tmux pane + git worktree per OPEN child and launches the agent CLI in each.
3. **Monitor.** Follow issue and PR state across the panes — see [Monitoring]({{< relref "/docs/monitoring" >}}).
4. **Merge.** Take a finished child branch in with `--merge <NUM>`.
5. **Clean up.** `--cleanup` folds away every recorded child whose issue is closed or whose PR is merged.
6. **Next wave.** Rerun fanout — typically with `--unblocked-only` — and the children whose blockers just closed become the next batch.

## Writing a fanout-ready issue tree

Children can be declared through GitHub **Sub-issues**, through the parent body's **task list** (`- [ ] #NUM ...`), or both — fanout takes the union of the two sources:

```text
- [ ] #4 Extract the parser
- [ ] #7 Port the formatter (blocked by #4)
```

Blockers — the dependencies that drive wave progression below — are read from two shapes:

- **The child body's `## Blocked by` section** — issue numbers are collected up to the next blank line.
- **The parent task-list row trailer** — a trailing `(blocked by #X, #Y)` on the child's row, as on `#7` above.

```text
## Blocked by
- #4
- #7
```

> The `blocked` label is only a weak signal: fanout logs it but does not infer specific blocker numbers from a bare label.

## Selecting children

Four flags narrow which OPEN children a run targets: `--limit`, `--only`, `--skip`, and `--include`. Each flag's effect is summarized in the [CLI Reference]({{< relref "/docs/cli" >}}).

## Wave progression with `--unblocked-only`

When several children depend on one another, you don't want to hold everyone back until a blocker clears. You want to run the children that can already proceed in parallel and add the next one each time a blocker closes. That staged execution is a wave.

`--unblocked-only` fans out only the children whose blockers are all CLOSED. Children with any OPEN blocker are reported as `deferred (blocked)` and skipped for this run — nothing is created for them, so there is nothing to undo.

Because reruns also skip children already recorded in `.fanout/state.json`, advancing the project is just running the same command again each time a blocker PR merges: Wave 1 → Wave 2 → … with no manual bookkeeping.

{{< diagram "waves" >}}

```bash
fanout 123 --unblocked-only

fanout 123 --unblocked-only --limit 3
```

The second form caps each wave while letting fanout pick the next unblocked batch.

## Label watcher

The watcher is an opt-in launcher that runs only while the no-argument TUI console is open, turning a `fanout:auto` label on a trusted issue into a one-shot session. It is off by default, and only user config or environment variables can enable it (repo config cannot opt a checkout into automatic launches). See [Watcher]({{< relref "/docs/watcher" >}}) for the enablement steps and label conventions.

## Issue-less plan fan-out

Sometimes the work is already broken down from a brainstorm or notes, and it isn't worth opening GitHub child issues for it. You want to feed that local breakdown straight into parallel panes without building an issue tree. That is what `fanout plan` is for. The workflow is the same loop, but the source of truth is a JSON spec instead of an issue tree:

1. Write a plan spec (JSON), or pick an existing one.
2. Preview first with `fanout plan <spec> --dry-run --agent <agent>`.
3. Run live with `fanout plan <spec> --agent <agent>`; add `--unblocked-only` too when a task has `blocked_by`.
4. Monitor via the TUI, the dashboard, or `fanout plan <slug> --status [--format table]`.
5. Merge or fold away tasks by task ID: `fanout plan <slug> --merge <task-id>`, `--close <task-id>`, `--cleanup`.
6. Rerun the saved slug for the next wave.

The spec format and the state that a live run records are in the [CLI Reference]({{< relref "/docs/cli" >}}).

```bash
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --dry-run
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --unblocked-only
fanout plan launch-plan --status --format table
fanout plan launch-plan --merge base-types
fanout plan launch-plan --cleanup
```

## Sibling coordination (peer messaging)

When sibling panes touch the same interface in parallel, they often need to share progress and decisions. But when a parent is fanned out, each child is an independent agent session in its own pane — by default the panes cannot see each other. Opt in per run with `--team`: fanout (best-effort) injects a "Coordinating with your sibling panes" section into each child's standard briefing and seeds a per-parent peer registry so the siblings know about one another. Codex children in Plan Mode are seeded too, but receive the minimal Plan-Mode briefing; Plan Mode takes precedence and disables their Codex team bridge.

Inside a fanned-out pane, `fanout msg` auto-detects which child (or parent) you are and talks over a per-parent bus.

```bash
fanout msg peers                # who are my siblings?
fanout msg post "auth interface merged — rebase before touching login"
fanout msg send --to 7 "renamed SessionStore to PaneStore"
fanout msg inbox --mark-read
```

The full verb table and how to address plan tasks are covered in the [CLI Reference]({{< relref "/docs/cli#fanout-msg" >}}).

Messages persist in the bus and siblings read them at their own checkpoints. On top of that pull loop, `--team` adds a push lane for `claude` panes and fresh non-Plan `codex` panes, delivering new messages as they arrive; panes without a working lane fall back to pull (`inbox` / `board`) plus `nudge`. How each lane delivers — and how to recover after an injection failure — is in the [CLI Reference]({{< relref "/docs/cli#fanout-msg" >}}).

It works the same for `claude`, `codex`, and `opencode` panes, and is distinct from Claude Code Agent Teams, which coordinates teammates inside a single session.

> **Security.** The bus is a **plaintext** SQLite file under `/tmp`. fanout creates it `0600` (owner-only) and refuses one that is group/world-readable or owned by another user, but `/tmp` is shared scratch space — **do not put secrets, tokens, or credentials in messages.**

## Naming and branches

By default each child gets the worktree slug `slugify(title)-<issueNum>` and the branch `fanout/<slug>`. Override the names with `--name` and `--branch-prefix`; `--base-branch` and `--no-refresh` control which base the children branch from instead.

```bash
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'
```

See the [CLI Reference]({{< relref "/docs/cli" >}}) for details.

## Project mode

The positional argument accepts a Projects v2 URL in place of a parent issue number (`https://github.com/users/<owner>/projects/<n>` or `https://github.com/orgs/<org>/projects/<n>`). The canonical `/views/<id>` suffix and trailing query strings are also accepted, so copy/paste from the browser address bar works. In this mode children come from the Project's items instead of the Sub-issues + task-list union.

```bash
fanout https://github.com/users/<owner>/projects/<n>

fanout https://github.com/orgs/<org>/projects/<n> --project-status "In Progress"

fanout https://github.com/users/<owner>/projects/<n> --project-status all
```

The default filter is items with `Status == Todo`; change it with `--project-status` (see the [CLI Reference]({{< relref "/docs/cli" >}})). Blockers come only from the child body's `## Blocked by` section — there is no parent body, so the `(blocked by #X)` task-list trailer does not exist here, and a child carrying only the `blocked` label is warned and treated as unblocked.

> Project mode requires the `gh` CLI to carry the `read:project` scope — see [Installation]({{< relref "/docs/installation" >}}).

## Merging and folding panes away (lifecycle)

Lifecycle commands operate only on entries recorded in `.fanout/state.json`.

```bash
fanout 123 --merge 4
fanout 123 --close 4

fanout 123 --cleanup
```

The full surface — the flags on this page plus the exact behavior of the lifecycle commands — is in the [CLI Reference]({{< relref "/docs/cli" >}}).
