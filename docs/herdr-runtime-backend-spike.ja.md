# herdr runtime backend 実機検証

ステータス: 0.7.5 wave 2 の実機検証を完了し、pane-targeted surface を使う fanout-owned herdr session の実装を解禁する。
判断主体はユーザー、floor 0.7.5 への改訂日は 2026-07-22 JST、crash 安全機構の tmux-parity 密度への簡素化改訂日は 2026-07-27 JST である。
この判断は後続 issue の実装条件を定めるものであり、この PR はコードを変更しない。
0.7.5 core runtime matrix と live-agent surface、0.7.4 metadata token reporting と sidebar row layout は実測済みである。
この文書の日付は日本標準時（JST、UTC+09:00）で記す。
core runtime の検証日は 2026-07-16、2026-07-21、2026-07-22、metadata token reporting と sidebar row layout の追試日は 2026-07-17 である。
0.7.3 と 0.7.4 は protocol `16`、0.7.5 は protocol `17` であり、schema version はすべて `1` である。
関連分析の「0.7.4 wave 2」は 2026-07-21 の旧判断を時系列で残した段落であり、その直後に記録された 0.7.5 breaking change と floor 改訂が優先する。
後続実装が従う current contract はこの文書である。
2026-07-24 のユーザー判断により、#526 の compatibility admission は stable `>=0.7.5` の version gate だけとする。
schema、method、field、CLI help、protocol、behavior profile、active manifest は事前検査せず、実際の method call が失敗した場合は共通の unavailable error を返す。
以下に残る structural gate、三段 gate、behavior profile、active manifest gate の記述は実測と旧判断の履歴であり、#526 の実装条件には使わない。

fanout の herdr backend wave 2 は CLI-first とし、集約読みには CLI wrapper の `herdr api snapshot` を使う。
raw Socket client は実装しない。
version / session の検査と owned session の検査後、snapshot / list / wait、targeted read、owned server の bootstrap、launch、cleanup、focus、emitter、metadata、自動 nudge の送信契約を後続実装へ解禁する。
console / coordinator / agent launch は加えて #528 の direct-launch 契約完了を要求する。
2026-07-27 のユーザー決定により、crash 安全機構は「簡素化方針」の判断基準で tmux backend と同水準の密度にする。
自動 mutation は state lock、送信直前の identity 再照合、mutation 直前の最小意図記録、事後条件検査を通し、応答喪失は再実行時の存在確認で採用または fail-closed `manual_cleanup_required` にする。
linked worktree 間の intent、console、final row、telemetry routing は canonical git common directory 配下の単一 Herdr control registry を正典とし、worktree-local `state.json` へ分散しない。
Herdr backend の `--team` は #528 の fail-closed gate で最初の mutation 前に拒否し、#568 が registry-backed peer 解決を導入した後に再評価する。
#552 の Herdr nudge 実装は #568 の完了を前提とする。
child cleanup は保存済み identity の照合後に `worktree remove` を発行し、branch は herdr に削除させず fanout の compare-and-delete だけが削除する。
owned XDG の config と plugin registry に予期しない setup hook があれば launch 前に fail closed にする。
console shell と agent の実行物は fanout が解決した絶対 path を launcher が直接起動し、content-addressed launch bundle は proof-grade tier の再導入候補とする。
provider hook の signal は agent process から偽造できる協調 telemetry とし、tmux backend と同じ nudge gate には使うが、完了判定または cleanup の根拠には使わない。
registry-backed peer 解決後の自動 nudge は current launch に束縛された fresh provider signal と送信直前の live process identity を照合した後だけ、tmux の `shouldNudge` と同じ条件で `agent prompt` を一回発行する。
`codexPlanMode` は恒久除外を撤回し、fanout-owned non-shell launcher が絶対パスの `fanout __codex-plan-tui` を root pane 内で起動する。
その実装は #528、#529、#544 の導入後の別 issue とする。
pane 消滅または 0.7.5 direct-launch row の `terminal_id` 変化は `stale` とする。
0.7.4 Codex integration v6 の resume 実測は履歴として残すが、0.7.5 direct launch の resume には流用しない。
session identity の read と mutation は別の CLI 接続になるため、直前の version / identity 検査だけでは mutation の対象を束縛できない。
0700 / 0600 と owner UID だけでは inherited ACL を検査しないため、別 UID の排除を証明しない。
後述の private namespace gate が対象 path に成功した場合だけ別 UID を排除し、同一 UID の agent は排除せず、ownership marker を mutation authority にしない。
tmux backend も同一 UID の agent が server 停止、他 pane への `send-keys`、state signal の偽造を実行できる協調プロセス信頼を前提に launch、cleanup、nudge を提供している。
private namespace gate 済みの fanout-owned private socket は影響範囲を fanout 所有 session に閉じるため、tmux-parity tier では同一 UID の残余リスクを受容する。
request-bound generation と conditional mutation、server-authenticated controller capability、server / agent の UID 分離は、herdr が primitive を提供した場合に proof-grade tier へ格上げする条件として保持する。

## 簡素化方針（2026-07-27）

ユーザー決定（2026-07-27）により、crash 安全機構の密度は tmux backend を基準にする。
実測比較では、旧正典に忠実な #573（worktree 段のみ、+8,256 行）が tmux 側同等機能（約 2,600 行）の 3〜4 倍に達し、根因は正典の intent / phase machine、CAS registry、replay 契約の密度だった。
tmux-parity は信頼モデルだけでなく機構の密度にも適用し、各契約条項を次の基準でこの順に分類する。

1. tmux backend が同じ失敗モードに持つ対処と同水準にする。tmux の水準は state lock、fail-fast、既存資源の idempotent skip である。迷ったら tmux 側（`internal/infra/worktree`、`internal/infra/state`、`internal/app/panelaunch`）に合わせる。
2. herdr にしか無い失敗モードだけ最小の追加対処を許す。対象は socket mutation の応答喪失窓と server 再起動、対処は「mutation 直前の最小意図記録（単一 journal 行程度）+ 再実行時の存在確認 → 採用 or fail-closed `manual_cleanup_required`」までとする。
3. 次は撤廃または任意記録へ格下げし、「後続 issue への契約」の再評価条件表に proof-grade tier の再導入候補として残す: フル phase machine（`worktree-planned` 以降の多段遷移）、CAS provisional registry、exact request / pre-state の replay 契約、canonical bytes / digest / golden bytes、bundle / journal / epoch / tombstone 群、ownership nonce の二重照合、plan spec snapshot の SHA-256 束縛。
4. 不変: tmux-parity 信頼モデル、capability gate、`--team` 拒否（#568 まで）、`codexPlanMode` 対応方針、dashboard read-only 境界、proof-grade tier の再評価条件表。実測事実の節も変更しない。

本文の launch / worktree / cleanup / state 系契約はこの基準の適用後の形であり、各条項は tmux 水準（基準 1）か応答喪失窓・server 再起動への最小追加対処（基準 2）のどちらかに属する。

## 採用判断

| 対象 | wave 2 の判断 | 理由 |
|---|---|---|
| owned server | Go（#526 が PR #572 で実装済み） | owner-only の mode / UID / ACL を検査した owned XDG / socket / marker で別 UID と session 外への影響を封じ込める |
| server 起動 | Go（#526 が PR #572 で実装済み） | per-repo supervisor が caller routing env を fanout-owned XDG / config / socket / session で上書きし、foreground server child を bootstrap する |
| owned server restart | Go（実装は #530） | 明示操作が marker / lease と saved process / socket の不在を照合して一回だけ spawn し、結果不明は fail closed にする（応答喪失窓への最小追加対処） |
| 集約読み | `herdr api snapshot` を使う | workspace、tab、pane、layout、agent、focus を 1 回で取得できる |
| content read / peek | Go | exact PaneRef と `terminal_id` を直前・直後に再照合し、不一致は結果を破棄する |
| raw Socket API | 不採用 | wave 2 で必要な操作は CLI wrapper で足りる |
| worktree | Go（#527） | state lock、workspace label nonce、送信直前再照合と Git 事後条件で誤採用を防ぐ |
| cleanup | Go（#531） | 保存済み identity の照合後に `worktree remove` を発行し、dirty は明示確認なしに force しない |
| cleanup 後の branch | fanout の compare-and-delete 所有 | herdr は branch を残すため、削除は fanout の compare-and-delete（tip 照合と checked-out worktree 不在の確認後の削除）に限る |
| console workspace | Go（#530） | console の intent 行と exact token で user shell を起動し、agent / nudge lane から分離する |
| agent 起動 | Go（#528） | owned config の `terminal.default_shell` を fanout launcher に固定し、fanout が解決した絶対 executable / argv / env を launcher が直接起動する |
| workload env | Go（#528） | 呼び出し元 env を snapshot し、Herdr / fanout の routing env を除いて one-shot 0600 env file で launcher へ渡す。raw value を registry / log に保存しない |
| plain shell への `pane run` | 自動 launch には不採用 | text と Enter の配送時に shell readiness と空入力を条件化できない |
| `agent start` | 自動 launch には不採用 | canonical agent executable を bare name で解決するため、fanout が選んだ絶対 executable を pin できない |
| compatibility gate | version だけを検査する | stable `>=0.7.5` を許可する。schema、method、field、CLI help、protocol、behavior profile、active manifest は事前検査しない |
| attach | custom socket を選ぶ bare `herdr` command を提示する | `session attach <name>` は別 daemon を自動起動し得るため実行しない |
| focus | Go | TUI の明示操作だけが送信直前再照合後に focus する |
| `--team` | 拒否（#568 の registry-backed peer 解決まで。暫定 gate は #528） | `--dry-run` を含む backend / flag validation で明確な invocation error を返し、tmux backend の既存経路は変更しない |
| nudge | Go（#552 は #568 完了後） | shared registry から宛先を解決し、fresh provider signal と送信直前の live process identity が一致する許可状態だけへ no-wait の `agent prompt` を発行する |
| `codexPlanMode` | Go(実装は #528 / #529 / #544 後の別 issue) | 同じ non-shell launcher で絶対 path の `fanout __codex-plan-tui` を起動し、`agent start --kind codex` の args にしない |
| live identity | Go | routing、checkout、terminal、会話、process を別々に照合する |
| 0.7.5 direct launch の cold restart resume | 保留 | real Codex の direct launch から restart / attach / resume まで未実測のため、`terminal_id` 変化時は `stale` にする |
| console / coordinator close | Go | checkout を持たない exact owned workspace / pane だけを送信直前に再照合し、応答喪失は再実行時の存在確認で確定する |
| child launch rollback | tmux の `failCleanup` と同水準 | 今回作った資源だけを identity 照合後に削除し、照合不一致または response loss では資源を残して fail closed にする |
| dirty `--force` | 明示確認後だけ許可 | dirty checkout はユーザーの明示確認なしに force しない。launch rollback の remove も force なしで発行する |
| emitter | Go | cooperative telemetry と nudge gate に限り、completion / cleanup authority にしない |
| metadata | Go | exact target を直前・直後に照合し、表示専用 token だけを報告する |
| 通知 | 手動検証だけに使う | detached 時も `shown:true` で、表示完了の応答ではなく、fanout は自動発行しない |

## 検証条件

検証用の git repository、bare remote、linked worktree、named herdr session を `/private/tmp` に作った。
2026-07-16 の v1 検証ではユーザーの default herdr session を停止も削除もしていない。
plugin event の検証では `XDG_CONFIG_HOME` と `XDG_STATE_HOME` も `/private/tmp` へ向け、plugin registry と state を隔離した。

追加検証では公式 `v0.7.3` macOS arm64 リリースバイナリ（SHA-256 `b31345392d004ec1f1b2c821e1ad601019fa8385fe1e4c6931321eb58a920773`）を `/private/tmp` に置き、named session と state を隔離した。
herdr 公式 Codex integration v6 の再開試験だけは、すでに信頼済みのこの worktree を cwd に使った。

sidebar 追試と最初の wave 2 では公式 `v0.7.4` macOS arm64 リリースバイナリ（SHA-256 `24992e1625dbdcb18354a59e299e4b263c312400b31396cdc07cd46ed57f24a7`）を使った。
インストール済みの `herdr 0.7.4` はこのリリースバイナリと byte 単位で一致した。
隔離 session の `status --json` は client / server version `0.7.4` と protocol `16`、`api snapshot` は version `0.7.4` と protocol `16`、`api schema --json` は schema version `1` を返した。

0.7.5 再検証では公式 `v0.7.5` macOS arm64 リリースバイナリを使った。
インストール済み binary の SHA-256 は `37350546b0012555943b92eaf962665de4e264395baeb44227b8015e8ff5b0d6` であり、公式 release asset の digest と byte 単位で一致した。
release tag の commit は `ef4c23f5775bb8cfec05f05d0844226ff959a07a` である。
検証資源は `/private/tmp/fanout-557-herdr-075.5AXZTV` 配下へ置き、XDG 四変数、config、server socket、client socket をすべて隔離した。
隔離 session の `status --json` は client / server version `0.7.5`、protocol `17`、`compatible:true`、`restart_needed:false` と exact session / socket を返した。
`api schema --json` は protocol `17`、schema version `1`、89 methods を返し、SHA-256 は `1ef4eb9ec655cb0c89726895f437d8654bdde13a22e591fda06a9015d03d88c7` だった。
0.7.5 の検証では default Herdr session を停止、削除、上書きしていない。

post-work review 後の launch readiness 追試は `/private/tmp/fanout-557-shell-readiness-2` 配下の別 session で実行した。
owned config の `[terminal] default_shell` を絶対 launcher path、`shell_mode` を `non_login` にすると、launcher は exact cwd、`HERDR_PANE_ID`、`HERDR_WORKSPACE_ID`、server env の nonce を受け取った。
一回だけ即時に出した marker は最初の pane の buffer に残らなかったが、capture 開始後の nonce 付き marker は `pane wait-output` が検出した。
`w3:p1` では marker 検出後に `pane run` で exact operation token だけを送り、launcher が absolute fake process へ空白を含む env / argv と exact cwd を渡した。
この probe は non-shell launcher surface の成立だけを示し、production helper は marker を bounded に再送し、token 以外の入力を拒否して intent の絶対 executable / env / argv を shell interpretation なしで起動する。
この追試では nonce を server env から直接渡したため、production の registry からの intent 採用と state handoff は実測していない。

同日の installed entrypoint 調査では `~/.nodebrew/current/bin/codex` は `codex.js` への symlink で、script は `#!/usr/bin/env node` から platform-native Codex child を spawn する形だった。
`~/.local/bin/claude` は versioned Mach-O executable への symlink だった。
これは source inspection であり real agent の process chain 実測ではないが、`exec.LookPath` が返す lexical path と live foreground executable の byte 完全一致を共通 matcher にできないことを示す。

current Go 1.26.5 darwin-arm64 の `syscall` と installed `x/sys/unix` surface には Darwin 用 `fexecve` / `execveat` がなく、verified FD を直接実行する経路は確認できなかった。
`/private/tmp` の 0500 copy に `UF_IMMUTABLE` を設定した追試では rename と chmod が `Operation not permitted` になり、flag 解除後にだけ変更できた。
同じ copy の実行は current sandbox で exit 137 になったため、この追試は seal の mutation 防止だけを示し、bundled provider の起動成立を証明しない。
（この immutable-copy 追試は launch bundle 撤廃後は履歴であり、seal は proof-grade tier の再導入候補に残る。）
#528 は real Claude / Codex の launcher 起動と process 照合を実機で検証してから各 provider を admit する。

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

## owned session lifecycle

### server lifecycle と ownership

`herdr server` は foreground process として起動でき、fanout supervisor の直接の子として監視できる。
検証では session directory を 0700、server socket と client socket を 0600 とし、log は 0700 directory 配下の 0644 file に分離した。
server log は隔離した `XDG_CONFIG_HOME/herdr/sessions/<session>/herdr-server.log` に置かれた。
0644 は実測値であり、production 契約ではない。
production supervisor は 0600、ACL-free の log file を使い、workload env の name / value、env file の path、launcher token を log へ出さない。
同じ socket path への二重起動は `herdr server is already running` で終了した。
同じ path に non-herdr の Unix listener がいる場合も同じ error で終了し、protocol、version、owner は検査しなかった。
`status --json` の `detached_server_daemon:true` は capability 表示であり、明示的に起動した server 自体は daemonize しなかった。

実測した mode 値だけでは inherited ACL を検査していないため、別 UID の排除を証明しない。
実装は後述の private namespace gate が成功した場合だけ、この permission 境界を別 UID の排除に使う。
herdr が起動した agent は同じ UID で動き、`HERDR_SOCKET_PATH` を継承するため、socket が分かれば `server.stop`、`plugin.*`、`worktree.remove` を発行できる。
同じ UID の process は外部 `owner.json` の stable bytes と registry の PID / start token を変更できるため、status、schema、snapshot の応答は marker nonce または session generation に束縛されない。
server 停止後に同じ path の server へ置換する ABA も原子的には検出できない。

owned runtime directory の `owner.json` は協調する fanout process 間の ownership、封じ込め、crash recovery、誤操作防止に使う。
marker は schema ID `fanout.herdr-owner.v1` の JSON とし、decoder は missing / unknown field を拒否する。
field は git common directory の path / device / inode、owner nonce、session 名、runtime directory、server / client socket path、pinned Herdr binary の path / SHA-256 / version、supervisor の PID / start token、owned XDG 四 path、config path とし、PR #572 の実装形を正とする。
marker は owned runtime directory の検査後に exclusive create で書き、supervisor lease（owner nonce / PID / start token）を別 file で保持する。
再利用は marker、lease、live supervisor process の照合が一致した場合だけ許可する。
不一致、foreign、または検証不能なら fail closed にして server を停止しない。
この marker を mutation authority として扱わない。

### 未確定事項

旧正典が #526 / #527 の deferral とした exact CAS shape、canonical golden bytes、registry codec、behavior profile fixture、production server log protocol、restart quiescence proof は、2026-07-24 の version gate 決定と 2026-07-27 の簡素化決定で解消または撤廃した。
#526 は version gate と owned session lifecycle を PR #572 で実装済みであり、marker / lease / layout の具体形はその実装を正とする。
撤廃した機構は「後続 issue への契約」の再評価条件表に proof-grade tier の再導入候補として残す。

### 共有 registry と lifecycle 契約

Herdr control state は physical canonical git common directory 配下の `fanout/herdr-control.json` を唯一の正典とし、同じ directory の `herdr-control.json.lock` で直列化する。
lock、atomic replace（temporary file への書き込みと rename）、fail-fast の水準は tmux backend の `.fanout/state.json`（`internal/infra/state` + `internal/infra/atomicfs`）と同じにし、実装にない durability 要件を追加しない。
**private namespace gate** は fanout-owned path（`fanout` control directory、owned runtime root、XDG 四 root、config、marker、socket parent）に適用する簡素な owner-only 検査であり、owner UID、exact mode、空の extended ACL、symlink 不在を照合する。
検査は対象 leaf に加えてその祖先（`fanout` control directory は physical common directory まで、owned runtime root は runtime base の parent まで）にも適用し、別 UID が書込み可能な祖先（sticky bit の rename 保護がない world-writable directory など）があれば fail closed にする（祖先の rename / 差し替えによる検査後の registry / lock 置換の防止）。
`fanout` directory は 0700、registry と lock は 0600 とし、private namespace gate または physical common-directory identity の照合に失敗した場合は Herdr backend を開始しない。
pre-existing object の ACL / mode は自動修復せず fail closed にする。
registry は schema version、common-directory identity、console / coordinator / child の final row、最小意図記録（intent 行）、telemetry routing を保持する。
intent 行は owner process の PID / start token と絶対 expiry を持ち、crash recovery は owner process の不在を確認した場合だけ開始する。
live owner の intent は expiry の超過にかかわらず in-progress として明確な error で扱い、削除も mutation の再発行もしない（並行 invocation が実行中 operation を crash と誤認する二重発行の防止。expiry は launcher / token の deadline であり recovery の許可条件ではない）。
registry / intent / final row は raw workload env value を保持しない。
mutation は state lock 下で snapshot を読み、identity を照合してから同じ lock 下の atomic save で確定する。
state lock は一回の snapshot / state save に限り、Herdr CLI call、polling、sleep、外部応答待ちをまたいで保持しない。
launcher の lock-free read は rename 前後どちらかの完全な JSON だけを受理し、decode failure は intent 不在として待たず fail closed にする。
各 row は起動元の physical worktree root と task provenance を保持するが、mutable な Herdr row を各 checkout の `.fanout/state.json` へ複製しない。
registry-backed peer 移行までは state-dependent な team 共通経路へ Herdr row を渡さず、`--team` の拒否を registry save または SQLite open より先に確定する。
status、lifecycle、backend stickiness、session view は worktree-local tmux state と共有 Herdr registry を backend ごとに読み分けて集約する。
この文書でいう Herdr の `state lock` と `state save` は、以後この共有 registry の lock と atomic replace を指す。

共有 registry の row key は repo-global な kind-tagged tuple とし、tmux backend の state key（issue 番号、`plan:<slug>` + `TaskID`）と同じ粒度に保つ。
positive GitHub issue は `(parent, issue 番号)`、plan task は `(plan slug, task ID)`、synthetic / manual launch は `(operation kind, launch nonce)`、coordinator は `(親 identity, launch nonce)` とする。
coordinator は親ごとの singleton とし、同じ親の重複を拒否する。
plan spec snapshot の SHA-256 束縛は行わない（proof-grade tier の再導入候補）。
`launch_nonce` は最初の mutation 前に生成して intent 行に保存し、crash 後の再実行で再生成しない。
issue と plan task は同じ key の row / intent を idempotency hit とし、synthetic launch は invocation ごとに別 key、crash 後の再実行だけが保存済み nonce の同じ key を使う。
`ParentRef`、plan slug、`TaskID`、負の `IssueNum` は display / CLI selector provenance として保持する。
status、close、merge、cleanup は保存済み key をそのまま routing し、selector が 0 件または複数件なら fail closed にして cwd、slug、負番号だけから mutable row を選ばない。

config と plugin registry は全 XDG directory の差し替えで default state から隔離できる。
supervisor の全 Herdr CLI call と server child は SHA-256 で pin した owned copy の Herdr binary を使う。
owned config は `[update] manifest_check = false` を固定して background remote manifest check を無効にし、#528 以降は `[terminal] default_shell` を絶対 fanout launcher path、`shell_mode` を `non_login` に固定する。
server env の `FANOUT_HERDR_PANE_LAUNCHER=1` は no-arg TUI より先に non-shell launcher mode へ dispatch する。
server env の `FANOUT_HERDR_LAUNCHER_MAX_WAIT_MS` も `300000` に固定する。
#427 の hook emitter は絶対 fanout path を使い、launcher control env は operation child env から除く。
launcher は checkout 内 file を実行せず、Herdr が別 root process を起動した場合は launch 前に fail closed にする。

fresh session の bootstrap は PR #572 の実装を正とする。
supervisor は owned runtime layout（0700 の runtime directory、owned XDG 四 root、config、socket path）を private namespace gate で検査し、admitted Herdr binary を SHA-256 で pin した owned copy に固定する。
owner marker を exclusive create し、foreground `herdr server` child を spawn して status の session / socket 一致を確認してから操作を解禁する。
bootstrap 失敗時は起動した supervisor / server を停止して fail closed にし、marker / lease を残さない。
crash 後の再実行は marker と supervisor lease を読み、live supervisor の照合が一致すれば再利用する。
supervisor が不在で marker / lease が残る場合は PR #572 の実装どおり fail closed にし、正常に retire された layout だけを検証して作り直す。
marker / lease / socket の不一致、foreign resource、検証不能は自動採用も自動削除もせず fail closed にする。

server loss 後の restart は herdr 固有の最小追加対処とする（簡素化方針の基準 2。未実装で、実装 owner は #530）。
restart は明示操作だけが開始し、restart の intent 行を state save してから、saved supervisor / server process と socket の不在と旧 marker の一致を照合する。
restart intent 行がある間は shutdown と同じく、restart 自身と read-only 操作以外の mutation を明確な error で拒否する。
照合成功後に旧 marker / lease を削除し、fresh bootstrap と同じ手順（新しい owner nonce での marker / lease の exclusive create、単一 spawn、status の session / socket 検査）で作り直す。
照合失敗（旧 process の残存、marker 不一致）では削除に進まず、intent 行と旧 marker を残して fail closed にする。
旧 marker / lease の削除後に crash した場合は marker 不在と intent 行が残り、spawn の結果不明では新しい owner nonce の marker / lease と intent 行が残る。
どちらも再実行時に intent 行と現存する marker 世代の存在確認から分類し、新 marker の supervisor が live で status 検査を満たす場合だけ完了へ進み、それ以外は fail closed のまま資源に触らない。
restart intent 行の削除は、version gate と status 検査の再実行、旧 `terminal_id` の direct-launch row の `stale` 化を確定した同じ state save で行う（削除が先行すると re-gate 前に別 worktree の mutation が再開する）。
自動 resume は行わない。

通常 shutdown は明示操作だけとし（未実装で、実装 owner は #530）、state lock 下で active row / intent と foreign resource の不在を確認し、同じ save で shutdown の intent 行を保存してから server を停止し、marker を削除する。
shutdown intent 行がある間、別 worktree を含む新しい launch / mutation は明確な error で拒否する（空状態確認と server 停止の間に intent が入り込む race の fence）。
停止と marker 削除を確認した save で shutdown intent 行を削除する。
response loss で停止の発生を証明できない場合は shutdown intent 行と marker を残して fail closed にし、再実行時の存在確認（process / socket の不在）で完了を確定する。

0.7.5 の plugin registry は session 単位ではなく、同じ `XDG_CONFIG_HOME` を使う全 session で共有される per-user global state になった。
実測では session を変えても link 済み plugin が見え、同じ `HOME` でも `XDG_CONFIG_HOME` を変えると空になった。
owned XDG を repo session ごとに分ける現行方針は global registry も隔離し、cold restart 後も同じ owned XDG の registry を復元した。
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

### 0.7.4 core matrix（履歴）

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

### 0.7.5 core matrix

0.7.5 では workspace-level agent command が pane-targeted command へ変わったため、worktree lifecycle と live-agent lifecycle を分けて再検証した。

| 対象 | 0.7.5 実測 | wave 2 判定 |
|---|---|---|
| `workspace create --cwd --env` | root pane の cwd と env に反映された | coordinator bootstrap に使う |
| `worktree create` | child workspace と root pane を作り、branch、path、base、provenance を返した | 0.7.4 契約から非回帰 |
| child root pane の env | source workspace の明示 env を継承しなかった | launcher が launch nonce に束縛した one-shot 0600 env file を読み、workload env を process へ一回だけ明示する |
| `worktree open` | cold restart 後も同じ checkout を `already_open:true` で再採用した | exact ownership を満たす recovery に使う |
| `worktree remove` | clean checkout を削除し、local branch を残した | cleanup 経路が identity 照合後に使う。branch 削除は fanout の compare-and-delete が担う |
| plain shell への `pane run` | 絶対 executable、空白を含む env / argv、exact cwd を happy path では保持した | readiness / empty-input precondition がないため自動 launch には使わない |
| owned `terminal.default_shell` | launcher が exact cwd、pane / workspace ID、nonce を受け、readiness marker 後の exact token から env / argv を保持した fake process を起動した | non-shell launcher protocol を agent と Plan Mode controller の launch vehicle に使う |
| `pane process-info` | argv0 / argv / cwd を返した | OS process 情報の実 executable / ancestry / process group と合わせて exact identity を検査する |
| cold restart | public workspace / pane ID と layout を維持し、全 `terminal_id` を更新した | public ID だけでは再束縛しない |
| cleanup | workspace / worktree cleanup 後の snapshot と unlink 後の plugin list は空だった | production cleanup は同じ CLI surface を identity 照合と存在確認を通して使う |

再起動前の `w1:p1`、`w2:p1`、`w3:p1` から `w3:p4` は再起動後も同じ public ID だった。
対応する六つの `terminal_id` はすべて更新され、manual / fake agent の name と record は失われた。
`worktree open` は同じ `w3` と checkout provenance を返したが、0.7.5 direct-launch agent の resume は証明しない。

### topology、restart、teardown

wave 2 の実装では session を per-repo とする。
linked worktree は canonical git common directory を共有し、独立 clone は full common-directory identity の hash で名前を分離する。
marker の full identity が一致しなければ hash が一致しても fail closed にする。
同じ common directory から起動した supervisor、launcher、emitter、cleanup はすべて同じ `herdr-control.json` と lock を使い、呼び出し元 checkout の `.fanout/state.json` を Herdr intent の探索に使わない。
これにより linked worktree A が作った console / intent / row を linked worktree B の attach と shutdown も同じ順序で観測する。
registry-backed peer 移行後の nudge は、同じ shared registry から current recipient row を解決する場合だけこの性質を持つ。

repo root に console workspace を一つ置き、実際の親ごとに repo-root cwd の coordinator workspace を一つ置く。
coordinator の state row は `@manual` の負番号を display address として維持するが、shared registry の typed key には使わず、backend stickiness と lifecycle provenance は実際の親へ帰属させる。
child は sibling workspace とし、workspace label で親を識別する。
create は `--no-focus` とし、明示的な TUI launch だけが返却 ID を focus する。
focused child の close 後は exact live identity を再照合した同じ親の coordinator、存在しなければ idle な console shell を focus し、どちらも条件を満たさなければ focus を変更しない。

global `terminal.default_shell` は全 workspace の root process に適用されるため、console も launcher の明示 operation とする。
console intent / row の key は `(canonical git common directory, operation:console)` とし、issue / task row、backend stickiness、nudge roster に含めない。
console shell は user config、未指定なら fanout 起動時の `SHELL` から解決した絶対 path を使う。
no-arg TUI の attach 準備は、workspace mutation 前に console の intent 行（row key、launch nonce、cwd、user shell の絶対 path、env file path、owner の PID / start token、`total_timeout` と絶対 expiry、発行時刻）を state save し、launch nonce から workspace label と `FANOUT_READY:<launch-nonce>` / `FANOUT_EXEC:<launch-nonce>` を導出する。
そのうえで `workspace create --cwd <repo-root> --label <launch-nonce> --no-focus` を一回発行し、応答の workspace ID、root PaneRef / `terminal_id` / cwd を照合して intent 行へ記録する。
応答喪失または crash 後の再実行は存在確認で分類する。intent の nonce と一致する label の workspace が一つだけ存在して cwd が一致すれば採用して続行する。続行は現状から分岐し、root pane の foreground が launcher なら fresh marker の再観測後に readiness / token から、intent の user shell が既に動いているなら process 照合の finalization から再開する。workspace が不在で create request の非発行を証明できる場合だけ intent 行を消して作り直し、それ以外は `manual_cleanup_required` にする。
launcher marker と root identity を照合した後だけ exact token を一回発行し、launcher は intent の user shell を interactive child として起動する。
parent は `pane process-info` と OS process 情報で shell の argv / cwd / ancestry を照合して final console row を確定し、同じ state save で intent 行を削除する。
console は agent detection、rename、emitter、initial operation token 以外の automatic `pane run`、`agent prompt`、nudge の対象にせず、ユーザーが明示的に focus した後の入力だけを受ける。
attach 準備は exact live console row を再利用し、console が存在しない場合だけ作成する。
explicit attach で owned stale console を見つけた場合は `workspace close` / `pane close` で閉じて row を削除してから作り直し、cleanup を確認できない場合は fail closed にする。
fanout-owned session は console / coordinator / child の intent-backed workspace だけを受け入れ、TUI または外部 CLI が intent なしで作る generic workspace の root launcher は shell fallback を起動せず deadline で終了する。
ユーザーの generic Herdr workspace は別 session のまま扱い、fanout はその config / default shell を変更しない。

server process は per-repo supervisor が所有する foreground `herdr server` child とし、attached console process の子にはしない。
これにより console の detach または終了後も server を存続させる。
wave 2 は owner marker、socket、version gate を満たす owned server の bootstrap を実行する。
server loss 後の restart は「owned session lifecycle」の最小追加対処に従う。
0.7.5 direct-launch row は provider にかかわらず `terminal_id` が変われば `stale` とし、自動 resume しない。
最後の child close では server を止めず、active row / intent と foreign resource がない場合の明示的な repo-session shutdown だけを teardown とする。
明示 shutdown は console shell / workspace を `workspace close` / `pane close` で先に閉じ、console row を含む全 active row / intent の不在を再観測してから server を停止する。

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
0.7.5 direct-launch row は `terminal_id` が変わった時点で、保存済みの `{source, agent, kind, value}` と完全一致する一意な `agent_session` があっても `stale` とし、新しい terminal へ再対応付けしない。
`agent_session` ref は current terminal 上の会話識別の補助照合と #532 の再実測入力として保存するが、#532 の実測完了と別 PR による current contract の改訂までは resume または再束縛の実装条件に使わない。
0.7.4 の attach 前に同じ ref が resume placeholder として現れた履歴から、ref は process の生存証拠にせず、同じ `terminal_id` の live row でも provider 固有の process identity を別に検査する。
fanout-owned marker / nonce は owned lifecycle の reconciliation token として使うが、同一 UID に対する mutation authority の証明には使わない。

## 操作面

次の表は実測した CLI surface を示す。
wave 2 の後続実装は次の targeted read と mutation を、intent 行と送信直前再照合を通して呼べる。

| 実測操作 | CLI | 結果 | 制約 |
|---|---|---|---|
| launch | `workspace create --cwd ... --no-focus` | `workspace_created` | intent 行を先に保存し、console / root coordinator 作成に使う |
| launch | `worktree create --workspace ... --branch ... --base ... --path ... --label <nonce> --no-focus --json` | `worktree_created` | workspace label の nonce と branch / path / `HEAD` の事後条件を照合する |
| launch / recover | `worktree open --workspace ... --path ... --label <nonce> --no-focus --json` | `worktree_opened` | state / intent が所有する既存 checkout だけを採用する |
| launcher readiness | `pane wait-output <root-pane> --match <operation-bound-marker> --timeout ...` | `output_matched` | exact terminal と launcher process を同時に照合し、一回だけの早すぎる marker に依存しない |
| launch | `pane run <operation-root-pane> <operation-bound-token>` | `ok` | fanout-owned non-shell launcher だけを target にし、launcher が intent の console shell または agent env / executable / argv を直接起動する |
| launch identity | `agent rename <pane-id> <name>` | AgentInfo | agent 検出後に pane ID を deterministic name へ束縛する |
| live-agent probe | `agent start <name> --kind <kind> --pane <pane-id> -- <args...>` | AgentInfo と argv | binary pin ができないため自動 launch には使わない |
| list | `api snapshot` | `session_snapshot` | `session.snapshot` の CLI wrapper。identity / status 観測に使う |
| list | `worktree list --workspace ... --json`、`agent list` | `worktree_list`、`agent_list` | `worktree list` は基準 workspace を明示する |
| content read | `pane read`、`agent read` | text または `pane_read` | target を直前・直後に再照合し、不一致なら content を破棄する |
| structured read | `pane get` | `pane_info` | exact PaneRef と worktree provenance の identity 検査に使う |
| structured read | `pane process-info --pane ...` | `pane_process_info` | launcher と live agent の process identity 検査に使う |
| focus | `agent focus <name>`、`workspace focus <id>` | 対象 agent または workspace を focus | TUI の明示操作だけが target の直前再照合後に発行する |
| send | `agent prompt <pane-id> <text>` | `agent_prompted` | registry-backed peer gate、`shouldNudge`、送信直前再照合を通した no-wait 自動 nudge に使う |
| close | `worktree remove --workspace ... --json` | `worktree_removed` | cleanup と launch rollback が identity 照合後に発行する。dirty は明示確認なしに force しない |
| close | `workspace close`、`pane close` | `ok` | checkout は消えない。console / coordinator close と、`worktree remove` 後に workspace だけが残った場合の整理に使う |
| wait | `agent wait <name> --until ... --timeout ...` | `wait_matched` と event | current state が一致すれば即時に成功し、明示的な settled-state workflow に使う |
| wait | `pane wait-output <pane> --match ... --timeout ...` | `output_matched` | current buffer に一致済みでも即時に成功し、nonce 付き launcher readiness と controller bootstrap の出力確認に使う |

generic pane の exact focus が必要になった場合、Socket API の `pane.focus` を同じ直前再照合を持つ追加候補にする。

`worktree remove`、`workspace close`、`pane close` は snapshot 照合と mutation が別 CLI 接続になり、照合済み nonce、`terminal_id`、session epoch を request の precondition として渡す手段が 0.7.5 にない。
同名 session の再作成で session 名、socket、public ID は再利用されるため、照合と mutation の間の TOCTOU は CLI では閉じられない。
この TOCTOU は tmux の `ListLive` 照合から `kill-pane` / `git worktree remove` までの race と同種であり、tmux-parity tier の受容済み残余リスクとする。
resource generation を mutation と原子的に条件化する server 側 primitive は proof-grade tier の格上げ条件として保持する。
response loss または mutation の有無が不明な場合は blind retry せず、再実行時の存在確認で採用または fail closed にする。

herdr backend は root coordinator の intent 行を保存してから `workspace create` を実行する。
root coordinator の `workspace create` も副作用を持つ launch 操作として intent 行の対象にし、console と同じ「nonce label の存在確認 → 採用 or fail closed」で応答喪失を処理する。
coordinator root の launcher は worktree root と同じ readiness / token / agent detection 契約を通してから通常 state へ確定する。
herdr pane 内から fanout を起動する通常ケースでは同じ root cwd のユーザー workspace が既にあるため、root cwd / provenance の一致だけでは coordinator を識別せず、label nonce で識別する。

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

fanout は branch と base の対応を row に保存する。
row は full branch ref、deterministic checkout path、base branch の表示名（tmux backend の `worktree.BuildPlan.BaseBranch` と同じ public value）、解決済み base commit SHA を持つ。
base selector は launch 時に一回だけ commit SHA へ解決し、ambiguous ref、non-commit、解釈不能な selector は mutation 前に拒否する。
session view は解決済み base を worktree 比較へ渡し、ref が解決不能なら保存済み base SHA を使って無言で `HEAD` へ落とさない。
lifecycle hook は base branch 名を既存 state の `BaseBranch` と `FANOUT_BASE_BRANCH` へ渡し、tmux backend と同じ public value を保つ。
auto-PR の base は tmux backend と同じ規則で決め、registry の base field を書き換えない。
既存 local branch は tmux backend の `git worktree add` と同じく採用し、`--base` は fresh branch だけに渡す。

child の launch と cleanup の crash 対処は次の一つの形に従う。

```mermaid
flowchart TD
  A["state lock 下で row / intent / branch / path を検査"] --> B["intent 行を保存"]
  B --> C["mutation を一回発行(branch 作成 / worktree create / remove など)"]
  C -->|応答成功| D["事後条件を照合して operation を確定(launch は final row 確定、cleanup は row 削除)し、同じ save で intent 行を削除"]
  C -->|応答喪失 / crash| E["再実行時の存在確認"]
  E -->|create 系: nonce / identity が一致する資源が一意に存在| R["採用して operation を続行"]
  E -->|remove 系: 対象資源が不在| D
  E -->|remove 系: identity 一致のまま残存| B
  E -->|mutation 非発生を証明できる(local git mutation など)| F["intent 行を削除して最初から"]
  E -->|判定不能 / 部分一致| G["fail-closed manual_cleanup_required"]
  R --> D
```

### safety gate

source checkout に untracked file があっても `worktree create` は成功した。
local `main` と `origin/main` を 1 commit ずつ diverge させた場合も、`--base main` は local の `6af20aa`、`--base origin/main` は remote-tracking ref の `b05fa51` をそのまま使った。
herdr は fetch、dirty gate、divergence gate を実行しない。

herdr backend は tmux-parity trust、owned session、version gate を確認してから child launch に入る。
以下は wave 2 の自動 launch の契約であり、各条項は tmux 水準（基準 1）か応答喪失窓への最小追加対処（基準 2）のどちらかである。

- base selector を解決済み commit SHA へ一回だけ解決し、source checkout の dirty と divergence を検査する既存の fail-closed 契約を保つ（tmux backend の refresh / safety gate と同一）。
- state lock 下で row / intent を検査して分類する。final row は idempotency hit として skip し、intent 行だけが残る launch は後述の存在確認 recovery へ進む。row / intent が所有しない既存 checkout / branch は tmux backend の launch と同じく fail closed にする（`.fanout/worktrees/<slug>` の migration fallback は cleanup 系 action に限り、launch では適用しない）。
- 最初の mutation（fresh branch の ref create を含む）より前に intent 行（row key、slug、branch、path、workspace label 用 launch nonce、branch の事前存在、agent の絶対 executable / 正規化済み argv / env file path、owner の PID / start token、`total_timeout` と絶対 expiry、発行時刻）を state save する。
- fresh branch は fanout が atomic ref create（old OID を空とする `update-ref` 相当）で base SHA に作る。既存なら失敗し、tmux backend の `git worktree add -b` と同じ fail-fast にする。
- 既存 local branch は tmux backend と同じく採用する。`--base` は fresh branch だけに渡し、採用時は事前に記録した branch tip を事後条件に使う。
- branch の準備後に `worktree create --branch --base --path --label <nonce> --no-focus` を一回発行する。
- 応答成功時は workspace label と intent nonce の一致、branch、path、`HEAD` の事後条件を照合し、workspace ID / root PaneRef / `terminal_id` を intent 行へ記録して launcher readiness へ進む。
- intent 行は launch finalization（agent 検出 / rename / process 照合の完了）まで保持し、final row の確定と同じ state save で削除する。launcher はこの intent から実行内容を読む。
- 構造化 error が workspace / checkout の非作成を示す失敗では、今回 fanout が作った branch だけを compare-and-delete で削除して fail-fast する（tmux backend の `git worktree add` 失敗時の branch 削除と同じ）。response loss では branch を削除しない。
- compare-and-delete は fanout の branch 削除手順であり、照合済み tip（launch rollback は記録済み base SHA、cleanup は merge 確認後の current tip）との一致と、同じ full branch ref を checkout する linked worktree の不在を確認してから削除する。cleanup の tip 照合は current tip が merged PR の head と一致するか merge 先 branch の ancestor であることも検証し、未マージ commit を持つ branch は残す。checkout を検査しない `update-ref -d` 単独では行わず、checked-out branch を拒否する tmux backend の `git branch -D` と同じ guard を保つ。
- `worktree create` 成功後の launch 失敗（launcher timeout / exit、token 失敗、agent 検出失敗）は tmux backend の `failCleanup` と同水準で rollback する。rollback は launch cycle と別の operation として新しい intent 行と budget で実行し、保存済み label nonce / branch / path / `terminal_id` を再照合してから force なしの `worktree remove` を一回発行し、workspace / checkout の不在を確認できた場合だけ自作 branch を記録済み base SHA の compare-and-delete で削除して両 intent 行を消す。照合不一致と dirty 拒否では資源を残して `manual_cleanup_required` にする（tmux も close を確認できない場合は worktree を温存する）。remove の response loss は再実行時の存在確認で分類し、workspace / checkout の不在を確認できれば rollback 完了として branch 削除と intent 整理へ進み、identity 一致のまま残存していれば新しい rollback として再発行し、判定できない場合だけ `manual_cleanup_required` にする。
- 応答喪失または crash 後の再実行は存在確認で分類する。recovery は intent の owner process の不在を確認した場合だけ開始し、live owner の intent は expiry の超過にかかわらず in-progress として明確な error を返す。intent の nonce と一致する label の workspace / checkout が一意に存在して branch / path / `HEAD` が一致すれば採用して launch を続行する。続行は現状から分岐し、pane の foreground が launcher なら fresh marker を新たに観測して launcher が未 token であることを確認したうえで readiness / token から、intent に一致する child process chain が既に動いているなら agent 検出 / rename / process 照合の finalization から再開する。branch だけが存在する場合（workspace / checkout 不在）は、intent の事前存在が false で tip が記録済み base SHA と一致するなら自作 branch として `worktree create` から続行する。
- socket mutation（`worktree create` など）の再発行と intent 削除は、request の非発行を証明できる場合に限る。workspace / checkout が不在でも request が server 内で進行中の可能性を排除できないため、非発行を証明できない場合は `manual_cleanup_required` にする（採用 or fail closed が基準 2 の全部であり、暗黙の作り直しをしない）。branch ref create など local git mutation は構造化結果で非発生を証明できるため、intent 行を削除して最初からやり直せる。
- 既存 checkout への `worktree open` は同じ intent の launch recovery と cleanup 前の再検証に限り、`already_open:true` は同じ workspace ID / label が row / intent に束縛済みの場合だけ受理する。
- launch cycle は 3 秒以上 300 秒以下の `total_timeout`（既定 300 秒）を最初の mutation 前に確定し、retry で延長しない。
- deterministic agent name は `fanout-` + SHA-256（canonical git common directory、row key、launch nonce の length-prefix 連結）の先頭 24 lowercase hex とし、0.7.5 の `[a-z][a-z0-9_-]{0,31}` を満たす。同名 agent が別 pane にある場合は fail closed にする。
- 操作直前の共通再照合として、root pane の `cwd`、`terminal_id`、`HERDR_PANE_ID` / `HERDR_WORKSPACE_ID`、foreground launcher process を intent と照合してから token を発行する。
- 失敗した child launch の後は次の child に進まず停止する（tmux backend の `executePlan` fail-fast と同じ）。rollback は前項の identity 照合と不在確認を満たした場合だけ行い、それ以外の削除は cleanup 経路が行う。
- 照合と mutation の間の TOCTOU（同名 session の差し替え）は tmux と同種の受容済み残余リスクとする。
- plugin registry は owned XDG の registry と config を launch 前に照合し、予期しない setup hook があれば fail closed にする。この preflight は協調プロセス前提の操作 gate であり atomic proof ではない。

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

state row 起点の cleanup でも、0.7.5 CLI は nonce、`terminal_id`、session epoch を `worktree remove` / `workspace close` / `pane close` の precondition として受け取らない。
fanout が state、snapshot、checkout git dir を照合してから別接続で mutation を発行するまでに、同名 session と public ID が別資源へ再利用され得る。
この TOCTOU は tmux cleanup の `ListLive` 照合から `kill-pane` / `git worktree remove` までの race と同種の残余リスクとして tmux-parity tier で受容する。
tracked / untracked / ignored subtree generation を remove と原子的に条件化する server-side conditional remove、または kernel-enforced write-exclusion fence は proof-grade tier の格上げ条件として保持する。

cleanup の契約は次のとおりとする（#531 が実装する）。

- state lock 下で保存済み row の workspace ID / label nonce、branch、path、`terminal_id` を現在値と照合し、不一致、非所有、または照合不能なら mutation せず fail closed にする。
- dirty checkout は明示確認なしに force しない。確認後の force remove でも branch は herdr に削除させない。
- 照合成功後は intent 行を保存してから `worktree remove` を発行する。実測どおり remove は checkout と child workspace の両方を削除するため、応答成功後に checkout / workspace の不在を確認して row と intent 行を削除し、workspace だけが残る場合に限り `workspace close` で整理する。
- `workspace close` が先行して checkout が残った系は、owned registry の setup hook preflight を通過している場合に限り、`worktree open` で削除用 workspace を再登録してから同じ identity 照合を経て `worktree remove` を発行する。
- branch 削除は fanout の compare-and-delete（「safety gate」節で定義。tip 照合と checked-out worktree 不在の確認後の削除）だけが行う。cleanup 経路は merge 確認後の current tip、launch rollback は記録済み base SHA を照合する。
- 応答喪失または crash 後の再実行は存在確認で分類する。checkout / workspace が不在なら削除完了として row を整理し、identity が一致したまま残存していれば新しい cleanup として再発行し、判定できない場合は `manual_cleanup_required` にする。
- cleanup 済み row の再 launch は fresh launch として扱い、既存 branch があれば tmux backend と同じ規則で採用する。

`workspace close` を先に実行すると checkout は残る。
続く `worktree remove --workspace <closed-id>` は `workspace_not_found` になる。
0.7.3 の `worktree open` が `worktree.opened` setup hook を発火した実測から、削除用の再登録は owned registry の setup hook preflight を通過している場合に限る。

branch lineage、cleaned tombstone、explicit continue、tombstone forget の機構は 2026-07-27 の簡素化で撤廃した（proof-grade tier の再導入候補）。
cleanup 後の branch 継続は「既存 branch を採用する fresh launch」（tmux backend と同じ）で行う。
branch の compare-and-delete が失敗した場合（tip が動いた、tip が merged PR head とも merge 先の ancestor とも一致しない、同じ branch を checkout する worktree が残る、確認不能）は branch を残して報告し、削除はユーザーの Git 操作に委ねる。

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

### 0.7.5 live-agent と exact launch

0.7.5 の live-agent 起動面は `agent start <name> --kind <kind> --pane <pane-id> -- <args...>` である。
`AgentStartParams` は `name`、`kind`、`pane_id` を必須とし、`args` と `timeout_ms` だけを optional にする。
0.7.4 の `argv`、`cwd`、`env`、`workspace_id`、`tab_id`、`split`、`focus` は廃止された。

実機の `agent start probe_codex_b --kind codex --pane w3:p2 -- -c 'model_reasoning_effort = "low"' --no-alt-screen` は成功した。
応答は AgentInfo の workspace / tab / pane / `terminal_id` / name / status / `interactive_ready` / `launch_pending` と、canonical `codex` に続く argv を返した。
指定した `model_reasoning_effort = "low"` は空白を含む一要素のまま応答と `process-info` で保持された。

`agent start` は kind ごとの canonical executable を bare name で選び、`--` 以降をその executable の args に追加する。
`server agent-manifests --json` は manifest の source、version、更新情報を返すが、解決済み executable path または launch argv を返さない。
検証 server は起動時に remote manifest を取得し、owned XDG の cache へ保存した。
remote manifest は restart なしで in-memory detection rule を更新できるため、executable pin には使わない。
manifest は herdr 側の provider detection 入力に留め、fanout は owned config の `[update] manifest_check = false` で background remote check を無効にする。
旧正典の behavior profile / detection fixture / `manifest_set_digest` / no-refresh proof は 2026-07-24 の version gate 決定で撤廃し、canonical fixture 束縛は proof-grade tier の再導入候補とする。
PATH を一時 directory で prefix しても pane の shell startup が PATH を変更し、実機では別の user-installed `codex` が選ばれた。
したがって fanout が解決した絶対 executable を `agent start` または manifest 検査で pin することはできない。
`agent start --kind codex --pane ... -- /abs/fanout __codex-plan-tui` も `/abs/fanout` を Codex の引数にしたため、Plan Mode controller の起動には使えない。
一方、root pane へ absolute `fanout-go __codex-plan-tui --help` を `pane run` すると、`pane wait-output` が controller の usage を検出した。
これは clean prompt の happy path であり、plain shell の readiness / empty-input proof にはならない。
Plan Mode は後述する owned launcher protocol を使い、controller が起動する Codex child と restore の end-to-end は #554 の実装検証に残す。

#### entrypoint 解決と provider process 照合

wave 2 は content-addressed launch bundle を作らない（2026-07-27 の簡素化決定。sealed bundle と verified-FD spawn は proof-grade tier の再導入候補）。
launcher は fanout が launch 時に解決した絶対 executable path、正規化済み argv、cwd、workload env を直接起動する。
tmux backend が pane 内で agent command を直接起動するのと同じ水準であり、source installation が launch 後に更新された場合の追加保全はしない。
`exec.LookPath` の lexical path と live foreground executable は symlink chain と wrapper のため byte 一致しない実測があるため（前述の installed entrypoint 調査）、起動後の照合は path の byte 一致ではなく次で行う。
`pane process-info` と OS process 情報から launcher の child chain を取り、foreground process の argv / cwd が intent の記録と一致し、herdr の provider detection が expected provider を一つだけ返すことを要求する。
欠落、重複、別 provider、identity 不一致は fail closed にする。
`entrypoint_spec`（source provenance と symlink chain の監査記録）は任意記録とし、実行 authority には使わない。
#528 は supported provider ごとに real process chain の fixture / live test を追加してから自動 launch を有効にする。

name は session 全体で一意であり、0.7.5 は `[a-z][a-z0-9_-]{0,31}` を要求する。
先頭が大文字または数字、`.` を含む名前、33 byte の名前は `invalid_agent_name` で失敗し、32 byte の名前は成功した。
同名を再利用すると `agent_name_taken` になった。
wave 2 の 31 byte deterministic name は前節の hash 契約に従う。

自動 launch は `worktree create` が返す root pane を再利用するが、root process は owned config で fanout-owned non-shell launcher に固定する。
child root pane の cwd は checkout path と一致したが、source workspace の `workspace create --env` で設定した値は child root pane に届かなかった。
`pane split --cwd <checkout> --env 'FANOUT_AGENT_PROBE=value with spaces'` では値が届いたものの、shell startup による PATH の変更は同じだった。
この pane を target にした `agent start` の wrapper probe は cwd に child checkout、env に `FANOUT_AGENT_PROBE=value with spaces`、argv に空白を含む指定値を記録した。
したがって 0.7.5 の `agent start` へ cwd / env は target pane の継承値として届くが、request ごとに指定または検証する field はない。
さらに worktree root pane は source workspace の明示 env を継承しないため、この到達経路だけでは fanout の exact workload env 契約を満たさない。

実機では root pane へ次の形を `pane run` し、fake executable が exact env、空白を含む argv、child checkout cwd を受け取った。

```console
pane run <root-pane> '/usr/bin/env "FANOUT_AGENT_PROBE=value with spaces" "/absolute/path/to/codex" "arg with spaces" --flag'
```

Herdr はこの process を Codex として検出し、`process-info` は absolute argv0、`["arg with spaces","--flag"]`、exact cwd を返した。
OS process 情報から解決した実 executable も発行した absolute path と一致した。
続く `agent rename <pane-id> <name>` と current-state の `agent wait <name> --until idle --timeout 5000` は成功し、wait は約 2 ms で返った。
ただし `pane run` と backing `pane.send_input` は expected `terminal_id`、foreground shell、空入力の precondition を持たず、text と Enter を一操作で送るだけである。
したがって plain shell への command 送信は diagnostic probe に限り、自動 launch には使わない。

追加の隔離 probe では owned config の `terminal.default_shell` を absolute launcher、`shell_mode` を `non_login` に固定した。
launcher は exact checkout cwd、`HERDR_PANE_ID=w3:p1`、`HERDR_WORKSPACE_ID=w3`、operation nonce を受け、`pane wait-output` は `FANOUT_READY:<nonce>` を検出した。
続いて `pane run w3:p1 FANOUT_EXEC:<nonce>` を一回送ると、launcher が absolute fake process へ `FANOUT_AGENT_PROBE=value with spaces`、一要素の `arg with spaces`、exact cwd を渡した。
最初の pane で process 起動直後に一回だけ出した marker は buffer に残らなかったため、production launcher は有限 budget 内で marker を再送する。
この結果から、owned launcher readiness、operation-bound token、agent 検出、rename、process chain 照合を 0.7.5 の launch contract にする。
`agent wait` は current-state 即時評価を確認した server-owned wait として後続 workflow に使い、agent の初回 turn を待つ launch fence にはしない。

wave 2 は Herdr control-plane env と agent workload env を分離する。
supervisor / Herdr server / control-plane runner の env は空の base から組み立てた secret-free control env とし、owned XDG / config / socket / session、absolute fanout / Herdr entry、必要な固定 control value だけを持つ。
workload env は呼び出し元 env を launch 時に一回 snapshot し、Herdr routing env（`HERDR_` prefix）、fanout の launcher control env（`FANOUT_HERDR_` prefix）、`TMUX` / `TMUX_PANE` / `TMUX_TMPDIR` を除いてそのまま child へ渡す。
これは tmux backend の pane が呼び出し元 env を継承するのと同じ水準であり、versioned allow / deny policy は撤廃する。
snapshot は owned XDG / routing env を設定する前に取るため、owned XDG は child へ渡らず、呼び出し元の XDG はそのまま通る。
raw workload env value を control env、process argv、registry、intent、final row、log へ複製しない。

0.7.5 の child root pane は source workspace の env を継承しないため、workload env は launch nonce に束縛した one-shot の 0600 env file で launcher へ渡す。
env file は private namespace gate 済みの 0700 directory 配下に exclusive create し、registry / intent には path と name 数だけを記録して raw value を保存しない。

launcher の bootstrap protocol は次のとおりとする。
launcher は process start 時に `HERDR_PANE_ID` / `HERDR_WORKSPACE_ID` と exact cwd に一致する未失効 intent を server env の `FANOUT_HERDR_CONTROL_PATH` から lock-free read で採用し、shell、line editor、checkout 内 code を起動しない。
採用後は `FANOUT_READY:<launch-nonce>` を直ちに一回、その後は一秒ごとに bootstrap deadline（hard 300 秒と intent に保存した絶対 expiry の早い方）まで出す。
一回だけの即時 marker は capture 前に失われた実測があるため、parent の readiness 判定は再送 marker の `pane wait-output` 検出と launcher process identity の照合で行う。
matching intent がないまま deadline へ達した launcher は入力を受理せず非ゼロで終了する。
token の byte 完全一致以外の入力、先行入力、別 nonce は拒否し、child を起動せず終了する。
launcher は exact token の受理後に env file を読んで直ちに unlink し、intent に記録した cwd / 絶対 executable / argv と合わせて child を exec する。
env file の欠落、識別不一致、読み取り失敗では child を起動せず fail closed にし、crash 後も current env から再生成しない。
crash 後に残った env file は、対応する intent の存在確認を経た cleanup だけが unlink する。
旧正典の versioned allow / deny policy、MAC 付き env capsule、claim / consume CAS、disposal journal は撤廃し、必要になれば proof-grade tier で再評価する。

agent は fanout が解決した絶対 entrypoint を使い、#427 の lifecycle hook が呼ぶ fanout executable も絶対 path で注入する。
agent workload 内から利用する Herdr CLI は control-plane runner を唯一の入口とし、owned XDG / config / socket / session env を call ごとに secret-free control env として再構築する。

### 0.7.4 から 0.7.5 への契約写像

workspace-level `agent start` の各条項は次のように移す。

| 0.7.4 の条項 | 0.7.5 の判定 | 実装 owner |
|---|---|---|
| `argv` | `AgentStartParams` から廃止し、intent に記録した絶対 executable / 正規化済み argv を non-shell launcher が直接起動する | #528 |
| `cwd` | `AgentStartParams` から廃止し、worktree root PaneRef / cwd の precondition と process cwd の事後照合へ分ける | #527 / #528 |
| `env` | `AgentStartParams` から廃止し、one-shot 0600 env file で launcher から child process へ一回だけ渡す | #528 |
| `workspace_id` / `tab_id` / `split` | 廃止し、`worktree create` / `open` が返す既存 root pane を使う | #527 |
| `focus` | agent launch から廃止し、worktree の `--no-focus` と明示 TUI focus に分離する | #527 |
| `name` | start request の長い slug を使わず、検出後に 31 byte deterministic name を `agent rename` する | #528 |
| start response の PaneRef | 新規 pane は返らないため、保存済み root PaneRef と検出後の AgentInfo / `terminal_id` / process 照合結果を束縛する | #527 / #528 |
| manifest / binary resolution | Herdr manifest は executable pin に使わず、fanout が解決した絶対 executable と argv / cwd / provider detection の事後照合を使う | #528 |

後続 issue の担当境界は次に固定する。

| issue | 担当契約 |
|---|---|
| #526 | version gate と owned session bootstrap（owned XDG / socket / marker / lease、binary pin、supervisor bootstrap と bootstrap 失敗時の停止、live supervisor の再利用）。PR #572 で実装済み。owned server restart と明示 shutdown は含まない |
| #527 | worktree 実体化: base 解決と dirty / divergence gate、branch の atomic ref create と失敗時の branch 削除、intent 行と存在確認による応答喪失処理、workspace label nonce と事後条件照合、shared registry の row / intent 永続化と state lock、provisionalIntents の backend resolver 配線。目標規模は tmux 側同等機能（worktree / state / panelaunch）の 1〜1.5 倍（概ね 2,500〜4,000 行） |
| #528 | non-shell launcher（marker / token / env file / exec）、agent detection / rename / process 照合、issue / Project / plan / watcher の launch レーン解禁、deterministic agent name、coordinator row と stickiness、`--team` の fail-closed gate |
| #529 | provider hook adapter、fresh signal、pending emitter telemetry、`state_refinement` |
| #530 | plain shell からの owned session bootstrap 導線、console workspace と user shell 起動、owned server の明示 restart と明示 repo-session shutdown、TUI の focus / launch / peek と dashboard peek の解禁（owned session 限定。dashboard は read-only 境界のまま peek content 表示だけ） |
| #531 | `--close` / `--merge` / `--cleanup` と TUI close: identity 照合後の `worktree remove` と残存 workspace の整理、dirty の明示確認、branch の compare-and-delete、応答喪失の存在確認 |
| #532 | 0.7.5 direct launch の cold restart resume 再実測。解禁までは `terminal_id` 変化を `stale` にする |
| #552 | #568 の registry-backed peer 登録 / 自己識別 / 宛先解決を前提に、live `pane process-info` / OS process identity と final state を送信直前に再照合した exact pane ID へ no-wait の `agent prompt` nudge を発行し、移行前は Herdr row に `state.json` 依存経路を適用しない |
| #554 | owned launcher 経由の `fanout __codex-plan-tui` controller 起動と controller / Codex child の process 照合 |
| #568 | #528 の fail-closed gate を前提に、issue / plan cohort の peer 登録、plan lane の preseed / cleanup、自己識別、宛先解決、Claude / Codex push caller を shared registry の canonical Herdr row へ移行し、`--team` の再評価条件を満たす |

#568 は #528 に blocked され、#528 が実装する暫定拒否 gate を registry-backed peer 移行の完了まで維持する。
#552 の Herdr 実装は #568 の完了後に開始する。
owned server の bootstrap は #526（PR #572）が実装済みで、明示 restart / shutdown と console lifecycle は #530、cleanup lifecycle は #531 が実装する。

### process exit

次の bare process の結果は 0.7.3 workspace-level `agent start` の履歴であり、0.7.5 の launch vehicle には使わない。
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
0.7.5 の direct launch probe へ `agent send-keys <name> ctrl+c` を送ると agent record が消え、同じ root pane は shell に戻った。
wave 2 の production launcher は single-shot の operation child parent として pane に残り、console shell または agent child の終了後は別の shell を起動せず、次の intent または入力を受理せずに終了する。
pane 消滅後は final row を `stale` にし、同じ launcher process または同じ final row key を自動再利用しない。
pane 消滅後の `stale` row の整理は「cleanup」節の契約に従い、自動再 launch は行わない。
launcher が得た child exit status は診断に使えるが、fanout task の完了または cleanup authority にはしない。
#427 は fanout CLI を呼ぶ runtime 非依存の telemetry emitter として agent の報告状態を backend 固有 state へ記録する。
Claude は direct launch の argv へ `--settings` lifecycle hook を注入する。
Codex は launch-scoped の provider hook adapter から同じ emitter command を呼ぶ。
Codex adapter が未実装、注入不能、または検証不能なら `reported_state` を未設定のままにして nudge を no-op とする。
provider hook adapter と event-to-state mapping の検証成功だけでは `state_refinement:true` にしない。
tmux pane option は使わない。
hook 環境には絶対 `FANOUT_EMITTER_STATE_PATH`、state row key、launch ごとの opaque emitter nonce、backend、session / workspace / agent identity を注入する。
`FANOUT_EMITTER_STATE_PATH` は Herdr では shared registry、tmux では owning worktree の `FANOUT_STATE_PATH` と同じ file を指し、emitter は backend と path の組を検証してから更新する。
row key は shared registry の kind-tagged tuple を intent からコピーし、hook へ row key と launch nonce、emitter nonce を渡す。
emitter は `ParentRef`、`TaskID`、`IssueNum`、cwd、slug から key を作り直さない。
emitter nonce は state row にも保存し、再 launch ごとに更新する。
final row は synthetic launch telemetry として `reported_state:"running"` を保存できるが、current launch に束縛された fresh provider signal を受理するまでは `state_refinement:false` とする。
その後は provider hook の `working` / `plan` / `blocked` / `idle` / `done` だけで更新する。
これらの値は agent が起動する tool と checkout 内 script に継承されるため、secret、capability、event provenance の証明にはならない。
agent process は正規 hook と同じ emitter call を偽造できる。
emitter signal は協調プロセスの `reported_state` telemetry に保存し、tmux と同じ `shouldNudge` gate の入力には使うが、完了判定または cleanup の根拠には使わない。
emitter は state lock を短時間取得して更新し、launcher は hook の完了を待たずに agent 検出、rename、process-info 照合を進める。
emitter は state lock 下の state で分岐し、final row があれば `reported_state` update、matching intent だけがあれば pending telemetry として保存する。
final row の確定前に届いた signal は authoritative state を更新せず、key、nonce、backend、session / workspace / agent identity が intent と完全一致する場合だけ pending として保存する。
pending `done` は同じ nonce の先行 telemetry より優先するが、final row 確定前は query 結果へ出さない。
agent 検出後、保存済み root PaneRef、`terminal_id`、process 照合結果を同じ nonce に束縛し、pending fresh signal もこれらと一致する場合だけ、その signal の `reported_state` と `state_refinement:true` を final row の同じ save で確定する。
final row 確定後も、emitter は state lock 下で key、nonce、backend、PaneRef、`terminal_id`、process 照合結果が current launch と一致する fresh signal だけを受理し、その state と `state_refinement:true` を同じ save で確定する。
0 件、複数件、世代不一致、PaneRef 不一致は fail closed にする。
`terminal_id` の変化を検出した時点で state lock 下で `reported_state` を未設定、`state_refinement:false` にし、emitter nonce を回転して row を `stale` にする。
旧 nonce または旧 `terminal_id` に束縛された signal は拒否する。
cwd や slug から更新先を再解決しない。
Claude の `SessionEnd` 由来の `done` も診断用 telemetry に留める。
Claude と Codex は pane 消滅時に正常終了と外部からの kill を区別できないため、state row の有無にかかわらず `stale` とする。
identity 不一致も `stale` とする。

### 0.7.4 cold restart（履歴）

以下は 0.7.4 で Codex integration v6 を実測した履歴であり、0.7.5 direct launch の resume authority には使わない。
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

この実測は 0.7.4 の workspace-level `agent start` と公式 integration の経路である。
0.7.5 の owned launcher で direct launch した real Codex が同じ `agent_session` を登録し、cold restart 後に placeholder と `codex resume <id>` を復元することは確認していない。
したがって current contract は 0.7.5 direct-launch row の `terminal_id` が変わった時点で `stale` とし、0.7.4 matcher を実行しない。

#532 が resume を再解禁するには、同じ direct-launched Codex について restart 前の session ref、restart 後の exact placeholder、attach 後の resume process を一続きの隔離実機試験で確認する。
最低受入条件は、保存済み `agent_session` が `{source:"herdr:codex", agent:"codex", kind:"id", value:<session-id>}` と完全一致し、現在の pane に同じ ref が一つだけ存在することである。
attach 後は保存済み Codex の絶対 entrypoint / argv の記録と resume 用 matcher を再評価し、`pane process-info` と OS process 情報から同じ launcher に由来する chain を一つだけ得なければならない。
matcher は wrapper / interpreter / native child の許可 ancestry を検査し、resume process の provider args が `["resume", "<session-id>"]` と完全一致することを要求して追加引数を許可しない。
`<session-id>` は保存済み `agent_session.value` と byte-for-byte で一致させる。
候補の `process-info.foreground_processes[].cwd` は final row に保存した root pane cwd と完全一致させ、`pane get.cwd` または snapshot の `foreground_cwd` で代用しない。
候補 PID が保存済み launcher process の子孫であり、現在の foreground process group に属することを OS process 情報で確認する。
OS ancestry または process group を取得できない場合は再束縛しない。
この実機連鎖と全条件が成立した場合だけ、新しい terminal / process identity を一回の state save で束縛する設計を別 PR で解禁できる。
その場合も `reported_state` は未設定、`state_refinement:false` から始め、新しい `terminal_id` と回転後の emitter nonce に束縛された fresh provider signal まで nudge を no-op にする。
ref、placeholder、候補 chain の欠落または重複、matcher / argv / process cwd / ancestry / process group の不一致では緩い process 名一致へ fallback しない。

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
`process-info.foreground_processes[].cwd` は候補 PID に結び付いた process cwd であり、live identity と将来の resume 受入条件はこの値だけを使う。
`pane get.cwd` と snapshot の `foreground_cwd` は process cwd の照合を代替しない。

`foreground_cwd` は表示と診断の telemetry とし、PaneRef の識別または生存判定には使わない。
wave 2 の targeted structured read は、PaneRef の routing を backend、session namespace、workspace ID、pane ID で行う。
記録した launch との一致は `terminal_id`、task との対応は workspace の `repo_key` と `checkout_path` を含む worktree provenance で別々に検証する。
worktree provenance がない generic workspace では、fanout state に保存した checkout path と pane の `cwd` を補助照合に使う。

### send と nudge

wave 2 の Herdr backend は registry-backed peer 解決が実装されるまで `--team` を受理しない。
backend / flag validation は最初の state、filesystem、git、Herdr mutation より前に `herdr backend: --team requires registry-backed peer resolution` を返して終了する。
`--dry-run` も同じ error で終了し、移行前の Herdr `--team` を部分的に計画または seed しない。
この gate は Herdr backend だけに適用し、tmux backend の `--team` と既存 message bus を変更しない。
#528 はこの fail-closed gate を実装し、#568 が registry-backed peer 移行を完了するまで解除しない。
Herdr `--team` の再評価には、`internal/app/run/team.go` の peer 登録が shared registry の issue / plan task row から cohort と `TaskID` を作れることを要求する。
`internal/app/run/task_team_registry.go` の `preseedTaskTeamRegistry` / `cleanupUncreatedTaskPeers` は、最初の Codex pane より前の provisional plan cohort と fail-fast 後の未作成 peer cleanup を shared registry の canonical plan-task row で処理しなければならない。
`internal/infra/team/detect.go` の自己識別は shared registry の canonical typed row key、pane / worktree identity、issue number または `TaskID` を一意に解決しなければならない。
`internal/app/peermsg/nudge.go` の宛先解決は shared registry から current Herdr row と exact pane identity を取得し、後述の nudge gate へ渡さなければならない。
四経路とその Claude / Codex push caller が registry-backed になるまで、`state.json` を読む共通経路を Herdr row へ適用せず、prompt fallback や worktree-local row の複製で補わない。
この移行実装は本 PR のスコープ外とし、#568 が担当する。

0.7.3 / 0.7.4 の `pane send-text` は literal text だけを送り、別の `pane send-keys enter` まで shell cwd は変わらなかった。
同じ version の `agent send` も literal text を直ちに送り、agent status を working または blocked と報告した状態でも送信した。
0.7.5 は `agent send` を削除し、`agent send-keys`、`agent prompt`、`pane send-input` に分割した。

blocked 状態へ送った文字列は画面に入り、Enter は送らずに `ctrl+u` で消した。
文字列と Enter を別操作にすると、その間に permission dialog が focus される事故を防げない。

`pane run` は text と Enter を一操作で送り、command を実行したが、0.7.5 の自動 nudge には agent-aware な `agent prompt` を使う。
no-wait の `agent prompt <pane-id> <text>` は text と Enter を一操作で送り、`agent_prompted` を直ちに返した。
status read と submit の間の race はどちらにも残る。

fake Codex の state を変えないまま `agent prompt ... --wait --until idle --timeout 10000` を実行すると、約 5 秒で `agent_prompt_stalled` を返した。
同じ操作の timeout を 2000 ms にすると、stall 判定より先に通常の `timeout` を返した。
fake Codex を `idle`、`working`、unfocused settled state の順に遷移させると `--until idle` は timeout し、`--until done` は約 1 秒で成功した。
`--wait` は turn を識別せず、すでに working の agent では既存 turn の完了にも一致し得る。
したがって `agent prompt --wait` は nudge、delivery proof、turn completion の判定に使わない。

実 agent の再検証では Herdr が `idle` と `interactive_ready:true` を返した pane に startup update UI が残り、prompt がその UI を操作して npm の global update を開始した。
update は `ENOTEMPTY` で失敗したが、native status だけでは permission / update UI を nudge 対象から除外できないことを確認した。

herdr の `done` は process exit ではなく、agent が処理を終えて `idle` になった後にまだ focus されていない状態である。
実際、focus されていない pane に `working`、`idle` の順で報告すると snapshot は `working`、`done` と遷移し、`terminal_id` と focus は変わらなかった。
focus 後は `done` から `idle` へ変わる。

2026-07-21 JST のユーザー決定により、herdr backend の自動 nudge の送信契約を tmux-parity tier で解禁する。
#552 の Herdr 実装は前述の registry-backed peer 解決が完了した後に開始し、それまでは `--team` の fail-closed gate により到達不能にする。
対象 agent は current launch の fresh provider signal を受理して `state_refinement:true` になった Claude と Codex に限る。
provider hook adapter の注入と mapping を検証できない agent、または fresh signal が未着の agent は、名前が Claude または Codex でも no-op にする。
trim 済み `reported_state` が `running`、`working`、`plan`、`idle` の場合だけ送信候補とし、`blocked`、`done`、未設定、未知値は no-op とする。
synthetic initial `running` は `state_refinement:false` のため、値が allowlist にあっても送信候補にしない。
送信直前に live snapshot を一回取得し、保存済み backend / session / workspace / pane、`terminal_id`、`agent_session`、operation 固有の root provenance、agent を再照合する。
同じ target の `pane process-info` と OS process 情報を新たに取得し、保存済み launch 記録（絶対 executable、argv、cwd、launcher ancestry、foreground process group）に一致する chain が一つだけであることを要求する。
次に state lock 下で最新 row を再読し、row key、emitter nonce、PaneRef、`terminal_id`、いま取得した live process identity、deterministic name、`state_refinement:true` が同じ launch に一致することを確認して、その row の `reported_state` を `shouldNudge` へ渡す。
Herdr snapshot の native public status を `reported_state` の代用にしない。
照合成功時だけ lock を解放して no-wait の `agent prompt <saved-pane-id> <text>` を一回発行する。
移行後の recipient 欠落、pane 消滅、identity 再利用、不一致、非許可状態、runtime failure、send failure は message bus の保存を維持した no-op success とする。
応答喪失時は prompt が送信済みか判定できないため再発行しない。
hook telemetry は agent process から偽造でき、screen detection は未知の permission UI を除外できない。
この signal は tmux の `@fanout_agent_state` と同じ協調プロセス信頼で `shouldNudge` の入力に使い、screen manifest または `agent explain --json` は送信許可に使わない。
`terminal_id`、`agent_session`、root provenance、live process identity を送信直前に再照合しても、その後の `agent prompt` までに pane の状態は変わり得る。
herdr 0.7.5 には状態条件付き prompt または CAS がなく、この check-and-send race は tmux の `ListLive` から non-transactional な `send-keys` までの race と同種の受容済み残余リスクである。
runtime の atomic conditional send または terminal permission UI を操作しない out-of-band queue のいずれかと、agent process から分離した event provenance が揃えば proof-grade tier へ格上げする。
`codexPlanMode` は `plan` state の通常 nudge と別契約であり、owned launcher が絶対 `fanout __codex-plan-tui` を root pane 内で起動する controller として対応する。
controller の working / plan は emitter lane の `reported_state` で報告し、tmux pane option には書き込まない。
tmux backend の peer message bus への保存は維持する。
Herdr backend は registry-backed peer 解決まで message bus / nudge lane を開始せず、herdr の `notification show` も nudge に使わない。

### focus と wait

`agent focus <name>` と `workspace focus <id>` は対象を正確に focus した。
`--no-focus` は二つ目以降の workspace / worktree 作成で focus を維持し、direct agent launch は対象 pane を focus しなかった。
wave 2 は TUI の明示操作または focused child の cleanup 後だけ focus を発行し、target identity を送信直前に再照合する。
focus check と mutation の race は tmux focus と同種の受容済み残余リスクとし、background watcher は focus を奪わない。
request-bound server / target generation が使える場合は proof-grade tier へ格上げする。

0.7.3 の `wait output` と `agent wait --status idle` は後続 event だけを待ち、current state が一致済みでも即時成功しなかった。
0.7.5 の `agent wait <target> --until <status> --timeout <ms>` は request 時の current state を先に評価する。
実測では一致済みの `idle` が約 2 ms と約 99 ms で成功した。
`pane wait-output <pane> --match <text> --timeout <ms>` も current buffer を先に検索し、既存の cwd 出力へ即時に `output_matched`、read result、revision を返した。
wave 2 は agent の settled-state workflow に `agent wait`、launcher readiness と controller bootstrap の出力確認に `pane wait-output` を使い、どちらにも有限の `--timeout` を付ける。
launch finalization と nudge には使わない。

launcher readiness、console shell 検出、direct launch 後の agent 検出は次の共有 budget を使う。

- 一回の launch cycle は最初の mutation 前に monotonic clock で一つの deadline を確定する。`total_timeout` は 3 秒以上 300 秒以下（既定 300 秒）とし、retry で延長しない。
- observation は 2 秒間隔の snapshot / `pane process-info` polling とし、read-only CLI call の timeout は `min(5 秒, 残時間)` にする。read-only observation の失敗だけを budget 内で retry する。
- version / schema の不一致と malformed snapshot は retry せず、直ちに `failed` とする。
- mutation と launcher token は intent 行を保存してから一回だけ発行し、timeout、cancellation、non-zero exit、response loss で同じ request を retry しない。応答喪失は再実行時の存在確認に委ねる。
- terminal result は `matched`、`timed_out`、`cancelled`、`failed` の四値とし、`timed_out` / `cancelled` / `failed` 後は同じ launch cycle の CLI call を始めない。rollback は別 operation の新しい intent 行と budget で行う。
- launcher readiness は exact marker、PaneRef / `terminal_id`、launcher process identity が一致した場合だけ token 発行へ進み、deadline までに揃わなければ token を送らず `timed_out` とする。
- caller context の cancellation または SIGINT を受けた場合は interval sleep と実行中の process tree を止めて reap し、`cancelled` を返す。

event 駆動へ移す後続版は raw Socket で subscription を確立してから snapshot を取得し、以後の event と再同期を処理する。

## 実行環境と UI の境界

### backend 検出

0.7.3 の workspace-level `agent start` が作った direct process には `HERDR_ENV=1` が届いた。
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
wave 2 は初回の live identity 確定後に `report-metadata` を発行し、`terminal_id` 変化で `stale` になった row には再発行しない。
metadata が表示専用であることは、同名 session の差し替え後に無関係な pane / workspace へ issue、PR、CI token を書く race を安全にはしない。
report 前に exact backend / session / workspace / pane、`terminal_id`、worktree provenance を再照合し、不一致なら発行せず fail closed にする。
report 後の再照合で不一致なら結果を不明として fail closed にし、自動再送しない。
照合と report の race は tmux-parity tier の受容済み残余リスクとする。
request が authoritative server generation と target `terminal_id` / workspace generation を原子的に束縛できる場合は proof-grade tier へ格上げする。
`seq` は reporter 内の順序制御であり、cold restart で失われるため identity precondition の代用にしない。
metadata は表示専用データとし、backend state、liveness、nudge authority、完了判定には使わない。

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

## 0.7.4 Socket schema（履歴）

`herdr api schema --json` は protocol `16`、schema version `1` を返した。
0.7.4 の server 停止中と起動中に取得した schema は byte 単位で一致し、SHA-256 はどちらも `4c138114dfdb8ed9ddb906fcd117967ca7fac0c10251abf238452c52b766d49c` だった。
schema は client binary に同梱された capability document であり、接続先 server の attestation ではない。
top-level は `$schema`、`protocol`、`schema_version`、`schemas`、`title` を持ち、request の `oneOf` は 85 method を列挙した。
0.7.4 設計では `session.snapshot`、`workspace.create`、`workspace.close`、`workspace.report_metadata`、`worktree.create`、`worktree.open`、`worktree.remove`、`agent.start`、`pane.process_info`、`pane.report_metadata`、`pane.send_text`、`pane.send_keys` の params と response shape を structural gate 候補にした。
CLI の `pane run` は Socket method ではなく send method を組み合わせる CLI surface なので structural gate では検査できなかった。
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
0.7.4 schema には `agent.send`、`pane.send_input`、`pane.focus` もあり、手動検証には CLI の `pane run`、`agent focus`、`workspace focus` を使った。

## 0.7.5 Socket schema と capability 集合

0.7.5 の `herdr api schema --json` は protocol `17`、schema version `1`、89 methods を返した。
同じ artifact の SHA-256 は `1ef4eb9ec655cb0c89726895f437d8654bdde13a22e591fda06a9015d03d88c7` である。
0.7.4 の 85 methods から `agent.send` が消え、`agent.send_keys`、`agent.prompt`、`agent.wait`、`agent.view.set`、`agent.view.clear` が加わった。
以下の structural gate の記述は冒頭の注記のとおり実測と旧判断の履歴であり、current contract は version gate だけである。method / field の一覧は wave 2 が実際に使う surface の reference として残す。

wave 2 の structural gate は次の 25 raw methods と、その params / result が参照する type、enum、const を再帰的に検査する。

| 用途 | 必須 raw methods |
|---|---|
| lifecycle / snapshot / detection admission | `server.stop`、`server.agent_manifests`、`session.snapshot` |
| workspace / worktree | `workspace.create`、`workspace.focus`、`workspace.report_metadata`、`workspace.close`、`worktree.list`、`worktree.create`、`worktree.open`、`worktree.remove` |
| agent | `agent.list`、`agent.read`、`agent.rename`、`agent.focus`、`agent.prompt`、`agent.wait` |
| pane | `pane.get`、`pane.process_info`、`pane.read`、`pane.send_input`、`pane.report_metadata`、`pane.close`、`pane.wait_for_output` |
| plugin preflight | `plugin.list` |

CLI-only の `pane run` は raw method 名を持たない。
`pane.send_input` の shape に加え、`pane run` の token surface、text と Enter の一操作、`ok` response を別の fail-closed gate で検査する。
owned config の `terminal.default_shell` / `shell_mode` / `update.manifest_check` は Socket schema にないため、`config check`、config bytes、session bundle digest / root identity、実際の root process identity を別の fail-closed gate で検査する。
`agent.start` は実機 probe の診断面であり、採用する自動 launch の必須 method 集合には入れない。
`server.agent_manifests` は executable pin には使わないが、active detection fixture の source / version を照合する admission read として必須にする。

使用する request params の必須 field と optional field は次のとおりである。
各 request envelope はこれらに加えて top-level `id`、`method`、`params` を必須にする。

```text
workspace.create:
  required: []
  optional: [cwd, env, focus, label]

workspace.report_metadata:
  required: [workspace_id, source, tokens]
  optional: [seq, ttl_ms]

worktree.list:
  required: []
  optional: [cwd, workspace_id]

worktree.create:
  required: []
  optional: [base, branch, cwd, focus, label, path, workspace_id]

worktree.open:
  required: []
  optional: [branch, cwd, focus, label, path, workspace_id]

worktree.remove:
  required: [workspace_id]
  optional: [force]

agent.rename:
  required: [target]
  optional: [name]

agent.prompt:
  required: [target, text]
  optional: [wait]

agent.wait:
  required: [target]
  optional: [timeout_ms, until]

agent.read:
  required: [target, source]
  optional: [format, lines, strip_ansi]

pane.send_input:
  required: [pane_id]
  optional: [keys, text]

pane.process_info:
  required: []
  optional: [pane_id]

pane.read:
  required: [pane_id, source]
  optional: [format, lines, strip_ansi]

pane.report_metadata:
  required: [pane_id, source]
  optional used by fanout: [tokens, seq, ttl_ms]

pane.wait_for_output:
  required: [pane_id, source, match]
  optional: [lines, strip_ansi, timeout_ms]
```

`workspace.focus` / `workspace.close` は `workspace_id`、`agent.focus` は `target`、`pane.get` / `pane.close` は `pane_id` を必須にする。
`session.snapshot`、`server.stop`、`server.agent_manifests`、`agent.list` は空 params を使う。
`plugin.list` は required field がなく、optional `plugin_id` を持つ。
`agent.read` は `target` と `source`、`pane.read` は `pane_id` と `source` を必須にする。
fanout は rename request で non-null `name`、no-wait prompt で `wait` を省略し、launcher token 用 `pane run` の backing request で exact token の `text` と `keys:["enter"]` を送る。
selected method ごとに fanout が送受信する field path とその type、ref、enum、const、`required` membership を検査する。
未使用の optional field、未知の追加 field、追加 method は拒否理由にしない。

受理する result variant は `ok`、`session_snapshot`、`workspace_info`、`workspace_created`、`worktree_list`、`worktree_created`、`worktree_opened`、`worktree_removed`、`agent_list`、`agent_info`、`agent_prompted`、`wait_matched`、`pane_info`、`pane_process_info`、`pane_read`、`output_matched`、`agent_manifest_status`、`plugin_list` である。
各 variant の `type` と payload field を必須にし、unknown variant、field 欠落、type 不一致を拒否する。
`wait_matched` は `type` と `event`、`output_matched` は `type`、`pane_id`、`revision`、`read` を必須にする。
success envelope は `id` と `result`、error envelope は `id` と `error`、error body は `code` と `message` を必須にする。

`SessionSnapshot` は `version`、`protocol`、`workspaces`、`tabs`、`panes`、`layouts`、`agents` を必須にする。
`WorkspaceInfo` は `workspace_id`、`number`、`label`、`focused`、`pane_count`、`tab_count`、`active_tab_id`、`agent_status` を必須にする。
`TabInfo` は `tab_id`、`workspace_id`、`number`、`label`、`focused`、`pane_count`、`agent_status` を必須にする。
workspace の worktree provenance は `repo_key`、`repo_name`、`repo_root`、`checkout_path`、`is_linked_worktree` を必須にする。
`WorktreeInfo` は `path`、`is_bare`、`is_detached`、`is_prunable`、`is_linked_worktree`、`label` を必須にし、`branch` と `open_workspace_id` の property shape も要求する。
`PaneInfo` は `pane_id`、`terminal_id`、`workspace_id`、`tab_id`、`focused`、`agent_status`、`revision` を必須にする。
`AgentInfo` は `terminal_id`、`agent_status`、`workspace_id`、`tab_id`、`pane_id`、`focused`、`revision` を必須にする。
wave 2 が読む `agent`、`name`、`cwd`、`foreground_cwd`、`agent_session`、`interactive_ready`、`launch_pending`、`state_change_seq` は optional field として schema 上に存在することを要求するが、値の存在を全 response に要求しない。
`PaneProcessInfo` は `pane_id` を必須にし、`shell_pid`、`foreground_process_group_id`、`foreground_processes` を schema 上の optional field として要求する。
各 foreground process は `pid` と `name` を必須にし、exact matcher が使う `argv`、`argv0`、`cwd` を optional field として schema 上に要求する。
`AgentSessionInfo` は `source`、`agent`、`kind`、`value` を必須にする。
`PaneReadResult` は `pane_id`、`workspace_id`、`tab_id`、`source`、`format`、`text`、`revision`、`truncated` を必須にする。
`agent_manifest_status` は `type` と `manifests` を必須にし、`last_check_unix` は null または non-negative uint64、`last_result` は null または string の optional property shape を要求する。
各 `AgentManifestInfo` は string の `agent` / `source` / `source_kind` と boolean の `local_override_shadowing_remote` を必須にする。
`active_version`、`cached_remote_version`、`remote_update_error`、`remote_update_result`、`warning` は null または string、`remote_last_checked_unix` は null または non-negative uint64 の optional property shape を要求する。
active manifest gate は `active_version` の non-null exact fixture version、profile で固定した local-override `source_kind`、exact agent / source と manifest 集合の完全一致を追加で要求し、nullable update metadata を active rule の proof に使わない。

診断用 `agent.start` は `name`、`kind`、`pane_id` を必須、`args`、`timeout_ms` を optional にする。
成功時の `agent_started` は AgentInfo と top-level argv を返すが、独立した PaneRef response は返さない。
`server.agent_manifests` の response に executable path と launch argv はないため binary pin の根拠にはしないが、active detection manifest の source / version gate に使う。

## version と JSON 対応

stable public workspace、tab、pane ID の契約は 0.7.0、既存 local branch の worktree create/open は 0.7.1、`session.snapshot` と `api schema --json` は 0.7.2 で入った。
current contract は 2026-07-24 のユーザー決定による stable `>=0.7.5` の version gate だけであり、以下の三段 gate / behavior profile / active manifest gate の記述は冒頭の注記のとおり実測と旧判断の履歴である。
core runtime の exact client / server / protocol / schema version tuple allowlist は廃止し、structural compatibility は stable SemVer `>=0.7.5`、protocol `17` / schema version `1`、接続先 status の三段 gate で判定する。
この structural gate は schema 外の CLI / mutation 挙動を証明しないため、wave 2 の mutation admission は fanout binary に固定した code-owned behavior profile allowlist も必須とする。
初期 profile `herdr-wave2-behavior-v1` は source provenance が公式 release asset、stable SemVer `>=0.7.5,<0.7.6`、platform が `darwin/arm64`、executable SHA-256 が `37350546b0012555943b92eaf962665de4e264395baeb44227b8015e8ff5b0d6`、reviewed agent-detection fixture ID / declared versions / `manifest_set_digest`、no-refresh policy ID の組だけを許可する。
この文書で未確定の fixture constants、`manifest_set_digest`、no-refresh policy / 実機 proof / golden を #526 が同じ reviewed profile entry へ追加するまでは、この ID を operational admission に使わず、read-only preflight を除く fresh bootstrap と registry / filesystem / Herdr mutation を拒否する。
未登録の version、platform、executable digest、source、detection fixture、no-refresh policy は owned server spawn と最初の Herdr mutation より前に拒否し、repo config、user config、接続先 status は allowlist を拡張できない。
0.7.4 以下は method inspection より先に floor 未満として拒否する。
version 文字列は stable SemVer として parse し、prerelease と解釈不能な値を拒否する。
schema が定義する version string には prerelease を拒否する pattern がないため、fanout が検査する。

起動前は admitted Herdr source の provenance、platform、executable digest、detection fixture / `manifest_set_digest`、no-refresh policy を behavior profile に照合してから session bundle へ固定し、bundled Herdr の SHA-256、`herdr --version`、offline `api schema --json` を取得して protocol `17`、schema version `1`、使用 method、request / response の必須 field と、それらが参照する type、enum、const を再帰的に検査する。
server 接続後は `status --json` の client / server version が同じ admitted release identity に一致して floor 以上、client / server protocol がともに `17` で admitted schema と一致、session と socket が admitted attach と一致、`compatible:true`、`restart_needed:false` であることを要求する。
同じ attach gate で active manifest の source / version / exact fixture bytes / `manifest_set_digest` と no-refresh config / cache absence も検査する。
`api snapshot` の version と protocol も同じ admitted server に一致させる。
full schema gate は behavior profile ID、Herdr executable digest、agent-detection fixture ID / `manifest_set_digest`、no-refresh policy ID、admitted session bundle digest の組ごとに一回 cache でき、connected status / snapshot / active manifest gate は attach ごとに一回実行する。
owned server restart は同じ bundle digest でも cached proof を使わず、full schema、connected status、snapshot、active manifest の gate を新 server tuple に対して最初から再実行する。
各 CLI call は behavior profile ID、bundled executable identity / digest、agent-detection fixture / `manifest_set_digest`、no-refresh config、session bundle digest、response の必須 field を再検査し、version と protocol を持つ response ではその値も再検査する。
manifest-dependent な agent observation、rename、focus、nudge はその call の直前に fresh active manifest proof と同じ saved admission ID を追加で要求する。
session bundle digest は admitted executable の continuity key であり、未登録 binary の behavior proof ではない。
将来 release は `(source provenance, stable version, platform, executable digest, detection fixture ID, manifest_set_digest, no-refresh policy ID)` ごとに隔離 XDG と disposable repository を使い、owned server bootstrap / stop / restart と restart 後の public ID / `terminal_id`、routing / status を実機再検証する。
mutation matrix は `workspace.create` / `workspace.focus` / `workspace.report_metadata` / `workspace.close`、`worktree.create` / `worktree.open` / `worktree.remove` と branch / checkout 残存、`agent.rename` / `agent.focus` / `agent.prompt`、`pane.send_input` を使う CLI `pane run`、`pane.report_metadata` / `pane.close` の全解禁操作を含める。
各操作は exact CLI argv、backing raw method / params、response、operation 固有の事後条件、response loss 時の no-blind-retry を確認し、`pane run` は lowercase `enter` の token 配送、`agent prompt` は `wait` を省略した no-wait 配送として検証する。
non-shell launcher の readiness / env / process-info、plugin / setup-hook、fresh bootstrap と遅延後 / restart 後の active manifest source / version / digest、remote refresh 拒否、override / cache drift 検出も同じ profile の必須 probe とし、matrix の一部だけに成功した release を wave 2 profile へ追加しない。
再検証が成功した platform だけを behavior profile entry、fixture、この契約の同じ reviewed change で追加し、挙動が変わる release は state machine を先に改訂する。
0.7.5 は比較可能な server generation を返さないため、connection loss、restart、binary / active manifest drift、refresh attempt、`restart_needed:true`、その他の不一致を検出した場合は admission を失効させる。
`ready` 中の connection loss で server / socket の不在と旧 admission quiescence proof を証明した経路だけが owned server restart へ進み、三段 capability gate と active manifest gate を最初から再実行する。
restart candidate の re-gate 失敗は candidate の exact absence を証明した terminal CAS で `manifest-invalid` へ移し、spawn を再発行しない。
binary / active manifest / override / cache drift、refresh attempt、`restart_needed:true`、restart re-gate 失敗、その他の live admission 不一致は `manifest-invalid` へ移し、mutation-free reconciliation、明示 shutdown、fresh bootstrap を通すまで通常操作へ戻さない。
shutdown 後に retained manifest realization が欠落または drift している場合、fresh bootstrap は registry-only retirement CAS 後の next generation namespace にだけ新しい realization を publish する。
0.7.5 direct-launch row は gate の再成功にかかわらず `terminal_id` が変われば `stale` とする。
別 CLI 接続間の server continuity は証明できず、この gate を mutation authority にしない。
schema にない CLI-only surface は structural admission の対象外とし、wave 2 で使う surface は command と出力を別の fail-closed gate で検査する。

0.7.3 client から 0.7.4 server と、0.7.4 client から 0.7.3 server の両方向を実測した。
どちらも protocol `16` で `compatible:true` と snapshot success を返したが、`restart_needed:true` で client / server version は不一致だった。
0.7.4 client の `api schema --json` は 0.7.3 server 接続中も offline schema と同じ SHA-256 を返した。
したがって protocol compatibility と client schema だけでは接続先 server の capability を証明しない。

この gate は compatibility だけを証明し、mutation authority を与えない。
tmux-parity tier は同一 UID の協調プロセスを信頼するため mutation authority の証明を要求せず、intent 行と送信直前再照合を crash safety と誤操作防止に使う。
request-bound immutable generation と conditional mutation、server-authenticated controller capability、agent と別 UID の server のいずれかが使える場合は proof-grade tier へ格上げする。
cleanup、metadata、targeted read も resource-specific generation を原子的に束縛できる場合は同 tier へ格上げする。

| 実測コマンド | 導入 / 実測 version（runtime capability gate ではない） | JSON 対応 |
|---|---:|---|
| `herdr --version` | 0.7.3 core baseline、0.7.4 / 0.7.5 core wave 2 | text のみ |
| `status --json` | 0.7.3 core baseline、0.7.4 / 0.7.5 core wave 2 | 明示 `--json` |
| `session list --json` | 0.7.3 core baseline | 明示 `--json` |
| `workspace create/list/focus/close` | 0.7.3 baseline、0.7.4 / 0.7.5 core wave 2 | JSON envelope を標準出力へ返す。`--json` は付けない |
| `worktree create/open/list/remove` | 0.7.1、0.7.4 / 0.7.5 core wave 2 | 明示 `--json` |
| `agent start --kind --pane` | 0.7.5 live-agent probe | JSON envelope を標準出力へ返す。自動 launch には使わない |
| `agent rename/prompt/wait` | 0.7.5 pane-targeted wave 2 | JSON envelope を標準出力へ返す。`prompt` nudge は `--wait` を付けない |
| `pane get/run/close` | 0.7.3 baseline、0.7.4 / 0.7.5 core wave 2 | mutation と get は JSON envelope。`--json` は付けない |
| `pane process-info` | 0.7.3 baseline、0.7.4 / 0.7.5 core wave 2 | JSON envelope を標準出力へ返す。対象は `--pane` で指定する |
| `pane wait-output` | 0.7.5 pane-targeted wave 2 | JSON envelope を標準出力へ返す。有限の `--timeout` を付ける |
| `pane read` | 0.7.3 baseline | text または ANSI を直接出力する |
| `pane report-metadata` の presentation fields / seq / TTL | 0.7.3 | 成功時は出力なし（`--json` 非対応） |
| `pane report-metadata` の token patch | 0.7.4 | 成功時は出力なし（`--json` 非対応） |
| `workspace report-metadata` の token / seq / TTL | 0.7.4 | 成功時は出力なし（`--json` 非対応） |
| `api snapshot` | 0.7.2、0.7.4 / 0.7.5 core wave 2 と token projection | JSON envelope を標準出力へ返す。`--json` は付けない |
| `api schema --json` | 0.7.2、0.7.4 / 0.7.5 structural gate | 明示 `--json` |
| `notification show` | 0.7.3 baseline | JSON envelope を標準出力へ返す。`--json` は付けない |
| `plugin link/list/log list` | 0.7.3 baseline、0.7.4 isolation、0.7.5 global registry | `list` は `--json` 対応。他は JSON envelope |

この表は機能導入時期と実測 provenance を示すだけで、floor 未満または structural gate を通らない version の互換性を認めない。

version ごとの根拠は [v0.7.0](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.0)、[v0.7.1](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.1)、[v0.7.2](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.2)、[v0.7.3](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.3)、[v0.7.4](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.4)、[v0.7.5](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.5) を参照する。

## 後続 issue への契約

#423、#425 から #429、#494、#527 から #532、#544、#552、#554、#568 は次の制約を前提にする。

判断主体はユーザー、tmux-parity tier の判断日は 2026-07-21 JST、floor 0.7.5 の改訂日は 2026-07-22 JST、crash 機構の tmux-parity 密度への簡素化改訂日は 2026-07-27 JST である。
herdr backend は tmux backend と同水準の協調プロセス信頼を採用し、private socket が同一 UID の認証境界にならない実測を受容したうえで、影響範囲を fanout-owned session へ封じ込める。
#527 / #528 / #530 / #531 の受入契約は、tmux 側同等機能の 1〜1.5 倍で実装できる本文の密度を上限とする。
各機能の wave 2 判定と proof-grade tier への格上げ条件は次のとおりである。

| 機能 | wave 2 | tmux-parity tier の条件 | proof-grade tier への格上げ条件 |
|---|---|---|---|
| owned bootstrap / launch | Go（bootstrap は #526 が PR #572 で実装済み、launch は #527 / #528） | owned XDG / socket / marker の owner-only 検査、state lock、intent 行と存在確認、non-shell launcher の marker / token、送信直前再照合と事後条件検査 | request-bound direct spawn、controller capability、別 UID の bundle owner、または server / agent の UID 分離 |
| owned server restart | Go（実装は #530） | 明示操作による marker / lease と saved process / socket 不在の照合後の単一 spawn、結果不明の fail closed、restart 後の version gate 再実行と direct-launch row の `stale` 化 | authenticated server generation と request-bound conditional restart |
| console / coordinator close | Go | checkout を持たない exact owned workspace / pane を送信直前に再照合し、応答喪失は再実行時の存在確認で確定する | close が authoritative server generation と target resource generation を原子的に検査する |
| child cleanup | Go（#531） | identity 照合後の `worktree remove`（checkout と workspace を削除）、dirty の明示確認、branch の compare-and-delete、存在確認による応答喪失処理 | tracked / untracked / ignored subtree generation を remove と原子的に条件化する server-side conditional remove、または remove postcondition まで保持する kernel-enforced write-exclusion fence |
| child launch rollback | tmux の `failCleanup` と同水準 | 今回作った資源だけを identity 照合後に force なしで削除し、照合不一致、dirty 拒否、response loss では資源を残して fail closed にする | child cleanup と同じ conditional remove または fence |
| dirty `--force` | 明示確認後だけ許可 | dirty checkout はユーザーの明示確認なしに force しない | conditional remove / fence と fingerprint-bound receipt |
| #427 emitter | Go | cooperative telemetry と `shouldNudge` gate に限り、completion / cleanup authority にしない | agent process から分離した event provenance |
| 0.7.5 direct launch の cold restart resume | 保留 | `terminal_id` 変化時は `stale`。#532 が real direct launch / restart / attach / resume の実機連鎖を証明した後に再判定する | authoritative server generation と launch provenance の原子的な束縛 |
| #494 metadata | Go | exact target の直前・直後照合、固定 source、seq / TTL、表示専用 token | `report-metadata` が authoritative server generation と target generation を原子的に検査する |
| focus | Go | TUI の明示操作だけが target を直前再照合する | request-bound server / target generation |
| peek / targeted read | Go | exact PaneRef、`terminal_id`、worktree provenance を直前・直後に再照合する | response が authoritative server generation と target terminal identity を束縛する |
| `--team` | 拒否（#568 の registry-backed peer 解決まで。暫定 gate は #528） | `--dry-run` を含め、SQLite open、registry save、branch / workspace / Herdr mutation より先に明確な invocation error を返す | #568 が peer 登録、plan preseed / cleanup、自己識別、宛先解決、push caller の移行を完了した後に wave 2 条件を再評価する |
| 自動 nudge | Go（#552 の Herdr 実装は #568 完了後） | shared registry から peer / self / recipient の current Herdr row を一意に解決し、fresh provider signal と送信直前の live `process-info` / OS process identity が一致する許可状態だけへ、exact pane ID を target に no-wait の `agent prompt` を一回発行する | atomic conditional send または permission UI を操作しない out-of-band queue と、agent process から分離した event provenance |
| `codexPlanMode` | Go(実装は #528 / #529 / #544 後の別 issue) | owned launcher が絶対 path の `fanout __codex-plan-tui` を起動し、working / plan は emitter lane で報告する | 依存する launch / emitter lane の格上げ条件に従う |

2026-07-27 の簡素化で撤廃または任意記録へ格下げした機構は、proof-grade tier の再導入候補として次に保持する。
herdr が対応する primitive を提供するか、proof-grade tier へ格上げする判断が出た場合だけ再評価する。

| 撤廃 / 格下げした機構 | 扱い |
|---|---|
| フル phase machine（`branch-planned` / `worktree-starting` などの多段遷移）と exact request / pre-state の replay 契約 | 撤廃。intent 行 + 存在確認で置換し、request-bound conditional mutation が使える proof-grade tier で再評価する |
| CAS provisional registry（revision CAS、owner lease / heartbeat / takeover、admission state machine、epoch） | 撤廃。state lock + atomic save で置換し、authoritative server generation が使える proof-grade tier で再評価する |
| canonical bytes / digest / golden bytes（owner marker golden、registry codec golden、`manifest_set_digest`、removal fingerprint） | 撤廃。対応する機構の proof-grade 再導入と同時に再評価する |
| content-addressed launch bundle と build / publish / GC journal、sealed bundle 起動 | 撤廃。fanout が解決した絶対 entrypoint の直接起動で置換し、verified FD spawn または別 UID の bundle owner が使える proof-grade tier で再評価する |
| branch lineage / cleaned tombstone / explicit continue / tombstone forget | 撤廃。row 削除 + 既存 branch 採用の fresh launch で置換し、conditional remove / fence の格上げと同時に再評価する |
| ownership nonce の二重照合（workspace label + checkout git-dir marker） | 格下げ。workspace label の単一照合を正とする |
| plan spec snapshot の SHA-256 束縛 | 撤廃。plan task key は tmux と同じ `(plan slug, task ID)` にする |
| MAC 付き env capsule と claim / consume CAS / disposal journal | 撤廃。one-shot 0600 env file で置換し、必要になれば proof-grade tier で再評価する |
| proof-grade 前提の automatic remove 全面拒否（removal fingerprint / write-exclusion fence 要求） | tmux 水準の remove へ置換。fence / conditional remove は child cleanup の格上げ条件のまま保持する |

request-bound generation / conditional mutation、controller capability、UID 分離は削除せず、herdr 上流へ別 issue で提案する proof-grade 強化として保持する。
response loss 時の no-blind-retry（存在確認による分類）、intent 行、workspace label nonce、起動後の process 照合、exact pane cwd、identity の分離、bounded wait、metadata の表示専用性は維持する。
0.7.4 Codex integration v6 exact matcher は #532 の再解禁条件として残すが、current 0.7.5 row には適用しない。
emitter は telemetry のまま `shouldNudge` の協調 signal に使い、完了判定または cleanup の証明には使わない。

- backend は per-repo supervisor が owned XDG / socket / marker を exclusive create して foreground `herdr server` child を bootstrap する（#526 が PR #572 で実装済み）。
  supervisor は呼び出し元から継承した Herdr routing env の値に依存せず、`status` と bootstrap を含む各 Herdr CLI call 用の env を構築し、owned XDG、`HERDR_CONFIG_PATH`、`HERDR_SESSION`、`HERDR_SOCKET_PATH`、`HERDR_CLIENT_SOCKET_PATH` を fanout-owned 値で上書きする。
  `FANOUT_HERDR_CONTROL_PATH` も physical common directory から supervisor が導出して上書きし、repo config、呼び出し元 env、agent workload からの指定を受け付けない。
  server loss 後の restart は明示操作（実装は #530）が marker / lease と saved process / socket 不在の照合後に一回だけ spawn し、結果不明は fail closed にする。
  console detach 後も server を存続させ、最後の child close では止めず、active row / intent と foreign resource のない明示 repo-session shutdown（実装は #530。空状態確認と同じ save の shutdown intent 行で並行 mutation を fence する）だけを teardown とする。
- herdr backend wave 2 は snapshot / list / wait、targeted content read、root coordinator、worktree / agent launch、focus、nudge、metadata、console / coordinator close、child cleanup を後続実装へ解禁する。
  console / coordinator / agent launch は #528 の direct-launch 契約完了まで fail closed にする。
  `--team` と #552 の Herdr nudge 実装は例外とし、#528 の fail-closed gate は #568 が peer 登録、plan preseed / cleanup、自己識別、宛先解決を shared registry の Herdr row へ移行するまで最初の mutation 前に拒否する。
  移行前の Herdr row を worktree-local `state.json` に複製せず、tmux 用の state-dependent peer / nudge 経路へ渡さない。
  各 operation は保存済み identity と live snapshot を直前に再照合し、operation 固有の事後条件を検査する。
  check と operation の間の race は tmux-parity tier の受容済み残余リスクとし、不一致と重複は fail closed に、応答喪失は再実行時の存在確認で採用または fail closed にする。
- compatibility gate は 2026-07-24 のユーザー決定により stable `>=0.7.5` の version gate だけとする（「version と JSON 対応」の structural gate 記述は履歴）。
  実際の method call が失敗した場合は共通の unavailable error を返す。
- backend 選択の resolver は final state rows と provisional intents のすべてを入力にする。
  legacy row の空 backend は tmux に正規化する。
  実際の issue / Project / plan の親では、既存 rows / intents が一つの backend に一致する場合だけその backend を再利用し、mixed state または `--backend` / env との不一致は fail closed にする(明示的な移行はユーザー操作)。
  stickiness の単位は実際の issue / Project / plan の親に限る。
  wave 2 は親 issue の orchestrator pane を `@manual` の負番号 display row として保存するが、shared registry は actual owner identity と launch nonce の typed coordinator key を使い、issue / plan の provenance を実親へ帰属させて同じ stickiness 判定に含める。
  それ以外の `@manual` synthetic launch は互いに独立した launch の集まりであり、row identity とその intent の単位で backend を固定する。
- canonical git common directory で識別する per-repo session を使う。
  repo root の console workspace、実際の親ごとの coordinator workspace、sibling child workspace を配置する。
  linked worktree は session を共有し、独立 clone は full common-directory identity の hash で分離する。
  physical common directory 配下の `fanout/herdr-control.json` と `herdr-control.json.lock` を全 linked worktree が共有し、tmux backend の `.fanout/state.json` と同じ lock + atomic replace で直列化する。
  Herdr の console、intent、final row、telemetry routing はこの registry だけを正典とし、worktree-local `.fanout/state.json` へ複製しない。
  console は `(canonical git common directory, operation:console)` の専用 intent / row を使い、issue / task row、backend stickiness、nudge roster に含めない。
  console / coordinator / child の launch と cleanup は「worktree の配置と lifecycle」の intent 行 + 存在確認の契約に従う。
- fanout が worktree safety gate と idempotency を所有し、herdr は checkout と workspace の実体化を担当する。
  base selector は launch 時に一回だけ commit SHA へ解決し、dirty / divergence 検査は tmux backend と同一の fail-closed 契約を保つ。
  fresh branch は fanout の atomic ref create で base SHA に作り、既存 local branch は tmux backend と同じく採用する。
  失敗した launch の rollback は tmux backend の `failCleanup` と同水準とし、identity 照合と不在確認を満たした場合だけ今回作った資源を削除して、それ以外は資源を残して fail closed にする。
- workload env は呼び出し元 env の snapshot から Herdr / fanout routing env を除いてそのまま渡し、one-shot 0600 env file で launcher へ届ける。
  raw value を registry / intent / final row / log へ複製しない。
  launcher は marker / token / env file / exec の single-shot protocol に従い、child 終了後は別の shell を起動せず終了する。
- #427 は fanout CLI 経由で backend 固有 state の `reported_state` を更新する runtime 非依存の telemetry emitter を追加する。
  Claude は direct launch の argv へ `--settings` lifecycle hook を注入し、Codex は launch-scoped の provider hook adapter から同じ emitter command を呼ぶ。
  signal は協調 telemetry として表示、診断、`shouldNudge` gate に使い、完了判定または cleanup に使わない。
  `terminal_id` の変化で `reported_state` を未設定にし、emitter nonce を回転して row を `stale` にする。
- PaneRef の routing、worktree ownership、terminal 実体、論理上の会話、process の生存を別々に判定する。
  `unknown` record を無条件に running へ写像しない。
  0.7.5 direct-launch row は `terminal_id` が変わった時点で provider と `agent_session` の有無にかかわらず `stale` にし、#532 の実機連鎖まで resume を保留する。
- `agent wait` と `pane wait-output` は server-owned current-state / current-buffer wait として有限 timeout 付きで使い、launch finalization と nudge には使わない。
  polling は「read、入力、focus、wait」の共有 budget に従う。
- fanout-owned session 外の generic workspace shell は `HERDR_ENV=1` から自動検出し、その external session の config / default shell は変更しない。
  nested tmux では `--backend tmux` または `FANOUT_BACKEND=tmux` で明示的に上書きできるようにする。
- wave 2 は初回の live identity 確定後に `report-metadata` を発行する。
  metadata は backend state、liveness、nudge authority、完了判定に使わない。
  #494 の `report-metadata` call は対象 `pane_id` または `workspace_id`、固定 `source`、空でない token patch、必要な `seq` / `ttl_ms` だけで構成し、title、display agent、state label を書き換えない。
  `rows`、`rows_by_agent`、`row_gap` と styling は herdr とユーザーが所有し、fanout は config を書き換えない。
- in-app notification を配信保証のある channel として扱わず、fanout から自動呼び出しもしない。

## 参考

- [CLI reference](https://herdr.dev/docs/cli-reference/)
- [Socket API](https://herdr.dev/docs/socket-api/)
- [Agents](https://herdr.dev/docs/agents/)
- [Agent automation v0.7.5](https://github.com/ogulcancelik/herdr/blob/v0.7.5/website/src/content/docs/agent-automation.mdx)
- [v0.7.5 release](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.5)
- [Configuration](https://herdr.dev/docs/configuration/)
- [Config reference](https://herdr.dev/docs/config-reference/)
- [Session state](https://herdr.dev/docs/session-state/)
- 関連分析: [herdr 競合分析](competitive-herdr.ja.md)
- 親設計: [#423](https://github.com/butaosuinu/fanout/issues/423)
- 親設計の承認: [#424 spike 反映](https://github.com/butaosuinu/fanout/issues/423#issuecomment-4986704437)
- 検証 issue: [#424](https://github.com/butaosuinu/fanout/issues/424)
- wave 2 検証 issue: [#525](https://github.com/butaosuinu/fanout/issues/525)
- 0.7.5 再検証 issue: [#557](https://github.com/butaosuinu/fanout/issues/557)
