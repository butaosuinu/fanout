---
title: クイックスタート
linkTitle: クイックスタート
description: "5 分で最初のファンアウト — 親 issue から並列ペインまで、その間に実際に何が起きるのかも。"
weight: 20
kanji: 開
yomi: quickstart
---

## 最初のファンアウト（5 分）

fanout は、GitHub の親 issue に紐づく OPEN のサブ issue を、子ごとに 1 つの tmux ペインへファンアウトします。各ペインは独立した git worktree を持ち、issue ごとの briefing ファイルを参照するプロンプトでエージェント CLI が起動します。

対象リポジトリで tmux セッションを開始（または attach）し、子ペインで使う agent を決めます:

```bash
# Use Claude for child panes
export FANOUT_AGENT=claude
```

まずは計画をプレビューします。`--dry-run` は git worktree + tmux コマンド列を実行せずに表示するだけで、worktree もペインも一切作りません:

```bash
# Preview the git worktree + tmux commands without executing them
fanout 123 --dry-run
```

計画に問題がなければ、本実行します:

```bash
# Fan out all OPEN sub-issues of #123
fanout 123
```

各子 issue は、現在の tmux セッション内のペイン、`.fanout/worktrees/<slug>/` 配下の独立した worktree、そして 1 行 briefing prompt 付きで起動した agent を得ます。

> ペイン作成フローには `gh`、`git`、`tmux` が `PATH` 上に必要です。fanout は依存を起動時にチェックし、失敗時にはインストールのヒントを表示します — [インストール]({{< relref "/docs/installation" >}})を参照してください。

## 子 issue の宣言方法

fanout は 2 つのソースの和集合で子を列挙します:

- **Sub-issues API** に正式リンクされている issue（`gh api repos/{owner}/{repo}/issues/<N>/sub_issues`）
- **親本文のタスクリスト参照** — `- [ ] #NUM ...` にマッチする行の `#NUM`

```text
- [ ] #124 Extract the parser
- [ ] #125 Add the --format flag
- [ ] other/repo#126 Upstream fix   <- skipped: same-repo references only
```

タスクリスト参照は同一 repo 内のみで、`owner/repo#NUM` 形式はスキップされます。本文由来の番号は `gh issue view` で本体情報を引きます。子 issue はどちらか一方のソースでも両方でも宣言でき（和集合なので重複は除かれます）、`state == "OPEN"` の子だけが処理されます。CLOSED の子にペインが作られることはありません。

## 実行時に起きること

live 実行は次のステップで進みます:

1. `gh`、`git`、`tmux` がインストールされているかを確認する。
2. `git rev-parse --show-toplevel` でリポジトリルートを、現在の tmux セッションと起動元 pane を、`--agent` または `FANOUT_AGENT` から agent を解決する。
3. Sub-issues と親タスクリスト行の和集合で子を列挙する（OPEN の子のみ処理）。
4. `.fanout/state.json` を読み、`(parent, issueNum)` ペアが記録済みの子はスキップする。
5. 各対象 issue について、issue 本文と短い Requirements チェックリストからなる briefing を `/tmp/fanout-<repo>-<NUM>.md` に書き出す。
6. fresh 化した base branch から `.fanout/worktrees/<slug>/` を作成し、`tmux split-window` で子ペインを選択せずに作り、ペインタイトルを設定して `tmux select-layout tiled` を適用し、次の子に進む前に `--sleep` 秒（既定 4）スリープする。
7. created / skipped / deferred / failed の件数サマリを表示する。

> pane 起動 prompt が短いのは意図的です。完全な issue 本文は `/tmp/fanout-<repo>-<NUM>.md` の briefing にあり、agent はそれを読むよう指示されます。`deferred` は `--unblocked-only` 指定時のみ現れ、OPEN な blocker が残る子を保留します。

## 再実行しても安全（冪等性）

`.fanout/state.json` は起動済みの pane を `(parent, issueNum)` キーで記録するため、同じ親での再実行では記録済みの子をスキップし、足りない分だけを作成します:

```bash
# Rerun after the first batch — already-recorded children are skipped
fanout 123
```

state 行の無い既存の `.fanout/worktrees/<slug>/` directory も、ファンアウト済みとして扱われます — pre-state run や中断された launch のための移行用 fallback です。

## agent の指定

agent 名が解決できることが必須です。`--agent claude` / `--agent codex` を渡すか、`FANOUT_AGENT` を設定してください。

```bash
fanout 123 --agent claude
fanout 123 --agent codex
export FANOUT_AGENT=claude   # applies to every following run in this shell
```

未知の agent はペイン作成前に失敗し、live 実行では選択された agent CLI がインストール済みかも確認します。fanout は tmux セッション内から実行する必要があります — 子ペインは `tmux split-window` で直接作成され、`--session` で別セッションを指定しない限り起動元 pane が target になります。唯一の例外は TUI コンソール（引数なしの `fanout`）で、素のシェルからも起動できます — [モニタリング]({{< relref "/docs/monitoring" >}})を参照してください。

次は[ワークフロー]({{< relref "/docs/workflow" >}})で、run を形作る flag を見ていきます。
