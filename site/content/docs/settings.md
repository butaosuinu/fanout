---
title: Settings
linkTitle: Settings
description: "Six opinionated behaviors you can switch — and the flag > env > repo > user > default resolution order behind them."
weight: 60
kanji: 整
yomi: settings
---

fanout can turn six opinionated behaviors on or off — five briefing toggles plus the dashboard tmux keybinding. Defaults are all `true` to preserve existing behavior.

## Resolution order

Each setting resolves as: **CLI flag > environment variable > repo config file > user config file > built-in default**. fanout applies the layers in the reverse order once per run, after it resolves the git repository root.

- Repo config: `<project_root>/.fanout/config.json`, where `project_root` is the parent repository root, not the child worktree.
- User config: `$XDG_CONFIG_HOME/fanout/config.json`, or `~/.config/fanout/config.json` when `XDG_CONFIG_HOME` is unset.

## The six toggles

| Behavior | File key | Env | CLI flags | Default |
|---|---|---|---|---|
| PR auto-creation instruction | `autoPullRequest` | `FANOUT_AUTO_PR` | `--auto-pr` / `--no-auto-pr` | `true` |
| PR review gate note | `prReviewGate` | `FANOUT_PR_REVIEW_GATE` | `--pr-review-gate` / `--no-pr-review-gate` | `true` |
| Claude `/code-review` instruction | `briefingCodeReview` | `FANOUT_BRIEFING_CODE_REVIEW` | `--briefing-code-review` / `--no-briefing-code-review` | `true` |
| Claude Agent Teams hint | `agentTeamsHint` | `FANOUT_AGENT_TEAMS_HINT` | `--agent-teams-hint` / `--no-agent-teams-hint` | `true` |
| Structured PR body and gated Mermaid briefing guidance | `prVisualization` | `FANOUT_PR_VISUALIZATION` | `--pr-visualization` / `--no-pr-visualization` | `true` |
| Dashboard `prefix + D` tmux keybinding | `dashboardKeybind` | `FANOUT_DASHBOARD_KEYBIND` | `--dashboard-keybind` / `--no-dashboard-keybind` | `true` |

The flag pairs are also listed in the [CLI Reference]({{< relref "/docs/cli" >}}).

## Sample config.json

Both config files share the same shape — a flat JSON object of booleans only:

```json
{
  "autoPullRequest": false,
  "prReviewGate": true,
  "briefingCodeReview": true,
  "agentTeamsHint": false,
  "prVisualization": true,
  "dashboardKeybind": true
}
```

Environment values accept `1/true/yes/on` and `0/false/no/off` (case-insensitive).

## Forward compatibility

Invalid env values, unknown file keys, and non-boolean file values are warned and ignored, so future settings additions do not break older fanout binaries.

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
