---
title: トラブルシューティング
linkTitle: トラブルシューティング
description: "よくある失敗とその直し方、そして state・ロック・pane 作成の背後にある設計メモ。"
weight: 80
kanji: 直
yomi: troubleshoot
---

## "fanout must be run inside tmux"

action mode は `tmux split-window` で子ペインを直接作成するため、fanout は tmux セッション内から実行する必要があります。対象リポジトリの worktree で tmux セッションを開始または attach してから再実行してください。唯一の例外は TUI モード(引数なしの `fanout`)で、fanout 管理の tmux session を自分で作成または attach するため素のシェルから起動できます — [モニタリング]({{< relref "/docs/monitoring" >}})を参照してください。

## "agent is required"

`--agent claude` / `--agent codex` を渡すか、環境変数 `FANOUT_AGENT` を設定してください:

```bash
fanout 123 --agent claude
FANOUT_AGENT=codex fanout 123
```

未知の agent はペイン作成前に失敗します。live 実行では、選択された agent CLI が `PATH` 上に無い場合も失敗します。

## "prepare worktree"

git worktree の準備に失敗しています。出力に含まれる内側の git エラーを確認してください。よくある原因:

- fast-forward できない dirty な checked-out base branch
- diverge した local base branch
- 既存の branch 名
- stale または missing な remote branch

base を変えるには `--base-branch <branch>` を使います。remote-tracking ref から直接切りたい場合は `origin/<branch>` を指定できます:

```bash
fanout 123 --base-branch release/v2
fanout 123 --base-branch origin/main
```

`--no-refresh` は、意図的に現在の local base/ref から切りたいときにだけ使ってください。fanout は base branch を force で整えることはしません — 安全に refresh できない場合は、ユーザーのローカル作業を壊す代わりに失敗します。

## "sub-issues fetch failed"

- 未認証:

```bash
gh auth status
```

- 親 issue が存在しない: Sub-issues API が HTTP 404 を返し、fanout は exit 1 します。issue 番号を確認してください。
- リンクされたサブ issue がゼロなのはエラーではなく、fanout は次を表示して exit 0 します:

```text
no sub-issues on #<parent>
```

## slug や branch 名が意図と違う

既定では slug に `slugify(title)-<issueNum>`、branch に `fanout/<slug>` を使います。特定 issue は `--name <NUM>=<slug>|<display>|<branch>` で個別に、run 全体の branch 名は `--branch-prefix <prefix>` で一括して上書きできます — [ワークフロー]({{< relref "/docs/workflow" >}})を参照:

```bash
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'
fanout 123 --name 8='feat-x|Feature X|feat/issue-8-x'   # all three segments
fanout 123 --name 9='||release/v2.0'                    # branch override only
fanout 123 --branch-prefix fanout/release/
```

## `gh pr create` が deny される("post-work-review が未実施です")

`PreToolUse(Bash)` hook(`.claude/hooks/pre-pr-review-gate.sh`、コミット済みの `.claude/settings.json` に登録)が、現在の HEAD が `/post-work-review` を通過するまで `gh pr create` をブロックします。`/post-work-review` を実行すると最終ステップでレビュー済みコミットが記録されるので、その後 `gh pr create` を再実行してください。一度だけバイパスしたいときは、コマンドの先頭に付けます:

```bash
FANOUT_SKIP_PR_REVIEW=1 gh pr create ...
```

fanout settings で `prReviewGate=false` になっている場合、子 Claude briefing にもこの bypass 許可が入りますが、コミット済み hook 自体は変更されません — このスイッチの意味は [Settings]({{< relref "/docs/settings" >}}) を参照してください。

メモ:

- ゲートは HEAD に固定されます。新しいコミットを積むと再武装されるので、PR の前にもう一度レビューしてください。marker は worktree ローカルなので、fanout の並列ペイン同士が干渉することはありません。
- 検出はシェルトークナイザ(Python 製のコンパニオンパーサ)を通します。コマンド語と引用された引数値を区別するので、コミットメッセージに `gh pr create` と書いただけでは引っかかりません。`eval` / `xargs` / `sh -c "<文字列>"` のような間接実行はすり抜けることがありますが、fanout の通常フローでは許容範囲としています。
- `python3` が無い環境では fail-closed になり、PR 作成らしきコマンドを粗い判定で deny します。`python3` をインストールするか、`export FANOUT_SKIP_PR_REVIEW=1` してください。
- `make install` は Claude / Codex 配下の同名グローバル `post-work-review` / `pr-watch` skill を上書きします。独自に管理しているコピーがある場合は事前にバックアップしてください。

## Project モードで items が取れない

Project モードは GraphQL クエリで Project items を取得するため、`gh` CLI に `read:project` スコープが必要です。無いとクエリが失敗します — スコープを付与して再実行してください:

```bash
gh auth refresh -s read:project
```

items が消えたように見えて意図どおりのケースが 2 つあります:

- Project に Status フィールドが無い場合は、警告を出して `--project-status` を無視し、全 item を対象にフォールバックします。
- repository が現在の git repository root と一致しない item は警告を出してスキップします — fanout は今でも 1 回 1 repo の前提です。

## 設計メモ(FAQ)

state・ロック・pane 作成について「なぜそうなっているのか」への答えです。

### `state.json` のスキーマ

`.fanout/state.json` には `schemaVersion` と、pane ごとに 1 行 — `parent` / `issueNum` / 任意の `taskId` / `kind` / `slug` / `branchName` / `paneId` / `agent` / `displayName` / `worktreePath` / `prompt` / `createdAt` を持つ行 — を保存します。TUI の shell terminal は `kind: "shell"` を使うため、close は tmux pane と state 行だけを消します。`--status` と lifecycle コマンドはこの行を対象に動作します。

### atomic 書き込みとロック

書き込みは sibling temp file + rename で行うため、クラッシュしても書きかけの `state.json` が残ることはありません。live run は planning から launch までの間 `.fanout/state.json.lock` を保持するため、並列の fanout 実行が同じ `(parent, issueNum)` の pane を二重作成することはありません。state 行の無い既存 worktree directory は、移行用 fallback として引き続き fanned 済み扱いでスキップされます。

### 同じ子 issue が別親に記録済みのとき

既定の slug/branch 生成は issue suffix の前に親トークンを足すため、2 回目の run は 1 回目と衝突せず独自の worktree を得ます。今回の run が作る slug に一致する既存 worktree がある場合だけは、中断復旧用に引き続きスキップされます。

### pane 作成にポーリングが不要な理由

```bash
tmux split-window -t <invoking-pane> -d -h -P -F '#{pane_id}' -c <worktree>
```

が子ペインを選択せずに新しい pane id を同期的に返すため、popup の横取りも完了ポーリングも不要です。
