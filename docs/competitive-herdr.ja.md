# herdr 競合分析 — agent multiplexer との棲み分けと取り込み

ステータス: 分析 + 提案。作成: 2026-07。herdr 公式ドキュメント・GitHub リポジトリの調査と、fanout 実コード(`internal/tmuxrun` / `internal/sessionview` / `cmd/fanout/msg.go`)での実現性検証に基づく。

## herdr とは

[herdr](https://herdr.dev/) は「agent multiplexer」— 複数のコーディングエージェントを 1 つのターミナルで走らせるための、tmux 代替の永続 PTY ランタイム。Rust 製シングルバイナリ(実測 14〜17MB)で、サーバー・クライアント構成をローカル Unix socket でつなぐ。AGPL-3.0 + 商用のデュアルライセンス。GitHub 約 1 万 stars(2026-07 時点)、2026-06-30 に GitHub Trending 1 位。v0.7.1(2026-06-24)時点でリリース 66 回とアクティブに開発されている。

中核は 3 つ。

1. **エージェント状態のセマンティック追跡** — 各ペインのエージェントを idle(プロンプト待ち)/ working(作業中)/ blocked(許可・入力待ち)/ done で色分け表示する。検出はプロセス名マッチ + 出力ヒューリスティクスに加え、`herdr integration install <agent>` が各エージェントの hooks / plugin 機構に状態報告スクリプトを書き込む
2. **エージェント向け API** — 改行区切り JSON の Socket API と CLI(`herdr pane split/run/read`、`herdr wait output/agent-status`、`herdr worktree create/open`)。SKILL.md をエージェントに与え、エージェント自身がペインを割り、ヘルパーを起動し、隣のペインの完了を待てる
3. **session resume** — Claude Code / Codex を含む主要エージェントの公式 session 参照を記録し、サーバー再起動後に復元する

対応エージェントは 14 以上(Claude Code / Codex / Droid / Amp / OpenCode / Cursor / Copilot CLI ほか)。GitHub 連携・PR ライフサイクル・issue 駆動の作業割り当ては持たない。

## レイヤーが違う、しかし排他

herdr は実行環境レイヤー(tmux の代替)、fanout はワークフローレイヤー(tmux の上で GitHub issue → worktree → ペイン → PR を配線する)で、機能面の直接競合は狭い。ただし**実行環境としては排他**になる: fanout は tmux 必須で、herdr は tmux 非互換の自前ランタイムだから、ユーザーはどちらかを選ぶ。herdr が issue 駆動やレビューゲートを足せば fanout の領域への侵食が始まるし、逆に fanout のユーザーが herdr の状態表示や wait を求めて移る流れもありうる。

| 軸 | fanout | herdr |
|---|---|---|
| 実行基盤 | tmux の上のレイヤー | 自前 PTY ランタイム(tmux 非互換) |
| 起動の駆動源 | GitHub issue / Project / plan spec → briefing 自動生成 | 手動、またはエージェント自身が Socket API で起動 |
| worktree | 子 issue 単位で自動計画・作成・cleanup | `worktree create/open` コマンド(駆動源なし) |
| エージェント状態 | `@fanout_agent_state` の running / done の 2 値 | idle / working / blocked / done + カスタムラベル |
| 待機プリミティブ | なし(`msg nudge` の片方向 push のみ) | `wait agent-status` / `wait output` |
| session resume | なし(Codex Plan Mode の thread 引き継ぎは起動時のみで、復元には使えない) | 主要エージェントの公式 session 参照で復元 |
| PR ライフサイクル | `--status`(CI・PR 状態)/ `--close` / `--merge` / `--cleanup` | なし |
| 依存関係 | blocker / wave、`--unblocked-only` | なし |
| エージェント間協調 | `fanout msg`(SQLite bus、pull 型) | Socket API 経由の read / send |
| 無人化 | label watcher(opt-in) | なし |
| UI | TUI コンソール + read-only web dashboard | マウス対応 TUI(web なし) |
| 対応エージェント | claude / codex(#361 opencode、#241 cursor/copilot が進行中) | 14+、未知のエージェントもヒューリスティクスで検出 |

## 脅威評価

短期の脅威は「機能の重複」ではなく「実行環境の乗り換え」で、herdr の吸引力は上の表の状態・待機・resume の 3 行に集約される。fanout はこの 3 つを tmux の上で提供できる(後述)。提供すれば、ユーザーが fanout を捨てて herdr に移る理由は「マウス対応 TUI が欲しい」くらいまで細る。

中期の脅威は herdr のエージェント自己オーケストレーション路線。SKILL.md + Socket API は「エージェントがヘルパーを生やし、待ち、回収する」方向で、fanout の skill 層(fan-out 判断は LLM、CLI は決定論)と思想が近い。herdr がここに GitHub 連携を足すと正面衝突するが、現時点の herdr は issue・PR・レビューに一切触れていない。

fanout 側の守りは 2 つある。実行基盤が tmux という枯れた共有インフラでロックインがないこと、そして GitHub 駆動ワークフロー(briefing 生成・wave・review gate・PR lifecycle・可観測性)の積み上げが herdr には無いこと。攻めに使える差は、herdr がローカル完結なのに対し fanout は「issue が入力、マージ済み PR が出力」という閉ループを持つ点で、ロードマップ(`docs/roadmap.ja.md`)の閉ループハーネス方針そのものが差別化になる。

## fanout に取り込む機能

herdr が上位互換な 3 点を、tmux の上のレイヤーという形のまま取り込む。すべて CLI は決定論(LLM を呼ばない)のまま実装できる。

### A. エージェント状態のセマンティック化(規模 M + web S)

`@fanout_agent_state` を running / working / idle / blocked / done の 5 値 + 空に拡張する。キーも書き込み先(tmux pane user option)も変えない。running(起動済みだが粒度不明)と done(プロセス終了)は現行の起動ラッパーがそのまま書くので、hooks 非対応エージェントや旧版 fanout が起動したペインは自然に 2 値に留まり、後方互換は値のセマンティクスだけで成立する。

- Claude Code: 起動コマンドに `--settings`(インライン JSON)で hooks を注入する。`UserPromptSubmit` / `PreToolUse` → working、`Notification` → blocked、`Stop` → idle。hook 実体は `tmux set-option -p -t "$TMUX_PANE" @fanout_agent_state <state> 2>/dev/null || true` の 1 行シェルで、fanout バイナリを経由しない
- Codex: `-c notify=[...]` の注入で idle 遷移のみ検出できる。blocked / working は検出不能とドキュメントに明示する
- herdr の「user 設定に hooks を書き込む」インストール方式(`integration install`)は採らない。起動時注入なら fanout が起動したペインにだけ効き、ユーザーの他セッションを汚さない

波及先は `internal/sessionview` の `normalizeAgentState`(許可リスト拡張)、`cmd/fanout/msg.go` の `shouldNudge`(idle と粒度不明の running だけ nudge し、working / blocked / done は no-op のまま)、TUI detail と web dashboard の状態表示。起動コマンドが変わるので Tier 2 dry-run golden は全件再生成になる。#106(レビュー追従 nudge)は blocked / busy を避けて idle の瞬間に届ける品質になり、#59(Wave 自動進行)の完了検知の土台にもなる。

### B. `fanout wait` — 待機プリミティブ(規模 M、A に依存)

```
fanout wait --parent <ref> --target <N|taskID> --status idle[,done] [--timeout 300]
fanout wait --parent <ref> --target <N|taskID> --output <regex> [--timeout 300]
```

`ListLivePanes` の 2 秒間隔ポーリング(dashboard poller と同じ)で、状態一致または capture-pane 出力の正規表現一致までブロックする。`--timeout` はデフォルト 300 秒で必ず効かせ、無限ブロックを構造的に禁止する。ペイン照合は `msg.go` の id + worktree 再検証をヘルパーに昇格して共有する。tmux の読み取り(`list-panes` / `show-options` / `capture-pane`)だけで完結し、state.json も GitHub も書かない。`--output` はペイン出力という攻撃可能面をトリガーに使うため、セキュリティ境界にはしない調整プリミティブと位置づけ、`--status`(hooks の明示信号)を第一候補とする。

skill 層の待機との住み分け: ScheduleWakeup は分〜時間単位の gh 側条件(PR マージ待ち)、`fanout wait` は秒〜数分単位の tmux ローカル条件(子の手が空くのを待つ)。#59 の波ループはこの 2 段構えになり、skill が `--status` を連打するターン消費を 1 コマンドに畳める。A なしでは running / done しか待てず価値が半減するため、順序は A → B。

### C. session resume(規模 M、独立)

- Claude Code: 起動時に fanout が UUID を生成して `claude --session-id <uuid>` で起動し、`state.Pane` に `SessionID`(additive フィールド)として記録する。hooks もログ走査も不要で決定論。記録が stale な場合のみ `~/.claude/projects/<worktree のエスケープ名>/` の最新 jsonl にフォールバックする
- Codex: 起動時の id 事前指定ができないため、resume 時に `~/.codex/sessions/**/rollout-*.jsonl` の先頭行 `session_meta.payload.cwd` が worktree に一致する最新ファイルから id を発見する
- `fanout resume <N>`: agent がまだ動いていれば no-op。`@fanout_agent_state` が done(起動ラッパーは agent 終了後もシェルを exec してペインを生かすため、ペイン生存では判定しない)またはペイン消滅なら、同じ worktree で `claude --resume <id>` / `codex resume <id>` を新ペインとして起動する(既存の起動ラッパーと split 後デコレーションを再利用)。`CodexPlanMode` 行はペイン内 app-server の thread に紐づく別プロトコルで、ペインが死ぬと thread への経路も失われるため、v1 では明示的に拒否する
- dry-run コマンドには `--session-id` を付けず、Tier 2 golden を無風に保つ

tmux server 再起動やペイン事故からの復帰手段で、#59 の多波・長時間運用と組み合わさって効く。A / B と依存が無く並行実装できる。

### 実装順序

A → B の順で入れ、C は並行。#59 / #106 は A / B の上に乗る(#106 の nudge は A の idle 検知を、#59 の波ループは B の wait を使う)。

## やらないこと(判断記録)

- **multiplexer 化**: dmux 脱却(#81 / #145)で tmux 直制御へ単純化した判断と矛盾する。tmux は共有インフラで、fanout はその上のレイヤーに留まる
- **Socket API / デーモン化**: fanout のエージェント向けインターフェースは CLI(`msg` / `--status` / 将来の `wait`)+ state.json で足りる。常駐サーバーは read-only dashboard の境界(GET のみ・mutation なし)を崩す誘因になる
- **capture-pane ヒューリスティクスによる状態推定**: ペイン内容は攻撃可能面(peek の検証チェーンが前提とする設計判断)であり、TUI 再描画で壊れやすい。状態は hooks / notify の明示信号だけから取る。B の `wait --output` はペイン出力を使うが、明示指定パターンの待機であって状態推定ではない — B に書いたとおり調整用途に限る
- **SSH リモートアタッチ・マウス対応 TUI**: tmux 自体の機能(attach / mouse mode)で代替できる。fanout が再実装する層ではない

## 参考

- https://herdr.dev/ / https://github.com/ogulcancelik/herdr
- Socket API: https://herdr.dev/docs/socket-api/ — `pane.*` / `agent.*` / `events.subscribe/wait` / worktree 操作
- Agent skill: https://herdr.dev/docs/agent-skill/ — SKILL.md によるエージェント自己オーケストレーション
- Integrations: https://herdr.dev/docs/integrations/ — lifecycle authority 型(状態を直接報告)と session identity 型(復元用 session 参照)の 2 系統
- 比較ページ: https://herdr.dev/compare/ — tmux / Zellij / cmux / Warp / Solo / OpenCode(web モード)/ Conductor / Emdash / Superset との対比。他ツールが取りこぼす「永続ランタイム × エージェント状態認識」の交差点("The intersection other tools miss")という位置づけ
- 関連 issue: #59(Wave 自動進行)/ #106(PR レビュー追従)/ #72(nudge、closed)/ #361(opencode)/ #241(cursor / copilot)
