---
title: herdr backend
linkTitle: herdr backend
description: "The opt-in herdr runtime backend: owned CLI launches, prerequisites, backend selection, differences from tmux, server restart and shutdown, and plugin cautions."
weight: 90
kanji: 観
yomi: herdr
---

The herdr backend runs CLI fan-outs in [herdr](https://herdr.dev/), a persistent-PTY runtime for coding agents. It is opt-in. Issue, Project, plan, label-watcher, and interactive TUI launches use a repository-scoped session owned by fanout. The no-argument TUI also supports merge, close, and cleanup for verified owned rows. The default backend stays tmux. fanout does not bundle herdr; install it separately. v0.8.0 and later are Apache-2.0; 0.7.x is AGPL-3.0 with a commercial option.

## What it does

For a CLI launch, fanout starts or readopts its repository-owned herdr session, creates one project-root coordinator workspace, creates a worktree workspace per child, and starts the selected agent through a pinned non-login fanout launcher. The launcher receives one operation-bound token, consumes an owner-only environment capsule once, and replaces itself with the agent without invoking a shell. Direct Claude and direct Codex receive the owned socket and exact pane ID their official session report needs, but not the session or workspace route. fanout records the exact workspace, pane, terminal, repository, agent, session, and socket identities in `.fanout/state.json` only after the launch is verified. It also records the provider session identity when the installed herdr integration reports one.

The owned session pins its own `config.toml`: fanout's launcher as the non-login default shell, herdr's restore-time agent resume off, and herdr's update manifest check off. When `dashboardKeybind` is enabled, it also registers a Herdr `F12` shell command that opens the web dashboard from the focused pane. Nothing restarts an agent behind fanout's back — resume is the explicit path below.

If `gh` authentication comes from `GH_TOKEN` or `GITHUB_TOKEN`, create the owned session from a shell that has the variable. fanout passes the token to the owned server under an internal control-plane name; it writes only its SHA-256 fingerprint to the ownership marker and keeps the value out of `config.toml` and the dashboard descriptor. If a live server did not inherit the current token, fanout removes the F12 binding. Close or clean up its rows, run `fanout herdr shutdown`, then relaunch from a shell with the token.

The persistent TUI console, `--status`, and the web dashboard show recorded sessions with each pane's runtime backend and identity (see [Monitoring]({{< relref "/docs/monitoring" >}})). The TUI console and web dashboard match herdr rows against `herdr api snapshot`; `--status` reads recorded state and GitHub only. Inside the owned console, the TUI can launch issue, Prompt, attached-agent, and shell panes, focus them, and peek at their output. The dashboard can peek at owned rows without adding a mutation endpoint. Before reading or mutating a session, fanout checks `herdr --version`, the exact owned route, and the saved workspace ownership label. A failed public method returns `herdr method "<name>" is unavailable`.

Claude launches receive launch-scoped `--settings` hooks. Codex Plan Mode launches receive the same launch-bound emitter environment without hooks; fanout's app-server controller reports `working`, `plan`, and `idle`. The emitter accepts `working`, `plan`, `blocked`, `idle`, and `done`, and the Claude lifecycle hooks report the states available from Claude's hook events. fanout accepts a report only when its row key, launch nonce, emitter nonce, saved pane identity, current herdr identity, and agent process match. A verified launch starts with synthetic `reported_state: running`; the first accepted provider report sets `state_refinement: true`. Codex launches outside Plan Mode and OpenCode launches do not install this emitter.

The TUI console and web dashboard use `reported_state` only while the matching pane and agent are live. `--status --format json` includes `reported_state`, and the table format shows it in `REPORTED_STATE`. The value does not complete an issue or authorize cleanup. Automatic nudge uses it only after current launch telemetry sets `state_refinement: true` and the live pane, worktree, agent, and process identity pass a fresh check. A disappeared pane remains `stale`.

`--merge`, `--close`, `--cleanup`, and their TUI actions operate only on complete rows from that owned session. fanout compares the saved workspace ID, label, terminal, repository, path, and branch before mutation. It records a cleanup intent, issues non-force `herdr worktree remove`, verifies that the checkout and workspace are absent, and closes a residual workspace when needed. A checkout left after an earlier workspace close is re-registered only after the owned plugin registry passes the empty-registry preflight.

Before issuing the removal, fanout separates tracked or untracked work from ignored files. Either state stops cleanup without issuing a herdr mutation; the error says which state blocked it. Retry checks the checkout again. A saved manual-cleanup intent whose failure was `dirty_worktree_requires_force` is replanned from the current checkout and workspace state, so committing or removing the files unblocks it ([#721](https://github.com/butaosuinu/fanout/issues/721)). An ambiguous issued mutation remains manual and is never replayed. Branch deletion uses fanout's compare-and-delete and applies only to a branch recorded as fanout-created.

### Recover a blocked cleanup

Use the worktree path from the cleanup error to inspect what remains:

```bash
git -C "<worktree>" status --short --untracked-files=all --ignored
```

Commit or stash tracked work before retrying. Commit untracked files or copy them elsewhere. If only ignored files remain, preview and then remove only ignored files:

```bash
git -C "<worktree>" clean -ndX
git -C "<worktree>" clean -fdX
```

Rerun the original `--close` / `--cleanup` command or TUI action. After a checkout-content block or a saved `dirty_worktree_requires_force` failure, a retry also closes a residual workspace when the worktree was removed separately.

For an ambiguous manual-cleanup error, do not delete `.fanout/state.json` or the intent journal. In a shell attached to the fanout-owned herdr session, use the workspace prefix from the saved pane ID (`w7` from `w7:p1`). Run only the command for the current state, then retry fanout so it can verify absence and remove the saved row:

```bash
# The worktree still exists.
herdr worktree remove --workspace <workspace-id> --json
# Only the workspace remains.
herdr workspace close <workspace-id>
```

The unsupported paths fail closed with a clear error.

- Interactive send, restore, and plan capture remain unavailable for herdr rows.
- TUI focus, launch, and peek require a complete saved identity in fanout's owned session. Foreign, stale, and legacy rows stay disabled with a reason.
- Focus additionally needs a saved agent session. Only the agent integration reports one, so focus is refused until `herdr integration install claude` / `codex` is in place. Three kinds of row stay refused even with it installed: agents started with attach ([#732](https://github.com/butaosuinu/fanout/issues/732)), Codex Plan Mode and team (their workload is fanout's controller, not the provider), and OpenCode. None of them receive the socket environment the hook needs.
- When a provider starts a new conversation in the same pane (Claude's `/clear`, Codex's `/new`), herdr replaces the conversation and also drops the agent record's name. The row's recorded conversation follows the new one, and fanout re-asserts the name it minted, so focus, peek, `--close`, and `--cleanup` keep working across it. A conversation from a different provider, a reference the runtime did not issue, and an agent record already answering to another name are all refused as before.
- Codex child Plan Mode runs through fanout's app-server controller and owned launcher. Claude and OpenCode keep their native mode flags.
- Herdr registers only the dashboard's direct `F12` command. The tmux-only `prefix + D`, `prefix + M`, console-return bindings, and in-app `notification show` remain unavailable.

The TUI header always shows the selected backend and why it was selected, such as `backend: herdr (HERDR_ENV)`.

## Prerequisites

- **stable herdr 0.7.5 or newer** — the CLI and server must run the same stable version. Prerelease and malformed versions fail closed. fanout does not reject newer stable versions based on protocol, API schema, CLI help, platform, or an exact release digest.
- The `herdr` binary on your `PATH`, installed separately.
- The selected agent CLI on your `PATH`.

CLI and no-argument TUI launches do not require a pre-existing herdr session: fanout creates or adopts an isolated session under its owner marker. A TUI started inside a foreign herdr session remains observational; its interactive actions do not gain owned-session authority (`default` is rejected).

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

A parent that already has recorded panes keeps its recorded backend. A conflicting `--backend` or `FANOUT_BACKEND` fails with `explicit migration is required` rather than mixing backends under one parent. There is no backend migration command — existing tmux parents stay on tmux.

## Start an owned session

From a plain shell, no-argument `fanout` bootstraps the session and enters it:

```bash
export FANOUT_BACKEND=herdr
fanout
```

That starts or adopts the owned session and one repository-root console workspace, then replaces the fanout process with the pinned herdr client — the terminal is attached, nothing to copy. The console pane runs fanout's TUI console directly, so entering the session lands you in the console. The client opens on the session's last selected workspace, so after a CLI fan-out you may land on an agent pane first; the console workspace is the sidebar row without an issue. Quitting the TUI drops the pane to your shell — run `fanout` there to reopen it (herdr sets `HERDR_ENV=1` in the pane); a session reused after a quit keeps that shell until you do. Linked worktrees share the one console row.

Like the detached tmux console, the console TUI stays resident in the owned session while nobody is attached: it keeps refreshing state and GitHub, and a watcher enabled in user config keeps running.

Without a terminal — stdin or stdout is a pipe, as in scripts and CI — fanout prints the attach command as the last stdout line instead of attaching. A failed exec falls back to the same print, so the command is always there to run by hand.

A CLI fan-out needs neither an attach nor an existing session. It creates or adopts the same owned session, adds the project-root coordinator workspace and one worktree workspace per child, and starts each agent; attach when you want to watch them.

## Coming from the observation-only release

v0.13.0's herdr backend was observation-only: you started a named herdr session yourself, fanout pinned herdr 0.7.3 exactly, and every launch and mutation was refused — so it recorded no herdr rows of its own. To move to the owned model:

1. Upgrade the `herdr` CLI to stable 0.7.5 or newer; 0.7.3 and 0.7.4 now fail closed with `unsupported herdr CLI version …`. The owned session starts its own server from the pinned CLI, so your existing herdr server needs no restart.
2. Stop starting and naming a session by hand. fanout creates its own under an owner marker; in a session it does not own, interactive actions stay disabled with a reason.
3. Leave the old herdr pane and bootstrap from a plain shell, with `HERDR_SESSION` and `HERDR_SOCKET_PATH` unset. Those variables still select the server fanout reads foreign rows from, and inside a herdr pane they must match the owned session or the TUI drops back to observation with a warning.

`.fanout/state.json` needs no conversion, and tmux parents are untouched.

## Differences from tmux

| Capability | tmux backend | herdr backend |
|---|---|---|
| Issue / Project / plan / watcher launch | Creates worktrees, panes, agents | Creates owned herdr workspaces and verified agents |
| Worktree creation | One per child under `.fanout/worktrees/` | One per child through `herdr worktree create` / `open` |
| Liveness and agent state (TUI console, web dashboard) | tmux queries and pane options | `herdr api snapshot` plus launch-bound Claude or Codex Plan telemetry |
| Exit status display | Launch wrapper reports `✓ done` | None — herdr's public API keeps no exit status |
| Pane after the agent exits | Pane stays open with the wrapper message | herdr drops the pane and its own record on normal exit; the fanout row turns `stale` |
| Interactive TUI launch / focus / peek | TUI keys | Available for ownership-verified panes in fanout's session |
| TUI focus + zoom (`Z`) | Focuses the pane, then zooms it | Unavailable — `herdr backend interactive TUI action is unavailable; zoom is unavailable` |
| Interactive send / restore / plan capture | Supported tmux paths | Unavailable — `runtime backend herdr does not support …` |
| `--team` peer messaging | SQLite registry, Claude watcher, Codex app-server bridge | Same registry and push lanes |
| `--merge`, `--close`, `--cleanup`; TUI merge / close / cleanup | Supported | Supported for verified fanout-owned rows; cleanup never forces a dirty checkout |
| Automatic nudge (`fanout msg nudge`) | Delivered when the peer can take input | One no-wait `agent prompt` after fresh refined telemetry and live identity/process checks; otherwise no-op |
| Global keybindings | `F12` / `prefix + D` dashboard, `prefix + M` worktree action, console return | `F12` dashboard only, through the fanout-owned Herdr config |
| Notifications | bell / tmux / ntfy / slack channels | bell / ntfy / slack work; the tmux channel and herdr's `notification show` do not fire |
| Child Plan Mode launch | Supported | Supported; Codex uses fanout's app-server controller, while Claude / OpenCode use native flags |
| TUI forms (settings, help) | tmux popups | Inline in-process forms |
| Session resume | fanout's restore flow | An explicit `fanout herdr restart` resumes an exactly verified direct Codex session; every other provider or incomplete binding stays `stale` |

An explicit `fanout herdr restart` re-binds a direct Codex row only when the restored shell placeholder has the exact saved `agent_session` and the launched process matches the saved absolute executable, `codex resume <session-id>` argv, cwd, ancestry, and foreground process group. Missing, duplicate, mismatched, or unverifiable data leaves the row `stale`; Claude, OpenCode, Codex Plan / Team controllers, and a Codex attached to an existing worktree from the TUI are never resumed by this path. An `idle` placeholder does not prove process liveness or completion.

Because herdr keeps no exit status and drops the pane record on normal exit, a finished agent disappears from the herdr session instead of leaving a `✓ done` pane behind; the recorded fanout row stays and shows `stale`.

## Restart and shutdown

fanout never restarts or stops the owned server as a side effect. Two explicit verbs do it:

```bash
fanout herdr restart    # replace a dead owned server, then re-bind what verifies
fanout herdr shutdown   # stop an empty owned server
```

`restart` is for a server that died. fanout replaces it only after proving the old supervisor process and its sockets are gone — a generation that is still running refuses with `herdr owned server generation is still live` — then starts a fresh one, replaces an owned `config.toml` written by an older fanout, and re-binds the recorded rows under the rules above. Rerunning either verb after a failure is safe: fanout records what it set out to do and confirms that outcome rather than repeating the work.

After a fanout update, a launch may refuse with `owned Herdr launcher predates the current fanout`. Remove child rows with `--close` / `--cleanup`, quit the console TUI and exit its shell along with any coordinator shells, then run `fanout herdr shutdown`. `restart` still does not replace a live generation; `shutdown` folds the empty session and its stale scaffold rows so the next launch can create a generation with the current launcher.

`shutdown` also recovers a `realized` intent left by a failed launch. It prunes the intent only when an owned-workspace snapshot has no workspace with the recorded label; worktree and resume intents additionally require the checkout to be absent. If a fanout-created branch remains, shutdown compare-and-deletes it only at the saved base SHA before pruning the intent. A failed snapshot, a matching workspace, a remaining checkout, a moved branch, or a branch observation failure keeps the intent and refuses shutdown.

`shutdown` retires an empty server. It refuses while a child herdr row remains in this repository's state — every linked worktree counts — while the owned session still holds a workspace, or while an unprunable herdr intent is pending. After proving the workspace snapshot is empty, shutdown removes the console row recorded by a plain-shell TUI bootstrap and the project-root coordinator rows recorded by issue, Project, and plan fan-outs. Exit any running shell first.

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

`herdr integration install claude` / `codex` writes hooks into your agent configuration that report the agent's session identity to herdr, which is what makes herdr's session tracking and restore work. fanout never runs it for you — your agent configuration stays yours. It is optional, but TUI focus on a Claude or Codex row depends on the session those hooks report, as does herdr's own session restore.

fanout passes `HERDR_ENV`, `HERDR_SOCKET_PATH`, and `HERDR_PANE_ID` to Claude and Codex workloads so the hook reaches the owned socket; the session and workspace route stay with the launcher. The hook itself lives in your own agent configuration, outside the owned session's XDG isolation, so it needs no relocation.

fanout-owned sessions isolate herdr's XDG directories and require an empty plugin registry before creating a workspace or worktree. Herdr notification and worktree-setup plugins do not run for fanout-owned launches. A non-empty registry makes the launch fail before mutation; use fanout's notification channels and hooks for those launches.

Keep one notification source per event. fanout's `ntfy` / `slack` channels announce agent transitions — plan ready, waiting for input — next to PR and CI transitions, and a herdr notification plugin announces its own view of the same agent states. Owned launches never reach those plugins, so the duplicate only appears in sessions you configure yourself; turn one side off there. The agent integration is a separate path: its hooks report the session identity to herdr, run alongside fanout's launch-scoped hooks, and send no notification.

The two tools sit on different layers. Outside fanout-owned sessions, herdr plugins approach parallel agent work from the runtime side: launching worktrees from GitHub or Jira, a diff-review sidebar, a multi-project sidebar, layout, and notifications. fanout approaches it from the GitHub workflow side: parent/child fan-out, briefing generation, blocker waves, PR lifecycle, and review gates. herdr runs and displays panes; fanout plans the work, launches it on tmux or herdr, and tracks the GitHub side.

## Older fanout binaries

Older fanout binaries read the herdr fields in `.fanout/state.json` as unknown keys: a herdr row shows as stale there, and an old binary's `--close` leaves the herdr workspace behind — clean it up in herdr. Any state write from an old binary saves only the fields it knows, so it drops the herdr identity from the row.

The `--backend` flag and `FANOUT_BACKEND` are in the [CLI Reference]({{< relref "/docs/cli" >}}), the `runtimeBackend` key in [Settings]({{< relref "/docs/settings" >}}), and the herdr error messages with their fixes in [Troubleshooting]({{< relref "/docs/troubleshooting" >}}).
