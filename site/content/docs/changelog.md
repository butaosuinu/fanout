---
title: Changelog
linkTitle: Changelog
description: "What changed in each fanout release — newest first, with links into the docs."
weight: 110
kanji: 録
yomi: changelog
---

Release highlights, newest first. Every tag also has a [GitHub release](https://github.com/butaosuinu/fanout/releases) with the full commit list and prebuilt binaries (darwin / linux × amd64 / arm64). Versions come from git tags via ldflags — check yours with `fanout --check-update`.

## v0.14.0 (2026-07-21)

- **OpenCode child agents.** `--agent opencode` now works in issue / Project and `fanout plan` runs, including per-target overrides and the TUI new-session picker.
  fanout passes prompts with `--prompt`, resumes with `opencode --continue`, and gives OpenCode the base briefing plus generic validation; team messaging remains pull-only and `nudge` skips these panes.
  See [Agent Integrations]({{< relref "/docs/agents" >}}) and [CLI Reference]({{< relref "/docs/cli" >}}).
- **Safe adoption of legacy panes.** TUI restore can now migrate live pre-#503 rows without `shellKey` when the tmux server generation, pane process start time, launch markers, and cross-root claimant scan prove ownership.
  It stamps and persists a liveness key so lifecycle close works; ambiguous rows remain unchanged and fail closed.
  See [Monitoring]({{< relref "/docs/monitoring" >}}) and [CLI Reference]({{< relref "/docs/cli#--merge----close----cleanup" >}}).
- **Clearer agent and setup docs.** The docs landing page and Agent Integrations now compare Claude Code, Codex, and OpenCode in one place; README and installation guidance put prerequisites first and link detailed TUI, messaging, watcher, and Plan Mode behavior to their canonical pages.
  See [Agent Integrations]({{< relref "/docs/agents" >}}) and [Installation]({{< relref "/docs/installation" >}}).

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.14.0)

## v0.13.0 (2026-07-20)

- **Observation-only herdr backend.** fanout can now select an opt-in `herdr` runtime backend and match recorded sessions against `herdr api snapshot` in the TUI and web dashboard.
  v1 pins herdr 0.7.3 and rejects launches and other mutations; tmux remains the default.
  Status tables add `BACKEND` / `PANE` columns, while JSON adds optional `backend` / `pane_id` fields.
  See [herdr backend]({{< relref "/docs/herdr-backend" >}}) and [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Push delivery for team messages.** `fanout msg watch` streams new peer messages through Claude's Monitor tool, while fresh non-Plan Codex panes receive unread rows through an app-server bridge when idle.
  Plan Mode and restored panes remain pull-based; recover failed injections with `fanout msg inbox --all`.
  See [Workflow]({{< relref "/docs/workflow" >}}) and [CLI Reference]({{< relref "/docs/cli#fanout-msg" >}}).
- **Safer Codex pane cleanup.** Closing a Codex pane now stops its app-server and descendant Node / MCP processes, verifies pane ownership with `shellKey`, and preserves recovery state when cleanup cannot be proven safe.
  Existing live rows without `shellKey` fail closed instead of targeting a reused pane.
  See [CLI Reference]({{< relref "/docs/cli#--merge----close----cleanup" >}}).
- **Native post-work-review.** `$post-work-review` now delegates to an ordinary fresh native Codex subagent and no longer uses custom agents, a model pin, an app-server controller, or a JSON result parser.
  Install or update with integrations to remove the retired driver; a binary-only `--no-skills` update stops while that driver remains.
  See [Agent Integrations]({{< relref "/docs/agents" >}}) and [Installation]({{< relref "/docs/installation" >}}).
- **godep-cruiser architecture guard.** Layer checks now run through the pinned godep-cruiser rule set with an expiring baseline, replacing the hand-written import test while keeping the same four-layer boundary.
  See `docs/architecture.ja.md`.

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.13.0)

## v0.12.0 (2026-07-15)

- **Configurable Codex Plan Mode.** Normal issue / Project child fan-outs now resolve `codexPlanMode` through CLI, environment, repo config, or user config, and the TUI settings popup exposes the same switch.
  TUI Issue fan-outs with OPEN children honor it, while mixed non-Codex assignments fail before any pane is created.
  See [Agent Integrations]({{< relref "/docs/agents" >}}) and [Settings]({{< relref "/docs/settings" >}}).
- **Parent-issue orchestrator pane.** A TUI Issue fan-out with OPEN children now creates a single project-root orchestrator before its child panes.
  The orchestrator owns parent-scope coordination and final rollup work; repeated selections reuse it, and an all-blocked first selection creates no panes.
  See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Long prompts without truncation.** The new-session popup now preserves oversized bracketed pastes outside the textarea limits and submits their full contents.
  Large launch payloads use briefing files where needed, including Codex Plan Mode and attach paths, avoiding OS argument limits.
  See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Verified post-work-review sessions.** `$post-work-review` now records reviewer and verifier results only after checking the native child rollout's parent, role, read-only sandbox, approval policy, exact bundle path, and session UUID.
  Call reservations and fixed budgets fail closed on incomplete, duplicate, or unverifiable runs.
  See [Agent Integrations]({{< relref "/docs/agents" >}}).

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.12.0)

## v0.11.0 (2026-07-11)

- **Issue-mode plan fan-out.** The new-session popup gained the same plan fan-out control in Issue mode as in Prompt mode, decomposing a selected issue into issue-less `fanout plan` tasks with separate coordinator and task-agent choices.
  The control is disabled while the selected issue has OPEN children.
  See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Reliable Codex Plan Mode startup.** fanout now creates the Plan Mode thread through app-server, attaches the interactive Codex TUI, and records the launch only after the initial Plan turn is accepted.
  Plan generation and approval waiting no longer have a startup timeout.
  See [Agent Integrations]({{< relref "/docs/agents" >}}).
- **Codex integrations for GPT-5.6.** The five bundled Codex skills now keep their main decision flow in `SKILL.md` and load references or scripts only when needed.
  `$post-work-review` uses pinned read-only reviewer and verifier models and stops instead of substituting unavailable models, while `$pr-watch` uses a foreground watcher that suppresses unchanged snapshots.
  See [Agent Integrations]({{< relref "/docs/agents" >}}).
- **Focus after new-session launch.** Successful Prompt, plan coordinator, and Issue launches from `n` now move focus to the first pane actually created.
  Agent attach (`a`), shell (`A` / `t`), watcher, and ordinary CLI launches leave focus unchanged.
  See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Prompt Session PR status.** When a Prompt Session's recorded branch has a PR, the web dashboard now shows its link and CI status.
  See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Contributor quality gates.** The repository now uses `make check` as its canonical local gate, binds `post-work-review` results to the reviewed base and diff, and blocks branch pushes unless the exact clean tip has passed the gate.
  Repo-local Claude and Codex hooks enforce the push check, with a Codex Stop hook as a backstop; release-tag pushes remain exempt.
  See `docs/review-checklist.ja.md`.

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.11.0)

## v0.10.0 (2026-07-09)

- **Agent-state telemetry.** The `@fanout_agent_state` contract expanded to a six-value vocabulary (`running` / `working` / `plan` / `blocked` / `idle` / `done`), emitted by the launch wrapper and Codex Plan Mode and surfaced as TUI / web glyphs and badges. The console now sounds a notification when an agent presents a plan, waits for input, or exits. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **TUI popup actions.** Added a settings popup and a pane-close chooser popup, both positioned next to the active pane. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **TUI picker refinements.** Open issue links straight from the picker, and keep the compact switcher selection synced with the focused pane. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Automated PR review-risk.** New `tools/reviewrisk` derives a `review:<level>` judgment from the H/M/A canon; run it locally with `make review-risk`. See `docs/review-risk.ja.md`.
- **Briefings out of `/tmp`.** Child briefings moved from `/tmp` to `.fanout/briefings/`.

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.10.0)

## v0.9.0 (2026-07-06)

- **Persistent console overhaul.** The no-argument console gained a compact Session switcher (toggle with `v`), `1`–`9` number jumps, `Z` zoom, an AgentState column, and a tmux-popup shortcut help modal, and it restores its panes on restart. Return to it from any pane with `F11` or the `prefix T` binding. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Richer new-session modal.** Pressing `n` opens Prompt and Issue modes. Prompt mode takes a multi-line prompt with `@`-mention file completion and a plan fan-out checkbox; Issue mode adds a GraphQL-paged issue picker that marks parent / child / standalone issues and count-based `claude` / `codex` selection. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Themed, auto-laid-out panes.** Panes tile through a dmux-style auto-layout and carry fanout-colored borders labeled `#parent · name`. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **New review skills.** Added the `session-retro` skill, which mines past sessions for recurring tool errors, CI failures, and review findings, and gave `post-work-review` a project-verification pass plus a review checklist. See [Agent Integrations]({{< relref "/docs/agents" >}}).
- **4-layer internal architecture.** Reorganized `internal/` into `core` / `app` / `infra` / `ui` layers with a CI-enforced import direction. No behavior change; the canonical reference is `docs/architecture.ja.md`.

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.9.0)

## v0.8.0 (2026-06-24)

- **Label watcher.** Opt in to a TUI-resident watcher that turns trusted `fanout:auto` issues into one-shot fanout sessions. It swaps the trigger label to `fanout:running` before launch, classifies issues with OPEN children as parent fan-outs, and honors a live-pane budget. Enable it only through user config or `FANOUT_WATCHER*` environment variables; repo config can set the labels, interval, child agent, and session cap but never opt a checkout into launching. See [Workflow]({{< relref "/docs/workflow" >}}) and [Settings]({{< relref "/docs/settings" >}}).
- **Dashboard spans every worktree.** The web dashboard now aggregates Sessions across every worktree in the repo, not just the current one, so a single browser tab covers all parallel work. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Multi-agent TUI launches.** The persistent console's `n` modal now takes per-agent `claude` / `codex` launch counts behind framed text inputs, and `codex` panes start in Plan Mode. See [Monitoring]({{< relref "/docs/monitoring" >}}).

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.8.0)

## v0.7.0 (2026-06-21)

- **Lifecycle hooks.** fanout now runs user shell hooks around worktree, pane, and merge events. Configure them in `$XDG_CONFIG_HOME/fanout/hooks.json` (a Codex-style `hooks` object); they are always enabled, and a missing file or an event with no commands is a no-op. See [Lifecycle hooks]({{< relref "/docs/cli#lifecycle-hooks" >}}).
- **TUI shell terminals.** The persistent console can now open a plain shell in the selected row's worktree with `A`, or at the project root with `t`. Shell rows are recorded as manual entries for focus / peek, and close removes only the tmux pane and state row. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Compact Session navigator.** The persistent console gained a compact Session navigator for jumping between sessions, alongside the existing focus / peek / lifecycle keys. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **`fanout plan` without `origin`.** Plan runs no longer require an `origin` remote — base branch resolution falls back to the current local branch or `HEAD`, so a local-only repo can still fan out a plan. See [CLI Reference]({{< relref "/docs/cli" >}}).
- **Cleaner dry-run semantics.** Issue / Project and `fanout plan` dry-runs remain the safety previews for pane creation, but issue / Project dry-runs no longer write `/tmp` briefing files. The unused `fanout msg --dry-run` surface was removed.

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.7.0)

## v0.6.0 (2026-06-15)

- **Peer messaging for issue-less plans (`fanout plan --team`).** The plan lane now accepts `--team`, wiring the same sibling-pane coordination the issue / Project lanes use into `fanout plan`. Issue-less plan tasks have no GitHub issue number, so peers are addressed by **task id** — `fanout msg send --to <task-id>`, and `fanout msg peers` lists the live task ids. The plan bus lives at `/tmp/fanout-<repo>-plan-<slug>.db`. See [Workflow]({{< relref "/docs/workflow" >}}).

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.6.0)

## v0.5.0 (2026-06-14)

- **Per-target agent overrides.** A bare `--agent <name>` still sets the default for every child, but you can now mix agents in one run with the repeatable `--agent <NUM>=<name>` form (issue / Project children) or `--agent <task-id>=<name>` (`fanout plan`). Each target resolves a matching override first, then the global `--agent`, then `FANOUT_AGENT`; only the agents actually selected are validated. See [CLI Reference]({{< relref "/docs/cli" >}}).
- **Multi-line TUI session modal.** Pressing `n` in the persistent console now opens a modal that takes a multi-line prompt, a `claude` / `codex` choice, and an optional slug. `Ctrl+J` inserts a newline, `Shift+Enter` is available only with enhanced keyboard input, and `Enter` creates the pane. See [Monitoring]({{< relref "/docs/monitoring" >}}).

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.5.0)

## v0.4.0 (2026-06-14)

The release where fanout became a standalone CLI — no dmux dependency — with its own console, dashboard, and planning lane.

- **Standalone runtime + persistent TUI console.** Dropped the dmux dependency for direct `tmux` control, and added the no-argument `fanout` console with pane focus / output peek, lifecycle actions, wave & blocker columns, manual agent panes, and in-memory search / filter. See [Monitoring]({{< relref "/docs/monitoring" >}}).
- **Read-only web dashboard.** A `127.0.0.1`-bound, GET-only, token-gated Session view, rebuilt as a React + Vite + TypeScript SPA in the PAPER BREEZE theme, with a detail drawer, a plan-mode proposed-plan view, and synthetic rows for not-yet-fanned children. Launch it with `fanout dashboard --web`.
- **Issue-less `fanout plan`.** Fan out a local JSON plan spec instead of GitHub child issues, with task IDs, `blocked_by` dependency waves, and task lifecycle (`--status` / `--merge` / `--close` / `--cleanup`). See [CLI Reference]({{< relref "/docs/cli" >}}).
- **Peer messaging (`--team` / `fanout msg`).** Opt-in sibling coordination over a per-parent SQLite bus, plus a best-effort `nudge`. See [Workflow]({{< relref "/docs/workflow" >}}).
- **Codex Plan Mode + review skills.** `--codex-plan-mode` starts Codex children as interactive Plan-Mode TUI sessions, and the bundled `post-work-review` / `pr-watch` skills landed for both agents. See [Agent Integrations]({{< relref "/docs/agents" >}}).
- **Docs site, lighter prerequisites.** Published this Hugo site (English / 日本語), dropped the `jq` and `gh-sub-issue` requirements in favor of the official GitHub Sub-issues API, and modernized the Go toolchain on golangci-lint v2.

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.4.0)

## v0.3.0 (2026-06-06)

- **Self-update.** `fanout update` replaces the running binary plus the bundled Claude / Codex integrations through the same `install.sh` path, and `fanout --check-update` compares your binary against the latest release without touching anything. See [CLI Reference]({{< relref "/docs/cli" >}}).

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.3.0)

## v0.2.0 (2026-06-04)

- **Settings mechanism.** Opinionated child-briefing behaviors became switchable, resolved across CLI flags, `FANOUT_*` environment variables, and config files. See [Settings]({{< relref "/docs/settings" >}}).
- **Go-only.** Removed the legacy Bash implementation in favor of the Go CLI, added a Codex review gate to child briefings, and taught the fanout skill to surface OPEN issue / Project candidates.

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.2.0)

## v0.1.0 (2026-06-04)

- **First Go release.** The parallel Go port became the default install with prebuilt release distribution, alongside the `--status` JSON reporter, Projects v2 mode, a PreToolUse gate that enforces review before `gh pr create`, and a deprecation notice for the old Bash entrypoint.

[Release notes →](https://github.com/butaosuinu/fanout/releases/tag/v0.1.0)
