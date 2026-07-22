---
title: Documentation
description: "Everything fanout does, from the first install to folding the last pane away."
---

**fanout** is a CLI for developers who want to push several features forward at once. It fans a GitHub parent issue's OPEN children out into one tmux pane per child, gives each pane its own git worktree, and launches an agent CLI with a per-issue briefing. Because every child works in its own worktree and branch, they never collide when editing in parallel — and while one child waits on a blocker, you keep moving in another pane.

Three agent CLIs can drive a child pane:

| Agent | `--agent` | In one line |
|---|---|---|
| Claude Code | `claude` | Bundled skills and the `/fanout` command; receives `--team` messages as they arrive |
| Codex CLI | `codex` | Bundled skills; children can start in Codex Plan Mode |
| OpenCode | `opencode` | No bundled skills — reads `AGENTS.md` natively |

The capability matrix in [Agent Integrations]({{< relref "/docs/agent-integrations" >}}) shows the full differences.

[Installation]({{< relref "/docs/installation" >}}) gets the binary and the skills in with one curl line. [Quickstart]({{< relref "/docs/quickstart" >}}) takes you from a parent issue to parallel panes in a few minutes. [Workflow]({{< relref "/docs/workflow" >}}) walks the loop: grow an issue tree, fan it out, select children, then merge and fold panes away before the next wave. [Monitoring]({{< relref "/docs/monitoring" >}}) watches a fan-out through three windows — the persistent TUI, `--status`, and the web dashboard. [Watcher]({{< relref "/docs/watcher" >}}) hands over the launching itself: while the TUI console is open, a `fanout:auto` label starts a session without you.

[CLI Reference]({{< relref "/docs/cli" >}}) collects every command form, flag, environment variable and exit code on one page. [Settings]({{< relref "/docs/settings" >}}) controls the generated briefing switches and their resolution order. [Agent Integrations]({{< relref "/docs/agent-integrations" >}}) covers the bundled skills, the `/fanout` slash command, and Codex Plan Mode. [herdr backend]({{< relref "/docs/herdr-backend" >}}) covers the opt-in, observation-only runtime backend and how it differs from tmux. When something breaks, [Troubleshooting]({{< relref "/docs/troubleshooting" >}}) has the cause and the fix, and [Changelog]({{< relref "/docs/changelog" >}}) tracks what changed in each release.
