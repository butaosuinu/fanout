---
title: Claude Code・Codex 連携
linkTitle: エージェント連携
description: "Claude Code / Codex 向けの /fanout スラッシュコマンドと fanout・fanout-issues skill、そして Codex Plan Mode。"
weight: 70
kanji: 連
yomi: agents
---

## エージェントセッションの中から呼ぶ

fanout は、tmux の中で動いている agent セッション(Claude Code、Codex など)から呼び出しても安全です。作るのは子用の新規 pane だけで、呼び出し元の pane には一切触れません。子 pane がどの agent CLI を起動するか分かるよう、`--agent` を渡すか `FANOUT_AGENT` を設定してください。

CLI の前提条件はそのまま適用されます: tmux 内で実行すること、そして子を分岐させたいリポジトリの中で実行することです。以下の連携ファイルはリポジトリに同梱されており、[インストールスクリプト]({{< relref "/docs/installation" >}})が配置します。

## Claude Code

### `/fanout` スラッシュコマンド

`~/.claude/commands/fanout.md` がスラッシュコマンドをインストールします:

```text
/fanout [parent-issue] [--go] [extra fanout flags]
```

まず `fanout <N> --dry-run` を実行してターゲット一覧を表示し、ユーザーが確認した後にのみ本物のコマンドを実行します — `--go` が渡されたときは確認をスキップして即実行します。

### `fanout` skill

`~/.claude/skills/fanout/` は、fanout が適用できる場面を agent が認識し、勝手に実行せず `/fanout` を提案するよう働きます。それに加えてこの skill は、CLI 本体がパースしない**暗黙の子参照** — `Closes #N` などのクローズキーワード、`Depends on #N` のような依存表現、素の箇条書き、`#N に関連` のような日本語の慣用句 — を親本文から読み取り、候補をユーザーに提示して、承認された番号を `--include` で fanout に渡します。

skill は `--name` フラグ(slug / display name / branch)も issue のタイトルと本文から生成します。CLI 自体は LLM を呼ばない設計で、命名の知性は skill 側にあります。

### `fanout-issues` skill

`~/.claude/skills/fanout-issues/` は、計画を fanout-ready な GitHub 親 issue + リンクされた子 issue 群へ変換する場面で agent を導きます。同一 repo 内の子 issue を作成し、GitHub Sub-issues でリンクし、親本文のタスクリストにミラーし、`fanout --unblocked-only` が読める `## Blocked by` / `(blocked by #N)` 形式で blocker wave も記録します。

## Codex CLI

Codex 版の skill は `~/.codex/skills/fanout/` と `~/.codex/skills/fanout-issues/` に配置されます。skill のインストールや更新の後、実行中の Codex セッションがある場合は再起動すると新しいファイルを認識します。

fanout skill は、Codex に「#123 を fan out して」のように依頼するか、明示的に `$fanout` を指定すると起動します。Claude のコマンドと同じ安全フロー — まず dry-run、ターゲットを確認、それから本実行 — に従い、暗黙の子参照のスキャンと `--name` 生成も同様に行います。

`fanout-issues` skill も Claude 版をミラーします: fanout-ready な GitHub issue ツリーの作成、計画の親子 issue 化、`fanout --unblocked-only` 用の blocker wave の準備を Codex に依頼したときに使われ、同一 repo の子 issue、GitHub Sub-issues のリンク、親本文のタスクリスト、`## Blocked by` 注記を同じように揃えます。

## Codex Plan Mode

`--codex-plan-mode` は `--agent codex` 専用の opt-in 起動モードです:

```bash
fanout 123 --agent codex --codex-plan-mode
```

通常の positional `codex "<prompt>"` ではなく、fanout は子ごとに Codex app-server を起動し、collaboration mode `plan` の thread を作成し、fanout プロンプトを app-server 経由で初回 turn として開始してから、その remote セッションに interactive Codex TUI を attach します。

子 briefing も Plan Mode 向けに差し替わります: `<proposed_plan>` に包んだ実装計画を出すこと、最初の turn ではファイル編集・commit・push・PR 作成をしないことを明示します。

この経路では tmux 経由で `/plan` やプロンプトのテキストを送信しません。pane は interactive Codex TUI セッションのまま残るため、その Plan Mode の会話から続行できます。app-server の Plan turn セットアップまたは TUI attach に失敗した場合は、state 記録前に launch を失敗扱いにし、pane / worktree を後始末するため、同じ子を再実行できます。

## briefing の仕組み

子 pane に送られるのは 1 行のプロンプトだけです。issue の完全な本文と短い Requirements チェックリストは `/tmp/fanout-<repo>-<NUM>.md` に書き出され、起動プロンプトは agent にその briefing ファイルを読むよう短く伝えます。

briefing の内容は、解決済みの settings — `autoPullRequest`・`prReviewGate`・`briefingCodeReview`・`agentTeamsHint`・`prVisualization` — でフィルタされます。CLI フラグ・環境変数・config ファイルがどう解決されるかは [Settings]({{< relref "/docs/settings" >}}) を参照してください。
