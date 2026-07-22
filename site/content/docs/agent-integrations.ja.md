---
title: エージェント連携
linkTitle: エージェント連携
description: "対応エージェントの比較、agent ごとの Plan Mode の挙動、Claude Code と Codex 向けの同梱 skill、/fanout スラッシュコマンド、OpenCode。"
weight: 80
slug: agents
kanji: 連
yomi: agents
---

## 対応エージェント

子ペインを起動できるエージェント CLI は 3 つです。
ファンアウトの仕組み(1 子 = 1 worktree = 1 ペイン = 1 briefing)は共通で、違いはセッションの周辺に現れます。

| | Claude Code | Codex CLI | OpenCode |
|---|---|---|---|
| `--agent` 名 | `claude` | `codex` | `opencode` |
| 同梱 skill | `/fanout` + skills | `$fanout` + skills | なし |
| `--team` の push 配信 | ✓ Monitor tool 下の `fanout msg watch` | ✓ fresh な非 Plan セッション(app-server bridge) | —(pull のみ) |
| `fanout msg nudge` の対象 | ✓ | ✓ | —(スキップ) |
| briefing の構成 | base + Claude 専用 | base + Codex 専用 | base + 共通検証のみ |

push と nudge の行の挙動は [CLI リファレンス]({{< relref "/docs/cli#fanout-msg" >}})にまとめています。

## Plan Mode

どのレーンが session を agent の plan mode で始めるかは、[Settings]({{< relref "/docs/settings" >}}) の 3 つの launch posture 設定(`newSessionPlanMode` / `orchestratorPlanMode` / `childPlanMode`)が決めます。
mode は agent ごとに次のように対応します。

| | Plan Mode | build mode |
|---|---|---|
| Claude Code | `--permission-mode plan` | `--permission-mode auto` |
| Codex CLI | app-server 経由の Codex Plan Mode TUI | 素の `codex` |
| OpenCode | `--agent plan` | `--agent build` |

Claude の明示 mode 起動には Claude Code v2.1.207 以降が必要です。
fanout は起動前に `claude --version` を確認し、floor 未満または判定不能なら警告して mode フラグを省き、claude 自身の既定姿勢で起動します。
`--permission-mode auto` は v2.1.207 以降なら全プロバイダで使えますが、旧バージョン、Team / Enterprise プランで Owner が未有効化、非対応モデル、managed policy のいずれかで無効になります。
無効環境でも起動は失敗しません。claude が通知を出して `default` mode にフォールバックします。
fanout は実効 mode を検出しません — 権限プロンプトは TUI の `blocked` 状態として現れます。

Codex の Plan Mode は app-server 経由の plan TUI controller で動きます。
ダッシュボードの Plan セクションが plan 出力を capture するのは codex の plan ペインだけです。claude / opencode の plan 出力に capture できるフォーマットはありません。
Plan Mode は `--team` より優先されます — codex の plan 子は最小の Plan briefing のまま、team bridge は付きません。

OpenCode の plan / build は `--agent plan` / `--agent build` に対応します。
OpenCode は plan fan-out coordinator になれないため、coordinator レーンの plan 配線は claude / codex だけに効きます。

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

Codex には repo 管理の skill 5 個を `~/.codex/skills/` 配下へインストールします([インストール]({{< relref "/docs/installation" >}}) を参照)。
各 skill は主な判断手順を `SKILL.md` に置き、必要なときだけ同梱の reference や script を読み込みます。

fanout skill は「#123 を fan out して」のように依頼するか、明示的に `$fanout` を指定すると起動します。
Claude の `/fanout` と同じ安全フロー(dry-run → ターゲット確認 → 本実行)をたどります。
`fanout-issues`、`fanout-plan`、`post-work-review`、`pr-watch` も Codex 版として同梱されており、`$fanout-issues` や `$pr-watch` のように呼び出すと Claude 版と同じ役割を果たします。

### `$post-work-review` ゲート

`$post-work-review` は fresh な native Codex subagent を 1 つ起動し、対象全体を広域レビューさせます。
親はその指摘を自然言語のまま解釈し、修正で対象が変わったら新しい対象をまた fresh にレビューし直します。
reviewer には repository path と diff 範囲を渡すため、repository の内容は Codex model へ送信されます。

ゲートは自分自身の信頼境界を守ります。
spawn 前に marker helper が、適用対象の `AGENTS.md` / `AGENTS.override.md` と repository の `.codex` bootstrap files が trusted merge base から変わっていないことを検証し、ゲート自身(`post-work-review` のファイル、root の既定 makefile、`install.sh`)への変更を含む candidate を拒否します。
インストール済みの skill と helper を配置・置換・削除できるのは checksum 検証付きの release installer だけで、checkout の make target は触れません。
helper は追跡できないものに対して fail-closed です — symlink や case 違いの instruction ファイル、instruction の場所を変える project config、submodule の変更(clean な submodule は先に deinitialize してください)。
拒否された対象は trusted checkout から、または人がレビューしてください。

subagent は親セッションの sandbox と approval policy を継承します。
read-only を強制したいときは、Codex 自体を read-only で開始してください。
dirty worktree はレビュー専用の pass になり、PR gate の marker は書きません — candidate を commit してから再実行してください。
clean な committed branch では canonical validation を 1 回実行し、exact HEAD・PR base・diff hash を記録します。
その後の commit、base の移動、diff の変化で marker は無効になります。

`$pr-watch` は foreground で動き、変化のない snapshot は出力せず、cursor を Git metadata に保存します(linked worktree でも同様)。
Codex セッションの終了後に background watcher は残りません。

## OpenCode

OpenCode(`opencode`)は同梱 skill を持たない子 agent です。
`--agent opencode` を渡すか、`--agent NUM=opencode` / `--agent task-id=opencode` で対象ごとに混在させます。

### 起動と resume

opencode の位置引数はプロジェクトパスなので、fanout は起動プロンプトを `--prompt` フラグの値として渡します。
ペインの resume には `opencode --continue` を使います。

### プロジェクトルールと briefing

OpenCode はリポジトリの `AGENTS.md` をネイティブに読むため、子ペインは追加のセットアップなしでプロジェクトのルールを拾います。
briefing には base の requirements と共通の最終検証手順が入り、Claude 専用・Codex 専用のセクションは付きません。
fanout の Claude 連携が入れる `/fanout` コマンドは Claude Code 専用です。
OpenCode の Claude Code 互換が読むのは `CLAUDE.md` と `~/.claude/skills/` で、`~/.claude/commands` は読みません。
TUI の plan fan-out coordinator には `claude` か `codex` を選んでください。

### メッセージングは pull ベース

`fanout msg nudge` は opencode ペインを対象から外し、push 配信レーンもありません。
`--team` でも、opencode の sibling は自分のチェックポイントで bus を読みます(`inbox` / `board`)。
nudge の除外は意図的な設計です。opencode ペインは `running` より細かいペイン状態を報告しないため、queue された入力を安全に送れるタイミングを fanout が判定できません。

## briefing の仕組み

issue や plan task の子ペインに送られるのは 1 行のプロンプトだけです。
issue や task の本文と短い Requirements チェックリストは `.fanout/briefings/fanout-<repo>-<NUM>.md` または task 用の briefing path に書き出され、起動プロンプトはそのファイルを読むよう agent に伝えるだけです。
どの指示を briefing に含めるかは [Settings]({{< relref "/docs/settings" >}}) のトグルで変わります。
