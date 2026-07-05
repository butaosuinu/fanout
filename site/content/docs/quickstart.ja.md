---
title: クイックスタート
linkTitle: クイックスタート
description: "5 分で最初のファンアウト。親 issue から並列ペインを立ち上げ、その間に何が起きるかも示します。"
weight: 20
kanji: 開
yomi: quickstart
---

## 最初のファンアウト（5 分）

親 issue に OPEN なサブ issue が 3 つあるとします。1 つずつ着手して順番待ちさせる代わりに、fanout は全部を同時に動かします。子ごとに tmux のペインを 1 枚開き、それぞれが独立した git worktree を持つので、3 つのエージェントが互いの編集と衝突せずに並行して進みます。

{{< diagram "overview" >}}

issue ツリーがまだ無い場合は、同梱の `fanout-issues` skill が計画を親 issue + リンク済みの子 issue に変換します（[エージェント連携]({{< relref "/docs/agents" >}}) を参照）。fanout が期待する形は[後述](#子-issue-の宣言方法)します。

tmux セッションを開始（または attach）します。

```bash
tmux new -A -s work
```

次に、セッションの中でエージェントを決めて対象リポジトリへ移動します。

```bash
# Use Claude for child panes, from the repository root
export FANOUT_AGENT=claude
cd path/to/repo
```

まず計画をプレビューします。`--dry-run` は git worktree と tmux のコマンド列を実行せずに表示するだけで、worktree もペインも state 行も briefing ファイルも作りません。

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

> ペイン作成には `gh`、`git`、`tmux 3.3+` が `PATH` 上に必要です。fanout は起動時に依存を確認し、欠けていればインストールのヒントを表示します（[インストール]({{< relref "/docs/installation" >}})を参照）。

## 子 issue の宣言方法

自分の issue ツリーをどう書けば fanout が拾うかを示します。fanout は 2 つのソースの和集合で子を列挙します。

- **Sub-issues** として親に正式リンクされた issue
- **親本文のタスクリスト参照**。`- [ ] #NUM ...` にマッチする行の `#NUM`

```text
- [ ] #124 Extract the parser
- [ ] #125 Add the --format flag
- [ ] other/repo#126 Upstream fix   <- skipped: same-repo references only
```

タスクリスト参照は同一 repo 内のみで、`owner/repo#NUM` 形式はスキップされます。子はどちらか一方のソースでも両方でも宣言でき、和集合なので重複は除かれます。処理されるのは `state == "OPEN"` の子だけで、CLOSED の子にペインは作られません。

つまり、親 issue にサブ issue を 3 つぶら下げてもよいし、親本文に 3 行のタスクリストを書いてもよいし、両方を混ぜてもかまいません。どう書いても、OPEN な子がそのまま並列ペインになります。

## 実行時に起きること

live 実行は次のステップで進みます。

1. `gh`、`git`、`tmux 3.3+` がインストールされているかを確認する。
2. リポジトリルート、現在の tmux セッションと起動元ペイン、`--agent` または `FANOUT_AGENT` から使うエージェントを解決する。
3. Sub-issues と親タスクリスト行の和集合で子を列挙する（OPEN の子のみ処理）。
4. `.fanout/state.json` を読み、`(parent, issueNum)` ペアが記録済みの子はスキップする。
5. 対象 issue ごとに、issue 本文と短い Requirements チェックリストからなる briefing を書き出す。
6. fresh 化した base branch から `.fanout/worktrees/<slug>/` を作り、`tmux split-window` で子ペインを選択せずに開き、ペインタイトルを設定してウィンドウを快適幅グリッドに組み直し（失敗時は `main-vertical` → `tiled` にフォールバック）、次の子に進む前に `--sleep` 秒（既定 4）スリープする。
7. created / skipped / deferred / failed の件数サマリを表示する。

> ペイン起動プロンプトが短いのは意図的です。完全な issue 本文は briefing ファイルにあり、エージェントはそれを読むよう指示されます。`deferred` は `--unblocked-only` 指定時のみ現れ、OPEN な blocker が残る子を保留します。

> `--dry-run` では確認用に将来の briefing path と size を表示するだけで、ファイルを書くのは live 実行時だけです。

## 再実行しても安全（冪等性）

冪等とは、同じ操作を複数回行っても 1 回行ったのと同じ結果になることです。`.fanout/state.json` が起動済みのペインを `(parent, issueNum)` キーで記録するため、同じ親で再実行しても記録済みの子はスキップされ、足りない分だけが作られます。

```bash
# Rerun after the first batch — already-recorded children are skipped
fanout 123
```

state 行の無い既存の `.fanout/worktrees/<slug>/` ディレクトリも、ファンアウト済みとして扱われます。pre-state run や中断された launch のための移行用 fallback です。

## エージェントの指定

エージェント名が解決できることが必須です。`--agent claude` か `--agent codex` を渡すか、`FANOUT_AGENT` を設定してください。

```bash
fanout 123 --agent claude
fanout 123 --agent codex
export FANOUT_AGENT=claude   # applies to every following run in this shell
```

未知のエージェントはペイン作成前に失敗し、live 実行では選択したエージェント CLI がインストール済みかも確認します。fanout は tmux セッション内から実行してください。子ペインは `tmux split-window` で直接作られ、`--session` で別セッションを指定しない限り起動元ペインが target になります。唯一の例外は引数なしの `fanout`（TUI コンソール）で、素のシェルからも起動できます（[モニタリング]({{< relref "/docs/monitoring" >}})を参照）。

次は[ワークフロー]({{< relref "/docs/workflow" >}})で、run を形作る flag を見ていきます。
