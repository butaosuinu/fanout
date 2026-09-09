---
name: session-retro
description: "Mine past Codex rollout tool failures, CI failures, and Codex review findings; compare them with the previous snapshot; and propose recurrence-prevention changes. Use for `$session-retro`, retro, session retrospectives, or requests to analyze recurring Codex failures. Collection is read-only except for the snapshot, and applying proposals requires explicit user approval."
---

# session-retro

Codex CLI 専用の振り返り skill。Codex の内部 rollout JSONL 形式に依存する。
fanout run のレビュー往復数や time-to-merge は、#369 `fanout retro` CLI と
#370 `/fanout-retro` が扱う別レーン。

## ガードレール

- 収集で書いてよいのは Codex 専用スナップショットだけ。
  `.fanout/retro/` が ignore される repo では
  `<root>/.fanout/retro/codex-session-<date>.json`、それ以外では
  `${CODEX_HOME:-$HOME/.codex}/fanout-retro/<repo-key>/codex-session-<date>.json`
  に保存する。Claude 版の `session-<date>.json` を上書きしない。
- repo ファイルへの改善適用は提案まで。ユーザーが承認した後にブランチと PR で
  適用する。`internal/app/briefing` と settings は自動で書き換えない。
- issue、PR コメント、review reply を投稿しない。GitHub は read-only で調べる。
- memory は、ユーザーが明示的に更新を依頼し、現在の Codex 環境が定める更新手順を
  確認した場合だけ変更する。
- rollout 全文や user prompt をコンテキストへ読み込まない。先に構造的に集計し、
  代表例はカテゴリごとに最大 3 件、1 件につき 1〜2 行だけ示す。credential、token、
  header、個人情報は伏せる。
- 収集中に subagent を起動しない。現在の thread とその子孫 rollout は集計から外す。
- snapshot path、lock、前回ファイルの検証に失敗したら書き込まずに止める。

## Step 1: 対象と期間

冒頭で変数と一時領域を 1 回だけ用意する。

```bash
codex_home="${CODEX_HOME:-$HOME/.codex}"
tmpdir=$(mktemp -d)
lockdir=
cleanup() {
  if [ -n "$lockdir" ] && ! rmdir "$lockdir"; then
    echo "警告: session-retro lock を解放できなかった: $lockdir" >&2
  fi
  rm -rf "$tmpdir"
}
trap cleanup EXIT
common_dir=$(git rev-parse --path-format=absolute --git-common-dir)
common_dir=$(cd "$common_dir" && pwd -P)
root=$(dirname "$common_dir")
repo_key=$(printf '%s' "$common_dir" | git hash-object --stdin)
```

- `root` は linked worktree から実行しても main repo root を指す。
- `git -C "$root" check-ignore .fanout/retro/` が成功する場合は
  `$root/.fanout/retro`、失敗する場合は
  `$codex_home/fanout-retro/$repo_key` を snapshot directory にする。
  `check-ignore` の対象には末尾 `/` を付け、必ず `git -C "$root"` で実行する。
- 新規 snapshot を書く前に、最新の `codex-session-*.json` の内容を比較用に退避する。
  その snapshot の `repository.root`、`repository.common_dir`、`repository.key` が
  現在の `root`、`common_dir`、`repo_key` に一致する場合だけ前回値として扱い、
  `SINCE` に `window.until` を使う。既存 snapshot がない初回だけ、14 日前の UTC 時刻を
  使う。既存 snapshot の identity が欠落または不一致なら、初回扱いや上書きをせず止める。
- `UNTIL=$(date -u +%Y-%m-%dT%H:%M:%S.000000000Z)` は、Step 2〜4 の収集を
  始める前に固定する。現在の UTC 秒の先頭を境界にするため、その秒の途中で追加された
  event は次回 window に残る。`SINCE` と `UNTIL` は小数部 9 桁で保存する。
- rollout root は `$codex_home/sessions` と、存在する場合だけ
  `$codex_home/archived_sessions`。`browser/sessions` と
  `computer-use/sessions` は Codex rollout ではないので走査しない。
- `rollout-*.jsonl` の mtime が `SINCE` 以降のファイルを粗い候補にする。
  mtime に `UNTIL` 上限を付けない。厳密な window は各 JSONL 行の top-level
  `timestamp` で `(SINCE, UNTIL]` に絞る。
- 各候補の先頭行は `session_meta` として読み、`payload.cwd` から
  `git -C "$candidate_cwd" rev-parse --path-format=absolute --git-common-dir` を解決する。
  その物理パスが `common_dir` と一致する session だけを対象にする。repo の
  subdirectory、linked worktree、root 外の worktree は common-dir が同じ場合だけ含め、
  nested repository と submodule は除外する。cwd が消失している、Git repository ではない、
  または common-dir を解決できない候補は除外して `tool_errors.truncated=true` にする。
- `CODEX_THREAD_ID` と `CODEX_SESSION_ID` は空でない値だけを self ID として使う。
  候補の `session_meta.payload.id` / `session_id` と `parent_thread_id` から子孫を
  推移的に求め、self ID とその全子孫を除外する。空値を wildcard として扱わない。
- 読めないファイル、壊れた先頭行、未知の必須フィールドが 1 件でもあれば警告し、
  `tool_errors.truncated=true` にする。rollout root が両方とも無い場合も 0 件と
  断定しない。

既存 snapshot は前回値を読む前に全体を検証する。次を 1 つでも満たさなければ、
`SINCE` を進めず、新 snapshot も書かずに止める。

- top-level が object で、`schema == 1`、`source == "codex"`。
- `generated_at`、`window.since`、`window.until` が timestamp として正規化でき、
  `window.since < window.until <= UNTIL`。今回の `SINCE=window.until` についても
  `SINCE < UNTIL`。時計の巻き戻りや空の window を正常値として扱わない。
- `repository` が object で、`root`、`common_dir`、`key` が string かつ現在値と一致する。
- `tool_errors`、`ci`、`review` が object。`total` / `failed_runs` / `comments` は
  0 以上の integer、各 `truncated` は boolean、`by_category` / `by_workflow` /
  `by_pattern` は object で全 value が 0 以上の integer。

既知の限界: session 開始時の `session_meta.payload.cwd` で repo を決めるため、別の
directory で開始してからこの repo に移動した session は含まれない。この repo で
開始後に別 repo へ移動した session は含まれる。削除済み worktree は common-dir を
再検証できないので除外して truncated とする。rollout は Codex の内部形式なので、
schema が変わったら過少集計せず `truncated=true` で止める。

### Snapshot boundary と排他制御

snapshot directory を作る前後に、保存先の境界を検証する。

- repo-local では物理パスの `$root/.fanout/retro`、fallback では物理パスの
  `$codex_home/fanout-retro/$repo_key` だけを許可する。許可 root から保存先までの
  各 component を `lstat` 相当で調べ、symlink、現在の uid が所有しない directory、
  directory 以外を拒否する。物理パスが期待値と異なる場合も拒否する。
- directory は `umask 077` の下で component ごとに作り、作成後に同じ検証を繰り返す。
  snapshot directory の device と inode を記録する。
- snapshot directory 内に repo 単位の `.codex-session-retro.lock` directory を
  atomic に作る。`lockdir` には `mkdir` が成功した後だけその path を代入する。
  すでに存在する場合は待機、削除、上書きせず、別実行が進行中または
  stale lock の可能性を報告して止める。lock は前回 snapshot を選ぶ前に取得し、
  新 snapshot の rename 完了まで保持する。終了時は自分が作った空の lock directory
  だけを `rmdir` し、`tmpdir` とともに EXIT trap で片付ける。lock directory の
  device と inode も取得時に記録する。
- `codex-session-*.json` に symlink、現在の uid が所有しない file、regular file 以外が
  1 件でもあれば止める。前回 snapshot と同日再実行の置換先は、regular non-symlink
  file のみ受け入れる。
- 新 snapshot は `umask 077` のまま `mktemp` で snapshot directory 内に直接作る。
  rename の直前に directory chain、snapshot directory と lock directory の device・
  inode を再検証する。不一致なら temp file を消して止める。別 filesystem の temp file
  や追跡不能な固定名を使わない。

この lock により、前回値の読取から cursor 更新までを直列化する。プロセス停止後の
stale lock は自動回収しない。利用者が実行中プロセスと path identity を確認してから
手動で空 directory を除く。

## Step 2: ツール失敗

Codex rollout の tool result は Claude transcript の top-level
`"is_error":true` と同じ形ではない。固定文字列の `error` 検索は user prompt、
tool input、過去の retro 自体を誤検出するので使わない。

現行 rollout では、正規化済みの完了状態を `event_msg` から読む。

```jq
select(.type == "event_msg" and .payload.type == "item_completed")
| . as $row
| .payload.item as $item
| select(
    $item.status == "failed"
    or $item.status == "incomplete"
    or ($item.type == "CommandExecution"
        and (($item.exit_code? | type) == "number" and $item.exit_code != 0))
  )
| {timestamp: $row.timestamp, id: $item.id, kind: $item.type,
   exit_code: ($item.exit_code // null),
   detail: (if ($item.stderr // "") != "" then $item.stderr
            else ($item.aggregated_output // null) end)}
```

1 個の `item_completed` を 1 tool failure と数える。`CommandExecution` は数値の
`exit_code != 0` も失敗とする。それ以外の item は明示的な `status == "failed"`
または `"incomplete"` だけを数え、status が無い item から失敗を推測しない。

modern / legacy を rollout 単位で分けない。すべての rollout で `event_msg` の失敗と
`response_item` の明示的な失敗を別々に集める。event 側は空でない item `id`、response
側は空でない `call_id` を surface 内の identity とし、同じ identity の行を 1 件にする。
response の `call_id` が event item の `id` または明示的な `call_id` と一致した場合だけ、
同じ logical call と証明して response 側を重複除外する。対応しない response failure は、
同じ rollout に `item_completed` があっても 1 件として数える。別 surface の event と同じ
失敗か判定できない場合も response 側を捨てず、両方を数えて
`tool_errors.truncated=true` にする。identity が欠ける response failure はその行を 1 件と
数え、同様に truncated とする。

古い rollout や別 surface では、失敗が `response_item` にしか残らない場合がある。
`payload.type` が `custom_tool_call_output` または `function_call_output` の行について、
配列なら `input_text.text`、文字列ならその文字列、object なら object 自体を対象にし、
JSON 文字列を `fromjson?` で decode する。decode 後の `isError == true` または
`is_error == true`、数値の `exit_code != 0` を fallback として数える。decode 結果が
object 以外ならこの判定へ渡さない。

```jq
def decoded_output:
  .payload.output as $out
  | if ($out | type) == "array" then
      $out[]? | select(type == "object" and .type == "input_text")
      | .text | fromjson?
    elif ($out | type) == "string" then
      $out | fromjson?
    elif ($out | type) == "object" then
      $out
    else empty end
  | select(type == "object");

select(.type == "response_item")
| select(.payload.type == "custom_tool_call_output"
      or .payload.type == "function_call_output")
| . as $row
| decoded_output
| select(.isError == true or .is_error == true
      or ((.exit_code? | type) == "number" and .exit_code != 0))
| {timestamp: $row.timestamp, call_id: $row.payload.call_id,
   exit_code: (.exit_code // null), detail: (.output // .content // null)}
```

output で失敗を確認できない call だけ、対応する `custom_tool_call` /
`function_call` の明示的な `status == "failed"` または `"incomplete"` を fallback
にする。`call_id` で output と結び、1 call 1 件に deduplicate する。この status
fallback も、event との明示的な identity 一致だけを重複除外し、対応しない明示的 failure
は数える。

rollout timestamp と snapshot の境界は、UTC の
`YYYY-MM-DDTHH:MM:SS[.1〜9桁]Z` だけを受け入れる。比較前に小数部の欠落を 0 とし、
右側を 0 で埋めた 9 桁へ正規化する。固定長の正規化値で `(SINCE, UNTIL]` を比較し、
秒単位へ切り捨てない。旧 snapshot の秒精度 `window.until` もこの方法で移行する。
`timestamp_key` が各入力につきちょうど 1 値を返すことを検証する。0 値または例外なら、
rollout は `tool_errors.truncated=true`、既存 snapshot は停止とする。tool input
全文は読み込まない。shell の non-zero は、expected no-match / probe、command failure、
timeout、cancellation、sandbox / approval に分ける。cancellation は exit `130` または
明示的な signal / cancellation metadata がある場合だけとする。exit `128` は stderr と
status を確認し、Git の fatal error などを cancellation にしない。ほかの既知カテゴリは
stale-read edit、PR gate deny、browser / MCP、zsh 構文、`gh api` / rate limit、
inline Python、誤パス、`jq`。新しいまとまりは新カテゴリにする。

```jq
def timestamp_key:
  capture("^(?<second>[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(?:\\.(?<fraction>[0-9]{1,9}))?Z$") as $parts
  | ($parts.second + "Z") as $whole
  | (try ($whole | fromdateiso8601 | todateiso8601) catch empty) as $roundtrip
  | select($roundtrip == $whole)
  | $parts.second + "." + ((($parts.fraction // "") + "000000000")[0:9]) + "Z";
```

`tool_errors.total`、`by_category`、`truncated` を作ってから、必要な output だけを
代表例として読む。通常の非 JSON output を文字列だけで failure 扱いしない。

## Step 3: CI 失敗

`gh run list --status` は 1 回につき 1 status だけ受け取る。`failure`、
`startup_failure`、`timed_out` を別々に取得する。

```bash
runs="$tmpdir/runs"
: > "$runs"
ci_truncated=false
for st in failure startup_failure timed_out; do
  if out=$(gh run list --all --status "$st" --created "*..${UNTIL}" --limit 100 \
      --json databaseId,workflowName,displayTitle,headBranch,createdAt,updatedAt,conclusion); then
    printf '%s\n' "$out" >> "$runs"
  else
    ci_truncated=true
    printf '[]\n' >> "$runs"
    echo "警告: gh run list --status $st が失敗した" >&2
    continue
  fi
  n=$(printf '%s' "$out" | jq 'length' 2>/dev/null) || n=0
  if [ "${n:-0}" -ge 100 ]; then
    ci_truncated=true
    echo "警告: --status $st が --limit 100 に到達した" >&2
  fi
done
jq -s "(add // []) | [.[] | select(.updatedAt > \"${SINCE}\" and .updatedAt <= \"${UNTIL}\")]" "$runs"
```

window は run の開始時刻ではなく完了時刻 `updatedAt` で判定する。100 件に達した
status は期間を分割して再取得するか、`ci.truncated=true` のまま比較対象外にする。
workflow 別に数え、上位 workflow だけ `gh run view <id> --log-failed` から原因を
1 件抽出する。

## Step 4: レビュー指摘

REST API の `since` は `updated_at` 基準なので、取得後に `created_at` で
`(SINCE, UNTIL]` を適用する。API の終了コードを pipeline で隠さない。

```bash
comments="$tmpdir/comments"
if gh api --paginate \
    "repos/{owner}/{repo}/pulls/comments?since=${SINCE}&per_page=100" \
    --jq ".[] | select(.created_at > \"${SINCE}\" and .created_at <= \"${UNTIL}\")" \
    > "$comments"; then
  review_truncated=false
else
  review_truncated=true
  : > "$comments"
  echo "警告: gh api pulls/comments が失敗した" >&2
fi
```

fanout では `user.login == "chatgpt-codex-connector[bot]"` かつ
`in_reply_to_id == null` の inline review comment だけを指摘として数える。
人間の返信は含めない。`docs/review-checklist.ja.md` の既知パターンに分類し、
該当しない新種を改善候補にする。チェックリストが無い repo では Step 2 の
カテゴリを基準にする。

## Step 5: スナップショットと比較

同じ UTC 日の再実行では同じファイル名になる。Snapshot boundary の lock を保持した
状態で既存 snapshot の内容を退避してから、新しい snapshot を同じ directory の安全な
一時ファイルへ書き、directory identity を再検証して rename する。書いた後に読み直した
値を「前回」にしない。

```json
{"schema":1,"source":"codex","generated_at":"<ISO8601>",
 "repository":{"root":"<canonical-root>","common_dir":"<canonical-git-common-dir>",
               "key":"<repo-key>"},
 "window":{"since":"<ISO8601>","until":"<ISO8601>"},
 "tool_errors":{"total":0,"by_category":{},"truncated":false},
 "ci":{"failed_runs":0,"by_workflow":{},"truncated":false},
 "review":{"comments":0,"by_pattern":{},"truncated":false}}
```

`window.until` は Step 1 で固定した `UNTIL` をそのまま書く。退避した前回値と比較し、
チャットに新規、再発、改善の 3 区分で報告する。初回は今回の集計だけを報告する。
今回か前回のどちらかで対象メトリクスが `truncated=true` なら、そのメトリクスは
増減判定から外し「不完全につき比較対象外」とする。

既知の限界: truncated なメトリクスがあっても次回の `SINCE` は snapshot 全体の
`window.until` まで進む。その期間の欠落は次回収集で回収できない。メトリクス別 window
が必要になる変更は、この skill の範囲を超える。

## Step 6: 改善提案

新規、再発カテゴリごとに件数、伏せ字済み代表例、提案先を示す。

- Claude と Codex の全 child pane に効かせる規則は `CLAUDE.md` と `AGENTS.md` を
  対で提案する。Codex 固有なら `AGENTS.md` または `codex/skills/` に絞る。
- レビュー品質は `docs/review-checklist.ja.md` または `post-work-review` skill に提案する。
- root Codex session の挙動は memory 候補として示す。明示的な更新依頼が無ければ書かない。

## やらないこと

- briefing template、settings、repo file、memory の自動書き換え
- GitHub への書き込み
- `fanout retro` CLI の代替
