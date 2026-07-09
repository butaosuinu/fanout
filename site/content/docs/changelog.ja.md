---
title: 変更履歴
linkTitle: 変更履歴
description: "各リリースの変更点。新しい順で、ドキュメントへのリンク付き。"
weight: 90
kanji: 録
yomi: changelog
---

リリースのハイライトを新しい順に並べています。各タグには [GitHub release](https://github.com/butaosuinu/fanout/releases) があり、完全なコミット一覧とビルド済みバイナリ（darwin / linux × amd64 / arm64）を含みます。バージョンは git タグから ldflags 経由で埋め込まれます。`fanout --check-update` で自分の版を確認できます。

## v0.10.0 (2026-07-09)

- **Agent-state テレメトリ。** `@fanout_agent_state` 契約を 6 値の語彙(`running` / `working` / `plan` / `blocked` / `idle` / `done`)に拡張しました。起動ラッパーと Codex Plan Mode が状態を発行し、TUI / web の glyph とバッジに反映されます。agent がプランを提示・入力待ち・終了したときにコンソールが通知音を鳴らします。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **TUI popup アクション。** settings popup とペインクローズの選択 popup を追加し、いずれもアクティブなペインの隣に表示します。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **TUI picker の改善。** picker から issue リンクを直接開けるようにし、コンパクト switcher の選択をフォーカス中のペインと同期させました。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **PR review-risk の機械判定。** H/M/A 正典から `review:<level>` を判定する `tools/reviewrisk` を新設しました。ローカルでは `make review-risk` で実行できます。`docs/review-risk.ja.md` を参照。
- **briefing を `/tmp` の外へ。** 子の briefing を `/tmp` から `.fanout/briefings/` へ移設しました。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.10.0)

## v0.9.0 (2026-07-06)

- **常駐コンソールの刷新。** 引数なしコンソールに、コンパクトな Session switcher(`v` で切り替え)、`1`–`9` の数字ジャンプ、`Z` zoom、AgentState 列、tmux popup のショートカットヘルプを追加し、再起動時にペインを復元するようにしました。任意のペインから `F11` または `prefix T` で復帰できます。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **新規 Session modal の強化。** `n` で Prompt と Issue の 2 モードを開けます。Prompt モードは `@`-mention ファイル補完と plan fan-out チェックボックス付きの複数行 prompt、Issue モードは親 / 子 / 単独の issue を marker 表示する GraphQL ページングの issue picker とカウント式の `claude` / `codex` 選択を備えます。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **テーマ化とペインの自動レイアウト。** ペインは dmux 相当の自動レイアウトで並び、`#parent · name` ラベル付きの fanout カラーの境界を持ちます。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **新しいレビュー skill。** 過去のセッションから再発するツールエラー・CI 失敗・レビュー指摘を掘り出す `session-retro` skill を新設し、`post-work-review` にプロジェクト検証パスとレビューチェックリストを追加しました。[Agent Integrations]({{< relref "/docs/agents" >}}) を参照。
- **4層の内部アーキテクチャ。** `internal/` を `core` / `app` / `infra` / `ui` の 4 層に再配置し、import 方向を CI で強制するようにしました。挙動は変わりません。正典は `docs/architecture.ja.md` です。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.9.0)

## v0.8.0 (2026-06-24)

- **ラベル watcher。** 有効化すると、TUI 常駐の watcher が信頼できる `fanout:auto` issue を 1 回限りの fanout session に変えます。起動前に trigger ラベルを `fanout:running` へ付け替え、OPEN な子を持つ issue を親ファンアウトとして分類し、live ペインの上限を尊重します。有効化できるのは user config か `FANOUT_WATCHER*` 環境変数だけです。repo config はラベル、間隔、子 agent、session 上限を設定できますが、checkout を起動側に切り替えることはできません。[Workflow]({{< relref "/docs/workflow" >}}) と [Settings]({{< relref "/docs/settings" >}}) を参照。
- **全 worktree を横断するダッシュボード。** Web ダッシュボードが、現在の worktree だけでなくリポジトリ内の全 worktree の Session を横断集約するようになりました。ブラウザのタブ 1 つで並列作業すべてを見渡せます。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **複数 agent の TUI 起動。** 常駐コンソールの `n` modal が、枠付きテキスト入力で agent ごとの `claude` / `codex` 起動数を指定できるようになり、`codex` ペインは Plan Mode で起動します。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.8.0)

## v0.7.0 (2026-06-21)

- **ライフサイクルフック。** worktree やペイン、merge のイベントの前後でユーザーの shell hook を実行するようになりました。`$XDG_CONFIG_HOME/fanout/hooks.json`（Codex 形式の `hooks` オブジェクト）で設定します。常時有効で、ファイルが無い場合やコマンドの無いイベントは no-op です。[Lifecycle hooks]({{< relref "/docs/cli#lifecycle-hooks" >}}) を参照。
- **TUI shell terminal。** 常駐コンソールで、選択行の worktree に `A`、project root に `t` で plain shell を開けます。shell 行は focus / peek 用に manual entry として記録され、close は tmux ペインと state 行だけを消します。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **コンパクトな Session ナビゲーター。** 常駐コンソールに、セッション間を移動するためのコンパクトな Session ナビゲーターが加わりました（既存の focus / peek / lifecycle キーはそのままです）。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **`origin` なしの `fanout plan`。** plan 実行に `origin` remote が不要になりました。base branch 解決は現在のローカルブランチや `HEAD` にフォールバックするため、ローカルだけのリポジトリでも plan をファンアウトできます。[CLI Reference]({{< relref "/docs/cli" >}}) を参照。
- **dry-run の整理。** issue / Project と `fanout plan` の dry-run はペイン作成前の安全な preview として残しつつ、issue / Project dry-run は `/tmp` briefing file を書かなくなりました。未使用だった `fanout msg --dry-run` surface は削除されました。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.7.0)

## v0.6.0 (2026-06-15)

- **issue-less plan の peer messaging（`fanout plan --team`）。** plan レーンが `--team` に対応し、issue / Project レーンと同じ兄弟ペイン協調を `fanout plan` に組み込みました。issue-less な plan task には GitHub issue 番号が無いため、peer は **task id** で指定します。`fanout msg send --to <task-id>` で送り、`fanout msg peers` が現在の task id 一覧を表示します。plan のバスは `/tmp/fanout-<repo>-plan-<slug>.db` に置かれます。[Workflow]({{< relref "/docs/workflow" >}}) を参照。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.6.0)

## v0.5.0 (2026-06-14)

- **Per-target agent overrides（ターゲット別 agent 上書き）。** 素の `--agent <name>` は引き続き全ての子の既定を設定しますが、繰り返し可能な `--agent <NUM>=<name>`（issue / Project の子）や `--agent <task-id>=<name>`（`fanout plan`）で 1 回の run の中に agent を混在させられるようになりました。各ターゲットはまず一致する上書き、次に global `--agent`、最後に `FANOUT_AGENT` の順に解決し、検証は実際に選択された agent のみに行います。[CLI Reference]({{< relref "/docs/cli" >}}) を参照。
- **複数行対応の TUI セッション modal。** 常駐コンソールで `n` を押すと modal が開き、複数行の prompt、`claude` / `codex` の選択、任意 slug を入力できます。改行は `Ctrl+J` で入力し、`Shift+Enter` は enhanced keyboard input 有効時だけ使えます。`Enter` でペインを作成します。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.5.0)

## v0.4.0 (2026-06-14)

fanout が独自のコンソール、ダッシュボード、計画レーンを備え、dmux に依存しない単体の CLI になったリリースです。

- **単体ランタイム + 常駐 TUI コンソール。** dmux 依存を外して `tmux` を直接制御し、引数なし `fanout` のコンソールにペインフォーカス / 出力 peek、ライフサイクル操作、wave & blocker 列、manual agent ペイン、メモリ内の検索 / フィルタを追加しました。[Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **読み取り専用 Web ダッシュボード。** `127.0.0.1` バインドで GET 専用、token ゲート付きの Session ビューを、PAPER BREEZE テーマの React + Vite + TypeScript SPA として作り直し、詳細 drawer、plan モードの提案プラン表示、未ファンアウトの子の synthetic 行を備えました。`fanout dashboard --web` で起動します。
- **issue を作らない `fanout plan`。** GitHub の子 issue ではなくローカルの JSON plan spec をファンアウトします。task ID、`blocked_by` 依存の wave、task ライフサイクル（`--status` / `--merge` / `--close` / `--cleanup`）に対応します。[CLI Reference]({{< relref "/docs/cli" >}}) を参照。
- **Peer messaging（`--team` / `fanout msg`）。** parent ごとの SQLite バスを介した、有効化すると使える兄弟協調と、best-effort の `nudge` を追加しました。[Workflow]({{< relref "/docs/workflow" >}}) を参照。
- **Codex Plan Mode + review skill。** `--codex-plan-mode` で Codex の子を interactive Plan-Mode TUI セッションとして起動できるようになり、両 agent 向けに `post-work-review` / `pr-watch` skill を同梱しました。[Agent Integrations]({{< relref "/docs/agents" >}}) を参照。
- **ドキュメントサイトと前提の軽量化。** この Hugo サイト（英語 / 日本語）を公開し、`jq` と `gh-sub-issue` の前提を公式 GitHub Sub-issues API へ置き換え、Go ツールチェーンを golangci-lint v2 ベースに刷新しました。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.4.0)

## v0.3.0 (2026-06-06)

- **自己更新。** `fanout update` は同じ `install.sh` 経路で実行中のバイナリと同梱の Claude / Codex 連携を置き換え、`fanout --check-update` は何も変更せずに最新リリースとバイナリを比較します。[CLI Reference]({{< relref "/docs/cli" >}}) を参照。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.3.0)

## v0.2.0 (2026-06-04)

- **Settings 機構。** opinionated な子 briefing の挙動を切り替え可能にし、CLI フラグ、`FANOUT_*` 環境変数、config ファイルにまたがって解決するようにしました。[Settings]({{< relref "/docs/settings" >}}) を参照。
- **Go 一本化。** レガシーな Bash 実装を削除して Go CLI に統一し、子 briefing に Codex review ゲートを追加、fanout skill が OPEN な issue / Project 候補を提示するようにしました。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.2.0)

## v0.1.0 (2026-06-04)

- **最初の Go リリース。** 並列 Go 移植版を既定のインストール対象とし、ビルド済みリリース配布、`--status` JSON レポーター、Projects v2 モード、`gh pr create` 前にレビューを強制する PreToolUse ゲート、旧 Bash エントリポイントの deprecation 通知を備えました。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.1.0)
