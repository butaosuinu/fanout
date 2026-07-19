---
title: 並列開発ワークフロー
linkTitle: ワークフロー
description: "wave 駆動のループ。issue ツリーを育ててファンアウトし、blocker が解けた子から merge して次の wave へ進みます。"
weight: 30
kanji: 流
yomi: workflow
---

## ループの全体像

fanout の日常は 1 回きりのコマンドではなくループです。
OPEN な子を持つ親 issue を育て、子を並列ペインにファンアウトし、ペインの作業を眺め、終わった子を畳んで、次のバッチへ再実行します。
一度に全部を終わらせるのではなく、blocker が解けた子から次々と並列で進める流れです。

1. **issue ツリーを作る。** 親 issue と、それにリンクされた子 issue 群を用意します。同梱の `fanout-issues` skill が計画を fanout-ready な形に変換します([エージェント連携]({{< relref "/docs/agents" >}})を参照)。
2. **ファンアウトする。** `fanout <parent>` が OPEN な子ごとに tmux ペインと git worktree を作り、それぞれで agent CLI を起動します。
3. **モニタする。** 各ペインの issue と PR の状態を追います([モニタリング]({{< relref "/docs/monitoring" >}})を参照)。
4. **merge する。** 完了した子 branch を `--merge <NUM>` で取り込みます。
5. **後始末する。** `--cleanup` が、issue が close 済みか PR が merge 済みの記録済み子をまとめて畳みます。
6. **次の wave へ。** fanout を再実行します。通常は `--unblocked-only` を付け、blocker が閉じたばかりの子を次のバッチにします。

## fanout-ready な issue ツリーの書き方

子 issue は GitHub の **Sub-issues** 経由でも、親本文の**タスクリスト**(`- [ ] #NUM ...`)経由でも、両方でも宣言できます。
fanout は両ソースの和集合を取ります。

```text
- [ ] #4 Extract the parser
- [ ] #7 Port the formatter (blocked by #4)
```

blocker(後述の wave 進行を駆動する依存関係)は 2 つの書式から読み取られます。

- **子本文の `## Blocked by` セクション。** 次の空行までに並ぶ issue 番号を集めます。
- **親タスクリスト行のトレイラ。** 上の `#7` のように、子の行末尾に付ける `(blocked by #X, #Y)` です。

```text
## Blocked by
- #4
- #7
```

> `blocked` ラベルは弱いシグナルです。
> fanout はログに出すだけで、ラベル単体から具体的な blocker 番号を推測することはありません。

## 子の選択

その run が対象にする OPEN な子は `--limit`、`--only`、`--skip`、`--include` の 4 flag で絞り込めます。
各 flag の効果は [CLI リファレンス]({{< relref "/docs/cli" >}}) にまとめてあります。

## wave 進行(`--unblocked-only`)

依存のある複数の子を抱えていると、blocker が解けるまで全員を待たせたくはありません。
先に進められる子だけを並列で動かし、blocker が閉じるたびに次の子を足したい。
この段階実行が wave です。

`--unblocked-only` は、blocker がすべて CLOSED の子だけをファンアウトします。
OPEN な blocker が 1 つでも残る子は `deferred (blocked)` として報告され、その run ではスキップされます。
スキップされた子には何も作られないので、取り消すものもありません。

再実行では `.fanout/state.json` に記録済みの子もスキップされます。
そのため、blocker PR が merge されるたびに同じコマンドをもう一度実行するだけでプロジェクトが進みます。
Wave 1 → Wave 2 → … が手動の管理なしで進みます。

{{< diagram "waves" >}}

```bash
fanout 123 --unblocked-only

fanout 123 --unblocked-only --limit 3
```

2 つ目の形は、fanout に次の unblocked バッチを選ばせつつ、各 wave の件数を制限します。

## ラベル watcher

watcher は引数なしの TUI コンソールを開いている間だけ動く opt-in のランチャで、信頼できる issue に付けた `fanout:auto` ラベルを one-shot session に変えます。
既定は off で、有効化できるのは user config か環境変数だけです(repo config では checkout を自動起動の対象にできません)。
有効化の手順とラベル運用の詳細は [Watcher]({{< relref "/docs/watcher" >}}) を参照してください。

## issue を介さない plan ファンアウト

ローカルでブレストやメモから作業をすでに分解済みで、GitHub に child issue を立てるほどではない、というときがあります。
issue ツリーを作らずに、手元の分解をそのまま並列ペインに流したい。
そのためのレーンが `fanout plan` です。
ループ自体は同じですが、source of truth は issue tree ではなく JSON spec です。

1. plan spec(JSON)を書く、または既存の spec を選ぶ。
2. まず `fanout plan <spec> --dry-run --agent <agent>` で preview する。
3. live run は `fanout plan <spec> --agent <agent>`。task に `blocked_by` がある場合は通常 `--unblocked-only` も付ける。
4. TUI や dashboard、または `fanout plan <slug> --status [--format table]` で見る。
5. task ID を指定して取り込みや後始末をする: `fanout plan <slug> --merge <task-id>`、`--close <task-id>`、`--cleanup`。
6. 次 wave は保存済み slug で再実行する。

spec フォーマットと、live run が記録する state の詳細は [CLI リファレンス]({{< relref "/docs/cli" >}}) にあります。

```bash
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --dry-run
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --unblocked-only
fanout plan launch-plan --status --format table
fanout plan launch-plan --merge base-types
fanout plan launch-plan --cleanup
```

## 兄弟協調(peer messaging)

並列で動く兄弟ペインが同じ interface を触っていると、互いの進捗や決め事を伝え合いたくなります。
ところが親を複数ペインにファンアウトすると、各子は自分のペインで動く独立した agent セッションになり、既定ではペイン同士は互いを認識できません。
run ごとに `--team` で opt-in すると、fanout が(best-effort で)各子の通常 briefing に「Coordinating with your sibling panes」節を注入し、per-parent の peer レジストリに seed するので、兄弟がお互いを把握できます。
`--codex-plan-mode` の子はレジストリには seed されますが最小限の Plan-Mode briefing を受け取るため、協調節は付きません。

ファンアウトしたペイン内では、`fanout msg` が自分が今どの子(または親)かを自動検出し、per-parent のバス上でやり取りします。

```bash
fanout msg peers                # 兄弟は誰か?
fanout msg post "auth interface merged — login を触る前に rebase して"
fanout msg send --to 7 "SessionStore を PaneStore にリネームした"
fanout msg inbox --mark-read
```

verb の全表や plan task の指定方法など、詳しい仕組みは [CLI リファレンス]({{< relref "/docs/cli#fanout-msg" >}}) にあります。

メッセージはバスに永続し、兄弟は自分のチェックポイントで読みます。その pull の上に、`--team` は `claude` ペインと新規起動の非 Plan `codex` ペインに push レーンを載せます(Codex Plan Mode のペインは pull のまま)。`claude` ペインは briefing の指示で、最初のツール操作として Monitor ツール(persistent)の下で `fanout msg watch` を起動し、以後の新着は到着ごとに流れてきて配信時に既読になります(mark-on-emit)。新規起動の非 Plan `codex` ペインは app-server ブリッジ経由になり、idle な turn へ未読メッセージを引用付きの untrusted data として注入します。動作中のペインでレーンが使えないとき(Monitor 不可、restore した codex ペイン)は pull(`inbox` / `board`)と `nudge` に戻ります。注入失敗の回収は `inbox --all` です(失敗した分は既読化済みのため)。

`claude` と `codex` どちらのペインでも同じく動き、1 セッション内のチームメイトを協調させる Claude Code Agent Teams とは別物です。

> **セキュリティ。** バスは `/tmp` 配下の**平文** SQLite ファイルです。
> fanout は `0600`(所有者のみ)で作成し、group/world-readable や別ユーザー所有のファイルは開くのを拒否します。
> ただし `/tmp` は共有のスクラッチ領域なので、**秘密情報やトークン、認証情報をメッセージに載せないでください。**

## 命名とブランチ

既定では、各子の worktree slug は `slugify(title)-<issueNum>`、branch は `fanout/<slug>` です。
名前は `--name` と `--branch-prefix` で上書きできます。
`--base-branch` と `--no-refresh` は名前ではなく、子の分岐元 base を制御します。

```bash
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'
```

詳細は [CLI リファレンス]({{< relref "/docs/cli" >}}) を参照してください。

## Project モード

位置引数には、親 issue 番号の代わりに Projects v2 の URL も渡せます(`https://github.com/users/<owner>/projects/<n>` または `https://github.com/orgs/<org>/projects/<n>`)。
正規形の `/views/<id>` suffix やトレイリングのクエリ文字列付き URL も受け付けるので、ブラウザのアドレスバーからそのままコピペできます。
このモードでは、子は Sub-issues とタスクリストの和集合ではなく Project の item から取り出されます。

```bash
fanout https://github.com/users/<owner>/projects/<n>

fanout https://github.com/orgs/<org>/projects/<n> --project-status "In Progress"

fanout https://github.com/users/<owner>/projects/<n> --project-status all
```

既定フィルタは `Status == Todo` の item で、`--project-status` で変更できます([CLI リファレンス]({{< relref "/docs/cli" >}}) を参照)。
blocker は子本文の `## Blocked by` セクションからだけ読み取られます。
親本文が無いためタスクリスト行の `(blocked by #X)` トレイラは存在せず、`blocked` ラベルだけの子は警告のうえ unblocked として扱われます。

> Project モードでは `gh` CLI に `read:project` スコープが必要です([インストール]({{< relref "/docs/installation" >}})を参照)。

## 取り込みと後始末(lifecycle)

lifecycle コマンドは `.fanout/state.json` に記録された entry に対してのみ動作します。

```bash
fanout 123 --merge 4
fanout 123 --close 4

fanout 123 --cleanup
```

このページの flag や lifecycle コマンドの正確な動作を含む全サーフェスは [CLI リファレンス]({{< relref "/docs/cli" >}}) にあります。
