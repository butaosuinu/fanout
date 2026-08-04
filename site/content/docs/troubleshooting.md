---
title: Troubleshooting
linkTitle: Troubleshooting
description: "Common fanout error messages and their fixes."
weight: 100
kanji: 直
yomi: troubleshoot
---

## "fanout must be run inside tmux"

Action mode creates child panes directly with `tmux split-window`, so start or attach a tmux session in the repository worktree, then run fanout. The one exception is TUI mode (`fanout` with no arguments): it can start from a plain shell, because it creates or attaches a fanout-managed tmux session itself — see [Monitoring]({{< relref "/docs/monitoring" >}}) for details.

## "agent is required"

Pass `--agent claude`, `--agent codex`, or `--agent opencode`, or set the `FANOUT_AGENT` environment variable.

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

A `PreToolUse(Bash)` hook (`.claude/hooks/pre-pr-review-gate.sh`, registered in the committed `.claude/settings.json`) blocks `gh pr create` until the current HEAD has passed `/post-work-review`. Claude's legacy review marker permits the default PR base only. When Codex records reviewed-base metadata, the hook requires both the PR base and the current `origin/<base>...head` diff hash to match it. Invalid or stale metadata fails closed. Run `/post-work-review`, then rerun `gh pr create` with the reviewed base (to bypass once, prefix the command with the following).

```bash
FANOUT_SKIP_PR_REVIEW=1 gh pr create ...
```

If fanout settings resolve `prReviewGate=false`, child Claude briefings also carry this bypass permission, but the committed hook itself remains unchanged (see [Settings]({{< relref "/docs/settings" >}}) for what that switch means).

The gate is pinned to HEAD, so adding a new commit re-arms it — review again before the PR (the marker is worktree-local, so fanout's parallel panes don't interfere with each other). Without `python3` the hook fails closed and denies anything that coarsely looks like PR creation, so install `python3` or use `FANOUT_SKIP_PR_REVIEW=1`.

## `post-work-review` reports an `agent_type` error

Current `$post-work-review` does not request `agent_type`. It uses an ordinary native `spawn_agent` call and treats `task_name` only as a task label. If the old error still appears, run `fanout update`, then start a new Codex session. Codex loads skills at session startup; the checksum-verified release installer also removes the retired custom agents and driver.

If `make install` or `make link` reports that retired driver, the checkout stops before building or replacing the binary. Run `fanout update` without `--no-skills`, then retry the make target.

The current gate requires native `spawn_agent` and `wait_agent` plus an available concurrency slot. If any of these are unavailable, it stops with an error instead of starting `codex exec`, app-server, or another reviewer fallback.

The child inherits the parent session's permissions. Start the parent read-only if reviewer writes must be prevented by the sandbox. The reviewer still receives repository content through its prompt and repository reads.

## Project mode returns no items

Project mode lists items through a GraphQL query that requires the `gh` CLI to carry the `read:project` scope (without it the query fails). Add the scope and rerun.

```bash
gh auth refresh -s read:project
```

Items can look fewer than expected, but that's expected behavior: a Project with no Status field falls back to every item, and an item whose repository differs from the current git repository root is skipped with a warning. See [Workflow]({{< relref "/docs/workflow" >}}) and the [CLI reference]({{< relref "/docs/cli" >}}) for details.

## "herdr named session ... is not running"

This error belongs to observation of an external [herdr backend]({{< relref "/docs/herdr-backend" >}}) session. Start that explicitly named session in herdr, then verify it from the same shell (`default` is rejected):

```bash
herdr status --json   # server and session state
```

`HERDR_SOCKET_PATH` takes precedence over `HERDR_SESSION`, so a stale socket path can point fanout at the wrong server — unset it if `status` disagrees with what you expect. The TUI variant `run fanout inside an existing herdr pane (HERDR_ENV=1)` means what it says: the console only starts under the herdr backend when launched from a pane inside the herdr session. CLI issue, Project, plan, and watcher launches instead create or readopt fanout's repository-owned session.

## "unsupported herdr CLI version ..."

fanout requires stable herdr 0.7.5 or newer. The CLI and server versions must match. Prerelease or malformed versions fail closed. `herdr stable >=0.7.5 is required: ...` means the `herdr` binary was not found on `PATH`. Check what is actually installed:

```bash
herdr --version      # stable 0.7.5 or newer
herdr status --json  # matching client/server version
```

Install or upgrade to stable herdr 0.7.5 or newer. If the message is `requires a client/server restart`, restart the herdr server and client so both run the same build.

fanout does not preflight methods or response fields. `herdr method "<name>" is unavailable` means that the named call failed; check that the installed herdr version provides it.

## "herdr backend interactive TUI actions are read-only"

Not a fault. This message now applies to interactive TUI mutation and the remaining unsupported operations, not to CLI issue, Project, plan, or watcher launch. Use `--backend herdr` for those launch lanes. herdr with `--team` and Codex child Plan Mode fail before mutation with a specific `runtime backend herdr does not support ...` error. A conflicting backend on a parent with recorded panes still fails with `explicit migration is required`; there is no migration command in v1. See [herdr backend]({{< relref "/docs/herdr-backend" >}}) for the capability table.
