---
title: エージェント連携
linkTitle: エージェント連携
description: "Claude Code と Codex 向けの同梱 skill、/fanout スラッシュコマンド、Codex Plan Mode。"
weight: 70
kanji: 連
yomi: agents
---

## エージェントセッションの中から呼ぶ

あるペインで Claude Code や Codex を動かしながら、その場で子へ fanout したいことがあります。
fanout は tmux の中で動く agent セッションから呼び出しても安全です。
作るのは子用の新規ペインだけで、呼び出し元のペインには触れません。

子ペインが起動する agent CLI を指定するには、`--agent` を渡すか `FANOUT_AGENT` を設定します。
1 回の run で agent を混在させたいときは、issue や Project の子に `--agent NUM=name`、`fanout plan` の task に `--agent task-id=name` の per-target 上書きを繰り返し指定します。
上書きの解決順は [CLI Reference]({{< relref "/docs/cli" >}}) にまとめています。

## Claude Code

### `/fanout` スラッシュコマンド

会話の中から fanout を呼びたいが、本実行の前にターゲットを確認したいことがあります。
`~/.claude/commands/fanout.md` が次のスラッシュコマンドをインストールします。

```text
/fanout [parent-issue] [--go] [extra fanout flags]
```

まず `fanout <N> --dry-run` でターゲット一覧を表示し、確認したあとにだけ本実行に進みます。
`--go` を渡すと確認をスキップして即実行します。

### `fanout` skill

親 issue の本文に子への参照が散らばっていて、番号を手で拾い集めるのは面倒なことがあります。
`~/.claude/skills/fanout/` はその参照を読み取って候補を提示し、承認された番号を `--include` に渡したうえで `/fanout` の実行を提案する skill です。

### `fanout-issues` skill

計画を fanout にかけられる issue ツリーに落とし込みたいことがあります。
`~/.claude/skills/fanout-issues/` は、同一 repo での子 issue 作成と GitHub Sub-issues でのリンク、親本文のタスクリストへのミラー、`fanout --unblocked-only` 用の blocker wave 記録までを担う skill です。

### `fanout-plan` skill

GitHub の子 issue を作らず、手元の実装計画をそのまま fanout にかけたいことがあります。
`~/.claude/skills/fanout-plan/` は計画を `fanout plan` の JSON spec に変換し、dry-run で確認したうえで issue-less な task ペインを起動する skill です。

### レビューと PR follow-up の skill

実装が一段落し、コミットや PR 作成の前にもう一度見直したいことがあります。
`~/.claude/skills/post-work-review/` はローカルの PR review gate を支え、最終レビュー loop を回します。
PR を作ったあとの追従には `~/.claude/commands/pr-watch.md` と `~/.claude/skills/pr-watch/` を使い、コンフリクトや CI、レビューコメントへの対応を見張ります。

## Codex CLI

Codex にも `~/.codex/skills/` 配下に同じ skill が用意されています([インストール]({{< relref "/docs/installation" >}}) を参照)。

fanout skill は「#123 を fan out して」のように依頼するか、明示的に `$fanout` を指定すると起動します。
Claude の `/fanout` と同じ安全フロー(dry-run → ターゲット確認 → 本実行)をたどります。
`fanout-issues`、`fanout-plan`、`post-work-review`、`pr-watch` も Codex 版として同梱されており、`$fanout-issues` や `$pr-watch` のように呼び出すと Claude 版と同じ役割を果たします。

## Codex Plan Mode

子を起動する前に、Codex に実装計画を提案させてから先に進めたいことがあります。
batch の子起動では `--codex-plan-mode` でこの Plan Mode を有効にできます(既定は off)。

```bash
fanout 123 --agent codex --codex-plan-mode
```

Plan Mode の子は interactive な Codex TUI として起動し、関連する文脈を調査したうえで `<proposed_plan>` に包んだ実装計画を提示します。
その turn ではファイル編集や commit、push、PR 作成をしません。
ペインは Plan Mode の会話のまま残るので、そこから続行できます。

選択された子はすべて `codex` に解決される必要があります。
`claude` の子が混ざっているとペイン作成前に失敗します。

## briefing の仕組み

issue や plan task の子ペインに送られるのは 1 行のプロンプトだけです。
issue や task の本文と短い Requirements チェックリストは `.fanout/briefings/fanout-<repo>-<NUM>.md` または task 用の briefing path に書き出され、起動プロンプトはそのファイルを読むよう agent に伝えるだけです。
どの指示を briefing に含めるかは [Settings]({{< relref "/docs/settings" >}}) のトグルで変わります。
