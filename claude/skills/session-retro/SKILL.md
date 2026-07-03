---
name: session-retro
description: 過去の Claude Code セッションのツールエラー・CI 失敗・レビュー指摘をマイニングし、前回スナップショットとの差分 (新規・再発・改善) を報告して、CLAUDE.md / AGENTS.md / docs / skills / memory への再発防止策を提案する振り返り skill。ユーザーが「retro」「振り返り」「セッション失敗を分析」と言ったとき、または /session-retro が呼ばれたときに使う。収集は read-only、提案の適用は PR 経由のみ。
---

# session-retro

Claude Code 専用 (transcript 形式に依存)。fanout run メトリクス (レビュー往復・time-to-merge) の retro は #369 `fanout retro` CLI + #370 `/fanout-retro` が担う別レーン。

## ガードレール

- 収集は read-only。書いてよいのはスナップショット (`.fanout/retro/session-<date>.json`、ignore されない repo では `~/.claude/fanout-retro/<repo-slug>/`) のみ。
- 改善は提案止まり。repo ファイル (CLAUDE.md / AGENTS.md / docs / skills) への適用はユーザー承認後にブランチ + PR。briefing テンプレ (`internal/briefing`) と settings の自動書き換えは禁止 (#373 のガードレール)。
- GitHub への書き込み (issue / PR コメント投稿) はしない。提案はチャットに出す。
- memory feedback だけは repo 外なので、ユーザー承認後に直接追記してよい。
- transcript 全文をコンテキストに読み込まない。件数集計を先に取り、代表例はカテゴリごとに最大 3 件・1〜2 行の抜粋に留める。

## Step 1 — 対象と期間

- main repo root を解決する: `dirname "$(git rev-parse --path-format=absolute --git-common-dir)"`。素の `--git-common-dir` は main worktree では相対 `.git` を返すので絶対化が必須。
- root の絶対パスの `/` と `.` を `-` に置換した slug で `~/.claude/projects/<slug>*` を glob する。fanout worktree のセッションは `<slug>--fanout-worktrees-…` という別ディレクトリに分かれるため、prefix glob で拾う。ディレクトリ名は `-` 始まりなので、rg / ls に渡すときは `--` 区切りを入れる (無いとフラグ解釈されて 0 件になる)。
- スナップショットディレクトリを決める: `git -C "$root" check-ignore .fanout/retro` が ignore を返す repo (fanout 自身など) は `<root>/.fanout/retro/`、そうでない repo は `~/.claude/fanout-retro/<repo-slug>/`。cwd が linked worktree のままだと root 側のパスは `outside repository` になるので、check-ignore は必ず `git -C "$root"` で実行する。
- 期間: 上で決めたスナップショットディレクトリの最新 `session-*.json` の `window.until` 以降。初回は直近 14 日 (jsonl の mtime でフィルタ)。以降の手順の `SINCE` は **`YYYY-MM-DD` に正規化した日付** (`window.until` が ISO8601 ならその日付部分) を指す。

## Step 2 — ツールエラーのマイニング

tool_result のエラーは transcript の JSON にトップレベルで **素の `"is_error":true`** として入っている。fixed-string 検索を使う (zsh では単引用符必須):

```bash
rg -l -F '"is_error":true' -- <projects-glob>/*.jsonl   # 該当セッション
rg -c -F '"is_error":true' -- <files>                    # セッション別行数
```

エスケープ形 `\"is_error\":true` で検索してはいけない — それはこの文字列を引用しているセッション (過去の retro 実行など) だけに当たるノイズで、実測 (2026-07) では素の形 302 ファイル / 660 行に対しエスケープ形は 14 ファイル / 74 行だった。`rg -c` は行数を数える (1 行に複数エラーがあり得る) ことにも注意。

件数を取ってからマッチ行を選択的に読み、エラー本文で分類する。既知カテゴリ: stale-read Edit / PR ゲート deny / 権限・AskUserQuestion 摩擦 / ブラウザ MCP / sleep ブロック / zsh 構文 / gh api・rate limit / インライン python / 誤パス / jq。新カテゴリは追加してよい (新カテゴリは Step 6 の提案候補)。

**自セッションを除外する**: 実行中セッションの transcript (この skill の解析コマンドやプロンプトが `is_error` 文字列を引用している) は誤検出になるので集計から外す。自セッションの ID はカレント transcript のファイル名か `CLAUDE_SESSION_ID` で特定する。

## Step 3 — CI 失敗

```bash
gh run list --status failure --created ">${SINCE}" --limit 100 \
  --json databaseId,workflowName,displayTitle,headBranch,createdAt
```

workflow 別に集計する。上位 workflow は `gh run view <id> --log-failed` の先頭から代表原因を 1 つ拾う。

## Step 4 — レビュー指摘

まず期間内のコメントを login 別に集計してレビュアーを特定する:

```bash
gh api --paginate \
  "repos/{owner}/{repo}/pulls/comments?since=${SINCE}T00:00:00Z&per_page=100" \
  --jq '.[].user.login' | sort | uniq -c | sort -rn
```

集計はページ横断で安全な**行ストリーム**にする。`--paginate` は **ページごとに** jq を適用するため、`group_by` のような配列集計を `--jq` に書くと 100 件超の期間で同じ login がページをまたいで別々に数えられる (`--slurp` は gh の `--jq` と併用不可)。

そのうえでレビュー bot (fanout では `chatgpt-codex-connector[bot]`) のコメントに絞って分類する。bot の login はサフィックス `[bot]` 付き (素の名前で filter すると 0 件になる)。人間の `in_reply_to` 付き返信 (「対応しました」) は指摘ではないので数えない。`docs/review-checklist.ja.md` の頻出パターンに分類し、パターン外の新種を抽出する (新種はチェックリスト更新の提案候補)。チェックリストが無い repo では Step 2 の既知カテゴリだけで分類する。

## Step 5 — スナップショットと差分報告

Step 1 で決めたスナップショットディレクトリに `session-<YYYY-MM-DD>.json` を書く (前回分の読み取りと同じ場所。ディレクトリは無ければ作成)。ignore されない repo で作業ツリーに書かないのは、`git status` を汚さず「収集は read-only」を保つため。スキーマ:

```json
{"schema": 1, "generated_at": "<ISO8601>",
 "window": {"since": "<ISO8601>", "until": "<ISO8601>"},
 "tool_errors": {"total": 0, "by_category": {}},
 "ci": {"failed_runs": 0, "by_workflow": {}},
 "review": {"comments": 0, "by_pattern": {}}}
```

前回スナップショットと比較し、チャットに 3 区分で報告する: **新規** (前回に無いカテゴリ) / **再発** (前回も今回も非ゼロ) / **改善** (減少・ゼロ化)。初回はスナップショット作成と今回分の集計のみ報告する。

## Step 6 — 改善提案

再発・新規カテゴリごとに、根拠 (件数 + 代表例) と提案先を付けて提示する:

- 全 fanout 子ペインに効かせたい → CLAUDE.md と AGENTS.md (**両方**。片方だけの更新はドリフトになる)。memory は子ペインの project dir に届かない。
- レビュー品質 → `docs/review-checklist.ja.md` / post-work-review skill。
- root repo セッションの挙動 → memory feedback (`feedback_*.md` + MEMORY.md の行)。

適用はユーザー承認後: repo ファイルはブランチ + PR、memory は直接追記。

## やらないこと

- briefing テンプレ・settings・skills の自動書き換え
- GitHub への書き込み
- `fanout retro` CLI (#369) の代替。CLI 実装後は同じ `.fanout/retro/` に同居し、/fanout-retro (#370) がまとめて読む
