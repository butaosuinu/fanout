---
title: クイックスタート
linkTitle: クイックスタート
description: "5 分で最初のファンアウト。親 issue から並列ペインを立ち上げるまでの手順を示します。"
weight: 20
kanji: 開
yomi: quickstart
---

## 最初のファンアウト(5 分)

親 issue に OPEN なサブ issue が 3 つあるとします。
1 つずつ着手して順番待ちさせる代わりに、fanout は全部を同時に動かします。
子ごとに tmux のペインを 1 枚開き、それぞれが独立した git worktree を持つので、3 つのエージェントが互いの編集と衝突せずに並行して進みます。

{{< diagram "overview" >}}

issue ツリーがまだ無い場合は、同梱の `fanout-issues` skill が計画を親 issue とリンク済みの子 issue に変換します([エージェント連携]({{< relref "/docs/agent-integrations" >}})を参照)。

tmux セッションを開始(または attach)します。

```bash
tmux new -A -s work
```

次に、セッションの中でエージェントを決めて対象リポジトリへ移動します。

```bash
# Use Claude for child panes, from the repository root
export FANOUT_AGENT=claude
cd path/to/repo
```

まず計画をプレビューします。
`--dry-run` は git worktree と tmux のコマンド列を実行せずに表示するだけで、worktree もペインも state 行も briefing ファイルも作りません。

```bash
# Preview commands without creating worktrees, panes, state, or briefings
fanout 123 --dry-run
```

計画に問題がなければ本実行します。

```bash
# Fan out all OPEN sub-issues of #123
fanout 123
```

子 issue ごとに、現在の tmux セッション内のペイン、`.fanout/worktrees/<slug>/` 配下の独立した worktree、issue ごとの briefing を指す 1 行プロンプトで起動したエージェントが得られます。

> ペイン作成には `gh`、`git`、`tmux 3.3+` が `PATH` 上に必要です。
> fanout は起動時に依存を確認し、欠けていればインストールのヒントを表示します([インストール]({{< relref "/docs/installation" >}})を参照)。

このページは既定の tmux backend を前提にしています。opt-in の [herdr backend]({{< relref "/docs/herdr-backend" >}}) は v1 では観測専用で、このファンアウトは実行できません。

## 子 issue の宣言方法

fanout は子を GitHub の Sub-issues と、親本文のタスクリスト行 `- [ ] #NUM ...` の和集合で列挙し、処理するのは `state == "OPEN"` の子だけです。
タスクリスト参照は同一 repo 内のみ有効で、`owner/repo#NUM` はスキップされます。
書き方の詳細は[ワークフロー]({{< relref "/docs/workflow" >}})を参照してください。

## 再実行しても安全(冪等性)

`.fanout/state.json` が起動済みの子を記録するため、同じ親で再実行しても記録済みの子はスキップされ、足りない分だけが作られます。

```bash
# Rerun after the first batch — already-recorded children are skipped
fanout 123
```

## エージェントの指定

エージェント名が解決できることが必須です。
`--agent claude`、`--agent codex`、`--agent opencode` のいずれかを渡すか、`FANOUT_AGENT` を設定してください。

```bash
fanout 123 --agent claude
fanout 123 --agent codex
fanout 123 --agent opencode
export FANOUT_AGENT=claude   # applies to every following run in this shell
```

3 つのエージェントは同梱 skill、Codex Plan Mode、メッセージングが異なります。
違いは[エージェント連携]({{< relref "/docs/agent-integrations" >}})のマトリクスで比較できます。

未知のエージェントはペイン作成前に失敗し、live 実行では選択したエージェント CLI がインストール済みかも確認します。
fanout は tmux セッション内から実行してください。
子ペインは `tmux split-window` で直接作られ、`--session` で別セッションを指定しない限り起動元ペインが target になります。
唯一の例外は引数なしの `fanout`(TUI コンソール)で、素のシェルからも起動できます([モニタリング]({{< relref "/docs/monitoring" >}})を参照)。

次は[ワークフロー]({{< relref "/docs/workflow" >}})です。
run を形作る flag をそこで扱います。
