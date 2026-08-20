# herdr backend 実機検証記録

wave 2 (親 #524) 完了後、herdr backend を初めて実機で通した記録です。
`herdr` を実際に起動し、owned session を作り、agent を立ち上げ、observe から
lifecycle まで一巡させました。

結論を先に書きます。**検証前の main では herdr backend の launch は一度も成功
しませんでした。** 原因は独立した 3 つの不具合で、いずれも実機を通していれば
最初の 1 分で露見するものです。修正後は herdr 0.7.5 と 0.8.0 の両方で
issue-less plan のファンアウト、telemetry、peer messaging、lifecycle が動きます。

## 検証環境

| 項目 | 値 |
|---|---|
| 日付 | 2026-08-19 JST |
| fanout | `023f318d` (main) + 本 PR の修正 |
| herdr | 0.8.0 (protocol 19, method 90) / 0.7.5 (protocol 17) |
| herdr 0.8.0 SHA-256 | `d53a9f93fccfdfcc55632927bf51002f5add0aa7990bcdf508ffbd84ac658178` |
| herdr 0.7.5 SHA-256 | `37350546b0012555943b92eaf962665de4e264395baeb44227b8015e8ff5b0d6` |
| OS / Go | macOS 15.6 arm64 / go1.26.6 |
| 検証リポジトリ | `/private/tmp` 配下の使い捨て clone |

owned session は fanout が XDG 四 root と socket を `/private/tmp/fhr-501/` 配下へ
隔離します。検証の前後で `~/.config/herdr/` の
`config.toml` / `session.json` / `session-history.json` / `release-notes.json` の
SHA-256 が変わらないことを確認しました。spike doc の事故 (`herdr-runtime-backend-spike.ja.md`
「事故記録と再発防止」) は再発していません。

## 見つかった 3 つの不具合

### 1. pane launcher が起動直後に panic する

`cmd/fanout/main.go` の `selfExecDispatch` が `os.Args[2:]` を評価していました。
herdr の pane launcher は予約トークンを持たず環境変数だけで認識されるため
`os.Args` の長さは 1 で、`[2:1]` が範囲外になります。

```
panic: runtime error: slice bounds out of range [2:1]
main.selfExecDispatch.func1()  cmd/fanout/main.go:42
```

launcher は herdr の `default_shell` なので、pane は生成の 290ms 後に exit 2 で
死に、fanout が待つ ready marker は永久に来ません。#701 (P4a、self-exec registry)
で混入しました。それ以前は `RunPaneLauncher(os.Stdin, os.Stdout, os.Stderr)` と
引数なしで呼んでいます。同居する supervisor はトークン付きで起動されるため
無傷でした。タグには未到達で、影響は main のみです。

### 2. `pane run` の応答契約が実在しない

`validatePaneRunResponse` が `{"id":"cli:pane:run","result":{"type":"ok"}}` を
要求していました。実測では **0.7.5 も 0.8.0 も exit 0 で stdout 0 バイト**を返します。
0.8.0 で変わったのではなく、最初から実機と食い違っていました。

同じファイルの `validateRestartResumeResponse` だけが空応答を許容しており、
launch 経路と restart resume 経路で扱いが割れていたのが手掛かりです。

### 3. claude が自分の launch を拒否する

launch を記録するときは emitter の `--settings` を含む argv を作り、後で検証する
ときは含めずに再構築していました。`saved Herdr launch does not match the current
agent command` で必ず失敗します。

影響は claude だけです。`--settings` を足すのは claude の launch に限られ
(`managedEmitterBackendArgs`)、Codex Plan Mode は emitter を環境変数だけで運ぶため、
記録側も検証側も同じ argv を組んでいました。直 codex も同じく無傷です。

## なぜテストが素通りしたか

- `cmd/fanout/dispatch_test.go` と `internal/infra/paneruntime/paneruntime_test.go` は
  self-exec エントリの**認識**(`Match`、エントリ名)だけを検査し、`handle` を一度も
  呼びません。1 の panic は `handle` の中で起きます。
- `pane run` の応答は Go 単体テストが期待値を自分で書いており、実機と照合された
  ことがありません。bats の `tests/bin/herdr` shim には `herdr-pane-run.json` fixture
  自体が無く、dry-run しか通らないのでこの経路に到達しません (#714)。
- 3 は保存側と検証側が別々に argv を組み立てており、テストも同じ非対称を再現して
  いたため一致していました。

修正では、認識ではなく起動をテストする回帰テスト
(`TestSelfExecDispatchRunsTokenlessEntry`)、実機応答を provenance 付きで固定する
テスト (`TestPaneRunResponse`)、emitter args を含めた binding テスト
(`TestValidateManagedLaunchBindingAcceptsRecordedEmitterLaunch`) を追加しました。
最後のものは記録側の実経路 (`resolveManagedLaunch`) が作った argv を検証側へ通します。
手元で組み直した spec と突き合わせると、同じ関数を往復するだけで元の欠陥を再現できません。

## herdr CLI の応答契約 (0.7.5 / 0.8.0 実測)

両バージョンで差はありませんでした。

| 応答の形 | コマンド |
|---|---|
| JSON envelope を stdout | `api snapshot` / `status --json` / `plugin list --json` / `workspace create`・`list`・`focus`・`close` / `worktree create`・`list`・`remove` / `pane get`・`list`・`process-info`・`close`・`wait-output` / `agent list`・`get`・`rename`・`focus`・`prompt` |
| **exit 0 で stdout 0 バイト** | `pane run` / `pane send-text` / `pane send-keys` / `pane report-agent` / `pane report-metadata` / `workspace report-metadata` |
| 生テキスト | `pane read --format text` / `agent read` |
| 失敗時 | **stderr** に `{"error":{"code":…},"id":"cli:…"}` + 非 0 exit |

`pane run` は CLI にありますが API schema に `pane.run` メソッドはありません
(0.8.0 の 90 メソッドを列挙して確認)。

エラー envelope が stderr に出る点は要注意です。`decodeMutationRejection`
(`internal/infra/herdrrun/mutation.go`) は stdout だけを見るため、
`MutationRejectedError` への分類は現状効きません。失敗自体は非 0 exit で検出
できるので launch は fail closed のままですが、rollback の判断材料が 1 つ欠けます。
別 issue として起票しました ([#711](https://github.com/butaosuinu/fanout/issues/711))。

## 検証マトリクス

修正後の結果です。判定は **OK** (docs どおり動く) / **FAIL-CLOSED**
(docs どおり明確なエラーで落ちる) / **乖離** (docs の記述と実機が食い違う)。

### mutation ゼロ

| 対象 | 結果 |
|---|---|
| issue / plan の `--dry-run` | OK — `herdr workspace create` / `worktree create` を表示 |
| backend 解決順 8 パターン (`--backend` / `FANOUT_BACKEND` / `HERDR_ENV` / `TMUX` / user config / repo config / 既定 / 不正値) | OK — `resolve.go` の順序どおり。`HERDR_ENV`+`TMUX` は herdr が勝ち、repo config の `runtimeBackend` は警告付きで無視 |
| `--session` を herdr に渡す | FAIL-CLOSED — `--session is only supported by the tmux backend` |
| agent 省略 | FAIL-CLOSED — `agent is required` |
| `--team` + 相対 `FANOUT_DB_PATH` | FAIL-CLOSED — `must be absolute with --backend herdr --team` |
| `--backend` と `--status` / lifecycle の併用 | FAIL-CLOSED — 排他エラー |
| version floor (0.7.4 / prerelease / 不正形式) | FAIL-CLOSED。1.0.0 は通過 (上限を拒否しない設計どおり) |

### owned session と launch

| 対象 | 結果 |
|---|---|
| 素のシェルからの bootstrap | OK — `[ ok ] Herdr console w1:p1 is ready` + attach command |
| XDG / socket の隔離 | OK — ユーザーの `~/.config/herdr/` は SHA-256 一致で無傷 |
| plan fan-out (claude / codex) | OK — worktree、workspace、agent を作成し `.fanout/state.json` に identity を保存 |
| sidebar metadata token | OK — `fanout_issue` / `fanout_slug` を workspace に報告 |
| dirty checkout | FAIL-CLOSED — `source checkout … has uncommitted changes; refusing Herdr worktree mutation` |
| launcher SHA 不一致 (fanout 更新後) | FAIL-CLOSED — `owned Herdr launcher predates the current fanout; restart is required` |

### 観測

| 対象 | 結果 |
|---|---|
| TUI ヘッダ | OK — `backend: herdr (FANOUT_BACKEND) session=… ` |
| TUI の `BACKEND` 列 / 詳細パネル | OK — `backend=herdr pane=w3:p1 runtime=live agent=claude run=idle` |
| TUI `p` (peek) | OK — owned pane の出力を取得 |
| TUI `v` (view 切替) | OK — ランタイム非依存 |
| TUI `?` / `s` | OK — tmux popup ではなく inline modal |
| TUI `Z` (zoom) | **乖離** — 後述 |
| `--status` table / json | OK — `BACKEND` / `REPORTED_STATE` 列に `herdr` / `idle`・`working` |
| dashboard 一覧・`backend:herdr` フィルタ | OK |
| `GET /api/peek` | OK — owned 行は 200 |
| `GET /api/plan` | FAIL-CLOSED — 404 `pane … is not a tmux plan-mode pane` |
| `GET /api/diff` | OK — pane live 不要で merge-base 相対の patch を返す |

### telemetry と messaging

| 対象 | 結果 |
|---|---|
| claude の agent state (`idle` / `working`) | OK — emitter hook 経由で `--status` と dashboard に反映 |
| `msg peers` / `post` / `board` | OK |
| `msg nudge` | OK — herdr の `agent prompt` で配送され、claude が `fanout msg inbox` を実行 |
| codex `--team` ブリッジ | 未達 — 後述 |

### lifecycle

| 対象 | 結果 |
|---|---|
| `--close` | OK — herdr の workspace・pane・agent、state 行、worktree をすべて削除 |
| `--merge` (非 FF) | FAIL-CLOSED — `no conflict resolution was attempted` |
| `--cleanup` | OK — 対象なしで no-op |
| `herdr restart` (生存世代) | FAIL-CLOSED — `owned server generation is still live` |
| `herdr shutdown` (console + coordinator 行あり) | FAIL-CLOSED — `active Herdr intent rows remain` |

## docs と食い違った点

### TUI の `Z` (zoom) — #713

`site/content/docs/herdr-backend.ja.md` は「未対応の経路は明確なエラーで fail
closed します」と書いていますが、`Z` は herdr 行で focus だけ実行し、zoom を
黙ってスキップします。notice は `focusing w3:p1...` だけで、zoom が効かないこと
は画面に出ません。差分表にも zoom の行がなかったため、本 PR で例外として追記しました。

### fanout を更新すると launch できなくなる — #710

owned session は launcher バイナリを SHA で pin します。`fanout update` や再ビルド
の後は launcher が一致せず launch が fail closed になり、`herdr restart` は生存
世代を拒否し、`herdr shutdown` は console 行と coordinator 行が残るため拒否します。
3 つの verb がすべて閉じ、owned runtime ディレクトリを手で消す以外に復旧路が
ありません。

### 失敗した launch が intent を残す — #710

launcher が起動に失敗すると `realized` の intent が journal に残り、その pane は
既に存在しないのに `shutdown` / `restart` が拒否され続けます。docs は「失敗した
後にどちらの verb を再実行しても安全です」と書いていますが、この状態からは
回収できません。

### codex `--team` が 10 秒で諦める — #712

2026-08-20 に Codex CLI 0.146.0 と macOS 15.6 で追試しました。
tmux backend でも `codex TUI did not report an active thread within 10s` が再現するため、
herdr 固有の問題ではありません。

remote TUI は thread を作る前に 0.148.0 への更新確認ダイアログで停止していました。
更新確認を有効にした再試行は全体 13.457 秒後に失敗し、
`check_for_update_on_startup=false` を remote TUI だけに渡した 3 回は
3.991 秒、4.057 秒、4.221 秒で ready を報告しました。
そのため、内側の 10 秒は変更せず、fanout が起動する remote TUI の更新確認だけを
無効にします。

`--team` なしの Codex は remote TUI を使わないため、この起動引数の変更を受けません。
元の tmux 失敗では worktree、state 行、pane、ブランチを残さず cleanup できました。

修正済みバイナリと Herdr 0.8.0 の使い捨て clone では、
`plan --backend herdr --agent codex --team` が `w3:p1` を作成し、
初回 turn は task identity を返して idle になりました。
`plan --close probe` は pane、worktree、state 行を削除し、続く `--cleanup` は no-op です。

後始末の失敗は #710 のままです。
Herdr 0.8.2 で console bootstrap が `herdr launch requires manual cleanup` になった後、
`fanout herdr shutdown` は `1 active Herdr intent rows remain` で拒否しました。
これは team bridge より前の起動経路で、#712 には含めません。

## 再現手順

```bash
make build-go

# mutation ゼロ
./fanout-go plan <spec.json> --backend herdr --agent claude --dry-run

# owned session を起動して claude をファンアウト (tmux の外から)
export FANOUT_BACKEND=herdr
./fanout-go                                   # attach command が出る
./fanout-go plan <spec.json> --agent claude --team

# 観測
./fanout-go plan <spec.json> --status --format table
./fanout-go dashboard --web

# 片付け
./fanout-go plan <spec.json> --close <task-id>
```

owned session を外から覗くには、bootstrap が表示した attach command と同じ env を
前置します。素の `herdr api snapshot` では owned session は見えません。
