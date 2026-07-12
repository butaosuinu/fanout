---
title: CLI リファレンス
linkTitle: CLI リファレンス
description: "fanout のコマンド形式、フラグ、環境変数、exit code を 1 ページに集めたリファレンス。"
weight: 50
kanji: 引
yomi: reference
---

## コマンド形式

```text
fanout # start the persistent tmux console
fanout <parent-issue|project-url>
       [--agent <name|NUM=name>] [--limit <N>] [--only <list>] [--skip <list>]
       [--include <list>] [--unblocked-only] [--project-status <name>]
       [--name <NUM>=<slug>[|<display>[|<branch>]]]
       [--base-branch <branch>] [--branch-prefix <prefix>] [--no-refresh]
       [--session <tmux-session>] [--sleep <seconds>]
       [--dry-run] [--debug]
       [--auto-pr|--no-auto-pr] [--pr-review-gate|--no-pr-review-gate]
       [--briefing-code-review|--no-briefing-code-review]
       [--agent-teams-hint|--no-agent-teams-hint]
       [--codex-plan-mode|--no-codex-plan-mode]
       [--pr-visualization|--no-pr-visualization]
       [--dashboard-keybind|--no-dashboard-keybind]
       [--team]
fanout plan <spec.json|plan-slug> [--agent <name|task-id=name>] [--dry-run]
       [--limit <N>] [--only <task-id[,id...]>] [--skip <task-id[,id...]>]
       [--unblocked-only] [--team] [--base-branch <branch>]
       [--branch-prefix <prefix>] [--no-refresh] [--session <tmux-session>]
       [--sleep <seconds>]
fanout plan <spec.json|plan-slug> --status [--format json|table]
fanout plan <spec.json|plan-slug> --merge <task-id>
fanout plan <spec.json|plan-slug> --close <task-id>
fanout plan <spec.json|plan-slug> --cleanup
fanout <parent-issue> --status [--format json|table] [--post-dashboard]
                                      # status of fanned children; optionally post dashboard
fanout <parent-issue> --merge <NUM> # fast-forward merge
fanout <parent-issue> --close <NUM> # remove child worktree/pane
fanout <parent-issue> --cleanup     # remove merged/closed children
fanout dashboard --web              # read-only localhost web dashboard (Session view)
fanout msg <verb> [options] [body...]  # 兄弟ペイン間の peer messaging
fanout --check-update               # Compare this binary with the latest release
fanout update                       # Replace this binary + integrations via install.sh
fanout --help
```

## 位置引数

第 1 引数は GitHub issue 番号（Sub-issues + タスクリストモード）または Projects v2 URL（Project モード）のいずれかです。URL は `https://github.com/users/<owner>/projects/<n>` か `https://github.com/orgs/<org>/projects/<n>` の形式です。正規形の `/views/<id>` サフィックスやトレイリングのクエリ文字列付き URL も受け付けるため、ブラウザのアドレスバーからそのままコピペできます。

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

## 命名とブランチのフラグ

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
| `--agent` | `<name>` または `<NUM>=<name>` | 子ペインで起動する agent CLI: `claude` または `codex`。`FANOUT_AGENT` 未設定なら必須。素の `--agent <name>` は全ての子の既定を設定し、繰り返し可能な `--agent <NUM>=<name>` 形式は子 issue（または Project item）1 件を番号で上書きする。例: `--agent codex --agent 456=claude`。各子はまず一致する per-target 上書きから agent を解決し、次に global `--agent`、最後に `FANOUT_AGENT` の順に解決する。未知の agent はペイン作成前に失敗し、live 実行では agent CLI のインストールも確認するが、いずれもその run で実際に選択された agent についてのみ行う。 |
| `--session` | `<tmux-session>` | 起動元のペインではなく指定した tmux セッション名を target にする。fanout 自体は引き続き tmux 内から実行する必要がある。 |
| `--sleep` | `<seconds>` | 子の作成成功ごとに挟む待機秒数。既定: `4`。launch 間の rate limit であり、retry 用ノブではない。 |
| `--team` | — | その run を兄弟協調に opt-in する。各子の通常 briefing に「Coordinating with your sibling panes」roster 節を付け、作成済みペインを親の peer レジストリ（[`fanout msg`](#fanout-msg) サブコマンドが読む parent ごとの SQLite バス）に seed する。`--codex-plan-mode` の子はレジストリには seed されるが最小限の Plan-Mode briefing を受け取るため、roster 節は付かない。どちらも best-effort で、レジストリの失敗が fan-out を止めることはない。既定: off。 |
| `--dry-run` | — | git worktree、tmux split-window、agent 起動のコマンド列を実行せずに表示する。worktree、ペイン、state row、briefing file は作らない。 |
| `--debug` | — | 追加の診断ログを有効化する。 |

```bash
fanout 123 --agent codex                  # 全ての子で codex
fanout 123 --agent codex --agent 456=claude   # 既定は codex、#456 だけ claude
```

## Plan fan-out (issue-less)

`fanout plan <spec.json|plan-slug>` は、GitHub child issue ではなくローカル JSON
spec から task ペインを起動します。path または `*.json` 引数はそのまま読み、
bare slug は `<git-root>/.fanout/plans/<slug>.json` を読みます。live run は元 spec
をそこへコピーするため、以後は短い slug で再実行できます。

spec フォーマット:

```json
{
  "version": 1,
  "plan": {
    "slug": "launch-plan",
    "title": "Launch plan",
    "source": "docs/launch.md"
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
| `--limit` | `<N>` | 作成する task ペインを N 件までに制限し、残りは task ID の再実行 hint として表示する。 |
| `--unblocked-only` | — | `blocked_by` 依存 task の明示 branch または生成 branch に merge 済み PR がまだ無い task を deferred にする。 |
| `--base-branch` | `<branch>` | `plan.base_branch` を上書きする。どちらも無い場合は repository default branch を解決し、`origin` remote が無い場合は現在の local branch / `HEAD` を使う。 |
| `--branch-prefix` | `<prefix>` | 生成 task branch 名の prefix。 |
| `--no-refresh` | — | task worktree 作成前の base branch refresh をスキップする。 |
| `--team` | — | その plan run を兄弟協調に opt-in する。issue モードと同じだが、peer は issue 番号ではなく **task ID** で指定する（issue-less な plan task には `#N` が無い）。plan の per-parent peer レジストリに seed し、各 task briefing に roster 節を付ける。plan のバスは `/tmp/fanout-<repo>-plan-<slug>.db`。plan の read / lifecycle モード（`--status` / `--close` / `--merge` / `--cleanup`）とは併用不可。既定: off。 |

`--agent` は issue モードと同じ働きですが、per-target 上書きは issue 番号ではなく task ID をキーにします。`--agent <name>` が既定を設定し、繰り返し可能な `--agent <task-id>=<name>` 形式が task 1 件を上書きします。各 task はまず一致する上書き、次に global `--agent`、最後に `FANOUT_AGENT` の順に解決します。

```bash
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --dry-run
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --unblocked-only
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --agent api-client=codex
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
します。`--close <task-id>` は記録済み task worktree、ペイン、state row を削除します。
`--cleanup` は head branch に merge 済み PR がある記録済み plan task ペインを閉じます。
これらの mode は `FANOUT_STATE_PATH` を尊重します。

agent wrapper は同梱 skill 経由で plan fan-out へ routing します。Claude Code は
`/fanout plan ...` と `~/.claude/skills/fanout-plan/`、Codex は `$fanout-plan` または
`fanout plan` 依頼と `~/.codex/skills/fanout-plan/` を使います。skill は spec を
作成または選択し、`fanout plan ... --dry-run` を実行して task / wave / branch を
要約し、確認スキップが明示されていない限り確認後に live 実行します。

## settings 系フラグ

これらのペアになったスイッチは、子の起動モード、briefing の指示、tmux キーバインドをその run だけ切り替えます。
CLI flag は常に環境変数や設定ファイルのレイヤより優先されます。
各設定の適用範囲と解決順序は [Settings]({{< relref "/docs/settings" >}}) を参照してください。

| フラグ | 引数 | 説明 |
|---|---|---|
| `--auto-pr` / `--no-auto-pr` | — | テスト通過後に `Closes #N` 付きで PR を開く要求を子 briefing に含めるか外すか。既定: on。 |
| `--pr-review-gate` / `--no-pr-review-gate` | — | 既定の PR レビューゲート前提を維持するか、hook が PR 作成をブロックした場合に `FANOUT_SKIP_PR_REVIEW=1 gh pr create ...` を許可する注記を Claude briefing に加えるか。既定: on。 |
| `--briefing-code-review` / `--no-briefing-code-review` | — | Claude 専用の `/code-review` briefing 指示を含めるか外すか。既定: on。 |
| `--agent-teams-hint` / `--no-agent-teams-hint` | — | Claude 専用の Agent Teams ヒントを子 briefing に含めるか外すか。既定: on。 |
| `--codex-plan-mode` / `--no-codex-plan-mode` | — | 通常の issue / Project の子 fan-out について、解決済みの `codexPlanMode` 設定を上書きする。有効時は、positional の `codex "<prompt>"` ではなく app-server で各 Codex child の Plan Mode thread を作成し、interactive Codex TUI を接続する。初期 Plan turn の受理後に launch を記録し、plan 生成と承認待ちには startup timeout を設けない。ビルトイン既定値: off。詳細と対象外の起動経路は[エージェント連携]({{< relref "/docs/agents" >}})。 |
| `--pr-visualization` / `--no-pr-visualization` | — | auto-PR の子 briefing に構造化 PR 本文とゲート付き Mermaid の指示を含めるか外すか。既定: on。 |
| `--dashboard-keybind` / `--no-dashboard-keybind` | — | ライブ fan-out 後に tmux の `F12` / `prefix + D` ダッシュボードキーと `prefix + M` 同一 worktree 操作キーを登録する（またはスキップする）。既定: on。 |

## 読み取りとライフサイクルのモード

### `--status`

`fanout <parent> --status` は読み取り専用です。`.fanout/state.json`（または `FANOUT_STATE_PATH`）からその parent の記録済み子を列挙し、各子について `gh api graphql` で issue state と closed-by PR の merge/review/CI 状態を取得して、既定では JSON 1 ドキュメントを stdout に出力します。

JSON ドキュメントは `parent`、`children[]`（各子は `num` / `state` / `prs[]`（`number` / `state` / `mergedAt` / `reviewDecision` / `ci`）/ `has_merged_pr`）、`summary`（`total` / `merged` / `pending` / `blocked` / `all_merged`）を持ちます。

- `--format <json|table>`（出力形式。既定: `json`）。table 形式は正規化した PR 状態（`open`、`draft`、`review-required`、`approved`、`changes-requested`、`merged`、`closed`）、CI、差分バー、変更ファイル数、Conventional-Commit 種別、PR リンクを追加する。
- `--post-dashboard`（親 issue に marker 付き rollup コメントを 1 つ upsert する）。各子の PR リンク、PR 状態、CI、差分規模、Conventional-Commit 種別、TL;DR、Review effort score を機械可読な PR データから集約する。`--status` 系で唯一 GitHub に書き込む option。

parent は issue モードのみです。Projects v2 URL を parent にした `--status` は最初に拒否されます。

```bash
fanout 123 --status
fanout 123 --status | jq '.summary.all_merged'
fanout 123 --status --format table
fanout 123 --status --post-dashboard
```

`--status` はすべての action 系フラグ（`--agent`、`--limit`、`--only`、`--skip`、`--include`、`--name`、`--base-branch`、`--branch-prefix`、`--no-refresh`、`--session`、`--sleep`、`--popup-timeout`、`--dry-run`、`--unblocked-only`、`--close`、`--merge`、`--cleanup`、`--team`、`--auto-pr`、`--no-auto-pr`、`--pr-review-gate`、`--no-pr-review-gate`、`--briefing-code-review`、`--no-briefing-code-review`、`--agent-teams-hint`、`--no-agent-teams-hint`、`--codex-plan-mode`、`--no-codex-plan-mode`、`--pr-visualization`、`--no-pr-visualization`、`--dashboard-keybind`、`--no-dashboard-keybind`）と排他です。

### `--merge` / `--close` / `--cleanup`

Lifecycle コマンドは `.fanout/state.json` の記録済み entry だけを対象にします。任意の worktree を filesystem scan で探すことはしません。`--status` と同じく `FANOUT_STATE_PATH` を尊重します。

`.fanout/state.json` には `schemaVersion` と、ペインごとに 1 行を保存します。
各行は `parent` / `issueNum` / `slug` / `branchName` / `paneId` / `agent` / `displayName` / `worktreePath` / `prompt` / `createdAt` を必ず持ちます。
空のとき省略されるキーは `taskId` / `kind` / `shellKey` / `baseBranch` / `wave` / `agentStatus`、Codex メタデータ(`codexPlanMode` / `codexThreadId` / `codexSessionId`)、attach 元(`sourceParent` / `sourceIssueNum` / `sourceTaskId`)です。
TUI の shell terminal は `kind: "shell"` で記録されるため、close は tmux ペインと state 行だけを消します(`shellKey` が行と live tmux ペインを結びつけます)。
既存 worktree に追加した agent は `kind: "attached-agent"` で記録されます。

- `fanout <parent> --merge <NUM>` は、記録済み branch を `git -C <project-root> merge --ff-only <recorded-branch>` で取り込む。fast-forward できない場合は git エラーを報告するだけで、エディタや conflict 解決フローは起動しない。
- `fanout <parent> --close <NUM>` は、記録済み worktree を `git worktree remove <path> --force` で削除し、記録済み tmux ペインが残っていれば kill し、state entry を削除して `git worktree prune` を実行する。
- `fanout <parent> --cleanup` は、issue が `CLOSED`、または closed-by PR に `MERGED` を含む記録済み子をまとめて後始末する。保留中の子は記録されたまま残る。

```bash
fanout 123 --merge 4
fanout 123 --close 4
fanout 123 --cleanup
```

## Lifecycle hooks

fanout は worktree・ペイン・merge の各 event で user shell hook を実行します —
fan-out 中（`worktree_created`、`before_pane_create`）と上記の lifecycle
コマンドの両方が対象です。Hook は常に有効です。user hook config が無い場合、
または event に command が無い場合、その event は no-op です。fanout は
`$XDG_CONFIG_HOME/fanout/hooks.json`、
または `XDG_CONFIG_HOME` が無い場合は `~/.config/fanout/hooks.json` を読みます。
ファイルは Codex 風の `hooks` object です:

```json
{
  "hooks": {
    "worktree_created": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "echo worktree=$FANOUT_WORKTREE_PATH",
            "timeout": 10,
            "statusMessage": "Preparing child worktree"
          }
        ]
      }
    ]
  }
}
```

対応する `type` は `"command"` だけです。command は project root から
`/bin/sh -c` で実行します。`timeout` は秒数で、省略時は 60 秒です。同じ event
の command はファイル順に実行します。

対応 event:

| Hook | 実行タイミング |
|---|---|
| `worktree_created` | Blocking。`git worktree add` 後、ペイン作成前。 |
| `before_pane_create` | Background。worktree 作成後、`tmux split-window` 前。 |
| `before_worktree_remove` | Blocking。`--close` / `--cleanup` の `git worktree remove` 前。 |
| `worktree_removed` | Background。記録済み worktree の削除後。 |
| `before_pane_close` | Background。記録済みペインを閉じる前。 |
| `pane_closed` | Background。ペイン close 試行後。 |
| `pre_merge` | Blocking。`git merge --ff-only` 前。 |
| `post_merge` | Background。fast-forward merge 成功後。 |

Blocking hook が失敗すると操作を止め、hook の出力を表示します。Hook には
`FANOUT_ROOT`、`FANOUT_PARENT`、`FANOUT_ISSUE_NUM`、`FANOUT_TASK_ID`、
`FANOUT_SLUG`、`FANOUT_PROMPT`、`FANOUT_AGENT`、`FANOUT_TMUX_PANE_ID`、
`FANOUT_WORKTREE_PATH`、`FANOUT_BRANCH`、`FANOUT_BASE_BRANCH`、
`FANOUT_TARGET_BRANCH` を渡します。fanout に対応する値がある項目は、互換用の
`DMUX_*` 変数にも入ります。

## サブコマンド

### `fanout dashboard`

```text
fanout dashboard --web [--port N] [--open] [--no-token] [--no-keybind]
```

読み取り専用の localhost Web ダッシュボードを起動します。`127.0.0.1` にのみバインドし、GET 専用、トークンでゲートされます。fanout の Session（親ごとにまとめた記録済みペイン）を、ペインの生存、issue 状態、PR マージ状態とともにライブ表示します。

| フラグ | 引数 | 説明 |
|---|---|---|
| `--port` | `N` | バインドする port。既定: `0`（OS 割り当ての ephemeral port）。確定した URL が表示される。 |
| `--open` | — | 既定ブラウザで URL を開く。既に起動中のサーバ（`.fanout/dashboard.json` に記録）があればそれを再利用し、二重起動しない。 |
| `--no-token` | — | `/api/*` をゲートする起動毎のランダムトークンを外す。単一ユーザ端末向け。 |
| `--no-keybind` | — | ダッシュボード起動時の tmux `F12` / `prefix + D` / `prefix + M` キーバインド登録をスキップする。 |

全フラグは `fanout dashboard --help` を参照してください。

### `fanout focus-console`

```text
fanout focus-console [--from <pane-id>] [--client <client-name>]
```

live な [TUI コンソール]({{< relref "/docs/monitoring" >}})ペインに切り替えます。コンソール起動時に登録される `F11` / `prefix + T` キーが実行するコマンドです。live なコンソールが無いときはステータスラインに通知して正常終了します。

| flag | 引数 | 説明 |
|---|---|---|
| `--from` | `pane-id` | 記録済み project root で複数コンソールから選ぶ基準ペイン。既定: `$TMUX_PANE`。 |
| `--client` | `client-name` | 切り替える tmux クライアント。既定: 現在のクライアント。 |

### `fanout msg`

```text
fanout msg <verb> [options] [body...]
```

parent ごとの SQLite メッセージバス上での兄弟協調です。fanout したペイン内で実行すると、`fanout msg` は自分がどの子でどの親に属すかを、tmux ペインと `.fanout/state.json` から自動検出します。ペインは fan-out 時に [`--team`](#実行制御フラグ) で opt-in しますが、後から任意のペインが自分で `register` することもできます。Claude Code の Agent Teams との違いと協調のワークフローは[ワークフロー]({{< relref "/docs/workflow" >}})を参照してください。

| verb | 説明 |
|---|---|
| `peers` | この親に登録済みの兄弟を一覧する。 |
| `inbox` | `[--all] [--mark-read]`: 未読の 1:1 メッセージと未読の共有ボード投稿。`--all` は既読も含め、`--mark-read` は表示分を drain する。 |
| `board` | `[--all]`: 共有ボード（全兄弟へのブロードキャスト）。cursor ベース。`--all` は既読の投稿も含める。 |
| `send` | `--to <N> [--kind K] <body...>`: 子 issue `<N>` 宛に 1:1 メッセージを送る。末尾の語が body になる。 |
| `post` | `[--kind K] <body...>`: `<body...>` を共有ボードに投稿する。 |
| `mark-read` | `[--id <N> ... \| --all]`: 1:1 メッセージを id 指定（繰り返し可）で既読にするか、`--all` で全件を既読にしてボードカーソルを進める。 |
| `register` | このペインを peers テーブルに upsert する（`--team` が自動で行う。再 join に使う）。 |
| `nudge` | `<N>`: best-effort で、peer `#N` の agent が入力を queue できる状態（`running` / `working` / `plan` / `idle`）のときだけ tmux 経由でそのペインに inbox の hint を送る。メッセージではなく通知専用 verb で、DB は触らない。それ以外（ペイン消失 / 状態不明 / 許可待ちの `blocked` / done）は何もせず success（no-op）。 |

verb 共通のオプション: `--json`（機械可読出力）、`--self <N>` と `--parent <ref>`（ペイン検出を上書き）。

[`fanout plan --team`](#plan-fan-out-issue-less) の run では、peer は issue 番号ではなく **task ID** で指定します: `send --to <task-id>`、`peers` は現在の task ID 一覧を表示します。plan モードのペインの `--json` 出力には `selfTask` / `fromTask` / `toTask` フィールドが付き、合成 peer 番号から task ID を解決できます。issue / Project の JSON は変わりません。

データベースは `/tmp/fanout-<repo>-<parent>.db` に置かれ、`FANOUT_DB_PATH` で上書きできます。協調は **pull ベース**です。メッセージは DB に永続し、兄弟は自分のチェックポイントで読むため、`fanout msg` は忙しいペインに割り込みません。pure-Go の SQLite ドライバが同梱されているため、外部 `sqlite3` は不要です。

| Exit code | 意味 |
|---|---|
| `0` | 成功（対象の agent へ nudge できない状態のときの best-effort `nudge` no-op を含む） |
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
| `FANOUT_CODEX_PLAN_MODE` | 通常の issue / Project の子を Codex Plan Mode で起動する設定（`codexPlanMode`）の環境変数レイヤ。 |
| `FANOUT_PR_VISUALIZATION` | 構造化 PR 本文とゲート付き Mermaid 指示（`prVisualization`）の環境変数レイヤ。 |
| `FANOUT_DASHBOARD_KEYBIND` | tmux ダッシュボード / 同一 worktree 操作キーバインド（`dashboardKeybind`）の環境変数レイヤ。 |
| `FANOUT_CONSOLE_KEYBIND` | tmux コンソール復帰キーバインド（`consoleKeybind`）の環境変数レイヤ。 |
| `FANOUT_WATCHER` | watcher opt-in（`watcher`）の環境変数レイヤ。 |
| `FANOUT_WATCHER_TRIGGER_LABEL` | watcher trigger label（`watcherTriggerLabel`）の環境変数レイヤ。 |
| `FANOUT_WATCHER_RUNNING_LABEL` | watcher running label（`watcherRunningLabel`）の環境変数レイヤ。 |
| `FANOUT_WATCHER_INTERVAL_SECONDS` | watcher interval（`watcherIntervalSeconds`）の環境変数レイヤ。 |
| `FANOUT_WATCHER_AGENT` | watcher child agent（`watcherAgent`）の環境変数レイヤ。 |
| `FANOUT_WATCHER_MAX_SESSIONS` | watcher session 上限（`watcherMaxSessions`）の環境変数レイヤ。 |
| `FANOUT_NOTIFICATIONS` | TUI 遷移通知チャネル（`notifications`）の環境変数レイヤ。[設定]({{< relref "/docs/settings" >}})を参照。 |
| `FANOUT_TUI_ENHANCED_KEYS` | `0` で TUI prompt 欄の enhanced keyboard input を無効化する。既定は有効。`Shift+Enter` の改行は端末が区別して報告する必要があり（fanout が tmux `extended-keys` を有効化する）、`Ctrl+J` は常に使える。 |
| `FANOUT_NTFY_URL` | ntfy POST URL（`ntfyURL`）の環境変数レイヤ。 |
| `FANOUT_SLACK_WEBHOOK_URL` | Slack webhook POST URL（`slackWebhookURL`）の環境変数レイヤ。 |
| `FANOUT_DB_PATH` | `--team` と `fanout msg` が使う parent ごとの peer messaging SQLite パスを上書きする。既定: `/tmp/fanout-<repo>-<parent>.db`。 |
| `FANOUT_SKIP_PR_REVIEW` | PR レビューゲート hook の 1 回限りのバイパス: `gh pr create` の先頭に `FANOUT_SKIP_PR_REVIEW=1` を付ける。[トラブルシューティング]({{< relref "/docs/troubleshooting" >}})を参照。 |

bool の settings 変数は `1/true/yes/on` と `0/false/no/off` を受け付けます（大小文字は無視）。integer の watcher 変数は 10 進整数を受け付けます。不正な値は warn して無視されます。settings の解決順序では CLI flag と設定ファイルの間に位置します。

## Exit codes

既定の fan-out フローは、成功（「子が無く、何もすることが無い」を含む）で `0`、前提条件 / 環境の問題で `1`、不正な呼び出しで `2` を返します。`fanout plan` の live / dry-run task 作成も同じ lane を使い、成功または何もすることが無い場合は `0`、環境、spec、filter、preflight、launch の失敗で `1`、不正な呼び出しで `2` です。読み取りと lifecycle 系の mode は独立した exit code 体系を持ちます。

### `--status`

| Exit code | 意味 |
|---|---|
| `0` | status を出力した（実際の状態は JSON mode の `summary.all_merged` で確認する） |
| `2` | 列挙不能: 不正な呼び出し、読めない / 壊れた state file、使えない project root、Projects v2 URL を parent に指定。state file が無い場合は空の state として扱う |
| `3` | `gh` API 呼び出しが失敗した（認証、ネットワーク、存在しない issue など） |

### `fanout plan --status`

| Exit code | 意味 |
|---|---|
| `0` | plan status を出力した（実際の状態は JSON mode の `summary.all_merged` で確認する） |
| `1` | status preflight で `git` や `gh` などの必須 dependency が見つからない |
| `2` | 不正な呼び出し、読めない / 壊れた spec/state、または使えない project root |
| `3` | task PR 状態を解決する `gh pr list --head <branch>` が失敗した |

### `fanout plan --close` / `--merge` / `--cleanup`

| Exit code | 意味 |
|---|---|
| `0` | lifecycle が完了した。cleanup 対象 row が無い場合も含む |
| `1` | 環境、git merge、worktree 削除、ペイン cleanup、state 更新のいずれかが失敗した |
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
