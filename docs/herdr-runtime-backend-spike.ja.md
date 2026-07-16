# herdr runtime backend 実機検証

ステータス: v1 契約確定。
検証日: 2026-07-16。
対象: `herdr 0.7.3` stable、protocol `16`、schema version `1`。

fanout の herdr backend v1 は CLI-first とし、集約読みには CLI wrapper の `herdr api snapshot` を使う。
raw Socket client は実装しない。
worktree の作成、既存 checkout の採用、削除は herdr に委譲できるが、naming、base 解決、dirty と divergence の検査、idempotency は fanout に残す。
Claude hook の signal は agent process から偽造できるため telemetry に限定し、nudge authority または完了判定には使わない。
herdr backend v1 の自動 nudge は agent 種別にかかわらず無効にし、pane 消滅は `stale` とする。

## 採用判断

| 対象 | v1 の判断 | 理由 |
|---|---|---|
| server 起動 | fanout は既存の named session を要求する | headless CLI は server を自動起動しなかった |
| 集約読み | `herdr api snapshot` を使う | workspace、tab、pane、layout、agent、focus を 1 回で取得できる |
| raw Socket API | 不採用 | v1 で必要な操作は CLI wrapper で足りる |
| worktree | safety gate 後に `worktree create/open/remove` へ委譲する | branch、path、base を指定でき、plugin event も発火する |
| agent 起動 | `agent start` に bare argv、`--cwd`、`--env` を渡す | shell wrapper は終了検出を wrapper に張り付ける |
| nudge | v1 では無効 | hook signal は authority ではなく、状態検査と submit を原子的に実行する CAS もない |
| identity | routing、checkout、terminal、会話、process を別々に照合する | cold restart では `terminal_id` が変わっても公式 session resume が成功する |
| 通知 | best-effort の in-app 通知としてだけ使う | detached 時も `shown:true` で、表示完了の応答ではない |

## 検証条件

検証用の git repository、bare remote、linked worktree、named herdr session を `/private/tmp` に作った。
ユーザーの default herdr session は停止も削除もしていない。
plugin event の検証では `XDG_CONFIG_HOME` と `XDG_STATE_HOME` も `/private/tmp` へ向け、plugin registry と state を隔離した。

追加検証では公式 `v0.7.3` macOS arm64 リリースバイナリ（SHA-256 `b31345392d004ec1f1b2c821e1ad601019fa8385fe1e4c6931321eb58a920773`）を `/private/tmp` に置き、named session と state を隔離した。
herdr 公式 Codex integration v6 の再開試験だけは、すでに信頼済みのこの worktree を cwd に使った。

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
v1 は検証済みの socket path を session namespace と併せて保存し、各 CLI 呼び出しで明示的に選択し、mutation 前に session identity を再確認する。

`terminal_id` は server が所有する terminal 実体の識別子であり、論理上の会話または agent process の識別子ではない。
同じ `terminal_id` でも、想定した agent process の生存は別に確認する。
`terminal_id` が変わった場合は、保存済みの `{source, agent, kind, value}` と完全一致する一意な `agent_session` があれば、論理上の会話を新しい terminal へ再対応付けできる。
`agent_session` ref が欠落、不一致、重複した場合は再対応付けせず fail closed にする。
この ref は attach 前の再開待ちにも現れるため、process の生存を示す証拠には使わない。
fanout-owned epoch は fanout が session lifecycle も所有する後続版でなければ検証に使えない。

## 操作面

| fanout 操作 | 採用 CLI | 結果 | 制約 |
|---|---|---|---|
| launch | `workspace create --cwd ... --no-focus` | `workspace_created` | 最初の workspace は focus 対象がないため focus される |
| launch | `worktree create --workspace ... --branch ... --base ... --path ... --label <nonce> --no-focus --json` | `worktree_created` | fanout の preflight と intent 保存後に呼ぶ |
| launch / recover | `worktree open --workspace ... --path ... --label <nonce> --no-focus --json` | `worktree_opened` | 既存 checkout の採用と git fallback に使う |
| launch | `agent start <name> --workspace ... --cwd ... --env ... --no-focus -- <argv...>` | `agent_started` | `--json` flag はないが JSON envelope を返す |
| list | `api snapshot` | `session_snapshot` | `session.snapshot` の CLI wrapper |
| list | `worktree list --workspace ... --json`、`agent list` | `worktree_list`、`agent_list` | `worktree list` は基準 workspace を明示する |
| read | `pane read`、`agent read`、`pane get` | text または `pane_read`、`pane_info` | 実行中 cwd は telemetry の `foreground_cwd` に出る |
| read | `pane process-info --pane ...` | `pane_process_info` | 想定した agent process の argv と cwd を確認する |
| focus | `agent focus <name>`、`workspace focus <id>` | 対象 agent または workspace を focus | 任意 pane ID への exact focus は CLI にない |
| send | `pane run <pane> <text>` | text と Enter を一操作で送る | 明示的な手動操作に限り、自動 nudge には使わない |
| close | `worktree remove --workspace ... [--force] --json` | `worktree_removed` | workspace を先に閉じない |
| close | `workspace close`、`pane close` | `ok` | checkout は `workspace close` では消えない。mutation 直前に terminal identity を再照合する |
| wait | `api snapshot` の bounded polling | `session_snapshot` | current-state predicate として使い、event wait は v1 では採用しない |

generic pane の exact focus が必要になった場合、Socket API の `pane.focus` を追加候補にする。
child workspace が一つの agent pane を持つ v1 では `agent focus` と `workspace focus` で足りる。

`workspace close` と `pane close` にも `worktree remove` と同等の precondition を課す。
同名 session の再作成で session 名・socket・public ID は再利用されるため、mutation 直前に snapshot を再取得し、保存済み `terminal_id` と agent / workspace provenance が state row と一致することを照合してから close を発行する。
不一致または照合不能は fail closed にし、stale row からの close が無関係な process を終了させないようにする。

root coordinator の `workspace create` も副作用を持つ launch 操作として provisional intent の対象にする。
create 前に owner row key、backend / session identity、root cwd を intent へ保存し、成功応答の workspace ID を intent に束縛してから通常 state へ確定する。
応答喪失または crash 後の再実行は、検証済み session に root cwd と provenance が一致する coordinator workspace が一つだけあればそれを決定的に採用し、複数または不一致は fail closed にして重複作成しない。

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

fanout は herdr を呼ぶ前に次を実行する。

- base ref を immutable commit SHA へ解決する。
- source checkout の dirty と divergence を検査し、既存の fail-closed 契約を保つ。
- branch、path、base SHA、state mapping を照合する。
- 既存 branch の HEAD が期待する base SHA と違う場合は、herdr に渡さず fail closed にする。
- 既存 checkout を作業 pane として再採用する場合は、fanout state が同じ task の所有を示し、branch、path、HEAD がすべて一致するときだけ `worktree open` を使う。
  cleanup の削除前再登録では、path と fanout state の task 所有一致に加え、state row と checkout git dir の作成時 nonce 一致を要求するが、HEAD 一致は要求しない。
- `worktree create`、既存 checkout の `worktree open`、git fallback の最初の mutation より前に、state lock 下で phase `worktree-planned` の provisional launch intent を保存する。
  intent は owner row key、backend、検証済み herdr session / socket identity、operation、worktree ownership nonce、slug、branch、path、base SHA、mutation 前の runtime / git snapshot を持つ。
  新規 create と git fallback では nonce を mutation 前に生成し、既存 checkout の採用では state row と checkout git dir で一致済みの作成時 nonce を使う。
  同じ launch の再実行では intent の backend / session identity が env / default の backend 選択より優先され、明示指定(`--backend` / env)が intent と矛盾する場合は fail closed にする — workspace mutation 後・row 確定前に crash した launch を、別 backend の再実行が intent recovery より先に拾う事故を防ぐ。
  `worktree create` と `worktree open` は intent の nonce を `--label` に渡す。
  open 前の pre-state で対象 checkout を指す workspace がない場合は `already_open:false` だけを受理する。
  `already_open:true` は、pre-state の時点で同じ workspace ID と label が task の state / intent に束縛済みで、全所有条件が一致する場合だけ受理する。
  git fallback は intent 保存後に checkout を作り、git dir marker を書いてから同じ nonce の `worktree open --label` を呼ぶ。
  create 応答後は、workspace ID が pre-state にないこと、応答 checkout path が intent path と一致して pre-state にはないこと、label が nonce と一致すること、repo provenance が source request と一致することを先に確認し、応答 checkout の git dir marker を exclusive create で書く。
  marker の書き込み後に label と marker を再読し、branch と HEAD を含む残りの事後条件を検査する。
  create / open 成功後は応答または snapshot の `workspace.label` と checkout git dir marker の両方が intent nonce と一致することを要求し、workspace ID、nonce、provenance と phase `worktree-ready` を intent へ保存する。
  label は workspace object の所有しか示さないため、git dir marker の代用にしない。
  各 worktree mutation の直前に step、exact request、per-step pre-state を intent へ加え、phase `worktree-starting` を同じ state lock 下で保存してから呼び出す。
  phase `worktree-planned` の再実行は、記録済み pre-state と現在値が一致する場合だけ `worktree-starting` へ遷移して request を一回発行できる。
  phase `worktree-starting` の再実行は、対象資源が見つからない場合も request を再発行しない。
  一意な workspace と checkout がすでにあり、label、git dir marker、branch、path、HEAD、provenance が intent と一致する場合だけ応答喪失として `worktree-ready` へ進める。
  operation `git-fallback` の git 作成 step は、workspace がなくても checkout と git dir marker が intent に一致すれば、次の open step、exact request、現在の per-step pre-state を同じ state save で更新して `worktree-planned` へ遷移できる。
  create 成功から git dir marker 書き込みまでの crash を含め、nonce の両側を証明できない crash window は自動採用せず fail closed にする。
  mutation 前失敗後または検証済み rollback 後に launch を中止する場合は、pre-state の不変または復元と intent 削除を同じ state save で確定する。
  rollback 後に fallback へ進む場合は intent を削除せず、後述の operation / phase へ同じ state save で遷移させる。
  mutation の有無または rollback 完了が不明な場合は intent を残して fail closed にする。
  `agent start` の前に agent launch nonce と emitter nonce を生成し、hook へ注入する telemetry routing binding、agent launch nonce の衝突耐性のある hash を含む session 一意の deterministic agent name、発行する argv / env fingerprint、API で再観測できる argv / cwd を intent へ保存して、phase `agent-starting` を state lock 下で確定する。
  env fingerprint は発行内容の監査にだけ使い、lost-response recovery の照合条件には使わない。
  成功応答を受けたら returned PaneRef、terminal identity、応答の argv を加えて phase `agent-started` を保存する。
  phase `worktree-ready` で同名 agent が存在せず、child checkout が clean かつ HEAD == 記録済み base SHA の場合だけ `agent start` へ進める。
  phase `agent-starting` の再実行は、expected session / workspace と nonce 所有 worktree 上に同名 agent が一つだけあり、その pane の `process-info` が intent と同じ argv / cwd の process を一つだけ返す場合に限る。
  この場合は観測した PaneRef、terminal identity、PID を含む process identity を新たに束縛して `agent-started` へ進める。
  phase `agent-started` の再実行は、pane が生存していれば保存済み PaneRef と terminal identity が現在値と一致し、`process-info` の argv / cwd も intent と一致する場合だけ process identity を束縛して final row へ進める。
  保存済み agent start 応答があり pane がすでに消滅している場合は、returned PaneRef を束縛した `stale` row を確定する。
  同じ emitter nonce の pending `done` があっても agent-reported telemetry として保存するだけで、`stale` を `done` に変えない。
  `agent-starting` 後に agent が存在しない場合も mutation の有無を証明できないため自動で再発行せず fail closed にする。
  worktree、agent、process の照合失敗、欠落、重複は自動では触らず fail closed にする。
  final row の確定、pending emitter telemetry の反映、intent の削除は state lock 下の同じ state save で実行する。
- `worktree create` 後は、応答、workspace の worktree provenance、git の branch、path、HEAD を照合する。
- `worktree create` の事後条件違反をそのまま fallback の条件にしない。
  今回の呼び出しが作ったと証明できる workspace と checkout だけを rollback 対象にする。
  応答 checkout path が intent path と違う場合は marker を書かず、資源へ自動では触れずに fail closed とする。
  `worktree remove` 後に workspace と target path がなく、branch がどの worktree にも checkout されていないことを確認する。
  作成前に存在しない branch は、作成直後に記録した OID を old OID とする compare-and-delete で削除する。
  branch ref が変わっていれば削除せず fail closed にする。
  fanout-owned workspace、checkout、branch、state mapping が呼び出し前の状態へ戻ったことを再検証する。
  plugin event の発火は rollback しない。
  所有を証明できない場合、rollback に失敗した場合、または既存資源を削除する必要がある場合は fail closed にする。
- fanout による git worktree 作成と続く `worktree open` への fallback は、応答 checkout path と repo provenance の一致を確認して marker を書けた後、branch または HEAD の事後条件が契約と違った場合だけ実行する。
  rollback が完了し、target path が存在せず、branch が不存在または期待する HEAD にあることを実行条件にする。
  rollback と pre-state 復元を確認した後、同じ intent を operation `git-fallback` と phase `worktree-planned` へ state lock 下で遷移させてから git mutation を始める。
  herdr が mutation 前に失敗した場合と、事後条件違反以外のエラーでは fallback せず fail closed にする。

rollback では `worktree remove --workspace <id> --json` を force なしで使う。
rollback 前にも workspace ID が create 応答と一致して一意であること、現在の workspace label と checkout git dir marker が intent の worktree ownership nonce と一致すること、worktree provenance を照合し、今回の create が作った資源だけを対象にする。
dirty または untracked file がある場合は、今回の呼び出し後に plugin や別 process が書いた可能性を除外できないため fail closed にする。
自動 rollback では `--force` を使わない。

### workspace 配置

`worktree create --workspace w1` は w1 内の pane を作らず、独立した child workspace w2 を作った。
親と子は同じ `repo_key` と worktree provenance で repository group に並ぶ。
`session.snapshot` には child から parent workspace を指す ID がない。

親 issue の実現形は「親 workspace の内部に child pane」ではない。
project root の coordinator workspace を一つ作り、各 child worktree を sibling workspace として開く。
fanout の state が parent issue と child workspace の対応を保持する。
pane split は同じ checkout 内に補助 process を追加する場合だけ使う。

### cleanup

dirty な child worktree を force なしで削除すると、`dirty_worktree_requires_force` で拒否された。
`--force` では checkout と workspace が消え、branch は残った。
clean な focused child を削除すると、focus は repository root workspace へ戻った。

state row 起点の cleanup は、通常、force、削除用再登録の全経路で最初の mutation 前に row key、backend / session、state row の旧 workspace ID、expected workspace label、repo、branch、path、worktree provenance、作成時 nonce、pre-state を cleanup intent として state lock 下で保存する。
通常と force の経路は旧 workspace ID を removal binding に設定し、削除用再登録の経路は removal binding を空のまま開始する。
各 mutation の直前にも intent、state row、runtime、checkout git dir を再取得し、同じ所有条件を照合する。
mutation 対象の workspace identity は cleanup intent の removal binding とし、削除用再登録では open 後に得た新しい ID と観測 label を phase `removal-bound` として追記する。
workspace label は両経路とも作成時 nonce と完全一致させる。
public workspace ID、path、branch の一致だけを削除権限に使わない。
nonce が欠落または不一致の場合、または一致する state row が一意でない場合は fail closed にする。

`workspace close` を先に実行すると checkout は残る。
続く `worktree remove --workspace <closed-id>` は `workspace_not_found` になる。
この場合は保存済みの旧 workspace ID が検証済み session に存在せず、対象 checkout を指す workspace が一つもないことを確認し、path と fanout state の task 所有一致に加え、checkout の実体所有権を照合する。
実体所有権は、fanout が create / adopt 時に checkout の git dir(`git rev-parse --git-dir` 配下)へ書き込み state row にも保存した作成時 nonce の一致で確認する(HEAD の進行は許容する)。
git common dir・branch・path の一致は nonce の前提条件にすぎず、単独では所有権の証明にしない — 元 checkout の削除後に同じ path・branch で作り直された worktree は nonce を持たないため、この照合で除外して削除しない。
nonce が欠落または不一致の checkout は fail closed にし、自動 cleanup では触らない。
削除用再登録では cleanup intent の operation を `removal-reopen` として確定してから、同じ nonce を渡す `worktree open --label` を呼ぶ。
初回応答は `already_open:false` だけを受理する。
open 応答を失った場合だけ、pre-state 後に現れた workspace が一つで、旧 ID の不在、label、git dir marker、provenance が一致すれば、その新しい workspace ID を回復できる。
open 応答または回復した新 ID は cleanup intent の一時 removal binding として保存し、通常の state row の旧 IDを上書きしない。
remove の直前に旧 ID が引き続き不在で、新 ID の workspace が一意に同じ nonce 所有 checkout を指すことと全所有条件を再照合する。
open が mutation 前に失敗した場合は runtime / git の不変を確認して同じ cleanup intent を再利用または削除し、remove 完了時は資源消滅の検証、state row 更新、intent 削除を同じ state save で確定する。
mutation の有無または remove 完了が不明な場合は cleanup intent を残して fail closed にする。
削除用の再登録は作業 pane の再採用ではないため、branch の HEAD 一致ゲートを適用しない。

cleanup の順序を次で固定する。

1. 全所有条件を mutation 直前に照合し、`worktree remove --workspace <id> --json` を実行する。
2. dirty 拒否時だけ、fanout の既存確認を通して `--force` を使う。
3. branch 削除は fanout の git lifecycle が担当する。

### plugin event

隔離した local plugin を `plugin link` し、`worktree.created` と `worktree.removed` の hook を登録した。
CLI 経由の create と remove で両 hook が各一回実行され、plugin log は `status:"succeeded"`、`exit_code:0` を返した。

作成 event は `workspace` と `worktree`、削除 event は `workspace_id`、最終 `workspace`、`worktree`、`forced` を含んだ。
fanout が worktree 実体化を herdr へ委譲すれば、worktree setup plugin と共存できる。
追加検証では公式 herdr 0.7.3 の `worktree open` が `worktree.opened` hook を一回だけ実行し、plugin log は `status:"succeeded"`、`exit_code:0` を返した。
payload は workspace、active tab、checkout path、branch、`already_open:false` を含んだ。
公式 Socket API と hook 実測の両方で、`worktree.opened` は open 経路、`worktree.created` は create 経路の event だった。

## pane と agent の lifecycle

### bare argv と env

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

fanout は fanout 固有の値と呼び出し元で解決した `PATH` を `--env KEY=VALUE` で渡し、herdr の実行環境識別子は herdr の env と snapshot から取得する。
agent executable は fanout の起動環境で解決した絶対パスを bare argv の先頭に置く。
#427 が注入する lifecycle hook も、同じ時点で解決した fanout executable の絶対パスを呼ぶ。
絶対パスだけでは、agent が起動後に実行する `git` や `gh` が server の ambient PATH で解決されるため、server を minimal env で起動した環境では作業が失敗する(Pass 2 レビュー時の 0.7.3 実機確認)。
tmux backend の `BuildResolvedCommand` と同じく、呼び出し元 `PATH` を明示 `--env` で引き継ぎ、agent と hook の実行を herdr server の ambient PATH に依存させない。

`agent.start` の agent 名は workspace 内ではなく session 全体で一意が要求され、重複は `agent_name_taken` で失敗する(Pass 2 レビュー時の 0.7.3 実機確認)。
複数の repo や親が同じ session を共有しても衝突しないよう、v1 の launch 名は repo、親参照、intent に保存した agent launch nonce の hash から `core/naming` で生成する。

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
fanout は bare argv を採用し、tmux backend の「agent 終了後も shell を残す」契約を herdr backend には持ち込まない。
#427 は `agent start` の Claude argv に `--settings` lifecycle hook を注入し、fanout CLI を呼ぶ runtime 非依存の telemetry emitter として agent の報告状態を `state.json` へ記録する。
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

ログと agent 出力の読み取りには `recent-unwrapped`、TUI の視覚確認には `visible` を使う。
`pane read` は raw text を出力し、`agent read` は `pane_read` result に text、source、revision、truncated を入れる。

`pane get` の `cwd` は label、follow-cwd、session restore に使われる pane または workspace の cwd を表す。
`foreground_cwd` は現在 PTY を制御する foreground process の cwd を表す。
実際、foreground で `(cd /; sleep 15)` を実行している間も `cwd` は元 repository のままで、`foreground_cwd` は `/` になった。

`foreground_cwd` は表示と診断の telemetry とし、PaneRef の識別または生存判定には使わない。
PaneRef の routing は backend、session namespace、workspace ID、pane ID で行う。
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
peer message の bus への保存は維持し、best-effort notification は入力を伴わない通知としてだけ使える。
ユーザーが対象 pane を確認して明示的に実行する `pane run` は手動操作として扱う。
自動 nudge の再導入には、runtime の atomic conditional send または terminal permission UI を操作しない out-of-band queue と、agent process から分離した event provenance が必要になる。

### focus と wait

`agent focus <name>` と `workspace focus <id>` は対象を正確に focus した。
`--no-focus` は二つ目以降の worktree と agent 起動で focus を維持した。

`wait output` は出力遷移を待ち、`output_matched` と matched line、read result、revision を返した。
`agent wait --status idle` は current state がすでに idle でも即時成功せず、後続 event を待った。
focus されていない agent が `idle` を報告した場合は `done` event が返り、`agent focus` 後に `idle` へ変わった。

snapshot 後に event wait を登録すると、二つの操作の間に起きた遷移を逃す lost-wakeup race がある。
CLI-first の v1 は bounded snapshot polling を使い、CLI の一回待機を current-state predicate と組み合わせない。
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

`pane report-metadata` で title、display agent、idle の state label を設定できた。
agent pane から OSC title sequence を出しても、これらの metadata field は変わらなかった。
metadata は cold restart 後に消える表示専用データであり、`state.json` または liveness 判定には使わない。

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
fanout の notify backend は維持し、herdr の in-app notification は設定済み利用者向けの best-effort channel とする。

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
`agent.send`、`pane.send_input`、`pane.focus` も schema にはあるが、今回の v1 操作は手動の `pane run`、`agent focus`、`workspace focus` で足りる。

## version と JSON 対応

stable public workspace、tab、pane ID の契約は 0.7.0、既存 local branch の worktree create/open は 0.7.1、`session.snapshot` と `api schema --json` は 0.7.2 で入った。
v1 は done focus 修正を含み、今回の全操作を確認した `herdr >= 0.7.3` を要求する。
`pane report-metadata` 自体は 0.7.3 で動作し、#494 が対象にする sidebar token 表示は `herdr >= 0.7.4` を要求する。

| 採用コマンド | 最低 version | JSON 対応 |
|---|---:|---|
| `herdr --version` | 0.7.3 baseline | text のみ |
| `status --json`、`session list --json` | 0.7.3 baseline | 明示 `--json` |
| `workspace create/list/focus/close` | 0.7.3 baseline | JSON envelope を標準出力へ返す。`--json` は付けない |
| `worktree create/open/list/remove` | 0.7.1 | 明示 `--json` |
| `agent start/list/read/focus` | 0.7.3 baseline | JSON envelope を標準出力へ返す。`--json` は付けない |
| `pane get/run/close` | 0.7.3 baseline | mutation と get は JSON envelope。`--json` は付けない |
| `pane process-info` | 0.7.3 baseline | JSON envelope を標準出力へ返す。対象は `--pane` で指定する |
| `pane read` | 0.7.3 baseline | text または ANSI を直接出力する |
| `pane report-metadata` | 0.7.3（sidebar token 表示は 0.7.4） | 成功時は出力なし（`--json` 非対応） |
| `api snapshot` | 0.7.2 | JSON envelope を標準出力へ返す。`--json` は付けない |
| `api schema --json` | 0.7.2 | 明示 `--json` |
| `notification show` | 0.7.3 baseline | JSON envelope を標準出力へ返す。`--json` は付けない |
| `plugin link/list/log list` | 0.7.3 baseline | `list` は `--json` 対応。他は JSON envelope |

`baseline` はその version 以前の導入時期を主張せず、v1 が要求する実機確認済み version を示す。

version ごとの根拠は [v0.7.0](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.0)、[v0.7.1](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.1)、[v0.7.2](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.2)、[v0.7.3](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.3) を参照する。

## 後続 issue への契約

#423、#425 から #429、#494 は次の制約を前提にする。

- backend は明示的に起動済みの named herdr session を使う。
- backend 選択の resolver は final state rows と provisional intents の両方を入力にする。
  legacy row の空 backend は tmux に正規化する。
  実際の issue / Project / plan の親では、既存 rows / intents が一つの backend に一致する場合だけその backend を再利用し、mixed state または `--backend` / env との不一致は fail closed にする(明示的な移行はユーザー操作)。
  stickiness の単位は実際の issue / Project / plan の親に限る。
  `@manual` のような synthetic parent は互いに独立した launch の集まりであり、row identity とその intent の単位で backend を固定する。
- worktree は root coordinator と sibling child workspace で配置する。
- fanout が worktree safety gate と idempotency を所有し、herdr は checkout と workspace の実体化を担当する。
- create / open / git fallback の mutation 前に provisional intent を保存し、同じ worktree ownership nonce を workspace label と checkout git dir marker の両方で照合する。
  各 worktree mutation の直前に phase `worktree-starting`、exact request、per-step pre-state を保存し、starting の再実行では対象資源がなくても request を再発行しない。
  git fallback の git 作成後は、checkout と marker を含む現在値を次の open step の pre-state として同じ state save で更新する。
  response loss は phase と事後条件から回復し、mutation の有無を証明できない場合は intent を残して fail closed にする。
  `worktree open` の `already_open:true` は pre-state で同じ workspace ID / label が task に束縛済みの場合だけ受理する。
- `worktree create` の応答 checkout path が intent path と違う場合は marker を書かず、自動 rollback / fallback を行わず fail closed にする。
  path と provenance の一致後に marker を書けた資源だけを rollback 対象とし、branch / HEAD 違反からの rollback 完了後だけ git fallback へ進める。
- agent は bare argv、明示 `--cwd`、fanout 固有値と呼び出し元 `PATH` を渡す明示 `--env` で起動する。
  launch 名は repo と親参照に agent launch nonce の hash を加え、同じ intent では安定し session 全体で一意になる決定論的名前を `core/naming` で生成する。
  `agent start` 前に agent launch nonce、emitter nonce と telemetry routing binding、agent name、argv / env fingerprint、argv / cwd を intent の phase `agent-starting` として保存する。
  応答喪失時は expected session / workspace と nonce 所有 worktree 上の一意な同名 agent について `process-info` の argv / cwd を照合し、観測した PaneRef、terminal identity、process identity を束縛する。
  保存済み応答後に pane が消滅した場合は returned PaneRef を束縛した `stale` row を確定し、pending `done` は telemetry としてだけ保存する。
  応答未保存の agent 欠落、重複、識別不一致は fail closed にする。
- CLI 呼び出しは保存済みの検証済み socket path を明示的に選択し、mutation 前に session identity を再確認する(`HERDR_SOCKET_PATH` が `HERDR_SESSION` より優先されるため)。
  agent executable と注入 hook が呼ぶ fanout executable は、fanout の起動環境で解決した絶対パスを使う。
- #427 は `agent start` の Claude argv に `--settings` lifecycle hook を注入し、fanout CLI 経由で `state.json` の `reported_state` を更新する runtime 非依存の telemetry emitter を追加する。
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
  public pane の存在、保存した `terminal_id` との一致、完全一致する一意な `agent_session`、workspace の worktree provenance、想定した agent process を別々に判定する。
  `foreground_cwd` は識別に使わず、worktree provenance がない場合だけ保存された `cwd` を補助照合に使う。
- `terminal_id` が変わっても、一致する `agent_session` があれば論理上の会話を再対応付けできる。
  `agent_session` ref が欠落、不一致、重複した場合は再対応付けせず、ref 一致後も想定した agent process を確認するまで running にしない。
- state machine は、focus されていない agent が `idle` を報告すると public status が `done` へ変わり、focus されると `idle` へ戻る遷移を扱う。
  これは herdr runtime の表示遷移であり、fanout child の terminal completion または nudge authority には使わない。
  cold restart 後の resume placeholder で観測した `idle` はこの遷移に含めず、process の生存を別に確認する。
- herdr backend v1 の自動 nudge は agent 種別にかかわらず無効にする。
  public status、hook telemetry、screen manifest、`agent explain` のどれも送信許可に使わず、peer message または watcher を契機に `pane run` を呼ばない。
  message bus への保存と入力を伴わない best-effort notification は維持できる。
  自動 nudge の再導入には atomic conditional send / CAS または terminal UI を操作しない out-of-band queue と、agent process から分離した event provenance を要求する。
- CLI-first の wait は bounded snapshot polling にし、snapshot と event wait を直列に組み合わせない。
- generic workspace shell は `HERDR_ENV=1` から自動検出し、nested tmux では `--backend tmux` または `FANOUT_BACKEND=tmux` で明示的に上書きできるようにする。
- cleanup は `worktree remove` を `workspace close` より先に実行する。
  通常、force、削除用再登録の全経路で最初の mutation 前に cleanup intent を保存する。
  通常 / force は state row の旧 workspace ID を removal binding とし、削除用再登録は旧 ID と expected label を保存して空の binding から始める。
  通常、force、削除用再登録の全経路で remove 直前に backend / session / workspace identity、workspace label、repo、branch、path、provenance、state row と checkout git dir の作成時 nonce を照合し、欠落または不一致は fail closed にする。
  `workspace close` が先行した場合は旧 workspace ID の不在、対象 checkout を指す workspace が 0 件であること、nonce 所有 checkout を確認し、cleanup intent を operation `removal-reopen` へ遷移させてから同じ nonce の `worktree open --label` を呼ぶ。
  初回 open は `already_open:false` を要求する。
  open 応答または応答喪失から回復した新 workspace ID を一時 removal binding として保存し、remove 直前に旧 ID の不在、新 ID、nonce、全所有条件を再照合する。
  削除用再登録は作業再採用の HEAD 一致ゲートから除外する。
- `report-metadata` は表示専用とし、cold restart 後に消える前提で再送する。
  metadata は `state.json` または liveness 判定に使わない。
  `pane report-metadata` は 0.7.3 で動作するが、sidebar token 表示は 0.7.4 以上を要求する。
  #494 は issue 番号、slug、親参照、PR と CI の状態を pane または workspace の metadata として報告し、sidebar layout は herdr とユーザーが所有する。
- in-app notification を配信保証のある channel として扱わない。

## 参考

- [CLI reference](https://herdr.dev/docs/cli-reference/)
- [Socket API](https://herdr.dev/docs/socket-api/)
- [Agents](https://herdr.dev/docs/agents/)
- [Configuration](https://herdr.dev/docs/configuration/)
- [Session state](https://herdr.dev/docs/session-state/)
- 関連分析: [herdr 競合分析](competitive-herdr.ja.md)
- 親設計: [#423](https://github.com/butaosuinu/fanout/issues/423)
- 親設計の承認: [#424 spike 反映](https://github.com/butaosuinu/fanout/issues/423#issuecomment-4986704437)
- 検証 issue: [#424](https://github.com/butaosuinu/fanout/issues/424)
