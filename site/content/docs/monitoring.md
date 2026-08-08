---
title: Monitoring
linkTitle: Monitoring
description: "Three windows for surveying every fanned-out pane at once and spotting where it is stuck: the persistent TUI console, --status JSON / table, and the read-only web dashboard."
weight: 40
kanji: 見
yomi: monitoring
---

Fan out five child issues and tmux fills with five panes, each running a different agent in a different worktree. The next thing you want to know is which pane reached a PR, and where one is stuck.

fanout answers that through three windows: the **persistent TUI console** if you want to watch from your terminal, `--status` **JSON** if you want to feed automation, and the read-only **web dashboard** if you want to share with a team or a browser. `--status` and the web dashboard are read-only — they only read `.fanout/state.json`, the selected runtime, and GitHub — but the TUI's tmux paths can also merge, close, and clean up panes (the same operations as `--merge` / `--close` / `--cleanup`).

## Pane border labels

Before any of the three windows, tmux itself tells the panes apart: fanout labels each pane it creates on its top border with `<parent> · <name>` — `#123 · fix-login-bug-123` for issue children, `plan:my-feature · task-slug` for plan tasks — and themes the borders with fanout's colors (asagi teal for the active pane, ai indigo for the rest). A glance at a tiled window shows which pane belongs to which child without focusing anything.

## Persistent TUI console

To watch every pane from your terminal, run `fanout` with no arguments to start the persistent console.

```bash
fanout   # start the persistent console
```

With the tmux backend, a plain-shell launch creates or attaches to fanout's managed tmux session; inside tmux, the current pane becomes the console. With the herdr backend, a plain-shell launch bootstraps fanout's repository-owned session and console workspace, then prints the attach command. Run that command to enter the console; fanout leaves the calling shell intact. The console reads `.fanout/state.json`, periodically refreshes the issue and PR state of recorded panes, and shows each row's worktree change size as `+X/-Y` and whether it holds uncommitted work as `dirty` / `clean`. The `RUN` column shows the agent's execution state as a glyph — `●` running, `✓` done from the launch wrapper, plus `◐` working, `◇` plan, `◆` blocked, `○` idle when agent hooks report them — and the detail panel shows the same value as `run=`. When you focus a recorded tmux pane with the mouse or tmux `prefix` movement keys, the selected TUI row follows that pane.

The console is backend-aware: the header names the selected runtime backend and why it was selected — `backend: herdr (HERDR_ENV)`, for example — and the detail panel shows each row's `backend=` and `pane=` identity. In fanout's owned [herdr backend]({{< relref "/docs/herdr-backend" >}}) session, issue / Prompt / attach / shell launch, focus, and peek are enabled. Foreign or incomplete Herdr rows stay disabled with the reason beside each key. Herdr send, restore, lifecycle close / merge / cleanup, and plan capture remain unavailable.

{{< diagram "console" >}}

### Agent-state notification sounds

The TUI's notification channels also cover agent-state changes. With the default `notifications=bell`, the same terminal bell that already reports issue / PR transitions also sounds when an agent presents a plan, waits for user input or approval, or exits. Change the destination with `notifications` or `FANOUT_NOTIFICATIONS`; see [Settings]({{< relref "/docs/settings" >}}).

fanout reads structured agent state from the pane's `@fanout_agent_state` tmux option. It does not scrape pane output. The observed states are `running`, `working`, `plan`, `blocked`, `idle`, and `done`, but notifications are sent only for `plan` (`plan ready`), `blocked` (`waiting for input`), and `done` (`agent exited`). The other states are display state only.

### Key bindings

| Key | Action |
|---|---|
| `?` | Open keyboard shortcut help (tmux popup or inline Herdr form). Press `Esc`, `q`, or `?` again to close it. |
| `j` / `k` | Move the selection down / up (arrow keys work too). |
| `[` / `]` | Jump to the previous / next Session group. |
| `/` | Filter the loaded rows (free text or a predicate such as `state:open`). `Esc` only leaves the input; pressing `Esc` again from the list clears the filter. |
| `n` | Open the new-session form (tmux popup or inline Herdr form). Its Mode row switches between Prompt and Issue; see [New session modes](#new-session-modes). |
| `s` | Open settings (tmux popup or inline Herdr form). Choose user or repo config, edit the same keys as `config.json`, and save with `Ctrl+S`. |
| `Ctrl+O` | In the new-session Issue list, open the selected issue in the default browser. |
| `a` | Attach one or more agent panes to the selected row's recorded worktree. No git worktree is created. The attached rows share the selected worktree and branch, can be focused and peeked, and do not count toward merge progress. Attached agents use the same launch posture as [new-session panes](#new-session-modes). |
| `A` | Open a shell terminal in the selected row's recorded worktree. Shell rows are recorded as `@manual` entries, can be focused and peeked, and do not count toward merge progress. |
| `t` | Open a shell terminal at the project root. On tmux, closing it removes only the pane and state row; it never deletes the git worktree. Herdr lifecycle close remains unavailable. |
| `Enter` / `o` | Focus the selected live row's pane. |
| `1`-`9` | Jump to the Nth row of the displayed list and focus its pane. Out-of-range numbers show a notice. |
| `Z` | On tmux, focus the selected pane and zoom it. Creating or closing a pane, or resizing the window, unzooms it — press `Z` again if you need to. |
| `p` | Refresh the read-only output snapshot in the detail panel. |
| `v` | Cycle the view override: auto → compact → full. Auto picks the [compact view](#compact-view) below 80 columns; compact forces the switcher even on a wide screen, full forces the table even on a narrow one. Not persisted. |
| `c` / `x` | Open close options for the selected pane: close only the pane, close the pane and the worktree, or also delete the local branch. |
| `m` | Fast-forward merge the selected pane's branch (with a confirmation prompt). |
| `X` | Clean up merged/closed children of the same parent together (with a confirmation prompt). |
| `q` | Leave the console. The tmux session and child panes are left running. |

### Compact view

Below 80 columns, the table and detail panel become a one-line-per-pane switcher, and only the selected row expands into details such as branch, PR, and ci / wave / blockers / dirty.

### Settings popup

Press `s` to edit fanout's JSON settings without leaving the console. The Target row switches between user config and repo config; each setting can be set or returned to `inherit`, which removes that key from the selected file. Launch-posture settings are user-only. CLI flags and `FANOUT_*` environment variables still win over saved files. Repo config keeps the same safety rules as `config.json`: it cannot set launch posture, enable the watcher, or set HTTP notification endpoints, and its notification channels are limited to `bell`, `tmux`, and `none`.

### F11 / prefix + T

From any tmux pane, **`F11`** or **`prefix + T`** returns to the console — the counterpart of `F12` / `prefix + D` — but with no live console it just prints a notice on the status line. Herdr does not register tmux keybindings.

Disable it with the `consoleKeybind` config key or `FANOUT_CONSOLE_KEYBIND=0` (see [Settings]({{< relref "/docs/settings" >}})).

### New session modes

`n` opens a form with a Mode row that switches between Prompt and Issue. It is a tmux popup under tmux and an inline form under Herdr.

After this popup successfully launches a Prompt, plan coordinator, or Issue Session, the console focuses the first newly created pane in actual creation order. When an Issue fan-out creates an orchestrator, that pane is first in the creation order. Press `F11` or `prefix + T` to return. Agent attach (`a`), shell (`A` / `t`), watcher, and ordinary CLI launch paths leave focus where it was.

**Prompt** is the classic manual pane. Write a multi-line prompt and set the launch counts per agent (`claude` / `codex` / `opencode`); in the prompt field, `Shift+Enter` or `Ctrl+J` inserts a newline and `@` completes repository file paths. Manual panes use `newSessionPlanMode` for all three agents. It defaults to `true`, so Claude, Codex, and OpenCode start in Plan Mode. Enabling the plan fan-out checkbox below switches the launch to a single coordinator (select exactly one agent) that decomposes the prompt into parallel tasks with `fanout plan`. The same setting puts Claude and Codex coordinators in Plan Mode; OpenCode cannot run the coordinator — it does not read the bundled `/fanout plan` command, so pick claude or codex.

**Issue** lists the repository's OPEN issues and lets you narrow them by number, title, or label. `Ctrl+O` opens the selected issue in the default browser. Pick an issue, choose the default agent, and `Enter` opens an assignment screen that flips the agent per child of that issue — the equivalent of repeatable `--agent NUM=name`. When a parent Issue fan-out creates its orchestrator, it starts that pane with the popup's default agent at the project root without a worktree, before any new child panes. The orchestrator follows `orchestratorPlanMode` (default `true`). A codex orchestrator cannot combine Plan Mode with the start gate that holds its launch until the child fan-out finishes, so fanout warns and starts plain codex. The children keep their per-child agent assignments. The orchestrator briefing tells it not to implement child-scoped work and instead to poll `fanout <N> --status`, own parent-scope work, integrate and post the final rollup comment after all children merge, and use `--merge` / `--cleanup` for lifecycle work. The child fan-out is the equivalent of `--unblocked-only`, so blocked children stay deferred. An all-blocked first selection creates no panes; re-select after a child unblocks to create the orchestrator and child pane. Once the orchestrator exists, later selections do not create another and start only newly unblocked children. Child launch posture applies independently to every selected agent. An issue without children uses the same child posture when it starts as a standalone pane under `@watch` (see [Watcher]({{< relref "/docs/watcher" >}})).

The same plan fan-out checkbox from Prompt mode appears here for a single issue: turn it on and the child-assignment screen is skipped, launching one coordinator pane — following `newSessionPlanMode` like Prompt mode — that decomposes the issue into issue-less `fanout plan` tasks run by the chosen task agent. The checkbox creates a coordinator and new tasks; child launch posture applies to those tasks as well as existing issue / Project children. The checkbox is grayed out while the selected issue has OPEN children — fan those out instead.

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

See the [CLI Reference]({{< relref "/docs/cli" >}}) for every JSON field and exit code. Each child row carries a `backend` field naming the runtime that owns its pane (`tmux` or `herdr`). `--format table` lists PR state, CI, diff size, and changed-file count. `--post-dashboard` is the only `--status` variant that writes to GitHub — it upserts one rollup comment on the parent issue and keeps updating it.

## Web dashboard (fanout dashboard --web)

When you want to share every Session with a team or in a browser, `fanout dashboard --web` starts a read-only web dashboard.

```bash
fanout dashboard --web [--port N] [--open] [--no-token] [--no-keybind]
```

The server binds only to `127.0.0.1` and exposes GET-only endpoints, generating a random token each start and embedding it in the URL (drop it with `--no-token` on a single-user machine). The embedded SPA shows the live Session list with a filter, a detail drawer, and a live peek of recent output.
The dashboard also shows the PR link and CI status for a Prompt Session when a PR exists for its recorded branch.

Each Session row names its runtime backend and pane identity, with a runtime state of `live` / `stale` / `unknown` / `unsupported` / `-`, and the filter accepts `backend:tmux` / `backend:herdr`. For rows on the [herdr backend]({{< relref "/docs/herdr-backend" >}}), live peek returns content only when the saved row still matches a pane in fanout's owned session. Foreign and stale rows return 404, and the dashboard remains GET-only.

### Diff viewer

Click the diff column of a Session row, or **Show changes** in the detail drawer header, to read that worktree's changes against the merge-base. Rows showing `+0/-0` open too — binary-only, mode-only, and rename-only changes do not show up as lines. A sidebar groups the changed files by directory and shows each one's added and deleted line counts; a moved file gets one row naming where it came from. Every row starts with a circle marking what happened to the file: a plus for a new file, a dot for an edit, a minus for a deletion, an arrow for a move. Line counts cannot carry that — an empty new file is `+0/-0` and a full rewrite is `+N/-N`. Click a name to jump to its diff. Files whose patch the server omitted (binary, over the size limit) are listed under the warning banner instead, with the reason.

Files are expanded with syntax highlighting by default. Only a file whose diff renders 1,000 or more rows starts collapsed; that count includes the context lines inside each hunk, not just the changed ones. Expanding it keeps the highlighting. Very large files are the exception and render as plain text: highlighting is dropped above 150,000 characters or 20,000 rows in the rendered diff — both counts cover what the diff draws, including the context lines inside each hunk — because tokenizing runs over the whole file rather than the visible rows. The viewer renders just the visible rows, so a diff of several thousand lines stays responsive. The file name stays pinned to the top while you scroll through that file, and long lines wrap. When deletions and additions are side by side, a file with only additions or only deletions is shown on one side instead of the full width.

It opens compact by default: a panel beside the detail drawer. Drag its left edge to widen it up to 95% of the window, over the drawer. In compact the Session list and the drawer stay usable as long as part of the list is still visible, so you can follow a pane's output while reading its diff. Once the panel covers the list completely — below 1,100px it fills the width under the nav, and on a wider window a wide drawer plus a wide panel can leave nothing beside it — it behaves like full screen instead. The diagonal-arrow icon switches between compact and full screen. The mode and the width are stored in the browser, per origin — which includes the port. `fanout dashboard --web` takes an OS-assigned port by default, so a restart lands on a new origin and the stored settings start fresh; pass a fixed `--port N` to keep them.

How deletions and additions are laid out follows the available width, not the mode: by default they stack in one column below about 1,000px of panel width and sit side by side above it, so a narrow window is stacked even in full screen. The frame icon cycles auto → side by side → stacked; picking one explicitly pins it regardless of width.

Buttons in the header and the sidebar are icons; hover one to see what it does.

### Settings

The gear in the top-right opens settings. Language is Auto / 日本語 / English — Auto follows the browser's language, and picking one explicitly keeps it. Appearance is System / Light / Dark, and the diff viewer's syntax theme is chosen separately for light and dark from nine curated themes each (Pierre, GitHub, Catppuccin, Gruvbox, Tokyo Night, and more). Both come with a preview, and the diff theme preview uses the same rendering as the real diff, so you see the exact colors before choosing. Settings are stored in the browser and restored on the next visit to the same origin (see the port note above).

The dashboard ships in Japanese and English. Column headers, tags, and filter values stay in English in both languages because they are the filter query syntax — `state:open` and `ci:fail` are what you type, so they read the same either way.

### F12 / prefix + D

From any pane, **`F12`** or **`prefix + D`** opens the dashboard. Disable it with `--no-dashboard-keybind` (fan-out side), `--no-keybind` (dashboard side), the `dashboardKeybind` config key, or `FANOUT_DASHBOARD_KEYBIND=0` (see [Settings]({{< relref "/docs/settings" >}})).

### prefix + M

The same registration also binds **`prefix + M`**: press it from a recorded fanout pane to open a popup that attaches an agent to that worktree or opens a shell there.

### Graceful degradation

- With `gh` logged out, it shows a banner and a state-only view.
- Outside tmux it keeps serving, with pane liveness left as unknown.
- A herdr row matches its saved identity against `herdr api snapshot` for liveness and agent state. If the row has no `agent_session`, the first unique valid reference from the expected provider is persisted under the owning state lock; later observations must match it exactly. No other identity field is filled in from the snapshot. Output peek returns content only for a live row in this repository's fanout-owned Herdr session.

Every flag on this page is listed in the [CLI Reference]({{< relref "/docs/cli" >}}).
