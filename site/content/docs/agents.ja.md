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

Codex には repo 管理の skill 5 個を `~/.codex/skills/` 配下へインストールします([インストール]({{< relref "/docs/installation" >}}) を参照)。
各 skill は主な判断手順を `SKILL.md` に置き、必要なときだけ同梱の reference や script を読み込みます。

fanout skill は「#123 を fan out して」のように依頼するか、明示的に `$fanout` を指定すると起動します。
Claude の `/fanout` と同じ安全フロー(dry-run → ターゲット確認 → 本実行)をたどります。
`fanout-issues`、`fanout-plan`、`post-work-review`、`pr-watch` も Codex 版として同梱されており、`$fanout-issues` や `$pr-watch` のように呼び出すと Claude 版と同じ役割を果たします。

`$post-work-review` は `fork_turns: "none"` を指定し、通常の Codex native subagent を fresh な広域レビューとして起動します。
親は自然言語の指摘を解釈します。
修正で対象が変わった場合は、別の fresh subagent が新しい対象全体を広域レビューします。
custom agent、model 固定、app-server controller、result parser は使いません。

reviewer には対象 repository path と diff 範囲を渡すため、repository の内容が Codex model へ送信されます。
marker helper は spawn 前に、適用対象の `AGENTS.md`、`AGENTS.override.md`、repository の `.codex` files が trusted merge base から変わっていないことを検証します。
base と同一の instruction は trusted repository conventions として扱い、それ以外の target file と directive は untrusted review evidence として扱います。
helper は `post-work-review` gate の変更も拒否します。
root の既定 makefile と `install.sh` の変更も拒否します。
installed skill package と helper は、review 対象 repository 外に置いた symlink ではない copy が必要です。
checksum 検証付き release installer だけがこれらを配置、置換、削除します。
checkout の make target は Codex review gate を変更しません。
いずれかの Codex root に旧 driver が残る場合、install と link は停止します。
instruction、gate、gate installer の変更は trusted checkout から起動した reviewer、または人がレビューしてください。
native subagent は親 session の sandbox、approval policy、network 制限を継承します。
skill は編集、approval 要求、network 使用を禁止しますが、子だけを厳しい sandbox にはできません。
強制された read-only が必要なら、Codex を read-only で開始してから実行してください。
この信頼境界、native spawn、wait のいずれかを維持できない場合は fallback せず停止します。

helper は filesystem 間で同じ境界を保つため、instruction と gate の path を case-insensitive で照合します。
symlink の `AGENTS.md` / `AGENTS.override.md`、case variant または nested `.codex` path、`model_instructions_file` または `project_doc_fallback_filenames` を定義した project config、escape を含む project config key、commit 済みまたは worktree の submodule 変更を拒否します。
checkout 済みの submodule も拒否します。
clean かつ base と同一の submodule は、review 前に deinitialize してください。
これらの target は trusted checkout から起動した reviewer、または人がレビューしてください。

dirty worktree は review-only scope です。
reviewer は staged、unstaged、untracked の変更を確認し、親は focused checks だけを実行します。
この scope では marker を書きません。PR gate には candidate を commit してから再実行してください。
submodule 変更は review 前に fail-closed で停止します。

clean な committed branch では、skill は repository の canonical validation を 1 回実行し、clean な exact HEAD、PR base commit、diff hash を記録します。
commit、base の移動、review diff の変更で marker は無効です。

`$pr-watch` は foreground で動きます。
helper は変化のない snapshot の出力を省き、linked worktree でも cursor を Git metadata に保存します。
Codex セッション終了後に background watcher は残りません。

## Codex Plan Mode

通常の issue / Project の子 fan-out は、起動前に `codexPlanMode` 設定を解決します。
user config または repo config に保存するか、`FANOUT_CODEX_PLAN_MODE` を設定します。
CLI の 1 run だけ上書きするには `--codex-plan-mode` / `--no-codex-plan-mode` を使います。
ビルトイン既定値は `false` で、優先順位は CLI > env > repo > user > default です。
TUI の設定 popup では Launch グループに同じキーが表示されます。

```bash
fanout 123 --agent codex --codex-plan-mode
```

Plan Mode の子は interactive な Codex TUI として起動し、関連する文脈を調査したうえで `<proposed_plan>` に包んだ実装計画を提示します。
その turn ではファイル編集や commit、push、PR 作成をしません。
ペインは Plan Mode の会話のまま残るので、そこから続行できます。

この設定は通常の CLI issue / Project fan-out と、OPEN な子を持つ issue を選んだ TUI Issue モードに適用します。
選択された子はすべて `codex` に解決される必要があります。
`claude` の子が混ざっているとペイン作成前に失敗します。

watcher、子のない issue の単独ペイン、`fanout plan` task、plan coordinator はこの設定を無視します。
manual と attach の `codex` ペインは、この設定に関係なく従来どおり Plan Mode で起動します。
対応する `claude` ペインは通常モードで起動します。

## OpenCode

OpenCode(`opencode`)は同梱 skill のない子 agent として使えます。`--agent opencode` を渡すか、`--agent NUM=opencode` / `--agent task-id=opencode` で対象ごとに混在させてください。
opencode の位置引数はプロジェクトパスなので、fanout は起動プロンプトを `--prompt` フラグの値として渡します。ペインの resume には `opencode --continue` を使います。
OpenCode はリポジトリの `AGENTS.md` をネイティブに読むため、子ペインは追加のセットアップなしでプロジェクトのルールを拾います。
briefing には base の requirements のみが入り、Claude 専用・Codex 専用のセクションは付きません。

## briefing の仕組み

issue や plan task の子ペインに送られるのは 1 行のプロンプトだけです。
issue や task の本文と短い Requirements チェックリストは `.fanout/briefings/fanout-<repo>-<NUM>.md` または task 用の briefing path に書き出され、起動プロンプトはそのファイルを読むよう agent に伝えるだけです。
どの指示を briefing に含めるかは [Settings]({{< relref "/docs/settings" >}}) のトグルで変わります。
