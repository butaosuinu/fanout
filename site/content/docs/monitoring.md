---
title: Monitoring
linkTitle: Monitoring
description: "Three windows for surveying every fanned-out pane at once and spotting where it is stuck: the persistent TUI console, --status JSON / table, and the read-only web dashboard."
weight: 40
kanji: 見
yomi: monitoring
---

Fan out five child issues and tmux fills with five panes, each running a different agent in a different worktree. The next thing you want to know is which pane reached a PR, and where one is stuck.

fanout answers that through three windows: the **persistent TUI console** if you want to watch from your terminal, `--status` **JSON** if you want to feed automation, and the read-only **web dashboard** if you want to share with a team or a browser. `--status` and the web dashboard are read-only — they only read `.fanout/state.json`, tmux, and GitHub — but the TUI's key bindings can also merge, close, and clean up panes (the same operations as `--merge` / `--close` / `--cleanup`).

## Pane border labels

Before any of the three windows, tmux itself tells the panes apart: fanout labels each pane it creates on its top border with `<parent> · <name>` — `#123 · fix-login-bug-123` for issue children, `plan:my-feature · task-slug` for plan tasks — and themes the borders with fanout's colors (asagi teal for the active pane, ai indigo for the rest). A glance at a tiled window shows which pane belongs to which child without focusing anything.

## Persistent TUI console

To watch every pane from your terminal, run `fanout` with no arguments to start the persistent console.

```bash
fanout   # start the persistent tmux console
```

From a plain shell it creates or attaches to fanout's managed tmux session; from inside tmux it turns the current pane into the console. The console reads `.fanout/state.json`, periodically refreshes the issue and PR state of recorded panes, and shows each row's worktree change size as `+X/-Y` and whether it holds uncommitted work as `dirty` / `clean`. The `RUN` column shows the agent's execution state as a glyph — `●` for running, `✓` for done — and the detail panel shows the same value as `run=`.

{{< diagram "console" >}}

### Key bindings

| Key | Action |
|---|---|
| `?` | Open the keyboard shortcut help in a tmux popup. Press `Esc`, `q`, or `?` again to close it. |
| `j` / `k` | Move the selection down / up (arrow keys work too). |
| `[` / `]` | Jump to the previous / next Session group. |
| `/` | Filter the loaded rows (free text or a predicate such as `state:open`). `Esc` only leaves the input; pressing `Esc` again from the list clears the filter. |
| `n` | Open the new-session tmux popup. Its Mode row switches between Prompt and Issue; see [New session modes](#new-session-modes). |
| `a` | Attach one or more agent panes to the selected row's recorded worktree. No git worktree is created. The attached rows share the selected worktree and branch, can be focused and peeked, and do not count toward merge progress. `codex` starts in Codex Plan Mode. |
| `A` | Open a shell terminal in the selected row's recorded worktree. Shell rows are recorded as `@manual` entries, can be focused and peeked, and do not count toward merge progress. |
| `t` | Open a shell terminal at the project root. Closing it removes only the tmux pane and the state row; it never deletes the git worktree. |
| `Enter` / `o` | Focus the selected live row's pane. |
| `1`-`9` | Jump to the Nth row of the displayed list and focus its pane. Out-of-range numbers show a notice. |
| `Z` | Focus the selected pane and zoom it. Creating or closing a pane, or resizing the tmux window, unzooms it — press `Z` again if you need to. |
| `p` | Refresh the read-only output snapshot in the detail panel. |
| `v` | Cycle the view override: auto → compact → full. Auto picks the [compact view](#compact-view) below 80 columns; compact forces the switcher even on a wide screen, full forces the table even on a narrow one. Not persisted. |
| `c` / `x` | Open close options for the selected pane: close only the pane, close the pane and the worktree, or also delete the local branch. |
| `m` | Fast-forward merge the selected pane's branch (with a confirmation prompt). |
| `X` | Clean up merged/closed children of the same parent together (with a confirmation prompt). |
| `q` | Leave the console. The tmux session and child panes are left running. |

### Compact view

Below 80 columns, the table and detail panel become a one-line-per-pane switcher, and only the selected row expands into details such as branch, PR, and ci / wave / blockers / dirty.

### F11 / prefix + T

From any pane, **`F11`** or **`prefix + T`** returns to the console — the counterpart of `F12` / `prefix + D` — but with no live console it just prints a notice on the status line.

Disable it with the `consoleKeybind` config key or `FANOUT_CONSOLE_KEYBIND=0` (see [Settings]({{< relref "/docs/settings" >}})).

### New session modes

`n` opens a tmux popup with a Mode row that switches between Prompt and Issue.

**Prompt** is the classic manual pane. Write a multi-line prompt and set the `claude` / `codex` launch counts; in the prompt field, `Shift+Enter` or `Ctrl+J` inserts a newline and `@` completes repository file paths. Manual `codex` panes start in Codex Plan Mode with the prompt passed inline; `claude` starts normally. Enabling the plan fan-out checkbox below switches the launch to a single coordinator (select exactly one agent) that decomposes the prompt into parallel tasks with `fanout plan` — the coordinator always launches as a normal agent, even `codex`.

**Issue** lists the repository's OPEN issues and lets you narrow them by number, title, or label. Pick an issue, choose the default `claude` / `codex` agent, and `Enter` opens an assignment screen that flips the agent per child of that issue — the equivalent of repeatable `--agent NUM=name`. An issue with OPEN children fans out the equivalent of `--unblocked-only`, leaving blocked children deferred. An issue without children starts as a single pane under `@watch`.

## --status (JSON / table / --post-dashboard)

Use `fanout <parent> --status` when you want to feed progress to CI or jq — it enumerates the specified parent's child issues from `.fanout/state.json` and prints each child's state and linked PR as JSON (read-only).

```json
{ "parent": 123,
  "children": [
    { "num": 4, "state": "CLOSED",
      "prs": [ { "number": 250, "state": "MERGED",
                 "mergedAt": "2026-05-04T10:00:00Z",
                 "reviewDecision": "APPROVED", "ci": "pass" } ],
      "has_merged_pr": true },
    { "num": 7, "state": "OPEN", "prs": [], "has_merged_pr": false }
  ],
  "summary": { "total": 2, "merged": 1, "pending": 1,
               "blocked": 0, "all_merged": false } }
```

```bash
fanout 123 --status | jq '.summary.all_merged'
fanout 123 --status --format table       # human-readable table format
fanout 123 --status --post-dashboard     # upsert a rollup comment on the parent issue
```

See the [CLI Reference]({{< relref "/docs/cli" >}}) for every JSON field and exit code. `--format table` lists PR state, CI, diff size, and changed-file count. `--post-dashboard` is the only `--status` variant that writes to GitHub — it upserts one rollup comment on the parent issue and keeps updating it.

## Web dashboard (fanout dashboard --web)

When you want to share every Session with a team or in a browser, `fanout dashboard --web` starts a read-only web dashboard.

```bash
fanout dashboard --web [--port N] [--open] [--no-token] [--no-keybind]
```

The server binds only to `127.0.0.1` and exposes GET-only endpoints, generating a random token each start and embedding it in the URL (drop it with `--no-token` on a single-user machine). The embedded SPA shows the live Session list with a filter, a detail drawer, and a live peek of recent output.

### F12 / prefix + D

From any pane, **`F12`** or **`prefix + D`** opens the dashboard. Disable it with `--no-dashboard-keybind` (fan-out side), `--no-keybind` (dashboard side), the `dashboardKeybind` config key, or `FANOUT_DASHBOARD_KEYBIND=0` (see [Settings]({{< relref "/docs/settings" >}})).

### prefix + M

The same registration also binds **`prefix + M`**: press it from a recorded fanout pane to open a popup that attaches an agent to that worktree or opens a shell there.

### Graceful degradation

- With `gh` logged out, it shows a banner and a state-only view.
- Outside tmux it keeps serving, with pane liveness left as unknown.

Every flag on this page is listed in the [CLI Reference]({{< relref "/docs/cli" >}}).
