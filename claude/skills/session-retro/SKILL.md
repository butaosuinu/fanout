---
name: session-retro
description: 過去の Claude Code セッションのツールエラー・CI 失敗・レビュー指摘をマイニングし、前回スナップショットとの差分 (新規・再発・改善) を報告して、CLAUDE.md / AGENTS.md / docs / skills / memory への再発防止策を提案する振り返り skill。ユーザーが「retro」「振り返り」「セッション失敗を分析」と言ったとき、または /session-retro が呼ばれたときに使う。収集は read-only、提案の適用は PR 経由のみ。
---

# session-retro

Claude Code 専用 (transcript 形式に依存)。fanout run メトリクス (レビュー往復・time-to-merge) の retro は #369 `fanout retro` CLI + #370 `/fanout-retro` が担う別レーン。

## ガードレール

- 収集は read-only。書いてよいのはスナップショット (`.fanout/retro/session-<date>.json`、ignore されない repo では `${CLAUDE_CONFIG_DIR:-$HOME/.claude}/fanout-retro/<repo-slug>/`) のみ。
- 改善は提案止まり。repo ファイル (CLAUDE.md / AGENTS.md / docs / skills) への適用はユーザー承認後にブランチ + PR。briefing テンプレ (`internal/briefing`) と settings の自動書き換えは禁止 (#373 のガードレール)。
- GitHub への書き込み (issue / PR コメント投稿) はしない。提案はチャットに出す。
- memory feedback だけは repo 外なので、ユーザー承認後に直接追記してよい。
- transcript 全文をコンテキストに読み込まない。件数集計を先に取り、代表例はカテゴリごとに最大 3 件・1〜2 行の抜粋に留める。

## Step 1 — 対象と期間

- 以降の全ステップが前提にする変数と一時領域を、**この Step 1 の冒頭で 1 回だけ**用意する:

  ```bash
  claude_home="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
  tmpdir=$(mktemp -d)
  trap 'rm -rf "$tmpdir"' EXIT
  ```

  `claude_home` を Step 2 のコード例で初めて代入すると、それより前に `"$claude_home"` を参照する Step 1 のスナップショットディレクトリ決定と矛盾する (手順を上から実行すると未定義のまま使うことになる)。一時ファイルも Step 2〜3 それぞれで `mktemp` せずここで 1 個の一時ディレクトリにまとめる — EXIT trap はプロセスに 1 つしか登録できないため、ステップごとに `trap ... EXIT` を打つと後段の登録が前段を上書きし、前段の一時ファイル (transcript のエラー行を含む `matches` 等) が `/tmp` に残ったままになる (実機で再現・確認済み)。
- main repo root を解決する: `dirname "$(git rev-parse --path-format=absolute --git-common-dir)"`。素の `--git-common-dir` は main worktree では相対 `.git` を返すので絶対化が必須。
- root の絶対パス中の **英数字以外の文字を全て** `-` に置換した slug を作る (Claude Code の project ディレクトリ命名規則。`/` や `.` だけでなく空白・`_`・`+` 等も対象)。
- Claude project ディレクトリのルートは `"$claude_home"/projects`。`CLAUDE_CONFIG_DIR` を設定している環境では `~/.claude` を直書きすると transcript を 1 件も見つけられない。
- 対象ディレクトリは **`<projects_root>/<slug>`(完全一致)と `<projects_root>/<slug>--dmux-worktrees-*`・`<projects_root>/<slug>--fanout-worktrees-*`(worktree セッション。旧 dmux 時代と現行 fanout の両方の命名を明示的に列挙する)の 3 パターンだけ** を glob する。`<slug>*` のような緩い prefix glob や `<slug>--*-worktrees-*` のようなワイルドカード区間は使わない — 前者は `<slug>2` や `<slug>-old` のような別リポジトリ、後者は無関係な別ツール由来の `<slug>--other-worktrees-*` まで拾ってしまう。ディレクトリ名は `-` 始まりなので、rg / ls に渡すときは `--` 区切りを入れる (無いとフラグ解釈されて 0 件になる)。
- **既知の限界**: この 3 パターンは git repo のルートで開始したセッションのみを対象にする。`$root/cmd/fanout` のようなサブディレクトリで開始したセッションは Claude Code 側で別の project ディレクトリに保存されるため、この skill では拾わない (対応するには全 project ディレクトリを横断して各 transcript の `cwd` を照合する必要があり、この skill のスコープを超える — フォローアップ #392)。
- スナップショットディレクトリを決める: `git -C "$root" check-ignore .fanout/retro/`(**末尾スラッシュ必須**)が ignore を返す repo (fanout 自身など) は `<root>/.fanout/retro/`、そうでない repo は `"$claude_home"/fanout-retro/<repo-slug>/`。`.fanout/retro/` がまだ存在しない初回実行時、末尾スラッシュを付けずに `git check-ignore .fanout/retro` を呼ぶと、git は「ファイルかディレクトリか判別できないパス」として扱い、ディレクトリ限定の ignore ルール (`.fanout/retro/` のような末尾スラッシュ付きパターン) にマッチしなくなる (実機で再現・確認済み — 既存ディレクトリでは両方マッチするが、未作成だと末尾スラッシュ無しだけ失敗する)。cwd が linked worktree のままだと root 側のパスは `outside repository` になるので、check-ignore は必ず `git -C "$root"` で実行する。
- 期間の下限 `SINCE`: 上で決めたスナップショットディレクトリの最新 `session-*.json` の `window.until`。**フルの ISO8601 のまま使う (日付に丸めない)** — 丸めると同じ日の中で既に報告済みのイベントを次回の window が再集計してしまう。初回はスナップショットが無いので、直近 14 日前の ISO8601 UTC 時刻を `SINCE` とする。
- 期間の上限 `UNTIL`: **Step 2〜4 のデータ収集を始める前に** `UNTIL=$(date -u +%Y-%m-%dT%H:%M:%SZ)` で固定する。収集後に「いま」を書くと、クエリ実行〜スナップショット書き込みの間に発生したイベント (例: gh run list 実行直後に落ちた CI) が今回にも次回にも入らず永久に欠落する。Step 5 で書く `window.until` はこの固定値をそのまま使う。
- タイムスタンプは常に `date -u +%Y-%m-%dT%H:%M:%SZ` 形式 (末尾リテラル `Z`、offset 表記 `+00:00` や小数秒を使わない) で統一する。Step 2 の macOS フォールバック (`date -j -u -f "%Y-%m-%dT%H:%M:%SZ" ...`) はこの形式限定でしか解釈できないため、`SINCE`/`UNTIL` どちらもこの関数で生成・保存する。

## Step 2 — ツールエラーのマイニング

まず Step 1 の `SINCE`/`UNTIL` で候補ファイルを絞る (ファイル単位の粗いフィルタ。厳密な判定は後述の行単位で行う)。`find -newermt` は環境によって書式非互換で失敗する (BSD/macOS の `find` は ISO8601 の `T`/`Z` 付き文字列を解釈できないことがある) ので、mtime を epoch 秒に変換して比較する:

```bash
since_epoch=$(date -u -d "$SINCE" +%s 2>/dev/null || date -j -u -f "%Y-%m-%dT%H:%M:%SZ" "$SINCE" "+%s")
until_epoch=$(date -u -d "$UNTIL" +%s 2>/dev/null || date -j -u -f "%Y-%m-%dT%H:%M:%SZ" "$UNTIL" "+%s")
candidates="$tmpdir/candidates"
for f in "$claude_home"/projects/"$slug"/*.jsonl \
         "$claude_home"/projects/"$slug"--dmux-worktrees-*/*.jsonl \
         "$claude_home"/projects/"$slug"--fanout-worktrees-*/*.jsonl; do
  [ -e "$f" ] || continue
  mtime=$(stat -c %Y "$f" 2>/dev/null || stat -f %m "$f")
  [ "$mtime" -ge "$since_epoch" ] && printf '%s\n' "$f" >> "$candidates"
done
```

**既知の限界**: この glob はトップレベルの `*.jsonl` だけを見る。Agent/Task ツールで起動したサブエージェントの tool 実行履歴は `<sessionId>/subagents/agent-*.jsonl`(workflow 経由なら `subagents/workflows/<runId>/agent-*.jsonl`)という別ファイルに記録されるため、サブエージェントに委譲した作業中の tool error はこの集計に含まれない (フォローアップ #393)。

候補選定は **下限のみ** で絞る (上限は付けない)。並行/長時間セッションが window 内にエラー行を書いた後、収集前にさらに別の行を書いて mtime が `UNTIL` を超えると、上限フィルタがあるとそのファイルごと候補から落ち、window 内の有効な行まで欠落する。二重計上を防ぐ役目は後述の行単位フィルタ (`(SINCE, UNTIL]`) が正確に担うので、ファイル選定側は粗い下限フィルタで十分。

候補ファイルのリストは `$(cat …)` で rg にそのまま渡さない。シェルの単語分割にかかるため、`$HOME` やリポジトリパスに空白を含む環境ではパスが途中で分割されて存在しないパス扱いになる (実機で再現・確認済み)。1 行 1 パスとして配列に読み込み、**この時点で自セッションを除外する** (集計後に除外しても `total` は既に膨らんでいるので手遅れ — 実行中セッションの transcript がこの skill の解析コマンドやプロンプト文字列を引用しているため、含めると誤検出する):

```bash
files=()
while IFS= read -r f; do
  if [ -n "$CLAUDE_CODE_SESSION_ID" ]; then
    case "$f" in *"$CLAUDE_CODE_SESSION_ID"*) continue ;; esac
  fi
  files+=("$f")
done < "$candidates"
```

env var は **`CLAUDE_CODE_SESSION_ID`** (`CLAUDE_SESSION_ID` ではない — Claude Code の env vars ドキュメント通り、こちらは通常未設定)。**`[ -n ... ]` のガードが必須**: 変数が空文字列のまま `case "$f" in *""*)` を評価すると `**` として全パスにマッチし、候補が丸ごと除外されて `tool_errors.total` が常に 0 になる致命的な退行になる (実機で再現・確認済み)。

**対象ファイルが 0 件なら以降のマイニングを実行せず `tool_errors.total=0` として次へ進む**。`files` が空配列のまま `rg -- "${files[@]}"` を呼ぶと path 引数 0 個になり、rg はカレントディレクトリを再帰検索してしまう (この repo なら SKILL.md 自身が引用する `"is_error":true` まで拾って誤集計になる)。

tool_result のエラーは transcript の JSON にトップレベルで **素の `"is_error":true`** として入っている。fixed-string 検索を使う (zsh では単引用符必須)。エスケープ形 `\"is_error\":true` で検索してはいけない — それはこの文字列を引用しているセッション (過去の retro 実行など) だけに当たるノイズで、実測 (2026-07) では素の形 302 ファイル / 660 行に対しエスケープ形は 14 ファイル / 74 行だった。

候補ファイルの mtime は「セッション最終更新時刻」でしかなく、SINCE より前から続く同一セッションが今回 window 内に 1 行でも書き込むとファイル全体が候補に入る。そのままファイル単位で `rg -c` すると、既に前回報告済みの古い行まで再集計してしまう。**行ごとの `timestamp` フィールドで window に絞ってから数える**:

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

`rg` は no-match (終了コード 1) と実行時エラー (終了コード 2、権限や壊れたパス等) を区別する。`2>/dev/null || true` だけだと両方とも「0 件」として握りつぶし `tool_errors.total` を過少にするので、終了コードを保存して 2 以上なら警告を出す。1 行に複数 tool call が失敗すると同じ JSONL 行に `is_error:true` が複数入るが、行単位の `rg` マッチは 1 行 1 カウントにしかならない。行を `jq` で構造的にパースし、`.message.content[]` 内で `is_error == true` の要素数を数えてから加算する (jq 解析に失敗した行は `${n:-1}` で 1 件として保守的に数える)。

ミリ秒付きの transcript timestamp とミリ秒無しの `SINCE`/`UNTIL` を文字列のまま比較しない — `"...00:00:00Z" < "...00:00:00.001Z"` は Python で確認すると `False` になる (ピリオド `.` の ASCII コードが `Z` より小さいため、辞書順では後者が「小さい」と誤判定される)。両者を同じ秒精度の epoch に変換してから数値比較する。**既知の限界**: 秒精度に丸めるため、ある window の `UNTIL` とちょうど同じ秒に書かれた行 (ミリ秒だけ後) は次の window の `SINCE` との比較で境界からこぼれることがある。この skill は傾向を追う定期集計であり監査ログではないので、この程度の秒境界の誤差は許容する (ナノ秒精度が要る場合は `date` のサブ秒サポートに依存せず言語処理系で扱うこと)。

`rg` は複数ファイルを渡すと既定で各行に `<path>:` を前置する (`-H`/`--with-filename` が複数ファイル時の既定値)。前置されたままだと行が JSON として不正になり `jq` が全行パースエラーになる (実機で再現・確認済み — 候補が 38 ファイルある状態で `total=0` になり原因を特定した)。`-I`/`--no-filename` を必ず付けて素の JSON 行のまま渡す。

`rg` の exit code を `rg_exit=$?` で直接受けず `if rg …; then rg_exit=0; else rg_exit=$?; fi` の形にするのは、`set -e` が効いている呼び出し元で `rg` が非 0 (no-match の 1 を含む) を返した瞬間にスクリプトが打ち切られ、次の行の `$?` 捕捉に到達できなくなるのを防ぐため。ループ内の 2 つの `jq` 呼び出し (`ts=$(...)` / `n=$(...)`) も同じ理由で `|| ts=""` / `|| n=""` を付ける — 壊れた/書き込み中の transcript 行で `jq` がパースエラー (非 0) を返すと、`2>/dev/null` は標準エラー出力を消すだけで終了コードは変えないため、`set -e` 環境ではその行で集計全体が止まってしまう。

件数を取ってからマッチ行を選択的に読み、エラー本文で分類する。既知カテゴリ: stale-read Edit / PR ゲート deny / 権限・AskUserQuestion 摩擦 / ブラウザ MCP / sleep ブロック / zsh 構文 / gh api・rate limit / インライン python / 誤パス / jq。新カテゴリは追加してよい (新カテゴリは Step 6 の提案候補)。

## Step 3 — CI 失敗

`gh run list --status` は 1 回の呼び出しに 1 値しか渡せない。`failure` だけでは `startup_failure` (セットアップ自体の失敗) や `timed_out` を見落とすので、値ごとに分けて呼び、結果をまとめる (`--status` を外して全 run を撮ると、`--limit` の枠を success な run が消費してしまい、window の古い方の失敗が切り捨てられる — 実測で 9 件 → 3 件に減ることを確認済み。1 呼び出し 1 status を維持する)。`--all` を付けて disabled/rename された workflow の過去 run も含める (`gh run list` の manual どおり、既定では disabled workflow の run が除外される):

```bash
runs="$tmpdir/runs"
: > "$runs"
ci_truncated=false
for st in failure startup_failure timed_out; do
  if ! gh run list --all --status "$st" --created "*..${UNTIL}" --limit 100 \
      --json databaseId,workflowName,displayTitle,headBranch,createdAt,updatedAt,conclusion >> "$runs"; then
    ci_truncated=true
    echo "警告: gh run list --status $st が失敗した。ci 集計は不完全な可能性がある (snapshot に truncated=true を記録する)" >&2
  fi
done
jq -s "(add // []) | [.[] | select(.updatedAt > \"${SINCE}\" and .updatedAt <= \"${UNTIL}\")]" "$runs"
```

3 回のうちどれか 1 回でも `gh run list` が失敗すると (認証切れ・rate limit・一時障害)、成功した他の呼び出しの JSON だけで `jq -s` がそのまま完走してしまい、失敗した status 分が黙って欠けたまま `ci.failed_runs` が過少になる。**個々の呼び出しの終了コードを確認し、1 つでも失敗したら `ci_truncated=true` を立てて Step 5 の `ci.truncated` に反映する** (echo で警告するだけでは schema に残らず、次回比較が過少な値のまま進んでしまう)。3 回**全て**失敗すると `$runs` が空のままになり、`jq -s` の `add` は空配列の reduce で `null` になって後続の `.[]` がエラー終了する (実機で再現・確認済み)。`add // []` で null を空配列にフォールバックし、この場合も `ci.failed_runs=0, ci.truncated=true` として snapshot 作成まで進める。

window の判定は `createdAt` ではなく **`updatedAt`(完了時刻)** で行う。前回 window の直前に開始・queue され、前回収集時点ではまだ `failure` 未確定で、今回 window 内に失敗完了した run は `createdAt` が `SINCE` 以前になるため `createdAt` 基準だと永久に取得できない。`updatedAt` は completed run では完了時刻を指すので、これで window 判定すれば取りこぼさない。そのため取得クエリの下限は開けて `--created "*..${UNTIL}"`(**`*` は省略できない** — 空文字列で下限を省略しようとすると gh は空配列を返す。実機で確認済み)とし、`--status` ごとに直近 100 件を取ってから `updatedAt` で `(SINCE, UNTIL]` に絞り込む。`--limit` は取得上限も兼ねるため、3 つのうちどれかがちょうど 100 件なら打ち切られている疑いがある。その場合は期間を `"*..MID"` / `"MID..${UNTIL}"` のように分割して 2 回に分けて取得するか、それが難しければ黙って切り詰めず `ci.truncated=true` をスナップショットと報告に記録する。workflow 別に集計する。上位 workflow は `gh run view <id> --log-failed` の先頭から代表原因を 1 つ拾う。

## Step 4 — レビュー指摘

まず期間内のコメントを login 別に集計してレビュアーを特定する:

```bash
gh api --paginate \
  "repos/{owner}/{repo}/pulls/comments?since=${SINCE}&per_page=100" \
  --jq '.[].user.login' | sort | uniq -c | sort -rn
```

`SINCE` は Step 1 で決めたフルの ISO8601 文字列をそのまま使う (`T00:00:00Z` 等を追加で連結しない — 連結すると `...ZT00:00:00Z` のような不正な文字列になる)。

集計はページ横断で安全な**行ストリーム**にする。`--paginate` は **ページごとに** jq を適用するため、`group_by` のような配列集計を `--jq` に書くと 100 件超の期間で同じ login がページをまたいで別々に数えられる (`--slurp` は gh の `--jq` と併用不可)。

**`since=` は `updated_at` 基準であって `created_at` 基準ではない**。期間より前に投稿されたコメントが期間内に編集・minimize 等で更新されると取得結果に混ざり、既報告の指摘を新規扱いしてしまう。実際に分類対象にするコメントは `created_at` で window に絞り込む:

```bash
gh api --paginate \
  "repos/{owner}/{repo}/pulls/comments?since=${SINCE}&per_page=100" \
  --jq ".[] | select(.created_at > \"${SINCE}\" and .created_at <= \"${UNTIL}\")"
```

(`gh api --jq` は `--arg` を受け付けないため、`$SINCE`/`$UNTIL` はシェル展開でクエリ文字列に埋め込む。)

そのうえでレビュー bot (fanout では `chatgpt-codex-connector[bot]`) のコメントに絞って分類する。bot の login はサフィックス `[bot]` 付き (素の名前で filter すると 0 件になる)。人間の返信 (「対応しました」等) は指摘ではないので数えない — 返信判定は **`in_reply_to_id` が非 null** かどうかで行う (GitHub REST API のフィールド名はこれで、`in_reply_to` ではない)。`docs/review-checklist.ja.md` の頻出パターンに分類し、パターン外の新種を抽出する (新種はチェックリスト更新の提案候補)。チェックリストが無い repo では Step 2 の既知カテゴリだけで分類する。

## Step 5 — スナップショットと差分報告

**新しいスナップショットを書く前に、Step 1 で特定した「最新の既存 session-*.json」があればその中身をまるごとメモリ上に退避しておく** (比較用)。`session-<YYYY-MM-DD>.json` は日単位のファイル名なので、同じ UTC 日に 2 回目の `/session-retro` を実行すると新しいスナップショットが前回分を上書きする。書き込んでから前回分を読もうとすると、たった今書いた今回分を「前回」として読んでしまい自己比較になる (常に「書く前に退避 → 書く → 退避しておいた前回分と比較」の順を守る)。

退避できたら Step 1 で決めたスナップショットディレクトリに `session-<YYYY-MM-DD>.json` を書く (ディレクトリは無ければ作成)。ignore されない repo で作業ツリーに書かないのは、`git status` を汚さず「収集は read-only」を保つため。`window.until` には Step 1 で収集前に固定した `UNTIL` をそのまま書く (Step 5 実行時の現在時刻ではない — ズレると収集後〜書き込み前のイベントを恒久的に取りこぼす)。スキーマ:

```json
{"schema": 1, "generated_at": "<ISO8601>",
 "window": {"since": "<ISO8601>", "until": "<ISO8601>"},
 "tool_errors": {"total": 0, "by_category": {}, "truncated": false},
 "ci": {"failed_runs": 0, "by_workflow": {}, "truncated": false},
 "review": {"comments": 0, "by_pattern": {}}}
```

`tool_errors.truncated` は Step 2 の `$tool_errors_truncated`、`ci.truncated` は Step 3 の `$ci_truncated` をそのまま書く (警告を echo するだけでは schema に残らず、次回比較が過少な値のまま進んでしまう)。

退避しておいた前回スナップショットと比較し、チャットに 3 区分で報告する: **新規** (前回に無いカテゴリ) / **再発** (前回も今回も非ゼロ) / **改善** (減少・ゼロ化)。初回 (退避できる前回分が無い) はスナップショット作成と今回分の集計のみ報告する。

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
