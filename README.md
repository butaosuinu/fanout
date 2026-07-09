# fanout

[English](README.md) | [日本語](README.ja.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Latest release](https://img.shields.io/github/v/release/butaosuinu/fanout)](https://github.com/butaosuinu/fanout/releases)
[![Docs](https://img.shields.io/badge/docs-butaosuinu.github.io%2Ffanout-2b7a78)](https://butaosuinu.github.io/fanout/)

**Parallel agent orchestrator for tmux.** Point fanout at a GitHub parent
issue's OPEN sub-issues — or a local plan spec — and it fans each child or task
out into its own tmux pane, git worktree and agent CLI (Claude Code / Codex),
launched from a per-task briefing. Run it again and nothing gets a second pane:
`.fanout/state.json` remembers.

📖 **Full documentation:** <https://butaosuinu.github.io/fanout/> — installation,
quickstart, the complete CLI reference, settings, and troubleshooting, in
English and 日本語.

![fanout web dashboard](docs/assets/dashboard.jpg)

## Features

- **Idempotent fan-out** — `.fanout/state.json` keys every `(parent, issue)`
  pane, so reruns never duplicate work.
- **Pane border labels** — each pane shows its parent and name on its top
  border (e.g. `#123 · fix-login-bug-123`, `plan:my-feature · task-slug`), so
  tiled panes are easy to tell apart.
- **Wave progression** — `--unblocked-only` reads blockers and fans out only
  unblocked children; rerun as PRs merge and the next wave opens.
- **Persistent TUI console** — run `fanout` with no arguments for a live
  pane / issue / PR view with a compact Session navigator plus focus, peek,
  terminal, same-worktree agent attach, lifecycle keys, and automatic restore
  of missing worktree panes. The new-session popup starts work from a free
  prompt or an OPEN issue picked from a list; `Ctrl+O` opens the selected issue
  in the browser, and per-child agent choices let one task use claude while
  another uses codex. A prompt-mode checkbox instead decomposes the prompt into
  parallel tasks via `/fanout plan`. The `s` key opens the settings popup.
  Return to the console from any pane with `F11` or `prefix + T`; mouse or
  `prefix` movement keys keep the selected row in sync with the focused pane.
- **Label watcher** — opt in to a TUI-resident watcher that turns trusted
  `fanout:auto` issues into one-shot fanout sessions.
- **Web dashboard** — a read-only localhost dashboard with live updates; pop it
  from any pane with `F12` or `prefix + D`. Use `prefix + M` for
  same-worktree actions from a recorded pane.
- **Status & reporting** — `--status` JSON / table with PR review and CI state,
  plus an optional dashboard comment on the parent issue.
- **Lifecycle hooks** — user-configured shell commands around worktree, pane,
  and merge events.
- **Agent integrations** — the `/fanout` slash command and skills for Claude
  Code & Codex, bundled with the install.

## Installation

```bash
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh
```

This installs the `fanout` binary (to `~/.local/bin` by default) plus the
bundled Claude/Codex integration files. Binary-only install, custom
destinations, pinned versions, uninstall, and building from a checkout are all
covered in the [installation docs](https://butaosuinu.github.io/fanout/docs/installation/).

Make sure `~/.local/bin` is on your `PATH`.

## Quick start

From inside a tmux session, in the repository you want to work in:

```bash
# pick the child agent once (or pass --agent claude / --agent codex per run)
export FANOUT_AGENT=claude

fanout 123 --dry-run    # preview commands; create no worktrees, panes, state, or briefings
fanout 123              # fan every OPEN child of issue #123 out into its own pane
fanout 123 --status     # PR review + CI state for the fanned children
```

Run `fanout` with no arguments to open the persistent TUI console for the
current repository. Restarting it reuses existing state, rebinds panes still
alive in tmux, and recreates missing worktree panes by resuming their agent
CLI. See the
[five-minute quickstart](https://butaosuinu.github.io/fanout/docs/quickstart/)
for a guided first run.

## How it works

Three moves — **prepare → fan out → fold away**:

1. **Prepare the work.** Create a parent issue and its OPEN child issues (the
   bundled `fanout-issues` skill encodes blocker waves for you), or write a local
   plan spec for `fanout plan` — no issues required.
2. **Fan it out.** One run of `fanout 123` or `fanout plan spec.json`: one child
   or task = one worktree = one tmux pane = one agent.
3. **Merge and fold away.** Watch progress through the TUI or `--status`, then
   `--merge` and `--cleanup` finished work, and open the next wave.

`fanout plan <spec>` fans out a local plan spec instead of GitHub child issues,
and `--team` + `fanout msg` give sibling panes lightweight peer messaging — both
detailed in the [workflow docs](https://butaosuinu.github.io/fanout/docs/workflow/).

## Watcher mode

The watcher runs only while the no-argument TUI console is open. It is off by
default, and only user config or environment variables can enable it; repo
config cannot opt a checkout into background launches.

```bash
# One shell
export FANOUT_WATCHER=1
export FANOUT_WATCHER_AGENT=codex
fanout
```

Add `fanout:auto` to a trusted issue to queue it. On the next cycle fanout
swaps that label to `fanout:running`, then launches either a standalone pane
for an issue with no OPEN children or a normal parent fan-out for an issue with
OPEN children. Parent fan-outs use `--unblocked-only`; every watcher launch
counts against `watcherMaxSessions`. If blocked children or the session cap
leaves work for later, fanout swaps `fanout:running` back to `fanout:auto` so a
later cycle retries the parent automatically.

For parent fan-outs, `fanout <parent> --merge <child>`, `--close`, and
`--cleanup` remove `fanout:running` best-effort. For standalone watcher panes,
use the TUI lifecycle keys (`m`, `c`, `x`); the public CLI parent argument does
not target reserved `@watch` rows. To queue a fresh run after a standalone pane
or fully cleaned parent, add `fanout:auto` again. Do not apply the trigger label
to untrusted issues: the labeled issue and any OPEN children it launches become
agent briefings.

This watcher is separate from [#107](https://github.com/butaosuinu/fanout/issues/107):
it discovers labeled issues across the repository and starts one-shot sessions.
#107 remains the skill-led loop for revisiting children under a known parent.

## Daily commands

| Command | What it does |
|---|---|
| `fanout 123 --agent claude` | Fan the parent's OPEN children out into parallel panes |
| `fanout 123 --unblocked-only` | Fan out only children whose blockers are closed — the next wave |
| `fanout 123 --dry-run` | Print the plan without modifying git, tmux, state, or briefing files |
| `fanout plan spec.json --agent claude` | Fan out a local plan spec instead of GitHub child issues |
| `fanout` | Start the persistent TUI console (Session jump, numeric jump 1-9, focus, zoom, peek, compact switcher below 80 columns (`v`), terminal, prompt / issue session launch, settings popup (`s`), same-worktree attach, restore, lifecycle keys) |
| `fanout 123 --status` | Pane, PR review, and CI state as JSON or a table |
| `fanout dashboard --web` | Serve the read-only web dashboard on localhost |
| `fanout 123 --merge 4` | Fast-forward merge a child branch (`--close` / `--cleanup` fold panes away) |

Fan-out runs need a child agent — pass `--agent claude` / `--agent codex` or set
`FANOUT_AGENT`; the status, dashboard, and lifecycle commands (`--status`,
`dashboard`, `--merge`) don't.
The [full CLI reference](https://butaosuinu.github.io/fanout/docs/cli/) documents
every flag, environment variable, and exit code.

## Agent integrations

`make install` (and the install script) place a `/fanout` slash command and a
set of skills under `~/.claude/` and `~/.codex/`, so Claude Code and Codex can
recognize when fanout applies, preview with `--dry-run`, and run it after you
confirm. fanout itself never calls an LLM — the skills generate flags from issue
context. See the
[agent integration docs](https://butaosuinu.github.io/fanout/docs/agents/).

## Prerequisites

- **git** and **tmux 3.3+**. GitHub issue / Project workflows, PR status, and
  cleanup/status views also need the **GitHub CLI (`gh`)**, authenticated
  (`gh auth status`); local `fanout plan` runs and manual TUI panes do not.
- The agent CLI you launch children with — **`claude`** (Claude Code) and/or
  **`codex`** — on your `PATH` for live runs. The install only bundles fanout's
  skills/commands for them; it does not install the agents themselves.
  (`--dry-run` and read-only commands don't need one.)
- Batch fan-out (`fanout <parent>`) must run from inside tmux; the no-argument
  TUI console can start from a plain shell.
- Project mode needs the `read:project` gh scope
  (`gh auth refresh -s read:project`).
- Building from a checkout additionally needs Go 1.26.5+, Node.js 24+, and
  pnpm 10+ (the curl install ships a prebuilt binary and needs neither).

## Development

```bash
make build-go   # build the web bundle + the ./fanout-go CLI
make test       # Go unit tests + web vitest + bats black-box tiers
make lint       # golangci-lint v2 + shellcheck (make lint-web for the web UI)
```

See [CLAUDE.md](CLAUDE.md) for the repository architecture and maintenance notes.

## License

This project is licensed under the [MIT License](LICENSE).
