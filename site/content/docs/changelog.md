---
title: Changelog
linkTitle: Changelog
description: "What changed in each fanout release — newest first, with links into the docs."
weight: 90
kanji: 録
yomi: changelog
---

Release highlights, newest first. Every tag also has a [GitHub release](https://github.com/butaosuinu/fanout/releases) with the full commit list and prebuilt binaries (darwin / linux × amd64 / arm64). Versions come from git tags via ldflags — check yours with `fanout --check-update`.

## v0.8.0 — 2026-06-24

- **Label watcher.** Opt in to a TUI-resident watcher that turns trusted `fanout:auto` issues into one-shot fanout sessions. It swaps the trigger label to `fanout:running` before launch, classifies issues with OPEN children as parent fan-outs, and honors a live-pane budget. Enable it only through user config or `FANOUT_WATCHER*` environment variables; repo config can set the labels, interval, child agent, and session cap but never opt a checkout into launching. See [Workflow]({{< relref "/docs/workflow" >}}) and [Settings]({{< relref "/docs/settings" >}}).
- **Dashboard spans every worktree.** The web dashboard now aggregates Sessions across every worktree in the repo, not just the current one, so a single browser tab covers all parallel work. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Multi-agent TUI launches.** The persistent console's `n` modal now takes per-agent `claude` / `codex` launch counts behind framed text inputs, and `codex` panes start in Plan Mode. See [Monitoring]({{< relref "/docs/monitoring" >}}).

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.8.0)

## v0.7.0 — 2026-06-21

- **Lifecycle hooks.** fanout now runs user shell hooks around worktree, pane, and merge events. Configure them in `$XDG_CONFIG_HOME/fanout/hooks.json` (a Codex-style `hooks` object); they are always enabled, and a missing file or an event with no commands is a no-op. See [CLI Reference]({{< relref "/docs/cli" >}}).
- **TUI shell terminals.** The persistent console can now open a plain shell in the selected row's worktree with `A`, or at the project root with `t`. Shell rows are recorded as manual entries for focus / peek, and close removes only the tmux pane and state row. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Compact Session navigator.** The persistent console gained a compact Session navigator for jumping between sessions, alongside the existing focus / peek / lifecycle keys. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **`fanout plan` without `origin`.** Plan runs no longer require an `origin` remote — base branch resolution falls back to the current local branch or `HEAD`, so a local-only repo can still fan out a plan. See [CLI Reference]({{< relref "/docs/cli" >}}).
- **Cleaner dry-run semantics.** Issue / Project and `fanout plan` dry-runs remain the safety previews for pane creation, but issue / Project dry-runs no longer write `/tmp` briefing files. The unused `fanout msg --dry-run` surface was removed.

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.7.0)

## v0.6.0 — 2026-06-15

- **Peer messaging for issue-less plans (`fanout plan --team`).** The plan lane now accepts `--team`, wiring the same sibling-pane coordination the issue / Project lanes use into `fanout plan`. Issue-less plan tasks have no GitHub issue number, so peers are addressed by **task id** — `fanout msg send --to <task-id>`, and `fanout msg peers` lists the live task ids. The plan bus lives at `/tmp/fanout-<repo>-plan-<slug>.db`. See [Workflow]({{< relref "/docs/workflow" >}}).

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.6.0)

## v0.5.0 — 2026-06-14

- **Per-target agent overrides.** A bare `--agent <name>` still sets the default for every child, but you can now mix agents in one run with the repeatable `--agent <NUM>=<name>` form (issue / Project children) or `--agent <task-id>=<name>` (`fanout plan`). Each target resolves a matching override first, then the global `--agent`, then `FANOUT_AGENT`; only the agents actually selected are validated. See [CLI Reference]({{< relref "/docs/cli" >}}).
- **Multi-line TUI session modal.** Pressing `n` in the persistent console now opens a modal that takes a multi-line prompt, a `claude` / `codex` choice, and an optional slug. `Shift+Enter` inserts a newline (`Ctrl+J` is a fallback for terminals that do not distinguish it) and `Enter` creates the pane. See [Monitoring]({{< relref "/docs/monitoring" >}}).

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.5.0)

## v0.4.0 — 2026-06-14

A large release that turned fanout into a standalone tool with its own console, dashboard, and planning lane.

- **Standalone runtime + persistent TUI console.** Dropped the dmux dependency for direct `tmux` control, and added the no-argument `fanout` console with pane focus / output peek, lifecycle actions, wave & blocker columns, manual agent panes, and in-memory search / filter. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Read-only web dashboard.** A `127.0.0.1`-bound, GET-only, token-gated Session view, rebuilt as a React + Vite + TypeScript SPA in the PAPER BREEZE theme, with a detail drawer, a plan-mode proposed-plan view, and synthetic rows for not-yet-fanned children. Launch it with `fanout dashboard --web`.
- **Issue-less `fanout plan`.** Fan out a local JSON plan spec instead of GitHub child issues, with task IDs, `blocked_by` dependency waves, and task lifecycle (`--status` / `--merge` / `--close` / `--cleanup`). See [CLI Reference]({{< relref "/docs/cli" >}}).
- **Peer messaging (`--team` / `fanout msg`).** Opt-in sibling coordination over a per-parent SQLite bus, plus a best-effort `nudge`. See [Workflow]({{< relref "/docs/workflow" >}}).
- **Codex Plan Mode + review skills.** `--codex-plan-mode` starts Codex children as interactive Plan-Mode TUI sessions, and the bundled `post-work-review` / `pr-watch` skills landed for both agents. See [Agent Integrations]({{< relref "/docs/agents" >}}).
- **Docs site, lighter prerequisites.** Published this Hugo site (English / 日本語), dropped the `jq` and `gh-sub-issue` requirements in favor of the official GitHub Sub-issues API, and modernized the Go toolchain on golangci-lint v2.

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.4.0)

## v0.3.0 — 2026-06-06

- **Self-update.** `fanout update` replaces the running binary plus the bundled Claude / Codex integrations through the same `install.sh` path, and `fanout --check-update` compares your binary against the latest release without touching anything. See [CLI Reference]({{< relref "/docs/cli" >}}).

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.3.0)

## v0.2.0 — 2026-06-04

- **Settings mechanism.** Opinionated child-briefing behaviors became switchable, resolved across CLI flags, `FANOUT_*` environment variables, and config files. See [Settings]({{< relref "/docs/settings" >}}).
- **Go-only.** Removed the legacy Bash implementation in favor of the Go CLI, added a Codex review gate to child briefings, and taught the fanout skill to surface OPEN issue / Project candidates.

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.2.0)

## v0.1.0 — 2026-06-04

- **First Go release.** The parallel Go port became the default install with prebuilt release distribution, alongside the `--status` JSON reporter, Projects v2 mode, a PreToolUse gate that enforces review before `gh pr create`, and a deprecation notice for the old Bash entrypoint.

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.1.0)
