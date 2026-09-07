---
name: session-retro
description: 過去の Claude Code セッションのツールエラー・CI 失敗・レビュー指摘をマイニングし、前回スナップショットとの差分 (新規・再発・改善) を報告して、docs / skills / CLAUDE.md / memory への再発防止策を提案する振り返り skill。ユーザーが「retro」「振り返り」「セッション失敗を分析」と言ったとき、または /session-retro が呼ばれたときに使う。収集は read-only、提案の適用は PR 経由のみ。
---

# session-retro

Claude Code 専用(transcript 形式に依存)。fanout run メトリクス(レビュー往復・
time-to-merge)の retro は #369 `fanout retro` CLI + #370 `/fanout-retro` が担う別レーン。

## ガードレール

- 収集は read-only。書いてよいのはスナップショット(`.fanout/retro/session-<date>.json`、
  ignore されない repo では `${CLAUDE_CONFIG_DIR:-$HOME/.claude}/fanout-retro/<repo-slug>/`)
  のみ。
- 改善は提案止まり。repo ファイル(docs / skills / CLAUDE.md / AGENTS.md)への適用は
  ユーザー承認後にブランチ + PR。briefing テンプレ(`internal/app/briefing`)と settings
  は書き換えない(#373 のガードレール)。
- GitHub への書き込み(issue / PR コメント投稿)はしない。提案はチャットに出す。
- memory feedback だけは repo 外なので、ユーザー承認後に直接追記してよい。
- transcript 全文をコンテキストに読み込まない。件数集計を先に取り、代表例は
  カテゴリごとに最大 3 件・1〜2 行の抜粋に留める。

## Step 1: 対象と期間

以降の全ステップが使う変数と一時領域をここで 1 回だけ用意する。EXIT trap は
プロセスに 1 つしか登録できないので、ステップごとに `mktemp` と `trap` を打たず、
1 個の一時ディレクトリにまとめる。

```bash
claude_home="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
```

- main repo root: `dirname "$(git rev-parse --path-format=absolute --git-common-dir)"`。
  素の `--git-common-dir` は main worktree で相対 `.git` を返すので絶対化する。
- slug: root の絶対パス中の英数字以外の文字を全て `-` に置換する(Claude Code の
  project ディレクトリ命名規則。`/` や `.` だけでなく空白・`_`・`+` も対象)。
- project ディレクトリのルートは `"$claude_home"/projects`。`~/.claude` を直書きすると
  `CLAUDE_CONFIG_DIR` 環境で transcript が 1 件も見つからない。
- 対象ディレクトリは `<projects_root>/<slug>`(完全一致)、
  `<projects_root>/<slug>--dmux-worktrees-*`、`<projects_root>/<slug>--fanout-worktrees-*`
  の 3 パターンだけを glob する。`<slug>*` は `<slug>2` のような別リポジトリを、
  `<slug>--*-worktrees-*` は無関係な別ツール由来のディレクトリを拾う。ディレクトリ名は
  `-` 始まりなので、rg / ls に渡すときは `--` 区切りを入れる。
- スナップショットディレクトリ: `git -C "$root" check-ignore .fanout/retro/`(末尾
  スラッシュ付き)が ignore を返す repo は `<root>/.fanout/retro/`、そうでない repo は
  `"$claude_home"/fanout-retro/<repo-slug>/`。末尾スラッシュが無いと、未作成のパスは
  ディレクトリ限定の ignore ルールにマッチしない。cwd が linked worktree でも root 側の
  パスを判定できるよう `git -C "$root"` で実行する。
- `SINCE`: スナップショットディレクトリの最新 `session-*.json` の `window.until` を、
  フルの ISO8601 のまま使う(日付に丸めると同じ日の既報告イベントを再集計する)。
  初回は直近 14 日前の ISO8601 UTC 時刻。
- `UNTIL`: Step 2〜4 の収集を始める前に `UNTIL=$(date -u +%Y-%m-%dT%H:%M:%SZ)` で
  固定する。収集後に「いま」を書くと、収集〜書き込みの間に起きたイベントが今回にも
  次回にも入らず永久に欠落する。Step 5 の `window.until` にはこの固定値を書く。
- タイムスタンプは常に `date -u +%Y-%m-%dT%H:%M:%SZ` 形式(末尾リテラル `Z`、offset
  表記や小数秒なし)。Step 2 の macOS フォールバック(`date -j -u -f ...`)はこの形式
  だけを解釈する。

既知の限界: (1) 3 パターンは git repo のルートで開始したセッションだけを対象にし、
`$root/cmd/fanout` のようなサブディレクトリ開始のセッションは別 project ディレクトリに
保存されるため拾わない(#392)。(2) 前回スナップショットのメトリクスが `truncated=true`
でも `window.until` はそのまま次回の `SINCE` になるので、不完全だった期間のイベントは
二度と取得されない。メトリクスごとに独立した window が必要(#394)。

## Step 2: ツールエラーのマイニング

まず `SINCE` / `UNTIL` で候補ファイルをファイル単位で粗く絞る(厳密な判定は後述の
行単位で行う)。`find -newermt` は BSD/macOS で ISO8601 の `T`/`Z` 付き文字列を解釈
できないことがあるので、mtime を epoch 秒に変換して比較する:

```bash
since_epoch=$(date -u -d "$SINCE" +%s 2>/dev/null || date -j -u -f "%Y-%m-%dT%H:%M:%SZ" "$SINCE" "+%s")
until_epoch=$(date -u -d "$UNTIL" +%s 2>/dev/null || date -j -u -f "%Y-%m-%dT%H:%M:%SZ" "$UNTIL" "+%s")
candidates="$tmpdir/candidates"
: > "$candidates"
for f in "$claude_home"/projects/"$slug"/*.jsonl \
         "$claude_home"/projects/"$slug"--dmux-worktrees-*/*.jsonl \
         "$claude_home"/projects/"$slug"--fanout-worktrees-*/*.jsonl; do
  [ -e "$f" ] || continue
  mtime=$(stat -c %Y "$f" 2>/dev/null || stat -f %m "$f")
  [ "$mtime" -ge "$since_epoch" ] && printf '%s\n' "$f" >> "$candidates"
done
```

- 候補選定は下限のみで絞る。長時間セッションが window 内にエラー行を書いた後で
  さらに書き込み、mtime が `UNTIL` を超えることがあるため、上限を付けるとそのファイル
  ごと落ちる。二重計上は後述の行単位フィルタ `(SINCE, UNTIL]` が防ぐ。
- `: > "$candidates"` で空ファイルを先に作る。候補が 0 件だと `for` ループが
  `printf >>` を一度も実行せず、後段の `while read < "$candidates"` が
  「No such file or directory」になる。
- 既知の限界: この glob はトップレベルの `*.jsonl` だけを見る。サブエージェントの tool
  実行履歴は `<sessionId>/subagents/agent-*.jsonl` に別記録されるため、委譲中の tool
  error は集計に含まれない(#393)。

候補リストは配列に読み込み(`$(cat …)` で rg に渡すと空白を含むパスが単語分割される)、
この時点で自セッションを除外する。実行中セッションの transcript はこの skill の解析
コマンドやプロンプト文字列を引用しているため、含めると誤検出し、集計後の除外では
`total` が既に膨らんでいる:

```bash
files=()
while IFS= read -r f; do
  if [ -n "$CLAUDE_CODE_SESSION_ID" ]; then
    case "$f" in *"$CLAUDE_CODE_SESSION_ID"*) continue ;; esac
  fi
  files+=("$f")
done < "$candidates"
```

env var は `CLAUDE_CODE_SESSION_ID`(`CLAUDE_SESSION_ID` ではない)。`[ -n ... ]` の
ガードが要る: 変数が空のまま `case "$f" in *""*)` を評価すると全パスにマッチし、候補が
丸ごと除外されて `tool_errors.total` が常に 0 になる。

対象ファイルが 0 件なら以降のマイニングを実行せず `tool_errors.total=0` として次へ
進む。空配列のまま `rg -- "${files[@]}"` を呼ぶと path 引数 0 個になり、rg がカレント
ディレクトリを再帰検索して(この repo なら SKILL.md 自身の `"is_error":true` まで)
誤集計する。

tool_result のエラーは transcript の JSON にトップレベルで素の `"is_error":true` として
入っている。fixed-string 検索を使う(zsh では単引用符必須)。エスケープ形
`\"is_error\":true` はこの文字列を引用しているセッション(過去の retro 実行など)だけに
当たるノイズで、素の形の 1/10 以下しかない。

候補ファイルの mtime は「セッション最終更新時刻」でしかなく、SINCE より前から続く
セッションが window 内に 1 行でも書けばファイル全体が候補に入る。行ごとの `timestamp`
フィールドで window に絞ってから数える:

```bash
matches="$tmpdir/matches"
total=0
tool_errors_truncated=false
if [ "${#files[@]}" -gt 0 ]; then
  if rg -I -F '"is_error":true' -- "${files[@]}" > "$matches" 2>/dev/null; then
    rg_exit=0
  else
    rg_exit=$?
  fi
  if [ "$rg_exit" -ge 2 ]; then
    tool_errors_truncated=true
    echo "警告: rg が実行時エラーを返した (権限/読み取り不能なパス等)。tool_errors は過少の可能性がある (snapshot に truncated=true を記録する)" >&2
  fi
  while IFS= read -r line; do
    ts=$(printf '%s' "$line" | jq -r '.timestamp // empty' 2>/dev/null) || ts=""
    [ -z "$ts" ] && continue
    norm="${ts:0:19}Z"   # transcript は ".mmmZ" 付き。SINCE/UNTIL と同じ秒精度に丸めてから比較する
    line_epoch=$(date -u -d "$norm" +%s 2>/dev/null || date -j -u -f "%Y-%m-%dT%H:%M:%SZ" "$norm" "+%s")
    if [ "$line_epoch" -gt "$since_epoch" ] && [ "$line_epoch" -le "$until_epoch" ]; then
      n=$(printf '%s' "$line" | jq '[.message.content[]? | select(.is_error == true)] | length' 2>/dev/null) || n=""
      total=$((total + ${n:-1}))
    fi
  done < "$matches"
fi
echo "tool_errors.total=$total tool_errors_truncated=$tool_errors_truncated"
```

このスニペットが守っていること:

- `rg` は no-match(exit 1)と実行時エラー(exit 2、権限や壊れたパス)を区別する。
  `2>/dev/null || true` だと両方を「0 件」に握りつぶすので、exit code を保存して 2 以上
  なら警告し `truncated` を立てる。
- `-I`(`--no-filename`)を付ける。複数ファイルでは既定で各行に `<path>:` が前置され、
  JSON として壊れて `jq` が全行パースエラーになる。
- 1 行に複数の tool call が失敗すると同じ JSONL 行に `is_error:true` が複数入るので、
  行を `jq` で構造的にパースして `.message.content[]` 内の該当要素数を数える。jq 解析に
  失敗した行は `${n:-1}` で 1 件として保守的に数える。
- ミリ秒付きの transcript timestamp とミリ秒無しの `SINCE`/`UNTIL` は文字列比較しない
  (`.` は `Z` より ASCII が小さく、辞書順で逆転する)。同じ秒精度の epoch に変換して
  数値比較する。秒精度への丸めで window 境界の同一秒の行がこぼれることはあるが、
  この skill は傾向を追う定期集計なので許容する。
- `if rg …; then rg_exit=0; else rg_exit=$?; fi` の形と、ループ内 `jq` の `|| ts=""` /
  `|| n=""` は、`set -e` の呼び出し元で非 0 終了が集計を打ち切るのを防ぐ。

件数を取ってからマッチ行を選択的に読み、エラー本文で分類する。既知カテゴリ:
stale-read Edit / PR ゲート deny / 権限・AskUserQuestion 摩擦 / ブラウザ MCP /
sleep ブロック / zsh 構文 / gh api・rate limit / インライン python / 誤パス / jq。
新カテゴリは追加してよい(Step 6 の提案候補)。

## Step 3: CI 失敗

`gh run list --status` は 1 回の呼び出しに 1 値しか渡せない。`failure` だけでは
`startup_failure` や `timed_out` を見落とすので、値ごとに分けて呼んでまとめる。
`--status` を外して全 run を撮ると `--limit` の枠を success な run が消費し、window の
古い方の失敗が切り捨てられる。`--all` で disabled / rename された workflow の過去 run も
含める(既定では除外される):

```bash
runs="$tmpdir/runs"
: > "$runs"
ci_truncated=false
for st in failure startup_failure timed_out; do
  out=$(gh run list --all --status "$st" --created "*..${UNTIL}" --limit 100 \
      --json databaseId,workflowName,displayTitle,headBranch,createdAt,updatedAt,conclusion) || out=""
  if [ -z "$out" ]; then
    ci_truncated=true
    echo "警告: gh run list --status $st が失敗した。ci 集計は不完全な可能性がある (snapshot に truncated=true を記録する)" >&2
    continue
  fi
  printf '%s\n' "$out" >> "$runs"
  n=$(printf '%s' "$out" | jq 'length' 2>/dev/null) || n=0
  if [ "${n:-0}" -ge 100 ]; then
    ci_truncated=true
    echo "警告: --status $st が --limit 100 に到達した。打ち切られている可能性がある (snapshot に truncated=true を記録する)" >&2
  fi
done
jq -s "(add // []) | [.[] | select(.updatedAt > \"${SINCE}\" and .updatedAt <= \"${UNTIL}\")]" "$runs"
```

- 個々の呼び出しの終了コードを確認し、1 つでも失敗したら `ci_truncated=true` を立てて
  Step 5 の `ci.truncated` に反映する。echo で警告するだけでは schema に残らず、次回比較
  が過少な値のまま進む。3 回全て失敗すると `$runs` が空で `add` が `null` になるため、
  `add // []` で空配列に戻し `ci.failed_runs=0, ci.truncated=true` として進める。
- window の判定は `createdAt` ではなく `updatedAt`(完了時刻)。前回 window の直前に
  queue され、今回 window 内に失敗完了した run は `createdAt` 基準だと永久に取得
  できない。そのため取得は下限を開けて `--created "*..${UNTIL}"`(`*` は省略不可。空
  文字列だと gh は空配列を返す)とし、`--status` ごとに直近 100 件を取ってから
  `updatedAt` で `(SINCE, UNTIL]` に絞る。
- `--limit` は取得上限も兼ねるので、返り値が 100 件なら打ち切りの疑いがある。期間を
  `"*..MID"` / `"MID..${UNTIL}"` に分割して 2 回取るか、難しければ黙って切り詰めず
  `ci_truncated=true` を立てる。
- workflow 別に集計し、上位 workflow は `gh run view <id> --log-failed` の先頭から
  代表原因を 1 つ拾う。

## Step 4: レビュー指摘

`since=` は `updated_at` 基準で `created_at` 基準ではない。期間より前のコメントが期間内に
編集・minimize されると混ざるので、分類対象は `created_at` で window に絞る。取得は
一時ファイルに保存して終了コードを確認してから使う(パイプ末尾に `--jq`/`sort` を
繋ぐと `gh api` の失敗がパイプ全体の終了ステータスに表れず、黙って 0 件になる):

```bash
comments="$tmpdir/comments"
if gh api --paginate \
    "repos/{owner}/{repo}/pulls/comments?since=${SINCE}&per_page=100" \
    --jq ".[] | select(.created_at > \"${SINCE}\" and .created_at <= \"${UNTIL}\")" > "$comments"; then
  review_truncated=false
else
  review_truncated=true
  echo "警告: gh api pulls/comments が失敗した。review 集計は不完全な可能性がある (snapshot に truncated=true を記録する)" >&2
fi
```

- `gh api --jq` は `--arg` を受け付けないため、`$SINCE`/`$UNTIL` はシェル展開で埋め込む。
  `SINCE` は Step 1 のフルの ISO8601 をそのまま使い、`T00:00:00Z` 等を連結しない。
- login 別の内訳は `jq -r '.user.login' "$comments" | sort | uniq -c | sort -rn` で
  `$comments` から集計する(`gh api` を 2 回叩かない)。
- レビュー bot(fanout では `chatgpt-codex-connector[bot]`。login は `[bot]` サフィックス
  付きで、素の名前で filter すると 0 件)のコメントに絞って分類する。人間の返信は指摘では
  ないので数えない。返信判定は `in_reply_to_id` が非 null かどうか(REST API のフィールド名
  は `in_reply_to` ではない)。`docs/review-checklist.ja.md` の頻出パターンに分類し、
  パターン外の新種を抽出する(チェックリスト更新の提案候補)。チェックリストが無い repo
  では Step 2 の既知カテゴリだけで分類する。

## Step 5: スナップショットと差分報告

新しいスナップショットを書く前に、Step 1 で特定した最新の既存 `session-*.json` があれば
中身をメモリ上に退避する。ファイル名は日単位なので、同じ UTC 日に 2 回目を実行すると
新しいスナップショットが前回分を上書きし、書いてから読むと今回分を「前回」として
自己比較してしまう。順序は常に「退避 → 書く → 退避分と比較」。

Step 1 で決めたスナップショットディレクトリに `session-<YYYY-MM-DD>.json` を書く
(ディレクトリは無ければ作成)。ignore されない repo で作業ツリーに書かないのは、
`git status` を汚さず「収集は read-only」を保つため。`window.until` には Step 1 で
固定した `UNTIL` を書く。スキーマ:

```json
{"schema": 1, "generated_at": "<ISO8601>",
 "window": {"since": "<ISO8601>", "until": "<ISO8601>"},
 "tool_errors": {"total": 0, "by_category": {}, "truncated": false},
 "ci": {"failed_runs": 0, "by_workflow": {}, "truncated": false},
 "review": {"comments": 0, "by_pattern": {}, "truncated": false}}
```

`tool_errors.truncated` / `ci.truncated` / `review.truncated` には Step 2〜4 の各
`*_truncated` をそのまま書く。

退避した前回スナップショットと比較し、チャットに 3 区分で報告する: 新規(前回に無い
カテゴリ)/ 再発(前回も今回も非ゼロ)/ 改善(減少・ゼロ化)。初回(前回分が無い)は
スナップショット作成と今回分の集計のみ報告する。今回または前回のスナップショットで
該当メトリクスの `truncated=true` なら、そのメトリクスは改善/再発の判定に使わず
「不完全(truncated)につき比較対象外」として別扱いする。部分集計の減少を品質改善として
報告すると誤りになる。

## Step 6: 改善提案

再発・新規カテゴリごとに、根拠(件数 + 代表例)と提案先を付けて提示する:

- 全 fanout 子ペインに効かせたい → 正典 docs(`docs/*.ja.md`)に書き、CLAUDE.md と
  AGENTS.md からはポインタで参照する(片方だけの更新はドリフトになる)。memory は
  子ペインの project dir に届かない。
- レビュー品質 → `docs/review-checklist.ja.md` / post-work-review skill。
- root repo セッションの挙動 → memory feedback(`feedback_*.md` + MEMORY.md の行)。

適用はユーザー承認後: repo ファイルはブランチ + PR、memory は直接追記。
`fanout retro` CLI(#369)の代替ではない。CLI 実装後は同じ `.fanout/retro/` に同居し、
/fanout-retro(#370)がまとめて読む。
