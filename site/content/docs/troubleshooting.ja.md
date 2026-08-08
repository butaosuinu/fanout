---
title: トラブルシューティング
linkTitle: トラブルシューティング
description: "fanout のよくあるエラーメッセージと直し方。"
weight: 100
kanji: 直
yomi: troubleshoot
---

## "fanout must be run inside tmux"

action mode は `tmux split-window` で子ペインを直接作成するため、対象リポジトリの worktree で tmux セッションを開始または attach してから fanout を実行してください。
唯一の例外は TUI モード(引数なしの `fanout`)で、fanout 管理の tmux session を自分で作成または attach するため素のシェルから起動できます。
詳しくは[モニタリング]({{< relref "/docs/monitoring" >}})を参照してください。

## "agent is required"

`--agent claude`、`--agent codex`、または `--agent opencode` を渡すか、環境変数 `FANOUT_AGENT` を設定してください。

```bash
fanout 123 --agent claude
FANOUT_AGENT=codex fanout 123
```

未知の agent はペイン作成前に失敗し、live 実行では選択された agent CLI が `PATH` 上に無い場合も失敗します。

## "prepare worktree"

git worktree の準備に失敗しているので、出力に含まれる内側の git エラーを確認してください。
よくある原因は次のとおりです。

- fast-forward できない dirty な checked-out base branch
- diverge した local base branch
- 既存の branch 名
- stale または missing な remote branch

base を変えるには `--base-branch <branch>` を使い、remote-tracking ref から直接切りたい場合は `origin/<branch>` を指定します。

```bash
fanout 123 --base-branch release/v2
fanout 123 --base-branch origin/main
```

`--no-refresh` は、意図的に現在の local base/ref から切りたいときにだけ使ってください(fanout は base branch を force で整えることはせず、安全に refresh できない場合はユーザーのローカル作業を壊す代わりに失敗します)。

## "sub-issues fetch failed"

- 未認証:

```bash
gh auth status
```

- 親 issue が存在しない: Sub-issues API が HTTP 404 を返し、fanout は exit 1 します。issue 番号を確認してください。
- リンクされたサブ issue がゼロなのはエラーではありません。fanout は次を表示して exit 0 します:

```text
no sub-issues on #<parent>
```

## slug や branch 名が意図と違う

既定では slug に `slugify(title)-<issueNum>`、branch に `fanout/<slug>` を使います。
特定 issue は `--name <NUM>=<slug>|<display>|<branch>` で個別に、run 全体の branch 名は `--branch-prefix <prefix>` で一括して上書きできます。
詳しくは[ワークフロー]({{< relref "/docs/workflow" >}})を参照してください。

```bash
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'
fanout 123 --name 8='feat-x|Feature X|feat/issue-8-x'   # all three segments
fanout 123 --name 9='||release/v2.0'                    # branch override only
fanout 123 --branch-prefix fanout/release/
```

## `gh pr create` が deny される("post-work-review が未実施です")

`PreToolUse(Bash)` hook(`.claude/hooks/pre-pr-review-gate.sh`、コミット済みの `.claude/settings.json` に登録)が、現在の HEAD が `/post-work-review` を通過するまで `gh pr create` をブロックします。
Claude の legacy review marker では既定の PR base だけを許可します。
Codex が reviewed-base metadata を記録した場合は、PR base と現在の `origin/<base>...head` diff hash を metadata の値と照合します。
metadata が不正または stale なら deny します。
`/post-work-review` を実行してから、レビュー済みの base を指定して `gh pr create` を再実行してください(一度だけバイパスしたいときは、コマンドの先頭に次を付けます)。

```bash
FANOUT_SKIP_PR_REVIEW=1 gh pr create ...
```

fanout settings で `prReviewGate=false` になっている場合、子 Claude briefing にもこの bypass 許可が入りますが、コミット済み hook 自体は変更されません(スイッチの意味は[Settings]({{< relref "/docs/settings" >}})を参照)。

ゲートは HEAD に固定されるため新しいコミットを積むと再武装されるので、PR の前にもう一度レビューしてください(marker は worktree ローカルなので、fanout の並列ペイン同士が干渉することはありません)。
`python3` が無い環境では fail-closed になって PR 作成らしきコマンドを粗い判定で deny するため、`python3` をインストールするか `FANOUT_SKIP_PR_REVIEW=1` を使ってください。

## `post-work-review` で `agent_type` error が出る

現在の `$post-work-review` は `agent_type` を要求しません。
通常の native `spawn_agent` を使い、`task_name` は task label としてだけ扱います。
旧 error が残る場合は `fanout update` を実行してください。
その後、新しい Codex session を開始してください。
Codex は session 起動時に skill を読み込み、checksum 検証付き release installer が廃止済みの custom agent と driver を削除します。

`make install` または `make link` が旧 driver を報告した場合、checkout は build や binary の置換前に停止しています。
`fanout update` を `--no-skills` なしで実行してから make target を再実行してください。

現在の gate には native `spawn_agent`、`wait_agent`、空き concurrency slot が必要です。
いずれかを使えない場合は、`codex exec`、app-server、別 reviewer へ fallback せず停止します。

子は親 session の権限を継承します。
sandbox で reviewer の書き込みを禁止する場合は、親を read-only で開始してください。
reviewer は prompt と repository read を通じて repository の内容を受け取ります。

## Project モードで items が取れない

Project モードは GraphQL クエリで Project items を取得するため、`gh` CLI に `read:project` スコープが必要です(無いとクエリが失敗します)。
スコープを付与して再実行してください。

```bash
gh auth refresh -s read:project
```

items が想定より少なく見えることがありますが、Status フィールドの無い Project は全 item にフォールバックし、現在の git repository root と異なる repository の item は警告付きでスキップする仕様どおりの挙動です。
詳しくは[ワークフロー]({{< relref "/docs/workflow" >}})と[CLI リファレンス]({{< relref "/docs/cli" >}})を参照してください。

## "herdr named session ... is not running"

このエラーは、外部の [herdr backend]({{< relref "/docs/herdr-backend" >}}) session を観測する経路のものです。
herdr 側で名前付き session を起動し、同じシェルから確認してください。`default` は拒否されます。

```bash
herdr status --json   # server と session の状態
```

`HERDR_SOCKET_PATH` は `HERDR_SESSION` より優先されるため、古い socket path が残っていると fanout が別の server を見に行きます。`status` の結果が想定と合わないときは unset してください。
TUI 版のエラー `run fanout inside an existing herdr pane (HERDR_ENV=1)` は文字どおりの意味です。herdr backend でのコンソールは、herdr session 内の pane から起動したときだけ立ち上がります。
CLI の issue、Project、plan、watcher launch は、代わりにリポジトリの fanout-owned session を作成または再採用します。

## "unsupported herdr CLI version ..."

fanout は stable herdr 0.7.5 以上を要求します。CLI と server の version は一致させてください。
prerelease と解釈できない version は fail closed します。
`herdr stable >=0.7.5 is required: ...` は、`PATH` に `herdr` バイナリが見つからないという意味です。
実際に入っているものを確認してください。

```bash
herdr --version      # stable 0.7.5 以上
herdr status --json  # client/server の version 一致
```

stable herdr 0.7.5 以上へ更新してください。`requires a client/server restart` が出る場合は、herdr の server と client を再起動して同じビルドに揃えます。

fanout は method と response field を事前検査しません。`herdr method "<name>" is unavailable` は、その method call が失敗したことを示します。インストール済みの herdr がその method を提供するか確認してください。

## "herdr backend interactive TUI actions are read-only"

故障ではありません。
このメッセージは対話 TUI の変更操作と未対応の操作に適用され、CLI の issue、Project、plan、watcher、`--team` launch には適用されません。これらの launch レーンでは `--backend herdr` を使えます。
Codex 子の Plan Mode は、変更前に専用の `runtime backend herdr does not support ...` エラーで失敗します。
記録済みペインを持つ親への矛盾する backend 指定は、引き続き `explicit migration is required` で失敗します。v1 に移行コマンドはありません。
機能の対応表は [herdr backend]({{< relref "/docs/herdr-backend" >}}) にあります。
