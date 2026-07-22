---
title: 変更履歴
linkTitle: 変更履歴
description: "各リリースの変更点。新しい順で、ドキュメントへのリンク付き。"
weight: 110
kanji: 録
yomi: changelog
---

リリースのハイライトを新しい順に並べています。各タグには [GitHub release](https://github.com/butaosuinu/fanout/releases) があり、完全なコミット一覧とビルド済みバイナリ（darwin / linux × amd64 / arm64）を含みます。バージョンは git タグから ldflags 経由で埋め込まれます。`fanout --check-update` で自分の版を確認できます。

## v0.14.0 (2026-07-21)

- **OpenCode 子エージェント。** `--agent opencode` で issue / Project と `fanout plan` を実行でき、per-target override と TUI new-session picker でも選べるようになりました。
  fanout は prompt を `--prompt` で渡し、`opencode --continue` で resume します。
  briefing は base + 共通検証で、team message は pull のみ、`nudge` は対象外です。
  [エージェント連携]({{< relref "/docs/agent-integrations" >}}) と [CLI リファレンス]({{< relref "/docs/cli" >}}) を参照。
- **レガシーペインの安全な adoption。** TUI restore は、tmux server 世代、pane process の起動時刻、起動時の marker、全 restore root 横断の claimant 検査で所有を証明できた場合に限り、`shellKey` のない live な pre-#503 state 行を移行します。
  liveness key を live ペインへ刻印して state 行へ保存するため lifecycle close が可能になり、曖昧な行は変更せず fail closed のままです。
  [モニタリング]({{< relref "/docs/monitoring" >}}) と [CLI リファレンス]({{< relref "/docs/cli#--merge----close----cleanup" >}}) を参照。
- **agent とセットアップの導線整理。** ドキュメントトップとエージェント連携で Claude Code、Codex、OpenCode を一か所で比較できるようにし、README とインストール案内は前提ツールを先に示して、TUI、message、watcher、Plan Mode の詳細を正典ページへのリンクに置き換えました。
  [エージェント連携]({{< relref "/docs/agent-integrations" >}}) と [インストール]({{< relref "/docs/installation" >}}) を参照。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.14.0)

## v0.13.0 (2026-07-20)

- **観測専用の herdr backend。** opt-in の `herdr` runtime backend を選択し、TUI と web ダッシュボードで記録済み session を `herdr api snapshot` と照合できるようになりました。
  v1 は herdr 0.7.3 に固定し、launch や変更操作を拒否します。
  既定の backend は tmux のままです。
  status table に `BACKEND` / `PANE` 列を追加し、JSON には省略可能な `backend` / `pane_id` field を追加しました。
  [herdr backend]({{< relref "/docs/herdr-backend" >}}) と [モニタリング]({{< relref "/docs/monitoring" >}}) を参照。
- **team message の push 配信。** `fanout msg watch` が Claude の Monitor ツール経由で新着を配信し、新規起動した非 Plan Codex ペインには app-server bridge が idle 時に未読 message を注入します。
  Plan Mode と restore したペインは pull のままです。
  注入失敗は `fanout msg inbox --all` で回収します。
  [ワークフロー]({{< relref "/docs/workflow" >}}) と [CLI リファレンス]({{< relref "/docs/cli#fanout-msg" >}}) を参照。
- **Codex pane cleanup の安全化。** Codex ペインを close すると app-server と子孫の Node / MCP process を停止し、`shellKey` でペインの所有を照合し、安全な cleanup を確認できなければ recovery state を残します。
  `shellKey` のない既存の live state 行は、再利用されたペインを誤って対象にせず fail closed します。
  [CLI リファレンス]({{< relref "/docs/cli#--merge----close----cleanup" >}}) を参照。
- **native subagent による post-work-review。** `$post-work-review` は fresh な通常の Codex native subagent に委譲し、custom agent、model 固定、app-server controller、JSON result parser を使わなくなりました。
  廃止済み driver は integrations 込みの install または update で削除してください。
  driver が残っている間、`--no-skills` による binary-only update は停止します。
  [エージェント連携]({{< relref "/docs/agent-integrations" >}}) と [インストール]({{< relref "/docs/installation" >}}) を参照。
- **godep-cruiser による architecture guard。** layer check を手書きの import test から、期限付き baseline を持つ固定版 godep-cruiser rule set へ移し、同じ4層の境界を検証します。
  `docs/architecture.ja.md` を参照。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.13.0)

## v0.12.0 (2026-07-15)

- **Codex Plan Mode の保存設定。** 通常の issue / Project の子 fan-out は、CLI、環境変数、repo config、user config から `codexPlanMode` を解決し、TUI settings popup でも同じ設定を編集できます。
  OPEN な子を持つ TUI Issue fan-out にも適用し、`codex` 以外が混ざる割り当てはペイン作成前に拒否します。
  [Agent Integrations]({{< relref "/docs/agent-integrations" >}}) と [Settings]({{< relref "/docs/settings" >}}) を参照。
- **親 issue の orchestrator pane。** OPEN な子を持つ issue を TUI から fan-out すると、project root に orchestrator pane を 1 つ作成してから子 pane を起動します。
  orchestrator は親スコープの調整と最終集約を担当し、再選択時は重複せず、初回にすべての子が blocked ならペインを作成しません。
  [Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **長文 prompt の欠落防止。** new-session popup は textarea の上限を超える bracketed paste を外部に保持し、全文を起動処理へ渡すようになりました。
  長大な起動 payload は必要に応じて briefing file 経由で渡し、Codex Plan Mode と attach 経路でも OS の引数上限を回避します。
  [Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **`post-work-review` の実 session 検証。** `$post-work-review` は reviewer と verifier の結果を記録する前に、native child rollout の parent、role、read-only sandbox、approval policy、正確な bundle path、session UUID を検証します。
  呼び出し予約と固定上限を記録し、未完了、重複、検証不能な run は fail-closed します。
  [Agent Integrations]({{< relref "/docs/agent-integrations" >}}) を参照。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.12.0)

## v0.11.0 (2026-07-11)

- **Issue mode の plan fan-out。** 新規 Session popup の Issue mode に Prompt mode と同じ plan fan-out を追加し、選択した issue を issue-less な `fanout plan` task に分解できるようにしました。
  coordinator と task agent は別々に選択でき、選択中の issue に OPEN な子がある場合はチェックボックスを無効化します。
  [Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **Codex Plan Mode の起動安定化。** fanout は app-server で Plan Mode thread を作成して interactive Codex TUI を接続し、初回 Plan turn が受理されてから launch を記録するようになりました。
  plan 生成と承認待ちには startup timeout を設けません。
  [Agent Integrations]({{< relref "/docs/agent-integrations" >}}) を参照。
- **GPT-5.6 向け Codex 連携。** 同梱する 5 個の Codex skill は、主な判断手順を `SKILL.md` に置き、必要なときだけ reference や script を読む構成になりました。
  `$post-work-review` は固定した read-only の reviewer と verifier を使い、指定モデルを利用できない場合は停止します。
  `$pr-watch` は同じ状態の再通知を抑える foreground watcher で監視します。
  [Agent Integrations]({{< relref "/docs/agent-integrations" >}}) を参照。
- **新規 Session 起動後のフォーカス。** `n` popup から Prompt、plan coordinator、Issue Session を起動すると、実際の作成順で先頭の新規ペインへフォーカスするようになりました。
  agent 追加（`a`）、shell（`A` / `t`）、watcher、通常の CLI 起動では、従来どおりフォーカスを移しません。
  [Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **Prompt Session の PR 状態。** Prompt Session の記録済み branch に PR がある場合、Web ダッシュボードがそのリンクと CI 状態を表示するようになりました。
  [Monitoring]({{< relref "/docs/monitoring" >}}) を参照。
- **貢献者向け品質ゲート。** リポジトリの正典ローカルゲートを `make check` に統一し、`post-work-review` の結果をレビュー対象の base と diff に結び付けました。
  リポジトリ内の Claude と Codex の hook は、clean な対象 commit がゲートを通過するまで branch push を止め、Codex Stop hook も backstop として検証します。
  release tag の push は対象外です。
  `docs/review-checklist.ja.md` を参照。

[リリースノート →](https://github.com/butaosuinu/fanout/releases/tag/v0.11.0)

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
- **新しいレビュー skill。** 過去のセッションから再発するツールエラー・CI 失敗・レビュー指摘を掘り出す `session-retro` skill を新設し、`post-work-review` にプロジェクト検証パスとレビューチェックリストを追加しました。[Agent Integrations]({{< relref "/docs/agent-integrations" >}}) を参照。
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
- **Codex Plan Mode + review skill。** `--codex-plan-mode` で Codex の子を interactive Plan-Mode TUI セッションとして起動できるようになり、両 agent 向けに `post-work-review` / `pr-watch` skill を同梱しました。[Agent Integrations]({{< relref "/docs/agent-integrations" >}}) を参照。
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
