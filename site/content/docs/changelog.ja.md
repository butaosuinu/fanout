---
title: 変更履歴
linkTitle: 変更履歴
description: "各リリースの変更点 — 新しい順、ドキュメントへのリンク付き。"
weight: 90
kanji: 録
yomi: changelog
---

リリースのハイライトを新しい順に並べています。各タグには完全なコミット一覧とビルド済みバイナリ（darwin / linux × amd64 / arm64）を含む [GitHub release](https://github.com/butaosuinu/fanout/releases) があります。バージョンは git タグから ldflags 経由で埋め込まれます — `fanout --check-update` で自分の版を確認できます。

## Unreleased

- **TUI shell terminal。** 常駐コンソールで、選択行の worktree に `A`、project root に `t` で plain shell を開けます。shell 行は focus / peek 用に manual entry として記録され、close は tmux pane と state 行だけを消します。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **dry-run の整理。** issue / Project と `fanout plan` の dry-run は pane 作成前の安全な preview として残しつつ、issue / Project dry-run は `/tmp` briefing file を書かなくなりました。未使用だった `fanout msg --dry-run` surface は削除されました。

## v0.6.0 — 2026-06-15

- **issue-less plan の peer messaging（`fanout plan --team`）。** plan レーンが `--team` に対応し、issue / Project レーンと同じ兄弟ペイン協調を `fanout plan` に組み込みました。issue-less な plan task には GitHub issue 番号が無いため、peer は **task id** で指定します —— `fanout msg send --to <task-id>`、`fanout msg peers` が現在の task id 一覧を表示します。plan のバスは `/tmp/fanout-<repo>-plan-<slug>.db` に置かれます。[Workflow]({{< relref "/docs/workflow" >}}) を参照。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.6.0)

## v0.5.0 — 2026-06-14

- **Per-target agent overrides（ターゲット別 agent 上書き）。** 素の `--agent <name>` は引き続き全ての子の既定を設定しますが、繰り返し可能な `--agent <NUM>=<name>`（issue / Project の子）や `--agent <task-id>=<name>`（`fanout plan`）で 1 回の run の中に agent を混在させられるようになりました。各ターゲットはまず一致する上書き、次に global `--agent`、最後に `FANOUT_AGENT` の順に解決し、検証は実際に選択された agent のみに行います。[CLI Reference]({{< relref "/docs/cli" >}}) を参照。
- **複数行対応の TUI セッション modal。** 常駐コンソールで `n` を押すと modal が開き、複数行の prompt、`claude` / `codex` の選択、任意 slug を入力できます。`Shift+Enter` で改行（区別できない terminal 向けの fallback として `Ctrl+J`）、`Enter` で pane を作成します。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.5.0)

## v0.4.0 — 2026-06-14

fanout を、独自のコンソール・ダッシュボード・計画レーンを備えた単体ツールへと作り変えた大型リリースです。

- **単体ランタイム + 常駐 TUI コンソール。** dmux 依存を外して `tmux` を直接制御し、引数なし `fanout` のコンソールに pane フォーカス / 出力 peek、ライフサイクル操作、wave & blocker 列、manual agent pane、メモリ内の検索 / フィルタを追加しました。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **読み取り専用 Web ダッシュボード。** `127.0.0.1` バインド・GET 専用・token ゲート付きの Session ビューを、PAPER BREEZE テーマの React + Vite + TypeScript SPA として作り直し、詳細 drawer、plan モードの提案プラン表示、未 fan-out の子の synthetic 行を備えました。`fanout dashboard --web` で起動します。
- **issue を作らない `fanout plan`。** GitHub の子 issue ではなくローカルの JSON plan spec を fan out します。task ID、`blocked_by` 依存の wave、task ライフサイクル（`--status` / `--merge` / `--close` / `--cleanup`）に対応します。[CLI Reference]({{< relref "/docs/cli" >}}) を参照。
- **Peer messaging（`--team` / `fanout msg`）。** parent ごとの SQLite バスを介した opt-in の兄弟協調と、best-effort の `nudge` を追加しました。[Workflow]({{< relref "/docs/workflow" >}}) を参照。
- **Codex Plan Mode + review skill。** `--codex-plan-mode` で Codex の子を interactive Plan-Mode TUI セッションとして起動できるようになり、両 agent 向けに `post-work-review` / `pr-watch` skill を同梱しました。[Agent Integrations]({{< relref "/docs/agents" >}}) を参照。
- **ドキュメントサイト・前提の軽量化。** この Hugo サイト（英語 / 日本語）を公開し、`jq` と `gh-sub-issue` の前提を公式 GitHub Sub-issues API へ置き換え、Go ツールチェーンを golangci-lint v2 ベースに刷新しました。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.4.0)

## v0.3.0 — 2026-06-06

- **自己更新。** `fanout update` は同じ `install.sh` 経路で実行中のバイナリと同梱の Claude / Codex 連携を置き換え、`fanout --check-update` は何も変更せずに最新リリースとバイナリを比較します。[CLI Reference]({{< relref "/docs/cli" >}}) を参照。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.3.0)

## v0.2.0 — 2026-06-04

- **Settings 機構。** opinionated な子 briefing の挙動を切り替え可能にし、CLI フラグ・`FANOUT_*` 環境変数・config ファイルにまたがって解決するようにしました。[Settings]({{< relref "/docs/settings" >}}) を参照。
- **Go 一本化。** レガシーな Bash 実装を削除して Go CLI に統一し、子 briefing に Codex review ゲートを追加、fanout skill が OPEN な issue / Project 候補を提示するようにしました。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.2.0)

## v0.1.0 — 2026-06-04

- **最初の Go リリース。** 並列 Go 移植版を既定のインストール対象とし、ビルド済みリリース配布、`--status` JSON レポーター、Projects v2 モード、`gh pr create` 前にレビューを強制する PreToolUse ゲート、旧 Bash エントリポイントの deprecation 通知を備えました。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.1.0)
