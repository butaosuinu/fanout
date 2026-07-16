# herdr runtime backend 実機検証

ステータス: v1 契約確定。
検証日: 2026-07-16。
対象: `herdr 0.7.3` stable、protocol `16`、schema version `1`。

fanout の herdr backend v1 は CLI-first とし、集約読みには CLI wrapper の `herdr api snapshot` を使う。
raw Socket client は実装しない。
worktree の作成、既存 checkout の採用、削除は herdr に委譲できるが、naming、base 解決、dirty と divergence の検査、idempotency は fanout に残す。
agent の nudge と session identity には herdr 単独で満たせない条件があるため、後述の制約を実装へ引き継ぐ。

## 採用判断

| 対象 | v1 の判断 | 理由 |
|---|---|---|
| server 起動 | fanout は既存の named session を要求する | headless CLI は server を自動起動しなかった |
| 集約読み | `herdr api snapshot` を使う | workspace、tab、pane、layout、agent、focus を 1 回で取得できる |
| raw Socket API | 不採用 | v1 で必要な操作は CLI wrapper で足りる |
| worktree | safety gate 後に `worktree create/open/remove` へ委譲する | branch、path、base を指定でき、plugin event も発火する |
| agent 起動 | `agent start` に bare argv、`--cwd`、`--env` を渡す | shell wrapper は終了検出を wrapper に張り付ける |
| nudge | confirmed-idle への `pane run` に限定する | `agent send` は queue せず、blocked 中にも文字列を送る |
| liveness | public pane ID、cwd、`terminal_id` を照合する | cold restart と fresh state で public ID だけでは元 process を識別できない |
| 通知 | best-effort の in-app 通知としてだけ使う | detached 時も `shown:true` で、表示完了の応答ではない |

## 検証条件

検証用の git repository、bare remote、linked worktree、named Herdr session を `/private/tmp` に作った。
ユーザーの default Herdr session は停止も削除もしていない。
plugin event の検証では `XDG_CONFIG_HOME` と `XDG_STATE_HOME` も `/private/tmp` へ向け、plugin registry と state を隔離した。

fanout の複数行入力はインストール済みの `fanout v0.12.0` を実際の Herdr pane で起動して確認した。
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

Herdr から比較可能な session epoch は取得できない。
v1 は session 名を namespace として保存し、各 PaneRef に `terminal_id` を保存する。
public ID または cwd が一致しても `terminal_id` が変わった行は、元 agent process との対応を失ったものとして扱う。
fanout-owned epoch は fanout が session lifecycle も所有する後続版でなければ検証に使えない。

## 操作面

| fanout 操作 | 採用 CLI | 結果 | 制約 |
|---|---|---|---|
| launch | `workspace create --cwd ... --no-focus` | `workspace_created` | 最初の workspace は focus 対象がないため focused になる |
| launch | `worktree create --workspace ... --branch ... --base ... --path ... --no-focus --json` | `worktree_created` | fanout の preflight 後に呼ぶ |
| launch | `agent start <name> --workspace ... --cwd ... --env ... --no-focus -- <argv...>` | `agent_started` | `--json` flag はないが JSON envelope を返す |
| list | `api snapshot` | `session_snapshot` | `session.snapshot` の CLI wrapper |
| list | `worktree list --workspace ... --json`、`agent list` | `worktree_list`、`agent_list` | `worktree list` は基準 workspace を明示する |
| read | `pane read`、`agent read`、`pane get` | text または `pane_read`、`pane_info` | 実行中 cwd は `foreground_cwd` を見る |
| focus | `agent focus <name>`、`workspace focus <id>` | 対象 agent または workspace を focus | 任意 pane ID への exact focus は CLI にない |
| send | `pane send-text`、`agent send` | literal text を送る | Enter は送らず、queue もしない |
| send | `pane run <pane> <text>` | text と Enter を一操作で送る | status 検査との race は残る |
| close | `worktree remove --workspace ... [--force] --json` | `worktree_removed` | workspace を先に閉じない |
| close | `workspace close`、`pane close` | `ok` | checkout は `workspace close` では消えない |
| wait | `wait output <pane> --match ...` | `output_matched` | matched line、read、revision を返す |
| wait | `wait agent-status`、`agent wait` | status event | current state predicate には使わない |

generic pane の exact focus が必要になった場合、Socket API の `pane.focus` を追加候補にする。
child workspace が一つの agent pane を持つ v1 では `agent focus` と `workspace focus` で足りる。

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
- 既存 branch の HEAD が期待する base SHA と違う場合は、Herdr に渡さず fail closed にする。
- 既存 checkout は `worktree open` で採用する。
- `worktree create` の postcondition が fanout の branch、path、HEAD 契約と一致しない場合だけ、fanout が git worktree を作り、`worktree open` で Herdr に採用させる。

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

`workspace close` を先に実行すると checkout は残る。
続く `worktree remove --workspace <closed-id>` は `workspace_not_found` になる。
この場合は残った checkout を `worktree open` で再登録してから `worktree remove` する必要がある。

cleanup の順序を次で固定する。

1. `worktree remove --workspace <id> --json` を実行する。
2. dirty 拒否時だけ、fanout の既存確認を通して `--force` を使う。
3. branch 削除は fanout の git lifecycle が担当する。

### plugin event

隔離した local plugin を `plugin link` し、`worktree.created` と `worktree.removed` の hook を登録した。
CLI 経由の create と remove で両 hook が各一回実行され、plugin log は `status:"succeeded"`、`exit_code:0` を返した。

作成 event は `workspace` と `worktree`、削除 event は `workspace_id`、最終 `workspace`、`worktree`、`forced` を含んだ。
fanout が worktree 実体化を Herdr へ委譲すれば、worktree setup plugin と共存できる。
fanout が fallback で git worktree を直接作る場合は、続く `worktree open` が `worktree.opened` を発火し、`worktree.created` は発火しない。

## pane と agent の lifecycle

### bare argv と env

`agent start --workspace w2` だけでは cwd は child checkout にならず、CLI 呼び出し元の cwd が使われた。
`--cwd <child-checkout>` は必須である。

bare argv へ明示した PATH prefix と `FANOUT_*` env はそのまま届いた。
Herdr が設定する session、pane、workspace の env も同じ process で確認した。

```text
PATH=/private/tmp/fanout-path:/usr/bin:/bin
FANOUT_PROBE=ok
HERDR_ENV=1
HERDR_SESSION=e424
HERDR_PANE_ID=w1:p2
HERDR_WORKSPACE_ID=w1
```

fanout は PATH と `FANOUT_*` を `--env KEY=VALUE` で渡し、agent binary を bare argv の先頭に置く。

### process exit

bare `/usr/bin/false` は `agent_started` を返した後に終了し、pane と agent record は消えた。
Herdr には exit status と終了後 shell が残らなかった。

次の wrapper は exit status を表示して shell を残せた。

```console
/bin/sh -lc '/usr/bin/false; rc=$?; printf "WRAPPED_EXIT=%s\n" "$rc"; exec /bin/sh'
```

ただし Herdr が追跡する process は wrapper になり、agent record は `unknown` のまま残った。
fanout は bare argv を採用し、tmux backend の「agent 終了後も shell を残す」契約を Herdr backend には持ち込まない。

### cold restart

cold restart 前後の結果は次のとおり。

| 項目 | restart 前 | restart 後 |
|---|---|---|
| workspace、tab、pane public ID | `w1` から `w7` | 同じ ID |
| cwd、layout、worktree provenance | 記録あり | 維持 |
| `terminal_id` | 各 pane の元 ID | 全 pane で新しい ID |
| live agent process | shell、wrapper | restore shell に置換 |
| `agent list` の name | `shell-agent`、`wrapped-shell` | name は残る |
| agent status | idle または unknown | unknown |
| `report-metadata` | title、display agent、state label あり | 消失 |

「agent record がないなら done」だけでは restart 後を判定できない。
name が残った unknown record も元 agent process の生存を示さない。
#427 は `unknown` を無条件に `running` へ写像せず、保存した `terminal_id` との一致を先に確認する。

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

`pane get` の `cwd` は保存された shell cwd を表す。
foreground で `(cd /; sleep 15)` を実行している間も `cwd` は元 repository のままで、`foreground_cwd` は `/` になった。
実行中 process の cwd 照合には `foreground_cwd` を使い、restore 後の checkout 照合には `cwd` も使う。

### send と nudge

`pane send-text` は literal text だけを送り、別の `pane send-keys enter` まで shell cwd は変わらなかった。
`agent send` も literal text を直ちに送った。
agent status を working または blocked と報告した状態でも送信され、queue と blocked gate はなかった。

blocked 状態へ送った文字列は画面に入り、Enter は送らずに `ctrl+u` で消した。
文字列と Enter を別操作にすると、その間に permission dialog が focus される事故を防げない。

`pane run` は text と Enter を一操作で送り、command を実行した。
ただし status read と submit の間の race は残る。
親設計の「`agent send` を preferred nudge にする」は採用しない。
v1 は直前に idle を確認できた場合だけ `pane run` で atomic submit する。

### focus と wait

`agent focus <name>` と `workspace focus <id>` は対象を正確に focus した。
`--no-focus` は二つ目以降の worktree と agent 起動で focus を維持した。

`wait output` は出力遷移を待ち、`output_matched` と matched line、read result、revision を返した。
`agent wait --status idle` は current state がすでに idle でも即時成功せず、後続 event を待った。
unfocused agent が idle を報告した場合は done event が返り、`agent focus` 後に idle へ変わった。

snapshot 後に event wait を登録すると、二つの操作の間に起きた遷移を逃す lost-wakeup race がある。
CLI-first の v1 は bounded snapshot polling を使い、CLI の一回待機を current-state predicate と組み合わせない。
event 駆動へ移す後続版は raw Socket で subscription を確立してから snapshot を取得し、以後の event と再同期を処理する。

## 実行環境と UI の境界

### backend 検出

`agent start` の direct process には `HERDR_ENV=1` が届いた。
一方、検証環境の generic workspace shell では `HERDR_ENV` と `workspace create --env FANOUT_PROBE=1` の値が見えず、外側 tmux の `TMUX` と `TMUX_PANE` だけを継承した。
この shell から fanout を起動すると tmux と誤検出するため、`--backend herdr` または `FANOUT_BACKEND=herdr` を明示する。

Herdr pane 内で nested tmux server を起動すると、nested tmux の global env に `HERDR_ENV=1`、`HERDR_PANE_ID`、`HERDR_WORKSPACE_ID` と `TMUX` が同時に入った。
`HERDR_ENV` を `TMUX` より先に判定しても「最も内側の runtime」を選んだことにはならない。

`HERDR_ENV` と `TMUX` の両方がある場合は Herdr を既定にしつつ、nested tmux の利用者が `--backend tmux` または `FANOUT_BACKEND=tmux` で明示的に上書きできる契約にする。
自動で内側を判定する場合は process ancestry の検査が別途必要になる。

### metadata と OSC

`pane report-metadata` で title、display agent、idle の state label を設定できた。
agent pane から OSC title sequence を出しても、これらの metadata field は変わらなかった。
metadata は cold restart 後に消えるため、fanout の永続 state には使わない。

### Shift+Enter

attach 中の Herdr client に Shift+Enter の `ESC [ 13 ; 2 u` を送ると、pane 側は `ESC [ 27 ; 2 ; 13 ~` を受信した。
fanout の TUI parser は後者を Shift+Enter として処理する。

実際の `__tui-new-pane-popup` に `line-one`、Shift+Enter、`line-two` を入力した結果は次のとおり。

```json
{
  "prompt": "line-one\nline-two",
  "agents": ["codex"]
}
```

Herdr pane 内でも fanout の複数行 prompt を使える。

### notification

default config の `delivery="off"` では `notification show` が `shown:false`、`reason:"disabled"` を返した。
隔離 config で `delivery="herdr"`、`delay_seconds=0` にすると、client 未 attach と attach 中の両方で `shown:true`、`reason:"shown"` を返した。
attach 中は PTY 出力に title と body の toast 描画を確認した。

detached 時の `shown:true` は server が request を受理したことを示すが、利用者の画面へ表示済みであることは示さない。
fanout の notify backend は維持し、Herdr の in-app notification は設定済み利用者向けの best-effort channel とする。

## Socket schema と JSON 契約

`herdr api schema --json` は protocol `16`、schema version `1` を返した。
各 request は `method` と `params` を必須にする。

```json
{
  "properties": {
    "method": { "const": "agent.start", "type": "string" },
    "params": { "$ref": "#/schemas/request/$defs/AgentStartParams" }
  },
  "required": ["method", "params"],
  "type": "object"
}
```

v1 が使う shape を schema の `required` と optional field に分ける。

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

agent.send:
  required: [target, text]
  optional: []

pane.send_input:
  required: [pane_id]
  optional: [text, keys]

events.wait:
  required: [match_event]
  optional: [timeout_ms]

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

raw Socket の `events.subscribe` は常駐 client を増やすため v1 では使わない。
`pane.send_input` と `pane.focus` も schema にはあるが、今回の v1 操作は `pane run`、`agent focus`、`workspace focus` で足りる。

## version と JSON 対応

stable public workspace、tab、pane ID の契約は 0.7.0、既存 local branch の worktree create/open は 0.7.1、`session.snapshot` と `api schema --json` は 0.7.2 で入った。
v1 は done focus 修正を含み、今回の全操作を確認した `herdr >= 0.7.3` を要求する。

| 採用コマンド | 最低 version | JSON 対応 |
|---|---:|---|
| `herdr --version` | 0.7.3 baseline | text のみ |
| `status --json`、`session list --json` | 0.7.3 baseline | 明示 `--json` |
| `workspace create/list/focus/close` | 0.7.3 baseline | JSON envelope を標準出力へ返す。`--json` は付けない |
| `worktree create/open/list/remove` | 0.7.1 | 明示 `--json` |
| `agent start/list/read/focus/send/wait` | 0.7.3 baseline | JSON envelope を標準出力へ返す。`--json` は付けない |
| `pane get/run/send-text/send-keys/close` | 0.7.3 baseline | mutation と get は JSON envelope。`--json` は付けない |
| `pane read` | 0.7.3 baseline | text または ANSI を直接出力する |
| `wait output/agent-status` | 0.7.3 baseline | JSON envelope を標準出力へ返す。`--json` は付けない |
| `api snapshot` | 0.7.2 | JSON envelope を標準出力へ返す。`--json` は付けない |
| `api schema --json` | 0.7.2 | 明示 `--json` |
| `notification show` | 0.7.3 baseline | JSON envelope を標準出力へ返す。`--json` は付けない |
| `plugin link/list/log list` | 0.7.3 baseline | `list` は `--json` 対応。他は JSON envelope |

`baseline` はその version 以前の導入時期を主張せず、v1 が要求する実機確認済み version を示す。

version ごとの根拠は [v0.7.0](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.0)、[v0.7.1](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.1)、[v0.7.2](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.2)、[v0.7.3](https://github.com/ogulcancelik/herdr/releases/tag/v0.7.3) を参照する。

## 後続 issue への契約

#423 と #425 から #429 は次の制約を前提にする。

- backend は明示的に起動済みの named Herdr session を使う。
- worktree は root coordinator と sibling child workspace で配置する。
- fanout が worktree safety gate と idempotency を所有し、Herdr は checkout と workspace の実体化を担当する。
- agent は bare argv、明示 `--cwd`、明示 `--env` で起動する。
- `unknown` record を無条件に running へ写像せず、`terminal_id`、public ID、cwd を合わせて判定する。
- nudge は confirmed-idle の場合だけ `pane run` で送り、status 検査との race を許容する。
- CLI-first の wait は bounded snapshot polling にし、snapshot と event wait を直列に組み合わせない。
- generic Herdr shell では `--backend herdr` を明示し、nested tmux では `--backend tmux` を明示できるようにする。
- cleanup は `worktree remove` を `workspace close` より先に実行する。
- in-app notification を配信保証のある channel として扱わない。

## 参考

- [CLI reference](https://herdr.dev/docs/cli-reference/)
- [Socket API](https://herdr.dev/docs/socket-api/)
- [Agents](https://herdr.dev/docs/agents/)
- [Configuration](https://herdr.dev/docs/configuration/)
- [Session state](https://herdr.dev/docs/session-state/)
- 親設計: [#423](https://github.com/butaosuinu/fanout/issues/423)
- 親設計の承認: [#424 spike 反映](https://github.com/butaosuinu/fanout/issues/423#issuecomment-4986704437)
- 検証 issue: [#424](https://github.com/butaosuinu/fanout/issues/424)
