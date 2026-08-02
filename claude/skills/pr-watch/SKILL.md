---
name: pr-watch
description: "Use from Claude Code after PR creation or for an existing PR as a /loop /pr-watch workflow. Autonomously fix merge conflicts, failing CI, and actionable review comments, then use ScheduleWakeup to keep watching until the PR is mergeable, green, and GitHub review requirements are satisfied. A configured :+1: can be a soft approval signal only when GitHub review is not required. During approval/reaction waits, avoid repeatedly reading full review threads, comments, diffs, or CI logs; use compact status polling between loop passes and re-enter full repair only on actionable state changes. Use when the user says PR を見張って, コンフリクト直して, CI 直して, レビュー対応して, PR がマージできる状態まで, babysit this PR, autofix this PR, /pr-watch, /loop /pr-watch, or after PR creation says あとよろしく."
metadata:
  short-description: Watch and repair an existing PR
---

# pr-watch

PR 作成後に起きるマージコンフリクト、CI 失敗、レビューコメントを検知し、
安全に自動対応する Claude Code 用 workflow。

Claude 版の `pr-watch` は `/loop /pr-watch` と `ScheduleWakeup` を前提にする。
PR 作成後または「あとはよろしく」では、修正可能な CI 失敗・レビューコメント・
コンフリクトに対応し、PR が green / mergeable になった後、レビュー必須なら
`reviewDecision=APPROVED` まで待つ。レビュー不要ならそこで完了する。設定済み
`:+1:` は soft approval signal として報告できるが、必須 GitHub review の代替にはしない。

ただし、approval / reaction 待ちでは full One Pass を繰り返さず、compact status
polling と `ScheduleWakeup` で self-paced に待つ。Claude の loop を止めたら監視は
続かない。

## Operating Model

Default mode is `/loop /pr-watch` when the user wants continuous watching.

After PR creation, or when the user says "あとよろしく", this skill watches the
PR through Claude Code's loop, repairs tractable failures, and waits until
GitHub review requirements are satisfied.

Completion requires:

- PR is OPEN and non-draft
- mergeable or not blocked by conflicts
- required CI is pass/skipping
- no known actionable unresolved review work remains
- review is not required or `reviewDecision=APPROVED`; a configured `:+1:`
  can only be a soft approval signal for review-not-required PRs

The watch is local to the active Claude loop. It must not claim to keep watching
after the user stops the loop or the Claude process exits.

To reduce token usage, waiting must be compact and self-paced. Do not repeatedly
run full One Pass just to check whether approval or `:+1:` arrived. Run full
repair only when a cheap snapshot indicates actionable work.

## Two-loop Design

This skill has two loops:

1. `repair loop`
   - expensive
   - model reads CI logs, review bodies, diffs, and files
   - allowed to edit, test, commit, push
   - runs only when there is actionable work

2. `watch loop`
   - cheap
   - compact status polling plus `ScheduleWakeup`
   - model should not inspect repeated unchanged snapshots
   - reads only compact PR/check/reaction status
   - exits into repair loop only on actionable state change

The default after PR creation is:

```text
full repair pass -> cheap watch -> repair on event -> cheap watch -> finish on approval signal
```

The watch loop should suppress unchanged snapshots. If the compact digest has not
changed, sleep and poll again without asking the model to reason about the same
state.

## Cheap Snapshot

Cheap polling is the default while waiting for CI, mergeability calculation,
approval, or `:+1:` reactions. It must not fetch full review thread bodies, full
top-level comments, full diffs, or CI logs.

A cheap snapshot may fetch only compact PR status:

```bash
gh pr view ${pr:+"$pr"} \
  --json number,state,isDraft,mergeable,mergeStateStatus,reviewDecision,reviewRequests,updatedAt,headRefOid,url,headRefName,baseRefName,reactionGroups \
  --jq '{
    number,
    state,
    isDraft,
    mergeable,
    mergeStateStatus,
    reviewDecision,
    reviewRequests,
    updatedAt,
    headRefOid,
    url,
    headRefName,
    baseRefName,
    reactionGroups
  }'
```

and compact check buckets:

```bash
gh pr checks ${pr:+"$pr"} \
  --json name,bucket,state,workflow,link \
  --jq '
    group_by(.bucket)
    | map({
        bucket: .[0].bucket,
        count: length,
        checks: map({name, state, workflow, link}) | sort_by(.name, .workflow, .link, .state)
      })
  ' || true
```

The watcher must normalize these into a digest and compare it with the last
digest. If the digest is unchanged, do not ask the model to reason; sleep and
poll again.

## Approval Signals

The PR is considered approved when either:

1. review is not required (`reviewDecision` is empty/null and there are no
   pending `reviewRequests`),
2. `reviewDecision=APPROVED`.

`reviewDecision=APPROVED` is the primary signal when repository rules require
review.

`:+1:` is a configurable soft approval signal, not a generic reaction shortcut.
Count it as approval only when both the target and actor policy are configured.
Do not use `:+1:` to satisfy required GitHub review: if `reviewDecision` is
`REVIEW_REQUIRED` or `CHANGES_REQUESTED`, keep waiting for an approving review
or enter the repair loop.
Possible reaction targets are:

- the PR issue itself
- the latest Claude/watch status comment, if this skill posted one
- configured comment IDs saved in the repo-scoped local git metadata path under
  `git rev-parse --git-path pr-watch-state`

If `PR_WATCH_PLUS1_ACTOR_RE` is set, count only reactions whose actor login
matches it. If no actor policy is configured, report non-self `:+1:` reactions
as status but do not finish the PR as approved from them.

Reaction polling must be cheap. Do not fetch full PR comments or review threads
just to check for `:+1:`.

Examples:

```bash
# PR issue itself
gh api \
  "/repos/$owner/$repo/issues/$num/reactions?content=%2B1&per_page=100" \
  --paginate \
  --jq '[.[] | {login: .user.login, created_at}]'

# issue comment
gh api \
  "/repos/$owner/$repo/issues/comments/$comment_id/reactions?content=%2B1&per_page=100" \
  --paginate \
  --jq '[.[] | {login: .user.login, created_at}]'

# pull request review comment
gh api \
  "/repos/$owner/$repo/pulls/comments/$comment_id/reactions?content=%2B1&per_page=100" \
  --paginate \
  --jq '[.[] | {login: .user.login, created_at}]'
```

## Expensive Repair Triggers

Enter full One Pass only when a cheap snapshot shows actionable work:

- `mergeable=CONFLICTING`
- `mergeStateStatus=DIRTY`
- `mergeStateStatus=BEHIND` when branch protection or repo policy requires the
  PR branch to be up to date
- check bucket contains `fail` or `cancel`
- `reviewDecision=CHANGES_REQUESTED`
- `headRefOid` changed since the last full pass
- `updatedAt` changed and the watcher cannot classify it as pure approval/reaction activity
- a configured reaction target changed and the new reaction is not an approval signal
- CI completed after a push made by this skill and final verification has not yet run

Do not enter full One Pass for these states alone:

- CI pending
- `mergeable=UNKNOWN`
- unchanged check bucket digest
- unchanged `updatedAt`
- waiting for human approval
- waiting for a configured watcher/bot `:+1:`
- review requested but no actionable comment is known

When only `updatedAt` changed, first classify the update cheaply:

- check approval signal
- check check bucket digest
- check `reviewDecision`
- if still ambiguous, fetch only latest event/comment metadata
- fetch full comments or threads only if the update likely contains actionable text

## Preconditions

- git リポジトリ内で実行する。外なら終了する。
- `gh auth status` が通ること。失敗したら `gh auth login` を案内して終了する。
- 対象は現在ブランチに紐づく PR、またはユーザーが渡した PR 番号 / URL。
- この skill は PR を作らない。PR が見つからなければ、先に PR を作るよう伝える。
- 操作対象は自分が作成し、push 権限がある head topic branch だけ。

## Target PR

引数なしなら現在ブランチの PR を対象にする。PR 番号または URL がある場合だけ
位置引数として渡す。空文字を渡してはいけない。

```bash
gh pr view ${pr:+"$pr"} --json number,state,isDraft,mergeable,mergeStateStatus,reviewDecision,reviewRequests,headRefName,baseRefName,author,headRepository,headRepositoryOwner,isCrossRepository,maintainerCanModify,url,title,statusCheckRollup
```

`headRefName` が現在のローカルブランチ名と一致することを確認する。不一致なら
修正や push に入らず、対象 PR の head branch を checkout してから続けるか、
ユーザーに確認する。別ブランチの HEAD で PR head を上書きしてはいけない。

## One Pass

各ステップの前に、何を確認または修正するかを 1 文でユーザーへ伝える。

### A. 状態取得

1. `gh pr view` で PR 状態を取得する。
2. CI を取得する。`gh pr checks` は pending や failing で非 0 を返すことがあるので、
   exit code ではなく JSON の `bucket` を読む。

   ```bash
   gh pr checks ${pr:+"$pr"} --json name,state,bucket,link,workflow || true
   ```

3. 未解決 review thread を GraphQL で全ページ取得する。`isResolved=false` の thread、
   top-level comment の本文、latest comment の author/body を読む。

   ```bash
   gh api graphql --paginate -f query='
     query($owner:String!,$repo:String!,$num:Int!,$endCursor:String){
       repository(owner:$owner,name:$repo){
         pullRequest(number:$num){
           reviewThreads(first:100, after:$endCursor){
             pageInfo{ hasNextPage endCursor }
             nodes{ isResolved path line originalLine diffSide
               topLevel: comments(first:1){ nodes{ fullDatabaseId body diffHunk } }
               latest: comments(last:1){ nodes{ author{login} createdAt body } }
             }
           }
         }
       }
     }' -F owner="$owner" -F repo="$repo" -F num="$num"
   ```

4. `latestReviews` とトップレベル `comments` も読む。レビュー本文や PR コメントだけで
   actionable な修正依頼が来ることがあるため、thread だけで未対応 0 と判定しない。
   `gh pr view --json comments` は先頭 100 件に制限される。完了判定や blocked 判定で
   トップレベルコメントを使う前に、コメントが多い PR では GraphQL の
   `pullRequest.comments(first:100, after:$endCursor)` を全ページ取得し、後続ページの
   actionable request を取りこぼさない。

Codex connectorの指摘は、保存したcurrent head SHAに紐づくsubmitted reviewを確認して
から扱う。
そのHEADの未解決thread、review summary、top-level commentをすべて取得し、1つの
**current-head review batch**として固定する。
completed reviewがまだ見えない段階で、通知された1 commentだけを修正しない。

各起動時と、編集、commit、push、reply、review requestの前に、GitHubに永続化された
review履歴からPR単位のconnector wave数を復元する。
全reviewと全review threadをpaginateし、resolved / outdated threadも履歴数に含める。
actionableなconnector thread findingは`pullRequestReview.commit.oid`へ、review summaryは
reviewの`commit.oid`へ結び、distinctなreviewed headごとに数える。
current batchのHEADは1回だけ数える。
process-localな変数、ローカルcounter、watcher stateからwave数を初期化しない。
actionable feedbackをnon-nullのreview commit OIDへ結べない場合はfail closedにする。
復元結果が4 wave目ならPRを変更せず、人間へ引き継ぐ。

### B. 終了判定

対処前に終了済みか確認する。

- `state` が `MERGED` または `CLOSED` なら完了。
- OPEN かつ draft でなく、mergeable、CI が pass/skipping、未対応 review thread と
  actionable top-level 指摘が 0 の場合、自動修正は完了。
- 自動修正が完了していて review が不要（`reviewDecision` が空/null で
  `reviewRequests` も空）なら完了。
- review が必要な PR は、`reviewDecision=APPROVED` なら完了。configured `:+1:`
  だけでは必須 review を満たした扱いにしない。
- 自動修正が完了しているが必要な `reviewDecision=APPROVED` が未観測なら、default
  で cheap approval watch に入る。approval/reaction 待ちだけでは full
  comment/thread/log を再取得しない。
- `mergeable=UNKNOWN` は GitHub 計算中として cheap polling の候補にする。
- draft は完了扱いにせず、CI / conflict / mergeability の cheap watch と repair は
  継続する。ready 化まで approval / merge 完了だけを保留する。
- 仕様判断待ち、権限不足、外部 CI にアクセスできない状態は blocked として報告する。

`mergeStateStatus=BEHIND` は、base branch の up-to-date が必須なら rebase 対象。
必須でない repo では `mergeable=MERGEABLE` と green CI を優先し、無駄な rebase を
避ける。

### C. Push Remote と Force Push 認可

rebase、CI 修正、レビュー修正で push する前に必ず解決する。

- `me=$(gh api user -q .login)` を取得する。
- force push してよいのは、PR author が自分で、head branch に push 権限がある場合のみ。
- 同一リポジトリ PR は author が自分なら push 可。fork PR は head repository owner が
  自分のときだけ push 可。
- `maintainerCanModify=true` は履歴 rewrite の認可根拠にしない。
- local remote は PR の `headRepository.nameWithOwner` に実際に一致するものを使う。
  一致 remote がなければ URL を解決し、remote を追加して fetch してから使う。
- push は常に明示 refspec を使う。
- `--force-with-lease` は ancestry check ではない。fetch や background fetch で lease が
  更新されると、remote-only commit を含まないローカル HEAD でも上書きできてしまう。
  rebase などの履歴 rewrite や追加 commit を始める前に必ず head branch を fetch し、
  remote PR head が現在 HEAD の祖先であることを確認して、その SHA を保存する。false なら
  作業を進めず、remote-only commit を取り込むかユーザーにエスカレーションする。
- push 直前には head branch を再 fetch し、remote tip が保存した SHA から動いていないことを
  確認する。remote が動いていたら push せず、取り込みまたはエスカレーションする。
  rebase 後は古い PR tip が新しい HEAD の祖先とは限らないため、rebase 後に ancestry check を
  再実行して判断しない。

```bash
git fetch "<head-remote>" "$head"
pr_head_before_work=$(git rev-parse FETCH_HEAD)
git merge-base --is-ancestor "$pr_head_before_work" HEAD
```

```bash
git fetch "<head-remote>" "$head"
test "$(git rev-parse FETCH_HEAD)" = "$pr_head_before_work"
git push --force-with-lease="refs/heads/$head:$pr_head_before_work" "<head-remote>" HEAD:"$head"
```

無印 `--force`、refspec なしの `git push --force-with-lease`、保護ブランチや他者 PR
への force push は使わない。

### D. コンフリクトと Base Drift

`mergeable=CONFLICTING`、`mergeStateStatus=DIRTY`、または strict required checks で
`BEHIND` の場合に対応する。

1. dirty tree なら、勝手に捨てず、commit するか stash するかを判断する。
2. C の PR head guard を rebase 前に実行し、`pr_head_before_work` を保存する。
3. PR の base repository に一致する remote から base branch を fetch する。fork や
   triangular clone があるので `origin/main` 決め打ちはしない。
4. `git rebase FETCH_HEAD` で今 fetch した base に rebase する。
5. import 併合など確信できる hunk だけ自動解決する。意味的判断が必要な衝突は
   `git rebase --abort` して、具体的な hunk と理由を添えてエスカレーションする。
6. rebase 完了後、リポジトリの canonical full gate を rebase 後の HEAD に対して 1 回
   通す(プロジェクトが `make check` のような umbrella target を定義していればそれを
   優先し、無ければ AGENTS.md / CLAUDE.md / build ファイルから解決する)。自動解決した
   hunk が build を壊すのはこの時点で捕まえる。
7. push 直前に remote tip が `pr_head_before_work` から動いていないことを
   確認し、保存した SHA を期待値にした `--force-with-lease` で push する。
8. push したら、その pass では古い CI/log/thread を使わず終了する。必要なら次 pass で
   状態を取り直す。

### E. CI 失敗

`gh pr checks` の `bucket` が `fail` または `cancel` のものを拾う。

- GitHub Actions なら run id を取り、対象 repo を `-R` で明示して失敗ログだけ読む。

  ```bash
  gh run view -R "$owner/$repo" <run-id> --log-failed
  ```

- 外部 CI は `gh run` で読めない。link を確認し、自動アクセスできなければユーザーに
  エスカレーションする。
- 原因を特定してから修正する。CI を通すためにテスト削除、workflow 緩和、skip 追加を
  してはいけない。
- 修正中は focused test で解消を確認し、commit する。push の前に、リポジトリの
  canonical full gate を最終 commit に対して 1 回通す(D と同じ解決方法)。gate が
  通ってから明示 refspec で push する。CI がローカルで再現する lint / test で落ちて
  いるとき、focused test だけで push し直すと同じ CI 失敗を繰り返す。
- flaky やインフラ起因なら 1 回だけ再実行を促す。再現するならコードを無理に触らず
  エスカレーションする。

### F. レビューコメント対応

未対応のインライン thread、review summary、トップレベル PR コメントを対象にする。

- current-head review batchをroot causeごとにまとめ、同じ原因を持つ分岐、entrypoint、
  consumerをすべて確認してから編集する。
- headまたはreview metadataが変わった場合は、GitHubの全review履歴からPR単位の
  connector wave数を再構築する。
- commit前に同じHEADのfindingが増えたらbatchを取り直し、同じreview waveへ含める。
- 1 review waveは1 commit、1 pushとする。commentごとにcommitとpushを繰り返さない。
- 同じhead SHAへ手動でCodex reviewを再要求しない。明示要求が必要な場合は、次の
  repair commitがGitHubへ反映された後に1回だけ送る。

- `latest` comment が自分の返信で、その後レビュアー反応がなければ対応済みとして
  skip する。
- 新しいレビュアー返信があれば、top-level comment ではなく latest comment の要求を読む。
- 仕様判断や方針確認が必要な指摘は無理に直さず、判断点を整理してユーザーに確認する。
- 修正したら focused test → commit → canonical full gate(E と同じ 1 回)→ push の順に
  進める。
- 返信は push 後に行う。インライン返信は top-level comment の `fullDatabaseId` を使う。
- thread resolve は基本的にレビュアーへ委ねる。自分で resolve するのは確信がある場合のみ。

## Continuous Watching

Continuous watch via `/loop /pr-watch` is the default after PR creation or after
"あとよろしく".

Use cheap polling for long waits. The model should not repeatedly inspect
unchanged status output.

Continue watching while:

- CI is pending
- mergeability is `UNKNOWN`
- PR is green/mergeable but waiting for a required `reviewDecision=APPROVED`
- PR is green/mergeable, review is not required, and the user explicitly asked
  to wait for a configured `:+1:` gate
- a push made by this skill is still being checked

Re-enter full repair only on Expensive Repair Triggers.

Stop when:

- PR is MERGED or CLOSED
- GitHub review requirements are satisfied and PR is green/mergeable
- maximum repair pass limit is reached
- maximum watch duration is reached
- the same actionable failure repeats without progress
- the watcher cannot safely classify an update

Claude の loop を終了すると監視は続かない。バックグラウンドで見張っているように
表現しない。

## Token Budget Guard

Default limits:

- max full repair passes: 3
- max connector review-repair waves: 3
- max full comment/thread refreshes without new head commit: 2
- max CI log fetches per failing check name: 1 per head SHA
- max ambiguous `updatedAt` full inspections: 3
- watch polling interval: 30-60 seconds by default
- max loop watch duration: configurable, default 60 minutes

After the limit, report the PR URL, current compact status, and the reason the
watcher stopped.
On the third connector review-repair wave, report the remaining current-head
batch and hand it to a human instead of starting a fourth repair.

The approval/reaction wait itself should consume near-zero model tokens because
each loop pass should rely on compact status before deciding whether to run full
repair.

## Local Watcher Implementation Sketch

The watcher may create a repo-scoped local state file under the git metadata
path returned by `git rev-parse --git-path pr-watch-state`. Include the target
`owner/repo` and PR number in the filename so watching `#123` in another
repository cannot reuse this repository's `#123` state. This file is local and
must not be committed. Do not assume `.git` is a directory; linked worktrees
often use a `.git` file that points at the real gitdir.
When configured `:+1:` targets are used, the state JSON may include
`approval_reaction_targets` entries with `kind` values `issue`, `issue_comment`,
or `review_comment`.

Use `PR_WATCH_CONTINUE=1` only when Claude is continuing the same cheap wait
inside `/loop /pr-watch`. A fresh user-invoked watch should clear stored
`deadline_ts` / `last_digest` and issue a new loop deadline. If a stored
deadline is already expired, clear it before polling so a later explicit watch
does not immediately timeout.

The shell loop should emit output to the model only when:

- approval signal appears
- checks fail/cancel
- merge conflict appears
- `reviewDecision` becomes `CHANGES_REQUESTED`
- `headRefOid` changes
- an ambiguous update requires model classification
- timeout/block occurs

Example skeleton:

```bash
state_dir="$(git rev-parse --git-path pr-watch-state)"
mkdir -p "$state_dir"
repo_key="$(printf '%s\n' "$owner/$repo" | tr '/:' '--')"
state_file="$state_dir/$repo_key-$num.json"
state_json="{}"
if [ -f "$state_file" ]; then
  state_json="$(cat "$state_file")"
fi

now_ts="$(date +%s)"
max_seconds="${PR_WATCH_MAX_SECONDS:-3600}"
deadline_ts="$(printf '%s\n' "$state_json" | jq -r '.deadline_ts // empty')"
if [ "${PR_WATCH_CONTINUE:-0}" != "1" ] ||
   [ -z "$deadline_ts" ] ||
   [ "$deadline_ts" -le "$now_ts" ]; then
  state_json="$(printf '%s\n' "$state_json" | jq 'del(.deadline_ts, .last_digest)')"
  deadline_ts=$((now_ts + max_seconds))
fi
last_digest="$(printf '%s\n' "$state_json" | jq -c '.last_digest // empty')"

while :; do
  pr_tmp="$(mktemp)"
  gh pr view -R "$owner/$repo" "$num" \
    --json number,state,isDraft,mergeable,mergeStateStatus,reviewDecision,reviewRequests,updatedAt,headRefOid,url,reactionGroups \
    --jq '{number,state,isDraft,mergeable,mergeStateStatus,reviewDecision,reviewRequests,updatedAt,headRefOid,url,reactionGroups}' > "$pr_tmp"
  pr_status=$?
  pr_json="$(cat "$pr_tmp")"
  rm -f "$pr_tmp"
  if [ "$pr_status" -ne 0 ] || [ -z "$pr_json" ]; then
    jq -cn --argjson status "$pr_status" \
      '{event:"blocked", reason:"pr_snapshot_failed", status:$status}'
    break
  fi

  checks_tmp="$(mktemp)"
  checks_err="$(mktemp)"
  gh pr checks -R "$owner/$repo" "$num" \
    --json name,bucket,state,workflow,link \
    --jq 'group_by(.bucket) | map({bucket: .[0].bucket, count: length, checks: map({name,state,workflow,link}) | sort_by(.name,.workflow,.link,.state)})' > "$checks_tmp" 2> "$checks_err"
  checks_status=$?
  checks_json="$(cat "$checks_tmp")"
  checks_error="$(cat "$checks_err")"
  rm -f "$checks_tmp" "$checks_err"
  if [ -z "$checks_json" ]; then
    if [ "$checks_status" -ne 0 ]; then
      if printf '%s\n' "$checks_error" | grep -qi 'no checks reported'; then
        checks_json='[{"bucket":"pending","count":0,"checks":[],"reason":"checks_not_reported_yet"}]'
      else
        jq -cn --argjson status "$checks_status" --arg error "$checks_error" \
          '{event:"blocked", reason:"checks_snapshot_failed", status:$status, error:$error}'
        break
      fi
    else
      checks_json="[]"
    fi
  fi

  reaction_targets="$(
    printf '%s\n' "$state_json" |
      jq -c '.approval_reaction_targets // []'
  )"
  reaction_status_file="$(mktemp)"
  reaction_error_file="$(mktemp)"
  printf '0' > "$reaction_status_file"
  approval_reactions="$(
    printf '%s\n' "$reaction_targets" | jq -c '.[]' |
      while IFS= read -r target; do
        kind="$(printf '%s\n' "$target" | jq -r '.kind')"
        id="$(printf '%s\n' "$target" | jq -r '.id // empty')"
        case "$kind" in
          issue) path="/repos/$owner/$repo/issues/$num/reactions?content=%2B1&per_page=100" ;;
          issue_comment) path="/repos/$owner/$repo/issues/comments/$id/reactions?content=%2B1&per_page=100" ;;
          review_comment) path="/repos/$owner/$repo/pulls/comments/$id/reactions?content=%2B1&per_page=100" ;;
          *) continue ;;
        esac
        reaction_tmp="$(mktemp)"
        if ! gh api --paginate "$path" > "$reaction_tmp" 2>> "$reaction_error_file"; then
          printf '1' > "$reaction_status_file"
          rm -f "$reaction_tmp"
          break
        fi
        jq --arg kind "$kind" --arg id "$id" \
          '.[] | {target_kind: $kind, target_id: $id, login: .user.login, created_at}' \
          "$reaction_tmp"
        rm -f "$reaction_tmp"
      done |
      jq -s '.'
  )"
  if [ "$(cat "$reaction_status_file")" != "0" ]; then
    reaction_error="$(cat "$reaction_error_file")"
    rm -f "$reaction_status_file" "$reaction_error_file"
    jq -cn --arg error "$reaction_error" \
      '{event:"blocked", reason:"reaction_snapshot_failed", error:$error}'
    break
  fi
  rm -f "$reaction_status_file" "$reaction_error_file"
  if [ -z "$approval_reactions" ]; then
    approval_reactions="[]"
  fi

  digest="$(
    jq -cn \
      --argjson pr "$pr_json" \
      --argjson checks "$checks_json" \
      --argjson approvalReactions "$approval_reactions" \
      '{
        state: $pr.state,
        draft: $pr.isDraft,
        mergeable: $pr.mergeable,
        mergeStateStatus: $pr.mergeStateStatus,
        reviewDecision: $pr.reviewDecision,
        reviewRequests: ($pr.reviewRequests // []),
        headRefOid: $pr.headRefOid,
        updatedAt: $pr.updatedAt,
        reactionGroups: ($pr.reactionGroups // []),
        approvalReactions: $approvalReactions,
        checks: $checks
      }'
  )"

  if [ "$(date +%s)" -ge "$deadline_ts" ]; then
    jq -cn --argjson digest "$digest" '{event:"timeout", digest:$digest}'
    printf '%s\n' "$state_json" |
      jq 'del(.deadline_ts, .last_digest)' > "$state_file"
    break
  fi

  if [ "$digest" = "$last_digest" ]; then
    sleep "${PR_WATCH_INTERVAL:-45}"
    continue
  fi

  jq -cn \
    --argjson state "$state_json" \
    --argjson digest "$digest" \
    --argjson deadline "$deadline_ts" \
    '$state + {last_digest: $digest, deadline_ts: $deadline}' > "$state_file"
  last_digest="$digest"

  # Emit only changed digest. The model decides whether to finish,
  # continue cheap wait, or enter full repair.
  printf '%s\n' "$digest"
  break
done
```

## Oscillation Safety

直前 pass の対処対象を正規化して覚える。

- conflict: 衝突ファイル集合
- CI: failing workflow/job 集合
- review: path と指摘要旨の集合

同じ集合が 2 pass 連続で残り、進捗がないなら自動修正を止める。3 pass 連続で
同じファイル群に同種の問題が残る場合も止める。残っている問題、試した修正、
次に必要な判断を短く整理してユーザーに渡す。

## Do Not

- PR を新規作成しない。
- 明示指示なしに auto-merge しない。
- 他者 PR、保護ブランチ、push 権限が曖昧な fork に force push しない。
- CI を通すためにテストや必須チェックを弱めない。
- リポジトリの push ゲート(pre-push 系 hook や PreToolUse gate)を `--no-verify` や
  hooks 設定の書き換えで回避しない。push が deny されたら、指示されたコマンド
  (canonical full gate)を最終 commit で通してから push し直す。
- GitHub の古い thread や push 前の CI log を根拠に、push 後も同じ pass で修正を続けない。
- approval / `:+1:` 待ちだけのために full One Pass を繰り返さない。
- unchanged cheap snapshot をモデルに何度も読ませない。
- full review threads、top-level comments、CI logs、diffs を polling ごとに取得しない。
- Claude の loop / process 終了後も監視が続くように表現しない。

## Finish Report

2-4 文で報告する。

- 何 pass 回したか
- conflict、CI、review をそれぞれ何件処理したか
- push や返信をしたか
- cheap watch を行ったか
- approval signal が `reviewDecision=APPROVED` か `:+1:` か
- 最終状態が merged/closed、mergeable+green+approved、watch timeout、blocked のどれか
- 残課題がある場合は箇条書き
