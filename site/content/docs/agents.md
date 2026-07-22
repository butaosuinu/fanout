---
title: Agent Integrations
linkTitle: Agent Integrations
description: "The supported agents compared, bundled skills for Claude Code and Codex, the /fanout slash command, and OpenCode."
weight: 80
kanji: 連
yomi: agents
---

## Supported agents

fanout can start a child pane with any of three agent CLIs. The fan-out mechanics are shared — one worktree, one pane, one briefing per child — and the differences sit around the session:

| | Claude Code | Codex CLI | OpenCode |
|---|---|---|---|
| `--agent` name | `claude` | `codex` | `opencode` |
| Bundled skills | `/fanout` + skills | `$fanout` + skills | none |
| `--team` push delivery | ✓ `fanout msg watch` under the Monitor tool | ✓ fresh non-Plan sessions (app-server bridge) | — (pull only) |
| `fanout msg nudge` | ✓ | ✓ | — (skipped) |
| Briefing sections | base + Claude-specific | base + Codex-specific | base + generic validation |

The push and nudge rows are explained in the [CLI Reference]({{< relref "/docs/cli#fanout-msg" >}}).

## From inside an agent session

Say you are running Claude Code or Codex in one pane and want to fan out to children without leaving it. fanout is safe to call from an agent session (Claude Code, Codex, etc.) that is itself running inside tmux. It only creates new panes for children; the caller's pane is never touched.

To tell fanout which agent CLI the child panes should launch, pass `--agent` or set `FANOUT_AGENT`. To mix agents in one run, add repeatable per-target overrides: `--agent NUM=name` for issue / Project children, or `--agent task-id=name` with `fanout plan`. See the [CLI Reference]({{< relref "/docs/cli" >}}) for how the overrides resolve.

## Claude Code

### The `/fanout` slash command

Say you want to call fanout from a conversation but confirm the targets before the real run. `~/.claude/commands/fanout.md` installs the slash command:

```text
/fanout [parent-issue] [--go] [extra fanout flags]
```

It runs `fanout <N> --dry-run` first, shows the target list, and only fires the real command after you confirm — or immediately when `--go` was passed.

### The `fanout` skill

Say the parent issue body scatters child references around and collecting the numbers by hand is tedious. `~/.claude/skills/fanout/` reads those references, presents the candidates for approval, forwards the accepted numbers via `--include`, and suggests running `/fanout`.

### The `fanout-issues` skill

Say you want to shape a plan into the issue tree fanout can run against. `~/.claude/skills/fanout-issues/` creates same-repo children, links them through GitHub Sub-issues, mirrors them in the parent task list, and records blocker waves for `fanout --unblocked-only`.

### The `fanout-plan` skill

Say you want to fan out a local implementation plan without creating GitHub child issues. `~/.claude/skills/fanout-plan/` turns the plan into a `fanout plan` JSON spec, confirms it with a dry run, and launches issue-less task panes.

### Review and PR follow-up skills

Say the implementation is done and you want one more look before committing or opening a PR. `~/.claude/skills/post-work-review/` backs the local PR review gate and runs a final review loop. For follow-up after a PR exists, use `~/.claude/commands/pr-watch.md` and `~/.claude/skills/pr-watch/`, which watch for conflicts, CI, and review comments.

## Codex CLI

Codex installs five repo-managed skills under `~/.codex/skills/` (see [Installation]({{< relref "/docs/installation" >}})). Each skill keeps its main decision flow in `SKILL.md` and loads bundled references or scripts only when needed.

Invoke the fanout skill by asking Codex to fan out a parent issue (for example, "fan out #123") or explicitly with `$fanout`. It follows the same safety flow as Claude's `/fanout` — dry-run, confirm targets, then run. `fanout-issues`, `fanout-plan`, `post-work-review`, and `pr-watch` are also bundled as Codex versions; invoke them as `$fanout-issues` or `$pr-watch`, and they play the same role as the Claude versions.

### The `$post-work-review` gate

`$post-work-review` starts a fresh native Codex subagent for one broad review of the whole target; the parent interprets its findings, and a fix that changes the target triggers another fresh review. The reviewer receives the repository path and diff scope, so repository content is sent to the Codex model.

The gate protects its own trust boundary. Before spawning, a marker helper proves that the applicable `AGENTS.md` / `AGENTS.override.md` and repository `.codex` bootstrap files are unchanged from the trusted merge base, and it rejects candidate changes to the gate itself (the `post-work-review` files, root default makefiles, `install.sh`). The checksum-verified release installer owns the installed skill and helper; checkout make targets never touch them. The helper fails closed on anything it cannot trace — linked or case-variant instruction files, project config that relocates instructions, and submodule changes (deinitialize clean submodules first). Review rejected targets from a trusted checkout or by a human.

The subagent inherits the parent session's sandbox and approval policy; when enforced read-only access is required, start Codex itself read-only. A dirty worktree gets a review-only pass that never writes the PR-gate marker — commit the candidate and rerun. On a clean committed branch, the skill runs the repository's canonical validation once and records the exact HEAD, PR base, and diff hash; a later commit, base movement, or diff change invalidates the marker.

`$pr-watch` runs in the foreground, suppresses unchanged snapshots, and stores its cursor in Git metadata, including in linked worktrees. No background watcher outlives the Codex session.

## OpenCode

OpenCode (`opencode`) is a supported child agent with no bundled skills: pass `--agent opencode`, or mix it per target with `--agent NUM=opencode` / `--agent task-id=opencode`.

### Launch and resume

fanout passes the launch prompt as the `--prompt` flag value — opencode's positional argument is a project path — and resumes panes with `opencode --continue`.

### Project rules and briefings

OpenCode reads the repository's `AGENTS.md` natively, so child panes pick up project rules without extra setup. Its briefings carry the base requirements plus the generic final-validation instructions; the Claude-only and Codex-only sections do not apply. The `/fanout` command that fanout's Claude integrations install is Claude Code-only — OpenCode's Claude Code compatibility reads `CLAUDE.md` and `~/.claude/skills/`, not `~/.claude/commands` — so pick `claude` or `codex` as a TUI plan fan-out coordinator.

### Messaging stays pull-based

`fanout msg nudge` skips opencode panes, and there is no push lane: with `--team`, opencode siblings read the bus at their own checkpoints (`inbox` / `board`). The nudge exclusion is deliberate — opencode panes report no pane state beyond `running`, so fanout cannot tell when queued input is safe to send.

## How the briefing works

Each issue or plan-task child pane receives a one-line prompt only. The full issue or task body plus a short Requirements checklist is written to `.fanout/briefings/fanout-<repo>-<NUM>.md` or the task briefing path, and the launch prompt only tells the agent to read that file. Which instructions the briefing includes depends on the toggles in [Settings]({{< relref "/docs/settings" >}}).
