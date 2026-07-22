---
title: 設定
linkTitle: 設定
description: "briefing のトグル、session の launch posture、watcher 制御、TUI 通知 channel と設定の解決順。"
weight: 70
kanji: 整
yomi: settings
---

fanout settings は、子 briefing の指示、session の launch posture、watcher、tmux キーバインド、通知を制御します。
watcher は off、briefing とキーバインドの bool は `true`、通知は `bell` が既定です。
新規 Session と issue オーケストレーターは既定で agent の plan mode、子は build mode で始まります。

## 解決順序

同じ設定を CLI flag と config ファイルの両方で指定したらどちらが勝つか。優先順位は **CLI flag > 環境変数 > リポジトリ設定ファイル > ユーザー設定ファイル > ビルトイン既定値** です。fanout は git リポジトリルートを解決した後、run ごとに 1 回だけ低い層から順に重ねて解決します。

- リポジトリ設定: `<project_root>/.fanout/config.json`。この `project_root` は親リポジトリルートで、子 worktree ではありません。
- ユーザー設定: `$XDG_CONFIG_HOME/fanout/config.json`、`XDG_CONFIG_HOME` が無い場合は `~/.config/fanout/config.json`。

## TUI から設定を編集する

常駐 TUI コンソールで `s` を押すと、どちらの config ファイルも popup で編集できます。
Target 行で user config と repo config を切り替えます。
各キーは値を指定するか `inherit` に戻せます。`inherit` は選択中の JSON ファイルからそのキーを削除します。

popup が編集するのは config ファイルだけです。
CLI flag と `FANOUT_*` 環境変数は、保存した値より引き続き優先されます。
repo config から watcher は変更できず、launch posture の 3 キー(`newSessionPlanMode` / `orchestratorPlanMode` / `childPlanMode`)、HTTP 通知 URL、`runtimeBackend` も設定できません。
`notifications` に指定できるのは `bell`、`tmux`、`none` だけです。

## 各トグルの目的

挙動トグルは briefing の指示と tmux キーバインドを制御します。

- `autoPullRequest`: 子に作業完了後の PR 自動作成を指示します。PR を人手で作るチームなら外します。
- `prReviewGate`: 既定の on では PR レビューゲートの前提を保ちます。off にすると、PR 作成 hook に止められたときに `FANOUT_SKIP_PR_REVIEW=1 gh pr create` を許可する注記が Claude の子 briefing に入ります（後述）。
- `briefingCodeReview`: Claude の子に、コミット前に変更へ `/code-review` スラッシュコマンドを走らせるよう指示します。
- `agentTeamsHint`: Claude の子に Claude Code Agent Teams を使う余地があると伝えます。Claude 以外の子には影響しません。
- `prVisualization`: 子が開く PR の本文を構造化し、条件付きで Mermaid 図を入れる指示を加えます（後述）。
- `dashboardKeybind`: tmux に `F12` / `prefix + D` のダッシュボードキーと `prefix + M` の同一 worktree 操作キーを登録します。
- `consoleKeybind`: TUI コンソール起動時に、tmux へ `F11` / `prefix + T` のコンソール復帰キーを登録します。
- `runtimeBackend`: 解決順の上位で決まらなかったときの fallback の runtime backend（`tmux` または `herdr`）です。親に記録済みの backend、`--backend`、`FANOUT_BACKEND`、実行環境のコンテキスト（`HERDR_ENV` / `TMUX`）がいずれも優先されます。user config 専用で、repo config では警告付きで無視されます。[herdr backend]({{< relref "/docs/herdr-backend" >}}) は v1 では観測専用です。

3 つの launch posture 設定は、各レーンの session を agent の plan mode で始めるかを決めます。3 つの agent(claude / codex / opencode)すべてに共通で効きます:

- `newSessionPlanMode`(既定 `true`): TUI の新規 Session — 手動プロンプトペイン、plan fan-out coordinator(claude / codex のみ)、`a` で attach する agent ペイン。
- `orchestratorPlanMode`(既定 `true`): issue fan-out のプロジェクトルートに立つオーケストレーターペイン。codex のオーケストレーターは Plan Mode と start gate を両立できないため、fanout は警告して素の codex で起動します。
- `childPlanMode`(既定 `false`): issue / Project の子、OPEN 子なし issue の単独 Session、`fanout plan` のタスク、watcher 起動。on にすると無人の [watcher]({{< relref "/docs/watcher" >}}) Session が plan 承認待ちで止まります(v2.1.207 未満または version 判定不能の claude は警告つきで mode フラグが省かれ、止まりません)。

3 キーとも user 専用です。変更できるのは user config、環境変数、TUI 設定フォーム(`s`)だけで、repo config の値は警告して無視され、CLI flag もありません。Plan Mode は `--team` より優先されます — codex の plan 子は最小の Plan briefing のまま team bridge を失います。agent ごとのフラグと版要件は [Agent Integrations]({{< relref "/docs/agent-integrations" >}}) を参照してください。廃止された codex 専用の `codexPlanMode` キーと `FANOUT_CODEX_PLAN_MODE` は、3 キーへの置き換えを促す警告つきで無視されます。

watcher と通知 channel は別系統の設定です。
watcher はラベル巡回による自動起動を opt-in で制御します。

- `watcher`: opt-in スイッチです。有効化できるのは user config か `FANOUT_WATCHER` だけで、repo config では有効化できません。
- `watcherTriggerLabel`: 「この issue を起動せよ」を意味する trigger label です(既定 `fanout:auto`)。
- `watcherRunningLabel`: issue を起動する際に付け替える running label です(既定 `fanout:running`)。自前のラベル体系と衝突するなら変更します。
- `watcherIntervalSeconds`: watcher が trigger label を巡回する間隔です(既定 `60` 秒、最低 20 秒に丸められます)。
- `watcherAgent`: watcher が子を起動する agent です。未設定なら TUI の既定 agent にフォールバックします。
- `watcherMaxSessions`: 同時に生きている fanout ペイン数の上限です(既定 `4`、`0` で無制限)。

通知 channel は、TUI の状態遷移をどこへ知らせるかを選びます。

## トグルと通知 channel

| 挙動 | ファイルキー | env | CLI flag | 既定値 |
|---|---|---|---|---|
| PR 自動作成指示 | `autoPullRequest` | `FANOUT_AUTO_PR` | `--auto-pr` / `--no-auto-pr` | `true` |
| PR レビューゲート通知 | `prReviewGate` | `FANOUT_PR_REVIEW_GATE` | `--pr-review-gate` / `--no-pr-review-gate` | `true` |
| Claude `/code-review` 指示 | `briefingCodeReview` | `FANOUT_BRIEFING_CODE_REVIEW` | `--briefing-code-review` / `--no-briefing-code-review` | `true` |
| Claude Agent Teams ヒント | `agentTeamsHint` | `FANOUT_AGENT_TEAMS_HINT` | `--agent-teams-hint` / `--no-agent-teams-hint` | `true` |
| 構造化 PR 本文とゲート付き Mermaid の briefing 指示 | `prVisualization` | `FANOUT_PR_VISUALIZATION` | `--pr-visualization` / `--no-pr-visualization` | `true` |
| ダッシュボード / 同一 worktree 操作 tmux キーバインド | `dashboardKeybind` | `FANOUT_DASHBOARD_KEYBIND` | `--dashboard-keybind` / `--no-dashboard-keybind` | `true` |
| コンソール復帰 tmux キーバインド | `consoleKeybind` | `FANOUT_CONSOLE_KEYBIND` | n/a | `true` |
| 新規 Session の Plan Mode | `newSessionPlanMode` | `FANOUT_NEW_SESSION_PLAN_MODE` | n/a | `true` |
| オーケストレーターの Plan Mode | `orchestratorPlanMode` | `FANOUT_ORCHESTRATOR_PLAN_MODE` | n/a | `true` |
| 子の Plan Mode | `childPlanMode` | `FANOUT_CHILD_PLAN_MODE` | n/a | `false` |
| runtime backend | `runtimeBackend` | `FANOUT_BACKEND` | `--backend <tmux\|herdr>` | `tmux` |
| watcher opt-in | `watcher` | `FANOUT_WATCHER` | n/a | `false` |
| watcher trigger label | `watcherTriggerLabel` | `FANOUT_WATCHER_TRIGGER_LABEL` | n/a | `fanout:auto` |
| watcher running label | `watcherRunningLabel` | `FANOUT_WATCHER_RUNNING_LABEL` | n/a | `fanout:running` |
| watcher interval 秒 | `watcherIntervalSeconds` | `FANOUT_WATCHER_INTERVAL_SECONDS` | n/a | `60` |
| watcher child agent | `watcherAgent` | `FANOUT_WATCHER_AGENT` | n/a | 未設定 |
| watcher 最大 session 数 | `watcherMaxSessions` | `FANOUT_WATCHER_MAX_SESSIONS` | n/a | `4` |
| TUI 状態遷移通知 | `notifications` | `FANOUT_NOTIFICATIONS` | n/a | `bell` |
| ntfy POST URL | `ntfyURL` | `FANOUT_NTFY_URL` | n/a | 未設定 |
| Slack webhook POST URL | `slackWebhookURL` | `FANOUT_SLACK_WEBHOOK_URL` | n/a | 未設定 |

flag ペアは [CLI リファレンス]({{< relref "/docs/cli" >}})にも載っています。
launch posture、watcher、通知設定に CLI flag はありません。

## config.json サンプル

リポジトリ設定とユーザー設定はどちらも同じ形で、bool、string、integer のキーを持つフラットな JSON オブジェクトです:

```json
{
  "autoPullRequest": false,
  "prReviewGate": true,
  "briefingCodeReview": true,
  "agentTeamsHint": false,
  "prVisualization": true,
  "dashboardKeybind": true,
  "consoleKeybind": true,
  "newSessionPlanMode": true,
  "orchestratorPlanMode": true,
  "childPlanMode": false,
  "runtimeBackend": "tmux",
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

bool の環境変数は `1/true/yes/on` と `0/false/no/off` を受け付けます(大小文字は無視)。
integer の環境変数は 10 進整数を受け付けます。`watcherIntervalSeconds` は最低 `20` に解決され、`watcherMaxSessions=0` は無制限を意味します。

## 通知 channel

ターミナルを離れている間に子の状態遷移を知りたいなら、通知先を選びます。
`notifications` は、issue / PR 遷移(merged、CI failed、blocker 待ち)と TUI 由来の agent-state 遷移(plan ready、waiting for input、agent exited)の両方を扱います。
comma または空白区切りの selector で、指定できる値は `bell`、`tmux`、`ntfy`、`slack`、`none` です。
`ntfy` は `ntfyURL`、`slack` は `slackWebhookURL` が必要です。

どちらの HTTP channel も outbound POST のみで、inbound socket は開きません。repo の設定だけで外部へ勝手に送信されるのを防ぐため、repo config で選べるのは `bell`、`tmux`、`none` だけです。`ntfy`、`slack`、`ntfyURL`、`slackWebhookURL` は user config か環境変数からだけ有効になります。

agent-state 通知は `@fanout_agent_state` から発火し、ペイン出力からは推定しません。
通知するのは `plan`、`blocked`、`done` だけです。
`running`、`working`、`idle` は TUI に表示されますが、通知は送りません。

## watcher の安全制約

watcher は誰かが checkout しただけで自動起動してほしくない機能です。そのため repo config では opt-in できません。`<project_root>/.fanout/config.json` が `watcher` を設定していると、fanout は警告してそのキーを無視します。有効化は user config か `FANOUT_WATCHER` で行ってください。一方、`watcherTriggerLabel`、`watcherRunningLabel`、`watcherIntervalSeconds`、`watcherAgent`、`watcherMaxSessions` は repo config でも設定できます。

trigger label は、label を付けた issue と、それが parent fan-out なら起動される OPEN child から agent 作業を始める合図です。それらの本文はそのまま agent briefing になります。label は実行依頼として扱い、その issue と起動対象の child を信頼できるときだけ付けてください。運用の詳細は [Watcher]({{< relref "/docs/watcher" >}}) を参照してください。

## 前方互換

不正な bool / integer env 値、設定ファイル内の未知キー、JSON type が合わない値は warn して無視します。将来の設定追加で古い fanout バイナリが壊れないようにするためです。

Lifecycle hook は常に有効で、別の `hooks.json` で設定します。詳細は CLI リファレンスの [Lifecycle hooks]({{< relref "/docs/cli#lifecycle-hooks" >}}) を参照してください。

## prVisualization の詳細

`prVisualization=false` は、子 briefing から構造化 PR 本文とゲート付き Mermaid の指示を外します。この指示は子が開く PR の本文に向けたものなので、`autoPullRequest` も `true` のときだけ注入されます。1 回の run だけ外すなら `--no-pr-visualization`、shell 単位なら `FANOUT_PR_VISUALIZATION=0` です。

## prReviewGate の正確な意味

`prReviewGate=false` は子 Claude Code の hook を強制的に無効化しません。子 briefing に注記を 1 つ足すだけです。`/post-work-review` の前に `PreToolUse` hook が PR 作成を止めた場合、`FANOUT_SKIP_PR_REVIEW=1 gh pr create ...` を使ってよい、という注記です。hook 自体の仕組みは[トラブルシューティング]({{< relref "/docs/troubleshooting" >}})を参照してください。

## ユースケース例

リポジトリ全体で PR 自動作成を止めるには、repo config をコミットします:

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
