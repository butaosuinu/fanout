# TUI compact 表示とペイン移動 — herdr switcher の取り込み設計

ステータス: 設計。作成: 2026-07。kazuph 氏の herdr 運用記(参考節)と fanout 実コード(`internal/tui` / `internal/panelayout` / `internal/tmuxrun`)の検証に基づく。競合分析 `competitive-herdr.ja.md` の UI 面の続編で、TUI コンソールの縮小表示とペイン移動導線を設計する。

## 問題: 40 桁サイドバーで一覧が読めない

agent ペインを作ると auto-layout が TUI コンソールペインを幅 40 桁の左サイドバーに固定する(`internal/panelayout/layout.go` の `SidebarWidthDefault = 40`)。一方 TUI の一覧は bubbles table の 14 列で、列幅の合計は最小でも約 124 桁ある(`internal/tui/paneview.go` の `columnsForWidth`)。幅 40 では PARENT・ISSUE・WAVE あたりまでしか入らず、NAME・agent 状態・PR・CI・ペイン ID は画面外になる。fanout の主運用形態(コンソール + agent グリッド)で、コンソールの一覧はほぼ読めない。

レイアウト分岐(`internal/tui/view.go` の `monitorLayout`)は「幅 120 以上で Session サイドバー、それ未満はトップストリップ」の 2 段階だけで、どちらもテーブル + 固定 13 行の詳細という形は変わらない。狭い幅で表示形態そのものを切り替える仕組みがない。

表示項目にも穴がある。`@fanout_agent_state`(running / done)は sessionview から TUI まで届いているのに、一覧にも詳細にも出していない(フィルタ `run:running` でだけ使える)。ペインが動いているかを一目で判別できない。

移動導線は片方向で、enter / o で選択ペインへ飛べる(`internal/tui/focus.go` → `tmux switch-client`)が、コンソールへ戻るのは手動の tmux 操作だけになっている。

## herdr の知見

運用記事から fanout に効く観察は 4 つ。

1. 狭い端末ではステータスライン的な圧縮表示を諦め、ワークスペースと Agent を縦に並べた switcher に切り替える。細い表示を読むより一覧から選ぶ方が楽
2. pane ID(%1 %2)の常時表示。エージェント間の指示が「%2 の Claude に review 結果を投げて」と書けるようになる
3. agent 状態(working / blocked / idle)の色分け
4. 通知から該当ペインへワンアクションで遷移できる復帰導線

## 設計 1: compact(switcher)表示モード

幅 80 未満で、テーブル + 詳細を捨てて縦リスト(switcher)に切り替える。

### breakpoint とトグル

- `monitorLayout` に `Compact` を追加し、幅 80 未満(新定数 `compactWidthAt`)で compact にする。80〜119 の帯は現行のトップストリップ + テーブルが辛うじて機能するので変えない(既存テスト `TestNarrowShortLayoutCollapsesTopStrip` は幅 90 を pin しており無変更で通る)
- `v` キーで auto → compact → full をサイクルする手動オーバーライドを持つ(model に `viewOverride`、セッションローカルで永続化しない)。広い画面で switcher を流し見したい・狭くてもテーブルの生値を見たい、の両方向の脱出ハッチ
- 表示切替は `monitorLayout()` の返り値が変わるだけで tmux を触らない。panelayout の relayout デバウンスとのフィードバックループは構造的に発生しない

### 描画: table は状態、描画は自前

bubbles table は捨てず、compact 時は `View()` の分岐でレンダリングだけ自前にする(新規 `internal/tui/compact.go`)。選択状態の単一情報源は `m.table.Cursor()` で、`selectedPane()` / j・k / enter / peek / c・m・x・X / `[` `]` / フィルタはすべてこのカーソルを参照するから、キー処理は無変更で効く。

### 40 桁 1 行フォーマット

```
▏100 exit-plan-mode  t5 m2 b1        ← Session ヘッダ
>1● #101 rate-limiter-core       %5
 2✓ #102 api-cache               %8
   ⎇ fanout/issue-102 PR#77 open    ← 選択行のみ展開
   ci:fail W2 blk:#99 dirty
 3◆ #103 docs-site              %12
```

- 1 ペイン 1 行: 序数(1〜9、数字ジャンプの対応表示)+ 状態グリフ + itemLabel(`#N` / taskID / shell)+ Name + 右寄せの pane ID。幅が足りないときは Name だけを削る
- Session ヘッダは `compactParent` と `sessionSummary` のカウンタを再利用する
- 選択行の直下だけ 2〜3 行展開する: ブランチ + PR、ci / wave / blockers / dirty、peek の末尾 1 行。固定 13 行の詳細 viewport は compact では描画しない。peek の取得パイプラインは無変更で、表示だけ縮める
- スクロールは `sessionRows`(`internal/tui/session.go`)と同じ「アクティブ中心のスライド窓 + `...`」パターンの純関数にする

### 状態グリフ

グリフの単一情報源として `agentStateGlyph` を新設する: running `●` / done `✓` / stale `✗` / live だが状態不明 `·`。`competitive-herdr.ja.md` 提案 A の 5 値化(working / idle / blocked)を先行してマップに載せておき、5 値化の実装時は sessionview の `normalizeAgentState` の許可リスト拡張だけで表示側の変更をゼロにする。ペイン内容からの状態推定はしない(同 doc の判断記録どおり)。

フルテーブル側にも RUN 列(AgentState)と詳細の `run=` フィールドを足し、compact 専用の情報差を作らない。

## 設計 2: ペイン移動

### コンソール復帰キー(F11 / prefix T)

dashboard の前例(`BindDashboardKeys`、F12 / prefix D)と対にして、root `F11` + prefix `T` を登録する。バインド内容は `run-shell '<fanout-bin> focus-console --from "#{pane_id}"'`。pane id をバインドに焼き込む方式は、コンソール再起動で stale になり複数リポジトリの登録が上書き合戦になるため採らない。バイナリ経由なら押下のたびに探索するので自己修復的に働く。

新サブコマンド `fanout focus-console` は、`@fanout_role=console` のペインを列挙(`internal/tmuxrun` に `ListConsolePanes` を追加)し、①呼び出しペインと同一 session → ②`@fanout_project_root` 一致 → ③最初の console の順で選んで `SelectPane`(switch-client)で復帰する。console 不在時は `tmux display-message` で知らせて正常終了する。settings には `ConsoleKeybind`(default true、JSON `consoleKeybind` / env `FANOUT_CONSOLE_KEYBIND`)を `DashboardKeybind` と同型で足し、キー衝突時に個別に切れるようにする。登録は live の TUI 起動時のみで、dry-run 出力には何も足さないので Tier 2 golden への影響はない。

### 数字ジャンプ(1〜9)

monitor モードの `1`〜`9` を「表示リストの N 番目を選択して即ジャンプ」にする(`moveTableCursorTo` + `focusSelectedCmd` の合成)。compact 行の序数がその対応表示になる。monitor モードで数字キーは未使用で、close の選択肢(pendingAction 中の 1〜3)とフィルタ編集は monitor 分岐より先に処理されるため衝突しない。

### zoom(`Z`)

`Z` = focus + `resize-pane -Z` を opt-in の別キーとして足す。enter / o の挙動は変えない。次の relayout(select-layout)が zoom を解除するのは仕様とする — auto-layout のグリッドは comfortable 幅を保証しているので、zoom は常用ではなく一時拡大の位置づけ。

## 実装分割

親 issue + 子 4 つ。wave 1 の 3 つは互いに独立で並列実装できる。

| ID | 内容 | 規模 | 依存 |
|---|---|---|---|
| C1 | AgentState 表示(`agentStateGlyph` / RUN 列 / 詳細 `run=`) | S | なし |
| C2 | console 復帰(`BindConsoleKeys` / `focus-console` / settings) | M | なし |
| C3 | 数字ジャンプ 1〜9 + `Z` zoom | S | なし |
| C4 | compact switcher レンダラ + `v` トグル | M〜L | C1, C3 |

C4 は C1(グリフ)と C3(序数表示)に blocked。C2 は触るパッケージ(tmuxrun / cmd)が C1・C3(internal/tui)と別で完全に独立している。

テストは internal/tui の流儀(純関数 + Options 注入フェイク)に従う。pin する主な挙動: compact の 40 桁行フォーマット、閾値 80 と `v` の 3 状態サイクル、compact 中も enter / c が `selectedPane` に効くこと、`pickConsolePane` の優先順、数字ジャンプと close 選択肢の非衝突。README.md / README.ja.md のキーバインド節と in-app help(`internal/tui/help.go`)は各 PR で同期する。

## やらないこと(判断記録)

- ペイン移動履歴(herdr の Ctrl+[ / Ctrl+]): fanout の移動はハブ & スポーク(console 復帰 + 数字ジャンプ)で往復をカバーし、直前ペインとの往復は tmux 標準の `prefix + ;`(last-pane)に任せる。履歴スタックの自前実装は「tmux の上のレイヤーに留まる」判断(`competitive-herdr.ja.md`)に反する
- 通知クリックでのペイン遷移: OS 通知側のアクション機構に依存するため対象外。復帰キー + 数字ジャンプが代替導線
- ペイン内容からの状態推定(`competitive-herdr.ja.md` の判断記録どおり)
- compact 行フォーマットのユーザー設定: まず固定フォーマットで運用し、必要が出てから考える

## 参考

- https://kazuph.github.io/blog/2026/05/28/tmux-herdr-agent-multiplexer/ — herdr(fork)運用記。switcher / pane ID 常時表示 / 状態色分け / 復帰導線の観察元
- `docs/competitive-herdr.ja.md` — 競合分析と取り込み提案(A: 状態 5 値化 / B: `fanout wait` / C: session resume)。本書の C1 は A の表示側を先行する
- 関連 issue: #59(Wave 自動進行)/ #106(PR レビュー追従)
