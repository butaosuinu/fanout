---
title: エージェント連携
linkTitle: エージェント連携
description: "Claude Code と Codex 向けの同梱 skill、/fanout スラッシュコマンド、Codex Plan Mode。"
weight: 70
kanji: 連
yomi: agents
---

## エージェントセッションの中から呼ぶ

あるペインの中で Claude Code や Codex を動かしながら、その場で子へ fanout したいときがあります。fanout は tmux の中で動いている agent セッションから呼び出しても安全です。作るのは子用の新規ペインだけで、呼び出し元のペインには一切触れません。

子ペインがどの agent CLI を起動するかを fanout に伝えるには、`--agent` を渡すか `FANOUT_AGENT` を設定してください。1 回の run で agent を混在させたいときは、繰り返し可能な per-target 上書きを足します。issue や Project の子には `--agent NUM=name`、`fanout plan` では `--agent task-id=name` を使います。各ターゲットはまず一致する上書き、次に global の `--agent`、最後に `FANOUT_AGENT` の順で解決します。解決順の詳細は [CLI Reference]({{< relref "/docs/cli" >}}) を参照してください。

CLI の前提条件はそのまま適用されます。tmux の中で実行すること、そして子を分岐させたいリポジトリの中で実行することです。以下の連携ファイルはリポジトリに同梱されており、[インストールスクリプト]({{< relref "/docs/installation" >}})が配置します。

## Claude Code

### `/fanout` スラッシュコマンド

会話の中から fanout を呼びたいが、本実行の前にターゲットを確認したいときがあります。`~/.claude/commands/fanout.md` が次のスラッシュコマンドをインストールします。

```text
/fanout [parent-issue] [--go] [extra fanout flags]
```

このコマンドはまず `fanout <N> --dry-run` を実行してターゲット一覧を表示し、ユーザーが確認したあとにのみ本物のコマンドを実行します。`--go` を渡したときは確認をスキップして即実行します。

### `fanout` skill

親 issue の本文に子への参照が散らばっていて、手で番号を拾い集めるのが面倒なことがあります。`~/.claude/skills/fanout/` は、その拾い集めを agent に肩代わりさせる skill です。fanout が使える場面を agent が認識し、勝手に実行せず `/fanout` を提案します。

それに加えてこの skill は、CLI 本体がパースしない**暗黙の子参照**を親本文から読み取ります。読み取るのは `Closes #N` などのクローズキーワード、`Depends on #N` のような依存表現、素の箇条書き、`#N に関連` のような日本語の慣用句です。skill は候補をユーザーに提示し、承認された番号を `--include` で fanout に渡します。

skill は `--name` フラグ(slug、display name、branch)も issue のタイトルと本文から生成します。CLI 自体は LLM を呼ばない設計で、命名は skill 側が判断します。

### `fanout-issues` skill

計画を fanout にかけられる形の issue ツリーへ落とし込みたいときがあります。`~/.claude/skills/fanout-issues/` は、その変換で agent を導く skill です。同一 repo 内の子 issue を作成し、GitHub Sub-issues でリンクし、親本文のタスクリストにミラーします。さらに `fanout --unblocked-only` が読める `## Blocked by` や `(blocked by #N)` の形式で blocker wave も記録します。

### `fanout-plan` skill

GitHub の子 issue を作らず、手元の実装計画をそのまま fanout にかけたいときがあります。`~/.claude/skills/fanout-plan/` は `/fanout plan` を支える skill です。承認済みまたはローカルの実装計画を `fanout plan` の JSON spec に変換し、dry-run preview を実行して task や wave、branch を要約し、確認後に issue-less な task ペインを起動します。

### レビューと PR follow-up の skill

実装が一段落し、コミット前や PR 作成前にもう一度見直したいときがあります。`~/.claude/skills/post-work-review/` はローカルの PR review gate を支え、最終レビュー loop を回して reviewed HEAD marker を記録します。

PR を作ったあとの追従には別の skill を使います。`~/.claude/commands/pr-watch.md` と `~/.claude/skills/pr-watch/` は、PR 作成後のコンフリクトや CI、レビューコメントへの対応を安全に見張って処理します。

## Codex CLI

Codex 版の skill は `~/.codex/skills/fanout/`、`~/.codex/skills/fanout-issues/`、`~/.codex/skills/fanout-plan/`、`~/.codex/skills/post-work-review/`、`~/.codex/skills/pr-watch/` に配置されます。skill のインストールや更新のあと、実行中の Codex セッションがあれば再起動すると新しいファイルを認識します。

fanout skill は、Codex に「#123 を fan out して」のように依頼するか、明示的に `$fanout` を指定すると起動します。Claude のコマンドと同じ安全フローに従い、まず dry-run、次にターゲットの確認、それから本実行へ進みます。暗黙の子参照のスキャンと `--name` 生成も同様に行います。

`fanout-issues` skill も Claude 版をミラーします。fanout にかけられる GitHub issue ツリーの作成、計画の親子 issue 化、`fanout --unblocked-only` 用の blocker wave の準備を Codex に依頼したときに使われ、同一 repo の子 issue、GitHub Sub-issues のリンク、親本文のタスクリスト、`## Blocked by` 注記を同じように揃えます。

GitHub の子 issue を作らずローカルの実装計画を fan out したいときは、`$fanout-plan` または `fanout plan` の依頼を使います。spec を作成または選択し、`fanout plan ... --dry-run` を preview してから、確認後に live 実行します(確認スキップが明示された場合を除く)。

`$post-work-review` は、Codex にコミット前や PR 作成前の最終レビュー loop を依頼するときに使います。明示 scope 付きの `codex review` を実行し、actionable な指摘を修正し、clean になるまで再レビューします。HEAD が clean なら Claude の PR gate と同じ marker も記録します。

`$pr-watch` は、PR 作成後に mergeability や失敗 CI、レビューコメントを Codex に確認させて修正させたいときに使います。Codex は background scheduler を持たないため、green、reviewer 待ち、CI 待ち、blocked のどの状態かを報告して止まります。

## Codex Plan Mode

子を起動する前に、Codex に実装計画を提案させてから先へ進めたいときがあります。batch の子起動では `--codex-plan-mode` で Plan Mode を有効にします。これは(per-target の `--agent` 上書きを適用したあとに)`codex` へ解決される子向けの opt-in 起動モードです。選択した全ての子が `codex` へ解決される必要があり、`claude` の子が混ざっていると起動前に拒否されます。

```bash
fanout 123 --agent codex --codex-plan-mode
```

通常の positional な `codex "<prompt>"` ではなく、fanout は子ごとに Codex app-server を起動し、collaboration mode `plan` の thread を作成し、fanout プロンプト付きで interactive な Codex TUI から resume します。

子 briefing も Plan Mode 向けに差し替わります。関連する文脈を調査してから `<proposed_plan>` に包んだ実装計画を出すこと、その turn ではファイル編集や commit、push、PR 作成をしないことを明示します。TUI の manual ペイン popup で `codex` を起動する場合も、同じ Plan Mode 経路を自動で使いますが、popup の prompt と Plan Mode 指示は `/tmp` briefing file ではなく inline prompt として渡します。

この経路では tmux 経由で `/plan` やプロンプトのテキストを送信しません。ペインは interactive な Codex TUI セッションのまま残るため、その Plan Mode の会話から続行できます。Plan Mode thread のセットアップまたは TUI attach に失敗した場合は、state 記録前に launch を失敗扱いにし、ペインと worktree を後始末するため、同じ子を再実行できます。

## briefing の仕組み

issue または plan task の子ペインに送られるのは 1 行のプロンプトだけです。issue や task の完全な本文と短い Requirements チェックリストは `/tmp/fanout-<repo>-<NUM>.md` または task briefing path に書き出され、起動プロンプトは agent にその briefing ファイルを読むよう短く伝えます。

briefing の内容は、解決済みの settings でフィルタされます。対象は `autoPullRequest`、`prReviewGate`、`briefingCodeReview`、`agentTeamsHint`、`prVisualization` です。CLI フラグや環境変数、config ファイルがどう解決されるかは [Settings]({{< relref "/docs/settings" >}}) を参照してください。
