---
title: herdr backend
linkTitle: herdr backend
description: "The opt-in herdr runtime backend: owned CLI launches, prerequisites, backend selection, differences from tmux, and plugin cautions."
weight: 90
kanji: 観
yomi: herdr
---

The herdr backend runs CLI fan-outs in [herdr](https://herdr.dev/), a persistent-PTY runtime for coding agents. It is opt-in. Issue, Project, plan, label-watcher, and interactive TUI launches use a repository-scoped session owned by fanout. The no-argument TUI also supports merge, close, and cleanup for verified owned rows. The default backend stays tmux. fanout does not bundle herdr; install it separately. v0.8.0 and later are Apache-2.0; 0.7.x is AGPL-3.0 with a commercial option.

## What v1 does

For a CLI launch, fanout starts or readopts its repository-owned herdr session, creates one project-root coordinator workspace, creates a worktree workspace per child, and starts the selected agent through a pinned non-login fanout launcher. The launcher receives one operation-bound token, consumes an owner-only environment capsule once, and replaces itself with the agent without invoking a shell. fanout records the exact workspace, pane, terminal, repository, agent, session, and socket identities in `.fanout/state.json` only after the launch is verified. It also records the provider session identity when the installed herdr integration reports one.

From a plain shell, no-argument `fanout` starts or adopts the owned session, launches one repository-root console shell, and prints the isolated attach command. Run that command to enter the console workspace; fanout does not replace or attach the calling shell itself. The exact console row is shared across linked worktrees.

The persistent TUI console, `--status`, and the web dashboard show recorded sessions with each pane's runtime backend and identity (see [Monitoring]({{< relref "/docs/monitoring" >}})). The TUI console and web dashboard match herdr rows against `herdr api snapshot`; `--status` reads recorded state and GitHub only. Inside the owned console, the TUI can launch issue, Prompt, attached-agent, and shell panes, focus them, and peek at their output. The dashboard can peek at owned rows without adding a mutation endpoint. Before reading or mutating a session, fanout checks `herdr --version`, the exact owned route, and the saved workspace ownership label. A failed public method returns `herdr method "<name>" is unavailable`.

Claude launches receive launch-scoped `--settings` hooks. Codex Plan Mode launches receive the same launch-bound emitter environment without hooks; fanout's app-server controller reports `working`, `plan`, and `idle`. The emitter accepts `working`, `plan`, `blocked`, `idle`, and `done`, and the Claude lifecycle hooks report the states available from Claude's hook events. fanout accepts a report only when its row key, launch nonce, emitter nonce, saved pane identity, current herdr identity, and agent process match. A verified launch starts with synthetic `reported_state: running`; the first accepted provider report sets `state_refinement: true`. Codex launches outside Plan Mode and OpenCode launches do not install this emitter.

The TUI console and web dashboard use `reported_state` only while the matching pane and agent are live. `--status --format json` includes `reported_state`, and the table format shows it in `REPORTED_STATE`. The value does not complete an issue or authorize cleanup. Automatic nudge uses it only after current launch telemetry sets `state_refinement: true` and the live pane, worktree, agent, and process identity pass a fresh check. A disappeared pane remains `stale`.

`--merge`, `--close`, `--cleanup`, and their TUI actions operate only on complete rows from that owned session. fanout compares the saved workspace ID, label, terminal, repository, path, and branch before mutation. It records a cleanup intent, issues non-force `herdr worktree remove`, verifies that the checkout and workspace are absent, and closes a residual workspace when needed. A checkout left after an earlier workspace close is re-registered only after the owned plugin registry passes the empty-registry preflight. Dirty checkouts, ownership mismatches, and ambiguous responses preserve the row and intent for retry or manual cleanup. Branch deletion uses fanout's compare-and-delete and applies only to a branch recorded as fanout-created.

The unsupported paths still fail closed:

- Interactive send, restore, and plan capture remain unavailable for herdr rows.
- TUI focus, launch, and peek require a complete saved identity in fanout's owned session. Foreign, stale, and legacy rows stay disabled with a reason.
- Codex child Plan Mode runs through fanout's app-server controller and owned launcher. Claude and OpenCode keep their native mode flags.
- No tmux keybindings are registered, and fanout never calls herdr's in-app `notification show`.

The TUI header always shows the selected backend and why it was selected, such as `backend: herdr (HERDR_ENV)`.

## Prerequisites

- **stable herdr 0.7.5 or newer** — the CLI and server must run the same stable version. Prerelease and malformed versions fail closed. fanout does not reject newer stable versions based on protocol, API schema, CLI help, platform, or an exact release digest.
- The `herdr` binary on your `PATH`, installed separately.
- The selected agent CLI on your `PATH`.

CLI and no-argument TUI launches do not require a pre-existing herdr session. fanout creates or adopts an isolated session under its owner marker. After a plain-shell TUI bootstrap, run the printed attach command. A TUI started inside a foreign herdr session remains observational; its interactive actions do not gain owned-session authority (`default` is rejected).

## Opting in

Select herdr explicitly for one CLI run:

```bash
fanout 123 --backend herdr --agent claude
fanout plan launch-plan --backend herdr --agent claude
fanout 123 --backend herdr --agent codex --team
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
| Liveness and agent state (TUI console, web dashboard) | tmux queries and pane options | `herdr api snapshot` plus launch-bound Claude or Codex Plan telemetry |
| Exit status display | Launch wrapper reports `✓ done` | None — herdr's public API keeps no exit status |
| Pane after the agent exits | Pane stays open with the wrapper message | herdr drops the pane and its own record on normal exit; the fanout row turns `stale` |
| Interactive TUI launch / focus / peek | TUI keys | Available for ownership-verified panes in fanout's session |
| Interactive send / restore / plan capture | Supported tmux paths | Unavailable — `runtime backend herdr does not support …` |
| `--team` peer messaging | SQLite registry, Claude watcher, Codex app-server bridge | Same registry and push lanes |
| `--merge`, `--close`, `--cleanup`; TUI merge / close / cleanup | Supported | Supported for verified fanout-owned rows; cleanup never forces a dirty checkout |
| Automatic nudge (`fanout msg nudge`) | Delivered when the peer can take input | One no-wait `agent prompt` after fresh refined telemetry and live identity/process checks; otherwise no-op |
| tmux keybindings (dashboard, console return) | Registered | Not registered |
| Notifications | bell / tmux / ntfy / slack channels | bell / ntfy / slack work; the tmux channel and herdr's `notification show` do not fire |
| Child Plan Mode launch | Supported | Supported; Codex uses fanout's app-server controller, while Claude / OpenCode use native flags |
| TUI forms (settings, help) | tmux popups | Inline in-process forms |
| Session resume | fanout's restore flow | An explicit `fanout herdr restart` resumes an exactly verified direct Codex session; every other provider or incomplete binding stays `stale` |

An explicit `fanout herdr restart` re-binds a direct Codex row only when the restored shell placeholder has the exact saved `agent_session` and the launched process matches the saved absolute executable, `codex resume <session-id>` argv, cwd, ancestry, and foreground process group. Missing, duplicate, mismatched, or unverifiable data leaves the row `stale`; Claude, OpenCode, and Codex Plan / Team controllers are never resumed by this path. An `idle` placeholder does not prove process liveness or completion.

Because herdr keeps no exit status and drops the pane record on normal exit, a finished agent disappears from the herdr session instead of leaving a `✓ done` pane behind; the recorded fanout row stays and shows `stale`.

## Sidebar tokens

Every verified launch reports five display-only tokens under the source `fanout`, so a sidebar row can name the fan-out child it belongs to. The tokens are presentation data: fanout never reads them back, and they carry no backend state, liveness, or completion.

| Token | Resource | Value |
|---|---|---|
| `fanout_issue` | Workspace | `#<issue>` for issue and Project children, the task ID for a `fanout plan` task |
| `fanout_slug` | Workspace | The child slug, which is also its worktree directory name |
| `fanout_parent` | Pane | `#<parent>`, `plan:<slug>`, or the Projects path. A watcher launch names the issue it picked up |
| `fanout_pr` | Pane | Reserved for the pull request; cleared on every report today |
| `fanout_ci` | Pane | Reserved for CI; cleared on every report today |

One report writes fanout's whole token set for a resource and clears whatever it has no value for, so a reused workspace or pane never shows a stale value. fanout reports once, right after the launch verifies its live identity, and sends no `seq` and no `ttl_ms`. A cold herdr restart drops every token and changes `terminal_id`; an exact Codex resume can re-bind the row, but fanout does not re-send the display tokens.

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
