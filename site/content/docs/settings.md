---
title: Settings
linkTitle: Settings
description: "Opinionated behavior toggles, watcher controls, TUI notification channels, and the flag > env > repo > user > default resolution order behind them."
weight: 60
kanji: 整
yomi: settings
---

A few of fanout's behaviors are ones a team will want to change: whether children open PRs automatically, whether to prompt Claude for a review, whether to run the watcher, and where to send state transitions. These live as briefing toggles, the dashboard tmux keybinding, watcher controls, and TUI notification channels — all resolved from the same settings stack. The briefing and dashboard booleans default to `true`, the watcher defaults to off, and notifications default to `bell`.

## Resolution order

When the same setting is given on both the CLI and a config file, which one wins? The order is **CLI flag > environment variable > repo config file > user config file > built-in default**. fanout applies the layers from lowest to highest once per run, after it resolves the git repository root.

- Repo config: `<project_root>/.fanout/config.json`, where `project_root` is the parent repository root, not the child worktree.
- User config: `$XDG_CONFIG_HOME/fanout/config.json`, or `~/.config/fanout/config.json` when `XDG_CONFIG_HOME` is unset.

## Edit settings from the TUI

Press `s` in the persistent TUI console to edit either config file in a popup. The Target row switches between user config and repo config. Each key can be set to a value or returned to `inherit`, which removes that key from the selected JSON file.

The popup edits only config files. CLI flags and `FANOUT_*` environment variables still override what you save. Repo config keeps the same safety restrictions described below: it cannot enable the watcher, cannot set HTTP notification URLs, and can only choose `bell`, `tmux`, or `none` for `notifications`.

## What each toggle is for

The behavior toggles are instructions that ship on by default but a team may want off. Here is the purpose of each key, one line apiece.

- `autoPullRequest`: tells children to open a PR once their work is done. Turn it off if your team opens PRs by hand.
- `prReviewGate`: on by default, it keeps the PR-review-gate expectation. Turn it off and the Claude child briefing instead gets a note permitting `FANOUT_SKIP_PR_REVIEW=1 gh pr create` when the creation hook blocks (see below).
- `briefingCodeReview`: tells Claude children to run the `/code-review` slash command on their changes before committing.
- `agentTeamsHint`: tells Claude children that Claude Code Agent Teams is available. It has no effect on non-Claude children.
- `prVisualization`: asks children to structure the PR body they open and, conditionally, include a Mermaid diagram (see below).
- `dashboardKeybind`: registers the `F12` / `prefix + D` dashboard keys and `prefix + M` same-worktree action key in tmux.
- `consoleKeybind`: registers the `F11` / `prefix + T` console-return keys in tmux when the TUI console starts.

The watcher and notification channels are a separate track. The watcher gates opt-in label-driven launches:

- `watcher`: the opt-in switch. Only user config or `FANOUT_WATCHER` can turn it on — repo config cannot.
- `watcherTriggerLabel`: the label that means "launch this issue" (default `fanout:auto`).
- `watcherRunningLabel`: the label the watcher swaps to once it launches an issue (default `fanout:running`). Change it if it collides with your own label scheme.
- `watcherIntervalSeconds`: how often the watcher polls for the trigger label (default `60`, clamped to a 20-second minimum).
- `watcherAgent`: the agent the watcher launches children with. Falls back to the TUI's default agent when unset.
- `watcherMaxSessions`: the cap on live fanout panes at once (default `4`, `0` for unlimited).

Notification channels pick where TUI state transitions go.

## The toggles and notification channels

| Behavior | File key | Env | CLI flags | Default |
|---|---|---|---|---|
| PR auto-creation instruction | `autoPullRequest` | `FANOUT_AUTO_PR` | `--auto-pr` / `--no-auto-pr` | `true` |
| PR review gate note | `prReviewGate` | `FANOUT_PR_REVIEW_GATE` | `--pr-review-gate` / `--no-pr-review-gate` | `true` |
| Claude `/code-review` instruction | `briefingCodeReview` | `FANOUT_BRIEFING_CODE_REVIEW` | `--briefing-code-review` / `--no-briefing-code-review` | `true` |
| Claude Agent Teams hint | `agentTeamsHint` | `FANOUT_AGENT_TEAMS_HINT` | `--agent-teams-hint` / `--no-agent-teams-hint` | `true` |
| Structured PR body and gated Mermaid briefing guidance | `prVisualization` | `FANOUT_PR_VISUALIZATION` | `--pr-visualization` / `--no-pr-visualization` | `true` |
| Dashboard/action tmux keybindings | `dashboardKeybind` | `FANOUT_DASHBOARD_KEYBIND` | `--dashboard-keybind` / `--no-dashboard-keybind` | `true` |
| Console-return tmux keybindings | `consoleKeybind` | `FANOUT_CONSOLE_KEYBIND` | n/a | `true` |
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
  "consoleKeybind": true,
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

When you step away from the terminal and still want to know when a child changes state, pick where the notice goes. The `notifications` setting covers both issue / PR transitions (merged, CI failed, waiting on blockers) and agent-state transitions from the TUI (plan ready, waiting for input, agent exited). It is a comma- or space-separated selector. Supported values are `bell`, `tmux`, `ntfy`, `slack`, and `none`. `ntfy` requires `ntfyURL`; `slack` requires `slackWebhookURL`.

Both HTTP channels only send outbound POST requests and never open inbound sockets. To stop a repo's config from sending anywhere on its own, repo config may only select `bell`, `tmux`, or `none`; `ntfy`, `slack`, `ntfyURL`, and `slackWebhookURL` are honored only from user config or environment variables.

Agent-state notifications come from `@fanout_agent_state`, not from pane output. fanout sends them only for `plan`, `blocked`, and `done`; `running`, `working`, and `idle` are visible in the TUI but do not send notifications.

## Watcher safety

The watcher should never start just because someone checked out the repo, so repo config cannot opt into it. If `<project_root>/.fanout/config.json` sets `watcher`, fanout warns and ignores that key; enable it from user config or `FANOUT_WATCHER` instead. Repo config may still set `watcherTriggerLabel`, `watcherRunningLabel`, `watcherIntervalSeconds`, `watcherAgent`, and `watcherMaxSessions`.

The trigger label starts agent work from the labeled issue and, for parent fan-outs, any OPEN children it launches. Their bodies become agent briefings, so treat the label as an execution request and apply it only when you trust that issue and its launchable children. See [Watcher]({{< relref "/docs/watcher" >}}) for the operational details.

## Watcher operation

The watcher only runs while a TUI console is running. See [Watcher]({{< relref "/docs/watcher" >}}) for the full operation guide — enabling it, the label lifecycle, the session budget, and cleanup.

```bash
# One shell
export FANOUT_WATCHER=1
export FANOUT_WATCHER_AGENT=codex
fanout
```

## Forward compatibility

Invalid boolean or integer env values, unknown file keys, and file values with the wrong JSON type are warned and ignored, so future settings additions do not break older fanout binaries.

Lifecycle hooks are always enabled and configured separately in `hooks.json`; see [Lifecycle hooks]({{< relref "/docs/cli#lifecycle-hooks" >}}) in the CLI Reference.

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
