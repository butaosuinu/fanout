---
title: 設定
linkTitle: 設定
description: "オン/オフできる 6 つの opinionated な挙動と、その背後にある flag > env > repo > user > default の解決順序。"
weight: 60
kanji: 整
yomi: settings
---

fanout は opinionated な 6 つの挙動(briefing の 5 トグル+ダッシュボードの tmux キーバインド)をオン/オフできます。後方互換のため、既定値はすべて `true` です。

## 解決順序

各設定の優先順位は **CLI flag > 環境変数 > リポジトリ設定ファイル > ユーザー設定ファイル > ビルトイン既定値** です。fanout は git リポジトリルートを解決した後、run ごとに 1 回だけ逆順に重ねて解決します。

- リポジトリ設定: `<project_root>/.fanout/config.json`。この `project_root` は親リポジトリルートで、子 worktree ではありません。
- ユーザー設定: `$XDG_CONFIG_HOME/fanout/config.json`、`XDG_CONFIG_HOME` が無い場合は `~/.config/fanout/config.json`。

## 6 つのトグル

| 挙動 | ファイルキー | env | CLI flag | 既定値 |
|---|---|---|---|---|
| PR 自動作成指示 | `autoPullRequest` | `FANOUT_AUTO_PR` | `--auto-pr` / `--no-auto-pr` | `true` |
| PR レビューゲート通知 | `prReviewGate` | `FANOUT_PR_REVIEW_GATE` | `--pr-review-gate` / `--no-pr-review-gate` | `true` |
| Claude `/code-review` 指示 | `briefingCodeReview` | `FANOUT_BRIEFING_CODE_REVIEW` | `--briefing-code-review` / `--no-briefing-code-review` | `true` |
| Claude Agent Teams ヒント | `agentTeamsHint` | `FANOUT_AGENT_TEAMS_HINT` | `--agent-teams-hint` / `--no-agent-teams-hint` | `true` |
| 構造化 PR 本文とゲート付き Mermaid の briefing 指示 | `prVisualization` | `FANOUT_PR_VISUALIZATION` | `--pr-visualization` / `--no-pr-visualization` | `true` |
| ダッシュボード `prefix + D` tmux キーバインド | `dashboardKeybind` | `FANOUT_DASHBOARD_KEYBIND` | `--dashboard-keybind` / `--no-dashboard-keybind` | `true` |

flag の一覧は [CLI リファレンス]({{< relref "/docs/cli" >}})にも載っています。

## config.json サンプル

リポジトリ設定とユーザー設定はどちらも同じ形で、bool のみのフラットな JSON オブジェクトです:

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

環境変数は `1/true/yes/on` と `0/false/no/off` を受け付けます(大小文字は無視)。

## 前方互換

不正な env 値、設定ファイル内の未知キー、bool 以外の値は warn して無視します。将来の設定追加で古い fanout バイナリが壊れないようにするためです。

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
