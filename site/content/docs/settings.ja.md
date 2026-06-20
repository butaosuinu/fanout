---
title: 設定
linkTitle: 設定
description: "オン/オフできる opinionated な挙動トグルと TUI 通知 channel、そしてその背後にある flag > env > repo > user > default の解決順序。"
weight: 60
kanji: 整
yomi: settings
---

fanout は opinionated な 6 つの挙動(briefing の 5 トグル、ダッシュボードの tmux キーバインド)をオン/オフでき、TUI 通知 channel も選択できます。bool 既定値は `true` です。通知の既定値は `bell` です。

## 解決順序

各設定の優先順位は **CLI flag > 環境変数 > リポジトリ設定ファイル > ユーザー設定ファイル > ビルトイン既定値** です。fanout は git リポジトリルートを解決した後、run ごとに 1 回だけ逆順に重ねて解決します。

- リポジトリ設定: `<project_root>/.fanout/config.json`。この `project_root` は親リポジトリルートで、子 worktree ではありません。
- ユーザー設定: `$XDG_CONFIG_HOME/fanout/config.json`、`XDG_CONFIG_HOME` が無い場合は `~/.config/fanout/config.json`。

## トグルと通知 channel

| 挙動 | ファイルキー | env | CLI flag | 既定値 |
|---|---|---|---|---|
| PR 自動作成指示 | `autoPullRequest` | `FANOUT_AUTO_PR` | `--auto-pr` / `--no-auto-pr` | `true` |
| PR レビューゲート通知 | `prReviewGate` | `FANOUT_PR_REVIEW_GATE` | `--pr-review-gate` / `--no-pr-review-gate` | `true` |
| Claude `/code-review` 指示 | `briefingCodeReview` | `FANOUT_BRIEFING_CODE_REVIEW` | `--briefing-code-review` / `--no-briefing-code-review` | `true` |
| Claude Agent Teams ヒント | `agentTeamsHint` | `FANOUT_AGENT_TEAMS_HINT` | `--agent-teams-hint` / `--no-agent-teams-hint` | `true` |
| 構造化 PR 本文とゲート付き Mermaid の briefing 指示 | `prVisualization` | `FANOUT_PR_VISUALIZATION` | `--pr-visualization` / `--no-pr-visualization` | `true` |
| ダッシュボード `prefix + D` tmux キーバインド | `dashboardKeybind` | `FANOUT_DASHBOARD_KEYBIND` | `--dashboard-keybind` / `--no-dashboard-keybind` | `true` |
| TUI 状態遷移通知 | `notifications` | `FANOUT_NOTIFICATIONS` | n/a | `bell` |
| ntfy POST URL | `ntfyURL` | `FANOUT_NTFY_URL` | n/a | 未設定 |
| Slack webhook POST URL | `slackWebhookURL` | `FANOUT_SLACK_WEBHOOK_URL` | n/a | 未設定 |

これらの flag ペアは [CLI リファレンス]({{< relref "/docs/cli" >}})にも載っています（CLI リファレンスには、設定ではなく起動フラグである `--codex-plan-mode` も含まれます）。通知設定に CLI flag はありません。

## config.json サンプル

リポジトリ設定とユーザー設定はどちらも同じ形で、bool のトグルに加えて 3 つの string な通知キーを持つフラットな JSON オブジェクトです:

```json
{
  "autoPullRequest": false,
  "prReviewGate": true,
  "briefingCodeReview": true,
  "agentTeamsHint": false,
  "prVisualization": true,
  "dashboardKeybind": true,
  "notifications": "bell",
  "ntfyURL": "https://ntfy.sh/my-topic",
  "slackWebhookURL": "https://hooks.slack.com/services/..."
}
```

bool の環境変数は `1/true/yes/on` と `0/false/no/off` を受け付けます(大小文字は無視)。

## 通知 channel

`notifications` は comma または空白区切りの selector です。指定できる値は `bell`、`tmux`、`ntfy`、`slack`、`none` です。`ntfy` は `ntfyURL`、`slack` は `slackWebhookURL` が必要です。どちらの HTTP channel も outbound POST のみで、inbound socket は開きません。repository-controlled な外部送信を避けるため、repo config で選択できるのは `bell`、`tmux`、`none` だけです。`ntfy`、`slack`、`ntfyURL`、`slackWebhookURL` は user config または環境変数からだけ有効になります。

## 前方互換

不正な bool env 値、設定ファイル内の未知キー、JSON type が合わない値は warn して無視します。将来の設定追加で古い fanout バイナリが壊れないようにするためです。

Lifecycle hook は常に有効で、別の `hooks.json` で設定します。詳細は [CLI リファレンス]({{< relref "/docs/cli" >}})を参照してください。

## prVisualization の詳細

`prVisualization=false` は、子 briefing から構造化 PR 本文とゲート付き Mermaid の指示を外します。この指示は子が開く PR の本文に対するものなので、`autoPullRequest` も `true` のときだけ注入されます。1 回の run だけ外すなら `--no-pr-visualization`、shell 単位なら `FANOUT_PR_VISUALIZATION=0` です。

## prReviewGate の正確な意味

`prReviewGate=false` は、子 Claude Code の hook を強制的に無効化する設定ではありません。代わりに子 briefing へ、`/post-work-review` 前に `PreToolUse` hook が PR 作成を止めた場合は `FANOUT_SKIP_PR_REVIEW=1 gh pr create ...` を使ってよい、という注記を入れるだけです。hook 自体の仕組みは[トラブルシューティング]({{< relref "/docs/troubleshooting" >}})を参照してください。

## ユースケース例

リポジトリ全体で PR 自動作成を止めるには、リポジトリ設定をコミットします:

```bash
# <project_root>/.fanout/config.json — applies to every run in this repo
mkdir -p .fanout
cat > .fanout/config.json <<'EOF'
{
  "autoPullRequest": false
}
EOF
```

1 回の run だけ同じ指示を外す場合:

```bash
# Remove the automatic PR-opening requirement from child briefings for one run
fanout 123 --no-auto-pr
```

shell 単位でトグルを無効化する場合:

```bash
# Disable the Agent Teams hint globally for this shell
export FANOUT_AGENT_TEAMS_HINT=0
```
