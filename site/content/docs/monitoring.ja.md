---
title: 進捗モニタリング
linkTitle: モニタリング
description: "ファンアウトを見守る 3 つの窓 — 常駐 TUI コンソール、--status の JSON / table、そして読み取り専用の Web ダッシュボード。"
weight: 40
kanji: 見
yomi: monitoring
---

## 常駐 TUI コンソール

引数なしの `fanout` で常駐コンソールを起動します:

```bash
fanout   # start the persistent tmux console
```

素のシェルから起動した場合は、現在のリポジトリ用の deterministic な fanout 管理 tmux session を作成または attach し、その session 内でコンソールを開始します。tmux 内から起動した場合は、現在の pane をそのままコンソール画面にします。

コンソールは `<git-root>/.fanout/state.json` を読み、記録済み pane ID が tmux 上にまだ存在するかを確認し、`fanout <parent> --status` と同じ GitHub CLI 経路で issue / closed-by PR 状態を定期更新します。各行には pane worktree の `git diff --shortstat HEAD` による `+X/-Y` と、`git status --porcelain` による `dirty`/`clean` も表示するため、agent 側の instrumentation なしで未 commit 作業を確認できます。

`/` を押すとロード済み行をメモリ内でフィルタでき、フリーテキストのほか `state:open`、`agent:codex`、`wave:wave5` のような述語も使えます。フィルタが追加 fetch を発生させることはなく、フィルタ中も state / GitHub の自動更新は継続します。

記録済みの issue 親については親の子一覧も再読込し、`--unblocked-only` と同じ `## Blocked by` / `(blocked by #N)` ソースから wave / blocker 列を表示します。まだ fanout されていない blocked 子は `deferred` 行として表示され、CLOSED blocker は resolved として区別されます。ヘッダーには `total` / `merged` / `pending` / `blocked` の集約 count が並びます。

### キー操作

| キー | 動作 |
|---|---|
| `n` | 必須 prompt、`claude` / `codex` の agent 選択、任意 slug を指定して manual agent pane を作成する。manual pane は synthetic な `@manual` state entry として記録され、起動後に一覧へ表示される。 |
| `Enter` / `o` | 選択中の live 行の pane にフォーカスする。 |
| `p` | detail panel の read-only 出力スナップショットを更新する。 |
| `c` | 選択中の pane を close する — 確認を挟み、`--close` と同じコア処理を使う。 |
| `m` | 選択中の pane の branch を fast-forward merge する — 確認を挟み、`--merge` と同じコア処理を使う。 |
| `x` | 同じ親の merged/closed 子をまとめて cleanup する — 確認を挟み、`--cleanup` と同じコア処理を使う。 |
| `q` | コンソールを離脱する。tmux session と子 pane はそのまま残る。 |

> 記録はあるものの tmux 上に存在しない pane の行は `stale!` と表示され、フォーカス / ピーク操作の対象から除外されます。

## --status（JSON）

`fanout <parent> --status` は読み取り専用です。`.fanout/state.json` から指定 parent ですでに fanout 済みの子 issue を列挙し、各子について `gh api graphql` で `repository.issue.closedByPullRequestsReferences(first: 100)` を照会して（子を close する PR が 100 件を超える場合は cursor でページング）、`state`、`mergedAt`、`reviewDecision`、最新 commit の CI rollup を取得し、既定では JSON ドキュメントを 1 つ stdout に出力します:

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
    "all_merged": false
  }
}
```

JSON は automation 向けです:

```bash
fanout 123 --status | jq '.summary.all_merged'
```

複数の親を fanout した state file では、他の親の子はフィルタされ、`summary.all_merged` は指定した親だけを反映します。リポジトリのチェックアウト外から読む場合は `FANOUT_STATE_PATH` で state file を直接指定できます。指定が無ければ fanout は `<git-root>/.fanout/state.json` を読みます。

`--status` の exit code は通常フローとは別レーンです:

| Exit code | 意味 |
|---|---|
| `0` | status を出力した — JSON mode では実際の状態は `summary.all_merged` で確認する |
| `2` | 列挙できない: 不正な呼び出し、読めない / 壊れた state file、使えない project root、Projects v2 URL を parent に指定。state file が無い場合は空 state として扱う |
| `3` | `gh` API 呼び出しが失敗した（認証、ネットワーク、存在しない issue など） |

> **issue parent 専用:** 現在の JSON schema は issue parent 用なので、Projects v2 URL を parent にした `--status` は最初に拒否されます。

## --status --format table

```bash
fanout 123 --status --format table
```

`--format table` は人間向けの一覧を出力します。正規化した PR 状態（`open`、`draft`、`review-required`、`approved`、`changes-requested`、`merged`、`closed`）、CI、PR 差分バー、変更ファイル数、Conventional-Commit 種別、PR リンクが加わります。

## --status --post-dashboard

```bash
fanout 123 --status --post-dashboard
```

`--post-dashboard` は親 issue に marker 付きコメントを 1 つ upsert し、各子 PR の sub-issue 番号、PR リンク、PR 状態、CI、差分規模、Conventional-Commit 種別、TL;DR、`Review effort` score を集約します。dashboard は GitHub の機械可読データと PR 本文だけから作られ、LLM は呼びません。

コメント本文の先頭に `<!-- fanout:dashboard parent=N -->` を置き、paginated な GitHub REST comments endpoint で既存の marker コメントを探して、そのコメントだけを更新します。marker コメントが無い場合は `gh issue comment --body-file -` で新規作成します。

> `--post-dashboard` は `--status` 系で唯一 GitHub に書き込む option です。

## Web ダッシュボード（fanout dashboard --web）

`fanout dashboard --web` は**読み取り専用**の Web ダッシュボードを起動し、fanout の **Session**（`.fanout/state.json` に記録された pane を親 issue 単位でまとめたもの）をブラウザで SSE によりライブ表示します。pane の生存（`tmux list-panes`）、issue 状態、PR マージ状態（`--status` と同じデータ源を、リポジトリ内の全親について一度に再利用）を更新し続けます。GitHub の状態は一切変更せず、tmux も*読み取る*だけです。意図的な便宜が 2 つだけあります: 起動中サーバを `.fanout/dashboard.json` に記録して 2 回目の起動で再利用すること、そして後述の `prefix + D` tmux キーバインドを登録すること(`--no-keybind` でオプトアウト可)です。

```bash
fanout dashboard --web [--port N] [--open] [--no-token] [--no-keybind]
```

- **localhost 限定。** サーバは `127.0.0.1` にのみバインドし、GET 専用の endpoint（`/api/snapshot`、SSE の `/api/stream`、埋め込み UI）を公開します。`--port` は既定 `0`（OS 割り当ての ephemeral port）で、確定した URL が表示されます。
- **トークン既定 ON。** 起動毎にランダムトークンを生成して表示 / オープンされる URL に埋め込み、`/api/*` をゲートします。同一ホストの他ユーザ / プロセスがループバックポート経由で issue/PR データを読むのを防ぎます。単一ユーザ端末では `--no-token` で外せます。
- **`--open`** は既定ブラウザで URL を開きます。すでに起動中のサーバ（`.fanout/dashboard.json` に記録）があればそれを再利用し、二重起動しません。

全フラグは `fanout dashboard --help` を参照してください。

### prefix + D

ライブ fan-out 後（およびダッシュボード自体の起動時）に fanout が tmux キーバインドを自動登録するため、どの pane からでも **`prefix + D`** でダッシュボードを開けます。キーは detached な `fanout-dashboard` ウィンドウでサーバを起動するのでキー押下後も生き続け、2 回目以降は既存 URL を開き直すだけです。

自動登録は `--no-dashboard-keybind`（fan-out 側）、`--no-keybind`（dashboard 側）、設定キー `dashboardKeybind`、`FANOUT_DASHBOARD_KEYBIND=0` で無効化できます — [Settings]({{< relref "/docs/settings" >}}) を参照してください。

### 縮退動作

- `gh` 未ログインの場合は、バナーを出して state のみのビューを表示します。
- tmux 外でも配信は継続し、pane の生存は unknown のままになります。

このページに登場するフラグの一覧は [CLI リファレンス]({{< relref "/docs/cli" >}}) にあります。
