---
title: 並列開発ワークフロー
linkTitle: ワークフロー
description: "wave 駆動のループ — issue ツリーを育てて fan out し、子を選び、merge とペインの後始末を経て次の wave へ。"
weight: 30
kanji: 流
yomi: workflow
---

## ループの全体像

fanout の日常は 1 回きりのコマンドではなくループです。OPEN な子を持つ親 issue を育て、子を並列ペインに展開し、ペインの作業を眺め、終わった子を畳んで、次のバッチへ再実行します:

1. **issue ツリーを作る。** 親 issue と、それにリンクされた子 issue 群を用意します。同梱の `fanout-issues` skill が、計画を fanout-ready な形に変換してくれます — [エージェント連携]({{< relref "/docs/agents" >}})を参照。
2. **fan out する。** `fanout <parent>` が OPEN な子ごとに tmux pane + git worktree を作り、それぞれで agent CLI を起動します。
3. **モニタする。** 各ペインの issue / PR の状態を追います — [モニタリング]({{< relref "/docs/monitoring" >}})を参照。
4. **merge する。** 完了した子 branch を `--merge <NUM>` で取り込みます。
5. **後始末する。** `--cleanup` が、issue が close 済みか PR が merge 済みの記録済み子をまとめて畳みます。
6. **次の wave へ。** fanout を再実行します — 通常は `--unblocked-only` 付きで。blocker が閉じたばかりの子が次のバッチになります。

このページでは、このループを順に追って説明します。

## fanout-ready な issue ツリーの書き方

子 issue は GitHub の **Sub-issues** 経由でも、親本文の**タスクリスト**(`- [ ] #NUM ...`)経由でも、両方でも宣言できます — fanout は両ソースの和集合を取ります:

```text
- [ ] #4 Extract the parser
- [ ] #7 Port the formatter (blocked by #4)
```

blocker — 後述の wave 進行を駆動する依存関係 — は 2 つの書式から読み取られます:

- **子本文の `## Blocked by` セクション** — 次の空行までに並ぶ issue 番号を集める。
- **親タスクリスト行のトレイラ** — 上の `#7` のように、子の行末尾に付ける `(blocked by #X, #Y)`。

```text
## Blocked by
- #4
- #7
```

> `blocked` ラベルは弱いシグナルです。fanout はログに出すだけで、ラベル単体から具体的な blocker 番号を推測することはありません。

## 子の選択

その run が対象にする OPEN な子は、4 つの flag で絞り込めます:

| Flag | 効果 |
|---|---|
| `--limit <N>` | 今回の呼び出しを N 件までに制限する。残り分の再実行コマンドが表示される。 |
| `--only <list>` | 許可リスト: 指定した番号だけを fan out する。親の OPEN 子集合に無い番号は警告して無視する。 |
| `--skip <list>` | 指定した子を除外して残りを fan out する。 |
| `--include <list>` | 自動検出(Sub-issues API + タスクリスト)から漏れた子を強制追加する。CLOSED や存在しない番号は警告してスキップ。include で追加した後に `--only` / `--skip` のフィルタが適用される。 |

```bash
fanout 123 --limit 3
fanout 123 --only 4,7,8,10
fanout 123 --skip 6,9 --limit 3
fanout 123 --include 4,7
```

> `--include` は、同梱の Claude/Codex skill が自動で埋める口です。skill は親本文から暗黙の子参照 — `Closes #N` のようなクローズキーワード、`Depends on #N` のような依存表現、素の箇条書き、日本語の慣用句 — を読み取り、承認された番号をこの flag で fanout に渡します。CLI を agent セッション外で直接使うときは自分で指定してください。

## wave 進行(`--unblocked-only`)

`--unblocked-only` は、blocker がすべて CLOSED の子だけを fan out します。OPEN な blocker が 1 つでも残る子は `deferred (blocked)` として報告され、その run ではスキップされます — 何も作られないので、取り消すものもありません。

再実行では `.fanout/state.json` に記録済みの子もスキップされるため、プロジェクトを進めるのに必要なのは、blocker PR が merge されるたびに同じコマンドをもう一度実行することだけです。Wave 1 → Wave 2 → … が手動の管理なしで進みます。

```bash
fanout 123 --unblocked-only

fanout 123 --unblocked-only --limit 3
```

2 つ目の形は、fanout に次の unblocked バッチを選ばせつつ、各 wave の件数を制限します。

## issue-less plan fan-out

作業がローカルで既に分解されていて、GitHub child issue を作りたくない場合は
`fanout plan` を使います。ループ自体は同じですが、source of truth は issue tree
ではなく JSON spec です:

1. `version: 1`、`plan.slug`、`plan.title`、`tasks[]` entry（`id`、`title`、
   `briefing`、任意の `wave`、`blocked_by`、`branch`、`display_name`、`slug`）
   を持つ plan spec を書く、または選択する。
2. まず `fanout plan <spec> --dry-run --agent <agent>` で preview する。
3. live run は `fanout plan <spec> --agent <agent>`。task に `blocked_by` がある
   場合は通常 `--unblocked-only` も付ける。
4. TUI / dashboard、または `fanout plan <slug> --status [--format table]` で見る。
5. task ID を指定して取り込み / 後始末する:
   `fanout plan <slug> --merge <task-id>`、`--close <task-id>`、`--cleanup`。
6. 次 wave は保存済み slug で再実行する。

```bash
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --dry-run
fanout plan /tmp/fanout-plan-launch-plan.json --agent claude --unblocked-only
fanout plan launch-plan --status --format table
fanout plan launch-plan --merge base-types
fanout plan launch-plan --cleanup
```

live run は spec を `.fanout/plans/<slug>.json` に保存します。row は
`.fanout/state.json` に parent `plan:<slug>`、`taskId`、`issueNum: 0` として
記録されます。`blocked_by` 依存は、依存 task の明示 branch または生成 branch に
merge 済み PR ができたときだけ complete とみなされます。Plan task briefing は
GitHub issue-closing footer を避け、PR body 末尾を
`Plan: <slug> / Task: <id>` にするよう agent に伝えます。

同梱の agent wrapper は spec 作成も支援できます。Claude Code では
`/fanout plan <path-or-plan>` が `fanout-plan` skill へ routing されます。Codex では
`$fanout-plan`、または `fanout plan` の依頼を使います。skill は live launch 前に
dry-run を要約します（確認スキップが明示された場合を除く）。

## 兄弟協調（peer messaging）

親を複数ペインに fan out すると、各子は自分のペインで動く独立した agent
セッションになり、既定ではペイン同士は互いを認識できません。run ごとに
`--team` で opt-in すると、fanout が（best-effort で）各子の通常 briefing に
「Coordinating with your sibling panes」節を注入し、per-parent の peer
レジストリに seed するので、兄弟がお互いを把握できます。なお `--codex-plan-mode`
の子はレジストリには seed されますが最小限の Plan-Mode briefing を受け取るため、
協調節は付きません。

fanout したペイン内では、`fanout msg` が自分が今どの子（または親）かを自動
検出し、per-parent の SQLite バス `/tmp/fanout-<repo>-<parent>.db`
（`FANOUT_DB_PATH` で上書き可）上でやり取りします。バスは全員へ配る共有
`board`（ブロードキャスト）に加え、`--to <issue>` 宛の `1:1` メッセージを
運びます。協調は pull ベース — 自分から要求しない限り、何かがペインを
つつくことはありません。

| verb | 効果 |
|---|---|
| `peers` | この親で把握している兄弟ペインを一覧する。 |
| `inbox [--mark-read]` | 自分宛の未読 1:1 メッセージを表示する（任意で既読化）。 |
| `board [--all]` | 最近のブロードキャストを表示する（`--all` で全件）。 |
| `send --to <N> <body>` | 兄弟 `#N` へ 1:1 メッセージを送る。 |
| `post [--kind K] <body>` | 共有 board へブロードキャストを投稿する。 |
| `mark-read [--all]` | メッセージを既読にする。 |
| `register` | 自分を peer レジストリへ（再）seed する。 |
| `nudge <N>` | peer `#N` のペインへ `send-keys` でヒントを送る best-effort 通知。agent が running のときだけ送り、DB は一切触らない。 |

`claude` / `codex` どちらのペインでも同じく動きます。これは 1 セッション内の
チームメイトを協調させる Claude Code Agent Teams とは別物で、peer messaging は
別々の fanout ペイン同士を協調させます。全サーフェスは
[CLI リファレンス]({{< relref "/docs/cli" >}}) または `fanout msg --help` を
参照してください。

> **セキュリティ。** バスは `/tmp` 配下の**平文** SQLite ファイルです。fanout は
> `0600`（所有者のみ）で作成し、group/world-readable や別ユーザー所有のファイルは
> 開くのを拒否しますが、`/tmp` は共有のスクラッチ領域です — **秘密情報・トークン・
> 認証情報をメッセージに載せないでください。** DB は使い捨てなので、終わったら
> `/tmp/fanout-<repo>-<parent>.db*` を削除してください。

## 命名とブランチ

既定では、各子の worktree slug は `slugify(title)-<issueNum>`、branch は `fanout/<slug>` です。これを 3 つの flag で上書きできます:

- `--name <NUM>=<slug>[|<display>[|<branch>]]` — 特定の子の worktree slug stem、pane タイトル、branch を直接指定する。pipe 区切りの 3 セグメントはそれぞれ空でよいが、最低 1 つは非空であること。slug stem に issue 番号 suffix が無ければ fanout が `-<NUM>` を付け、3 つ目のセグメントは生成 branch 名を上書きする。繰り返し指定可 — 対象ごとに 1 つ。
- `--branch-prefix <prefix>` — run 全体の生成 branch 名の prefix を変える。
- `--base-branch <branch>` — 子が分岐する base branch を上書きする。bare な local branch 名と `origin/<branch>` の両方に対応。
- `--no-refresh` — base branch の refresh をスキップする。既定では、分岐前に `git fetch --quiet --no-tags` と fast-forward 更新で base を fresh 化する。

```bash
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'
fanout 123 --name 8='feat-x|Feature X|feat/issue-8-x'   # all three segments
fanout 123 --name 9='||release/v2.0'                    # branch override only

fanout 123 --base-branch release/v2 --branch-prefix fanout/release/

fanout 123 --no-refresh
```

> 同梱の Claude/Codex 連携は、追加の API 呼び出しなしに issue のタイトル/本文から `--name` flag を生成して渡します。上書きしたい場合は自分で `--name` を渡してください。rerun の冪等性は名前ではなく `.fanout/state.json` が担います。

## Project モード

位置引数には、親 issue 番号の代わりに Projects v2 の URL — `https://github.com/users/<owner>/projects/<n>` または `https://github.com/orgs/<org>/projects/<n>` — も渡せます。正規形の `/views/<id>` suffix やトレイリングのクエリ文字列付き URL も受け付けるので、ブラウザのアドレスバーからそのままコピペできます。このモードでは、子は Sub-issues + タスクリストの和集合ではなく Project の item から取り出されます。

```bash
fanout https://github.com/users/<owner>/projects/<n>

fanout https://github.com/orgs/<org>/projects/<n> --project-status "In Progress"

fanout https://github.com/users/<owner>/projects/<n> --project-status all
```

- **既定フィルタは `Status == Todo`。** 別の single-select 値にするには `--project-status "<name>"` を、フィルタを無効化して全 item(Done、Status 未設定なども含む)を対象にするには `--project-status all` を渡す。
- **親 body が無いので暗黙の子参照サルベージは無い。** skill が通常拾う `Closes #N` / 依存表現はここには存在しない — Project が source of truth。Project が取りこぼしている子は `--include` で強制追加する。
- **単一 repo のみ。** 現在の git リポジトリと異なる repository の item は警告してスキップする。
- **Project に Status フィールドが無い場合**は、警告を出して `--project-status` に関わらず全 item にフォールバックする。
- **blocker** の情報源は子本文の `## Blocked by` セクションのみ。親 body が無いため `(blocked by #X)` のタスクリストトレイラは存在しない。`blocked` ラベルはここでも弱いシグナルのままで、ラベルしか無い子は警告の上 unblocked として扱われる。

> Project モードでは `gh` CLI に `read:project` スコープが必要です — [インストール]({{< relref "/docs/installation" >}})を参照。

## 取り込みと後始末(lifecycle)

lifecycle コマンドは `.fanout/state.json` に記録された entry に対してのみ動作します。filesystem scan で任意の worktree を探すことはしません。

- `fanout <parent> --merge <NUM>` — `git -C <project-root> merge --ff-only <recorded-branch>` を実行する。fast-forward できない場合は git のエラーを報告して終了し、エディタや conflict 解決フローは起動しない。
- `fanout <parent> --close <NUM>` — 記録済み worktree を `git worktree remove <path> --force` で削除し、記録済み tmux pane が残っていれば kill し、state entry を削除して `git worktree prune` を実行する。
- `fanout <parent> --cleanup` — 記録済みの子を照会し、issue が `CLOSED`、または closed-by PR に `MERGED` がある子をまとめて close する。未完了の子は記録に残る。

```bash
fanout 123 --merge 4
fanout 123 --close 4

fanout 123 --cleanup
```

> `--status` と同様、これらのコマンドは `FANOUT_STATE_PATH` を尊重します。未設定なら `<git-root>/.fanout/state.json` を使います。

## 実行制御

| Flag | 効果 |
|---|---|
| `--session <name>` | 起動元 pane ではなく、名前付き tmux session を target にする。 |
| `--sleep <seconds>` | 成功した子起動の間のレート制限(既定 4 秒)。retry/backoff の knob ではない。 |
| `--dry-run` | git worktree + tmux のコマンド列を実行せずにプレビューする。 |
| `--debug` | 追加の診断ログを出力する。 |

```bash
fanout 123 --session work-repo
fanout 123 --sleep 8
fanout 123 --dry-run
```

このページの flag を含む全サーフェスは [CLI リファレンス]({{< relref "/docs/cli" >}})にあります。
