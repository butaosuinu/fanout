---
title: Workflow
linkTitle: Workflow
description: "The wave-driven loop — grow an issue tree, fan it out, select children, then merge and fold panes away."
weight: 30
kanji: 流
yomi: workflow
---

## The loop at a glance

fanout's day-to-day shape is a loop, not a one-shot command. You grow a parent issue with OPEN children, fan them out into parallel panes, watch the panes work, fold the finished ones away, and rerun for the next batch:

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

`--unblocked-only` fans out only the children whose blockers are all CLOSED. Children with any OPEN blocker are reported as `deferred (blocked)` and skipped for this run — nothing is created for them, so there is nothing to undo.

Because reruns also skip children already recorded in `.fanout/state.json`, advancing the project is just running the same command again each time a blocker PR merges: Wave 1 → Wave 2 → … with no manual bookkeeping.

```bash
fanout 123 --unblocked-only

fanout 123 --unblocked-only --limit 3
```

The second form caps each wave while letting fanout pick the next unblocked batch.

## Naming and branches

By default each child gets the worktree slug `slugify(title)-<issueNum>` and the branch `fanout/<slug>`. Three flags override this:

- `--name <NUM>=<slug>[|<display>[|<branch>]]` — name one child's worktree slug stem, pane title, and branch directly. The three pipe-separated segments may each be empty, but at least one must be non-empty. fanout appends `-<NUM>` to slug stems that do not already carry it; the third segment overrides the generated branch name. Repeatable — one per target.
- `--branch-prefix <prefix>` — change generated branch names for the whole run.
- `--base-branch <branch>` — override the base branch the children branch from. Bare local branch names and `origin/<branch>` are both supported.
- `--no-refresh` — skip the base-branch refresh. By default fanout refreshes the base with `git fetch --quiet --no-tags` plus a fast-forward update before branching.

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
- **Blockers** come only from the child body's `## Blocked by` section and the `blocked` label; the `(blocked by #X)` task-list trailer doesn't exist without a parent body.

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
| `--dry-run` | Preview the git worktree + tmux commands without executing them. |
| `--debug` | Print extra diagnostic logging. |

```bash
fanout 123 --session work-repo
fanout 123 --sleep 8
fanout 123 --dry-run
```

Every flag on this page — and the rest of the surface — is in the [CLI Reference]({{< relref "/docs/cli" >}}).
