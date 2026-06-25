---
title: Workflow
linkTitle: Workflow
description: "The wave-driven loop — grow an issue tree, fan it out, select children, then merge and fold panes away."
weight: 30
kanji: 流
yomi: workflow
---

## The loop at a glance

fanout's day-to-day shape is a loop, not a one-shot command. You grow a parent issue with OPEN children, fan them out into parallel panes, watch the panes work, fold the finished ones away, and rerun for the next batch. Rather than finishing everything at once, you advance the children whose blockers have cleared, batch by batch:

1. **Grow the issue tree.** Create a parent issue plus linked child issues. The bundled `fanout-issues` skill turns a plan into this fanout-ready shape for you — see [Agent Integrations]({{< relref "/docs/agents" >}}).
2. **Fan out.** `fanout <parent>` creates one tmux pane + git worktree per OPEN child and launches the agent CLI in each.
3. **Monitor.** Follow issue and PR state across the panes — see [Monitoring]({{< relref "/docs/monitoring" >}}).
4. **Merge.** Take a finished child branch in with `--merge <NUM>`.
5. **Clean up.** `--cleanup` folds away every recorded child whose issue is closed or whose PR is merged.
6. **Next wave.** Rerun fanout — typically with `--unblocked-only` — and the children whose blockers just closed become the next batch.

The rest of this page walks the loop in order.

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

Four flags shape which OPEN children a run targets:

| Flag | Effect |
|---|---|
| `--limit <N>` | Cap this invocation to N children. A rerun command is printed for the rest. |
| `--only <list>` | Allow-list: fan out only these numbers. Numbers not in the parent's OPEN child set are warned and ignored. |
| `--skip <list>` | Fan out everything except these children. |
| `--include <list>` | Force-add children that auto-detection (Sub-issues API + task list) misses. CLOSED or nonexistent numbers are warned and skipped. Included numbers are added first, then `--only`/`--skip` filter the result. |

```bash
fanout 123 --limit 3
fanout 123 --only 4,7,8,10
fanout 123 --skip 6,9 --limit 3
fanout 123 --include 4,7
```

> `--include` is the hole the bundled Claude/Codex skills fill automatically: they read the parent body for implicit references — close keywords like `Closes #N`, dependency wording like `Depends on #N`, plain bullets, Japanese idioms — and forward the approved numbers through this flag. Pass it yourself when running the CLI outside an agent session.

## Wave progression with `--unblocked-only`

When several children depend on one another, you don't want to hold everyone back until a blocker clears. You want to run the children that can already proceed in parallel and add the next one each time a blocker closes. That staged execution is a wave.

`--unblocked-only` fans out only the children whose blockers are all CLOSED. Children with any OPEN blocker are reported as `deferred (blocked)` and skipped for this run — nothing is created for them, so there is nothing to undo.

Because reruns also skip children already recorded in `.fanout/state.json`, advancing the project is just running the same command again each time a blocker PR merges: Wave 1 → Wave 2 → … with no manual bookkeeping.

```bash
fanout 123 --unblocked-only

fanout 123 --unblocked-only --limit 3
```

The second form caps each wave while letting fanout pick the next unblocked batch.

## Label watcher

The watcher runs only while the no-argument TUI console is open. It is off by
default, and only user config or environment variables can enable it; repo
config cannot opt a checkout into background launches.

```bash
# One shell
export FANOUT_WATCHER=1
export FANOUT_WATCHER_AGENT=codex
fanout
```

Add `fanout:auto` to a trusted issue to queue it. On the next cycle fanout
swaps that label to `fanout:running`, then launches either a standalone pane
for an issue with no OPEN children or a normal parent fan-out for an issue with
OPEN children. Parent fan-outs use `--unblocked-only`; every watcher launch
counts against `watcherMaxSessions`. If blocked children or the session cap
leaves work for later, fanout swaps `fanout:running` back to `fanout:auto` so a
later cycle retries the parent automatically.

For parent fan-outs, `fanout <parent> --merge <child>`, `--close`, and
`--cleanup` remove `fanout:running` best-effort. For standalone watcher panes,
use the TUI lifecycle keys (`m`, `c`, `x`); the public CLI parent argument does
not target reserved `@watch` rows. To queue a fresh run after a standalone pane
or fully cleaned parent, add `fanout:auto` again. Do not apply the trigger label
to untrusted issues: the labeled issue and any OPEN children it launches become
agent briefings.

This watcher is separate from [#107](https://github.com/butaosuinu/fanout/issues/107):
it discovers labeled issues across the repository and starts one-shot sessions.
#107 remains the skill-led loop for revisiting children under a known parent.

## Issue-less plan fan-out

Sometimes the work is already broken down from a brainstorm or notes, and it
isn't worth opening GitHub child issues for it. You want to feed that local
breakdown straight into parallel panes without building an issue tree. That is
what `fanout plan` is for. The workflow is the same loop, but the source of
truth is a JSON spec instead of an issue tree:

1. Write or select a plan spec with `version: 1`, `plan.slug`, `plan.title`, and
   `tasks[]` entries (`id`, `title`, `briefing`, optional `wave`,
   `blocked_by`, `branch`, `display_name`, and `slug`).
2. Preview first with `fanout plan <spec> --dry-run --agent <agent>`.
3. Run live with `fanout plan <spec> --agent <agent>`, usually adding
   `--unblocked-only` when any task has `blocked_by`.
4. Monitor with the TUI/dashboard or `fanout plan <slug> --status
   [--format table]`.
5. Merge or fold away tasks with task IDs:
   `fanout plan <slug> --merge <task-id>`, `--close <task-id>`, or
   `--cleanup`.
6. Rerun the saved slug for the next wave.

```bash
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --dry-run
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --unblocked-only
fanout plan launch-plan --status --format table
fanout plan launch-plan --merge base-types
fanout plan launch-plan --cleanup
```

Live runs save the spec to `.fanout/plans/<slug>.json`; rows are stored in
`.fanout/state.json` under parent `plan:<slug>` with `taskId` and `issueNum: 0`.
`blocked_by` dependencies are considered complete only after the dependency
task's explicit or generated branch has a merged PR. Plan task briefings avoid
GitHub issue-closing footers and tell agents to end PR bodies with
`Plan: <slug> / Task: <id>`.

The bundled agent wrappers can create the spec for you. In Claude Code,
`/fanout plan <path-or-plan>` routes to the `fanout-plan` skill; in Codex, use
`$fanout-plan` or ask for `fanout plan`. The skill summarizes the dry-run
before live launch unless confirmation was explicitly skipped.

## Sibling coordination (peer messaging)

When sibling panes touch the same interface in parallel, they often need to
share progress and decisions. But when a parent is fanned out, each child is an
independent agent session in its own pane — by default the panes cannot see each
other. Opt in per run with `--team`: fanout (best-effort) injects a
"Coordinating with your sibling panes" section into each child's standard
briefing and seeds a per-parent peer registry so the siblings know about one
another. (`--codex-plan-mode` children are seeded into the registry but receive
the minimal Plan-Mode briefing, so the roster section is not added to them.)

Inside any fanned pane, `fanout msg` auto-detects which child (or parent) you
are and talks over a per-parent SQLite bus at
`/tmp/fanout-<repo>-<parent>.db` (override with `FANOUT_DB_PATH`). The bus
carries a shared `board` (broadcast to everyone) plus `1:1` messages addressed
with `--to <issue>`. Coordination is pull-based — nothing nudges a pane unless
you ask it to.

The plan lane supports `--team` too (`fanout plan <spec> --team`). It behaves
identically, except issue-less plan tasks have no `#N`, so peers are addressed
by **task id**: `fanout msg send --to <task-id> "<body>"` messages a sibling
task and `fanout msg peers` lists the live task ids. The plan bus lives at
`/tmp/fanout-<repo>-plan-<slug>.db`, and `--team` is not combinable with the
plan read/lifecycle modes (`--status` / `--close` / `--merge` / `--cleanup`).

| Verb | Effect |
|---|---|
| `peers` | List the sibling panes known for this parent. |
| `inbox [--mark-read]` | Show your unread 1:1 messages (optionally mark them read). |
| `board [--all]` | Show recent broadcasts (all of them with `--all`). |
| `send --to <N> <body>` | Send a 1:1 message to sibling `#N`. |
| `post [--kind K] <body>` | Post a broadcast to the shared board. |
| `mark-read [--all]` | Mark messages read. |
| `register` | (Re-)seed yourself into the peer registry. |
| `nudge <N>` | Best-effort `send-keys` hint into peer `#N`'s pane — only when its agent is running, and it never touches the DB. |

This works the same for `claude` and `codex` panes. It is distinct from Claude
Code Agent Teams, which coordinates teammates inside a single session; peer
messaging coordinates separate fanout panes. See the
[CLI Reference]({{< relref "/docs/cli" >}}) or `fanout msg --help` for the full
surface.

> **Security.** The bus is a **plaintext** SQLite file under `/tmp`. fanout
> creates it `0600` (owner-only) and refuses one that is group/world-readable or
> owned by another user, but `/tmp` is shared scratch space — **do not put
> secrets, tokens, or credentials in messages.** The DB is throwaway; delete
> `/tmp/fanout-<repo>-<parent>.db*` when you are done.

## Naming and branches

By default each child gets the worktree slug `slugify(title)-<issueNum>` and the branch `fanout/<slug>`. Three flags override this:

- `--name <NUM>=<slug>[|<display>[|<branch>]]` — name one child's worktree slug stem, pane title, and branch directly. The three pipe-separated segments may each be empty, but at least one must be non-empty. fanout appends `-<NUM>` to slug stems that do not already carry it; the third segment overrides the generated branch name. Repeatable — one per target.
- `--branch-prefix <prefix>` — change generated branch names for the whole run.
- `--base-branch <branch>` — override the base branch the children branch from. Bare local branch names and `origin/<branch>` are both supported.
- `--no-refresh` — skip the base-branch refresh. By default fanout refreshes the base with `git fetch --quiet --no-tags` plus a fast-forward update before branching; local plan/manual panes automatically skip refresh and use the current local branch/`HEAD` when no `origin` remote exists.

```bash
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'
fanout 123 --name 8='feat-x|Feature X|feat/issue-8-x'   # all three segments
fanout 123 --name 9='||release/v2.0'                    # branch override only

fanout 123 --base-branch release/v2 --branch-prefix fanout/release/

fanout 123 --no-refresh
```

> The bundled Claude/Codex integrations generate `--name` flags from the issue title and body without any extra API call; pass `--name` yourself to override. Rerun idempotency comes from `.fanout/state.json`, not from names.

## Project mode

The positional argument accepts a Projects v2 URL in place of a parent issue number — `https://github.com/users/<owner>/projects/<n>` or `https://github.com/orgs/<org>/projects/<n>`. The canonical `/views/<id>` suffix and trailing query strings are also accepted, so copy/paste from the browser address bar works. In this mode children come from the Project's items instead of the Sub-issues + task-list union.

```bash
fanout https://github.com/users/<owner>/projects/<n>

fanout https://github.com/orgs/<org>/projects/<n> --project-status "In Progress"

fanout https://github.com/users/<owner>/projects/<n> --project-status all
```

- **Default filter is `Status == Todo`.** Pass `--project-status "<name>"` to pick a different single-select value, or `--project-status all` to disable the filter and include every item (Done, no status, etc.).
- **No parent body means no implicit-children salvage.** The `Closes #N` / dependency phrases the skills normally surface don't exist here — the Project is the source of truth. Use `--include` to force-add anything the Project happens to omit.
- **Single-repo only.** Items whose repository differs from the current git repository are warned and skipped.
- **No Status field on the Project?** fanout warns and falls back to every item regardless of `--project-status`.
- **Blockers** come only from the child body's `## Blocked by` section; the `(blocked by #X)` task-list trailer doesn't exist without a parent body. The `blocked` label stays a weak signal here too — a child carrying only the label is warned and treated as unblocked.

> Project mode requires the `gh` CLI to carry the `read:project` scope — see [Installation]({{< relref "/docs/installation" >}}).

## Merging and folding panes away

The lifecycle commands operate only on entries recorded in `.fanout/state.json`. They do not discover arbitrary worktrees by scanning the filesystem.

- `fanout <parent> --merge <NUM>` — runs `git -C <project-root> merge --ff-only <recorded-branch>`. If the merge is not a fast-forward, fanout reports the git error and stops; it never starts an editor or a conflict-resolution flow.
- `fanout <parent> --close <NUM>` — removes the recorded worktree with `git worktree remove <path> --force`, kills the recorded tmux pane when it is still present, removes the state entry, and runs `git worktree prune`.
- `fanout <parent> --cleanup` — queries the recorded children and closes any child whose issue is `CLOSED` or whose closed-by PR list contains a `MERGED` PR. Pending children remain recorded.

```bash
fanout 123 --merge 4
fanout 123 --close 4

fanout 123 --cleanup
```

> Like `--status`, these commands honor `FANOUT_STATE_PATH`; otherwise they use `<git-root>/.fanout/state.json`.

## Run control

| Flag | Effect |
|---|---|
| `--session <name>` | Target a named tmux session instead of the invoking pane. |
| `--sleep <seconds>` | Rate limit between successful child launches (default 4 seconds). It is not a retry/backoff knob. |
| `--dry-run` | Preview the git worktree + tmux commands without creating worktrees, panes, state rows, or briefing files. |
| `--debug` | Print extra diagnostic logging. |

```bash
fanout 123 --session work-repo
fanout 123 --sleep 8
fanout 123 --dry-run
```

Every flag on this page — and the rest of the surface — is in the [CLI Reference]({{< relref "/docs/cli" >}}).
