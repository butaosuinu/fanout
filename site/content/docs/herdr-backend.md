---
title: herdr backend
linkTitle: herdr backend
description: "The opt-in herdr runtime backend: owned CLI launches, prerequisites, backend selection, differences from tmux, and plugin cautions."
weight: 90
kanji: 観
yomi: herdr
---

The herdr backend runs CLI fan-outs in [herdr](https://herdr.dev/), a persistent-PTY runtime for coding agents. It is opt-in. Issue, Project, plan, and label-watcher launches use a repository-scoped session owned by fanout; the no-argument TUI remains read-only under herdr. The default backend stays tmux. fanout does not bundle herdr; it is AGPL-licensed and installed separately.

## What v1 does

For a CLI launch, fanout starts or readopts its repository-owned herdr session, creates one project-root coordinator workspace, creates a worktree workspace per child, and starts the selected agent through a pinned non-login fanout launcher. The launcher receives one operation-bound token, consumes an owner-only environment capsule once, and replaces itself with the agent without invoking a shell. fanout records the exact workspace, pane, terminal, repository, agent, session, and socket identities in `.fanout/state.json` only after the launch is verified. It also records the provider session identity when the installed herdr integration reports one.

The persistent TUI console, `--status`, and the web dashboard show recorded sessions with each pane's runtime backend and identity (see [Monitoring]({{< relref "/docs/monitoring" >}})). The TUI console and web dashboard match herdr rows against `herdr api snapshot`; `--status` reads recorded state and GitHub only. Before reading or mutating a session, fanout checks `herdr --version` and the exact owned route. A failed public method returns `herdr method "<name>" is unavailable`.

The unsupported paths still fail closed:

- Interactive TUI launch, focus, send, restore, output peek, and plan capture are unavailable for herdr rows.
- herdr with `--team` is rejected before state, filesystem, Git, or herdr mutation.
- Codex child Plan Mode is rejected; use build mode until its app-server launch matrix is supported. Claude and OpenCode keep their native mode flags.
- Automatic nudges (`fanout msg nudge` delivery) are disabled for every agent kind. Messages still persist to the bus for `inbox` / `board` reads.
- No tmux keybindings are registered, and fanout never calls herdr's in-app `notification show`.

The TUI header always shows the selected backend and why it was selected, such as `backend: herdr (HERDR_ENV)`.

## Prerequisites

- **stable herdr 0.7.5 or newer** — the CLI and server must run the same stable version. Prerelease and malformed versions fail closed. fanout does not reject newer stable versions based on protocol, API schema, CLI help, platform, or an exact release digest.
- The `herdr` binary on your `PATH`, installed separately.
- The selected agent CLI on your `PATH`.

CLI launches do not require a pre-existing herdr session. fanout creates or readopts an isolated session under its owner marker. The no-argument TUI is different: to observe an external named session, start the console from a pane in that session (`default` is rejected).

## Opting in

Select herdr explicitly for one CLI run:

```bash
fanout 123 --backend herdr --agent claude
fanout plan launch-plan --backend herdr --agent claude
```

For repeated runs, set `FANOUT_BACKEND=herdr` or the user-only `runtimeBackend` setting. Running fanout inside an existing herdr pane also sets `HERDR_ENV=1`, which selects herdr automatically. The label watcher uses the same owned launch path and revalidates the session before each launch.

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
| Issue / Project / plan / watcher launch | Creates worktrees, panes, agents | Creates owned herdr workspaces and verified agents |
| Worktree creation | One per child under `.fanout/worktrees/` | One per child through `herdr worktree create` / `open` |
| Liveness and agent state (TUI console, web dashboard) | tmux queries | `herdr api snapshot` — supported |
| Exit status display | Launch wrapper reports `✓ done` | None — herdr's public API keeps no exit status |
| Pane after the agent exits | Pane stays open with the wrapper message | herdr drops the pane and its own record on normal exit; the fanout row turns `stale` |
| Interactive TUI launch / focus / send / restore / peek / plan capture | TUI keys and lifecycle flags | Unavailable — `runtime backend herdr does not support …` |
| Automatic nudge (`fanout msg nudge`) | Delivered when the peer can take input | Disabled for every agent kind |
| tmux keybindings (dashboard, console return) | Registered | Not registered |
| Notifications | bell / tmux / ntfy / slack channels | bell / ntfy / slack work; the tmux channel and herdr's `notification show` do not fire |
| Child Plan Mode launch | Supported | Claude / OpenCode only; Codex is rejected |
| TUI forms (settings, help) | tmux popups | Inline in-process forms |
| Session resume | fanout's restore flow | Left to herdr (see below) |

Two consequences worth spelling out. A herdr pane whose `terminal_id` changed — after a cold server restart, for example — shows as `stale` rather than being re-bound. And because herdr keeps no exit status and drops the pane record on normal exit, a finished agent disappears from the herdr session instead of leaving a `✓ done` pane behind; the recorded fanout row stays and shows `stale`.

## Sidebar tokens

Every verified launch reports five display-only tokens under the source `fanout`, so a sidebar row can name the fan-out child it belongs to. The tokens are presentation data: fanout never reads them back, and they carry no backend state, liveness, or completion.

| Token | Resource | Value |
|---|---|---|
| `fanout_issue` | Workspace | `#<issue>` for issue and Project children, the task ID for a `fanout plan` task |
| `fanout_slug` | Workspace | The child slug, which is also its worktree directory name |
| `fanout_parent` | Pane | `#<parent>`, `plan:<slug>`, or the Projects path. A watcher launch names the issue it picked up |
| `fanout_pr` | Pane | Reserved for the pull request; cleared on every report today |
| `fanout_ci` | Pane | Reserved for CI; cleared on every report today |

One report writes fanout's whole token set for a resource and clears whatever it has no value for, so a reused workspace or pane never shows a stale value. fanout reports once, right after the launch verifies its live identity, and sends no `seq` and no `ttl_ms`. A cold herdr restart drops every token and changes `terminal_id`, which turns the fanout row `stale`; fanout does not re-send.

Rows and styling stay yours — fanout never writes sidebar config. Reference a token as `$name`:

```toml
[ui.sidebar.spaces]
rows = [["state_icon", "workspace"], ["$fanout_issue", "$fanout_slug"], ["branch", "git_status"]]

[ui.sidebar.agents]
rows = [["state_icon", "workspace", "tab"], ["$fanout_parent", "$fanout_pr", "$fanout_ci"], ["agent"]]
```

A fanout-owned session pins its own `config.toml`, so this example applies to a herdr session you configure yourself. Inside a fanout-owned session the tokens are readable through `herdr api snapshot`, and no sidebar row references them yet.

## herdr integrations and plugins

`herdr integration install claude` / `codex` writes hooks into your agent configuration that report the agent's session identity to herdr, which is what makes herdr's session tracking and restore work. fanout never runs it for you — your agent configuration stays yours. It is an optional step; consider it if you rely on restore.

fanout-owned sessions isolate herdr's XDG directories and require an empty plugin registry before creating a workspace or worktree. Herdr notification and worktree-setup plugins do not run for fanout-owned launches. A non-empty registry makes the launch fail before mutation; use fanout's notification channels and hooks for those launches.

The two tools sit on different layers. Outside fanout-owned sessions, herdr plugins approach parallel agent work from the runtime side: launching worktrees from GitHub or Jira, a diff-review sidebar, a multi-project sidebar, layout, and notifications. fanout approaches it from the GitHub workflow side: parent/child fan-out, briefing generation, blocker waves, PR lifecycle, and review gates. herdr runs and displays panes; fanout plans the work, launches it on tmux or herdr, and tracks the GitHub side.

## Older fanout binaries

Older fanout binaries read the herdr fields in `.fanout/state.json` as unknown keys: a herdr row shows as stale there, and an old binary's `--close` leaves the herdr workspace behind — clean it up in herdr. Any state write from an old binary saves only the fields it knows, so it drops the herdr identity from the row.

The `--backend` flag and `FANOUT_BACKEND` are in the [CLI Reference]({{< relref "/docs/cli" >}}), the `runtimeBackend` key in [Settings]({{< relref "/docs/settings" >}}), and the herdr error messages with their fixes in [Troubleshooting]({{< relref "/docs/troubleshooting" >}}).
