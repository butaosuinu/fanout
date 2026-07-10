---
title: Troubleshooting
linkTitle: Troubleshooting
description: "Common fanout error messages and their fixes."
weight: 80
kanji: 直
yomi: troubleshoot
---

## "fanout must be run inside tmux"

Action mode creates child panes directly with `tmux split-window`, so start or attach a tmux session in the repository worktree, then run fanout. The one exception is TUI mode (`fanout` with no arguments): it can start from a plain shell, because it creates or attaches a fanout-managed tmux session itself — see [Monitoring]({{< relref "/docs/monitoring" >}}) for details.

## "agent is required"

Pass `--agent claude` or `--agent codex`, or set the `FANOUT_AGENT` environment variable.

```bash
fanout 123 --agent claude
FANOUT_AGENT=codex fanout 123
```

Unknown agents fail before any pane is created, and in live mode fanout also fails if the selected agent CLI is not on `PATH`.

## "prepare worktree"

The git worktree setup failed, so check the nested git error in the output. Common causes:

- a dirty checked-out base branch that cannot be fast-forwarded
- a diverged local base branch
- an existing branch name
- a stale or missing remote branch

To change the base, use `--base-branch <branch>`; specify `origin/<branch>` when you want to branch directly from the remote-tracking ref.

```bash
fanout 123 --base-branch release/v2
fanout 123 --base-branch origin/main
```

Use `--no-refresh` only when you intentionally want to branch from the current local base/ref (fanout never forces the base branch into shape, and fails rather than destroying your local work when it cannot refresh safely).

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

By default, fanout uses `slugify(title)-<issueNum>` for the slug and `fanout/<slug>` for the branch. Use `--name <NUM>=<slug>|<display>|<branch>` to override a specific issue, or `--branch-prefix <prefix>` to override branch names for the whole run at once. See [Workflow]({{< relref "/docs/workflow" >}}) for details.

```bash
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'
fanout 123 --name 8='feat-x|Feature X|feat/issue-8-x'   # all three segments
fanout 123 --name 9='||release/v2.0'                    # branch override only
fanout 123 --branch-prefix fanout/release/
```

## `gh pr create` is denied ("post-work-review が未実施です")

A `PreToolUse(Bash)` hook (`.claude/hooks/pre-pr-review-gate.sh`, registered in the committed `.claude/settings.json`) blocks `gh pr create` until the current HEAD has passed `/post-work-review`. Claude's legacy review marker permits the default PR base only. When Codex records reviewed-base metadata, the hook also requires the PR base to match it. Invalid or stale metadata fails closed. Run `/post-work-review`, then rerun `gh pr create` with the reviewed base (to bypass once, prefix the command with the following).

```bash
FANOUT_SKIP_PR_REVIEW=1 gh pr create ...
```

If fanout settings resolve `prReviewGate=false`, child Claude briefings also carry this bypass permission, but the committed hook itself remains unchanged (see [Settings]({{< relref "/docs/settings" >}}) for what that switch means).

The gate is pinned to HEAD, so adding a new commit re-arms it — review again before the PR (the marker is worktree-local, so fanout's parallel panes don't interfere with each other). Without `python3` the hook fails closed and denies anything that coarsely looks like PR creation, so install `python3` or use `FANOUT_SKIP_PR_REVIEW=1`.

## Project mode returns no items

Project mode lists items through a GraphQL query that requires the `gh` CLI to carry the `read:project` scope (without it the query fails). Add the scope and rerun.

```bash
gh auth refresh -s read:project
```

Items can look fewer than expected, but that's expected behavior: a Project with no Status field falls back to every item, and an item whose repository differs from the current git repository root is skipped with a warning. See [Workflow]({{< relref "/docs/workflow" >}}) and the [CLI reference]({{< relref "/docs/cli" >}}) for details.
