---
title: Troubleshooting
linkTitle: Troubleshooting
description: "Common failures and their fixes, plus the design notes behind state, locking and pane creation."
weight: 80
kanji: 直
yomi: troubleshoot
---

## "fanout must be run inside tmux"

Action mode creates child panes directly with `tmux split-window`, so fanout has to be invoked from inside a tmux session. Start or attach a tmux session in the repository worktree, then rerun fanout. The one exception is TUI mode (`fanout` with no arguments): it can start from a plain shell, because it creates or attaches a fanout-managed tmux session itself — see [Monitoring]({{< relref "/docs/monitoring" >}}).

## "agent is required"

Pass `--agent claude` or `--agent codex`, or set the `FANOUT_AGENT` environment variable:

```bash
fanout 123 --agent claude
FANOUT_AGENT=codex fanout 123
```

Unknown agents fail before any pane is created. In live mode, fanout also fails if the selected agent CLI is not on `PATH`.

## "prepare worktree"

The git worktree setup failed. Check the nested git error in the output. Common causes:

- a dirty checked-out base branch that cannot be fast-forwarded
- a diverged local base branch
- an existing branch name
- a stale or missing remote branch

Use `--base-branch <branch>` to choose another base branch, including `origin/<branch>` when you want to branch directly from the remote-tracking ref:

```bash
fanout 123 --base-branch release/v2
fanout 123 --base-branch origin/main
```

Use `--no-refresh` only when you intentionally want to branch from the current local base/ref. fanout never forces the base branch into shape — if it cannot refresh it safely, it fails rather than destroying your local work.

## "sub-issues fetch failed"

- You are not authenticated:

```bash
gh auth status
```

- The parent issue does not exist: the Sub-issues API returns HTTP 404 and fanout exits 1 — check the issue number.
- Zero linked sub-issues is not an error — fanout exits 0 with:

```text
no sub-issues on #<parent>
```

## Slug or branch names are not what you want

By default, fanout uses `slugify(title)-<issueNum>` for the slug and `fanout/<slug>` for the branch. Use `--name <NUM>=<slug>|<display>|<branch>` to override a specific issue, or `--branch-prefix <prefix>` to change generated branch names for the whole run — see [Workflow]({{< relref "/docs/workflow" >}}):

```bash
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'
fanout 123 --name 8='feat-x|Feature X|feat/issue-8-x'   # all three segments
fanout 123 --name 9='||release/v2.0'                    # branch override only
fanout 123 --branch-prefix fanout/release/
```

## `gh pr create` is denied ("post-work-review が未実施です")

A `PreToolUse(Bash)` hook (`.claude/hooks/pre-pr-review-gate.sh`, registered in the committed `.claude/settings.json`) blocks `gh pr create` until the current HEAD has passed `/post-work-review`. Run `/post-work-review` — its final step records the reviewed commit — then rerun `gh pr create`. To bypass once, prefix the command:

```bash
FANOUT_SKIP_PR_REVIEW=1 gh pr create ...
```

If fanout settings resolve `prReviewGate=false`, child Claude briefings also carry this bypass permission, but the committed hook itself remains unchanged — see [Settings]({{< relref "/docs/settings" >}}) for what that switch means.

Notes:

- The gate is HEAD-pinned: any new commit re-arms it, so review again before the PR. The marker is worktree-local, so fanout's parallel panes don't interfere with each other.
- Detection runs through a shell tokenizer (a Python companion parser), so command words are distinguished from quoted argument values — a commit message that merely mentions `gh pr create` does not trip it. Indirect forms (`eval`, `xargs`, `sh -c "<string>"`) can still slip through; that is accepted for fanout's normal flow.
- Without `python3` the hook fails closed: it denies anything that coarsely looks like PR creation. Install `python3`, or `export FANOUT_SKIP_PR_REVIEW=1`.
- `make install` overwrites same-named global `post-work-review` and `pr-watch` skills under Claude and Codex; back up any custom copies first.

## Project mode returns no items

Project mode lists items through a GraphQL query that requires the `gh` CLI to carry the `read:project` scope. Without it the query fails — add the scope and rerun:

```bash
gh auth refresh -s read:project
```

Two cases that look like missing items are deliberate:

- If the Project has no Status field, fanout warns and falls back to every item regardless of `--project-status`.
- Items whose repository differs from the current git repository root are warned and skipped — fanout still assumes one repo per run.

## Design notes (FAQ)

The "why does it work that way" answers behind state, locking and pane creation.

### The `state.json` schema

`.fanout/state.json` stores `schemaVersion` plus one row per pane, each carrying `parent`, `issueNum`, optional `taskId` / `kind`, `slug`, `branchName`, `paneId`, `agent`, `displayName`, `worktreePath`, `prompt`, and `createdAt`. TUI shell terminals use `kind: "shell"` so close can kill only the tmux pane and state row. `--status` and lifecycle commands operate on these rows.

### Atomic writes and locking

Writes use a sibling temp file plus rename, so a crash never leaves a half-written `state.json`. Live runs hold `.fanout/state.json.lock` from planning through launch, so parallel fanout invocations cannot both create the same `(parent, issueNum)` pane. Existing worktree directories without a state row are still treated as already fanned out — a migration fallback — and skipped.

### The same child issue is already recorded for another parent

Default slug/branch generation adds a parent token before the issue suffix, so the second run gets its own worktree instead of colliding with the first one. An existing worktree matching the slug this current run would create is still skipped, as interrupted-launch recovery.

### Why pane creation needs no polling

```bash
tmux split-window -t <invoking-pane> -d -h -P -F '#{pane_id}' -c <worktree>
```

returns the new pane id synchronously, without selecting the child pane — so no popup interception and no completion polling is needed.
