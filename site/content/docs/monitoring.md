---
title: Monitoring
linkTitle: Monitoring
description: "Three windows onto a fan-out — the persistent TUI console, --status JSON / table, and the read-only web dashboard."
weight: 40
kanji: 見
yomi: monitoring
---

## Persistent TUI console

Run `fanout` with no arguments to start the persistent console:

```bash
fanout   # start the persistent tmux console
```

From a plain shell it creates a deterministic fanout-managed tmux session for the current repository, starts the console in that session, and attaches to it. From inside tmux it turns the current pane into the console.

The console reads `<git-root>/.fanout/state.json`, checks whether recorded pane IDs still exist in tmux, and periodically refreshes issue / closed-by PR state through the same GitHub CLI source used by `fanout <parent> --status`. Each row also shows the pane worktree's total work size as `+X/-Y` — `git diff --shortstat` against the merge-base with the recorded base branch, so committed and uncommitted changes both count (rows recorded before the base branch was tracked fall back to `origin/HEAD`, then `HEAD`) — and `dirty`/`clean` from `git status --porcelain`, which flags uncommitted work without any agent instrumentation.

Press `/` to filter the loaded rows in memory, with free-text terms or predicates such as `state:open`, `agent:codex`, and `wave:wave5`. Filtering never triggers extra data fetches, and the automatic state / GitHub refresh continues while a filter is active.

For recorded issue parents the console also reloads the parent's child set and shows wave / blocker columns, using the same `## Blocked by` and `(blocked by #N)` sources as `--unblocked-only`. Blocked children that have not been fanned out yet appear as `deferred` rows, and CLOSED blockers are shown as resolved. The header shows `total` / `merged` / `pending` / `blocked` rollup counts.

### Key bindings

| Key | Action |
|---|---|
| `n` | Open a modal to create manual agent panes from a required **multi-line** prompt, `claude` / `codex` launch counts, and an optional slug. `codex` starts in Codex Plan Mode; `claude` starts normally. Use `Up` / `Down` to pick an agent row, `Space` to toggle it, and `Left` / `Right` to change the count. In the prompt field, `Shift+Enter` inserts a newline (`Ctrl+J` is a fallback for terminals that do not distinguish `Shift+Enter`); `Enter` creates the selected panes. Manual panes are recorded as synthetic `@manual` state entries and appear in the list after launch. |
| `A` | Open a shell terminal in the selected row's recorded worktree. Shell rows are recorded as `@manual` entries, can be focused and peeked, and do not count toward merge progress. |
| `t` | Open a shell terminal at the project root. Closing it kills the tmux pane and removes the state row; it never removes a git worktree. |
| `Enter` / `o` | Focus the selected live row's pane. |
| `p` | Refresh the read-only output snapshot shown in the detail panel. |
| `c` | Close the selected pane — confirmation prompt, then the same core path as `--close`. |
| `m` | Fast-forward merge the selected pane's branch — confirmation prompt, then the same core path as `--merge`. |
| `x` | Clean up merged/closed children of the same parent — confirmation prompt, then the same core path as `--cleanup`. |
| `q` | Leave the console. The tmux session and child panes are left running. |

> Rows whose recorded pane no longer exists in tmux are marked `stale!` and are skipped by the focus and peek actions.

## --status (JSON)

`fanout <parent> --status` is read-only. It reads `.fanout/state.json` to enumerate the children already fanned out under that specific parent, queries each child through `gh api graphql` against `repository.issue.closedByPullRequestsReferences(first: 100)` — cursor-paginated when a child is closed by more than 100 PRs — so the response carries `state`, `mergedAt`, `reviewDecision`, and the latest commit's CI rollup when present, and prints one JSON document on stdout by default:

```json
{
  "parent": 123,
  "children": [
    { "num": 4, "state": "CLOSED",
      "prs": [ { "number": 250, "state": "MERGED",
                 "mergedAt": "2026-05-04T10:00:00Z",
                 "reviewDecision": "APPROVED", "ci": "pass" } ],
      "has_merged_pr": true },
    { "num": 7, "state": "OPEN",
      "prs": [],
      "has_merged_pr": false }
  ],
  "summary": {
    "total":      2,
    "merged":     1,
    "pending":    1,
    "blocked":    0,
    "all_merged": false
  }
}
```

The JSON is meant for automation:

```bash
fanout 123 --status | jq '.summary.all_merged'
```

In a state file that has fanned multiple parents, children of other parents are filtered out so `summary.all_merged` reflects only the requested parent. Set `FANOUT_STATE_PATH` to point directly at a state file when reading from outside the repository checkout; otherwise fanout reads `<git-root>/.fanout/state.json`.

`--status` exit codes are a separate lane from the default flow:

| Exit code | Meaning |
|---|---|
| `0` | status emitted — check `summary.all_merged` in JSON mode for the actual state |
| `2` | cannot enumerate: bad invocation, unreadable or malformed state file, unusable project root, or a Projects v2 URL as parent. A missing state file is treated as an empty state |
| `3` | `gh` API call failed (auth, network, non-existent issue, etc.) |

> **Issue-mode parents only:** a Projects v2 URL as the parent is rejected up-front, because the current JSON schema is built around issue parents.

## --status --format table

```bash
fanout 123 --status --format table
```

`--format table` prints a human-readable overview that adds a normalized PR state (`open`, `draft`, `review-required`, `approved`, `changes-requested`, `merged`, or `closed`), CI, PR diff bars, changed-file counts, the Conventional-Commit type, and PR links.

## --status --post-dashboard

```bash
fanout 123 --status --post-dashboard
```

`--post-dashboard` upserts one marker-based comment on the parent issue, listing for each child PR the sub-issue number, PR link, PR state, CI, diff size, Conventional-Commit type, TL;DR, and a `Review effort` score. The dashboard is built from machine-readable GitHub data and PR bodies; it does not call an LLM.

It puts `<!-- fanout:dashboard parent=N -->` at the start of the comment body, finds an existing marker comment with the paginated GitHub REST comments endpoint, and updates that exact comment. If no marker comment exists, it creates one with `gh issue comment --body-file -`.

> `--post-dashboard` is the only `--status` option that writes to GitHub.

## Web dashboard (fanout dashboard --web)

`fanout dashboard --web` starts a **read-only** web dashboard that visualizes fanout **Sessions** — the panes recorded in `.fanout/state.json`, grouped by parent issue — and keeps them live in the browser over SSE: pane liveness (from `tmux list-panes`), the live tmux pane title, a `running` / `done` agent-state badge, wave / open-blocker columns (from the parent issue graph), issue state, PR merge status, CI status, and diff/dirty (the same data source as `--status`, reused across every parent in the repo at once). Children that have not been fanned out yet appear as synthetic not-started rows. It never mutates GitHub state and only ever *reads* tmux, with two deliberate conveniences: it records the running server in `.fanout/dashboard.json` so a second launch reuses it, and it registers the `prefix + D` tmux keybinding described below (opt out with `--no-keybind`).

```bash
fanout dashboard --web [--port N] [--open] [--no-token] [--no-keybind]
```

- **localhost only.** The server binds `127.0.0.1` and exposes GET-only endpoints: `/api/snapshot`, an SSE `/api/stream`, `/api/peek` (a `tmux capture-pane` snapshot of one recorded pane), `/api/plan` (the last complete `<proposed_plan>` block of a `--codex-plan-mode` pane), and the embedded UI. `--port` defaults to `0` (an OS-assigned ephemeral port); the chosen URL is printed.
- **Token by default.** A random token is generated each start and embedded in the printed/opened URL, gating `/api/*` so other local users or processes cannot read your issue/PR data off the loopback port. Pass `--no-token` on a single-user machine to drop it.
- **`--open`** opens the URL in your default browser. The dashboard reuses a server that is already running (recorded in `.fanout/dashboard.json`) instead of starting a second one.
- **Single-page UI.** The embedded React + Vite SPA layers more onto the live Session list than the API alone: a structured filter box (free words ANDed with `state:` / `run:` / `agent:` / `wave:` / `ci:` / `dirty:` / `live:` / `issue:` / `task:` / `pr:` terms, plus dropdowns and removable chips), a detail drawer (click a row) showing pane metadata, wave / blockers, the worktree, PRs with CI, the original prompt, and a live *peek* of recent output refreshed every 5 s, a *plan* section for `--codex-plan-mode` panes, a top HUD with repo-wide running / blocked counts, and a PAPER BREEZE light/dark theme.

Run `fanout dashboard --help` for the full flag list.

### prefix + D

After a live fan-out — and whenever the dashboard itself starts — fanout registers a tmux keybinding so that **`prefix + D`** pops the dashboard from any pane. The key launches the server in a detached `fanout-dashboard` window, so it outlives the keypress; a second press just reopens the existing URL.

Disable the auto-binding with `--no-dashboard-keybind` (fan-out side), `--no-keybind` (dashboard side), the `dashboardKeybind` config key, or `FANOUT_DASHBOARD_KEYBIND=0` — see [Settings]({{< relref "/docs/settings" >}}).

### Graceful degradation

- With `gh` logged out, the dashboard shows a banner and a state-only view.
- Outside tmux it still serves, marking pane liveness as unknown.

Every flag on this page is listed in the [CLI Reference]({{< relref "/docs/cli" >}}).
