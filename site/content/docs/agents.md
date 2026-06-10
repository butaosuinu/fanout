---
title: Agent Integrations
linkTitle: Agent Integrations
description: "The /fanout slash command, the fanout and fanout-issues skills for Claude Code and Codex, and Codex Plan Mode."
weight: 70
kanji: 連
yomi: agents
---

## From inside an agent session

fanout is safe to call from an agent session (Claude Code, Codex, etc.) that is itself running inside tmux. It only creates NEW panes for children; the caller's pane is never touched. Pass `--agent` or set `FANOUT_AGENT` so child panes know which agent CLI to launch.

The CLI prerequisites still apply: run from inside tmux, and run from the repository whose children should branch from the selected base. The integration files below are bundled in the repo and placed by the [install script]({{< relref "/docs/installation" >}}).

## Claude Code

### The `/fanout` slash command

`~/.claude/commands/fanout.md` installs the slash command:

```text
/fanout [parent-issue] [--go] [extra fanout flags]
```

It runs `fanout <N> --dry-run` first, shows the target list, and only fires the real command after you confirm — or immediately when `--go` was passed.

### The `fanout` skill

`~/.claude/skills/fanout/` lets the agent recognize when fanout is applicable and suggest `/fanout` rather than invoking it unprompted. Beyond gating invocation, the skill reads the parent body for **implicit** child references that the CLI itself does not parse — close keywords like `Closes #N`, dependency wording like `Depends on #N`, plain bullets, and Japanese idioms like `#N に関連` — lists the candidates back to you for approval, and forwards the accepted numbers via `--include`.

The skill also generates the `--name` flags (slug / display name / branch) from the issue title and body. The CLI itself never calls an LLM by design; the naming intelligence lives in the skills.

### The `fanout-issues` skill

`~/.claude/skills/fanout-issues/` guides the agent when turning a plan into a fanout-ready GitHub parent issue plus linked child issues. It creates same-repo children, links them through GitHub Sub-issues, mirrors them in the parent task list, and records blocker waves in the `## Blocked by` / `(blocked by #N)` shapes that `fanout --unblocked-only` understands.

## Codex CLI

The Codex skills are installed to `~/.codex/skills/fanout/` and `~/.codex/skills/fanout-issues/`. Restart any running Codex session after installing or updating the skills so it picks up the new files.

Invoke the fanout skill by asking Codex to fan out a parent issue (for example, "fan out #123") or explicitly with `$fanout`. It follows the same safety flow as the Claude command — dry-run first, confirm targets, then run the real command — and it also performs the implicit-child scan and `--name` generation.

The `fanout-issues` skill mirrors the Claude version: ask Codex to create a fanout-ready GitHub issue tree, decompose a plan into parent/child issues, or prepare blocker waves for `fanout --unblocked-only`, and it produces the same same-repo children, GitHub Sub-issues links, parent task-list rows, and `## Blocked by` annotations.

## Codex Plan Mode

`--codex-plan-mode` is an opt-in launch mode for `--agent codex`:

```bash
fanout 123 --agent codex --codex-plan-mode
```

Instead of running positional `codex "<prompt>"`, fanout starts a Codex app-server for each child, creates a `plan` collaboration-mode thread, starts the initial turn with the fanout prompt through that app-server, and attaches an interactive Codex TUI to the remote session.

The child briefing is also rewritten for Plan Mode: it asks for an implementation plan wrapped in `<proposed_plan>` and explicitly forbids file edits, commits, pushes, and PR creation in that first turn.

This path never sends `/plan` or prompt text through tmux. The pane remains an interactive Codex TUI session, so you can continue from the Plan Mode conversation. If the app-server Plan turn setup or the TUI attach fails, the launch fails before state is recorded and fanout cleans up the pane/worktree so the child can be retried.

## How the briefing works

Each child pane receives a one-line prompt only. The full issue body plus a short Requirements checklist is written to `/tmp/fanout-<repo>-<NUM>.md`, and the launch prompt stays short and points the agent at that briefing file.

The briefing content is filtered through the resolved settings — `autoPullRequest`, `prReviewGate`, `briefingCodeReview`, `agentTeamsHint`, and `prVisualization`. See [Settings]({{< relref "/docs/settings" >}}) for how CLI flags, environment variables, and config files resolve into those switches.
