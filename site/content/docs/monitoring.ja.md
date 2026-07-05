---
title: 進捗モニタリング
linkTitle: モニタリング
description: "ファンアウトした全ペインの進捗を一望し、どこで止まっているかを見つける 3 つの窓。常駐 TUI コンソール、--status の JSON と table、読み取り専用の Web ダッシュボード。"
weight: 40
kanji: 見
yomi: monitoring
---

子 issue を 5 つファンアウトすると、tmux には 5 枚のペインが並び、それぞれ別のエージェントが別の worktree で動き出します。次に知りたいのは「どのペインが PR まで進んだか」「どこで止まっているか」「未 commit の作業を抱えたまま放置されているペインはないか」です。fanout はこれを 3 つの窓で見ます。手元で常時眺めるなら**常駐 TUI コンソール**、automation に食わせるなら `--status` の **JSON**、チームやブラウザで共有するなら読み取り専用の **Web ダッシュボード**。`--status` と Web ダッシュボードは読み取り専用で、`.fanout/state.json` と tmux と GitHub を読むだけです。TUI も同じように眺めるための窓ですが、キーバインドから merge、close、cleanup も実行できます（`--merge` / `--close` / `--cleanup` と同じ経路）。

## 常駐 TUI コンソール

手元で全ペインを眺め続けるなら、引数なしの `fanout` で常駐コンソールを起動します。

```bash
fanout   # start the persistent tmux console
```

素のシェルから起動したときは、現在のリポジトリ用の deterministic な fanout 管理 tmux session を作成または attach し、その session 内でコンソールを開始します。tmux 内から起動したときは、現在のペインをそのままコンソール画面にします。起動時と state 更新ごとに、コンソールは tmux 上に残っている記録済み worktree ペインへ再バインドし、消えた worktree ペインは agent CLI の resume で作り直します。使うのは `claude --continue`、`codex resume --last`、または Plan ペインに保存した Codex Plan Mode thread です。

コンソールは `<git-root>/.fanout/state.json` を読み、記録済みペイン ID が tmux 上にまだ存在するかを確認し、`fanout <parent> --status` と同じ GitHub CLI 経路で issue と closed-by PR の状態を定期更新します。エージェント側に計測の仕込みは要りません。各行にはペインの worktree の総作業量を `+X/-Y` で示します。これは記録した base ブランチとの merge-base に対する `git diff --shortstat` で、コミット済みと未 commit の合計です（base 未記録の旧行は `origin/HEAD` から `HEAD` に fallback します）。あわせて `git status --porcelain` 由来の `dirty` / `clean` を出すので、未 commit の作業を抱えたペインをその場で見分けられます。

行が増えてきたら `/` で絞り込みます。ロード済みの行をメモリ内でフィルタでき、フリーテキストのほか `state:open`、`agent:codex`、`wave:wave5` のような述語も使えます。フィルタは追加の fetch を起こさず、絞り込み中も state と GitHub の自動更新は続きます。

記録済みの issue 親については、親の子一覧も再読込し、`--unblocked-only` と同じ `## Blocked by` と `(blocked by #N)` のソースから wave 列と blocker 列を出します。まだファンアウトしていない blocked な子は `deferred` 行として並び、CLOSED の blocker は resolved として区別されます。ヘッダーには `total` / `merged` / `pending` / `blocked` の集約 count が出ます。

### キー操作

footer は短く保ちます。通常画面で `?` を押すと、全ショートカットのヘルプが開きます。

| キー | 動作 |
|---|---|
| `?` | キーボードショートカットのヘルプを開く。`Esc`、`q`、もう一度 `?` のいずれかで閉じる。 |
| `n` | 新規 Session の tmux popup を開く。Mode 行で Prompt / Issue を切り替える。詳細は[新規 Session のモード](#新規-session-のモード)を参照。 |
| `a` | 選択中の行に記録された worktree に、agent ペインを 1 つ以上追加する。git worktree は作らない。追加行は選択元の worktree と branch を共有し、focus と peek はできるが merge 進捗には数えない。`codex` は Codex Plan Mode で起動する。 |
| `A` | 選択中の行に記録された worktree で shell terminal を開く。shell 行は `@manual` entry として記録され、focus と peek はできるが merge 進捗には数えない。 |
| `t` | project root で shell terminal を開く。close は tmux ペインと state 行だけを消し、git worktree は削除しない。 |
| `Enter` / `o` | 選択中の live 行のペインにフォーカスする。 |
| `1`-`9` | 表示リストの N 行目へジャンプして、そのペインにフォーカスする。範囲外の数字は notice を表示する。 |
| `Z` | 選択中のペインにフォーカスして zoom する（`resize-pane -Z`）。次の relayout（ペインの作成・削除、tmux window のリサイズ）で zoom は解除されるので、必要ならもう一度 `Z` を押す。 |
| `p` | detail panel の read-only 出力スナップショットを更新する。 |
| `c` / `x` | 選択中のペインの close option を開く。ペインだけを閉じる、ペインと worktree を閉じる、local branch も削除する、から選ぶ。 |
| `m` | 選択中のペインの branch を fast-forward merge する（確認を挟み、`--merge` と同じコア処理を使う）。 |
| `X` | 同じ親の merged/closed な子をまとめて cleanup する（確認を挟み、`--cleanup` と同じコア処理を使う）。 |
| `q` | コンソールを離脱する。tmux session と子ペインはそのまま残る。 |

> worktree 行が `stale!` になるのは、worktree が無い、agent command が無いなど fanout が復元できない場合です。shell terminal は resume できないため、対応する tmux ペインが無ければ TUI が state 行を削除します。

### F11 / prefix + T

コンソール起動時に fanout が tmux キーバインドを登録するので、どのペインからでも **`F11`** または **`prefix + T`** でコンソールに戻れます。ダッシュボードの `F12` / `prefix + D` と対になる復帰キーです。どちらのキーも `fanout focus-console` を実行します。押したペインのリポジトリに記録された live コンソールを優先し（1 つの tmux サーバに複数リポジトリのコンソールが同居しても正しく着地します）、無ければ同一セッションのコンソールに切り替えます。live なコンソールが無いときはステータスラインに通知して終わります。

登録は設定キー `consoleKeybind` または `FANOUT_CONSOLE_KEYBIND=0` で無効化できます（[Settings]({{< relref "/docs/settings" >}}) を参照してください）。

### 新規 Session のモード

`n` は Mode 行つきの tmux popup を開きます。Mode 行で `Left` / `Right` を押すとモードが切り替わり、`Tab` でフィールドを移動し、`Esc` でキャンセルします（agent 割り当て画面では 1 つ前に戻る）。

- **Prompt** — 従来の manual ペイン。複数行の必須 prompt と `claude` / `codex` の起動数を指定する。`Up` / `Down` で agent 行を選び、`Space` で 0 / 1 を切り替え、`Left` / `Right` で起動数を変える。`codex` は Codex Plan Mode で起動し popup の prompt を inline で受け取る。`claude` は通常起動する。prompt の下の **plan fan-out** チェックボックスを入れると起動内容が変わる。agent をちょうど 1 本選んで有効にすると、project root にコーディネータ pane を 1 つ起動し、そのプロンプトに対して `/fanout plan`（claude）または `$fanout-plan`（codex）を実行して並列タスクに分解する。コーディネータは自身で `fanout plan` を走らせるため、常に通常 agent として起動する — plan fan-out では `codex` でも Codex Plan Mode にはならない。prompt 欄では `Shift+Enter` または `Ctrl+J` で改行し、`Enter` でペインを作成する。enhanced keyboard input は既定で有効（`FANOUT_TUI_ENHANCED_KEYS=0` で無効化）で、`Shift+Enter` を区別して送る terminal が必要なため fanout が tmux の `extended-keys` を有効化する。manual ペインは synthetic な `@manual` state entry として記録され、起動後に一覧へ表示される。
- **Issue** — リポジトリの OPEN issue を全件一覧する（cursor ページングで取得）。文字入力で番号・タイトル・ラベルで絞り込み、`Up` / `Down` で一覧をスクロールする。すでにペインが記録されている行は `(has session)` と表示されるが選択はできる。各行の先頭には GitHub Sub-issues グラフ上の位置を示すマーカーが付く — `▸` は OPEN な子を持つファンアウト親、`└` は子、`·` は単独。マーカーは Sub-issues リンクだけを見るので、子を本文のタスクリスト行（`- [ ] #N`）で管理している親は `·` と表示されても起動時にはファンアウトする。一覧の下の **Agent** 行でファンアウトの既定 agent を `claude` / `codex` の起動数として選ぶ — Prompt モードと同じカウント式表示だが、常にちょうど 1 つが `[1]`。`Up` / `Down` で行を移動し、`Space` / `Left` / `Right` で選択する。`Enter` で子 issue ごとの agent 割り当て画面が開き、`Left` / `Right` で行の agent を切り替え — 繰り返し指定の `--agent NUM=name` と同じ — もう一度 `Enter` で起動する。OPEN な子を持つ issue は `fanout <issue> --unblocked-only` 相当でファンアウトする。blocked な子は deferred のまま残るので、ブロッカーが閉じたら同じ issue を選び直す（全子を一度に起動したい場合は CLI を使う）。子のない issue は `@watch` 配下に記録される単独ペインを起動する。

## --status（JSON）

進捗を CI や jq に食わせて判定したいなら、`fanout <parent> --status` を使います。これは読み取り専用です。`.fanout/state.json` から指定 parent ですでにファンアウト済みの子 issue を列挙し、各子について `gh api graphql` で `repository.issue.closedByPullRequestsReferences(first: 100)` を照会して（子を close する PR が 100 件を超える場合は cursor でページング）、`state`、`mergedAt`、`reviewDecision`、最新 commit の CI rollup を取得します。既定では JSON ドキュメントを 1 つ stdout に出力します。

```json
{
  "parent": 123,
  "children": [
    { "num": 4, "state": "CLOSED",
      "prs": [ { "number": 250, "state": "MERGED",
                 "mergedAt": "2026-05-04T10:00:00Z",
                 "reviewDecision": "APPROVED", "ci": "pass" } ],
      "has_merged_pr": true },
    { "num": 7, "state": "OPEN",
      "prs": [],
      "has_merged_pr": false }
  ],
  "summary": {
    "total":      2,
    "merged":     1,
    "pending":    1,
    "blocked":    0,
    "all_merged": false
  }
}
```

この JSON は automation 向けです。

```bash
fanout 123 --status | jq '.summary.all_merged'
```

複数の親をファンアウトした state file では、他の親の子はフィルタされ、`summary.all_merged` は指定した親だけを反映します。リポジトリのチェックアウト外から読むなら `FANOUT_STATE_PATH` で state file を直接指定できます。指定が無ければ fanout は `<git-root>/.fanout/state.json` を読みます。

`--status` の exit code は通常フローとは別レーンです。

| Exit code | 意味 |
|---|---|
| `0` | status を出力した（JSON mode では実際の状態を `summary.all_merged` で確認する） |
| `2` | 列挙できない: 不正な呼び出し、読めない / 壊れた state file、使えない project root、Projects v2 URL を parent に指定。state file が無い場合は空 state として扱う |
| `3` | `gh` API 呼び出しが失敗した（認証、ネットワーク、存在しない issue など） |

> **issue parent 専用:** 現在の JSON schema は issue parent 用なので、Projects v2 URL を parent にした `--status` は最初に拒否されます。

## --status --format table

同じデータを人が一覧で読みたいときは `--format table` を付けます。

```bash
fanout 123 --status --format table
```

JSON に加えて、正規化した PR 状態（`open`、`draft`、`review-required`、`approved`、`changes-requested`、`merged`、`closed`）、CI、PR の差分バー、変更ファイル数、Conventional-Commit 種別、PR リンクが並びます。

## --status --post-dashboard

進捗を親 issue 上で共有したいときは `--post-dashboard` を使います。

```bash
fanout 123 --status --post-dashboard
```

このオプションは親 issue に marker 付きコメントを 1 つ upsert し、各子 PR の sub-issue 番号、PR リンク、PR 状態、CI、差分規模、Conventional-Commit 種別、TL;DR、`Review effort` score を集約します。集約は GitHub の機械可読データと PR 本文だけから作り、LLM は呼びません。

コメント本文の先頭に `<!-- fanout:dashboard parent=N -->` を置き、paginated な GitHub REST comments endpoint で既存の marker コメントを探して、そのコメントだけを更新します。marker コメントが無い場合は `gh issue comment --body-file -` で新規作成します。

> `--post-dashboard` は `--status` 系で唯一 GitHub に書き込むオプションです。

## Web ダッシュボード（fanout dashboard --web）

チームやブラウザで全 Session を共有しながら見たいときは、`fanout dashboard --web` で**読み取り専用**の Web ダッシュボードを起動します。fanout の **Session**（`.fanout/state.json` に記録されたペインを親 issue 単位でまとめたもの）をブラウザに出し、SSE でライブ更新します。各行ではペインの生存（`tmux list-panes`）、ライブの tmux ペインのタイトル、`running` / `done` の agent 実行状態バッジ、wave 列と未解決 blocker 列（親 issue グラフ由来）、issue 状態、PR マージ状態、CI 状態、diff/dirty を見られます。データ源は `--status` と同じで、リポジトリ内の全親について一度に再利用します。まだファンアウトしていない子 issue は synthetic な未開始行として並びます。GitHub の状態は一切変更せず、tmux も*読み取る*だけです。便宜は 2 つです。起動中サーバを `.fanout/dashboard.json` に記録して 2 回目の起動で再利用すること、そして後述の tmux キーバインドを登録すること（`--no-keybind` でオプトアウト可）です。

```bash
fanout dashboard --web [--port N] [--open] [--no-token] [--no-keybind]
```

- **localhost 限定。** サーバは `127.0.0.1` にのみバインドし、GET 専用の endpoint を公開します。内訳は `/api/snapshot`、SSE の `/api/stream`、`/api/peek`（記録ペイン 1 つの `tmux capture-pane` スナップショット）、`/api/plan`（`--codex-plan-mode` ペインの最後の完全な `<proposed_plan>` ブロック）、埋め込み UI です。`--port` は既定 `0`（OS 割り当ての ephemeral port）で、確定した URL が表示されます。
- **トークン既定 ON。** 起動毎にランダムトークンを生成し、表示 / オープンされる URL に埋め込んで `/api/*` をゲートします。同一ホストの他ユーザや他プロセスがループバックポート経由で issue/PR データを読むのを防ぎます。単一ユーザ端末では `--no-token` で外せます。
- **`--open`** は既定ブラウザで URL を開きます。すでに起動中のサーバ（`.fanout/dashboard.json` に記録）があればそれを再利用し、二重起動しません。
- **SPA。** 埋め込みの React+Vite SPA は、API 単体よりも多くの情報をライブ Session 一覧に重ねます。まず構造化フィルタ欄があり、自由語と `state:` / `run:` / `agent:` / `wave:` / `ci:` / `dirty:` / `live:` / `issue:` / `task:` / `pr:` の各 term を AND で絞り込めて、ドロップダウンと外せるチップが付きます。行をクリックすると詳細ドロワーが開き、ペインのメタ情報、wave と blocker、worktree、CI 付き PR、元プロンプト、5 秒ごとに更新される直近出力のライブ *peek* を見られます。`--codex-plan-mode` ペインには *plan* セクションが付きます。上部の HUD は repo 全体の running 数と blocked 数を出します。テーマは PAPER BREEZE のライト/ダークです。

全フラグは `fanout dashboard --help` を参照してください。

### F12 / prefix + D

TUI 起動時、ライブ fan-out 後、ダッシュボード自体の起動時に fanout が tmux キーバインドを自動登録するので、どのペインからでも **`F12`** または **`prefix + D`** でダッシュボードを開けます。どちらのキーも detached な `fanout-dashboard` ウィンドウでサーバを起動するため、キー押下後も生き続け、2 回目以降は既存 URL を開き直すだけです。**`prefix + M`** では記録済みペインから同一 worktree 操作を開けます。

ブラウザ opener が失敗した場合、fanout は tmux status line に dashboard URL を表示します。

自動登録は `--no-dashboard-keybind`（fan-out 側）、`--no-keybind`（dashboard 側）、設定キー `dashboardKeybind`、`FANOUT_DASHBOARD_KEYBIND=0` でまとめて無効化できます（[Settings]({{< relref "/docs/settings" >}}) を参照してください）。

### prefix + M

同じ登録処理で **`prefix + M`** も登録します。記録済みの fanout ペインから押すと、そのペインの worktree 用 popup が開きます。同じ worktree に別 agent を追加するか、その worktree で shell を開けます。未記録ペインや worktree path がないペインは拒否します。

### 縮退動作

- `gh` 未ログインの場合は、バナーを出して state のみのビューを表示します。
- tmux 外でも配信は続き、ペインの生存は unknown のままになります。

このページに登場するフラグの一覧は [CLI リファレンス]({{< relref "/docs/cli" >}}) にあります。
