# herdr runtime backend 実機検証

ステータス: 0.7.4 wave 2 の実機検証を完了し、tmux-parity の協調プロセス信頼で fanout-owned herdr session の実装を解禁する。
判断主体はユーザー、判断日は 2026-07-21 JST である。
この判断は後続 issue の実装条件を定めるものであり、この PR はコードを変更しない。
0.7.4 core runtime matrix、metadata token reporting、sidebar row layout は実測済みである。
この文書の日付は日本標準時（JST、UTC+09:00）で記す。
core runtime の検証日は 2026-07-16 と 2026-07-21、metadata token reporting と sidebar row layout の追試日は 2026-07-17 である。
0.7.3 と 0.7.4 の対象はいずれも protocol `16`、schema version `1` である。

fanout の herdr backend wave 2 は CLI-first とし、集約読みには CLI wrapper の `herdr api snapshot` を使う。
raw Socket client は実装しない。
version / session / schema の検査後、snapshot / list / wait、targeted read、owned server の bootstrap、launch、cleanup、focus、emitter、metadata、自動 nudge、Codex integration v6 の cold restart resume を後続実装へ解禁する。
自動 mutation は provisional intent と phase machine、nonce の二重照合、branch の atomic reservation、事後条件検査、no-blind-retry を通す。
owned XDG の config と plugin registry に予期しない setup hook があれば launch 前に fail closed にする。
provider hook の signal は agent process から偽造できる協調 telemetry とし、tmux backend と同じ nudge gate には使うが、完了判定または cleanup の根拠には使わない。
自動 nudge は tmux の `shouldNudge` と同じ条件で解禁する。
`codexPlanMode` は agent 種別にかかわらず恒久的に除外する。
pane 消滅は `stale` とし、`terminal_id` の変化は Codex integration v6 の exact matcher を満たす場合だけ再束縛し、それ以外は `stale` とする。
session identity の read と mutation は別の CLI 接続になるため、直前の version / identity 検査だけでは mutation の対象を束縛できない。
0700 directory、0600 socket、fanout 側の ownership marker は別 UID を排除するが、同一 UID の agent を排除せず、mutation authority にはならない。
tmux backend も同一 UID の agent が server 停止、他 pane への `send-keys`、state signal の偽造を実行できる協調プロセス信頼を前提に launch、cleanup、nudge を提供している。
fanout-owned private socket は影響範囲を fanout 所有 session に閉じるため、tmux-parity tier ではこの残余リスクを受容する。
request-bound generation と conditional mutation、server-authenticated controller capability、server / agent の UID 分離は、herdr が primitive を提供した場合に proof-grade tier へ格上げする条件として保持する。

## 採用判断

| 対象 | wave 2 の判断 | 理由 |
|---|---|---|
| owned server | Go | private socket、0700 XDG、owner marker で別 UID と session 外への影響を封じ込める |
| server 起動 | Go | per-repo supervisor が caller routing env を fanout-owned XDG / config / socket / session で上書きし、foreground server child を bootstrap する |
| 集約読み | `herdr api snapshot` を使う | workspace、tab、pane、layout、agent、focus を 1 回で取得できる |
| content read / peek | Go | exact PaneRef と `terminal_id` を直前・直後に再照合し、不一致は結果を破棄する |
| raw Socket API | 不採用 | wave 2 で必要な操作は CLI wrapper で足りる |
| worktree | Go | intent / phase、workspace label と git-dir marker の nonce、Git 事後条件で誤採用を防ぐ |
| agent 起動 | Go | control-plane env と workload env を分離し、bare argv、exact `--cwd`、明示 `--env`、保存済み process identity を要求する |
| capability gate | stable `>=0.7.4` と structural schema、接続先 status を検査する | exact tuple は廃止するが、gate は compatibility だけを証明する |
| attach | custom socket を選ぶ bare `herdr` command を提示する | `session attach <name>` は別 daemon を自動起動し得るため実行しない |
| focus | Go | TUI の明示操作だけが送信直前再照合後に focus する |
| nudge | Go | `running` / `working` / `plan` / `idle` だけへ送信し、`blocked` / `done` / 未設定 / 未知値は no-op とする |
| `codexPlanMode` | herdr backend から恒久除外 | runtime backend から terminal UI を操作する controller は採用しない |
| identity / resume | Go | routing、checkout、terminal、会話、process を別々に照合し、cold restart は Codex v6 exact matcher に限る |
| cleanup / rollback | Go | exact ownership と直前 snapshot を照合し、response loss では blind retry しない |
| emitter | Go | cooperative telemetry と nudge gate に限り、completion / cleanup authority にしない |
| metadata | Go | exact target を直前・直後に照合し、表示専用 token だけを報告する |
| 通知 | 手動検証だけに使う | detached 時も `shown:true` で、表示完了の応答ではなく、fanout は自動発行しない |

## 検証条件

検証用の git repository、bare remote、linked worktree、named herdr session を `/private/tmp` に作った。
2026-07-16 の v1 検証ではユーザーの default herdr session を停止も削除もしていない。
plugin event の検証では `XDG_CONFIG_HOME` と `XDG_STATE_HOME` も `/private/tmp` へ向け、plugin registry と state を隔離した。

追加検証では公式 `v0.7.3` macOS arm64 リリースバイナリ（SHA-256 `b31345392d004ec1f1b2c821e1ad601019fa8385fe1e4c6931321eb58a920773`）を `/private/tmp` に置き、named session と state を隔離した。
herdr 公式 Codex integration v6 の再開試験だけは、すでに信頼済みのこの worktree を cwd に使った。

sidebar 追試と wave 2 では公式 `v0.7.4` macOS arm64 リリースバイナリ（SHA-256 `24992e1625dbdcb18354a59e299e4b263c312400b31396cdc07cd46ed57f24a7`）を使った。
インストール済みの `herdr 0.7.4` はこのリリースバイナリと byte 単位で一致した。
隔離 session の `status --json` は client / server version `0.7.4` と protocol `16`、`api snapshot` は version `0.7.4` と protocol `16`、`api schema --json` は schema version `1` を返した。

wave 2 の成功経路は `XDG_CONFIG_HOME`、`XDG_STATE_HOME`、`XDG_DATA_HOME`、`XDG_CACHE_HOME`、config、socket を検証ごとの 0700 directory へ向けた。
`HERDR_CONFIG_PATH` と custom socket だけを隔離した負例では、herdr が既存の `~/.config/herdr/session.json` を復元して global log を書いた。
終了時に同ファイルの SHA-256 は `48cff490ab170d1327262076d7f3a71d336b0e2e5f4495c68d1e99133ecca0eb` から `3fbd67b380ee59c3098d125349cacab62178088bb1fd21a6aae478695ebc30ab` へ変わった。
既存の `~/.config/herdr/session-history.json`（SHA-256 `f597ddef792e7233b8035c7c2e7e6f5573523e00ad5801fc473d2eb051185445`）も削除された。
事前の byte backup がなかったため復元はしていない。
この負例は default Herdr state を変更した検証事故として扱う。
2026-07-21 JST の追加確認では `session.json` と `session-history.json` が再生成されていたが、検証前の bytes は復元も同一性検証もできない。
現在の両 file を検証前 state とみなして復旧には使わず、追加の overwrite も行わない。
この負例以降は default herdr state を操作せず、全 XDG directory を隔離した。
今後の実機検証では対象 file の存在、SHA-256、byte backup を開始前に記録し、全 XDG directory、config、socket を一時 directory へ隔離する。
default path を使う負例は実行しない。

fanout の複数行入力はインストール済みの `fanout v0.12.0` を実際の herdr pane で起動して確認した。
モックは使っていない。

## 0.7.4 wave 2 owned session

### server lifecycle と ownership

`herdr server` は foreground process として起動でき、fanout supervisor の直接の子として監視できる。
検証では session directory を 0700、server socket と client socket を 0600 とし、log は 0700 directory 配下の 0644 file に分離した。
server log は隔離した `XDG_CONFIG_HOME/herdr/sessions/<session>/herdr-server.log` に置かれた。
同じ socket path への二重起動は `herdr server is already running` で終了した。
同じ path に non-herdr の Unix listener がいる場合も同じ error で終了し、protocol、version、owner は検査しなかった。
`status --json` の `detached_server_daemon:true` は capability 表示であり、明示的に起動した server 自体は daemonize しなかった。

この permission 境界が排除するのは別 UID だけである。
herdr が起動した agent は同じ UID で動き、`HERDR_SOCKET_PATH` を継承するため、socket が分かれば `server.stop`、`plugin.*`、`worktree.remove` を発行できる。
同じ UID の process は外部 `owner.json` の nonce、PID、binary hash も変更でき、status、schema、snapshot の応答は marker nonce または session generation に束縛されない。
server 停止後に同じ path の server へ置換する ABA も原子的には検出できない。

owned runtime directory の `owner.json` は協調する fanout process 間の ownership、封じ込め、crash recovery、誤操作防止に使う。
canonical git common directory、owner nonce、socket path、binary SHA-256 と version、supervisor PID と start token、隔離 XDG path を記録し、0600 で exclusive create する。
既存 socket と marker が完全一致すれば owned session の reconciliation に使い、不一致、foreign、または検証不能なら fail closed にして server を停止しない。
この marker を mutation authority として扱わない。

config と plugin registry は全 XDG directory の差し替えで default state から隔離できる。
しかし同一 UID の agent は空 registry の確認後に同じ socket から plugin mutation を実行できるため、standalone registry read は atomic proof にならない。
tmux-parity tier では owned registry の preflight を協調プロセス前提の誤操作防止に使い、予期しない setup hook または config drift があれば launch 前に fail closed にする。
setup hook の抑止、registry generation precondition、operation-scoped completion receipt は proof-grade tier の格上げ条件として保持する。

### attach

custom socket へ plain terminal から接続する実測済みの形は、隔離 XDG path と socket path を指定した引数なしの bare `herdr` である。

```console
XDG_CONFIG_HOME=<owned-config> XDG_STATE_HOME=<owned-state> \
XDG_DATA_HOME=<owned-data> XDG_CACHE_HOME=<owned-cache> \
HERDR_CONFIG_PATH=<owned-config-file> HERDR_SESSION=<repo-session> \
HERDR_SOCKET_PATH=<owned-server-socket> HERDR_CLIENT_SOCKET_PATH=<owned-client-socket> herdr
```

`herdr session attach <name>` と明示 `--session` は custom socket より named session を優先し、別の background daemon を起動し得るため使わない。
wave 2 の UX は上の値を安全に shell quote した command の提示に限り、fanout console を `exec` で置き換えない。
custom socket の detach と reattach では public ID と `terminal_id` は変わらなかった。

### core matrix

0.7.4 owned session で #424 と同じ core matrix を再実測した。
`worktree create/open` の branch、path、base、既存 branch の `--base` 無視、`already_open:true` は 0.7.3 と同じだった。
root workspace と child worktree workspace は sibling になった。
bare `agent start` は argv、空白を含む env、exact cwd を保持し、`pane process-info` は shell PID、process group、候補 argv と cwd を返した。
`pane run` は text と Enter を一操作で送った。

cold restart の前後で `w1:p1`、`w2:p1`、`w3:p1`、`w4:p1` の public ID は維持された。
一方、対応する `terminal_id` は `term_6570ff5b8ee0a1`、`term_6570ff5b981e62`、`term_6570ff5ba1f6c3`、`term_6570ff5ba971e4` から、`term_657100c2ce6f51`、`term_657100c2ceead2`、`term_657100c2cf6513`、`term_657100c2cffdc4` へすべて変わった。

staged entry を持つ dirty worktree の remove は `dirty_worktree_requires_force` で拒否し、`--force` は checkout を削除した。
clean worktree の remove も checkout を削除したが、どちらも local branch は残した。
`workspace close` は root repository と外部 checkout を削除せず、最終 snapshot は空になった。

### topology、restart、teardown

wave 2 の実装では session を per-repo とする。
linked worktree は canonical git common directory を共有し、独立 clone は full common-directory identity の hash で名前を分離する。
marker の full identity が一致しなければ hash が一致しても fail closed にする。

repo root に console workspace を一つ置き、実際の親ごとに repo-root cwd の coordinator workspace を一つ置く。
coordinator の state row は `@manual` の負番号を維持するが、backend stickiness と lifecycle provenance は実際の親へ帰属させる。
child は sibling workspace とし、workspace label で親を識別する。
create は `--no-focus` とし、明示的な TUI launch だけが返却 ID を focus する。
focused child の close 後は同じ親の coordinator、存在しなければ console を focus する。

server process は per-repo supervisor が所有する foreground `herdr server` child とし、attached console process の子にはしない。
これにより console の detach または終了後も server を存続させる。
wave 2 は owner marker、socket、capability gate を満たす owned server の bootstrap を実行する。
server loss 後は ownership を再検査して server を再起動し、capability gate を通した後に Codex integration v6 exact matcher の cold restart reconciliation を実行する。
不一致または未検証 provider の row は `stale` とする。
最後の child close では server を止めず、active intent、row、または foreign resource がない場合の明示的な repo-session shutdown だけを teardown とする。

## 起動と session 境界

server 起動前の `HERDR_SESSION=fanout-spike-424 herdr status --json` は `server.status:"not_running"` を返した。
同じ状態で `herdr workspace list` を実行すると Unix socket が存在せず失敗した。
通常の headless CLI は server を自動起動しない。

wave 2 の per-repo supervisor は session 選択に使う継承値へ依存せず、attach と同じ owned XDG / config / socket / session env を `status` と `server` の各 call に明示する。

```console
XDG_CONFIG_HOME=<owned-config> XDG_STATE_HOME=<owned-state> \
XDG_DATA_HOME=<owned-data> XDG_CACHE_HOME=<owned-cache> \
HERDR_CONFIG_PATH=<owned-config-file> HERDR_SESSION=<repo-session> \
HERDR_SOCKET_PATH=<owned-server-socket> HERDR_CLIENT_SOCKET_PATH=<owned-client-socket> herdr status --json

XDG_CONFIG_HOME=<owned-config> XDG_STATE_HOME=<owned-state> \
XDG_DATA_HOME=<owned-data> XDG_CACHE_HOME=<owned-cache> \
HERDR_CONFIG_PATH=<owned-config-file> HERDR_SESSION=<repo-session> \
HERDR_SOCKET_PATH=<owned-server-socket> HERDR_CLIENT_SOCKET_PATH=<owned-client-socket> herdr server
```

herdr の session 解決順は明示 `--session`、`HERDR_SOCKET_PATH`、`HERDR_SESSION` であり、既存 herdr pane から継承した `HERDR_SOCKET_PATH` は `HERDR_SESSION` より先に評価される。
後続実装は呼び出し元の Herdr routing env を流用せず、各 call 用の env を構築して上記の全値を fanout-owned 値で設定する。
fanout は owner marker を exclusive create し、foreign socket または不一致 marker がない場合だけ server を bootstrap する。
`status --json` の `server.session` と socket path で対象 namespace は確認できるが、応答には session UUID、generation、state epoch がない。

client の detach と reattach では server と agent process が動き続け、workspace、tab、pane の public ID、cwd、agent record と status に detach 起因の変化はなかった。
status に現れる session identity は前後とも session 名と socket path だけだった。
server の cold restart でも public ID は維持されたが、全 pane の `terminal_id` は変わった。

同名 session を削除して作り直すと、最初の workspace、tab、pane は再び `w1`、`w1:t1`、`w1:p1` になった。
削除前の `w1:p1` は `terminal_id:"term_656aa954d42c11"`、fresh state の `w1:p1` は `terminal_id:"term_656aae4d0cc691"` だった。
session 名と public ID だけを state key にすると stale mapping が別 process へ一致する。

herdr から比較可能な session epoch は取得できない。
wave 2 は session 名を namespace として保存し、各 PaneRef に `terminal_id` と `agent_session` を保存する。

herdr 0.7.3 は明示 `--session` が無い場合、継承した `HERDR_SOCKET_PATH` を `HERDR_SESSION` より優先する(Pass 2 レビュー時の 0.7.3 実機確認)。
herdr pane 内で実行する fanout は常にこの変数を持つため、session 名だけの routing は custom socket や別 session の server に接続し得る。
public ID は session 間で再利用されるため、誤 routing の `send` や `close` は無関係な pane に届く。
wave 2 の CLI は検証済みの socket path を session namespace と併せて保存し、read と mutation の各呼び出しで明示的に選択する。
session identity の再確認は routing、crash recovery、誤操作防止に使い、mutation authority の証明には使わない。
targeted content read の前後で snapshot を再確認しても、read 中だけ別 session へ差し替わって戻る ABA を検出できない。
この ABA は tmux-parity tier の受容済み残余リスクとし、直前・直後の exact PaneRef、`terminal_id`、worktree provenance が一致した場合だけ content を公開する。

`terminal_id` は server が所有する terminal 実体の識別子であり、論理上の会話または agent process の識別子ではない。
同じ `terminal_id` でも、想定した agent process の生存は別に確認する。
`terminal_id` が変わった場合、保存済みの `{source, agent, kind, value}` と完全一致する一意な `agent_session` があれば、論理上の会話を新しい terminal へ再対応付けする候補にできる。
`agent_session` ref が欠落、不一致、重複した場合は再対応付けせず fail closed にする。
この ref は attach 前の再開待ちにも現れるため、process の生存を示す証拠には使わず、running への写像には provider 固有の process identity 検査も要求する。
fanout-owned epoch は owned lifecycle の reconciliation token として使うが、同一 UID に対する mutation authority の証明には使わない。

## 操作面

次の表は実測した CLI surface を示す。
wave 2 の後続実装は次の targeted read と mutation を、下記の intent / phase と送信直前再照合を通して呼べる。

| 実測操作 | CLI | 結果 | 制約 |
|---|---|---|---|
| launch | `workspace create --cwd ... --no-focus` | `workspace_created` | provisional intent を先に保存し、root coordinator 作成に使う |
| launch | `worktree create --workspace ... --branch ... --base ... --path ... --label <nonce> --no-focus --json` | `worktree_created` | nonce label と checkout git-dir marker を二重照合する |
| launch / recover | `worktree open --workspace ... --path ... --label <nonce> --no-focus --json` | `worktree_opened` | state / intent が所有する既存 checkout だけを採用する |
| launch | `agent start <name> --workspace ... --cwd ... --env ... --no-focus -- <argv...>` | `agent_started` | bare argv、exact cwd / env、保存済み launch identity を要求し、`--json` flag はない |
| list | `api snapshot` | `session_snapshot` | `session.snapshot` の CLI wrapper。identity / status 観測に使う |
| list | `worktree list --workspace ... --json`、`agent list` | `worktree_list`、`agent_list` | `worktree list` は基準 workspace を明示する |
| content read | `pane read`、`agent read` | text または `pane_read` | target を直前・直後に再照合し、不一致なら content を破棄する |
| structured read | `pane get` | `pane_info` | exact PaneRef と worktree provenance の identity 検査に使う |
| structured read | `pane process-info --pane ...` | `pane_process_info` | process identity と Codex v6 resume matcher に使う |
| focus | `agent focus <name>`、`workspace focus <id>` | 対象 agent または workspace を focus | TUI の明示操作だけが target の直前再照合後に発行する |
| send | `pane run <pane> <text>` | text と Enter を一操作で送る | `shouldNudge` と送信直前再照合を通した自動 nudge に使う |
| close | `worktree remove --workspace ... [--force] --json` | `worktree_removed` | exact ownership と dirty / force 条件を直前に再照合する |
| close | `workspace close`、`pane close` | `ok` | checkout は `workspace close` では消えないため cleanup order を固定する |
| wait | `api snapshot` の 2 秒間隔 polling | `session_snapshot` | `total_timeout` は整数 3 秒以上。既定 300 秒では最大 150 snapshot、各 CLI call 最大 5 秒で current-state predicate を評価する |

generic pane の exact focus が必要になった場合、Socket API の `pane.focus` を同じ直前再照合を持つ追加候補にする。

`worktree remove`、`workspace close`、`pane close` は snapshot 照合と mutation が別 CLI 接続になり、照合済み nonce、`terminal_id`、session epoch を request の precondition として渡す手段が 0.7.3 にない。
同名 session の再作成で session 名、socket、public ID は再利用されるため、照合と mutation の間の TOCTOU は CLI では閉じられない。
wave 2 は保存済み ownership、current snapshot、git-dir marker を送信直前に再照合して cleanup と既知の rollback を実行する。
照合と mutation の間の race は tmux-parity tier の受容済み残余リスクとし、response loss または mutation の有無が不明な場合は blind retry せず fail closed にする。

herdr backend は root coordinator の intent を保存してから `workspace create` を実行する。
version / session identity の precheck、provisional intent、nonce label は response loss と重複作成の検出には使えるが、precheck 後に同名 session が置き換わる TOCTOU を閉じない。

root coordinator の `workspace create` も副作用を持つ launch 操作として provisional intent の対象にする。
owner row key、backend / session identity、root cwd、intent 固有の coordinator nonce を intent へ保存する。
そのうえで `workspace create --label <nonce>` を発行し、成功応答の workspace ID を intent に束縛してから通常 state へ確定する。
herdr pane 内から fanout を起動する通常ケースでは同じ root cwd のユーザー workspace が既にあるため、root cwd / provenance の一致だけでは coordinator を識別しない。
応答喪失または crash 後の再実行は、pre-state 後に現れた intent nonce と同じ label の workspace が一つだけの場合にそれを回復対象とし、それ以外は fail closed にして既存のユーザー workspace を誤採用も重複作成もしない。

## worktree の配置と lifecycle

### branch、path、base

新規 branch の `worktree create` は指定した `--branch` と `--path` を使い、`--base ae24789` から作った branch の HEAD も `ae24789` と一致した。
この経路は fanout の決定済み slug、branch、checkout path、base commit SHA をそのまま渡せる。

既存 local branch `fanout/existing` の HEAD は `2f40ae8` だった。
この branch に異なる `--base ae24789` を指定しても、作成した worktree の HEAD は既存 branch の `2f40ae8` になった。
既存 branch では `--base` が branch の現在位置を上書きしない。

同じ branch と path で `worktree create` を繰り返すと `worktree_create_failed` になった。
`worktree create` 自体は idempotent ではない。
既存 checkout は `worktree open --path ...` または `worktree open --branch ...` で採用でき、すでに開いている場合は `already_open:true` が返った。

### safety gate

source checkout に untracked file があっても `worktree create` は成功した。
local `main` と `origin/main` を 1 commit ずつ diverge させた場合も、`--base main` は local の `6af20aa`、`--base origin/main` は remote-tracking ref の `b05fa51` をそのまま使った。
herdr は fetch、dirty gate、divergence gate を実行しない。

herdr backend は tmux-parity trust、owned session、capability gate を確認してから次の child launch state machine へ入る。
以下は wave 2 の自動 launch に必須の crash safety と誤操作防止の契約である。

- base ref を immutable commit SHA へ解決する。
- source checkout の dirty と divergence を検査し、既存の fail-closed 契約を保つ。
- branch、path、base SHA、state mapping を照合する。
- 既存 branch の HEAD が期待する base SHA と違う場合は、herdr に渡さず fail closed にする。
- 既存 checkout を作業 pane として再採用する場合は、fanout state が同じ task の所有を示し、branch、path、HEAD がすべて一致するときだけ `worktree open` を使う。
- `worktree create` または既存 checkout の `worktree open` の最初の mutation より前に、state lock 下で phase `worktree-planned` の provisional launch intent を保存する。
  intent は owner row key、backend、検証済み herdr session / socket identity、operation、worktree ownership nonce、slug、branch、path、base SHA、mutation 前の runtime / git snapshot を持つ。
  新規 create では nonce を mutation 前に生成し、既存 checkout の採用では state row と checkout git dir で一致済みの作成時 nonce を使う。
  同じ launch の再実行では intent の backend / session identity が env / default の backend 選択より優先され、明示指定(`--backend` / env)が intent と矛盾する場合は fail closed にする — workspace mutation 後・row 確定前に crash した launch を、別 backend の再実行が intent recovery より先に拾う事故を防ぐ。
  `worktree create` と `worktree open` は intent の nonce を `--label` に渡す。
  open 前の pre-state で対象 checkout を指す workspace がない場合は `already_open:false` だけを受理する。
  `already_open:true` は、pre-state の時点で同じ workspace ID と label が task の state / intent に束縛済みで、全所有条件が一致する場合だけ受理する。
  create 応答後は、workspace ID が pre-state にないこと、応答 checkout path が intent path と一致して pre-state にはないこと、label が nonce と一致すること、repo provenance が source request と一致することを先に確認し、応答 checkout の git dir marker を exclusive create で書く。
  marker の書き込み後に label と marker を再読し、branch と HEAD を含む残りの事後条件を検査する。
  create / open 成功後は応答または snapshot の `workspace.label` と checkout git dir marker の両方が intent nonce と一致することを要求し、workspace ID、nonce、provenance と phase `worktree-realized` を intent へ保存する。
  label は workspace object の所有しか示さないため、git dir marker の代用にしない。
  各 worktree mutation の直前に step、exact request、per-step pre-state を intent へ加え、phase `worktree-starting` を同じ state lock 下で保存してから呼び出す。
  phase `worktree-planned` の再実行は、記録済み pre-state と現在値が一致する場合だけ `worktree-starting` へ遷移して request を一回発行できる。
  phase `worktree-starting` の再実行は、対象資源が見つからない場合も request を再発行しない。
  一意な workspace と checkout がすでにあり、label、git dir marker、branch、path、HEAD、provenance が intent と一致する場合だけ応答喪失として `worktree-realized` へ進める。
  create 成功から git dir marker 書き込みまでの crash を含め、nonce の両側を証明できない crash window は自動採用せず fail closed にする。
  mutation request を発行しておらず pre-state の不変を証明できる失敗、またはユーザーの手動 cleanup 後に資源の不在を再観測できた場合だけ、intent の整理を同じ state save で確定する。
  request 発行後に mutation の非発生を証明できない場合は intent、観測資源、branch reservation を残し、`manual_cleanup_required` として fail closed にする。
  `agent start` の前に agent launch nonce と emitter nonce を生成し、hook へ注入する telemetry routing binding、agent launch nonce の衝突耐性のある hash を含む session 一意の deterministic agent name、provider、解決済みの絶対 executable、発行する argv / env fingerprint、API で再観測できる argv、`agent start --cwd` の exact path を intent へ保存して、phase `agent-starting` を state lock 下で確定する。
  provider が session ref を返した場合は exact ref も intent と final row に保存する。
  env fingerprint は発行内容の監査にだけ使い、lost-response recovery の照合条件には使わない。
  成功応答を受けたら returned PaneRef、terminal identity、応答の argv を加えて phase `agent-started` を保存する。
  plugin registry read は create / open request に registry generation を渡せず、read 後の plugin 変更を拒否できないため、setup hook 不在の proof には使わない。
  wave 2 は fanout-owned XDG の registry と config を直前に照合し、setup hook が空の場合だけ tmux-parity tier の操作 gate を通す。
  setup hook を許可する経路は、request による原子的な hook 抑止、registry generation precondition、または operation-scoped completion receipt が使える proof-grade tier まで fail closed にする。
  Git HEAD、index、tracked / untracked set の連続一致や一定時間の静止は、hook がまだ開始していない window と区別できないため completion proof に使わない。
  setup plugin を許可する proof-grade tier は、event type、workspace ID、checkout path、worktree ownership nonce、terminal `succeeded`、`exit_code:0` を一つの mutation に束縛した operation-scoped completion receipt を検証する。
  receipt 後の baseline 再照合は race 検出に使うが、completion proof にはしない。
  phase `worktree-realized` からは、owned registry に setup hook がなく checkout baseline を保存した場合だけ `worktree-ready` へ進める。
  phase `worktree-ready` で同名 agent が存在せず、checkout が記録済み baseline と一致し HEAD == 記録済み base SHA の場合だけ `agent start` へ進める。
  phase `agent-starting` で応答を保存できなかった場合は、同名 agent が見つかっても launch 時の `terminal_id` を証明できないため自動採用せず fail closed にする。
  phase `agent-started` の再実行は、pane が生存し、保存済み PaneRef と `terminal_id` が現在値と一致し、`process-info` の argv / process cwd も intent と一致する場合だけ process identity を束縛して final row へ進める。
  保存済み `terminal_id` が変わった場合は元の launch argv と比較せず、後述する provider 固有の cold-restart matcher を使う。
  保存済み agent start 応答があり pane がすでに消滅している場合は、returned PaneRef を束縛した `stale` row を確定する。
  同じ emitter nonce の pending `done` があっても agent-reported telemetry として保存するだけで、`stale` を `done` に変えない。
  `agent-starting` 後に agent が存在しない場合も mutation の有無を証明できないため自動で再発行せず fail closed にする。
  worktree、agent、process の照合失敗、欠落、重複は自動では触らず fail closed にする。
  final row の確定時は provider、解決済みの絶対 executable、元の launch argv、exact `agent start --cwd`、取得済みの exact session ref を intent から final row へ移す。
  final row の確定、pending emitter telemetry の反映、intent の削除は state lock 下の同じ state save で実行する。
- `worktree create` 後は、応答、workspace の worktree provenance、git の branch、path、HEAD を照合する。
- `worktree create` の事後条件違反、応答喪失、または mutation の有無を証明できない失敗では、intent を残し、資源へ自動では触れずに fail closed とする。
  応答 checkout path が intent path と違う場合は marker を書かない。
  0.7.3 の remove request は ownership nonce または session epoch を precondition として受け取らないため、検査後に同名 session が再作成される TOCTOU を自動 rollback では閉じられない。
  成功応答と exact ownership を保存済みの資源は、送信直前再照合後の `worktree remove` で自動 rollback できる。
  response loss または mutation の有無が不明な資源には触れず、rollback 後の git fallback も実行しない。
  新規 branch は herdr に作らせず、fanout が `worktree create` の前に atomic な ref 予約(old OID を空とする `update-ref` 相当。既存なら失敗)で base SHA に作成し、予約成功を intent へ記録する。
  preflight と create の間に別 process が同名 branch を作った場合は予約が失敗し、既存 branch 採用の実測挙動(113-115 行)による他者 branch の巻き込みを防ぐ。
  branch reservation は worktree mutation が起きていないことを証明できる場合だけ、予約時 OID を old OID とする compare-and-delete で解放する。
  worktree mutation の可能性がある場合、branch ref が変わっている場合、または予約の所有を証明できない場合は branch も残して fail closed にする。

### workspace 配置

`worktree create --workspace w1` は w1 内の pane を作らず、独立した child workspace w2 を作った。
親と子は同じ `repo_key` と worktree provenance で repository group に並ぶ。
`session.snapshot` には child から parent workspace を指す ID がない。

wave 2 の自動 launch は「親 workspace の内部に child pane」ではない。
project root の coordinator workspace を一つ作り、各 child worktree を sibling workspace として開く。
herdr backend はこの coordinator を作る。
fanout の state が parent issue と child workspace の対応を保持する。
pane split は同じ checkout 内に補助 process を追加する場合だけ使う。

### cleanup

dirty な child worktree を force なしで削除すると、`dirty_worktree_requires_force` で拒否された。
`--force` では checkout と workspace が消え、branch は残った。
clean な focused child を削除すると、focus は repository root workspace へ戻った。

state row 起点の cleanup でも、0.7.3 CLI は nonce、`terminal_id`、session epoch を `worktree remove` / `workspace close` / `pane close` の precondition として受け取らない。
fanout が state、snapshot、checkout git dir を照合してから別接続で mutation を発行するまでに、同名 session と public ID が別資源へ再利用され得る。
この TOCTOU は `--force` の有無にかかわらず残り、tmux-parity tier では tmux cleanup と同種の受容済み残余リスクとする。
fanout は保存済み backend / session、workspace ID、label、repo、branch、path、provenance、作成時 nonce と現在値を送信直前に再照合して `--cleanup` / `--close` を発行する。
成功応答後に workspace と checkout の不在を再観測できた場合だけ state row を整理する。
不一致、重複、response loss、または mutation の有無が不明な場合は row と intent を残して fail closed にする。

`workspace close` を先に実行すると checkout は残る。
続く `worktree remove --workspace <closed-id>` は `workspace_not_found` になる。
`worktree open` で checkout を削除用 workspace へ再登録する経路は、owned checkout、nonce、branch、path、HEAD が一致し、owned registry に setup hook がない場合だけ自動実行する。
0.7.3 の `worktree open` は `worktree.opened` setup hook を発火し、hook の完了を operation-scoped receipt で待つ手段がないため、cleanup 中の再登録と直後の remove を安全に直列化できない。
setup hook がある場合の削除用再登録は proof-grade tier まで fail closed にし、`--force` は明示的なユーザー確認と dirty state の再照合を要求する。

### plugin event

隔離した local plugin を `plugin link` し、`worktree.created` と `worktree.removed` の hook を登録した。
CLI 経由の create と remove で両 hook が各一回実行され、plugin log は `status:"succeeded"`、`exit_code:0` を返した。

作成 event は `workspace` と `worktree`、削除 event は `workspace_id`、最終 `workspace`、`worktree`、`forced` を含んだ。
fanout が worktree 実体化を herdr へ委譲したときに plugin event が発火することは確認したが、setup hook の完了を待って agent を自動起動できることは確認していない。
追加検証では公式 herdr 0.7.3 の `worktree open` が `worktree.opened` hook を一回だけ実行し、plugin log は `status:"succeeded"`、`exit_code:0` を返した。
payload は workspace、active tab、checkout path、branch、`already_open:false` を含んだ。
公式 Socket API と hook 実測の両方で、`worktree.opened` は open 経路、`worktree.created` は create 経路の event だった。
`plugin log list` の terminal record と mutation 応答を一意に対応付ける receipt はこの spike で確認していないため、wave 2 は plugin log を completion fence に使わない。

## pane と agent の lifecycle

### bare argv と env

以下は手動操作で得た実測と、wave 2 の自動 launch 契約である。
herdr backend は state machine の `worktree-ready` から `agent start` を自動実行する。

`agent start --workspace w2` だけでは cwd は child checkout にならず、CLI 呼び出し元の cwd が使われた。
`--cwd <child-checkout>` は必須である。

bare argv へ明示した PATH prefix と `FANOUT_*` env はそのまま届いた。
実装契約では PATH を prepend しない。

fresh state の追加検証では、source workspace `w1` から `worktree open` で作った child workspace `w2` に次の process を起動した。

```console
/private/tmp/herdr-0.7.3 agent start worktree-env-probe \
  --env FANOUT_AGENT_PROBE=worktree-order-fresh-a424 --workspace w2 \
  --cwd /private/tmp/h424-env-order-fresh/repos/child --no-focus -- \
  /bin/sh -c 'env | LC_ALL=C sort | grep -E "^(FANOUT|HERDR|TMUX)(_|=)"; sleep 120'
```

herdr が設定する session、socket、pane、tab、workspace の env は指定した child workspace と一致した。

```text
FANOUT_AGENT_PROBE=worktree-order-fresh-a424
HERDR_CONFIG_PATH=/private/tmp/h424-env-order-fresh/config.toml
HERDR_ENV=1
HERDR_PANE_ID=w2:p2
HERDR_SESSION=h424-env-order-fresh
HERDR_SOCKET_PATH=/private/tmp/h424-env-order-fresh/xdg-config/herdr/sessions/h424-env-order-fresh/herdr.sock
HERDR_TAB_ID=w2:t1
HERDR_WORKSPACE_ID=w2
```

`workspace get`、`pane get`、`tab get` の相互参照も `w2`、`w2:p2`、`w2:t1` で一致し、cwd と `foreground_cwd` は child checkout だった。
この追加検証では server と CLI の環境から `TMUX` と `FANOUT_BIN` を除き、外側 runtime の値が transcript に混ざらないようにした。
別の nested tmux 検証では、generic workspace shell に `HERDR_ENV` と `TMUX` が同時に届いた。

wave 2 は Herdr control-plane env と agent workload env を分離する。
fanout は owned XDG で supervisor を起動する前に、呼び出し元の `HOME`、`PATH` と effective `XDG_CONFIG_HOME`、`XDG_STATE_HOME`、`XDG_DATA_HOME`、`XDG_CACHE_HOME` を workload env として保存する。
未設定の XDG 変数はそれぞれ `$HOME/.config`、`$HOME/.local/state`、`$HOME/.local/share`、`$HOME/.cache` に解決し、owned XDG を agent へ漏らさない。
wave 2 の自動 launch は、fanout 固有の値、保存した `HOME` / `PATH`、workload XDG を `--env KEY=VALUE` で渡し、herdr の実行環境識別子は herdr の env と snapshot から取得する。
agent executable は fanout の起動環境で解決した絶対パスを bare argv の先頭に置く。
#427 が注入する lifecycle hook も、同じ時点で解決した fanout executable の絶対パスを呼ぶ。
絶対パスだけでは、agent が起動後に実行する `git` や `gh` が server の ambient PATH で解決されるため、server を minimal env で起動した環境では作業が失敗する(Pass 2 レビュー時の 0.7.3 実機確認)。
tmux backend の `BuildResolvedCommand` と同じく、保存した workload env を明示 `--env` で引き継ぎ、agent と hook の実行を herdr server の ambient env に依存させない。
呼び出し元の `HERDR_CONFIG_PATH`、`HERDR_SESSION`、`HERDR_SOCKET_PATH`、`HERDR_CLIENT_SOCKET_PATH` は workload env へ復元しない。
agent workload 内から利用する Herdr CLI も control-plane runner を唯一の入口とし、raw `herdr` を workload env で実行せず、owned XDG / config / socket / session env を call ごとに再構築する。

`agent.start` の agent 名は workspace 内ではなく session 全体で一意が要求され、重複は `agent_name_taken` で失敗する(Pass 2 レビュー時の 0.7.3 実機確認)。
複数の repo や親が同じ session を共有しても衝突しないよう、launch 名は repo、親参照、intent に保存した agent launch nonce の hash から `core/naming` で生成する。

### process exit

bare `/usr/bin/false` は `agent_started` を返した後に終了し、pane と agent record は消えた。
public snapshot と `pane_exited` event には exit status と終了後 shell が残らなかった。
server log は exit status を記録するが public API 契約ではないため、fanout は判定に使わない。
`events.subscribe` では child process の exit 0 と exit 7 がどちらも `pane_exited`、明示 `pane close` が `pane_closed` になった。
`pane_exited` の payload は pane ID と workspace ID だけで、exit code を含まない。
同じ server process 内では終了後の subscription に `pane_exited` が replay されたが、server の cold restart 後は replay されなかった。
`events.wait` の `pane_exited` match は schema にあるものの、0.7.3 の runtime は `unsupported_event_wait_match` を返した。
`agent start` の flag、`AgentStartParams`、default config に process 終了後も pane を残す設定はなかった。

次の wrapper は exit status を表示して shell を残せた。

```console
/bin/sh -lc '/usr/bin/false; rc=$?; printf "WRAPPED_EXIT=%s\n" "$rc"; exec /bin/sh'
```

ただし herdr が追跡する process は wrapper になり、agent record は `unknown` のまま残った。
wave 2 の自動 launch は bare argv を採用し、tmux backend の「agent 終了後も shell を残す」契約を herdr backend には持ち込まない。
#427 は fanout CLI を呼ぶ runtime 非依存の telemetry emitter として agent の報告状態を `state.json` へ記録する。
Claude は `agent start` の argv へ `--settings` lifecycle hook を注入する。
Codex は launch-scoped の provider hook adapter から同じ emitter command を呼び、exact event-to-state mapping と注入成功を検証できる場合だけ state refinement を有効にする。
Codex adapter が未実装、注入不能、または検証不能なら `reported_state` を未設定のままにして nudge を no-op とする。
final row の `state_refinement` は provider hook adapter と mapping の検証に成功した launch だけ `true` とする。
tmux pane option は使わない。
launch 時に owner の絶対 `FANOUT_STATE_PATH`、state row key、launch ごとの opaque emitter nonce、backend、session / workspace / agent identity を hook 環境へ注入する。
row key は `TaskID` が非空なら `(parent, taskId)`、それ以外は `(parent, issueNum)` とする。
emitter nonce は state row にも保存し、再 launch ごとに更新する。
state refinement を有効にした agent は final row 確定時の `reported_state` を `running` で初期化し、その後は provider hook の `working` / `plan` / `blocked` / `idle` / `done` だけで更新する。
これらの値は agent が起動する tool と checkout 内 script に継承されるため、secret、capability、event provenance の証明にはならない。
agent process は正規 hook と同じ emitter call を偽造できる。
emitter signal は協調プロセスの `reported_state` telemetry に保存し、tmux と同じ `shouldNudge` gate の入力には使うが、完了判定または cleanup の根拠には使わない。
launch は planning から final row の確定、intent の削除、または fail-closed 状態の保存まで同じ state lock を保持し、signal が生成されたことを理由に lock を解放しない。
同期 hook の emitter command は lock を待ち、その間も agent process と pane を生存させる。
launcher は hook の完了を待たずに `agent start` 応答を処理する。
emitter は同じ lock を取得した後の state で分岐し、final row があれば `reported_state` update、matching intent だけがあれば pending 保存を実行する。
`agent start` の応答前に届いた signal は authoritative state を更新せず、key、nonce、backend、session / workspace / agent identity が provisional intent と完全一致する場合だけ state lock 下で pending telemetry として保存する。
pending `done` は同じ nonce の先行 telemetry より優先するが、final row 確定前は query 結果へ出さない。
`agent start` の応答後、返された PaneRef を同じ nonce に束縛し、pending telemetry を検証して final row の `reported_state` へ反映する。
応答を回復できない intent の pending telemetry は final row へ移さない。
emitter は state lock 下で key、nonce、backend が完全一致する行が一つだけで、保存済み PaneRef が launch 時の binding と現在の runtime にも一致する場合だけ `reported_state` を更新する。
0 件、複数件、世代不一致、PaneRef 不一致は fail closed にする。
`terminal_id` の変化を検出した時点で、provider 固有 matcher を始める前に state lock 下で `reported_state` を未設定、`state_refinement:false` にし、emitter nonce を新しい process epoch へ回転する。
旧 nonce または旧 `terminal_id` に束縛された signal は拒否する。
cwd や slug から更新先を再解決しない。
Claude の `SessionEnd` 由来の `done` も診断用 telemetry に留める。
Claude と Codex は pane 消滅時に正常終了と外部からの kill を区別できないため、state row の有無にかかわらず `stale` とする。
identity 不一致も `stale` とする。

### cold restart

公式 session ref を持たない shell と wrapper の cold restart 前後は次のとおりだった。

| 項目 | restart 前 | restart 後 |
|---|---|---|
| workspace、tab、pane public ID | `w1` から `w7` | 同じ ID |
| cwd、layout、worktree provenance | 記録あり | 維持 |
| `terminal_id` | 各 pane の元 ID | 全 pane で新しい ID |
| live process | shell、wrapper | restore shell に置換 |
| `agent list` の name | `shell-agent`、`wrapped-shell` | name は残る |
| agent status | `idle` または `unknown` | `unknown` |
| `report-metadata` | title、display agent、state label あり | 消失 |

`idle` を記録した shell pane は restart 前の snapshot で focus 中だった。
focus されていない agent の `idle` が public `done` へ変わる遷移とは別の観測である。

次に `resume_agents_on_restore=true` と herdr 公式 Codex integration v6 を使い、Codex session `019f6908-3bc1-7c83-98df-d8ea91694d2c` を実際に resume した。

| 項目 | restart 前 | attach 前 | attach 後 |
|---|---|---|---|
| public pane ID | `w4:p2` | `w4:p2` | `w4:p2` |
| cwd | この worktree | 同じ | 同じ |
| `agent_session` | `herdr:codex`、`codex`、`id`、上記 ID | 完全一致 | 完全一致 |
| `terminal_id` | `term_656b2482088076` | `term_656b25521cdc25` | attach 前と同じ |
| agent status | `done` | `idle` | `working` |
| process | `codex` | resume 待ち | `codex resume <id>` |

attach 前の `idle` は公式 integration の resume placeholder であり、live agent が focus されていない状態で `idle` を報告した値ではない。
通常の「focus されていない idle は public `done`」という状態遷移には含めない。
この placeholder は process の生存証拠にも `shouldNudge` の入力にも使わず、`reported_state` が未設定なら nudge は no-op とする。

attach 後の pane には restart 前の prompt と `PROBE_OK` の応答履歴が復元された。
`pane process-info --pane w4:p2` は foreground process の argv が `codex resume 019f6908-3bc1-7c83-98df-d8ea91694d2c` であることも返した。
この実測から、`terminal_id` の変化だけでは論理上の会話の喪失を判定できない。
一方、attach 前の `agent_session` ref と `idle` だけでは process の生存を判定できない。

herdr backend が cold restart 後に running へ再束縛できる provider は、今回実測した herdr 公式 Codex integration v6 だけとする。
保存済み `agent_session` は `{source:"herdr:codex", agent:"codex", kind:"id", value:<session-id>}` と完全一致し、現在の pane に同じ ref が一つだけ存在しなければならない。
attach 後の `pane process-info` は foreground process の候補を一つだけ返し、OS process 情報から解決した実 executable が final row に保存した Codex executable の絶対パスと一致しなければならない。
候補の argv は argv0 を除く引数列が `["resume", "<session-id>"]` と完全一致し、追加引数を許可しない。
`<session-id>` は保存済み `agent_session.value` と byte-for-byte で一致させる。
候補の `process-info.foreground_processes[].cwd` は final row に保存した `agent start --cwd` と完全一致させ、`pane get.cwd` または snapshot の `foreground_cwd` で代用しない。
`pane process-info` は PPID chain を返さないため、候補 PID が現在の `shell_pid` 自身またはその子孫であり、現在の `foreground_process_group_id` と同じ process group に属することを OS process 情報で別途確認する。
OS ancestry または process group を取得できない場合は再束縛しない。
すべてを同じ再対応付け cycle で確認した後、新しい `terminal_id`、PID、executable、argv、process cwd、`shell_pid`、foreground process group ID、`agent_session` を state lock 下の一回の save で process identity として束縛するが、`reported_state` は未設定、`state_refinement` は `false` のままにする。
resumed process で provider hook adapter と mapping を再検証し、回転後の emitter nonce と新しい `terminal_id` に束縛された fresh signal を受けた場合だけ `state_refinement:true` とその signal の `reported_state` を同じ state save で確定する。
adapter の再注入または検証経路がない場合は resume を妨げず、nudge だけを no-op のままにする。
wave 2 は attach 前の exact placeholder だけを再開待ちへ入れ、結果が `matched` の場合だけ process identity を束縛する。
直近の compatible snapshot でも exact placeholder が続いたまま `timed_out` した場合だけ row を `stale` とし、reason `resume_timeout` を記録する。
`cancelled` と `failed` は `stale` に読み替えず、process identity を再束縛しないが、先に保存した nudge state の失効は維持する。
Claude を含む未検証 provider、ref の欠落または重複、候補 process の欠落または重複、executable / argv / process cwd / ancestry / process group の不一致は `stale` とし、緩い process 名一致へ fallback しない。

「agent record がないなら done」だけでは restart 後を判定できない。
name が残った `unknown` record も、一致する ref を持つ再開待ちの record も、現在の agent process の生存を単独では示さない。
#427 は `unknown` を無条件に `running` へ写像せず、terminal 実体、会話の識別、process の生存を別々に判定する。

## read、入力、focus、wait

### read source

| source | 実測 |
|---|---|
| `visible` | 現在の viewport 全体を返した |
| `detection` | agent 検出に使う bottom-buffer snapshot を返した。CLI help には未掲載だが 0.7.3 で受理された |
| `recent` | scrollback の末尾を表示上の soft wrap のまま返した |
| `recent-unwrapped` | soft wrap を論理行へ戻した。取得境界が行途中の場合は先頭断片が残る |

手動実測ではログと agent 出力の読み取りに `recent-unwrapped`、TUI の視覚確認に `visible` を使った。
`pane read` は raw text を出力し、`agent read` は `pane_read` result に text、source、revision、truncated を入れる。

これらの response は content と expected immutable session generation / target `terminal_id` を一つの応答へ束縛しない。
wave 2 は `pane read` / `agent read` の直前・直後に exact PaneRef、`terminal_id`、worktree provenance を再照合し、両方が一致した場合だけ取得結果を UI、ログ、state、agent / LLM input へ公開する。
別接続の post-read snapshot が一致しても、read 中だけ session が差し替わる ABA を排除できないため authority にはしない。
この ABA は tmux-parity tier の受容済み残余リスクとする。
request / response が authoritative server generation と target terminal identity を原子的に束縛できる場合は proof-grade tier へ格上げする。

手動実測した `pane get` の `cwd` は label、follow-cwd、session restore に使われる pane または workspace の cwd を表す。
`foreground_cwd` は現在 PTY を制御する foreground process の cwd を表す。
実際、foreground で `(cd /; sleep 15)` を実行している間も `cwd` は元 repository のままで、`foreground_cwd` は `/` になった。
`process-info.foreground_processes[].cwd` は候補 PID に結び付いた process cwd であり、cold-restart matcher はこの値だけを使う。
`pane get.cwd` と snapshot の `foreground_cwd` は process cwd の照合を代替しない。

`foreground_cwd` は表示と診断の telemetry とし、PaneRef の識別または生存判定には使わない。
wave 2 の targeted structured read は、PaneRef の routing を backend、session namespace、workspace ID、pane ID で行う。
記録した launch との一致は `terminal_id`、task との対応は workspace の `repo_key` と `checkout_path` を含む worktree provenance で別々に検証する。
worktree provenance がない generic workspace では、fanout state に保存した checkout path と pane の `cwd` を補助照合に使う。

### send と nudge

`pane send-text` は literal text だけを送り、別の `pane send-keys enter` まで shell cwd は変わらなかった。
`agent send` も literal text を直ちに送った。
agent status を working または blocked と報告した状態でも送信され、queue と blocked gate はなかった。

blocked 状態へ送った文字列は画面に入り、Enter は送らずに `ctrl+u` で消した。
文字列と Enter を別操作にすると、その間に permission dialog が focus される事故を防げない。

`pane run` は text と Enter を一操作で送り、command を実行した。
ただし status read と submit の間の race は残る。
親設計の「`agent send` を preferred nudge にする」は採用しない。

herdr の `done` は process exit ではなく、agent が処理を終えて `idle` になった後にまだ focus されていない状態である。
実際、focus されていない pane に `working`、`idle` の順で報告すると snapshot は `working`、`done` と遷移し、`terminal_id` と focus は変わらなかった。
focus 後は `done` から `idle` へ変わる。

2026-07-21 JST のユーザー決定により、herdr backend の自動 nudge を tmux-parity tier で解禁する。
対象 agent は state refinement を持つ Claude と Codex に限る。
provider hook adapter の注入と mapping を検証できない agent は、名前が Claude または Codex でも state refinement なしとして no-op にする。
trim 済み `reported_state` が `running`、`working`、`plan`、`idle` の場合だけ送信候補とし、`blocked`、`done`、未設定、未知値は no-op とする。
送信直前に live snapshot を一回取得し、保存済み backend / session / workspace / pane、`terminal_id`、`agent_session`、worktree provenance、agent を再照合する。
次に state lock 下で最新 row を再読し、row key、emitter nonce、PaneRef、`state_refinement:true` が同じ launch に一致することを確認して、その row の `reported_state` を `shouldNudge` へ渡す。
Herdr snapshot の native public status を `reported_state` の代用にしない。
照合成功時だけ lock を解放して `pane run` を一回発行する。
recipient の欠落、pane 消滅、identity 再利用、不一致、非許可状態、runtime failure、send failure は message bus の保存を維持した no-op success とする。
hook telemetry は agent process から偽造でき、screen detection は未知の permission UI を除外できない。
この signal は tmux の `@fanout_agent_state` と同じ協調プロセス信頼で `shouldNudge` の入力に使い、screen manifest または `agent explain --json` は送信許可に使わない。
`terminal_id`、`agent_session`、worktree provenance を送信直前に再照合しても、その後の `pane run` までに pane の状態は変わり得る。
herdr 0.7.4 には状態条件付き send または CAS がなく、この check-and-send race は tmux の `ListLive` から non-transactional な `send-keys` までの race と同種の受容済み残余リスクである。
runtime の atomic conditional send または terminal permission UI を操作しない out-of-band queue のいずれかと、agent process から分離した event provenance が揃えば proof-grade tier へ格上げする。
`codexPlanMode` は `plan` state の通常 nudge と別契約であり、agent 種別にかかわらず恒久的に除外する。
peer message の bus への保存は維持するが、fanout は herdr の `notification show` を nudge に使わない。

### focus と wait

`agent focus <name>` と `workspace focus <id>` は対象を正確に focus した。
`--no-focus` は二つ目以降の worktree と agent 起動で focus を維持した。
wave 2 は TUI の明示操作または focused child の cleanup 後だけ focus を発行し、target identity を送信直前に再照合する。
focus check と mutation の race は tmux focus と同種の受容済み残余リスクとし、background watcher は focus を奪わない。
request-bound server / target generation が使える場合は proof-grade tier へ格上げする。

`wait output` は出力遷移を待ち、`output_matched` と matched line、read result、revision を返した。
`agent wait --status idle` は current state がすでに idle でも即時成功せず、後続 event を待った。
focus されていない agent が `idle` を報告した場合は `done` event が返り、`agent focus` 後に `idle` へ変わった。

snapshot 後に event wait を登録すると、二つの操作の間に起きた遷移を逃す lost-wakeup race がある。
CLI-first の wave 2 は次の共有 budget を持つ snapshot polling を使い、CLI の一回待機を current-state predicate と組み合わせない。

- 一回の wait または cold-restart reconciliation は、最初の `herdr api snapshot` の直前に monotonic clock で一つの deadline を確定する。
- `total_timeout` は 3 秒以上の整数秒で受け取り、既定値を 300 秒として、無期限待機を許可しない。
- 最初の snapshot は直ちに呼び、次の呼び出しは前回の開始から 2 秒以上空け、遅れた tick を追い掛けず、複数の CLI process を同時実行しない。
- 各 herdr CLI process の timeout は `min(5 秒, deadline までの残時間)` とする。
- snapshot の最大呼出し回数は開始時に `ceil(total_timeout / 2 秒)` へ固定し、既定値は 150 回とする。
  最小値の 3 秒では初回と 2 秒時点の再取得の最大 2 回を許す。
- 一つの polling cycle では snapshot を一回だけ呼び、snapshot の current-state predicate だけを評価する。
- cold-restart reconciliation cycle に限り、一意な resume 候補を得た場合に `pane process-info` と各 OS process identity 検査を一回ずつ実行する。
- snapshot、補助検査、parse、interval sleep、retry は同じ deadline を消費し、valid snapshot、状態変化、placeholder、retryable error を観測しても deadline と呼出し上限を更新しない。
- version / schema の不一致と malformed snapshot は retry せず、直ちに `failed` とする。
- CLI timeout または non-zero exit は同じ budget 内でだけ retry し、budget 終了時の直近 cycle が失敗していた場合、または compatible snapshot を一度も取得できなかった場合は `failed` とする。
- terminal result は `matched`、`timed_out`、`cancelled`、`failed` の四値に固定する。
- v1 wait は compatible snapshot が predicate を満たした場合に `matched` とする。
  cold-restart reconciliation は compatible snapshot と必要な補助検査が predicate を満たした場合に `matched` とする。
  直近 cycle が valid な compatible snapshot を返したまま deadline または呼出し上限へ達した場合は `timed_out` とする。
- caller context の cancellation または SIGINT を受けた場合は interval sleep と実行中の process tree を止めて reap し、`cancelled` を返す。
- `cancelled` または `failed` の後は別の CLI call と state mutation を開始しない。

event 駆動へ移す後続版は raw Socket で subscription を確立してから snapshot を取得し、以後の event と再同期を処理する。

## 実行環境と UI の境界

### backend 検出

`agent start` の direct process には `HERDR_ENV=1` が届いた。
generic workspace shell でも `HERDR_ENV=1`、session、socket、pane、tab、workspace ID と `workspace create --env FANOUT_PROBE=v073-ok` の値が届いた。
当初の検査では `grep` の pattern が `^(FANOUT|HERDR|TMUX)=` だったため、`FANOUT_PROBE`、`HERDR_ENV`、`TMUX_PANE` をすべて落とす偽陰性になった。
公式 v0.7.3 バイナリで `^(FANOUT|HERDR|TMUX)(_|=)` を使って再測定し、この誤りを訂正した。

fanout は `HERDR_ENV=1` を tmux の env より先に判定し、generic workspace shell から backend を自動検出する。
`--backend` と `FANOUT_BACKEND` は明示的な上書きとして残す。

herdr pane 内で nested tmux server を起動すると、nested tmux の global env に `HERDR_ENV=1`、`HERDR_PANE_ID`、`HERDR_WORKSPACE_ID` と `TMUX` が同時に入った。
`HERDR_ENV` を `TMUX` より先に判定しても「最も内側の runtime」を選んだことにはならない。

`HERDR_ENV` と `TMUX` の両方がある場合は herdr を既定にしつつ、nested tmux の利用者が `--backend tmux` または `FANOUT_BACKEND=tmux` で明示的に上書きできる契約にする。
自動で内側を判定する場合は process ancestry の検査が別途必要になる。

### metadata と OSC

0.7.3 の `pane report-metadata` で presentation fields の title、display agent、state label を設定できた。
agent pane から OSC title sequence を出しても、これらの metadata field は変わらなかった。
OSC 由来の `terminal_title` / `terminal_title_stripped` は server-owned の sidebar built-in token であり、presentation field の title と区別する。

0.7.4 は pane token と workspace metadata reporting を追加した。
0.7.3 の `pane report-metadata ... --token issue=424` は `unknown option: --token` で失敗し、0.7.4 の同じ flag は成功した。

| resource | CLI | Socket method | sidebar での参照 |
|---|---|---|---|
| pane | `pane report-metadata <pane_id> --source <id> --token <name>=<value>` | `pane.report_metadata` | Agent row の `$name` |
| workspace | `workspace report-metadata <workspace_id> --source <id> --token <name>=<value>` | `workspace.report_metadata` | Space row の `$name` |

0.7.4 の `api schema --json` は両 method の `tokens`、`seq`、`ttl_ms` を返し、`tokens` は 1 report あたり最大 16 key、key は `^[A-Za-z0-9_-]{1,32}$`、`ttl_ms` は 1 から 86400000 と定義していた。
token map は patch であり、CLI の `--token` または Socket の string value で設定、CLI の `--clear-token` または Socket の `null` で削除し、未指定 key は維持する。
pane / workspace はそれぞれ最大 32 key を保持し、value は trim と control character 除去後の 80 文字までで、空になれば削除する。
同じ key は最後に受理された reporter の値が勝つため、fanout は `fanout_` prefix で衝突を避ける。
同じ source の古い `seq` は success を返しても反映されず、実測では `seq=3` の `ci=success` を後続の `seq=2` が上書きしなかった。
`--clear-token branch` は `branch` だけを削除した。

sidebar layout は次の config で設定できた。

```toml
[ui.sidebar.agents]
row_gap = 0
rows = [["state_icon", "workspace", "tab"], ["$fanout_parent", "$fanout_pr", "$fanout_ci"], ["agent"]]

[ui.sidebar.agents.rows_by_agent]
codex = [["state_icon", "workspace"], ["$fanout_parent", "$fanout_pr"], ["agent"]]

[ui.sidebar.spaces]
row_gap = 1
rows = [["state_icon", "workspace"], ["$fanout_issue", "$fanout_ci"], ["branch", "git_status"]]
```

`rows` の内側の配列が 1 表示行であり、`rows_by_agent` は canonical agent ID に一致した Agent の `rows` 全体を置換する。
`rows` は最大 16 行、各行は最大 16 token で、展開した desktop sidebar にだけ適用する。
`row_gap` は entry 間の空行数で、既定値は 0、1 にすると従来の entry 間隔へ戻る。
Space 内で連続する indented worktree child は `row_gap` にかかわらず packed のままになる。
実機では 2 つの Space entry に `#424 · success` と `#425 · pending` が描画され、`row_gap = 1` の空行も入った。
Agent entry には pane token の `#423 · #499 · success` と agent 名 `sidebar-probe` が別の row に描画された。
wave 2 の reporter は token 値だけを提供し、row と styling は herdr とユーザーが所有する。

pane / workspace token は `api snapshot` に返ったが、cold restart 後の snapshot から消えた。
wave 2 は初回と cold restart reconciliation 後に `report-metadata` を発行する。
metadata が表示専用であることは、同名 session の差し替え後に無関係な pane / workspace へ issue、PR、CI token を書く race を安全にはしない。
report 前に exact backend / session / workspace / pane、`terminal_id`、worktree provenance を再照合し、不一致なら発行せず fail closed にする。
report 後の再照合で不一致なら結果を不明として fail closed にし、自動再送しない。
照合と report の race は tmux-parity tier の受容済み残余リスクとする。
request が authoritative server generation と target `terminal_id` / workspace generation を原子的に束縛できる場合は proof-grade tier へ格上げする。
`seq` は reporter 内の順序制御であり、cold restart で失われるため identity precondition の代用にしない。
metadata は表示専用データとし、`state.json`、liveness、nudge authority、完了判定には使わない。

### Shift+Enter

attach 中の herdr client に Shift+Enter の `ESC [ 13 ; 2 u` を送ると、pane 側は `ESC [ 27 ; 2 ; 13 ~` を受信した。
fanout の TUI parser は後者を Shift+Enter として処理する。

実際の `__tui-new-pane-popup` に `line-one`、Shift+Enter、`line-two` を入力した結果は次のとおり。

```json
{
  "prompt": "line-one\nline-two",
  "agents": ["codex"]
}
```

herdr pane 内でも fanout の複数行 prompt を使える。

### notification

default config の `delivery="off"` では `notification show` が `shown:false`、`reason:"disabled"` を返した。
隔離 config で `delivery="herdr"`、`delay_seconds=0` にすると、client 未 attach と attach 中の両方で `shown:true`、`reason:"shown"` を返した。
attach 中は PTY 出力に title と body の toast 描画を確認した。

detached 時の `shown:true` は server が request を受理したことを示すが、利用者の画面へ表示済みであることは示さない。
fanout notify backend は herdr の `notification show` を呼ばない。
同名 session の差し替え時に別 session へ内容を送る TOCTOU を閉じられないため、設定済み利用者が fanout の外から使う手動実測面としてだけ残す。

## Socket schema と JSON 契約

`herdr api schema --json` は protocol `16`、schema version `1` を返した。
0.7.4 の server 停止中と起動中に取得した schema は byte 単位で一致し、SHA-256 はどちらも `4c138114dfdb8ed9ddb906fcd117967ca7fac0c10251abf238452c52b766d49c` だった。
schema は client binary に同梱された capability document であり、接続先 server の attestation ではない。
top-level は `$schema`、`protocol`、`schema_version`、`schemas`、`title` を持ち、request の `oneOf` は 85 method を列挙した。
fanout が使う `session.snapshot`、`workspace.create`、`workspace.close`、`workspace.report_metadata`、`worktree.create`、`worktree.open`、`worktree.remove`、`agent.start`、`pane.process_info`、`pane.report_metadata`、`pane.send_text`、`pane.send_keys` の params と response shape を structural gate で検査できる。
CLI の `pane run` は Socket method ではなく send method を組み合わせる CLI surface なので structural gate では検査できず、wave 2 実装は command surface と出力を別の fail-closed gate で検査する。
request は top-level の `id` を必須とし、各 `oneOf` variant がさらに `method` と `params` を必須にする。

```json
{
  "properties": {
    "id": { "type": "string" },
    "method": { "const": "agent.start", "type": "string" },
    "params": { "$ref": "#/schemas/request/$defs/AgentStartParams" }
  },
  "required": ["id", "method", "params"],
  "type": "object"
}
```

上は top-level required(`id`)と `oneOf` variant(`method`、`params`)を合成した、request 1 件の実効 shape である。
以降の一覧は request 全体ではなく各 method の params shape を、schema の `required` と optional field に分けて示す(`session.snapshot` の required が空なのは params に必須項目がないという意味で、request 自体は `id`、`method`、`params` を要する)。

```text
session.snapshot:
  required: []
  optional: []

worktree.create:
  required: []
  optional: [branch, base, path, workspace_id, cwd, label, focus=false]

worktree.open:
  required: []
  optional: [branch, cwd, focus=false, label, path, workspace_id]

worktree.remove:
  required: [workspace_id]
  optional: [force=false]

agent.start:
  required: [name, argv]
  optional: [cwd, env, focus=false, split, tab_id, workspace_id]

pane.read:
  required: [pane_id, source]
  optional: [format="text", lines, strip_ansi=true]

pane.process_info:
  required: []
  optional: [pane_id]

pane.wait_for_output:
  required: [pane_id, source, match]
  optional: [lines, timeout_ms, strip_ansi=true]
```

`worktree.remove` には expected label、path、nonce、session epoch の precondition がない。
`worktree.create` / `worktree.open` には setup hook を抑止する field も、照合済み plugin registry generation を mutation の precondition にする field もない。
`pane_process_info` result は `shell_pid`、`foreground_process_group_id`、候補ごとの PID、argv、cwd を返すが、親 PID または PPID chain を含まない。
実 executable、ancestry、process group membership は OS process 情報から検証する。

success と error の envelope は次の形だった。

```json
{"id":"...","result":{"type":"..."}}
{"id":"...","error":{"code":"...","message":"..."}}
```

`herdr api snapshot` は raw method `session.snapshot` を CLI から呼び、workspace、tab、pane、layout、agent、focused ID を一つの `session_snapshot` で返す。
worktree provenance は workspace に入るが、parent workspace ID と session UUID は入らない。
pane と agent の `agent_session` は optional で、存在する場合は `source`、`agent`、`kind`、`value` を必須にする。
`kind` は `id` または `path` である。
`pane.report_agent_session` は公式 integration が Socket method として使い、fanout は呼ばない。
fanout は snapshot の ref を読むだけにし、同じ ref を重複報告しない。
ref が欠落、不一致、重複した場合は fail closed にする。

raw Socket の `events.subscribe` は常駐 client を増やすため wave 2 では使わない。
`agent.send`、`pane.send_input`、`pane.focus` も schema にはあるが、手動検証には CLI の `pane run`、`agent focus`、`workspace focus` で足りる。

## version と JSON 対応

stable public workspace、tab、pane ID の契約は 0.7.0、既存 local branch の worktree create/open は 0.7.1、`session.snapshot` と `api schema --json` は 0.7.2 で入った。
core runtime の exact tuple allowlist は廃止し、stable SemVer `>=0.7.4`、structural capability、接続先 status の三段 gate へ置き換える。
version 文字列は stable SemVer として parse し、prerelease と解釈不能な値を拒否する。
schema が定義する version string には prerelease を拒否する pattern がないため、fanout が検査する。

起動前は admitted binary の SHA-256、`herdr --version`、offline `api schema --json` を取得し、protocol、schema version、使用 method、request / response の必須 field と、それらが参照する type、enum、const を再帰的に検査する。
server 接続後は `status --json` の client / server version が同じ stable version で floor 以上、protocol が admitted schema と一致、session と socket が admitted attach と一致、`compatible:true`、`restart_needed:false` であることを要求する。
`api snapshot` の version と protocol も同じ admitted server に一致させる。
full schema gate は admitted binary SHA-256 ごとに一回、connected status / snapshot gate は attach ごとに一回実行する。
各 CLI call は executable path と SHA-256、response の必須 field を再検査し、version と protocol を持つ response ではその値も再検査する。
0.7.4 は比較可能な server generation を返さないため、connection loss、restart、binary drift、`restart_needed:true`、その他の不一致を検出した場合は admission を失効させる。
owned server の restart 後は三段 gate を最初から再実行し、成功した場合だけ Codex integration v6 exact matcher の resume または通常操作へ進む。
別 CLI 接続間の server continuity は証明できず、この gate を mutation authority にしない。
schema にない CLI-only surface は structural admission の対象外とし、wave 2 で使う surface は command と出力を別の fail-closed gate で検査する。

0.7.3 client から 0.7.4 server と、0.7.4 client から 0.7.3 server の両方向を実測した。
どちらも protocol `16` で `compatible:true` と snapshot success を返したが、`restart_needed:true` で client / server version は不一致だった。
0.7.4 client の `api schema --json` は 0.7.3 server 接続中も offline schema と同じ SHA-256 を返した。
したがって protocol compatibility と client schema だけでは接続先 server の capability を証明しない。

この gate は compatibility だけを証明し、mutation authority を与えない。
tmux-parity tier は同一 UID の協調プロセスを信頼するため mutation authority の証明を要求せず、intent / phase と送信直前再照合を crash safety と誤操作防止に使う。
request-bound immutable generation と conditional mutation、server-authenticated controller capability、agent と別 UID の server のいずれかが使える場合は proof-grade tier へ格上げする。
cleanup、metadata、targeted read も resource-specific generation を原子的に束縛できる場合は同 tier へ格上げする。

| 実測コマンド | 導入 / 実測 version（runtime capability gate ではない） | JSON 対応 |
|---|---:|---|
| `herdr --version` | 0.7.3 core baseline、0.7.4 core wave 2 | text のみ |
| `status --json` | 0.7.3 core baseline、0.7.4 core wave 2 | 明示 `--json` |
| `session list --json` | 0.7.3 core baseline | 明示 `--json` |
| `workspace create/list/focus/close` | 0.7.3 baseline、0.7.4 core wave 2 | JSON envelope を標準出力へ返す。`--json` は付けない |
| `worktree create/open/list/remove` | 0.7.1、0.7.4 core wave 2 | 明示 `--json` |
| `agent start/list/read/focus` | 0.7.3 baseline、0.7.4 core wave 2 | JSON envelope を標準出力へ返す。`--json` は付けない |
| `pane get/run/close` | 0.7.3 baseline、0.7.4 core wave 2 | mutation と get は JSON envelope。`--json` は付けない |
| `pane process-info` | 0.7.3 baseline、0.7.4 core wave 2 | JSON envelope を標準出力へ返す。対象は `--pane` で指定する |
| `pane read` | 0.7.3 baseline | text または ANSI を直接出力する |
| `pane report-metadata` の presentation fields / seq / TTL | 0.7.3 | 成功時は出力なし（`--json` 非対応） |
| `pane report-metadata` の token patch | 0.7.4 | 成功時は出力なし（`--json` 非対応） |
| `workspace report-metadata` の token / seq / TTL | 0.7.4 | 成功時は出力なし（`--json` 非対応） |
| `api snapshot` | 0.7.2、0.7.4 core wave 2 と token projection | JSON envelope を標準出力へ返す。`--json` は付けない |
| `api schema --json` | 0.7.2、0.7.4 structural gate | 明示 `--json` |
| `notification show` | 0.7.3 baseline | JSON envelope を標準出力へ返す。`--json` は付けない |
| `plugin link/list/log list` | 0.7.3 baseline、0.7.4 isolation wave 2 | `list` は `--json` 対応。他は JSON envelope |

この表は機能導入時期と実測 provenance を示すだけで、floor 未満または structural gate を通らない version の互換性を認めない。

version ごとの根拠は [v0.7.0](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.0)、[v0.7.1](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.1)、[v0.7.2](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.2)、[v0.7.3](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.3)、[v0.7.4](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.4) を参照する。

## 後続 issue への契約

#423、#425 から #429、#494 は次の制約を前提にする。

判断主体はユーザー、判断日は 2026-07-21 JST である。
herdr backend は tmux backend と同水準の協調プロセス信頼を採用し、private socket が同一 UID の認証境界にならない実測を受容したうえで、影響範囲を fanout-owned session へ封じ込める。
各機能の wave 2 判定と proof-grade tier への格上げ条件は次のとおりである。

| 機能 | wave 2 | tmux-parity tier の条件 | proof-grade tier への格上げ条件 |
|---|---|---|---|
| owned bootstrap / launch | Go | caller routing env を上書きする owned XDG / socket / marker、control-plane / workload env の分離、capability gate、intent / phase、nonce 二重照合、空の setup-hook registry | controller capability、server / agent の UID 分離、または request-bound conditional mutation。setup hook を使う場合は suppression / registry generation / operation-scoped receipt |
| cleanup / rollback | Go | exact ownership と dirty / force 条件を送信直前に再照合し、response loss では blind retry しない | remove / close が authoritative server generation と target resource generation を原子的に検査する |
| #427 emitter | Go | cooperative telemetry と `shouldNudge` gate に限り、completion / cleanup authority にしない | agent process から分離した event provenance |
| Codex v6 cold restart resume | Go | exact `agent_session`、executable、argv、cwd、ancestry、process group matcher。旧 nudge state を失効し、fresh emitter signal まで no-op | authoritative server generation と launch provenance の原子的な束縛 |
| #494 metadata | Go | exact target の直前・直後照合、固定 source、seq / TTL、表示専用 token | `report-metadata` が authoritative server generation と target generation を原子的に検査する |
| focus | Go | TUI の明示操作だけが target を直前再照合する | request-bound server / target generation |
| peek / targeted read | Go | exact PaneRef、`terminal_id`、worktree provenance を直前・直後に再照合する | response が authoritative server generation と target terminal identity を束縛する |
| 自動 nudge | Go | state refinement を持つ agent の `running` / `working` / `plan` / `idle` だけへ、送信直前再照合後に `pane run` を一回発行する | atomic conditional send または permission UI を操作しない out-of-band queue と、agent process から分離した event provenance |
| `codexPlanMode` | 恒久除外 | runtime backend から terminal UI を操作する controller は採用しない | 再評価しない |

request-bound generation / conditional mutation、controller capability、UID 分離は削除せず、herdr 上流へ別 issue で提案する proof-grade 強化として保持する。
response loss 時の no-blind-retry、provisional intent と phase machine、workspace label と git-dir marker、branch の atomic reservation と compare-and-delete、bare argv と exact cwd / env、identity の分離、Codex integration v6 exact matcher、wait budget、metadata の表示専用性は維持する。
emitter は telemetry のまま `shouldNudge` の協調 signal に使い、完了判定または cleanup の証明には使わない。

- backend は per-repo supervisor が owned XDG / socket / marker を exclusive create して foreground `herdr server` child を bootstrap する。
  supervisor は呼び出し元から継承した Herdr routing env の値に依存せず、`status` と bootstrap を含む各 Herdr CLI call 用の env を構築し、owned XDG、`HERDR_CONFIG_PATH`、`HERDR_SESSION`、`HERDR_SOCKET_PATH`、`HERDR_CLIENT_SOCKET_PATH` を fanout-owned 値で上書きする。
  完全一致する owned marker は restart reconciliation に使い、不一致、foreign、または検証不能な socket / marker は停止せず fail closed にする。
  console detach 後も server を存続させ、最後の child close では止めず、active intent、row、foreign resource のない明示 repo-session shutdown だけを teardown とする。
- herdr backend wave 2 は snapshot / list / wait、targeted content read、root coordinator、worktree / agent launch、focus、nudge、metadata、cleanup を後続実装へ解禁する。
  各 operation は保存済み identity と live snapshot を直前に再照合し、operation 固有の事後条件を検査する。
  check と operation の間の race は tmux-parity tier の受容済み残余リスクとし、不一致、重複、response loss、mutation 不明では fail closed にする。
- core runtime compatibility は exact tuple allowlist ではなく、stable CLI / server `>=0.7.4`、structural schema、接続先 status の gate で判定する。
  prerelease、解釈不能または floor 未満の version、client / server 不一致、protocol / schema 不一致、session / socket 不一致、`restart_needed:true`、使用 method、必須 field、参照される type / enum / const の欠落または不一致は fail closed にする。
  full schema gate は admitted binary SHA-256 ごとに一回、connected status / snapshot gate は attach ごとに一回実行する。
  各 CLI call は executable path と SHA-256、response の必須 field を再検査し、version と protocol を持つ response ではその値も再検査する。
  connection loss、restart、binary drift、`restart_needed:true`、その他の不一致は admission を失効させ、owned server の再起動後に gate を最初から通す。
  gate が再び成功した場合だけ Codex integration v6 exact matcher の resume または通常 operation へ進む。
  schema にない CLI-only surface は structural admission の対象外とし、wave 2 で使う surface は command と出力を別の fail-closed gate で検査する。
  この gate は compatibility だけを証明し、0.7.4 private socket または fanout 側 marker と合わせても mutation authority にはしない。
  tmux-parity tier は同一 UID の協調プロセスを信頼するため、その proof を operation の前提にしない。
- backend 選択の resolver は final state rows と provisional intents の両方を入力にする。
  legacy row の空 backend は tmux に正規化する。
  実際の issue / Project / plan の親では、既存 rows / intents が一つの backend に一致する場合だけその backend を再利用し、mixed state または `--backend` / env との不一致は fail closed にする(明示的な移行はユーザー操作)。
  stickiness の単位は実際の issue / Project / plan の親に限る。
  wave 2 は親 issue の orchestrator pane を `@manual` の負番号 row として保存するが、issue / plan の provenance を実親へ帰属させて同じ stickiness 判定に含める — coordinator 作成後に child launch が失敗した再実行が coordinator の backend を見落とさないようにする。
  それ以外の `@manual` synthetic launch は互いに独立した launch の集まりであり、row identity とその intent の単位で backend を固定する。
- canonical git common directory で識別する per-repo session を使う。
  repo root の console workspace、実際の親ごとの coordinator workspace、sibling child workspace を配置し、coordinator の `@manual` 負番号 row の provenance は実際の親へ帰属させる。
  linked worktree は session を共有し、独立 clone は full common-directory identity の hash で分離する。
  create は `--no-focus` とし、TUI の明示 launch だけが focus を移す。
  per-repo supervisor が foreground server child を所有し、console detach 後も存続させる。
  最後の child close では停止せず、active intent、row、foreign resource のない明示 repo-session shutdown だけを teardown とする。
- 0.7.4 wave 2 は `worktree create`、`worktree open`、続く `agent start` を state machine から自動実行する。
  fanout-owned XDG の plugin registry と config を直前に照合し、setup hook が空の場合だけ tmux-parity tier の launch を続ける。
  setup hook がある場合は、atomic suppression、registry generation precondition、または operation-scoped completion receipt を持つ proof-grade tier まで fail closed にする。
- fanout が worktree safety gate と idempotency を所有し、herdr は checkout と workspace の実体化を担当する。
  create / open の mutation 前に provisional intent を保存し、同じ worktree ownership nonce を workspace label と checkout git dir marker の両方で照合する。
  各 worktree mutation の直前に phase `worktree-starting`、exact request、per-step pre-state を保存し、starting の再実行では対象資源がなくても request を再発行しない。
  response loss は phase と事後条件から `worktree-realized` まで回復し、mutation の有無を証明できない場合は intent を残して fail closed にする。
  `worktree open` の `already_open:true` は pre-state で同じ workspace ID / label が task に束縛済みの場合だけ受理する。
  plugin registry の standalone read は proof ではないが、tmux-parity tier の協調プロセス前提で setup hook が空であることを確認する operation gate に使う。
  `worktree-realized` からは空 registry の再確認と checkout baseline の保存後にだけ `worktree-ready` へ進み、Git 状態の連続一致や静止時間を completion proof に使わない。
  `worktree create` request の発行後に事後条件違反、応答喪失、または mutation の不明が生じた場合は intent、資源、branch reservation を残し、`manual_cleanup_required` として fail closed にする。
  成功応答と exact ownership を保存した資源は、直前再照合後の unconditioned remove で rollback できる。
  response loss または mutation 不明では rollback せず、rollback に依存する git fallback も実行しない。
  branch reservation は worktree mutation が起きていないことを証明できる場合だけ compare-and-delete で解放する。
- Herdr control-plane env と agent workload env を分離する。
  supervisor の owned XDG を設定する前に、呼び出し元の `HOME` / `PATH` と effective XDG 4 変数を workload env として保存し、未設定値は XDG の `$HOME` 基準の default path に解決する。
  agent は bare argv、明示 `--cwd`、fanout 固有値、保存した `HOME` / `PATH` と workload XDG を渡す明示 `--env` で起動する。
  呼び出し元の Herdr routing env は agent へ復元せず、agent workload 内の Herdr CLI は control-plane runner を唯一の入口として owned XDG、`HERDR_CONFIG_PATH`、`HERDR_SESSION`、`HERDR_SOCKET_PATH`、`HERDR_CLIENT_SOCKET_PATH` を再構築する。
  launch 名は repo と親参照に agent launch nonce の hash を加え、同じ intent では安定し session 全体で一意になる決定論的名前を `core/naming` で生成する。
  `agent start` 前に agent launch nonce、emitter nonce と telemetry routing binding、agent name、provider、絶対 executable、argv / env fingerprint、argv、exact `--cwd` を intent の phase `agent-starting` として保存し、provider が返す exact session ref も取得後に保存する。
  `agent-starting` で応答を保存できなかった場合は launch 時の `terminal_id` を証明できないため、一意な同名 agent が見つかっても自動採用せず fail closed にする。
  `agent-started` の回復は保存済み `terminal_id` が現在値と一致する場合だけ元の argv / process cwd を照合し、変わった場合は provider 固有の cold-restart matcher へ分岐する。
  final row の確定では provider、絶対 executable、元 argv、exact `--cwd`、exact session ref を intent から移し、intent 削除と同じ state save で永続化する。
  保存済み応答後に pane が消滅した場合は returned PaneRef を束縛した `stale` row を確定し、pending `done` は telemetry としてだけ保存する。
  応答未保存の agent 欠落、重複、識別不一致は fail closed にする。
- snapshot / list / wait の read-only CLI は保存済みの検証済み socket path を明示的に選択する(`HERDR_SOCKET_PATH` が `HERDR_SESSION` より優先されるため)。
  session identity の再確認は routing / identity / status validation、crash recovery、誤操作防止に使い、mutation authority の proof にはしない。
  `pane read` / `agent read`、`pane get` / `pane process-info` は exact PaneRef、`terminal_id`、worktree provenance を直前・直後に再照合し、不一致なら content または structured result を破棄する。
  read 中だけ session が差し替わる ABA と mutation の check-and-act race は tmux-parity tier の受容済み残余リスクとする。
  request / response が authoritative server / target generation を原子的に束縛できる場合は proof-grade tier へ格上げする。
  request-bound conditional mutation、server-authenticated controller capability、agent と別 UID の server のいずれかが使える場合も同 tier へ格上げする。
  0.7.4 private socket と fanout 側 marker はこの proof ではないが、別 UID 排除と fanout-owned session への封じ込めに使う。
  agent executable と注入 hook が呼ぶ fanout executable は、fanout の起動環境で解決した絶対パスを使う。
- #427 は fanout CLI 経由で `state.json` の `reported_state` を更新する runtime 非依存の telemetry emitter を追加する。
  Claude は `agent start` の argv へ `--settings` lifecycle hook を注入する。
  Codex は launch-scoped の provider hook adapter から同じ emitter command を呼び、exact event-to-state mapping と注入成功を検証できる場合だけ state refinement を有効にする。
  Codex adapter が未実装、注入不能、または検証不能なら `reported_state` を未設定のままにして nudge を no-op とする。
  final row の `state_refinement` は provider hook adapter と mapping の検証に成功した launch だけ `true` とする。
  tmux pane option は使わない。
  hook は child checkout を cwd として実行されるため、owner state を確実に更新できるよう、launch 時に owner の絶対 `FANOUT_STATE_PATH`、state row key、launch ごとの opaque emitter nonce、backend、session / workspace / agent identity を hook 環境へ注入する。
  注入値は tool と checkout 内 script に継承されるため secret / capability / provenance ではなく、agent process は signal を偽造できる。
  signal は協調 telemetry として表示、診断、`shouldNudge` gate に使い、完了判定または cleanup に使わない。
  state refinement を有効にした agent は final row 確定時の `reported_state` を `running` で初期化し、その後は provider hook の `working` / `plan` / `blocked` / `idle` / `done` だけで更新する。
  row key は `TaskID` が非空なら `(parent, taskId)`、それ以外は `(parent, issueNum)` とし、manual / watch 等の synthetic launch も後者で扱う。
  launch lock は final row、intent 削除、または fail-closed 状態の保存まで保持し、同期 hook は lock 待ちの間も pane を生存させ、launcher は hook 完了を待たずに `agent start` 応答を処理する。
  emitter は同じ lock の取得後に final row なら `reported_state` update、matching intent だけなら pending 保存へ分岐する。
  `agent start` 応答前の signal は authoritative state を更新せず、provisional intent と完全一致する場合だけ pending telemetry とし、返された PaneRef を nonce へ束縛する final state save で `reported_state` へ反映する。
  応答を回復できない intent の pending telemetry は final row へ移さない。
  emitter は state lock 下で key、nonce、backend が完全一致する行が一つだけで、保存済み PaneRef が launch 時の binding と現在の runtime にも一致する場合だけ `reported_state` を更新し、cwd や slug から再解決しない。
  0 件、複数件、世代不一致、PaneRef 不一致は fail closed にする。
  `terminal_id` の変化を検出した時点で、provider 固有 matcher より先に `reported_state` を未設定、`state_refinement:false` にし、emitter nonce を新しい process epoch へ回転する。
  旧 nonce または旧 `terminal_id` に束縛された signal は拒否する。
  `SessionEnd` の `done` も telemetry に留め、Claude / Codex とも pane 消滅時は正常終了と kill を区別できないため `stale` とする。
  identity 不一致も agent 種別にかかわらず `stale` とする。
- PaneRef の routing、worktree ownership、terminal 実体、論理上の会話、process の生存を別々に判定する。
- `unknown` record を無条件に running へ写像しない。
  public pane の存在、保存した `terminal_id` との一致、完全一致する一意な `agent_session`、workspace の worktree provenance、provider 固有 matcher で検証した agent process を別々に判定する。
  `foreground_cwd` は識別に使わず、worktree provenance がない場合だけ保存された `cwd` を補助照合に使う。
- 0.7.4 wave 2 は `terminal_id` が変わった row で、一致する `agent_session` があれば論理上の会話を再対応付けする候補にできる。
  running へ再束縛できる provider は実測済みの herdr 公式 Codex integration v6 だけとする。
  exact な Codex `agent_session` ref、一意な foreground process、保存済み絶対 executable、argv0 を除く引数列 `["resume", "<session-id>"]`、保存済み `agent start --cwd` と一致する process cwd を照合する。
  候補 PID が `shell_pid` 自身またはその子孫で、現在の foreground process group に属することを OS process 情報で確認し、新しい terminal / process identity を一回の state save で束縛する。
  process identity の再束縛後も `reported_state` は未設定、`state_refinement` は `false` のままとする。
  resumed process で provider hook adapter と mapping を再検証し、回転後の emitter nonce と新しい `terminal_id` に束縛された fresh signal を受けた場合だけ `state_refinement:true` と `reported_state` を同じ state save で確定する。
  adapter の再注入または検証経路がなければ resume は続け、nudge だけを no-op のままにする。
  完全一致する一意な `agent_session` と provider 固有の exact resume placeholder がある場合だけ共有 budget の再開待ちへ入れる。
  ref の欠落、不一致、重複、未検証 provider、resume 後の候補欠落または重複、executable / argv / process cwd / ancestry / process group の不一致または検証不能は retry せず、直ちに `stale` とする。
- state machine は、focus されていない agent が `idle` を報告すると public status が `done` へ変わり、focus されると `idle` へ戻る遷移を扱う。
  これは herdr runtime の表示遷移であり、fanout child の terminal completion または nudge authority には使わない。
  cold restart 後の resume placeholder で観測した `idle` はこの遷移に含めず、process の生存を別に確認する。
- herdr backend の自動 nudge は state refinement を持つ Claude / Codex に限り、tmux の `shouldNudge` と同じ条件で実行する。
  provider hook adapter の注入と mapping を検証できない agent は、名前が Claude / Codex でも state refinement なしとして no-op にする。
  trim 済み `reported_state` の `running` / `working` / `plan` / `idle` は送信候補、`blocked` / `done` / 未設定 / 未知値は no-op とする。
  live snapshot で backend / session / workspace / pane、`terminal_id`、`agent_session`、worktree provenance、agent を送信直前に再照合する。
  続いて state lock 下で最新 row の key、emitter nonce、PaneRef、`state_refinement:true` を再照合し、その row の `reported_state` だけを `shouldNudge` へ渡す。
  Herdr snapshot の native public status は `reported_state` の代用にせず、照合成功後に lock を解放して `pane run` を一回発行する。
  operational miss と send failure は message bus の保存を維持した no-op success とする。
  check-and-send race は tmux の `ListLive` から non-transactional `send-keys` までの race と同種の受容済み残余リスクである。
  atomic conditional send または permission UI を操作しない out-of-band queue と、agent process から分離した event provenance が揃えば proof-grade tier へ格上げする。
  herdr の `notification show` は nudge に使わない。
- `codexPlanMode` は agent 種別にかかわらず恒久的に除外し、再評価しない。
  downstream 実装は Herdr launch で controller command を構築する前に明示的に拒否する。
- CLI-first の wait と cold restart の再開待ちは、3 秒以上の整数 `total_timeout`、2 秒間隔、既定 300 秒、各 CLI call 最大 5 秒、snapshot 最大 `ceil(total_timeout / 2 秒)` 回の上記共有 budget を使う。
  terminal result は `matched`、`timed_out`、`cancelled`、`failed` の四値とし、snapshot と event wait を直列に組み合わせない。
- generic workspace shell は `HERDR_ENV=1` から自動検出し、nested tmux では `--backend tmux` または `FANOUT_BACKEND=tmux` で明示的に上書きできるようにする。
- herdr backend の cleanup は `worktree remove`、`workspace close`、`pane close`、必要な削除用 `worktree open` を exact ownership の送信直前再照合後に実行する。
  dirty worktree の `--force` は明示的なユーザー確認を要求する。
  remove / close request に nonce または session epoch の precondition を渡せず TOCTOU は残るが、tmux-parity tier の受容済み残余リスクとする。
  成功応答と資源不在の再観測後だけ state row を整理し、response loss、mutation 不明、identity 不一致、setup hook のある削除用再登録は fail closed にする。
- wave 2 は初回と cold restart reconciliation 後に `report-metadata` を発行する。
  metadata は `state.json`、liveness、nudge authority、完了判定に使わない。
  token の欠落自体は state transition に使わず、cold restart 後の stale / resume は `terminal_id` と Codex v6 exact matcher で決める。
  初回報告と再送は exact target、固定 source、sequence、TTL を直前・直後に再照合し、不一致なら fail closed にする。
  0.7.3 の presentation fields と 0.7.4 の pane / workspace metadata token reporting は別の version provenance として扱う。
  #494 の `report-metadata` call は対象 `pane_id` または `workspace_id`、固定 `source`、空でない token patch、必要な `seq` / `ttl_ms` だけで構成し、title、display agent、state label を書き換えない。
  #494 は実装前に `fanout_issue`、`fanout_slug`、`fanout_parent`、`fanout_pr`、`fanout_ci` の pane / workspace 配置、固定 source、sequence の永続化、TTL、値欠落時の clear を一意に決める。
  `rows`、`rows_by_agent`、`row_gap` と styling は herdr とユーザーが所有し、fanout は config を書き換えない。
  request が authoritative server generation と target generation を原子的に束縛できる場合は proof-grade tier へ格上げする。
- in-app notification を配信保証のある channel として扱わず、fanout から自動呼び出しもしない。

## 参考

- [CLI reference](https://herdr.dev/docs/cli-reference/)
- [Socket API](https://herdr.dev/docs/socket-api/)
- [Agents](https://herdr.dev/docs/agents/)
- [Configuration](https://herdr.dev/docs/configuration/)
- [Config reference](https://herdr.dev/docs/config-reference/)
- [Session state](https://herdr.dev/docs/session-state/)
- 関連分析: [herdr 競合分析](competitive-herdr.ja.md)
- 親設計: [#423](https://github.com/butaosuinu/fanout/issues/423)
- 親設計の承認: [#424 spike 反映](https://github.com/butaosuinu/fanout/issues/423#issuecomment-4986704437)
- 検証 issue: [#424](https://github.com/butaosuinu/fanout/issues/424)
- wave 2 検証 issue: [#525](https://github.com/butaosuinu/fanout/issues/525)
