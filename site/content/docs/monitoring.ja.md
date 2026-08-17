---
title: 進捗モニタリング
linkTitle: モニタリング
description: "ファンアウトした全ペインの進捗を一望し、どこで止まっているかを見つける 3 つの窓。常駐 TUI コンソール、--status の JSON と table、Web ダッシュボード。"
weight: 40
kanji: 見
yomi: monitoring
---

子 issue を 5 つファンアウトすると、tmux には 5 枚のペインが並び、それぞれ別のエージェントが別の worktree で動きます。
知りたいのは、どのペインが PR まで進み、どこで止まっているかです。

fanout はこれを 3 つの窓で見ます。
手元で常時眺めるなら**常駐 TUI コンソール**、automation に食わせるなら `--status` の **JSON**、チームやブラウザで共有するなら **Web ダッシュボード**です。
`--status` は読み取り専用で `.fanout/state.json`、選択中の runtime、GitHub を読むだけです。TUI は選択中の runtime で検証済みの行に merge、close、cleanup も実行でき(`--merge` / `--close` / `--cleanup` と同じ処理です)、Web ダッシュボードは読み取りが基本で、PR のマージだけ実行できます。

## ペイン枠線ラベル

3 つの窓を開く前に、tmux の画面そのものでもペインを見分けられます。
fanout は作成した各ペインの上枠に `<parent> · <name>` のラベルを付け(issue の子なら `#123 · fix-login-bug-123`、plan タスクなら `plan:my-feature · task-slug`)、枠線を fanout のテーマ色に染めます(アクティブは浅葱、それ以外は藍)。
タイル状に並んだ window を一瞥するだけで、どのペインがどの子か、フォーカスせずに分かります。

## 常駐 TUI コンソール

手元で全ペインを眺め続けるなら、引数なしの `fanout` で常駐コンソールを起動します。

```bash
fanout   # start the persistent console
```

tmux backend では、素のシェルから起動すると fanout 管理の tmux session を作成または attach し、tmux 内から起動すると現在のペインをそのままコンソールにします。
herdr backend では、素のシェルから起動するとリポジトリの fanout-owned session と console workspace を bootstrap し、attach command を表示します。
表示された command を実行して console へ入ってください。
fanout は呼び出し元の shell を残します。
コンソールは `.fanout/state.json` を読み、記録済みペインの issue と PR の状態を定期更新し、各行の worktree には変更量を `+X/-Y`、未 commit の作業の有無を `dirty` / `clean` で示します。
`RUN` 列には agent の実行状態がグリフで出て(起動ラッパー由来の `●` running・`✓` done に加え、agent hooks が報告すると `◐` working・`◇` plan・`◆` blocked・`○` idle)、detail panel には同じ値が `run=` として出ます。
マウスや tmux の `prefix` ペイン移動で記録済み tmux ペインへフォーカスすると、TUI の選択行もそのペインに追従します。

コンソールは backend を認識します。ヘッダには選択中の runtime backend と選択理由(例: `backend: herdr (HERDR_ENV)`)が出て、detail panel には各行の `backend=` と `pane=` の identity が出ます。
fanout-owned [herdr backend]({{< relref "/docs/herdr-backend" >}}) session では、issue / Prompt / attach / shell の launch、focus、peek を使えます。
検証済みの worktree 行には merge、close、cleanup も実行できます。
foreign または identity が不完全な Herdr 行は、キーごとの理由を表示して無効になります。
send、restore、plan capture は未対応です。
CLI と label watcher の launch も同じ owned runtime path を使います。

{{< diagram "console" >}}

### agent-state の通知音

TUI の通知 channel は agent-state の変化も扱います。
既定の `notifications=bell` では、従来の issue / PR 遷移と同じ terminal bell が、agent が plan を提示したとき、ユーザー入力や承認を待つとき、agent が終了したときにも鳴ります。
通知先は `notifications` または `FANOUT_NOTIFICATIONS` で変えられます。
詳細は [Settings]({{< relref "/docs/settings" >}}) を参照してください。

fanout は、ペインの `@fanout_agent_state` tmux option から構造化された agent state を読みます。
ペイン出力からは推定しません。
観測する state は `running`、`working`、`plan`、`blocked`、`idle`、`done` ですが、通知を送るのは `plan`(`plan ready`)、`blocked`(`waiting for input`)、`done`(`agent exited`)だけです。
それ以外の state は表示専用です。

### キー操作

| キー | 動作 |
|---|---|
| `?` | キーボードショートカットのヘルプを開く(tmux popup または Herdr の inline form)。`Esc`、`q`、もう一度 `?` のいずれかで閉じる。 |
| `j` / `k` | 選択を下 / 上に移動する(矢印キーでも可)。 |
| `[` / `]` | 前 / 次の Session グループへジャンプする。 |
| `/` | ロード済みの行を絞り込む(フリーテキストか `state:open` のような述語)。`Esc` は入力を抜けるだけで、一覧からもう一度 `Esc` を押すとフィルタを解除する。 |
| `n` | 新規 Session の form を開く(tmux popup または Herdr の inline form)。Mode 行で Prompt / Issue を切り替える。詳細は[新規 Session のモード](#新規-session-のモード)を参照。 |
| `s` | 設定を開く(tmux popup または Herdr の inline form)。user config / repo config を選び、`config.json` と同じキーを編集し、`Ctrl+S` で保存する。 |
| `Ctrl+O` | 新規 Session の Issue 一覧で、選択中の issue を既定ブラウザで開く。 |
| `a` | 選択中の行に記録された worktree に、agent ペインを 1 つ以上追加する。git worktree は作らない。追加行は選択元の worktree と branch を共有し、focus と peek はできるが merge 進捗には数えない。追加した agent は[新規 Session のペイン](#新規-session-のモード)と同じ launch posture を使う。 |
| `A` | 選択中の行に記録された worktree で shell terminal を開く。shell 行は `@manual` entry として記録され、focus と peek はできるが merge 進捗には数えない。 |
| `t` | project root で shell terminal を開く。tmux での close はペインと state 行だけを消し、git worktree は削除しない。Herdr の lifecycle close は未対応。 |
| `Enter` / `o` | 選択中の live 行のペインにフォーカスする。 |
| `1`-`9` | 表示リストの N 行目へジャンプして、そのペインにフォーカスする。範囲外の数字は notice を表示する。 |
| `Z` | tmux で選択中のペインにフォーカスして zoom する。ペインの作成や削除、window のリサイズで解除されるので、必要ならもう一度 `Z` を押す。 |
| `p` | detail panel の read-only 出力スナップショットを更新する。 |
| `v` | 表示の手動切替を auto → compact → full でサイクルする。auto は幅 80 桁未満で[コンパクト表示](#コンパクト表示)を選ぶ。compact は広い画面でも switcher を強制し、full は狭くてもテーブルを強制する。永続化はしない。 |
| `c` / `x` | 選択中のペインの close option を開く。ペインだけを閉じる、ペインと worktree を閉じる、local branch も削除する、から選ぶ。 |
| `m` | 選択中のペインの branch を fast-forward merge する(確認あり)。 |
| `X` | 同じ親の merged/closed な子をまとめて cleanup する(確認あり)。 |
| `q` | コンソールを離脱する。tmux session と子ペインはそのまま残る。 |

### コンパクト表示

幅 80 桁未満では、テーブル + detail panel が 1 ペイン 1 行の switcher に切り替わり、選択中の行だけが branch や PR、ci / wave / blockers / dirty などの詳細に展開されます。

### 設定 popup

`s` で、コンソールを離れずに fanout の JSON 設定を編集できます。
Target 行で user config と repo config を切り替えます。
各設定は値を指定するか `inherit` に戻せます。`inherit` は選択中のファイルからそのキーを削除します。
launch posture の設定は user config でのみ編集できます。
CLI flag と `FANOUT_*` 環境変数は、保存済みファイルより引き続き優先されます。
repo config の安全ルールも `config.json` と同じです。
repo config から launch posture と watcher は変更できず、HTTP 通知 endpoint も設定できません。
通知 channel は `bell`、`tmux`、`none` だけです。

### F11 / prefix + T

どの tmux ペインからでも **`F11`** または **`prefix + T`** でコンソールに戻れます(`F12` / `prefix + D` の対)が、live なコンソールが無いときはステータスラインに通知して終わります。
Herdr は tmux keybind を登録しません。

設定キー `consoleKeybind` または `FANOUT_CONSOLE_KEYBIND=0` で無効化できます([Settings]({{< relref "/docs/settings" >}}) を参照してください)。

### 新規 Session のモード

`n` は Mode 行つきの form を開き、Prompt / Issue を切り替えます。
tmux では popup、Herdr では inline form です。

この popup から Prompt、plan coordinator、Issue のいずれかを正常に起動すると、実際の作成順で先頭の新規ペインへフォーカスが移ります。
Issue fan-out では、tmux は orchestrator を先に作成し、Herdr は child ペインの後に作成するため、Herdr では最初の新規 child にフォーカスが移ります。
`F11` または `prefix + T` でコンソールに戻れます。
agent 追加(`a`)、shell(`A` / `t`)、watcher、通常の CLI 起動は、元のフォーカスを維持します。

**Prompt** は従来の manual ペインです。
複数行の prompt を書いて agent ごとの起動数を指定し(`claude` / `codex` / `opencode`)、prompt 欄では `Shift+Enter` または `Ctrl+J` で改行、`@` でリポジトリのファイルパス補完を使えます。
manual ペインでは、3 つの agent すべてに `newSessionPlanMode` を適用します。
既定値は `true` なので、Claude、Codex、OpenCode は Plan Mode で起動します。
下の plan fan-out チェックボックスを有効にすると、agent をちょうど 1 本選んだうえで、プロンプトを `fanout plan` で並列タスクに分解するコーディネータ 1 つの起動に切り替わります。
同じ設定により、Claude と Codex のコーディネータは Plan Mode で起動します。
OpenCode は同梱の `/fanout plan` command を読めずコーディネータになれないため、claude か codex を選んでください。

**Issue** はリポジトリの OPEN issue を一覧し、番号やタイトル、ラベルで絞り込めます。
`Ctrl+O` で選択中の issue を既定ブラウザで開けます。
issue を選んで既定の agent を決めると、`Enter` で子ごとに agent を切り替える割り当て画面が開きます(繰り返し指定の `--agent NUM=name` 相当)。
親 issue の Issue fan-out で orchestrator ペインを作成するときは、popup の既定 agent で project root に worktree なしで起動します。
tmux は start gate で待機する orchestrator を child ペインより先に作成し、start gate 非対応の Herdr は child ペインを先に作成します。
orchestrator は `orchestratorPlanMode`(既定 `true`)に従います。
codex の orchestrator は、子の fan-out 完了まで agent 起動を留める start gate と Plan Mode を両立できないため、fanout は警告して素の codex で起動します。
子は子ごとに割り当てた agent でファンアウトします。
briefing は orchestrator に、子スコープの実装を引き取らず、`fanout <N> --status` を定期的に実行して状態を確認し、親スコープ作業を担当するよう指示します。
全 child の merge 後は統合を行って最終集約コメントを投稿し、`--merge` / `--cleanup` で lifecycle 操作を進めます。
子の fan-out は `--unblocked-only` 相当なので、blocked な子は deferred のままです。
初回選択時に全 child が blocked なら、ペインは作成されません。
child の unblock 後に再選択すると orchestrator と child のペインを作成します。
orchestrator が作成済みなら、その後の再選択では重複せず、新たに unblock された子だけをファンアウトします。
child の launch posture は、選択した全 agent にそれぞれ適用されます。
子のない issue も同じ child posture を使い、`@watch` 配下の単独ペインとして起動します([Watcher]({{< relref "/docs/watcher" >}}) を参照)。

Prompt モードと同じ plan fan-out チェックボックスがここにもあります。
1 つの issue に対して有効にすると子への割り当て画面をスキップし、選んだ issue を issue-less な `fanout plan` タスクに分解するコーディネータ 1 つだけを起動します(Prompt モードと同じく `newSessionPlanMode` に従います。タスクは選択した agent で実行)。
チェックボックスはコーディネータと新しいタスクを作ります。
child の launch posture は、新しい task と既存の issue / Project の子の両方に適用されます。
選択中の issue に OPEN な子がある間、このチェックボックスは無効表示になります(その場合は子をファンアウトしてください)。

## --status（JSON / table / --post-dashboard）

進捗を CI や jq に食わせて判定したいなら `fanout <parent> --status` を使い、`.fanout/state.json` から指定 parent の子 issue を列挙して各子の状態と紐づく PR を JSON で出します(読み取り専用)。

```json
{ "parent": 123,
  "children": [
    { "num": 4, "state": "CLOSED",
      "prs": [ { "number": 250, "state": "MERGED",
                 "mergedAt": "2026-05-04T10:00:00Z",
                 "reviewDecision": "APPROVED", "ci": "pass" } ],
      "has_merged_pr": true },
    { "num": 7, "state": "OPEN", "prs": [], "has_merged_pr": false }
  ],
  "summary": { "total": 2, "merged": 1, "pending": 1,
               "blocked": 0, "all_merged": false } }
```

```bash
fanout 123 --status | jq '.summary.all_merged'
fanout 123 --status --format table       # 人が読みやすい表形式
fanout 123 --status --post-dashboard     # 親 issue に集約コメントを upsert
```

JSON の全 field と exit code は [CLI リファレンス]({{< relref "/docs/cli" >}}) を参照してください。
各子の行には、ペインを所有する runtime を示す `backend` field(`tmux` または `herdr`)が入ります。
`--format table` は PR 状態、CI、差分規模、変更ファイル数などを一覧にします。
`--status` 系で GitHub に書き込むのは `--post-dashboard` だけで、親 issue に集約コメントを 1 つ upsert して更新し続けます。

## Web ダッシュボード（fanout dashboard --web）

チームやブラウザで全 Session を共有しながら見たいときは、`fanout dashboard --web` で Web ダッシュボードを起動します。

```bash
fanout dashboard --web [--port N] [--open] [--no-token] [--no-keybind]
```

サーバは `127.0.0.1` にのみバインドし、起動毎にランダムトークンを生成して URL に埋め込みます(単一ユーザ端末では `--no-token` で外せます)。
読み取り系の endpoint はすべて GET 専用で、例外は後述のマージボタンだけです。
埋め込みの SPA は、フィルタと詳細ドロワー、直近出力の live peek でライブの Session 一覧を見せます。
Prompt Session の記録済み branch に PR があると、ダッシュボードに PR リンクと CI 状態も表示します。

`pr` 列にはその行の PR のレビュー状態が出ます。語彙は TUI と同じで、`merged` /
`closed` / `draft` / `approved` / `changes-requested` / `review-required` / `open`
です。隣には base branch と競合している PR を示す `conflict` タグと、会話コメントと
inline レビューコメントを合算したコメント件数が並びます。詳細ドロワーでは、primary
PR だけでなくその行のすべての PR について同じ 3 つが出ます。
conflict タグは GitHub が競合を報告したときだけ出ます。merge 済み・close 済みの PR は
マージ可能性を持たず、base に push した直後の再計算中も同じく持ちません。

列に出ている語はそのままフィルタに打てます。`pr:` はライフサイクル状態（`open` /
`closed` / `merged`）とピルのラベル（`approved` / `changes-requested` /
`review-required` / `draft`）の両方を取り、`conflict` は自由語で当たります。

`review:` は別の軸です。レビュー状態とライフサイクル状態は問いが違うためです。
`pr:open` は PR がライフサイクルのどこにいるかを、`review:approved` はレビューを
通ったかを訊きます。approved な PR は、ピルが `merged` に潰れたあとも
`review:approved` に当たります。`review:` が取る値は `approved` /
`changes-requested` / `review-required` / `none` です。どちらもダッシュボードの
フィルタで、TUI は別のフィルタ文法を持ち `review:` は受け付けません。

Session の各行には runtime backend と pane の identity が出て、runtime の状態は `live` / `stale` / `unknown` / `unsupported` / `-` で示されます。フィルタは `backend:tmux` / `backend:herdr` を受け付けます。
[herdr backend]({{< relref "/docs/herdr-backend" >}}) の行では、保存済みの行が fanout-owned session の pane と一致する場合だけ live peek が内容を返します。
foreign または stale な行は 404 を返し、ダッシュボードは GET 専用のままです。

### PR をマージする

PR が紐付いている Session には**マージ**ボタンが出ます。置き場所は詳細ドロワーのヘッダと diff ビュアーのツールバーの 2 か所です。押すとその PR を GitHub 上でマージします。
ローカルは何も変わりません。worktree もその branch も記録済みの状態もそのまま残り、ペインを畳むのは従来どおり `--close` / `--cleanup` の仕事です。

`▾` を押すとマージ方式(squash / merge commit / rebase)が出ます。選ぶとその方式でマージし、次回の既定として覚えます。この選択はドロワーと diff ビュアーで共通です。

マージが終わると、詳細ドロワーの PR の隣に**ブランチを削除**ボタンが現れます。GitHub 自身の "Delete branch" と同じ扱いです。消えるのは GitHub 側の branch だけで、worktree とローカル branch はそのまま残ります(それらは従来どおり `--cleanup` の担当)。fork の branch と、マージ後に動いた branch は対象外なので、未マージのコミットを巻き込むことはありません。

マージが成立しない状態ではボタンが理由付きで無効になります。PR が無い、すでにマージ済み、close 済み、draft のまま、base branch と競合している、の 5 つです。
CI の失敗やレビュー未承認では無効になりません。それらがマージを止めるかどうかは branch protection の設定次第なので、ボタンは押せるままにしてメニューに警告を出します。
GitHub が拒否した場合はその旨をエラーで表示し、PR は変わりません。

ダッシュボードは描画時に見ていた PR 番号と head commit を送り、サーバはその commit を `--match-head-commit` として GitHub に渡します。
ページを開いてからクリックするまでの間に push された PR は、そのままマージされるのではなく拒否されます。

diff ビュアーを開いている間は、開いた時点の PR(番号・head・base)を固定し、画面の差分を取ったコミットが PR の head と同じであることも要求します。読んでいる最中の push、head を動かさない base の付け替え、ローカル worktree の遅れ — どれもマージを塞ぎます。diff を開き直すまで実行できません。

merge queue が必要な base branch では、GitHub はリクエストを受け付けてもその場ではマージしません。その場合はボタンにその旨が出て、二重送信を防ぐため無効になります — 受理された時点で auto-merge は武装済みです。GitHub がマージを確認するまで、マージ済みとは報告せず、ブランチも削除しません。

このボタンがある以上、ダッシュボードの URL は閲覧権限ではなくマージ権限を持ちます。URL に埋め込まれたトークンが、URL の共有と他人によるマージとの間に立つ唯一のものです。
そのため `--no-token` を付けるとマージボタンは無効になります。単一ユーザ端末で読み取りを開けておくのは構いませんが、loopback ポートは同一マシンの全プロセスから届くので、マージだけは開けません。

### diff ビュアー

Session 行の diff 列、または詳細ドロワー上部の**変更を表示**から、その worktree の
merge-base 基準の変更を読めます。
`+0/-0` に見える行も開けます(binary だけ、mode だけ、rename だけの変更は行数に
出ません)。
サイドバーに変更ファイルがディレクトリごとにまとめて並び、追加・削除行数が付きます。
移動したファイルは 1 行にまとまり、移動元のファイル名が並びます。
各行の先頭には、そのファイルに何が起きたかを示す円のアイコンが付きます。プラスが
新規追加、点が変更、マイナスが削除、矢印が移動です。行数では表せません — 空の
新規ファイルは `+0/-0`、全行書き換えは `+N/-N` になるためです。
ファイル名をクリックするとその差分へ飛べます。
サーバが patch を省略したファイル(バイナリ、サイズ上限超過)は、サイドバーではなく
警告帯の下に理由付きでまとまります。

各ファイルは既定でシンタックスハイライト付きで展開されます。
折りたたまれた状態で始まるのは、diff の描画行数が 1,000 行以上のファイルだけです
(hunk 内の context 行も数えます。変更行だけではありません)。
展開してもハイライトはそのままです。
ただし非常に大きなファイルはプレーンテキストで描画します(描画される diff の
総文字数が 150,000 字、または描画行数が 20,000 行を超えるとハイライトを落とします。
どちらも hunk 内の context を含む描画対象の量です。トークン化は表示中の行ではなく
ファイル全体に走るためです)。
表示中の行だけを描画するので、数千行の diff でも操作は軽いままです。
スクロール中もファイル名は上端に固定され、長い行は折り返します。
左右 2 面のとき、追加のみ・削除のみのファイルは全幅ではなく片側に寄せます。

読み終えたファイルはファイル名ヘッダの**確認済み**にチェックを入れると畳めます。
サイドバーではその行が淡くなり、末尾にチェックが付き、見出しに残りの件数が出ます。
チェックは Session 行ごとにブラウザへ保存され、開き直しても再読み込みしても
残ります(保存はポートを含む URL 単位です。上の表示モードと同じ注記が効きます)。
そのファイルの差分が変わると自動で外れ、変わっていないファイルのチェックは
残るので、更新分だけを読み直せます。
目のアイコンを押すと、確認済みのファイルを本文と一覧の両方から隠せます。
隠したファイルのチェックは外せません — 外すにはボタンを戻してください。

既定はコンパクト表示で、詳細ドロワーの左隣にパネルが立ちます。
パネルの左端はドラッグで画面幅の 95% まで広げられます(詳細ドロワーに被さります)。
コンパクト中も、一覧が少しでも見えていれば Session 一覧やドロワーをそのまま
触れるので、ペインの出力を見ながら差分を追えます。
一覧が完全に隠れると全画面と同じ扱いになります(1,100px 以下では nav 下の全幅に
なり、それより広い画面でもドロワーとパネルが広ければ一覧が残りません)。
斜め矢印のアイコンで全画面と行き来できます。
選んだ表示と幅はブラウザに保存されます(保存はポートを含む URL 単位です。
`fanout dashboard --web` は既定で OS 任せのポートを使うため、再起動すると別扱いに
なって設定が初期値に戻ります。持ち越したいときは `--port N` を固定してください)。

削除と追加の並べ方は表示モードではなくパネルの幅で決まります。既定では幅
1,000px を境に、狭ければ縦積み、広ければ左右 2 面です(ウィンドウが狭ければ
全画面でも縦積みになります)。
枠アイコンのボタンを押すと自動 → 左右 2 面 → 縦積みの順に切り替わり、明示的に
選ぶと幅に関係なくその並べ方で固定されます。

ヘッダとサイドバーのボタンはアイコンで、ポインタを乗せると説明が出ます。

### 設定

右上の歯車から設定を開けます。
言語は自動 / 日本語 / English の 3 択です。自動はブラウザの言語に追従し、明示的に
選ぶとその言語で固定されます。
外観はシステム / ライト / ダークの 3 択で、diff ビュアーの
シンタックステーマはライト用・ダーク用を別々に、それぞれ 9 種(Pierre、GitHub、
Catppuccin、Gruvbox、Tokyo Night ほか)から選べます。
どちらも見本付きで、diff テーマは実際の差分表示そのままの配色を確認してから選べます。
設定はブラウザに保存され、同じ URL(ポートを含む)で開いたときに引き継がれます
(上のポートの注記を参照)。

表示は日本語と英語に対応します。表ヘッダ・タグ・フィルタの選択肢はどちらの言語でも
英語のままです。これらはフィルタ構文そのもので、`state:open` や `ci:fail` と打つ語と
表示が食い違わないようにしています。

### F12 / prefix + D

どのペインからでも **`F12`** または **`prefix + D`** でダッシュボードを開けます。
`--no-dashboard-keybind`(fan-out 側)、`--no-keybind`(dashboard 側)、設定キー `dashboardKeybind`、`FANOUT_DASHBOARD_KEYBIND=0` で無効化できます([Settings]({{< relref "/docs/settings" >}}) を参照してください)。

### prefix + M

同じ登録処理で **`prefix + M`** も登録し、記録済みの fanout ペインから押すとその worktree に agent を追加するか shell を開ける popup が開きます。

### 縮退動作

- `gh` 未ログインの場合は、バナーを出して state のみのビューを表示します。
- tmux 外でも配信は続き、ペインの生存は unknown のままになります。
- herdr の行は保存済みの identity を `herdr api snapshot` と照合して生死と agent state を反映します。行に `agent_session` がない場合は、expected provider の一意で有効な最初の ref を owning state lock 下で永続化し、以後の観測で完全一致を要求します。その他の identity field は snapshot から補完しません。出力の peek は、この repository の fanout-owned Herdr session に属する live row だけ内容を返します。

このページに登場するフラグの一覧は [CLI リファレンス]({{< relref "/docs/cli" >}}) にあります。
