---
title: watcher
linkTitle: watcher
description: "`fanout:auto` ラベルを無人 session に変える opt-in の TUI 常駐ランチャ。有効化、ラベルのライフサイクル、session 予算、後始末をまとめます。"
weight: 45
kanji: 巡
yomi: watcher
---

watcher は、`fanout:auto` ラベルを one-shot の fanout session に変える opt-in のランチャです。
CLI を手で叩かなくても起動します。
動くのは引数なしの TUI コンソールを開いている間だけです。
コンソールを抜けると watcher も止まります。
cron や webhook のサービスではありません。
リポジトリ全体からラベル付き issue を見つけ、それぞれを一度だけ起動します。
親配下の子を継続的に巡回し続けるものではありません。

blocker が解けるたびに `fanout <parent> --unblocked-only` を手で再実行する代わりに使います([ワークフロー]({{< relref "/docs/workflow" >}})を参照)。
信頼できる issue に一度ラベルを付ければ、あとは watcher が自分で拾います。

## 有効化する

watcher を有効化できるのは user config か `FANOUT_WATCHER` 環境変数だけです。
repo config では有効化できません。checkout しただけのリポジトリが勝手に session を起動し始めてはいけないからです。
ただし `watcherTriggerLabel`、`watcherRunningLabel`、`watcherIntervalSeconds`、`watcherAgent`、`watcherMaxSessions` は repo config でも設定できます。
これらの設定に CLI flag はありません。
キーの一覧と config ファイルの場所は [Settings]({{< relref "/docs/settings" >}}) を参照してください。

```bash
export FANOUT_WATCHER=1
export FANOUT_WATCHER_AGENT=codex
fanout
```

watcher は、この引数なし TUI コンソールが起動している間だけ動きます。
コンソールを終了すると止まります。

## ラベルのライフサイクル

信頼できる issue に trigger label(既定 `fanout:auto`)を付けます。
次の cycle で、watcher はラベルを running label(既定 `fanout:running`)に付け替えてから、その issue の session を起動します。
ラベルの付け替えに失敗した場合、watcher は起動しません。
running 状態を記録できないまま session が動くより、起動しないほうを選ぶ fail-closed な設計です。

watcher は `watcherIntervalSeconds` ごとに巡回します。
既定は 60 秒で、最低 20 秒に丸められます。

## 何が起動するか

起動するものは issue の子によって変わります。

- OPEN な子を持つ issue は、`--unblocked-only` 相当でファンアウトします。unblocked な子ごとに 1 ペインを作る親ファンアウトです。
- OPEN な子を持たない issue は、子が全部 CLOSED の場合も含めて、予約 parent `@watch` 配下の単独ペインとして起動します。

## session 予算

`watcherMaxSessions` が上限にするのは起動回数ではなく live ペイン数です。
watcher は cycle ごとに、起動元を問わずリポジトリ全体の live な(shell 以外の)fanout ペインを数え、その数が上限を下回っている間だけ起動します。
既定は 4 で、`watcherMaxSessions=0` は無制限を意味します。

親ファンアウトは起動した子 1 つにつき 1 枠を使い、ペインが閉じれば枠は空きます。
blocked な子や session 上限で積み残しが出た場合、watcher はラベルを `fanout:running` から `fanout:auto` に戻します。
その issue は後続の cycle で自動的に再試行されます。

## TUI で見張る

watcher 専用のペインや画面はありません。
代わりに、monitor 画面のフッターに 1 行のステータスが出ます。

```text
watch: on label=fanout:auto running last=12:03:45 launched=2 err=-
```

この行が消えるのは watcher を有効化していないときだけです。
watcher が起動したペインは、monitor 表の他の行と同じように現れ、同じように操作できます([モニタリング]({{< relref "/docs/monitoring" >}})を参照)。

`s` で設定 popup を開くと、コンソールを離れずに `watcher` や interval、label を変更できます。
変更は次の cycle から反映されます。

## 後始末と再投入

親ファンアウトでは、`fanout <parent> --merge <child>`、`--close`、`--cleanup` が、他の親ファンアウトと同じく `fanout:running` を best-effort で外します。

単独の `@watch` ペインには、それらのコマンドに渡す parent 引数がありません。
公開 CLI の parent 引数は、予約 parent `@watch` の row を受け付けないからです。
代わりに TUI の lifecycle key で畳んでください。
`c` か `x` でペインを閉じ(worktree や branch も一緒に落とせます)、`m` で branch を merge、`X` で merged / closed なペインを cleanup します。

watcher にもう一度その issue を処理させるには、まず上の方法でペインを畳んでから `fanout:auto` を付け直します。
ペインが記録されている間、watcher はその issue を実行中とみなしてスキップするので、畳む前にラベルを付け直しても何も起動しません。

## trigger label はプロンプトインジェクションの境界

> **セキュリティ。** ラベルを付けた issue の本文と、そこから起動される OPEN な子の本文は、そのまま agent の briefing になります。
> `fanout:auto` は実行依頼として扱い、その issue と起動対象の子を信頼できるときだけ付けてください。
