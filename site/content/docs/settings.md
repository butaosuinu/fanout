---
title: Settings
linkTitle: Settings
description: "Opinionated behavior toggles, watcher controls, TUI notification channels, and the flag > env > repo > user > default resolution order behind them."
weight: 60
kanji: 整
yomi: settings
---

fanout resolves briefing toggles, the dashboard tmux keybinding, watcher controls, and TUI notification channels from the same settings stack. The briefing/dashboard booleans default to `true`, the watcher defaults to off, and notifications default to `bell`.

## Resolution order

Each setting resolves as: **CLI flag > environment variable > repo config file > user config file > built-in default**. fanout applies the layers in the reverse order once per run, after it resolves the git repository root.

- Repo config: `<project_root>/.fanout/config.json`, where `project_root` is the parent repository root, not the child worktree.
- User config: `$XDG_CONFIG_HOME/fanout/config.json`, or `~/.config/fanout/config.json` when `XDG_CONFIG_HOME` is unset.

## The toggles and notification channels

| Behavior | File key | Env | CLI flags | Default |
|---|---|---|---|---|
| PR auto-creation instruction | `autoPullRequest` | `FANOUT_AUTO_PR` | `--auto-pr` / `--no-auto-pr` | `true` |
| PR review gate note | `prReviewGate` | `FANOUT_PR_REVIEW_GATE` | `--pr-review-gate` / `--no-pr-review-gate` | `true` |
| Claude `/code-review` instruction | `briefingCodeReview` | `FANOUT_BRIEFING_CODE_REVIEW` | `--briefing-code-review` / `--no-briefing-code-review` | `true` |
| Claude Agent Teams hint | `agentTeamsHint` | `FANOUT_AGENT_TEAMS_HINT` | `--agent-teams-hint` / `--no-agent-teams-hint` | `true` |
| Structured PR body and gated Mermaid briefing guidance | `prVisualization` | `FANOUT_PR_VISUALIZATION` | `--pr-visualization` / `--no-pr-visualization` | `true` |
| Dashboard `prefix + D` tmux keybinding | `dashboardKeybind` | `FANOUT_DASHBOARD_KEYBIND` | `--dashboard-keybind` / `--no-dashboard-keybind` | `true` |
| Watcher opt-in | `watcher` | `FANOUT_WATCHER` | n/a | `false` |
| Watcher trigger label | `watcherTriggerLabel` | `FANOUT_WATCHER_TRIGGER_LABEL` | n/a | `fanout:auto` |
| Watcher running label | `watcherRunningLabel` | `FANOUT_WATCHER_RUNNING_LABEL` | n/a | `fanout:running` |
| Watcher interval seconds | `watcherIntervalSeconds` | `FANOUT_WATCHER_INTERVAL_SECONDS` | n/a | `60` |
| Watcher child agent | `watcherAgent` | `FANOUT_WATCHER_AGENT` | n/a | unset |
| Watcher max sessions | `watcherMaxSessions` | `FANOUT_WATCHER_MAX_SESSIONS` | n/a | `4` |
| TUI transition notifications | `notifications` | `FANOUT_NOTIFICATIONS` | n/a | `bell` |
| ntfy POST URL | `ntfyURL` | `FANOUT_NTFY_URL` | n/a | unset |
| Slack webhook POST URL | `slackWebhookURL` | `FANOUT_SLACK_WEBHOOK_URL` | n/a | unset |

These flag pairs are also listed in the [CLI Reference]({{< relref "/docs/cli" >}}) (which also covers `--codex-plan-mode`, a launch flag rather than a resolved setting); the watcher and notification settings have no CLI flag.

## Sample config.json

Both config files share the same shape: a flat JSON object of booleans, strings, and integer keys:

```json
{
  "autoPullRequest": false,
  "prReviewGate": true,
  "briefingCodeReview": true,
  "agentTeamsHint": false,
  "prVisualization": true,
  "dashboardKeybind": true,
  "watcher": false,
  "watcherTriggerLabel": "fanout:auto",
  "watcherRunningLabel": "fanout:running",
  "watcherIntervalSeconds": 60,
  "watcherAgent": "codex",
  "watcherMaxSessions": 4,
  "notifications": "bell",
  "ntfyURL": "https://ntfy.sh/my-topic",
  "slackWebhookURL": "https://hooks.slack.com/services/..."
}
```

Boolean environment values accept `1/true/yes/on` and `0/false/no/off` (case-insensitive).
Integer environment values accept base-10 integers. `watcherIntervalSeconds` resolves to at least `20`; `watcherMaxSessions=0` means unlimited.

## Notification channels

`notifications` is a comma- or space-separated selector. Supported values are `bell`, `tmux`, `ntfy`, `slack`, and `none`. `ntfy` requires `ntfyURL`; `slack` requires `slackWebhookURL`. Both HTTP channels only send outbound POST requests and never open inbound sockets. To avoid repository-controlled exfiltration, repo config may only select `bell`, `tmux`, or `none`; `ntfy`, `slack`, `ntfyURL`, and `slackWebhookURL` are honored only from user config or environment variables.

## Watcher safety

Repo config cannot opt into the watcher. If `<project_root>/.fanout/config.json` sets `watcher`, fanout warns and ignores that key; use user config or `FANOUT_WATCHER` instead. Repo config may still set `watcherTriggerLabel`, `watcherRunningLabel`, `watcherIntervalSeconds`, `watcherAgent`, and `watcherMaxSessions`.

The trigger label starts agent work from the labeled issue and, for parent
fan-outs, any OPEN children it launches. Their bodies become agent briefings, so
treat the label as an execution request and apply it only when you trust that
issue and its launchable children.

## Forward compatibility

Invalid boolean or integer env values, unknown file keys, and file values with the wrong JSON type are warned and ignored, so future settings additions do not break older fanout binaries.

Lifecycle hooks are always enabled and configured separately in `hooks.json`; see [CLI Reference]({{< relref "/docs/cli" >}}).

## prVisualization in detail

`prVisualization=false` omits the structured PR-body and gated Mermaid guidance from child briefings. The guidance is only injected when `autoPullRequest` is also `true`, because it applies to the PR body the child will open. Turn it off for one run with `--no-pr-visualization`, or per shell with `FANOUT_PR_VISUALIZATION=0`.

## What prReviewGate actually means

`prReviewGate=false` does not forcibly disable child Claude Code hooks. It only adds a note to the child briefing allowing `FANOUT_SKIP_PR_REVIEW=1 gh pr create ...` if the `PreToolUse` hook blocks PR creation before `/post-work-review`. How the hook itself works is covered in [Troubleshooting]({{< relref "/docs/troubleshooting" >}}).

## Use-case examples

Stop PR auto-creation for the whole repository by committing a repo config:

```bash
# <project_root>/.fanout/config.json — applies to every run in this repo
mkdir -p .fanout
cat > .fanout/config.json <<'EOF'
{
  "autoPullRequest": false
}
EOF
```

Drop the same instruction for a single run instead:

```bash
# Remove the automatic PR-opening requirement from child briefings for one run
fanout 123 --no-auto-pr
```

Or switch a toggle off for the current shell:

```bash
# Disable the Agent Teams hint globally for this shell
export FANOUT_AGENT_TEAMS_HINT=0
```
