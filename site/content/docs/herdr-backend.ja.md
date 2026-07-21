---
title: herdr backend
linkTitle: herdr backend
description: "opt-in・観測専用の herdr runtime backend。前提条件、backend 選択の仕組み、tmux との差分、plugin の注意をまとめます。"
weight: 90
kanji: 観
yomi: herdr
---

herdr backend は、[herdr](https://herdr.dev/)(コーディングエージェント向けの tmux 代替・永続 PTY ランタイム)の中で fanout を read-only コンソールとして動かすための backend です。
opt-in で、v1 は観測専用です。fanout は記録済みの herdr pane を表示しますが、作成・変更・close は一切しません。
既定の backend は tmux のままで、herdr session の外では tmux 利用者の workflow は何も変わりません。herdr session の中では、上書きしない限り herdr が自動選択で勝ちます(後述)。
fanout は herdr を同梱しません。herdr は AGPL ライセンスで、別途インストールします。

## v1 でできること

名前付きの herdr session の中で fanout を起動すると、read-only な画面 — 常駐 TUI コンソール、`--status`、web ダッシュボード — にリポジトリの記録済み session が、各 pane の runtime backend と identity 付きで表示されます([モニタリング]({{< relref "/docs/monitoring" >}})を参照)。
TUI コンソールと web ダッシュボードは、herdr backend で記録された行を `herdr api snapshot` と照合して生死と agent state を反映します。`--status` が読むのは記録済みの state と GitHub だけです。
fanout は `herdr --version`、`herdr api schema --json`、`herdr pane --help`、`herdr workspace --help`、`herdr worktree --help`、`herdr status --json` で herdr を検証します。
名前付き session を最初に解決するときだけ、status は `herdr --session <name> status --json` になります。
admission 後は `herdr api snapshot` で生死を読みます。
schema gate は必要な request / response method と field を検査します。
help gate は対象を読み書きせず、pane read/run/close、workspace focus/close、worktree remove のコマンド形式を検査します。

herdr session を変更しうる操作は、劣化動作ではなく明確なエラーで fail closed します。

- issue / Project / plan の launch は、worktree や state の変更が起きる前に拒否されます。v1 は herdr 行を自分では記録しません。
- focus・send・close・restore・出力 peek・plan capture・自動 cleanup は herdr 行では使えません。
- 自動 nudge(`fanout msg nudge` の配送)は agent の種類にかかわらず無効です。メッセージ自体は bus に保存され、`inbox` / `board` で読めます。
- tmux keybind は登録されず、herdr のアプリ内通知 `notification show` も呼ばれず、Codex Plan Mode は使えません。

TUI のヘッダには、選択された backend とその理由が常に表示されます。例: `backend: herdr (HERDR_ENV)`。

## 前提条件

- **herdr stable 0.7.4 以降**：`herdr --version`、status の client/server、各 snapshot は、同じ admitted version を返す必要があります。
  status の client は stable channel と protocol 16、server は running、protocol 16、`compatible: true`、restart 不要である必要があります。
- **互換性のある API とコマンド surface**：API schema 文書は protocol 16、schema version 1 と必要な request / response 構造を返す必要があります。
  新しい version は schema gate と CLI help gate の両方を通る場合だけ受理されます。
- 明示的な名前を付けた herdr session が稼働していること(`default` は拒否されます)。fanout は herdr server を起動せず、session の作成も attach もしません。
- `PATH` 上の `herdr` バイナリ。別途インストールしてください。

## opt-in の手順

1. 名前付きの herdr session を自分で起動します([herdr のドキュメント](https://herdr.dev/docs/)を参照)。
2. その session の pane の中で `fanout` を実行します。herdr が `HERDR_ENV=1` を設定するので、fanout は自動で herdr backend を選びます。
3. TUI コンソール、`--status`、web ダッシュボードで記録済みの行を読みます。

launch が fail closed するため、v1 は herdr 行を自分では記録しません。herdr backend の行が存在するのは、通常の launch フローの外(手動の実験)で書かれた場合だけです。日常的には、herdr 内のコンソールはリポジトリの記録済み session の read-only ビューです。

backend は次の順で解決され、最初に一致したものが使われます。

1. 親に記録済みの backend(stickiness — 後述)
2. `--backend <tmux|herdr>`
3. `FANOUT_BACKEND`
4. 環境変数 `HERDR_ENV=1`
5. `TMUX` があれば tmux
6. user config の `runtimeBackend`
7. 既定: `tmux`

`HERDR_ENV` と `TMUX` の両方がある場合 — herdr の中で tmux を入れ子にしている場合 — は herdr が勝ちます。`FANOUT_BACKEND=tmux` で上書きするか、フラグを受け付ける launch 系コマンドでは `--backend tmux` も使えます(引数なしのコンソールが読むのは環境変数と config だけです)。
`runtimeBackend` は user config 専用のキーです。repo config では設定できず、警告付きで無視されます([設定]({{< relref "/docs/settings" >}}))。

記録済みの pane を持つ親は、記録された backend を使い続けます。
矛盾する `--backend` や `FANOUT_BACKEND` は、1 つの親の下で backend が混ざらないよう `explicit migration is required` で失敗します。
v1 に移行コマンドはありません。既存の tmux 親は tmux のままです。

## tmux との差分

| 機能 | tmux backend | herdr backend v1 |
|---|---|---|
| issue / Project / plan の launch | worktree・pane・agent を作成 | worktree / state の変更前に拒否 |
| worktree 作成 | 子ごとに `.fanout/worktrees/` 配下へ | しない |
| 生死と agent state(TUI コンソール、web ダッシュボード) | tmux へ照会 | `herdr api snapshot` — 対応 |
| exit status 表示 | launch wrapper が `✓ done` を報告 | なし — herdr の public API に exit status は残らない |
| agent 終了後の pane | wrapper のメッセージ付きで pane が残る | 正常終了で herdr は pane と自身の記録を消す。fanout の行は `stale` になる |
| focus / send / close / restore / peek / plan capture | TUI キーと lifecycle フラグ | 不可 — `runtime backend herdr does not support …` |
| 自動 cleanup(`--cleanup`) | merge/close 済み pane を畳む | 拒否。herdr workspace は herdr 側で片付ける |
| 自動 nudge(`fanout msg nudge`) | 相手が入力を受けられる状態なら配送 | agent の種類にかかわらず無効 |
| tmux keybind(ダッシュボード、コンソール復帰) | 登録する | 登録しない |
| 通知 | bell / tmux / ntfy / slack の channel | bell / ntfy / slack は動く。tmux channel と herdr の `notification show` は発火しない |
| Codex Plan Mode | `codexPlanMode` で opt-in | 非対応 |
| TUI フォーム(設定、ヘルプ) | tmux popup | インラインの in-process フォーム |
| session resume | fanout の restore フロー | herdr 任せ(後述) |

補足が 2 点あります。
`terminal_id` が変わった herdr pane — たとえば server の cold restart 後 — は、再束縛されずに `stale` と表示されます。
また herdr は exit status を残さず、正常終了で pane の記録も消えるため、終了した agent は `✓ done` の pane を残さずに herdr session から消えます。記録済みの fanout の行は残り、`stale` と表示されます。

## herdr の integration と plugin

`herdr integration install claude` / `codex` は、agent の session identity を herdr に報告する hook をあなたの agent 設定に書き込みます。herdr の session 追跡と復元はこれで機能します。
fanout はこれを代行しません。agent 設定の所有者はあなたです。
任意の手順です。restore に頼るなら検討してください。

plugin の注意が 2 点あります。

- herdr の通知 plugin(ntfy、モバイル push)は、fanout 自身の `ntfy` / `slack` channel と並行して発火します。両方を有効にすると同じイベントの通知が二重になるので、どちらかに絞ってください。
- herdr の worktree setup 系 plugin は、herdr が作成・open するすべての worktree で動きます。手動で herdr から fanout の worktree を open した場合も対象です。v1 の fanout 自身は herdr の worktree 操作を発行しません。

2 つのツールは別の層にあります。
herdr の plugin は並列エージェント作業を runtime 側から扱います: GitHub や Jira を起点にした worktree 起動、diff レビューの sidebar、複数プロジェクトの sidebar、レイアウトや通知の plugin。
fanout は GitHub ワークフロー側から扱います: 親子の fan-out、briefing 生成、blocker の wave、PR のライフサイクル、レビューゲート。
herdr が pane を実行・表示し、fanout が作業を計画して(tmux 上で)起動し、GitHub 側を追跡します。

## 旧 fanout バイナリ

旧版の fanout バイナリは `.fanout/state.json` の herdr フィールドを未知のキーとして読み飛ばします。
herdr 行は stale と表示され、旧版の `--close` は herdr workspace を残します。herdr 側で片付けてください。
旧版が state を書き込むと、知っているフィールドだけを保存するため、行から herdr の identity が落ちます。

`--backend` フラグと `FANOUT_BACKEND` は [CLI リファレンス]({{< relref "/docs/cli" >}})、`runtimeBackend` キーは[設定]({{< relref "/docs/settings" >}})、herdr のエラーメッセージと対処は[トラブルシューティング]({{< relref "/docs/troubleshooting" >}})にあります。
