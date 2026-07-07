---
title: 進捗モニタリング
linkTitle: モニタリング
description: "ファンアウトした全ペインの進捗を一望し、どこで止まっているかを見つける 3 つの窓。常駐 TUI コンソール、--status の JSON と table、読み取り専用の Web ダッシュボード。"
weight: 40
kanji: 見
yomi: monitoring
---

子 issue を 5 つファンアウトすると、tmux には 5 枚のペインが並び、それぞれ別のエージェントが別の worktree で動きます。
知りたいのは、どのペインが PR まで進み、どこで止まっているかです。

fanout はこれを 3 つの窓で見ます。
手元で常時眺めるなら**常駐 TUI コンソール**、automation に食わせるなら `--status` の **JSON**、チームやブラウザで共有するなら読み取り専用の **Web ダッシュボード**です。
`--status` と Web ダッシュボードは読み取り専用で `.fanout/state.json` と tmux と GitHub を読むだけですが、TUI はキー操作から merge、close、cleanup も実行できます(`--merge` / `--close` / `--cleanup` と同じ処理です)。

## ペイン枠線ラベル

3 つの窓を見る前に、tmux 自体がペインを見分けさせてくれます。
fanout は作成した各ペインの上枠に `<parent> · <name>` のラベルを付け(issue の子なら `#123 · fix-login-bug-123`、plan タスクなら `plan:my-feature · task-slug`)、枠線を fanout のテーマ色に染めます(アクティブは浅葱、それ以外は藍)。
タイル状に並んだ window を一瞥するだけで、どのペインがどの子か、フォーカスせずに分かります。

## 常駐 TUI コンソール

手元で全ペインを眺め続けるなら、引数なしの `fanout` で常駐コンソールを起動します。

```bash
fanout   # start the persistent tmux console
```

素のシェルから起動すると fanout 管理の tmux session を作成または attach し、tmux 内から起動すると現在のペインをそのままコンソールにします。
コンソールは `.fanout/state.json` を読み、記録済みペインの issue と PR の状態を定期更新し、各行の worktree には変更量を `+X/-Y`、未 commit の作業の有無を `dirty` / `clean` で示します。
`RUN` 列には agent の実行状態がグリフで出て(起動ラッパー由来の `●` running・`✓` done に加え、agent hooks が報告すると `◐` working・`◇` plan・`◆` blocked・`○` idle)、detail panel には同じ値が `run=` として出ます。

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
| `?` | キーボードショートカットのヘルプを tmux popup で開く。`Esc`、`q`、もう一度 `?` のいずれかで閉じる。 |
| `j` / `k` | 選択を下 / 上に移動する(矢印キーでも可)。 |
| `[` / `]` | 前 / 次の Session グループへジャンプする。 |
| `/` | ロード済みの行を絞り込む(フリーテキストか `state:open` のような述語)。`Esc` は入力を抜けるだけで、一覧からもう一度 `Esc` を押すとフィルタを解除する。 |
| `n` | 新規 Session の tmux popup を開く。Mode 行で Prompt / Issue を切り替える。詳細は[新規 Session のモード](#新規-session-のモード)を参照。 |
| `s` | 設定の tmux popup を開く。user config / repo config を選び、`config.json` と同じキーを編集し、`Ctrl+S` で保存する。 |
| `a` | 選択中の行に記録された worktree に、agent ペインを 1 つ以上追加する。git worktree は作らない。追加行は選択元の worktree と branch を共有し、focus と peek はできるが merge 進捗には数えない。`codex` は Codex Plan Mode で起動する。 |
| `A` | 選択中の行に記録された worktree で shell terminal を開く。shell 行は `@manual` entry として記録され、focus と peek はできるが merge 進捗には数えない。 |
| `t` | project root で shell terminal を開く。close は tmux ペインと state 行だけを消し、git worktree は削除しない。 |
| `Enter` / `o` | 選択中の live 行のペインにフォーカスする。 |
| `1`-`9` | 表示リストの N 行目へジャンプして、そのペインにフォーカスする。範囲外の数字は notice を表示する。 |
| `Z` | 選択中のペインにフォーカスして zoom する。ペインの作成や削除、tmux window のリサイズで解除されるので、必要ならもう一度 `Z` を押す。 |
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
CLI flag と `FANOUT_*` 環境変数は、保存済みファイルより引き続き優先されます。
repo config の安全ルールも `config.json` と同じです。repo config から watcher や HTTP 通知 endpoint は有効化できず、通知 channel は `bell`、`tmux`、`none` だけです。

### F11 / prefix + T

どのペインからでも **`F11`** または **`prefix + T`** でコンソールに戻れます(`F12` / `prefix + D` の対)が、live なコンソールが無いときはステータスラインに通知して終わります。

設定キー `consoleKeybind` または `FANOUT_CONSOLE_KEYBIND=0` で無効化できます([Settings]({{< relref "/docs/settings" >}}) を参照してください)。

### 新規 Session のモード

`n` は Mode 行つきの tmux popup を開き、Prompt / Issue を切り替えます。

**Prompt** は従来の manual ペインです。
複数行の prompt を書いて `claude` / `codex` の起動数を指定し、prompt 欄では `Shift+Enter` または `Ctrl+J` で改行、`@` でリポジトリのファイルパス補完を使えます。
manual の `codex` ペインは Codex Plan Mode で起動し、prompt を inline で受け取ります(`claude` は通常起動)。
下の plan fan-out チェックボックスを有効にすると、agent をちょうど 1 本選んだうえで、プロンプトを `fanout plan` で並列タスクに分解するコーディネータ 1 つの起動に切り替わります(コーディネータは `codex` でも常に通常 agent として起動します)。

**Issue** はリポジトリの OPEN issue を一覧し、番号やタイトル、ラベルで絞り込めます。
issue を選んで既定の `claude` / `codex` を決めると、`Enter` で子ごとに agent を切り替える割り当て画面が開きます(繰り返し指定の `--agent NUM=name` 相当)。
OPEN な子を持つ issue は `--unblocked-only` 相当でファンアウトし、blocked な子は deferred のまま残ります。
子のない issue は `@watch` 配下の単独ペインとして起動します。

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
`--format table` は PR 状態、CI、差分規模、変更ファイル数などを一覧にします。
`--status` 系で GitHub に書き込むのは `--post-dashboard` だけで、親 issue に集約コメントを 1 つ upsert して更新し続けます。

## Web ダッシュボード（fanout dashboard --web）

チームやブラウザで全 Session を共有しながら見たいときは、`fanout dashboard --web` で読み取り専用の Web ダッシュボードを起動します。

```bash
fanout dashboard --web [--port N] [--open] [--no-token] [--no-keybind]
```

サーバは `127.0.0.1` にのみバインドして GET 専用の endpoint だけを公開し、起動毎にランダムトークンを生成して URL に埋め込みます(単一ユーザ端末では `--no-token` で外せます)。
埋め込みの SPA は、フィルタと詳細ドロワー、直近出力の live peek でライブの Session 一覧を見せます。

### F12 / prefix + D

どのペインからでも **`F12`** または **`prefix + D`** でダッシュボードを開けます。
`--no-dashboard-keybind`(fan-out 側)、`--no-keybind`(dashboard 側)、設定キー `dashboardKeybind`、`FANOUT_DASHBOARD_KEYBIND=0` で無効化できます([Settings]({{< relref "/docs/settings" >}}) を参照してください)。

### prefix + M

同じ登録処理で **`prefix + M`** も登録し、記録済みの fanout ペインから押すとその worktree に agent を追加するか shell を開ける popup が開きます。

### 縮退動作

- `gh` 未ログインの場合は、バナーを出して state のみのビューを表示します。
- tmux 外でも配信は続き、ペインの生存は unknown のままになります。

このページに登場するフラグの一覧は [CLI リファレンス]({{< relref "/docs/cli" >}}) にあります。
