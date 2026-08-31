---
title: herdr backend
linkTitle: herdr backend
description: "opt-in の herdr runtime backend。owned CLI launch、前提条件、backend 選択、tmux との差分、server の restart と shutdown、plugin の注意をまとめます。"
weight: 90
kanji: 観
yomi: herdr
---

herdr backend は、コーディングエージェント向けの永続 PTY ランタイム [herdr](https://herdr.dev/)で CLI のファンアウトを実行します。
opt-in で、issue、Project、plan、label watcher、対話 TUI の launch はリポジトリ単位の fanout-owned session を使います。
引数なしの TUI は、identity を検証した owned 行に merge、close、cleanup も実行できます。
既定の backend は tmux です。
fanout は herdr を同梱しないので、別途インストールしてください。
v0.8.0 以降は Apache-2.0、0.7.x は AGPL-3.0 + 商用のデュアルライセンスです。

## できること

CLI launch では、fanout がリポジトリの owned session を起動または再採用し、プロジェクトルートの coordinator workspace と子ごとの worktree workspace を作ります。
選択した agent は pin 済みの non-login fanout launcher から起動されます。
launcher は operation-bound token を 1 回だけ受け取り、所有者だけが読める environment capsule を 1 回だけ消費して、shell を介さず agent に置き換わります。
direct claude と direct Codex には公式 session report に必要な owned socket と exact pane ID だけを渡し、session / workspace route は渡しません。
fanout は launch の検証後に限り、workspace、pane、terminal、repository、agent、session、socket の identity を `.fanout/state.json` へ保存します。
インストール済みの herdr integration が provider session の identity を報告した場合は、その値も保存します。

owned session は自分の `config.toml` を固定します。既定 shell は fanout の launcher(non-login)、herdr の復元時 agent resume は off、herdr の update manifest check も off です。`dashboardKeybind` が有効なら、focus 中の pane から web ダッシュボードを開く Herdr の `F12` shell command も登録します。
fanout の知らないところで agent が起動し直されることはありません。resume は後述の明示操作だけです。

`gh` の認証に `GH_TOKEN` または `GITHUB_TOKEN` を使う場合は、その変数を持つ shell から owned session を作成してください。
fanout は token を内部 control-plane 名で owned server へ渡し、ownership marker には SHA-256 fingerprint だけを保存します。`config.toml` と dashboard descriptor に token の値は書きません。
live server が現在の token を継承していない場合は F12 keybind を削除します。子の行を close または cleanup し、`fanout herdr shutdown` を実行してから、token を持つ shell で再起動してください。

常駐 TUI コンソール、`--status`、web ダッシュボードには、記録済み session と各 pane の runtime backend、identity が表示されます([モニタリング]({{< relref "/docs/monitoring" >}})を参照)。
TUI コンソールと web ダッシュボードは、herdr backend で記録された行を `herdr api snapshot` と照合して生死と agent state を反映します。
`--status` が読むのは記録済みの state と GitHub だけです。
owned console 内の TUI は issue、Prompt、attached-agent、shell の pane を起動し、focus と出力 peek を実行できます。
ダッシュボードも mutation endpoint を追加せず、owned 行の出力を peek できます。
fanout は session の読み書き前に `herdr --version`、owned route、保存済み workspace ownership label を検査します。
public method の呼び出しが失敗した場合は `herdr method "<name>" is unavailable` を返します。

Claude の launch には、launch 単位の `--settings` hook を渡します。
Codex Plan Mode の launch には、同じ launch-bound emitter environment を hook なしで渡し、fanout の app-server controller が `working`、`plan`、`idle` を報告します。
emitter は `working`、`plan`、`blocked`、`idle`、`done` を受理し、Claude の lifecycle hook は hook event から判定できる状態を報告します。
fanout が報告を受理するのは、row key、launch nonce、emitter nonce、保存済み pane identity、現在の herdr identity、agent process が一致した場合だけです。
検証済み launch の初期値は synthetic な `reported_state: running` であり、最初の provider 報告を受理すると `state_refinement: true` になります。
Plan Mode 以外の Codex と OpenCode の launch には emitter を設定しません。

TUI コンソールと web ダッシュボードが `reported_state` を使うのは、一致する pane と agent が live の場合だけです。
`--status --format json` は `reported_state` を含み、table 形式は `REPORTED_STATE` 列へ表示します。
この値は issue の完了や cleanup の許可には使いません。
自動 nudge に使うのは、current launch の telemetry によって `state_refinement: true` となり、live pane、worktree、agent、process identity の再照合を通った場合だけです。
pane が消えた場合は `stale` のままです。

`--merge`、`--close`、`--cleanup`、対応する TUI 操作は、owned session の identity がそろった行だけを対象にします。
fanout は保存済みの workspace ID、label、terminal、repository、path、branch を mutation 前に照合します。
cleanup intent を保存してから non-force の `herdr worktree remove` を発行し、checkout と workspace の不在を確認します。workspace だけが残れば close します。
先に workspace が閉じられて checkout が残った場合は、owned plugin registry が空であることを確認してから削除用 workspace を再登録します。

fanout は remove の発行前に、tracked / untracked の作業と ignored file を区別します。
どちらが残っていても herdr mutation を発行せず、原因を分けて表示します。再試行時は checkout を再検査します。
`dirty_worktree_requires_force` で manual cleanup になった保存済み intent は、現在の checkout と workspace から再計画するため、変更を commit または削除したあとに続行できます（[#721](https://github.com/butaosuinu/fanout/issues/721)）。
発行結果が曖昧な intent は再発行せず、manual cleanup のまま保持します。
branch は fanout-created と記録されたものだけを compare-and-delete します。

### cleanup の復旧

cleanup error に表示された worktree path で残存内容を確認します。

```bash
git -C "<worktree>" status --short --untracked-files=all --ignored
```

tracked の作業は commit または stash し、untracked file は commit または別の場所へ退避してから再試行してください。
ignored file だけなら、削除対象を確認してから ignored file だけを削除します。

```bash
git -C "<worktree>" clean -ndX
git -C "<worktree>" clean -fdX
```

ignored ディレクトリ内に別の Git repository がある場合は、`-f` を 2 回指定しないとそのディレクトリをスキップします。
削除する場合はディレクトリの内容を確認してから `git -C "<worktree>" clean -ffdX` を実行するか、対象ディレクトリを直接削除してください。
`-ff` はその Git repository と全内容を削除します。

元の `--close` / `--cleanup` または TUI 操作を再実行します。
checkout の内容で停止した場合と、保存済みの `dirty_worktree_requires_force` から復旧する場合は、worktree だけを別途削除しても再試行が残存 workspace を close します。

曖昧な manual-cleanup error では `.fanout/state.json` と intent journal を削除しないでください。
fanout-owned herdr session に attach した shell で、保存済み pane ID の workspace prefix（`w7:p1` なら `w7`）を使います。
現在の状態に合うコマンドだけを実行します。その後 fanout を再試行し、不在確認と保存済み行の削除を完了させます。

```bash
# worktree が残っている場合
herdr worktree remove --workspace <workspace-id> --json
# workspace だけが残っている場合
herdr workspace close <workspace-id>
```

未対応の経路は明確なエラーで fail closed します。

- 対話 send、restore、plan capture は herdr 行では使えません。
- TUI の focus、launch、peek には、fanout-owned session に属する完全な保存済み identity が必要です。foreign、stale、legacy の行は理由付きで無効になります。
- focus はさらに agent session を保存済みの行だけで通ります。session を報告するのは agent integration なので、`herdr integration install claude` / `codex` を入れていない環境では focus が拒否されます。integration を入れても通らない行が 3 種類あります。attach で起動した agent ([#732](https://github.com/butaosuinu/fanout/issues/732))、Codex の Plan Mode と team (workload が provider ではなく fanout の controller)、OpenCode です。いずれも socket 系の環境変数を渡さないので hook が session を報告できません。
- 同じ provider が同じ pane で新しい会話を始めると (claude の `/clear`、codex の `/new`)、herdr は conversation を差し替え、agent record の名前も落とします。行が保存した conversation はそちらへ追随し、落ちた名前は fanout が付け直すので、focus、peek、`--close`、`--cleanup` は前後で使えたままです。別の provider の conversation、runtime が発行していない ref、他の名前を名乗っている agent record は従来どおり拒否します。
- Codex 子の Plan Mode は fanout の app-server controller と owned launcher で動きます。Claude と OpenCode は固有の mode flag を使います。
- Herdr に登録するのはダッシュボード用の direct `F12` command だけです。tmux 専用の `prefix + D`、`prefix + M`、コンソール復帰 keybind、herdr のアプリ内通知 `notification show` は使いません。

TUI のヘッダには、選択された backend とその理由が常に表示されます。例: `backend: herdr (HERDR_ENV)`。

## 前提条件

- **stable herdr 0.7.5 以上** — CLI と server は同じ stable version で動かしてください。prerelease と解釈できない version は fail closed します。新しい stable version を protocol、API schema、CLI help、platform、release digest では拒否しません。
- `PATH` 上の `herdr` バイナリ。別途インストールしてください。
- `PATH` 上の選択済み agent CLI。

CLI launch と引数なしの TUI に既存の herdr session は不要です。
fanout が owner marker 配下の隔離 session を作成または再採用します。
foreign な herdr session 内で起動した TUI は観測専用のままで、対話操作に owned-session authority は与えられません(`default` は拒否されます)。

## opt-in の手順

CLI run ごとに herdr を明示できます。

```bash
fanout 123 --backend herdr --agent claude
fanout plan launch-plan --backend herdr --agent claude
fanout 123 --backend herdr --agent codex --team
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
backend の移行コマンドはありません。既存の tmux 親は tmux のままです。

## owned session を起動する

素のシェルからの引数なし `fanout` が session を bootstrap し、そのまま中に入ります。

```bash
export FANOUT_BACKEND=herdr
fanout
```

この実行で owned session と repository root の console workspace が 1 つ起動または再採用され、fanout プロセスは pin 済みの herdr client に置き換わります。端末は attach 済みで、コピーするものはありません。
console ペインでは fanout の TUI コンソールが最初から動いているので、session に入ればそのままコンソールに着地します。
client は session が最後に選択していた workspace を開くため、CLI ファンアウト後は agent ペインに先に着地することがあります。console workspace は sidebar の issue を持たない行です。
TUI を quit するとペインはシェルに落ちます。そのシェルで `fanout` を実行すれば再び開きます(ペインには herdr が `HERDR_ENV=1` を設定しています)。quit 後に再利用した session は、再実行するまでシェルのままです。
linked worktree 間では同じ console 行を共有します。

detached の tmux コンソールと同じく、console TUI は誰も attach していない間も owned session に常駐します。state と GitHub の更新は続き、user config で有効化した watcher も動き続けます。

端末がない場合 — stdin か stdout がパイプ、つまりスクリプトや CI — は attach せず、stdout の最終行に attach command を表示します。exec に失敗したときも同じ表示に落ちるので、手で実行する command は常に残ります。

CLI のファンアウトには attach も既存 session も不要です。
同じ owned session を作成または再採用し、プロジェクトルートの coordinator workspace と子ごとの worktree workspace を足して、各 agent を起動します。
様子を見たくなったときに attach してください。

## 観測専用リリースからの移行

v0.13.0 の herdr backend は観測専用でした。名前付き session は自分で起動し、fanout は herdr 0.7.3 に完全一致で固定し、launch と変更操作はすべて拒否していたため、herdr の行を自分で記録することもありません。
owned モデルへ移る手順は次のとおりです。

1. `herdr` CLI を stable 0.7.5 以上へ更新します。0.7.3 と 0.7.4 は `unsupported herdr CLI version …` で fail closed します。owned session は pin した CLI で自分の server を起動するため、既存の herdr server を再起動する必要はありません。
2. 名前付き session を手で起動する手順をやめます。fanout が owner marker 配下に自分の session を作ります。fanout が所有しない session では対話操作は理由付きで無効のままです。
3. 旧 herdr pane から出て、`HERDR_SESSION` と `HERDR_SOCKET_PATH` を unset した素のシェルから bootstrap します。これらは foreign な行を読むときの接続先を決めますし、herdr pane の中では owned session と一致しないと TUI が警告付きで観測専用に落ちます。

`.fanout/state.json` の変換は不要で、tmux の親もそのままです。

## tmux との差分

| 機能 | tmux backend | herdr backend |
|---|---|---|
| issue / Project / plan / watcher の launch | worktree、pane、agent を作成 | owned herdr workspace と検証済み agent を作成 |
| worktree 作成 | 子ごとに `.fanout/worktrees/` 配下へ | 子ごとに `herdr worktree create` / `open` を実行 |
| 生死と agent state(TUI コンソール、web ダッシュボード) | tmux への照会と pane option | `herdr api snapshot` と launch に束縛した Claude または Codex Plan telemetry |
| exit status 表示 | launch wrapper が `✓ done` を報告 | なし — herdr の public API に exit status は残らない |
| agent 終了後の pane | wrapper のメッセージ付きで pane が残る | 正常終了で herdr は pane と自身の記録を消す。fanout の行は `stale` になる |
| 対話 TUI launch / focus / peek | TUI キー | fanout-owned session の ownership 検証済み pane だけ対応 |
| TUI の focus + zoom (`Z`) | pane に focus して zoom | 不可 — `herdr backend interactive TUI action is unavailable; zoom is unavailable` |
| 対話 send / restore / plan capture | tmux の各対応経路 | 不可 — `runtime backend herdr does not support …` |
| `--team` peer messaging | SQLite registry、Claude watcher、Codex app-server bridge | 同じ registry と push lane |
| `--merge` / `--close` / `--cleanup`、TUI merge / close / cleanup | 対応 | fanout-owned 行の identity を検証して実行。dirty checkout は force しない |
| 自動 nudge(`fanout msg nudge`) | 相手が入力を受けられる状態なら配送 | current launch に束縛された refined telemetry と live identity/process の照合後に no-wait の `agent prompt` を 1 回発行。それ以外は no-op |
| global keybind | ダッシュボード用 `F12` / `prefix + D`、worktree 操作用 `prefix + M`、コンソール復帰 | fanout-owned Herdr config にダッシュボード用 `F12` だけ登録 |
| 通知 | bell / tmux / ntfy / slack の channel | bell / ntfy / slack は動く。tmux channel と herdr の `notification show` は発火しない |
| 子の Plan Mode launch | 対応 | 対応。Codex は fanout の app-server controller、Claude / OpenCode は固有の mode flag を使う |
| TUI フォーム(設定、ヘルプ) | tmux popup | インラインの in-process フォーム |
| session resume | fanout の restore フロー | 明示的な `fanout herdr restart` で完全に検証できた direct Codex session だけを resume。ほかの provider と不完全な binding は `stale` のまま |

明示的な `fanout herdr restart` は、復元された pane の `agent_session` が保存値と完全一致し、起動した process の絶対 executable、`codex resume <session-id>` argv、cwd、ancestry、foreground process group を検証できた direct Codex 行だけを再束縛します。
値の欠落、重複、不一致、検証不能では行を `stale` のまま残します。
Claude、OpenCode、Codex Plan / Team controller、TUI で既存 worktree に attach した Codex はこの経路で resume しません。
復元直後の shell placeholder が `idle` と表示されても、process の生存や完了を示しません。

live launch と `restart` の試行前には、exact な live pane が報告する direct Codex の `agent_session` を保存値へ反映します。
headless 運用で `/new` を実行した後も、このどちらかの経路が owned server を観測できれば新しい conversation を保存します。
どちらの経路より先に owned server が観測不能になると、新しい session は復元できず、後の restart でも行を `stale` のまま残します。

herdr は exit status を残さず、正常終了で pane の記録も消します。
終了した agent は `✓ done` の pane を残さずに herdr session から消え、記録済みの fanout 行は `stale` と表示されます。

## restart と shutdown

fanout が owned server を副作用で再起動・停止することはありません。明示的な 2 つの verb で操作します。

```bash
fanout herdr restart    # 停止済みの owned server を置き換え、検証を通った行を再束縛する
fanout herdr shutdown   # 空の owned server を停止する
```

`restart` は死んだ server 向けです。fanout は旧世代の supervisor process と socket の消失を確認できたときだけ置き換えます。まだ動いている世代は `herdr owned server generation is still live` で拒否します。
その後は新しい世代を起動し、旧版の fanout が書いた owned `config.toml` を置き換え、記録済みの行を上記のルールで再束縛します。
失敗した後にどちらの verb を再実行しても安全です。fanout は何をしようとしたかを記録しており、作業を繰り返すのではなくその結果を確認します。

fanout の更新後に launch が `owned Herdr launcher predates the current fanout` で拒否されたら、子の行を `--close` / `--cleanup` で片づけ、console の TUI を quit してそのシェルを終了し、coordinator の shell も終了してから `fanout herdr shutdown` を実行します。
`restart` は生存世代を置き換えません。`shutdown` が空の session と古い足場行を畳み、次の launch が現行 launcher を使う新世代を作ります。

失敗した launch の `realized` intent も `shutdown` が回収します。owned workspace の snapshot で記録済み label が無いことを確認し、worktree / resume では checkout の不在も確認します。
fanout-created branch が残っていれば保存済み base SHA と一致するときだけ compare-and-delete し、削除後に intent を剪定します。
snapshot の取得失敗、同じ label の workspace、残存 checkout、移動した branch、branch の観測失敗のいずれかがあれば intent を残して拒否します。

`shutdown` は空の server を retire します。子の herdr 行がこのリポジトリの state に残っている間(linked worktree もすべて対象)、owned session に workspace が残っている間、剪定できない herdr intent が保留中の間は拒否します。
素のシェルからの TUI bootstrap が記録する console 行と、issue / Project / plan のファンアウトが記録するプロジェクトルートの coordinator 行は、workspace が 1 つも残っていないことを確認した後に `shutdown` 自身が削除します。動作中の shell は先に終了してください。

## sidebar token

検証を通った launch ごとに、表示専用の token を 5 つ、source `fanout` で報告します。sidebar の行にどの子の作業かを出せます。
token は表示専用データです。fanout は読み戻さず、backend state、生死、完了判定にも使いません。

| token | resource | 値 |
|---|---|---|
| `fanout_issue` | workspace | issue / Project の子は `#<issue>`、`fanout plan` のタスクは task ID |
| `fanout_slug` | workspace | 子の slug。worktree ディレクトリ名と同じ |
| `fanout_parent` | pane | `#<parent>`、`plan:<slug>`、Projects のパス。watcher launch は拾った issue を出す |
| `fanout_pr` | pane | PR 用に予約。現時点では報告のたびに clear |
| `fanout_ci` | pane | CI 用に予約。現時点では報告のたびに clear |

1 回の報告でその resource の fanout token 一式を書き、値を持たないものは clear します。使い回された workspace や pane に古い値が残りません。
報告は launch が生きた identity を検証した直後の 1 回だけで、`seq` も `ttl_ms` も送りません。
herdr の cold restart では token がすべて消え、`terminal_id` も変わります。
完全一致した Codex resume は行を再束縛できますが、表示 token は再送しません。

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
任意の手順ですが、TUI の focus は claude / codex の行でこの hook が報告する session に依存します(herdr 自身の session 復元も同じ session を使います)。

fanout は claude / codex の workload に `HERDR_ENV` / `HERDR_SOCKET_PATH` / `HERDR_PANE_ID` だけを渡すので、hook は owned session の socket に届きます。session と workspace の route は渡しません。
hook 自体はあなたの agent 設定側にあり、owned session の XDG 隔離の外なので置き換えは不要です。

fanout-owned session は herdr の XDG directory を隔離し、workspace / worktree 作成前に plugin registry が空であることを要求します。
fanout-owned launch では herdr の通知 plugin と worktree setup plugin は動きません。
registry が空でない場合は mutation 前に launch が失敗します。通知と setup には fanout の channel と hook を使ってください。

通知元は 1 つに絞ってください。fanout の `ntfy` / `slack` channel は、PR や CI の遷移に加えて agent の遷移(plan ready、入力待ち)も通知し、herdr の通知 plugin も同じ agent state を独自に通知します。
owned launch はその plugin に届かないため、二重通知が起きるのは自分で設定した session だけです。そちらでどちらか一方を止めてください。
agent integration は別経路です。hook は session identity を herdr へ報告するもので、fanout の launch 単位の hook と併存し、通知は出しません。

2 つのツールは別の層にあります。
fanout-owned session 以外では、herdr の plugin が並列エージェント作業を runtime 側から扱います: GitHub や Jira を起点にした worktree 起動、diff レビューの sidebar、複数プロジェクトの sidebar、レイアウトや通知の plugin。
fanout は GitHub ワークフロー側から扱います: 親子の fan-out、briefing 生成、blocker の wave、PR のライフサイクル、レビューゲート。
herdr が pane を実行・表示し、fanout が作業を計画して tmux または herdr で起動し、GitHub 側を追跡します。

## 旧 fanout バイナリ

旧版の fanout バイナリは `.fanout/state.json` の herdr フィールドを未知のキーとして読み飛ばします。
herdr 行は stale と表示され、旧版の `--close` は herdr workspace を残します。herdr 側で片付けてください。
旧版が state を書き込むと、知っているフィールドだけを保存するため、行から herdr の identity が落ちます。

`--backend` フラグと `FANOUT_BACKEND` は [CLI リファレンス]({{< relref "/docs/cli" >}})、`runtimeBackend` キーは[設定]({{< relref "/docs/settings" >}})、herdr のエラーメッセージと対処は[トラブルシューティング]({{< relref "/docs/troubleshooting" >}})にあります。
