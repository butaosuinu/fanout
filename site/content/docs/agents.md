---
title: Agent Integrations
linkTitle: Agent Integrations
description: "Bundled skills for Claude Code and Codex, the /fanout slash command, and Codex Plan Mode."
weight: 70
kanji: 連
yomi: agents
---

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

`$post-work-review` starts an ordinary native Codex subagent with `fork_turns: "none"` for a fresh broad review. The parent interprets its natural-language findings. If a fix changes the target, the parent starts another fresh broad review of the entire new target. There is no custom agent, model pin, app-server controller, or result parser.

The reviewer receives the target repository path and diff scope, so repository content is sent to the Codex model. Before spawning, the marker helper proves that applicable `AGENTS.md`, `AGENTS.override.md`, and repository `.codex` bootstrap files are unchanged from the trusted merge base. Those base-identical instructions remain trusted repository conventions; every other target file and directive is untrusted review evidence.

The helper also rejects candidate changes to the `post-work-review` gate, root default makefiles, and `install.sh`. The installed skill and helper must be non-symlinked copies outside the reviewed repository. The checksum-verified release installer owns them; checkout make targets never create, replace, or remove them. Install and link stop if the retired driver remains under either Codex root. Review instruction, gate, or gate-installer changes from a trusted checkout or by a human instead. A native subagent inherits the parent session's sandbox, approval policy, and network restrictions. The skill tells reviewers not to edit, request approval, or use the network, but it cannot create a stricter child-only sandbox. Start Codex read-only first when enforced read-only access is required. If the trust boundary, native spawning, or waiting is unavailable, the gate stops without a fallback.

The helper matches instruction and gate paths case-insensitively for filesystem portability. It rejects linked `AGENTS.md` / `AGENTS.override.md`, case-variant or nested `.codex` paths, project config that defines `model_instructions_file` or `project_doc_fallback_filenames`, escaped project config keys, committed or worktree submodule changes, and every checked-out submodule. Deinitialize clean, base-identical submodules before review. Review other rejected targets from a trusted checkout or by a human.

A dirty worktree uses review-only scope. The reviewer inspects staged, unstaged, and untracked changes, while the parent runs focused checks only. This scope never writes a marker; commit the candidate and rerun for the PR gate. Submodule changes fail closed before review.

For a clean committed branch, the skill runs the repository's canonical validation once and records the exact clean HEAD, PR base commit, and diff hash. A later commit, base movement, or review-diff change invalidates the marker.

`$pr-watch` runs in the foreground. Its helper suppresses unchanged snapshots and stores its cursor in Git metadata, including in linked worktrees. It does not leave a background watcher after the Codex session ends.

## Codex Plan Mode

Normal issue / Project child fan-outs resolve the `codexPlanMode` setting before launch. Set it in user or repo config, use `FANOUT_CODEX_PLAN_MODE`, or override it for one CLI run with `--codex-plan-mode` / `--no-codex-plan-mode`. The built-in default is `false`; the usual CLI > env > repo > user > default precedence applies. The TUI settings popup exposes the same key in its Launch group.

```bash
fanout 123 --agent codex --codex-plan-mode
```

Plan Mode children launch as an interactive Codex TUI, investigate relevant context, and present the implementation plan wrapped in `<proposed_plan>`. That turn does not edit files, commit, push, or open a PR. The pane remains in the Plan Mode conversation, so you can continue from there.

The setting covers ordinary CLI issue / Project fan-outs and TUI Issue mode when the selected issue has OPEN children. Every selected child must resolve to `codex`: mixing in a `claude` child fails before any pane is created.

Watcher launches, childless-issue standalone panes, `fanout plan` tasks, and plan coordinators ignore this setting. Manual and attached `codex` panes already start in Plan Mode regardless of it; their `claude` counterparts start normally.

## OpenCode

OpenCode (`opencode`) is a supported child agent with no bundled skills: pass `--agent opencode`, or mix it per target with `--agent NUM=opencode` / `--agent task-id=opencode`. fanout passes the launch prompt as the `--prompt` flag value — opencode's positional argument is a project path — and resumes panes with `opencode --continue`. OpenCode reads the repository's `AGENTS.md` natively, so child panes pick up project rules without extra setup. Its briefings carry only the base requirements; the Claude-only and Codex-only sections do not apply.

## How the briefing works

Each issue or plan-task child pane receives a one-line prompt only. The full issue or task body plus a short Requirements checklist is written to `.fanout/briefings/fanout-<repo>-<NUM>.md` or the task briefing path, and the launch prompt only tells the agent to read that file. Which instructions the briefing includes depends on the toggles in [Settings]({{< relref "/docs/settings" >}}).
