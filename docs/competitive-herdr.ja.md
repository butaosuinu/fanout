# herdr 競合分析 — agent multiplexer との棲み分けと取り込み

ステータス: 分析 + 提案。作成: 2026-07。改訂: 2026-07-22 — v0.7.5 の live-agent CLI、公式デモ、plugin 生態系掃引(topic `herdr-plugin` 全 289 repo)を反映。herdr 公式ドキュメント・GitHub リポジトリの調査と、fanout 実コード(`internal/infra/tmuxrun` / `internal/app/sessionview` / `cmd/fanout/msg.go`)での実現性検証に基づく。

この文書は tmux backend に取り込む機能を扱う。
herdr runtime backend wave 2 の API と制約は [実機検証](herdr-runtime-backend-spike.ja.md) を参照。

## herdr とは

[herdr](https://herdr.dev/) は「agent multiplexer」— 複数のコーディングエージェントを 1 つのターミナルで走らせるための、tmux 代替の永続 PTY ランタイム。
Rust 製シングルバイナリ(実測 14〜17MB)で、サーバー・クライアント構成をローカル Unix socket でつなぐ。
AGPL-3.0 + 商用のデュアルライセンス。
GitHub 約 1 万 stars(2026-07 時点)で、2026-06-30 の GitHub Trending では 1 位だった。
2026-07-15 UTC に v0.7.4、2026-07-21 UTC に v0.7.5 が公開された。

中核は 3 つ。

1. **エージェント状態のセマンティック追跡** — 各ペインのエージェントを idle(プロンプト待ち)/ working(作業中)/ blocked(許可・入力待ち)/ done で色分け表示する。検出はプロセス名マッチ + 出力ヒューリスティクスに加え、`herdr integration install <agent>` が各エージェントの hooks / plugin 機構に状態報告スクリプトを書き込む
2. **エージェント向け API** — 改行区切り JSON の Socket API と CLI(`herdr pane split/run/read`、`herdr worktree create/open`)。v0.7.5 で agent が pane と別の一級 primitive になり、`agent start <name> --kind <kind> --pane <id>`(name は必須で session 内の live agent 間で一意 — exit / release で解放され再利用できる。`[a-z][a-z0-9_-]{0,31}`。対象は対話 shell prompt にいる既存 pane で、別 process が動いていると `agent_pane_busy` で失敗する。pane の作成・split は layout 側)、`agent prompt --wait --until <state>`(atomic なのは text + Enter の投入だけで、wait は turn を識別しない lifecycle state 待ち — 送信前から動いていた turn の完了でも成功し得る)、server 側で待つ `agent wait` / `pane wait-output` が入った(旧 top-level `wait` と `agent send` は置換)。SKILL.md をエージェントに与え、エージェント自身がペインを割り、ヘルパーを起動し、隣のペインの完了を待てる
3. **session resume** — Claude Code / Codex を含む主要エージェントの公式 session 参照を記録し、サーバー再起動後に復元する

対応エージェントは 21 種(Claude Code / Codex / OpenCode / Cursor / Copilot CLI / Gemini / Devin ほか)。core は GitHub 連携・PR ライフサイクル・issue 駆動の作業割り当てを持たない(plugin 層の接近は脅威評価を参照)。

## レイヤーの違いと wave 2 の制約

herdr は実行環境レイヤー(tmux の代替)、fanout は GitHub issue → worktree → ペイン → PR を配線するワークフローレイヤーであり、機能面の直接競合は狭い。
0.7.4 wave 2 は tmux-parity の協調プロセス信頼で owned server、launch、cleanup、peek、focus、nudge、emitter、metadata、Codex v6 resume の後続実装を解禁する。
判断主体はユーザー、判断日は 2026-07-21 JST である。
実装は private socket、0700 XDG、owner marker で影響を fanout-owned session へ封じ込め、intent / phase、nonce 二重照合、branch 予約、送信直前再照合を crash safety と誤操作防止に使う。
herdr は opt-in backend とし、tmux と herdr を一つの親 fan-out の runtime として排他にして親単位の stickiness を適用する。
fanout state には両 backend の行を共存させられる。
ユーザーが fanout と herdr のどちらか一方を製品として選ぶ関係ではない。
issue 駆動・レビューゲートへの侵食は plugin 層で始まっている(脅威評価を参照)。

v0.7.5 は agent コマンドを再編し(workspace-level 廃止、pane-targeted `agent start --kind`、`agent send` → `send-keys`、top-level `wait` 廃止)、wave 2 契約が前提とした 0.7.4 の `agent start --workspace --cwd --env` 形は最新版に存在しない。
floor は 0.7.5 へ引き上げる(ユーザー決定 2026-07-22)。契約改訂は 0.7.5 実機再検証 spike の docs PR が行う。

次の fanout 列は現行 tmux backend と wave 2 が解禁する herdr backend のワークフロー能力を示す。

| 軸 | fanout | herdr |
|---|---|---|
| 実行基盤 | tmux が end-to-end workflow を実行。herdr wave 2(floor 0.7.5)も owned session 上の同 workflow を後続実装へ解禁 | 自前 PTY ランタイム(tmux 非互換) |
| 起動の駆動源 | GitHub issue / Project / plan spec → briefing 自動生成 | 手動、または coordinator agent が agent CLI で起動・fan-out(v0.7.5) |
| worktree | 子 issue 単位で自動計画・作成・cleanup | `worktree create/open` コマンド(駆動源なし) |
| エージェント状態 | `@fanout_agent_state` の running / done の 2 値 | idle / working / blocked / done + カスタムラベル |
| 待機プリミティブ | なし(`msg nudge` の片方向 push のみ) | server 側 `agent wait --until` / `pane wait-output` / `agent prompt --wait`(v0.7.5) |
| session resume | なし(Codex Plan Mode の thread 引き継ぎは起動時のみで、復元には使えない) | 主要エージェントの公式 session 参照で復元 |
| PR ライフサイクル | `--status`(CI・PR 状態)/ `--close` / `--merge` / `--cleanup` | なし |
| 依存関係 | blocker / wave、`--unblocked-only` | なし |
| エージェント間協調 | `fanout msg`(SQLite bus、pull 型) | agent CLI の prompt / send-keys / read / wait |
| 無人化 | label watcher(opt-in) | なし |
| UI | TUI コンソール + read-only web dashboard | マウス対応 TUI(web なし)で、v0.7.4 は展開した Space / Agent entry を `rows` と `row_gap` で設定でき、Agent には `rows_by_agent`、custom metadata には `$name` token を使える |
| 対応エージェント | claude / codex(#361 opencode、#241 cursor/copilot が進行中) | 21 種、未知のエージェントもヒューリスティクスで検出 |

## 脅威評価

短期の脅威は二つある。ひとつは「実行環境の乗り換え」で、herdr の吸引力は上の表の状態・待機・resume の 3 行に集約される。もうひとつは v0.7.5 で現物になった plan lane の直接競合(次段)— issue lane も単発の issue → worktree → PR までは plugin が到達しており(後述)、未到達なのは親子 issue 列挙・wave・review gate を含む閉ループ全体である。
現行 binary はまだ herdr 上の issue / Project / plan fan-out を実行しないが、wave 2 契約は後続 issue に workflow continuity の実装を許可する。
同一 UID process に対する proof-grade authority は前提にせず、tmux と同じ協調プロセス信頼と fail-closed な crash / identity gate を使う。
tmux backend にはこの 3 点を tmux の上で提供する価値が残る(後述)。

エージェント自己オーケストレーション路線は v0.7.5 で「中期の脅威」から現物になった。
発表スレッドは agent CLI を multi-agent orchestration のために作ったと位置づけ、公式デモでは coordinator の Claude Code が自然言語指示から pane を割って `agent start` で codex 4 体を配置し、観点別レビューの briefing を `agent prompt` で配り、`agent wait` のループで全完了を待って結果を統合する。
これは fanout plan(issue-less lane)の中核体験 — 計画分解 → briefing 配布 → 並列起動 — を coordinator LLM が herdr primitive の即興合成で再現し、さらに fanout では未実装の完了待ち(後述 B の `fanout wait` 相当)と結果回収まで到達している。
fanout plan に残る差は次の 5 点。
(a) 決定論と再現性 — spec が正典で毎回同じ配線になり、dry-run / golden で監査できる。LLM 即興は毎回違う。
(b) worktree 分離と safety gate — デモは同一ディレクトリの read-only レビューで、write 系を herdr worktree の即興合成で組んでも dirty 検査・branch 予約・idempotency はない。
(c) `blocked_by` の wave 決定論。
(d) state.json による状態管理と status / cleanup。
(e) PR lifecycle への接続。

plugin 生態系(topic `herdr-plugin`、2026-07-22 時点の search total_count 289 を star 順で全ページ掃引)は fanout の隣接領域に到達している。
作者自身の `herdr-plugin-github-start`(GitHub issue / PR / discussion から agent 起動。issue body 非取得・worktree 非作成・fan-out なし)、GitHub issue 選択 → `gh issue develop` → worktree / workspace 作成 → PR の review / create / push まで一本で提供する `herdr-plugin-gh-workflow`、カンバンカードを prompt として agent に配り自動 Plan → Execute → Review と手動 gate を持つ `herdr-board`(`herdr-kanban` はタスクと tab の紐付けのみで agent 起動なし)、Claude Code teams の `herdmates`、Linear issue → worktree の `herdr-worktree-from-linear` と GitHub PR → worktree の `herdr-worktree-from-pr`、スケジュール起動の `herdr-routines`、merge 検知 branch cleanup の `herdr-branch-cleanup`、レビュー・PR 追跡の `herdr-reviewr` / `herdr-pr-tracker`、人間承認ゲートの `herdr-approval-gate`。
plan lane 相当は複数ある。`darjss/herdr-orchestrate`(Pi package / skill + `orch` CLI + herdr plugin の複合物、Pi 専用)は run board、worker spawn(research / implement / review)、隔離 worktree、background wait、cleanup を備える。`ribbons-digital/pi-herd` は planner / implementer / reviewer / tester の役割分担、分離 worktree、永続 run state、wait / collect、merge-plan、cleanup を提供する。
親子 issue 列挙・blocker wave・レビューゲートを貫く fanout の閉ループ全体を代替するものはまだ無いが、単発 issue → worktree → PR の線は既に plugin が引いており、「herdr は issue・PR・レビューに触れていない」はもう成り立たない。

fanout 側の守りは、runtime 選択と、GitHub 駆動ワークフロー(親子 issue 列挙・briefing 生成・wave・review gate・PR lifecycle・可観測性)の閉ループが herdr core にも単一 plugin にも無いことにある。
攻めに使える差は、herdr がローカル完結なのに対し fanout は「issue が入力、マージ済み PR が出力」という閉ループを持つ点で、ロードマップ(`docs/roadmap.ja.md`)の閉ループハーネス方針そのものが差別化になる。
fanout plan の価値は「即興オーケストレーションとの対比」で語る — 決定論・安全ゲート・再現性。

運用上の注意: herdr 公式 skill を持つ子 agent は自力でペインを割り helper agent を生やせる。
これらは state.json に載らないため status には出ない。記録済み child workspace 内の helper pane は cleanup(`worktree remove` / `workspace close`)で workspace ごと閉じられるが、helper 用に別 workspace / worktree を作られた場合は cleanup 対象外として残る。
fanout の briefing はこの動線を促さないが禁止もしない — 管理外 pane は herdr 側の表示でのみ追える。

## fanout に取り込む機能

herdr が上位互換な 3 点を、tmux の上のレイヤーという形のまま取り込む。すべて CLI は決定論(LLM を呼ばない)のまま実装できる。

### A. エージェント状態のセマンティック化(規模 M + web S)

`@fanout_agent_state` を running / working / idle / blocked / done の 5 値 + 空に拡張する。キーも書き込み先(tmux pane user option)も変えない。(2026-07 追記: 受理・表示側は session-notifications プランで実装済み。実装された契約は 5 値に Codex Plan Mode の plan を加えた 6 値 running / working / plan / blocked / idle / done。)running(起動済みだが粒度不明)と done(プロセス終了)は現行の起動ラッパーがそのまま書くので、hooks 非対応エージェントや旧版 fanout が起動したペインは自然に 2 値に留まり、後方互換は値のセマンティクスだけで成立する。

- Claude Code: 起動コマンドに `--settings`(インライン JSON)で hooks を注入する。`UserPromptSubmit` / `PreToolUse` / `PostToolUse` → working、`Notification` → blocked、`Stop` → idle。`PreToolUse` は許可判定の前に発火するため、許可待ち(blocked)からの復帰はそれでは拾えず、次の `PostToolUse` か `Stop` で解除する。hook 実体は `tmux set-option -p -t "$TMUX_PANE" @fanout_agent_state <state> 2>/dev/null || true` の 1 行シェルで、fanout バイナリを経由しない
- Codex: hooks が既定有効で(`~/.codex/hooks.json` / `config.toml`、https://developers.openai.com/codex/hooks)、`PermissionRequest` → blocked に置き換えるほかは Claude と同じマッピングを組める。起動時注入の経路(`-c` での hooks 定義可否)と非管理 hooks の信頼確認ダイアログの挙動は spike で検証する
- hook 信号は lifecycle 全体を覆う authority ではなく、表示・nudge・wait 向けの近似テレメトリと位置づける(herdr も Claude Code / Codex は session identity 統合で、状態検出には画面検出を併用している)。取りこぼしは `Stop` → idle と `--timeout` で回収し、状態値を正確性のクリティカルパスに置かない
- herdr の「user 設定に hooks を書き込む」インストール方式(`integration install`)は採らない。起動時注入なら fanout が起動したペインにだけ効き、ユーザーの他セッションを汚さない

波及先は `internal/app/sessionview` の `normalizeAgentState`(許可リスト拡張)、`internal/app/peermsg` の `shouldNudge`(idle と粒度不明の running だけ nudge し、working / blocked / done は no-op のまま。2026-07 追記: 実装では working / plan も nudge 許可に含めた — ターン中の入力は queue されて安全で、hooks 導入後はターンの大半が working になり running / idle だけでは nudge がほぼ届かないため。blocked / done / 未設定は no-op のまま)、TUI detail と web dashboard の状態表示。起動コマンドが変わるので Tier 2 dry-run golden は全件再生成になる。#106(レビュー追従 nudge)は blocked / busy を避けて idle の瞬間に届ける品質になり、#59(Wave 自動進行)の完了検知の土台にもなる。

### B. `fanout wait` — 待機プリミティブ(規模 M、A に依存)

```
fanout wait --parent <ref> --target <N|taskID> --status idle[,done] [--timeout 300]
fanout wait --parent <ref> --target <N|taskID> --output <regex> [--timeout 300]
```

`ListLivePanes` の 2 秒間隔ポーリング(dashboard poller と同じ)で、状態一致または capture-pane 出力の正規表現一致までブロックする。`--timeout` はデフォルト 300 秒で必ず効かせ、無限ブロックを構造的に禁止する。ペイン照合は `msg.go` の id + worktree 再検証をヘルパーに昇格して共有する。tmux の読み取り(`list-panes` / `show-options` / `capture-pane`)だけで完結し、state.json も GitHub も書かない。`--output` はペイン出力という攻撃可能面をトリガーに使うため、セキュリティ境界にはしない調整プリミティブと位置づけ、`--status`(hooks の明示信号)を第一候補とする。

skill 層の待機との住み分け: ScheduleWakeup は分〜時間単位の gh 側条件(PR マージ待ち)、`fanout wait` は秒〜数分単位の tmux ローカル条件(子の手が空くのを待つ)。#59 の波ループはこの 2 段構えになり、skill が `--status` を連打するターン消費を 1 コマンドに畳める。A なしでは running / done しか待てず価値が半減するため、順序は A → B。herdr の対応物は v0.7.5 で server 側の `agent wait` / `pane wait-output` に再編された。`fanout wait` は current-state の 2 秒間隔ポーリングのため、2 秒未満で通過する一過性状態(例: 自動投入で idle が即 working へ戻る)は見逃して timeout し得る。event 駆動の `agent wait` にはこの取りこぼしがない。ただし herdr 側の `--timeout` はミリ秒指定かつ省略時は無期限に待つ — `fanout wait` の「秒単位・timeout 必須」とは契約が逆なので、wave 2 で写像するなら単位変換と timeout 必須化が要る。

### C. session resume(規模 M、独立)

- Claude Code: 起動時に fanout が UUID を生成して `claude --session-id <uuid>` で起動し、`state.Pane` に `SessionID`(additive フィールド)として記録する。hooks もログ走査も不要で決定論。記録が stale な場合のみ `~/.claude/projects/<worktree のエスケープ名>/` の最新 jsonl にフォールバックする
- Codex: 起動時の id 事前指定ができないため、resume は worktree で `codex resume --last` を使う(`--last` は cwd スコープが公式仕様: https://developers.openai.com/codex/cli/reference)。`~/.codex/sessions/**/rollout-*.jsonl` の走査は内部レイアウト(非公式仕様)依存なので、`--last` で足りないケースの明示的フォールバックに留める
- `fanout resume <N>`: agent がまだ動いていれば no-op。`@fanout_agent_state` が done(起動ラッパーは agent 終了後もシェルを exec してペインを生かすため、ペイン生存では判定しない)またはペイン消滅なら、同じ worktree で `claude --resume <id>` / `codex resume <id>` を新ペインとして起動する(既存の起動ラッパーと split 後デコレーションを再利用)。`CodexPlanMode` 行はペイン内 app-server の thread に紐づく別プロトコルで、ペインが死ぬと thread への経路も失われるため、v1 では明示的に拒否する
- dry-run にも live と同じ `--session-id` の形を出す(`--dry-run` は実際の launch command を表示する契約で、live だけに付けると注入の破損を dry-run / golden で検出できない)。UUID は dry-run では決定論プレースホルダにし、Tier 2 golden を更新する

tmux server 再起動やペイン事故からの復帰手段で、#59 の多波・長時間運用と組み合わさって効く。A / B と依存が無く並行実装できる。

### 実装順序

A → B の順で入れ、C は並行。#59 / #106 は A / B の上に乗る(#106 の nudge は A の idle 検知を、#59 の波ループは B の wait を使う)。

## やらないこと(判断記録)

- **multiplexer 化**: dmux 脱却(#81 / #145)で tmux 直制御へ単純化した判断と矛盾する。tmux は共有インフラで、fanout はその上のレイヤーに留まる
- **Socket API / デーモン化**: fanout のエージェント向けインターフェースは CLI(`msg` / `--status` / 将来の `wait`)+ state.json で足りる。常駐サーバーは read-only dashboard の境界(GET のみ・mutation なし)を崩す誘因になる
- **capture-pane ヒューリスティクスによる状態推定**: ペイン内容は攻撃可能面(peek の検証チェーンが前提とする設計判断)であり、TUI 再描画で壊れやすい。状態は hooks / notify の明示信号だけから取る。B の `wait --output` はペイン出力を使うが、明示指定パターンの待機であって状態推定ではない — B に書いたとおり調整用途に限る
- **SSH リモートアタッチ・マウス対応 TUI**: tmux 自体の機能(attach / mouse mode)で代替できる。fanout が再実装する層ではない
- **sidebar layout の再実装**: wave 2 は tmux-parity の協調プロセス信頼で exact target を直前・直後に再照合し、pane / workspace の表示専用 token 値を報告できる。
  authoritative server generation と target `terminal_id` / workspace generation を request に原子的に束縛できる場合は proof-grade tier へ格上げする。
  Space / Agent の `rows` と `row_gap`、Agent の `rows_by_agent`、styling は herdr とユーザーが所有し、fanout は herdr config を書き換えない

## 参考

- https://herdr.dev/ / https://github.com/ogulcancelik/herdr
- v0.7.4 release / sidebar config: https://github.com/ogulcancelik/herdr/releases/tag/v0.7.4 / https://herdr.dev/docs/config-reference/
- v0.7.5 release / agent automation: https://github.com/ogulcancelik/herdr/releases/tag/v0.7.5 / https://herdr.dev/docs/agent-automation/
- v0.7.5 発表スレッド(2026-07-21): https://x.com/herdrdev/status/2079634095047413886 — agent CLI 紹介とデモ動画は同スレッド .../2079634098197348518
- plugin marketplace: https://herdr.dev/plugins/ — GitHub topic `herdr-plugin` の自動収集
- Socket API: https://herdr.dev/docs/socket-api/ — `pane.*` / `agent.*` / `events.subscribe/wait` / worktree 操作
- Agent skill: https://herdr.dev/docs/agent-skill/ — SKILL.md によるエージェント自己オーケストレーション
- Integrations: https://herdr.dev/docs/integrations/ — lifecycle authority 型(状態を直接報告)と session identity 型(復元用 session 参照)の 2 系統
- 比較ページ: https://herdr.dev/compare/ — tmux / Zellij / cmux / Warp / Solo / OpenCode(web モード)/ Conductor / Emdash / Superset との対比。他ツールが取りこぼす「永続ランタイム × エージェント状態認識」の交差点("The intersection other tools miss")という位置づけ
- 関連 issue: #59(Wave 自動進行)/ #106(PR レビュー追従)/ #72(nudge、closed)/ #361(opencode)/ #241(cursor / copilot)
