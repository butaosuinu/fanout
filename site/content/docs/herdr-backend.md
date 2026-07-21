---
title: herdr backend
linkTitle: herdr backend
description: "The opt-in, observation-only herdr runtime backend: prerequisites, how backend selection works, what differs from tmux, and the plugin cautions."
weight: 90
kanji: 観
yomi: herdr
---

The herdr backend lets fanout run inside [herdr](https://herdr.dev/) — a tmux-alternative persistent-PTY runtime for coding agents — as a read-only console. It is opt-in, and in v1 it is observation-only: fanout displays recorded herdr panes but never creates, mutates, or closes them. The default backend stays tmux, and outside a herdr session nothing changes for tmux users; inside one, herdr wins the automatic selection unless overridden (see below). fanout does not bundle herdr; it is AGPL-licensed and installed separately.

## What v1 does

Run fanout inside a named herdr session and the read-only surfaces — the persistent TUI console, `--status`, and the web dashboard — show the repository's recorded sessions, including each pane's runtime backend and identity (see [Monitoring]({{< relref "/docs/monitoring" >}})). The TUI console and the web dashboard match rows recorded with the herdr backend against `herdr api snapshot` for liveness and agent state; `--status` reads recorded state and GitHub only. fanout admits herdr with `herdr --version`, `herdr api schema --json`, `herdr pane --help`, `herdr workspace --help`, `herdr worktree --help`, and `herdr status --json`. Initial named-session resolution uses `herdr --session <name> status --json` instead. After admission, fanout reads liveness with `herdr api snapshot`. The schema gate checks the required request and response methods and fields. The help gate checks the command forms for pane read/run/close, workspace focus/close, and worktree remove without reading or mutating a target.

Everything that would mutate a herdr session fails closed with a clear error instead of degrading:

- Issue, Project, and plan launches are rejected before any worktree or state mutation — v1 never records herdr rows itself.
- Focus, send, close, restore, output peek, plan capture, and automatic cleanup are unavailable for herdr rows.
- Automatic nudges (`fanout msg nudge` delivery) are disabled for every agent kind. Messages still persist to the bus for `inbox` / `board` reads.
- No tmux keybindings are registered, fanout never calls herdr's in-app `notification show`, and Codex Plan Mode is unavailable.

The TUI header always shows the selected backend and why it was selected, such as `backend: herdr (HERDR_ENV)`.

## Prerequisites

- **herdr stable 0.7.4 or newer** — `herdr --version`, the status client/server, and each snapshot must report the same admitted version. The status client must use the stable channel and protocol 16; the server must be running with protocol 16, `compatible: true`, and no pending restart.
- **Compatible API and command surfaces** — the API schema document must report protocol 16 and schema version 1, with the required request/response structure. A newer version is accepted only when the schema and CLI help gates pass.
- A running herdr session with an explicit name (`default` is rejected). fanout never starts a herdr server and never creates or attaches a session.
- The `herdr` binary on your `PATH`, installed separately.

## Opting in

1. Start a named herdr session yourself (see the [herdr docs](https://herdr.dev/docs/)).
2. Run `fanout` inside a pane of that session. herdr sets `HERDR_ENV=1` there, so fanout picks the herdr backend on its own.
3. Read the recorded rows in the TUI console, `--status`, or the web dashboard.

Because launches fail closed, v1 never records herdr rows itself — a herdr-backend row exists only when written outside the normal launch flow (manual experiments). Day to day, the console under herdr is a read-only view of the repository's recorded sessions.

The backend for a run resolves in this order, first match wins:

1. The parent's recorded backend (stickiness — see below)
2. `--backend <tmux|herdr>`
3. `FANOUT_BACKEND`
4. `HERDR_ENV=1` in the environment
5. `TMUX` set → tmux
6. `runtimeBackend` in user config
7. Default: `tmux`

When both `HERDR_ENV` and `TMUX` are set — tmux nested inside herdr — herdr wins; override with `FANOUT_BACKEND=tmux`, or `--backend tmux` on the launch commands that accept the flag (the no-argument console reads the environment and config only). `runtimeBackend` is a user-config key: repo config cannot set it and is ignored with a warning ([Settings]({{< relref "/docs/settings" >}})).

A parent that already has recorded panes keeps its recorded backend. A conflicting `--backend` or `FANOUT_BACKEND` fails with `explicit migration is required` rather than mixing backends under one parent. There is no migration command in v1 — existing tmux parents stay on tmux.

## Differences from tmux

| Capability | tmux backend | herdr backend v1 |
|---|---|---|
| Issue / Project / plan launch | Creates worktrees, panes, agents | Rejected before any worktree or state mutation |
| Worktree creation | One per child under `.fanout/worktrees/` | Never |
| Liveness and agent state (TUI console, web dashboard) | tmux queries | `herdr api snapshot` — supported |
| Exit status display | Launch wrapper reports `✓ done` | None — herdr's public API keeps no exit status |
| Pane after the agent exits | Pane stays open with the wrapper message | herdr drops the pane and its own record on normal exit; the fanout row turns `stale` |
| Focus / send / close / restore / peek / plan capture | TUI keys and lifecycle flags | Unavailable — `runtime backend herdr does not support …` |
| Automatic cleanup (`--cleanup`) | Folds merged/closed panes away | Refused; clean herdr workspaces up in herdr |
| Automatic nudge (`fanout msg nudge`) | Delivered when the peer can take input | Disabled for every agent kind |
| tmux keybindings (dashboard, console return) | Registered | Not registered |
| Notifications | bell / tmux / ntfy / slack channels | bell / ntfy / slack work; the tmux channel and herdr's `notification show` do not fire |
| Codex Plan Mode | Opt-in via `codexPlanMode` | Unavailable |
| TUI forms (settings, help) | tmux popups | Inline in-process forms |
| Session resume | fanout's restore flow | Left to herdr (see below) |

Two consequences worth spelling out. A herdr pane whose `terminal_id` changed — after a cold server restart, for example — shows as `stale` rather than being re-bound. And because herdr keeps no exit status and drops the pane record on normal exit, a finished agent disappears from the herdr session instead of leaving a `✓ done` pane behind; the recorded fanout row stays and shows `stale`.

## herdr integrations and plugins

`herdr integration install claude` / `codex` writes hooks into your agent configuration that report the agent's session identity to herdr, which is what makes herdr's session tracking and restore work. fanout never runs it for you — your agent configuration stays yours. It is an optional step; consider it if you rely on restore.

Two plugin cautions:

- herdr notification plugins (ntfy, mobile push) fire alongside fanout's own `ntfy` / `slack` channels. Running both means duplicate notifications for the same events; keep one.
- herdr worktree-setup plugins run on every worktree herdr creates or opens — including a fanout worktree you open through herdr by hand. v1 fanout never issues herdr worktree operations itself.

The two tools sit on different layers. herdr plugins approach parallel agent work from the runtime side: launching worktrees from GitHub or Jira, a diff-review sidebar, a multi-project sidebar, layout and notification plugins. fanout approaches it from the GitHub workflow side: parent/child fan-out, briefing generation, blocker waves, PR lifecycle, and review gates. herdr runs and displays panes; fanout plans the work, launches it (on tmux), and tracks the GitHub side.

## Older fanout binaries

Older fanout binaries read the herdr fields in `.fanout/state.json` as unknown keys: a herdr row shows as stale there, and an old binary's `--close` leaves the herdr workspace behind — clean it up in herdr. Any state write from an old binary saves only the fields it knows, so it drops the herdr identity from the row.

The `--backend` flag and `FANOUT_BACKEND` are in the [CLI Reference]({{< relref "/docs/cli" >}}), the `runtimeBackend` key in [Settings]({{< relref "/docs/settings" >}}), and the herdr error messages with their fixes in [Troubleshooting]({{< relref "/docs/troubleshooting" >}}).
