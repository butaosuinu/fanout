---
title: herdr backend
linkTitle: herdr backend
description: "opt-in の herdr runtime backend。owned CLI launch、前提条件、backend 選択、tmux との差分、plugin の注意をまとめます。"
weight: 90
kanji: 観
yomi: herdr
---

herdr backend は、コーディングエージェント向けの永続 PTY ランタイム [herdr](https://herdr.dev/)で CLI のファンアウトを実行します。
opt-in で、issue、Project、plan、label watcher の launch はリポジトリ単位の fanout-owned session を使います。
引数なしの TUI は herdr 上で read-only のままです。
既定の backend は tmux です。
fanout は herdr を同梱しません。herdr は AGPL ライセンスで、別途インストールします。

## v1 でできること

CLI launch では、fanout がリポジトリの owned session を起動または再採用し、プロジェクトルートの coordinator workspace と子ごとの worktree workspace を作ります。
選択した agent は pin 済みの non-login fanout launcher から起動されます。
launcher は operation-bound token を 1 回だけ受け取り、所有者だけが読める environment capsule を 1 回だけ消費して、shell を介さず agent に置き換わります。
fanout は launch の検証後に限り、workspace、pane、terminal、repository、agent、session、socket の identity を `.fanout/state.json` へ保存します。
インストール済みの herdr integration が provider session の identity を報告した場合は、その値も保存します。

常駐 TUI コンソール、`--status`、web ダッシュボードには、記録済み session と各 pane の runtime backend、identity が表示されます([モニタリング]({{< relref "/docs/monitoring" >}})を参照)。
TUI コンソールと web ダッシュボードは、herdr backend で記録された行を `herdr api snapshot` と照合して生死と agent state を反映します。`--status` が読むのは記録済みの state と GitHub だけです。
fanout は session の読み書き前に `herdr --version` と owned route を検査します。
public method の呼び出しが失敗した場合は `herdr method "<name>" is unavailable` を返します。

未対応の経路は明確なエラーで fail closed します。

- 対話 TUI の launch、focus、send、restore、出力 peek、plan capture は herdr 行では使えません。
- herdr と `--team` の組み合わせは、state、filesystem、Git、herdr の変更前に拒否されます。
- Codex 子の Plan Mode は拒否されます。app-server の launch matrix が対応するまでは build mode を使ってください。Claude と OpenCode は固有の mode flag を使います。
- 自動 nudge(`fanout msg nudge` の配送)は agent の種類にかかわらず無効です。メッセージ自体は bus に保存され、`inbox` / `board` で読めます。
- tmux keybind は登録されず、herdr のアプリ内通知 `notification show` も呼ばれません。

TUI のヘッダには、選択された backend とその理由が常に表示されます。例: `backend: herdr (HERDR_ENV)`。

## 前提条件

- **stable herdr 0.7.5 以上** — CLI と server は同じ stable version で動かしてください。prerelease と解釈できない version は fail closed します。新しい stable version を protocol、API schema、CLI help、platform、release digest では拒否しません。
- `PATH` 上の `herdr` バイナリ。別途インストールしてください。
- `PATH` 上の選択済み agent CLI。

CLI launch に既存の herdr session は不要です。
fanout が owner marker 配下の隔離 session を作成または再採用します。
引数なしの TUI で外部 session を観測する場合は、その名前付き session の pane からコンソールを起動してください(`default` は拒否されます)。

## opt-in の手順

CLI run ごとに herdr を明示できます。

```bash
fanout 123 --backend herdr --agent claude
fanout plan launch-plan --backend herdr --agent claude
```

繰り返し使う場合は `FANOUT_BACKEND=herdr` または user config の `runtimeBackend` を設定します。
既存の herdr pane 内では `HERDR_ENV=1` により自動選択されます。
label watcher も同じ owned launch path を使い、launch ごとに session を再検証します。

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
| issue / Project / plan / watcher の launch | worktree、pane、agent を作成 | owned herdr workspace と検証済み agent を作成 |
| worktree 作成 | 子ごとに `.fanout/worktrees/` 配下へ | 子ごとに `herdr worktree create` / `open` を実行 |
| 生死と agent state(TUI コンソール、web ダッシュボード) | tmux へ照会 | `herdr api snapshot` — 対応 |
| exit status 表示 | launch wrapper が `✓ done` を報告 | なし — herdr の public API に exit status は残らない |
| agent 終了後の pane | wrapper のメッセージ付きで pane が残る | 正常終了で herdr は pane と自身の記録を消す。fanout の行は `stale` になる |
| 対話 TUI launch / focus / send / restore / peek / plan capture | TUI キーと lifecycle フラグ | 不可 — `runtime backend herdr does not support …` |
| 自動 nudge(`fanout msg nudge`) | 相手が入力を受けられる状態なら配送 | agent の種類にかかわらず無効 |
| tmux keybind(ダッシュボード、コンソール復帰) | 登録する | 登録しない |
| 通知 | bell / tmux / ntfy / slack の channel | bell / ntfy / slack は動く。tmux channel と herdr の `notification show` は発火しない |
| 子の Plan Mode launch | 対応 | Claude / OpenCode のみ。Codex は拒否 |
| TUI フォーム(設定、ヘルプ) | tmux popup | インラインの in-process フォーム |
| session resume | fanout の restore フロー | herdr 任せ(後述) |

補足が 2 点あります。
`terminal_id` が変わった herdr pane — たとえば server の cold restart 後 — は、再束縛されずに `stale` と表示されます。
また herdr は exit status を残さず、正常終了で pane の記録も消えるため、終了した agent は `✓ done` の pane を残さずに herdr session から消えます。記録済みの fanout の行は残り、`stale` と表示されます。

## sidebar token

検証を通った launch ごとに、source `fanout` で表示専用の token を 5 つ報告します。sidebar の行にどのファンアウト子かを出せます。
token は表示専用データです。fanout は読み戻さず、backend state、生死、完了判定にも使いません。

| token | resource | 値 |
|---|---|---|
| `fanout_issue` | workspace | issue / Project の子は `#<issue>`、`fanout plan` のタスクは task ID |
| `fanout_slug` | workspace | 子の slug。worktree ディレクトリ名と同じ |
| `fanout_parent` | pane | `#<parent>`、`plan:<slug>`、Projects のパス |
| `fanout_pr` | pane | PR 用に予約。現時点では報告のたびに clear |
| `fanout_ci` | pane | CI 用に予約。現時点では報告のたびに clear |

1 回の報告でその resource の fanout token 一式を書き、値を持たないものは clear します。使い回された workspace や pane に古い値が残りません。
報告は launch が生きた identity を検証した直後の 1 回だけで、`seq` も `ttl_ms` も送りません。
herdr の cold restart では token がすべて消え、`terminal_id` も変わるため fanout の行は `stale` になります。再送はしません。

行とスタイリングはあなたのものです。fanout は sidebar の設定を書き換えません。token は `$name` で参照します。

```toml
[ui.sidebar.spaces]
rows = [["state_icon", "workspace"], ["$fanout_issue", "$fanout_slug"], ["branch", "git_status"]]

[ui.sidebar.agents]
rows = [["state_icon", "workspace", "tab"], ["$fanout_parent", "$fanout_pr", "$fanout_ci"], ["agent"]]
```

fanout-owned session は自分の `config.toml` を固定するため、この例は自分で設定する herdr session に適用します。
fanout-owned session の中では token を `herdr api snapshot` で読めますが、参照する sidebar の行はまだありません。

## herdr の integration と plugin

`herdr integration install claude` / `codex` は、agent の session identity を herdr に報告する hook をあなたの agent 設定に書き込みます。herdr の session 追跡と復元はこれで機能します。
fanout はこれを代行しません。agent 設定の所有者はあなたです。
任意の手順です。restore に頼るなら検討してください。

fanout-owned session は herdr の XDG directory を隔離し、workspace / worktree 作成前に plugin registry が空であることを要求します。
fanout-owned launch では herdr の通知 plugin と worktree setup plugin は動きません。
registry が空でない場合は mutation 前に launch が失敗します。通知と setup には fanout の channel と hook を使ってください。

2 つのツールは別の層にあります。
fanout-owned session 以外では、herdr の plugin が並列エージェント作業を runtime 側から扱います: GitHub や Jira を起点にした worktree 起動、diff レビューの sidebar、複数プロジェクトの sidebar、レイアウトや通知の plugin。
fanout は GitHub ワークフロー側から扱います: 親子の fan-out、briefing 生成、blocker の wave、PR のライフサイクル、レビューゲート。
herdr が pane を実行・表示し、fanout が作業を計画して tmux または herdr で起動し、GitHub 側を追跡します。

## 旧 fanout バイナリ

旧版の fanout バイナリは `.fanout/state.json` の herdr フィールドを未知のキーとして読み飛ばします。
herdr 行は stale と表示され、旧版の `--close` は herdr workspace を残します。herdr 側で片付けてください。
旧版が state を書き込むと、知っているフィールドだけを保存するため、行から herdr の identity が落ちます。

`--backend` フラグと `FANOUT_BACKEND` は [CLI リファレンス]({{< relref "/docs/cli" >}})、`runtimeBackend` キーは[設定]({{< relref "/docs/settings" >}})、herdr のエラーメッセージと対処は[トラブルシューティング]({{< relref "/docs/troubleshooting" >}})にあります。
