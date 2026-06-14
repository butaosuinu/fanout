---
title: CLI リファレンス
linkTitle: CLI リファレンス
description: "コマンド形式、フラグ、環境変数、exit code まで — fanout の全サーフェスを 1 ページに。"
weight: 50
kanji: 引
yomi: reference
---

## コマンド形式

```text
fanout # start the persistent tmux console
fanout <parent-issue|project-url>
       [--agent <name>] [--limit <N>] [--only <list>] [--skip <list>]
       [--include <list>] [--unblocked-only] [--project-status <name>]
       [--name <NUM>=<slug>[|<display>[|<branch>]]]
       [--base-branch <branch>] [--branch-prefix <prefix>] [--no-refresh]
       [--session <tmux-session>] [--sleep <seconds>]
       [--popup-timeout <seconds>] [--dry-run] [--debug]
       [--auto-pr|--no-auto-pr] [--pr-review-gate|--no-pr-review-gate]
       [--briefing-code-review|--no-briefing-code-review]
       [--agent-teams-hint|--no-agent-teams-hint]
       [--codex-plan-mode|--no-codex-plan-mode]
       [--pr-visualization|--no-pr-visualization]
       [--team]
fanout plan <spec.json|plan-slug> [--agent <name>] [--dry-run]
       [--limit <N>] [--only <task-id[,id...]>] [--skip <task-id[,id...]>]
       [--unblocked-only] [--base-branch <branch>] [--branch-prefix <prefix>]
       [--no-refresh] [--session <tmux-session>] [--sleep <seconds>]
fanout plan <spec.json|plan-slug> --status [--format json|table]
fanout plan <spec.json|plan-slug> --merge <task-id>
fanout plan <spec.json|plan-slug> --close <task-id>
fanout plan <spec.json|plan-slug> --cleanup
fanout <parent-issue> --status [--format json|table] [--post-dashboard]
                                      # status of fanned children; optionally post dashboard
fanout <parent-issue> --merge <NUM> # fast-forward merge a recorded child branch
fanout <parent-issue> --close <NUM> # remove a recorded child worktree/pane
fanout <parent-issue> --cleanup     # remove merged/closed recorded children
fanout dashboard --web              # read-only localhost web dashboard (Session view)
fanout msg <verb> [options] [body...]  # 兄弟ペイン間の peer messaging
fanout --check-update               # Compare this binary with the latest release
fanout update                       # Replace this binary + integrations via install.sh
fanout --help
```

## 位置引数

第 1 引数は GitHub issue 番号（Sub-issues + タスクリストモード）または Projects v2 URL（Project モード）のいずれかです — `https://github.com/users/<owner>/projects/<n>` または `https://github.com/orgs/<org>/projects/<n>` 形式で、正規形の `/views/<id>` サフィックスやトレイリングのクエリ文字列付き URL も受け付けるので、ブラウザのアドレスバーからそのままコピペできます。

`--project-status` は Project モード専用で、issue モードでは無視されます。

## 子選択フラグ

| フラグ | 引数 | 説明 |
|---|---|---|
| `--limit` | `<N>` | 今回の run で起動する子の数を制限する。残り分は再実行コマンド付きで表示される。 |
| `--only` | `<list>` | ファンアウトする issue 番号のカンマ区切りリスト（例: `--only 4,7,8,10`）。OPEN 子集合に無い番号は警告付きで無視され、fanout が勝手に任意の issue を見に行くことは無い。`--skip` とは併用不可。`--limit` より先に適用。 |
| `--skip` | `<list>` | 除外する issue 番号のカンマ区切りリスト（例: `--skip 6,9`）。OPEN 子集合の残り全部がファンアウトされる。`--only` とは併用不可。`--limit` より先に適用。 |
| `--include` | `<list>` | Sub-issues API + 親本文タスクリストの自動検出で拾われない issue 番号を強制追加する。同梱の Claude/Codex 連携が親本文の暗黙の子参照を読み取り、承認された番号をここに載せる想定。CLOSED や存在しない番号は警告してスキップ。 |
| `--unblocked-only` | — | ブロッカーがすべて CLOSED の子だけをファンアウトする。OPEN ブロッカーが残る子は最終サマリで deferred として報告される。blocker PR が merge されるたびに再実行して安全。 |
| `--project-status` | `<name\|all>` | Project モード専用: single-select の `Status` フィールドが `<name>` と一致する Project item に絞る。既定: `Todo`。`all` でフィルタを無効化。 |

`--include` で追加した番号が先に子集合へ入り、その後に `--only`/`--skip` でフィルタされ、最後に `--limit` が残りを制限します。これらのフラグで wave 駆動のループを回す方法は[ワークフロー]({{< relref "/docs/workflow" >}})を参照してください。

```bash
fanout 123 --skip 6,9 --limit 3
fanout 123 --include 4,7
fanout 123 --unblocked-only --limit 3
```

## 命名・ブランチフラグ

| フラグ | 引数 | 説明 |
|---|---|---|
| `--name` | `<NUM>=<slug>[\|<display>[\|<branch>]]` | issue `<NUM>` の既定命名を上書きする。繰り返し可（issue ごとに 1 回）。pipe 区切りの 3 セグメントが worktree slug stem、tmux ペインタイトル、branch 名に対応する。各セグメントは空でよいが、最低 1 つは非空であること。issue 番号 suffix の無い slug stem には fanout が `-<NUM>` を付ける。 |
| `--base-branch` | `<branch>` | refresh して子 worktree の分岐元にする branch。bare な local branch 名と `origin/<branch>` に対応。既定: GitHub default branch、次に `origin/HEAD`、次に `main`。 |
| `--branch-prefix` | `<prefix>` | 生成 branch 名の prefix。既定: `fanout/`。 |
| `--no-refresh` | — | 子 worktree 作成前の base branch の `git fetch` + fast-forward refresh をスキップする。 |

```bash
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'
fanout 123 --name 8='feat-x|Feature X|feat/issue-8-x'   # all three segments
fanout 123 --name 9='||release/v2.0'                    # branch override only
fanout 123 --base-branch release/v2 --branch-prefix fanout/release/
```

## 実行制御フラグ

| フラグ | 引数 | 説明 |
|---|---|---|
| `--agent` | `<name>` | 子ペインで起動する agent CLI: `claude` または `codex`。`FANOUT_AGENT` 未設定なら必須。未知の agent はペイン作成前に失敗し、live 実行では agent CLI のインストールも確認する。 |
| `--session` | `<tmux-session>` | 起動元 pane ではなく指定した tmux セッション名を target にする。fanout 自体は引き続き tmux 内から実行する必要がある。 |
| `--sleep` | `<seconds>` | 子の作成成功ごとに挟む待機秒数。既定: `4`。launch 間の rate limit であり、retry 用ノブではない。 |
| `--team` | — | その run を兄弟協調に opt-in する。各子の通常 briefing に「Coordinating with your sibling panes」roster 節を付け、作成済みペインを親の peer レジストリ（[`fanout msg`](#fanout-msg) サブコマンドが読む parent ごとの SQLite バス）に seed する。`--codex-plan-mode` の子はレジストリには seed されるが最小限の Plan-Mode briefing を受け取るため、roster 節は付かない。どちらも best-effort で、レジストリの失敗が fan-out を止めることはない。既定: off。 |
| `--dry-run` | — | git worktree、tmux split-window、agent 起動のコマンド列を実行せずに表示する。 |
| `--debug` | — | 追加の診断ログを有効化する。 |

## Plan fan-out (issue-less)

`fanout plan <spec.json|plan-slug>` は、GitHub child issue ではなくローカル JSON
spec から task pane を起動します。path または `*.json` 引数はそのまま読み、
bare slug は `<git-root>/.fanout/plans/<slug>.json` を読みます。live run は元 spec
をそこへコピーするため、以後は短い slug で再実行できます。

spec フォーマット:

```json
{
  "version": 1,
  "plan": {
    "slug": "launch-plan",
    "title": "Launch plan",
    "source": "docs/launch.md",
    "base_branch": "main"
  },
  "tasks": [
    {
      "id": "base-types",
      "title": "Define base types",
      "briefing": "## Goal\nDefine the shared types.",
      "display_name": "Base types",
      "wave": "1"
    },
    {
      "id": "api-client",
      "title": "Extract API client",
      "briefing": "## Goal\nExtract the API client.",
      "blocked_by": ["base-types"],
      "wave": "2"
    }
  ]
}
```

必須 field は `version: 1`、`plan.slug`、`plan.title`、そして kebab-case
`id`、`title`、空でない `briefing` を持つ task 1 件以上です。任意 field は
`plan.source`、`plan.base_branch`、task ごとの `slug`、`display_name`、
`branch`、`wave`、`blocked_by` です。既定 task slug は plan 名で qualify され、
生成 branch は task が `branch` を持たない限り `fanout/<slug>` になります。

| フラグ | 引数 | 説明 |
|---|---|---|
| `--only` | `<task-id[,id...]>` | 対象を task ID に絞る。存在しない ID は警告して無視する。`--skip` とは併用不可。 |
| `--skip` | `<task-id[,id...]>` | 指定 task ID を除外する。`--only` とは併用不可。 |
| `--limit` | `<N>` | 作成する task pane を N 件までに制限し、残りは task ID の再実行 hint として表示する。 |
| `--unblocked-only` | — | `blocked_by` 依存 task の明示 branch または生成 branch に merge 済み PR がまだ無い task を deferred にする。 |
| `--base-branch` | `<branch>` | `plan.base_branch` を上書きする。どちらも無い場合は repository default branch を解決する。 |
| `--branch-prefix` | `<prefix>` | 生成 task branch 名の prefix。 |
| `--no-refresh` | — | task worktree 作成前の base branch refresh をスキップする。 |

```bash
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --dry-run
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --unblocked-only
fanout plan launch-plan --agent claude --unblocked-only --limit 2
```

Plan row は parent `plan:<slug>`、`taskId`、`issueNum: 0` として記録されます。
Task briefing は issue-closing footer を避け、PR body 末尾を
`Plan: <slug> / Task: <id>` にするよう指示します。

### Plan status と lifecycle

`fanout plan <spec|slug> --status` は spec と `.fanout/state.json` を読み、
plan task には issue 番号が無いため `gh pr list --head <branch>` で branch ごとの
PR を照会します。

```bash
fanout plan launch-plan --status
fanout plan launch-plan --status --format table
```

JSON 出力は `plan`、`tasks[]`（`id`、`branch`、`prs`、`has_merged_pr`、
`blocked`）、`summary`（`total`、`merged`、`pending`、`blocked`、
`all_merged`）を含みます。table 形式は PR state、CI、Conventional Commit type、
変更ファイル数、diff bar、link を追加します。

lifecycle コマンドは task ID を指定します:

```bash
fanout plan launch-plan --merge base-types
fanout plan launch-plan --close base-types
fanout plan launch-plan --cleanup
```

`--merge <task-id>` は記録済み task branch を project checkout へ fast-forward
します。`--close <task-id>` は記録済み task worktree、pane、state row を削除します。
`--cleanup` は head branch に merge 済み PR がある記録済み plan task pane を閉じます。
これらの mode は `FANOUT_STATE_PATH` を尊重します。

agent wrapper は同梱 skill 経由で plan fan-out へ routing します。Claude Code は
`/fanout plan ...` と `~/.claude/skills/fanout-plan/`、Codex は `$fanout-plan` または
`fanout plan` 依頼と `~/.codex/skills/fanout-plan/` を使います。skill は spec を
作成または選択し、`fanout plan ... --dry-run` を実行して task / wave / branch を
要約し、確認スキップが明示されていない限り確認後に live 実行します。

## settings 系フラグ

これらのペアになったスイッチは、fanout の opinionated な挙動をその run だけ切り替えます。CLI flag は常に環境変数・設定ファイルのレイヤより優先されます。各挙動が実際に何を注入するか、および解決順序の全体は [Settings]({{< relref "/docs/settings" >}}) を参照してください。

| フラグ | 引数 | 説明 |
|---|---|---|
| `--auto-pr` / `--no-auto-pr` | — | テスト通過後に `Closes #N` 付きで PR を開く要求を子 briefing に含めるか外すか。既定: on。 |
| `--pr-review-gate` / `--no-pr-review-gate` | — | 既定の PR レビューゲート前提を維持するか、hook が PR 作成をブロックした場合に `FANOUT_SKIP_PR_REVIEW=1 gh pr create ...` を許可する注記を Claude briefing に加えるか。既定: on。 |
| `--briefing-code-review` / `--no-briefing-code-review` | — | Claude 専用の `/code-review` briefing 指示を含めるか外すか。既定: on。 |
| `--agent-teams-hint` / `--no-agent-teams-hint` | — | Claude 専用の Agent Teams ヒントを子 briefing に含めるか外すか。既定: on。 |
| `--codex-plan-mode` / `--no-codex-plan-mode` | — | `--agent codex` のとき、positional の `codex "<prompt>"` ではなく Codex app-server 経由で initial Plan turn を開始し、interactive Codex TUI を attach する。既定: off。詳細は[エージェント連携]({{< relref "/docs/agents" >}})。 |
| `--pr-visualization` / `--no-pr-visualization` | — | auto-PR の子 briefing に構造化 PR 本文とゲート付き Mermaid の指示を含めるか外すか。既定: on。 |
| `--dashboard-keybind` / `--no-dashboard-keybind` | — | ライブ fan-out 後に tmux の `prefix + D` キーバインドを登録する（またはスキップする）。どのペインからでも読み取り専用 Web ダッシュボードを開けるようにする。既定: on。 |

## 読み取り・ライフサイクル

### `--status`

`fanout <parent> --status` は読み取り専用です。`.fanout/state.json`（または `FANOUT_STATE_PATH`）からその parent の記録済み子を列挙し、各子について `gh api graphql` で issue state と closed-by PR の merge/review/CI 状態を取得して、既定では JSON 1 ドキュメントを stdout に出力します。

- `--format <json|table>` — 出力形式。既定: `json`。table 形式は正規化した PR 状態（`open`、`draft`、`review-required`、`approved`、`changes-requested`、`merged`、`closed`）、CI、差分バー、変更ファイル数、Conventional-Commit 種別、PR リンクを追加する。
- `--post-dashboard` — 親 issue に marker 付き rollup コメントを 1 つ upsert し、各子の PR リンク、PR 状態、CI、差分規模、Conventional-Commit 種別、TL;DR、Review effort score を機械可読な PR データから集約する。`--status` 系で唯一 GitHub に書き込む option。

parent は issue モードのみ — Projects v2 URL を parent にした `--status` は最初に拒否されます。

```bash
fanout 123 --status
fanout 123 --status | jq '.summary.all_merged'
fanout 123 --status --format table
fanout 123 --status --post-dashboard
```

`--status` はすべての action 系フラグ（`--agent`、`--limit`、`--only`、`--skip`、`--include`、`--name`、`--base-branch`、`--branch-prefix`、`--no-refresh`、`--session`、`--sleep`、`--popup-timeout`、`--dry-run`、`--unblocked-only`、`--close`、`--merge`、`--cleanup`、`--auto-pr`、`--no-auto-pr`、`--pr-review-gate`、`--no-pr-review-gate`、`--briefing-code-review`、`--no-briefing-code-review`、`--agent-teams-hint`、`--no-agent-teams-hint`、`--codex-plan-mode`、`--no-codex-plan-mode`、`--pr-visualization`、`--no-pr-visualization`）と排他です。

### `--merge` / `--close` / `--cleanup`

Lifecycle コマンドは `.fanout/state.json` の記録済み entry だけを対象にします。任意の worktree を filesystem scan で探すことはしません。`--status` と同じく `FANOUT_STATE_PATH` を尊重します。

- `fanout <parent> --merge <NUM>` は、記録済み branch を `git -C <project-root> merge --ff-only <recorded-branch>` で取り込む。fast-forward できない場合は git エラーを報告するだけで、エディタや conflict 解決フローは起動しない。
- `fanout <parent> --close <NUM>` は、記録済み worktree を `git worktree remove <path> --force` で削除し、記録済み tmux pane が残っていれば kill し、state entry を削除して `git worktree prune` を実行する。
- `fanout <parent> --cleanup` は、issue が `CLOSED`、または closed-by PR に `MERGED` を含む記録済み子をまとめて後始末する。保留中の子は記録されたまま残る。

```bash
fanout 123 --merge 4
fanout 123 --close 4
fanout 123 --cleanup
```

## サブコマンド

### `fanout dashboard`

```text
fanout dashboard --web [--port N] [--open] [--no-token] [--no-keybind]
```

読み取り専用の localhost Web ダッシュボードを起動します。`127.0.0.1` にのみバインドし、GET 専用、トークンでゲートされ、fanout の Session（親ごとにまとめた記録済みペイン）をペイン生存・issue 状態・PR マージ状態とともにライブ表示します。

| フラグ | 引数 | 説明 |
|---|---|---|
| `--port` | `N` | バインドする port。既定: `0`（OS 割り当ての ephemeral port）。確定した URL が表示される。 |
| `--open` | — | 既定ブラウザで URL を開く。既に起動中のサーバ（`.fanout/dashboard.json` に記録）があればそれを再利用し、二重起動しない。 |
| `--no-token` | — | `/api/*` をゲートする起動毎のランダムトークンを外す。単一ユーザ端末向け。 |
| `--no-keybind` | — | ダッシュボード起動時の tmux `prefix + D` キーバインド登録をスキップする。 |

全フラグは `fanout dashboard --help` を参照してください。

### `fanout msg`

```text
fanout msg <verb> [options] [body...]
```

parent ごとの SQLite メッセージバス上での兄弟協調です。fanout したペイン内で実行すると、`fanout msg` は自分がどの子か（tmux pane と `.fanout/state.json` から）・どの親に属すかを自動検出します。ペインは fan-out 時に [`--team`](#実行制御フラグ) で opt-in しますが、後から任意のペインが自分で `register` することもできます。Claude Code の Agent Teams との違い、および協調のワークフローは[ワークフロー]({{< relref "/docs/workflow" >}})を参照してください。

| verb | 説明 |
|---|---|
| `peers` | この親に登録済みの兄弟を一覧する。 |
| `inbox` | `[--all] [--mark-read]` —— 未読の 1:1 メッセージと未読の共有ボード投稿。`--all` は既読も含め、`--mark-read` は表示分を drain する。 |
| `board` | `[--all]` —— 共有ボード（全兄弟へのブロードキャスト）。cursor ベース。`--all` は既読の投稿も含める。 |
| `send` | `--to <N> [--kind K] <body...>` —— 子 issue `<N>` 宛に 1:1 メッセージを送る。末尾の語が body になる。 |
| `post` | `[--kind K] <body...>` —— `<body...>` を共有ボードに投稿する。 |
| `mark-read` | `[--id <N> ... \| --all]` —— 1:1 メッセージを id 指定（繰り返し可）で既読にするか、`--all` で全件を既読にしてボードカーソルを進める。 |
| `register` | このペインを peers テーブルに upsert する（`--team` が自動で行う。再 join に使う）。 |
| `nudge` | `<N>` —— best-effort: peer `#N` の agent が running のときだけ、tmux 経由でそのペインに inbox の hint を送る。メッセージではなく通知専用 verb で、DB は触らない。対象の agent が running でない（ペイン消失 / 状態不明 / done）ときは何もせず success（no-op）。 |

verb 共通のオプション: `--json`（機械可読出力）、`--self <N>` と `--parent <ref>`（ペイン検出を上書き）、`--dry-run`（write / notify verb のみ —— `# would ...` の書き込み内容を表示し何も触らない。`--json` とは併用不可）。

データベースは `/tmp/fanout-<repo>-<parent>.db` に置かれ、`FANOUT_DB_PATH` で上書きできます。協調は **pull ベース**です: メッセージは DB に永続し、兄弟は自分のチェックポイントで読みます —— `fanout msg` は忙しいペインに割り込みません。pure-Go の SQLite ドライバが同梱されているため、外部 `sqlite3` は不要です。

| Exit code | 意味 |
|---|---|
| `0` | 成功（対象の agent が running でないときの best-effort `nudge` no-op を含む） |
| `2` | 不正な呼び出し |
| `4` | SQLite バックエンドの失敗 |

全サーフェスは `fanout msg --help` を参照してください。

### `fanout update`

```text
fanout update [--version <tag>] [--no-skills]
```

[インストール]({{< relref "/docs/installation" >}})で説明している同じ `install.sh` 経路を呼び、実行中の release バイナリと同梱の Claude/Codex 連携を置き換えます。`--version <tag>` は `FANOUT_VERSION=<tag>` を `install.sh` に渡して pin した release tag をインストールし、`--no-skills` はバイナリだけ更新します。ローカルの dev build は置き換えを拒否します。

### `fanout check-update`

```bash
fanout --check-update
fanout check-update
```

読み取り専用です。`butaosuinu/fanout` の最新 release tag を取得し、バイナリ埋め込みの version と比較して、更新の有無を表示します。`fanout check-update` は `--check-update` のサブコマンド形として受け付けられます。ローカルの dev build（`version == "dev"`）は `gh` を呼ばず、dev build 向けメッセージを出して exit 0 します。

## 環境変数

| 変数 | 説明 |
|---|---|
| `FANOUT_AGENT` | `--agent` 未指定時に子ペインで使う既定 agent。 |
| `FANOUT_STATE_PATH` | `--status` と lifecycle コマンドが読む state file を `<git-root>/.fanout/state.json` の代わりに直接指定する。 |
| `FANOUT_AUTO_PR` | PR 自動作成指示（`autoPullRequest`）の環境変数レイヤ。 |
| `FANOUT_PR_REVIEW_GATE` | PR レビューゲート注記（`prReviewGate`）の環境変数レイヤ。 |
| `FANOUT_BRIEFING_CODE_REVIEW` | Claude `/code-review` 指示（`briefingCodeReview`）の環境変数レイヤ。 |
| `FANOUT_AGENT_TEAMS_HINT` | Claude Agent Teams ヒント（`agentTeamsHint`）の環境変数レイヤ。 |
| `FANOUT_PR_VISUALIZATION` | 構造化 PR 本文とゲート付き Mermaid 指示（`prVisualization`）の環境変数レイヤ。 |
| `FANOUT_DASHBOARD_KEYBIND` | ダッシュボード `prefix + D` tmux キーバインド（`dashboardKeybind`）の環境変数レイヤ。 |
| `FANOUT_NOTIFICATIONS` | TUI 遷移通知チャネル（`notifications`）の環境変数レイヤ。[設定]({{< relref "/docs/settings" >}})を参照。 |
| `FANOUT_NTFY_URL` | ntfy POST URL（`ntfyURL`）の環境変数レイヤ。 |
| `FANOUT_SLACK_WEBHOOK_URL` | Slack webhook POST URL（`slackWebhookURL`）の環境変数レイヤ。 |
| `FANOUT_DB_PATH` | `--team` と `fanout msg` が使う parent ごとの peer messaging SQLite パスを上書きする。既定: `/tmp/fanout-<repo>-<parent>.db`。 |
| `FANOUT_SKIP_PR_REVIEW` | PR レビューゲート hook の 1 回限りのバイパス: `gh pr create` の先頭に `FANOUT_SKIP_PR_REVIEW=1` を付ける。[トラブルシューティング]({{< relref "/docs/troubleshooting" >}})を参照。 |

bool の settings 変数は `1/true/yes/on` と `0/false/no/off` を受け付けます（大小文字は無視）。不正な値は warn して無視されます。settings の解決順序では CLI flag と設定ファイルの間に位置します。

## Exit codes

既定の fan-out フローは、成功（「子が無く、何もすることが無い」を含む）で `0`、前提条件 / 環境の問題で `1`、不正な呼び出しで `2` を返します。`fanout plan` の live / dry-run task 作成も同じ lane を使い、成功または何もすることが無い場合 `0`、環境・spec・filter・preflight・launch の失敗で `1`、不正な呼び出しで `2` です。読み取り・lifecycle 系 mode は独立した exit code 体系を持ちます:

### `--status`

| Exit code | 意味 |
|---|---|
| `0` | status を出力した — 実際の状態は JSON mode の `summary.all_merged` で確認する |
| `2` | 列挙不能: 不正な呼び出し、読めない / 壊れた state file、使えない project root、Projects v2 URL を parent に指定。state file が無い場合は空の state として扱う |
| `3` | `gh` API 呼び出しが失敗した（認証、ネットワーク、存在しない issue など） |

### `fanout plan --status`

| Exit code | 意味 |
|---|---|
| `0` | plan status を出力した — 実際の状態は JSON mode の `summary.all_merged` で確認する |
| `1` | status preflight で `git` や `gh` などの必須 dependency が見つからない |
| `2` | 不正な呼び出し、読めない / 壊れた spec/state、または使えない project root |
| `3` | task PR 状態を解決する `gh pr list --head <branch>` が失敗した |

### `fanout plan --close` / `--merge` / `--cleanup`

| Exit code | 意味 |
|---|---|
| `0` | lifecycle が完了した。cleanup 対象 row が無い場合も含む |
| `1` | 環境、git merge、worktree 削除、pane cleanup、state 更新のいずれかが失敗した |
| `2` | 指定 task ID がその plan に記録されていない |
| `3` | cleanup が branch PR 状態を取得できなかった |

### `--check-update`

| Exit code | 意味 |
|---|---|
| `0` | 比較が完了した、または dev build |
| `2` | 現在の version か最新 tag が `MAJOR.MINOR.PATCH`（`v` prefix 可）でない |
| `3` | `gh release view -R butaosuinu/fanout` が失敗した |

### `update`

| Exit code | 意味 |
|---|---|
| `0` | 更新が完了した、または既に最新 |
| `1` | 環境 / preflight の失敗: dev build、`curl`/`wget` 無し、書き込めない binary ディレクトリ、オプション値の欠落 |
| `2` | 未知のオプション、想定外の引数、比較不能な version 文字列 |
| `3` | 最新 release の取得に失敗した |

## 非推奨フラグ

| フラグ | 引数 | 説明 |
|---|---|---|
| `--popup-timeout` | `<seconds>` | 旧ランタイム互換の deprecated フラグ。受理されるが、direct tmux path では無視される。 |
