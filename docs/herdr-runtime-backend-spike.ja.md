# herdr runtime backend 実機検証

ステータス: v1 の実機検証と fail-closed 境界を確定。
herdr backend v1 は自動 mutation を持たない read-only / manual experiment とし、fanout workflow の runtime としては提供しない。
v0.7.4 metadata token reporting と sidebar row layout は追試済み。
v0.7.4 core runtime matrix は未実施で、core runtime allowlist は変更しない。
core runtime の検証日は 2026-07-16、v0.7.4 追試日は 2026-07-17。
core runtime の対象は stable CLI / server `0.7.3`、protocol `16`、schema version `1`。
metadata token reporting と sidebar row layout の対象は stable CLI / server `0.7.4`、protocol `16`、schema version `1`。

fanout の herdr backend v1 は CLI-first とし、集約読みには CLI wrapper の `herdr api snapshot` を使う。
raw Socket client は実装しない。
fanout が実行する v1 の herdr 操作は version / session / schema の検査と snapshot / list / wait に限定する。
targeted content read の `pane read` / `agent read` は手動実測面としてだけ残し、fanout v1 は発行しない。
issue / Project / plan の launch は root coordinator の provisional intent、state row、`workspace create` を含む最初の mutation より前に fail closed にする。
worktree の作成と既存 checkout の採用は herdr CLI で実行できるが、fanout の自動 launch には採用しない。
herdr 0.7.3 は setup hook の抑止または registry generation を create / open request に束縛できず、plugin の実行完了 receipt も持たないため、自動 create / open とその直後の agent start を v1 では無効にする。
条件付き remove / close もないため、自動 cleanup、create rollback、それに依存する git fallback も無効にする。
Claude hook の signal は agent process から偽造できるため telemetry に限定し、nudge authority または完了判定には使わない。
herdr backend v1 の自動 nudge は agent 種別にかかわらず無効にし、pane 消滅または `terminal_id` の変化は `stale` とする。
session identity の read と mutation は別の CLI 接続になるため、直前の version / identity 検査だけでは mutation の対象を束縛できない。
自動 mutation の再導入には、request が expected immutable session / resource generation を原子的に検査するか、fanout が認証済み session と対象資源の lifecycle を排他的に所有する必要がある。
同じ TOCTOU は targeted content read にも残り、別接続の post-read snapshot は ABA を排除できないため content の公開 authority には使わない。

## 採用判断

| 対象 | v1 の判断 | 理由 |
|---|---|---|
| server 起動 | read-only experiment は既存の named session を要求する | headless CLI は server を自動起動しなかった |
| 集約読み | `herdr api snapshot` を使う | workspace、tab、pane、layout、agent、focus を 1 回で取得できる |
| content read | fanout v1 では無効 | response が immutable session generation と target `terminal_id` を束縛しない |
| raw Socket API | 不採用 | v1 で必要な操作は CLI wrapper で足りる |
| worktree | `worktree create/open` は手動操作としてだけ使う | setup hook gate を mutation に束縛できず、自動 remove も安全に実行できない |
| agent 起動 | `agent start` の bare argv、`--cwd`、`--env` は手動操作としてだけ使う | worktree setup 完了を API で証明できず、自動 launch には採用しない |
| nudge | v1 では無効 | hook signal は authority ではなく、状態検査と submit を原子的に実行する CAS もない |
| identity | routing、checkout、terminal、会話、process を別々に照合する | v1 は cold restart 後に再束縛せず、provider 固有 matcher は後続版に限る |
| cleanup | 自動 mutation は無効 | remove / close の request に nonce または session epoch の precondition を渡せない |
| 通知 | 手動検証だけに使う | detached 時も `shown:true` で、表示完了の応答ではなく、fanout v1 は発行しない |

## 検証条件

検証用の git repository、bare remote、linked worktree、named herdr session を `/private/tmp` に作った。
ユーザーの default herdr session は停止も削除もしていない。
plugin event の検証では `XDG_CONFIG_HOME` と `XDG_STATE_HOME` も `/private/tmp` へ向け、plugin registry と state を隔離した。

追加検証では公式 `v0.7.3` macOS arm64 リリースバイナリ（SHA-256 `b31345392d004ec1f1b2c821e1ad601019fa8385fe1e4c6931321eb58a920773`）を `/private/tmp` に置き、named session と state を隔離した。
herdr 公式 Codex integration v6 の再開試験だけは、すでに信頼済みのこの worktree を cwd に使った。

sidebar 追試では公式 `v0.7.4` macOS arm64 リリースバイナリ（SHA-256 `24992e1625dbdcb18354a59e299e4b263c312400b31396cdc07cd46ed57f24a7`）を `/private/tmp` に置き、named session、config、state を隔離した。
インストール済みの `herdr 0.7.4` はこのリリースバイナリと byte 単位で一致した。
隔離 session の `status --json` は client / server version `0.7.4` と protocol `16`、`api snapshot` は version `0.7.4` と protocol `16`、`api schema --json` は schema version `1` を返した。

fanout の複数行入力はインストール済みの `fanout v0.12.0` を実際の herdr pane で起動して確認した。
モックは使っていない。

## 起動と session 境界

server 起動前の `HERDR_SESSION=fanout-spike-424 herdr status --json` は `server.status:"not_running"` を返した。
同じ状態で `herdr workspace list` を実行すると Unix socket が存在せず失敗した。
通常の headless CLI は server を自動起動しない。

v1 は次の前提で実行する。

```console
HERDR_SESSION=<name> herdr status --json
HERDR_SESSION=<name> herdr server
```

fanout が server を暗黙に作成または attach する処理は v1 に入れない。
`status --json` の `server.session` と socket path で対象 namespace は確認できるが、応答には session UUID、generation、state epoch がない。

client の detach と reattach では server と agent process が動き続け、workspace、tab、pane の public ID、cwd、agent record と status に detach 起因の変化はなかった。
status に現れる session identity は前後とも session 名と socket path だけだった。
server の cold restart でも public ID は維持されたが、全 pane の `terminal_id` は変わった。

同名 session を削除して作り直すと、最初の workspace、tab、pane は再び `w1`、`w1:t1`、`w1:p1` になった。
削除前の `w1:p1` は `terminal_id:"term_656aa954d42c11"`、fresh state の `w1:p1` は `terminal_id:"term_656aae4d0cc691"` だった。
session 名と public ID だけを state key にすると stale mapping が別 process へ一致する。

herdr から比較可能な session epoch は取得できない。
v1 は session 名を namespace として保存し、各 PaneRef に `terminal_id` と `agent_session` を保存する。

herdr 0.7.3 は明示 `--session` が無い場合、継承した `HERDR_SOCKET_PATH` を `HERDR_SESSION` より優先する(Pass 2 レビュー時の 0.7.3 実機確認)。
herdr pane 内で実行する fanout は常にこの変数を持つため、session 名だけの routing は custom socket や別 session の server に接続し得る。
public ID は session 間で再利用されるため、誤 routing の `send` や `close` は無関係な pane に届く。
v1 の read-only CLI は検証済みの socket path を session namespace と併せて保存し、各呼び出しで明示的に選択する。
session identity の再確認は routing と read validation にだけ使い、同名 session の差し替えと次の CLI 接続の間の TOCTOU を閉じないため mutation authorization には使わない。
targeted content read の前後で snapshot を再確認しても、read 中だけ別 session へ差し替わって戻る ABA を検出できない。
v1 は `pane read` / `agent read` の出力を公開せず、post-read validation 単独を content の authority にしない。

`terminal_id` は server が所有する terminal 実体の識別子であり、論理上の会話または agent process の識別子ではない。
同じ `terminal_id` でも、想定した agent process の生存は別に確認する。
`terminal_id` が変わった場合、0.7.3 v1 は current row を `stale` とし、新しい terminal へ再対応付けしない。
自動 launch を再導入する後続版では、保存済みの `{source, agent, kind, value}` と完全一致する一意な `agent_session` があれば、論理上の会話を新しい terminal へ再対応付けする候補にできる。
`agent_session` ref が欠落、不一致、重複した場合は再対応付けせず fail closed にする。
この ref は attach 前の再開待ちにも現れるため、process の生存を示す証拠には使わず、running への写像には provider 固有の process identity 検査も要求する。
fanout-owned epoch は fanout が session lifecycle も所有する後続版でなければ検証に使えない。

## 操作面

次の表は実測した CLI surface を示す。
fanout v1 が呼ぶのは list / wait の read-only 行だけで、targeted read と mutation の行はユーザーが fanout の外から直接実行する手動操作である。

| 実測操作 | CLI | 結果 | 制約 |
|---|---|---|---|
| launch | `workspace create --cwd ... --no-focus` | `workspace_created` | 実測済みの手動操作。fanout の root coordinator 作成には使わない |
| launch | `worktree create --workspace ... --branch ... --base ... --path ... --label <nonce> --no-focus --json` | `worktree_created` | 実測済みの手動操作。fanout の自動 launch には使わない |
| launch / recover | `worktree open --workspace ... --path ... --label <nonce> --no-focus --json` | `worktree_opened` | 実測済みの手動操作。fanout の自動 launch には使わない |
| launch | `agent start <name> --workspace ... --cwd ... --env ... --no-focus -- <argv...>` | `agent_started` | 実測済みの手動操作。fanout の自動 launch には使わず、`--json` flag はない |
| list | `api snapshot` | `session_snapshot` | `session.snapshot` の CLI wrapper。v1 の identity / status 観測に使う |
| list | `worktree list --workspace ... --json`、`agent list` | `worktree_list`、`agent_list` | `worktree list` は基準 workspace を明示する |
| content read | `pane read`、`agent read` | text または `pane_read` | 手動実測のみ。fanout v1 は発行せず、結果を UI、ログ、state、agent / LLM input へ公開しない |
| structured read | `pane get` | `pane_info` | 手動実測または後続版の identity 検査に限り、fanout v1 は発行しない |
| structured read | `pane process-info --pane ...` | `pane_process_info` | 手動実測または後続版の process identity 検査に限り、fanout v1 は発行しない |
| focus | `agent focus <name>`、`workspace focus <id>` | 対象 agent または workspace を focus | 実測済みの手動操作。fanout v1 は発行しない |
| send | `pane run <pane> <text>` | text と Enter を一操作で送る | 明示的な手動操作に限り、自動 nudge には使わない |
| close | `worktree remove --workspace ... [--force] --json` | `worktree_removed` | 実測済みの手動操作。fanout の自動 cleanup には使わない |
| close | `workspace close`、`pane close` | `ok` | 実測済みの手動操作。checkout は `workspace close` では消えない |
| wait | `api snapshot` の 2 秒間隔 polling | `session_snapshot` | `total_timeout` は整数 3 秒以上。既定 300 秒では最大 150 snapshot、各 CLI call 最大 5 秒で current-state predicate を評価する |

generic pane の exact focus が必要になった場合、Socket API の `pane.focus` を手動操作の追加候補にする。

`worktree remove`、`workspace close`、`pane close` は snapshot 照合と mutation が別 CLI 接続になり、照合済み nonce、`terminal_id`、session epoch を request の precondition として渡す手段が 0.7.3 にない。
同名 session の再作成で session 名、socket、public ID は再利用されるため、照合と mutation の間の TOCTOU は CLI では閉じられない。
v1 は rollback を含めてこれらを fanout から自動実行せず、close はユーザーが対象を確認して fanout の外から直接行う手動操作、または条件付き mutation を提供する将来の Socket 経路に限る。

0.7.3 v1 は root coordinator の intent / state row を保存せず、`workspace create` も実行しない。
version / session identity の precheck、provisional intent、nonce label は response loss と重複作成の検出には使えるが、precheck 後に同名 session が置き換わる TOCTOU を閉じない。

自動 mutation の安全条件を満たす後続版では、root coordinator の `workspace create` も副作用を持つ launch 操作として provisional intent の対象にする。
create request が expected immutable session generation を原子的に検査するか、fanout が認証済み session lifecycle を排他的に所有することを前提に、owner row key、backend / session identity、root cwd、intent 固有の coordinator nonce を intent へ保存する。
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

0.7.3 v1 は root coordinator の intent / row / workspace を作る前に fail closed にし、次の child launch state machine へ入らない。
以下は expected immutable session / resource generation と hook gate を各 mutation に束縛できるか、fanout が認証済み session と対象資源の lifecycle を排他的に所有する後続 API で、自動 launch を再導入する場合の必須契約である。

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
  0.7.3 の plugin registry read は create / open request に registry generation を渡せず、read 後の plugin 変更を拒否できないため、setup hook 不在の証明には使わない。
  後続 API は create / open request で setup hook を原子的に抑止するか、照合済み registry generation を mutation の precondition にする必要がある。
  Git HEAD、index、tracked / untracked set の連続一致や一定時間の静止は、hook がまだ開始していない window と区別できないため completion proof に使わない。
  setup plugin を許可する後続版は、event type、workspace ID、checkout path、worktree ownership nonce、terminal `succeeded`、`exit_code:0` を一つの mutation に束縛した operation-scoped completion receipt を検証する。
  receipt 後の baseline 再照合は race 検出に使うが、completion proof にはしない。
  phase `worktree-realized` からは、原子的な hook 抑止または operation-scoped completion receipt を検証し、その後の checkout baseline を保存した場合だけ `worktree-ready` へ進める。
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
  `worktree remove` による自動 rollback と、rollback 後の git fallback は v1 では実行しない。
  新規 branch は herdr に作らせず、fanout が `worktree create` の前に atomic な ref 予約(old OID を空とする `update-ref` 相当。既存なら失敗)で base SHA に作成し、予約成功を intent へ記録する。
  preflight と create の間に別 process が同名 branch を作った場合は予約が失敗し、既存 branch 採用の実測挙動(113-115 行)による他者 branch の巻き込みを防ぐ。
  branch reservation は worktree mutation が起きていないことを証明できる場合だけ、予約時 OID を old OID とする compare-and-delete で解放する。
  worktree mutation の可能性がある場合、branch ref が変わっている場合、または予約の所有を証明できない場合は branch も残して fail closed にする。

### workspace 配置

`worktree create --workspace w1` は w1 内の pane を作らず、独立した child workspace w2 を作った。
親と子は同じ `repo_key` と worktree provenance で repository group に並ぶ。
`session.snapshot` には child から parent workspace を指す ID がない。

自動 launch を再導入する後続版の実現形は「親 workspace の内部に child pane」ではない。
project root の coordinator workspace を一つ作り、各 child worktree を sibling workspace として開く。
0.7.3 v1 はこの coordinator を作らない。
後続版では fanout の state が parent issue と child workspace の対応を保持する。
pane split は同じ checkout 内に補助 process を追加する場合だけ使う。

### cleanup

dirty な child worktree を force なしで削除すると、`dirty_worktree_requires_force` で拒否された。
`--force` では checkout と workspace が消え、branch は残った。
clean な focused child を削除すると、focus は repository root workspace へ戻った。

state row 起点の cleanup でも、0.7.3 CLI は nonce、`terminal_id`、session epoch を `worktree remove` / `workspace close` / `pane close` の precondition として受け取らない。
fanout が state、snapshot、checkout git dir を照合してから別接続で mutation を発行するまでに、同名 session と public ID が別資源へ再利用され得る。
この TOCTOU は `--force` の有無にかかわらず残るため、v1 の `--cleanup` / `--close` は herdr 資源を自動変更しない。
fanout は保存済み backend / session、workspace ID、label、repo、branch、path、provenance、作成時 nonce と現在値を read-only で表示し、ユーザーが対象を確認するための情報としてだけ使う。
同名 session の差し替えを検出できないため、ユーザーの手動 cleanup 後に workspace と checkout の不在を再観測しても v1 は state row を自動整理しない。
明示的なユーザー確認がなければ row を残して fail closed にする。

`workspace close` を先に実行すると checkout は残る。
続く `worktree remove --workspace <closed-id>` は `workspace_not_found` になる。
`worktree open` で checkout を削除用 workspace へ再登録する経路も v1 では自動実行しない。
0.7.3 の `worktree open` は `worktree.opened` setup hook を発火し、hook の完了を operation-scoped receipt で待つ手段がないため、cleanup 中の再登録と直後の remove を安全に直列化できない。
削除用再登録と `--force` は、ユーザーが checkout と hook の状態を確認して fanout の外から直接実行する手動操作に限る。

### plugin event

隔離した local plugin を `plugin link` し、`worktree.created` と `worktree.removed` の hook を登録した。
CLI 経由の create と remove で両 hook が各一回実行され、plugin log は `status:"succeeded"`、`exit_code:0` を返した。

作成 event は `workspace` と `worktree`、削除 event は `workspace_id`、最終 `workspace`、`worktree`、`forced` を含んだ。
fanout が worktree 実体化を herdr へ委譲したときに plugin event が発火することは確認したが、setup hook の完了を待って agent を自動起動できることは確認していない。
追加検証では公式 herdr 0.7.3 の `worktree open` が `worktree.opened` hook を一回だけ実行し、plugin log は `status:"succeeded"`、`exit_code:0` を返した。
payload は workspace、active tab、checkout path、branch、`already_open:false` を含んだ。
公式 Socket API と hook 実測の両方で、`worktree.opened` は open 経路、`worktree.created` は create 経路の event だった。
`plugin log list` の terminal record と mutation 応答を一意に対応付ける receipt はこの spike で確認していないため、v1 は plugin log を completion fence に使わない。

## pane と agent の lifecycle

### bare argv と env

以下は手動操作で得た実測と、自動 launch を再導入する後続版の契約である。
0.7.3 v1 の fanout は `agent start` を自動実行しない。

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

自動 launch を再導入する後続版では、fanout 固有の値と呼び出し元で解決した `PATH` を `--env KEY=VALUE` で渡し、herdr の実行環境識別子は herdr の env と snapshot から取得する。
agent executable は fanout の起動環境で解決した絶対パスを bare argv の先頭に置く。
#427 が注入する lifecycle hook も、同じ時点で解決した fanout executable の絶対パスを呼ぶ。
絶対パスだけでは、agent が起動後に実行する `git` や `gh` が server の ambient PATH で解決されるため、server を minimal env で起動した環境では作業が失敗する(Pass 2 レビュー時の 0.7.3 実機確認)。
tmux backend の `BuildResolvedCommand` と同じく、呼び出し元 `PATH` を明示 `--env` で引き継ぎ、agent と hook の実行を herdr server の ambient PATH に依存させない。

`agent.start` の agent 名は workspace 内ではなく session 全体で一意が要求され、重複は `agent_name_taken` で失敗する(Pass 2 レビュー時の 0.7.3 実機確認)。
複数の repo や親が同じ session を共有しても衝突しないよう、後続版の launch 名は repo、親参照、intent に保存した agent launch nonce の hash から `core/naming` で生成する。

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
自動 launch を再導入する後続版は bare argv を採用し、tmux backend の「agent 終了後も shell を残す」契約を herdr backend には持ち込まない。
#427 も自動 launch の前提が満たされた後に `agent start` の Claude argv へ `--settings` lifecycle hook を注入し、fanout CLI を呼ぶ runtime 非依存の telemetry emitter として agent の報告状態を `state.json` へ記録する。
tmux pane option は使わない。
launch 時に owner の絶対 `FANOUT_STATE_PATH`、state row key、launch ごとの opaque emitter nonce、backend、session / workspace / agent identity を hook 環境へ注入する。
row key は `TaskID` が非空なら `(parent, taskId)`、それ以外は `(parent, issueNum)` とする。
emitter nonce は state row にも保存し、再 launch ごとに更新する。
これらの値は agent が起動する tool と checkout 内 script に継承されるため、secret、capability、event provenance の証明にはならない。
agent process は正規 hook と同じ emitter call を偽造できる。
emitter signal は `reported_state` telemetry にだけ保存し、authoritative lifecycle state、nudge、完了判定、cleanup の根拠には使わない。
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
この placeholder は process の生存証拠に使わず、自動 nudge は別途 v1 全体で無効にする。

attach 後の pane には restart 前の prompt と `PROBE_OK` の応答履歴が復元された。
`pane process-info --pane w4:p2` は foreground process の argv が `codex resume 019f6908-3bc1-7c83-98df-d8ea91694d2c` であることも返した。
この実測から、`terminal_id` の変化だけでは論理上の会話の喪失を判定できない。
一方、attach 前の `agent_session` ref と `idle` だけでは process の生存を判定できない。

0.7.3 v1 は cold restart 後に process identity を再束縛せず、`terminal_id` が変わった row を `stale` とする。
自動 launch を再導入する後続版が running へ再束縛できる provider は、今回実測した herdr 公式 Codex integration v6 だけとする。
保存済み `agent_session` は `{source:"herdr:codex", agent:"codex", kind:"id", value:<session-id>}` と完全一致し、現在の pane に同じ ref が一つだけ存在しなければならない。
attach 後の `pane process-info` は foreground process の候補を一つだけ返し、OS process 情報から解決した実 executable が final row に保存した Codex executable の絶対パスと一致しなければならない。
候補の argv は argv0 を除く引数列が `["resume", "<session-id>"]` と完全一致し、追加引数を許可しない。
`<session-id>` は保存済み `agent_session.value` と byte-for-byte で一致させる。
候補の `process-info.foreground_processes[].cwd` は final row に保存した `agent start --cwd` と完全一致させ、`pane get.cwd` または snapshot の `foreground_cwd` で代用しない。
`pane process-info` は PPID chain を返さないため、候補 PID が現在の `shell_pid` 自身またはその子孫であり、現在の `foreground_process_group_id` と同じ process group に属することを OS process 情報で別途確認する。
OS ancestry または process group を取得できない場合は再束縛しない。
すべてを同じ再対応付け cycle で確認した後、新しい `terminal_id`、PID、executable、argv、process cwd、`shell_pid`、foreground process group ID、`agent_session` を state lock 下の一回の save で process identity として束縛する。
自動再束縛を導入する後続版では、attach 前の exact placeholder だけを再開待ちへ入れ、結果が `matched` の場合だけ process identity を束縛する。
直近の compatible snapshot でも exact placeholder が続いたまま `timed_out` した場合だけ row を `stale` とし、reason `resume_timeout` を記録する。
`cancelled` と `failed` は `stale` に読み替えず、row を更新しない。
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
v1 は `pane read` / `agent read` を発行せず、取得結果を UI、ログ、state、agent / LLM input へ公開しない。
別接続の post-read snapshot が一致しても、read 中だけ session が差し替わる ABA を排除できないため authority にはしない。
targeted content read の再導入は、request / response が immutable session generation と target terminal identity を原子的に束縛するか、fanout が認証済み session と対象資源の lifecycle を排他的に所有する後続版に限る。

手動実測した `pane get` の `cwd` は label、follow-cwd、session restore に使われる pane または workspace の cwd を表す。
`foreground_cwd` は現在 PTY を制御する foreground process の cwd を表す。
実際、foreground で `(cd /; sleep 15)` を実行している間も `cwd` は元 repository のままで、`foreground_cwd` は `/` になった。
`process-info.foreground_processes[].cwd` は候補 PID に結び付いた process cwd であり、cold-restart matcher はこの値だけを使う。
`pane get.cwd` と snapshot の `foreground_cwd` は process cwd の照合を代替しない。

`foreground_cwd` は表示と診断の telemetry とし、PaneRef の識別または生存判定には使わない。
targeted structured read を再導入する後続版では、PaneRef の routing を backend、session namespace、workspace ID、pane ID で行う。
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

herdr backend v1 の自動 nudge は Claude と Codex の両方で無効にする。
public `idle` / `done`、hook telemetry、`agent explain --json`、screen manifest のどれも送信許可には使わない。
hook telemetry は agent process から偽造でき、screen detection は未知の permission UI を除外できない。
`terminal_id`、`agent_session`、worktree provenance を送信直前に再照合しても、その後の `pane run` までに pane の状態は変わり得る。
herdr 0.7.3 には状態条件付き send または CAS がないため、この race を fail closed にできない。
fanout は peer message または watcher を契機に `pane run` を自動実行せず、Enter を送らない。
peer message の bus への保存は維持するが、fanout v1 は herdr の `notification show` を呼ばない。
ユーザーが対象 pane を確認して明示的に実行する `pane run` は手動操作として扱う。
自動 nudge の再導入には、runtime の atomic conditional send または terminal permission UI を操作しない out-of-band queue と、agent process から分離した event provenance が必要になる。

### focus と wait

`agent focus <name>` と `workspace focus <id>` は対象を正確に focus した。
`--no-focus` は二つ目以降の worktree と agent 起動で focus を維持した。

`wait output` は出力遷移を待ち、`output_matched` と matched line、read result、revision を返した。
`agent wait --status idle` は current state がすでに idle でも即時成功せず、後続 event を待った。
focus されていない agent が `idle` を報告した場合は `done` event が返り、`agent focus` 後に `idle` へ変わった。

snapshot 後に event wait を登録すると、二つの操作の間に起きた遷移を逃す lost-wakeup race がある。
CLI-first の v1 は次の共有 budget を持つ snapshot polling を使い、CLI の一回待機を current-state predicate と組み合わせない。

- 一回の wait または後続版の cold-restart reconciliation は、最初の `herdr api snapshot` の直前に monotonic clock で一つの deadline を確定する。
- `total_timeout` は 3 秒以上の整数秒で受け取り、既定値を 300 秒として、無期限待機を許可しない。
- 最初の snapshot は直ちに呼び、次の呼び出しは前回の開始から 2 秒以上空け、遅れた tick を追い掛けず、複数の CLI process を同時実行しない。
- 各 herdr CLI process の timeout は `min(5 秒, deadline までの残時間)` とする。
- snapshot の最大呼出し回数は開始時に `ceil(total_timeout / 2 秒)` へ固定し、既定値は 150 回とする。
  最小値の 3 秒では初回と 2 秒時点の再取得の最大 2 回を許す。
- v1 の一つの polling cycle では snapshot を一回だけ呼び、snapshot の current-state predicate だけを評価する。
- 後続版の cold-restart reconciliation cycle に限り、一意な resume 候補を得た場合に `pane process-info` と各 OS process identity 検査を一回ずつ実行する。
- snapshot、補助検査、parse、interval sleep、retry は同じ deadline を消費し、valid snapshot、状態変化、placeholder、retryable error を観測しても deadline と呼出し上限を更新しない。
- version / schema の不一致と malformed snapshot は retry せず、直ちに `failed` とする。
- CLI timeout または non-zero exit は同じ budget 内でだけ retry し、budget 終了時の直近 cycle が失敗していた場合、または compatible snapshot を一度も取得できなかった場合は `failed` とする。
- terminal result は `matched`、`timed_out`、`cancelled`、`failed` の四値に固定する。
- v1 wait は compatible snapshot が predicate を満たした場合に `matched` とする。
  後続版の cold-restart reconciliation は compatible snapshot と必要な補助検査が predicate を満たした場合に `matched` とする。
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
将来の reporter は token 値だけを提供し、row と styling は herdr とユーザーが所有する。

pane / workspace token は `api snapshot` に返ったが、cold restart 後の snapshot から消えた。
v1 は初回を含めて `report-metadata` を発行せず、cold restart 後も再送しない。
metadata が表示専用であることは、同名 session の差し替え後に無関係な pane / workspace へ issue、PR、CI token を書く race を安全にはしない。
初回報告と再送は、request が expected immutable session generation と target `terminal_id` / workspace generation を原子的に検査するか、fanout が認証済み session と対象資源の lifecycle を排他的に所有する後続版まで無効にする。
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
v1 の fanout notify backend は herdr の `notification show` を呼ばない。
同名 session の差し替え時に別 session へ内容を送る TOCTOU を閉じられないため、設定済み利用者が fanout の外から使う手動実測面としてだけ残す。

## Socket schema と JSON 契約

`herdr api schema --json` は protocol `16`、schema version `1` を返した。
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

raw Socket の `events.subscribe` は常駐 client を増やすため v1 では使わない。
`agent.send`、`pane.send_input`、`pane.focus` も schema にはあるが、手動検証には CLI の `pane run`、`agent focus`、`workspace focus` で足りる。

## version と JSON 対応

stable public workspace、tab、pane ID の契約は 0.7.0、既存 local branch の worktree create/open は 0.7.1、`session.snapshot` と `api schema --json` は 0.7.2 で入った。
herdr backend v1 の core runtime compatibility allowlist は、stable CLI / server `0.7.3`、protocol `16`、schema version `1` の組だけとする。
v1 は herdr mutation を発行しない。
後続版で mutation を再導入する場合は、各 mutation の直前に `herdr --version`、`status --json`、`api schema --json` を照合し、prerelease、解釈不能な version、または allowlist にない CLI / server version、protocol、schema を fail closed にする。
この precheck は compatibility 検査であり、mutation authority にはしない。
各 request には expected immutable session / resource generation の原子的な precondition、または fanout が認証済み session と対象資源の lifecycle を排他的に所有する条件も要求する。
semver の `>=` から互換性を推定しない。
後続 version は同じ実機 matrix を通し、exact version と protocol / schema の組を明示的に allowlist へ追加した場合だけ受理する。
0.7.4 の exact tuple `(CLI/server 0.7.4, protocol 16, schema version 1)` は metadata token reporting と sidebar row layout だけで実測済みとする。
この追試は core runtime matrix の代わりにならず、tuple を core runtime allowlist へ追加しない。
#494 は 0.7.4 の core runtime matrix、exact tuple の allowlist entry、#426 が operation-scoped safety 条件を満たす identity-bound resource を作れることだけでは有効にしない。
`report-metadata` request が expected immutable session generation と target `terminal_id` / workspace generation を原子的に検査するか、fanout が認証済み session と対象資源の lifecycle を排他的に所有するまで、初回報告と再送の両方を無効にする。
同じ protocol / schema でも 0.7.3 は `--token` を拒否したため、core runtime の上位互換を推定しない。

| 実測コマンド | 導入 / 実測 version（runtime acceptance range ではない） | JSON 対応 |
|---|---:|---|
| `herdr --version` | 0.7.3 core baseline、0.7.4 metadata token 追試 | text のみ |
| `status --json` | 0.7.3 core baseline、0.7.4 metadata token 追試 | 明示 `--json` |
| `session list --json` | 0.7.3 core baseline | 明示 `--json` |
| `workspace create/list/focus/close` | 0.7.3 baseline | JSON envelope を標準出力へ返す。`--json` は付けない |
| `worktree create/open/list/remove` | 0.7.1 | 明示 `--json` |
| `agent start/list/read/focus` | 0.7.3 baseline | JSON envelope を標準出力へ返す。`--json` は付けない |
| `pane get/run/close` | 0.7.3 baseline | mutation と get は JSON envelope。`--json` は付けない |
| `pane process-info` | 0.7.3 baseline | JSON envelope を標準出力へ返す。対象は `--pane` で指定する |
| `pane read` | 0.7.3 baseline | text または ANSI を直接出力する |
| `pane report-metadata` の presentation fields / seq / TTL | 0.7.3 | 成功時は出力なし（`--json` 非対応） |
| `pane report-metadata` の token patch | 0.7.4 | 成功時は出力なし（`--json` 非対応） |
| `workspace report-metadata` の token / seq / TTL | 0.7.4 | 成功時は出力なし（`--json` 非対応） |
| `api snapshot` | 0.7.2、token projection は 0.7.4 | JSON envelope を標準出力へ返す。`--json` は付けない |
| `api schema --json` | 0.7.2、metadata token schema は 0.7.4 | 明示 `--json` |
| `notification show` | 0.7.3 baseline | JSON envelope を標準出力へ返す。`--json` は付けない |
| `plugin link/list/log list` | 0.7.3 baseline | `list` は `--json` 対応。他は JSON envelope |

この表は機能導入時期と 0.7.3 core / 0.7.4 metadata token の実測 provenance を示すだけで、core runtime の上位 version 互換性を認めない。

version ごとの根拠は [v0.7.0](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.0)、[v0.7.1](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.1)、[v0.7.2](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.2)、[v0.7.3](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.3)、[v0.7.4](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.4) を参照する。

## 後続 issue への契約

#423、#425 から #429、#494 は次の制約を前提にする。

- backend は明示的に起動済みの named herdr session を使う。
- herdr backend v1 は既存 named session の snapshot / list / wait による identity / status 観測だけを行い、targeted content read を発行しない。
  root coordinator 作成、focus、send、notification、metadata、cleanup を含む herdr mutation も発行しない。
  issue / Project / plan の launch は coordinator intent / row / workspace を作る前に fail closed にする。
- core runtime compatibility allowlist は stable CLI / server `0.7.3`、protocol `16`、schema version `1` の組だけとする。
  後続版で mutation を再導入する場合は各 request の直前に client / server version、protocol、schema を照合し、prerelease、解釈不能な値、allowlist 外の組は fail closed にする。
  この precheck は mutation authority ではなく、request-bound immutable session / resource generation または fanout-owned authenticated lifecycle も要求する。
  上位 version は semver から互換性を推定せず、同じ実機 matrix と exact tuple の allowlist entry を追加した後にだけ受理する。
- backend 選択の resolver は final state rows と provisional intents の両方を入力にする。
  legacy row の空 backend は tmux に正規化する。
  実際の issue / Project / plan の親では、既存 rows / intents が一つの backend に一致する場合だけその backend を再利用し、mixed state または `--backend` / env との不一致は fail closed にする(明示的な移行はユーザー操作)。
  stickiness の単位は実際の issue / Project / plan の親に限る。
  自動 launch の安全条件を満たす後続版では、親 issue の orchestrator pane を `@manual` の負番号 row として保存するが、issue / plan の provenance を実親へ帰属させて同じ stickiness 判定に含める — coordinator 作成後に child launch が失敗した再実行が coordinator の backend を見落とさないようにする。
  それ以外の `@manual` synthetic launch は互いに独立した launch の集まりであり、row identity とその intent の単位で backend を固定する。
- 自動 launch の安全条件を満たす後続版では、worktree を root coordinator と sibling child workspace で配置する。
- 0.7.3 v1 は `worktree create`、`worktree open`、続く `agent start` を fanout から自動実行しない。
  plugin registry read を create / open mutation に束縛できず、setup hook の不在または完了を fail closed に証明できないためである。
  これらの CLI は手動実測面として残し、自動 launch は条件付き mutation または operation-scoped completion receipt を持つ後続 API まで fail closed にする。
- 自動 launch を再導入する後続版では、fanout が worktree safety gate と idempotency を所有し、herdr は checkout と workspace の実体化を担当する。
  create / open の mutation 前に provisional intent を保存し、同じ worktree ownership nonce を workspace label と checkout git dir marker の両方で照合する。
  各 worktree mutation の直前に phase `worktree-starting`、exact request、per-step pre-state を保存し、starting の再実行では対象資源がなくても request を再発行しない。
  response loss は phase と事後条件から `worktree-realized` まで回復し、mutation の有無を証明できない場合は intent を残して fail closed にする。
  `worktree open` の `already_open:true` は pre-state で同じ workspace ID / label が task に束縛済みの場合だけ受理する。
  plugin registry の standalone read は setup hook 不在の証明に使わず、request による原子的な hook 抑止、registry generation の precondition、または mutation と terminal hook result を束縛する operation-scoped completion receipt を要求する。
  `worktree-realized` からはこの証明と checkout baseline の保存後にだけ `worktree-ready` へ進み、Git 状態の連続一致や静止時間を completion proof に使わない。
  `worktree create` request の発行後に事後条件違反、応答喪失、または mutation の不明が生じた場合は intent、資源、branch reservation を残し、`manual_cleanup_required` として fail closed にする。
  0.7.3 の unconditioned remove は rollback にも使わず、rollback に依存する git fallback も実行しない。
  branch reservation は worktree mutation が起きていないことを証明できる場合だけ compare-and-delete で解放する。
- 自動 launch を再導入する後続版では、agent は bare argv、明示 `--cwd`、fanout 固有値と呼び出し元 `PATH` を渡す明示 `--env` で起動する。
  launch 名は repo と親参照に agent launch nonce の hash を加え、同じ intent では安定し session 全体で一意になる決定論的名前を `core/naming` で生成する。
  `agent start` 前に agent launch nonce、emitter nonce と telemetry routing binding、agent name、provider、絶対 executable、argv / env fingerprint、argv、exact `--cwd` を intent の phase `agent-starting` として保存し、provider が返す exact session ref も取得後に保存する。
  `agent-starting` で応答を保存できなかった場合は launch 時の `terminal_id` を証明できないため、一意な同名 agent が見つかっても自動採用せず fail closed にする。
  `agent-started` の回復は保存済み `terminal_id` が現在値と一致する場合だけ元の argv / process cwd を照合し、変わった場合は provider 固有の cold-restart matcher へ分岐する。
  final row の確定では provider、絶対 executable、元 argv、exact `--cwd`、exact session ref を intent から移し、intent 削除と同じ state save で永続化する。
  保存済み応答後に pane が消滅した場合は returned PaneRef を束縛した `stale` row を確定し、pending `done` は telemetry としてだけ保存する。
  応答未保存の agent 欠落、重複、識別不一致は fail closed にする。
- snapshot / list / wait の read-only CLI は保存済みの検証済み socket path を明示的に選択する(`HERDR_SOCKET_PATH` が `HERDR_SESSION` より優先されるため)。
  session identity の再確認は routing / identity / status validation に限り、targeted content の公開または後続 mutation の authority にはしない。
  v1 は `pane read` / `agent read` を発行せず、post-read validation が一致しても content を公開しない。
  targeted content read の再導入には request / response が immutable session generation と target terminal identity を原子的に束縛するか、fanout-owned authenticated lifecycle を要求する。
  `pane get` / `pane process-info` は手動実測面またはこの条件を満たす後続版の structured identity 検査に限る。
  後続版の mutation は request-bound immutable session / resource generation または fanout-owned authenticated lifecycle を要求する。
  agent executable と注入 hook が呼ぶ fanout executable は、fanout の起動環境で解決した絶対パスを使う。
- 自動 launch の前提を満たす後続版で、#427 は `agent start` の Claude argv に `--settings` lifecycle hook を注入し、fanout CLI 経由で `state.json` の `reported_state` を更新する runtime 非依存の telemetry emitter を追加する。
  tmux pane option は使わない。
  hook は child checkout を cwd として実行されるため、owner state を確実に更新できるよう、launch 時に owner の絶対 `FANOUT_STATE_PATH`、state row key、launch ごとの opaque emitter nonce、backend、session / workspace / agent identity を hook 環境へ注入する。
  注入値は tool と checkout 内 script に継承されるため secret / capability / provenance ではなく、agent process は signal を偽造できる。
  signal は表示と診断用 telemetry に限定し、authoritative lifecycle state、nudge、完了判定、cleanup に使わない。
  row key は `TaskID` が非空なら `(parent, taskId)`、それ以外は `(parent, issueNum)` とし、manual / watch 等の synthetic launch も後者で扱う。
  launch lock は final row、intent 削除、または fail-closed 状態の保存まで保持し、同期 hook は lock 待ちの間も pane を生存させ、launcher は hook 完了を待たずに `agent start` 応答を処理する。
  emitter は同じ lock の取得後に final row なら `reported_state` update、matching intent だけなら pending 保存へ分岐する。
  `agent start` 応答前の signal は authoritative state を更新せず、provisional intent と完全一致する場合だけ pending telemetry とし、返された PaneRef を nonce へ束縛する final state save で `reported_state` へ反映する。
  応答を回復できない intent の pending telemetry は final row へ移さない。
  emitter は state lock 下で key、nonce、backend が完全一致する行が一つだけで、保存済み PaneRef が launch 時の binding と現在の runtime にも一致する場合だけ `reported_state` を更新し、cwd や slug から再解決しない。
  0 件、複数件、世代不一致、PaneRef 不一致は fail closed にする。
  `SessionEnd` の `done` も telemetry に留め、Claude / Codex とも pane 消滅時は正常終了と kill を区別できないため `stale` とする。
  identity 不一致も agent 種別にかかわらず `stale` とする。
- PaneRef の routing、worktree ownership、terminal 実体、論理上の会話、process の生存を別々に判定する。
- `unknown` record を無条件に running へ写像しない。
  public pane の存在、保存した `terminal_id` との一致、完全一致する一意な `agent_session`、workspace の worktree provenance、provider 固有 matcher で検証した agent process を別々に判定する。
  `foreground_cwd` は識別に使わず、worktree provenance がない場合だけ保存された `cwd` を補助照合に使う。
- 0.7.3 v1 は `terminal_id` が変わった row を `stale` とし、cold restart 後の process identity を再束縛しない。
  自動 launch を再導入する後続版では、一致する `agent_session` があれば論理上の会話を再対応付けする候補にできる。
  running へ再束縛できる provider は実測済みの herdr 公式 Codex integration v6 だけとする。
  exact な Codex `agent_session` ref、一意な foreground process、保存済み絶対 executable、argv0 を除く引数列 `["resume", "<session-id>"]`、保存済み `agent start --cwd` と一致する process cwd を照合する。
  候補 PID が `shell_pid` 自身またはその子孫で、現在の foreground process group に属することを OS process 情報で確認し、新しい terminal / process identity を一回の state save で束縛する。
  後続版では、完全一致する一意な `agent_session` と provider 固有の exact resume placeholder がある場合だけ共有 budget の再開待ちへ入れる。
  ref の欠落、不一致、重複、未検証 provider、resume 後の候補欠落または重複、executable / argv / process cwd / ancestry / process group の不一致または検証不能は retry せず、直ちに `stale` とする。
- state machine は、focus されていない agent が `idle` を報告すると public status が `done` へ変わり、focus されると `idle` へ戻る遷移を扱う。
  これは herdr runtime の表示遷移であり、fanout child の terminal completion または nudge authority には使わない。
  cold restart 後の resume placeholder で観測した `idle` はこの遷移に含めず、process の生存を別に確認する。
- herdr backend v1 の自動 nudge は agent 種別にかかわらず無効にする。
  public status、hook telemetry、screen manifest、`agent explain` のどれも送信許可に使わず、peer message または watcher を契機に `pane run` を呼ばない。
  message bus への保存は維持できるが、fanout から herdr の `notification show` は呼ばない。
  自動 nudge の再導入には atomic conditional send / CAS または terminal UI を操作しない out-of-band queue と、agent process から分離した event provenance を要求する。
- CLI-first の wait と後続版の再開待ちは、3 秒以上の整数 `total_timeout`、2 秒間隔、既定 300 秒、各 CLI call 最大 5 秒、snapshot 最大 `ceil(total_timeout / 2 秒)` 回の上記共有 budget を使う。
  terminal result は `matched`、`timed_out`、`cancelled`、`failed` の四値とし、snapshot と event wait を直列に組み合わせない。
- generic workspace shell は `HERDR_ENV=1` から自動検出し、nested tmux では `--backend tmux` または `FANOUT_BACKEND=tmux` で明示的に上書きできるようにする。
- herdr backend v1 の cleanup は read-only の対象表示に限定し、手動 cleanup 後も state row を自動整理しない。
  `worktree remove`、`workspace close`、`pane close`、`--force`、削除用の `worktree open` を fanout から自動実行しない。
  remove / close request に nonce または session epoch の precondition を渡せず、検査から mutation までの TOCTOU を閉じられないためである。
  削除用再登録は setup hook を再実行するため、hook 抑止と operation-scoped completion receipt の両方が使える後続版まで採用しない。
- v1 は初回と cold restart 後の再送のどちらでも `report-metadata` を発行せず、metadata token の欠落を許容する。
  metadata は `state.json`、liveness、nudge authority、完了判定に使わない。
  token の欠落自体は state transition に使わず、cold restart 後の `stale` は `terminal_id` の identity 契約で決める。
  初回報告と再送は、request が expected immutable session generation と target `terminal_id` / workspace generation を原子的に検査するか、fanout が認証済み session と対象資源の lifecycle を排他的に所有する後続版まで有効にしない。
  0.7.3 の presentation fields と 0.7.4 の pane / workspace metadata token reporting は別の version provenance として扱う。
  #494 は 0.7.4 の core runtime matrix、exact tuple の allowlist entry、#426 の operation-scoped safety と identity-bound resource に加え、この request-bound precondition または owned lifecycle が揃うまで有効にしない。
  将来有効化する #494 の `report-metadata` call は対象 `pane_id` または `workspace_id`、固定 `source`、空でない token patch、必要な `seq` / `ttl_ms` だけで構成し、title、display agent、state label を書き換えない。
  #494 は実装前に `fanout_issue`、`fanout_slug`、`fanout_parent`、`fanout_pr`、`fanout_ci` の pane / workspace 配置、固定 source、sequence の永続化、TTL、値欠落時の clear を一意に決める。
  `rows`、`rows_by_agent`、`row_gap` と styling は herdr とユーザーが所有し、fanout は config を書き換えない。
- in-app notification を配信保証のある channel として扱わず、fanout v1 から自動呼び出しもしない。

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
