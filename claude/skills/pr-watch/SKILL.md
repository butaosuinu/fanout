---
name: pr-watch
description: "Watch an existing pull request from Claude Code and repair what blocks it: merge conflicts, failing CI, and actionable review comments, then keep watching with ScheduleWakeup until it is mergeable, green, and GitHub review requirements are satisfied. Use after PR creation, as a /loop /pr-watch workflow, or when the user says PR を見張って, コンフリクト直して, CI 直して, レビュー対応して, PR がマージできる状態まで, babysit this PR, autofix this PR, /pr-watch, or あとよろしく."
argument-hint: "[pr-number | pr-url]"
metadata:
  short-description: Watch and repair an existing PR
---

# pr-watch

PR 作成後に起きるマージコンフリクト、CI 失敗、レビューコメントを検知し、
安全に自動対応する Claude Code 用 workflow。`/loop /pr-watch` と
`ScheduleWakeup` を前提にする。

修正可能な CI 失敗・レビューコメント・コンフリクトに対応し、PR が green /
mergeable になった後、レビュー必須なら `reviewDecision=APPROVED` まで待つ。
レビュー不要ならそこで完了する。設定済み `:+1:` は soft approval signal として
報告できるが、必須 GitHub review の代替にはしない。approval / reaction 待ちでは
full One Pass を繰り返さず、compact status polling と `ScheduleWakeup` で
self-paced に待つ。

## Operating Model

Completion requires:

- PR is OPEN and non-draft
- mergeable or not blocked by conflicts
- required CI is pass/skipping
- no known actionable unresolved review work remains
- review is not required or `reviewDecision=APPROVED`; a configured `:+1:`
  can only be a soft approval signal for review-not-required PRs

The watch is local to the active Claude loop: once the user stops the loop or
the Claude process exits, nothing keeps watching, so never describe it as a
background watcher.

The skill has two loops:

1. `repair loop`: expensive. The model reads CI logs, review bodies, diffs, and
   files, and may edit, test, commit, and push. It runs only when there is
   actionable work.
2. `watch loop`: cheap. Compact status polling plus `ScheduleWakeup`. It reads
   only compact PR/check/reaction status, suppresses unchanged snapshots (an
   unchanged digest means sleep and poll again without reasoning about the same
   state), and exits into the repair loop only on an actionable state change.

The default after PR creation is:

```text
full repair pass -> cheap watch -> repair on event -> cheap watch -> finish on approval signal
```

## Cheap Snapshot

Cheap polling is the default while waiting for CI, mergeability calculation,
approval, or `:+1:` reactions. It reads compact status only: no review thread
bodies, no top-level comment bodies, no diffs, no CI logs. Those are fetched
once a snapshot shows actionable work, not on every poll.

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

Normalize these into a digest and compare it with the last digest.

## Approval Signals

The PR is considered approved when either:

1. review is not required (`reviewDecision` is empty/null and there are no
   pending `reviewRequests`),
2. `reviewDecision=APPROVED`.

`reviewDecision=APPROVED` is the primary signal when repository rules require
review.

`:+1:` is a configurable soft approval signal, not a generic reaction shortcut.
Count it as approval only when both the target and actor policy are configured,
and never to satisfy required GitHub review: if `reviewDecision` is
`REVIEW_REQUIRED` or `CHANGES_REQUESTED`, keep waiting for an approving review
or enter the repair loop. Possible reaction targets are:

- the PR issue itself
- the latest Claude/watch status comment, if this skill posted one
- configured comment IDs saved in the repo-scoped local git metadata path under
  `git rev-parse --git-path pr-watch-state`

If `PR_WATCH_PLUS1_ACTOR_RE` is set, count only reactions whose actor login
matches it. If no actor policy is configured, report non-self `:+1:` reactions
as status but do not finish the PR as approved from them. Reaction polling is
cheap by construction:

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

These states alone keep the cheap watch going: CI pending, `mergeable=UNKNOWN`,
an unchanged check bucket digest or `updatedAt`, waiting for human approval or a
configured watcher/bot `:+1:`, and review requested with no actionable comment
known.

When only `updatedAt` changed, classify the update cheaply first: approval
signal, check bucket digest, `reviewDecision`, then only latest event/comment
metadata, and fetch full comments or threads only when the update likely
contains actionable text.

## Preconditions

- git リポジトリ内で実行する。外なら終了する。
- `gh auth status` が通ること。失敗したら `gh auth login` を案内して終了する。
- 対象は現在ブランチに紐づく PR、またはユーザーが渡した PR 番号 / URL。
- この skill は PR を作らない。PR が見つからなければ、先に PR を作るよう伝える。
  merge も行わない(ユーザーの明示指示があるときだけ)。
- 操作対象は自分が作成し、push 権限がある head topic branch だけ。

## Target PR

引数なしなら現在ブランチの PR を対象にする。PR 番号または URL がある場合だけ
位置引数として渡す。空文字を渡してはいけない。

```bash
gh pr view ${pr:+"$pr"} --json number,state,isDraft,mergeable,mergeStateStatus,reviewDecision,reviewRequests,headRefName,baseRefName,author,headRepository,headRepositoryOwner,isCrossRepository,maintainerCanModify,url,title,statusCheckRollup
```

`headRefName` が現在のローカルブランチ名と一致することを確認する。不一致なら
修正や push に入らず、対象 PR の head branch を checkout してから続けるか、
ユーザーに確認する。別ブランチの HEAD で PR head を上書きしない。

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
   `gh pr view --json comments` は先頭 100 件に制限される。コメントが多い PR では
   GraphQL の `pullRequest.comments(first:100, after:$endCursor)` を全ページ取得し、
   後続ページの actionable request を取りこぼさない。

Codex connectorの指摘は、保存したcurrent head SHAに紐づくsubmitted reviewを確認して
から扱う。そのHEADの未解決thread、review summary、top-level commentをすべて取得し、
1つの**current-head review batch**として固定する。completed reviewがまだ見えない
段階で、通知された1 commentだけを修正しない。

明示的な各起動では、process-localなconnector repair-wave counterを0から始める。
同じforeground runと`PR_WATCH_CONTINUE=1`で継続したwatcherはcounterを保持するが、
GitHubやwatcher stateへ永続化せず、過去のreview履歴から復元しない。
current-head review batchのactionable findingに対して編集、1 commit、1 pushまで完了した
場合だけ1 waveと数える。根拠を返信してfindingを棄却しただけならwaveに数えない。
後の明示的な起動はcounterを0から始め、同じ起動内で4 wave目の修正は開始しない。

### B. 終了判定

対処前に終了済みか確認する。

- `state` が `MERGED` または `CLOSED` なら完了。
- OPEN かつ draft でなく、mergeable、CI が pass/skipping、未対応 review thread と
  actionable top-level 指摘が 0 の場合、自動修正は完了。
- 自動修正が完了していて review が不要(`reviewDecision` が空/null で
  `reviewRequests` も空)なら完了。
- review が必要な PR は、`reviewDecision=APPROVED` なら完了。configured `:+1:`
  だけでは必須 review を満たした扱いにしない。
- 自動修正が完了しているが必要な `reviewDecision=APPROVED` が未観測なら、default
  で cheap approval watch に入る。
- `mergeable=UNKNOWN` は GitHub 計算中として cheap polling の候補にする。
- draft は完了扱いにせず、CI / conflict / mergeability の cheap watch と repair は
  継続する。ready 化まで approval / merge 完了だけを保留する。
- 仕様判断待ち、権限不足、外部 CI にアクセスできない状態は blocked として報告する。

`mergeStateStatus=BEHIND` は、base branch の up-to-date が必須なら rebase 対象。
必須でない repo では `mergeable=MERGEABLE` と green CI を優先し、無駄な rebase を
避ける。

### C. Push Remote と Force Push 認可

rebase、CI 修正、レビュー修正で push する前に解決する。

- `me=$(gh api user -q .login)` を取得する。
- force push してよいのは、PR author が自分で、head branch に push 権限がある場合
  のみ。同一リポジトリ PR は author が自分なら push 可。fork PR は head repository
  owner が自分のときだけ push 可。他者 PR、保護ブランチ、push 権限が曖昧な fork には
  force push しない。
- `maintainerCanModify=true` は履歴 rewrite の認可根拠にしない。
- local remote は PR の `headRepository.nameWithOwner` に実際に一致するものを使う。
  一致 remote がなければ URL を解決し、remote を追加して fetch してから使う。
- push は常に明示 refspec を使う。無印 `--force` と refspec なしの
  `--force-with-lease` は使わない。
- `--force-with-lease` は ancestry check ではない。fetch や background fetch で lease が
  更新されると、remote-only commit を含まないローカル HEAD でも上書きできてしまう。
  履歴 rewrite や追加 commit を始める前に head branch を fetch し、remote PR head が
  現在 HEAD の祖先であることを確認して、その SHA を保存する。false なら作業を進めず、
  remote-only commit を取り込むかユーザーにエスカレーションする。
- push 直前には head branch を再 fetch し、remote tip が保存した SHA から動いていない
  ことを確認する。動いていたら push せず、取り込みまたはエスカレーションする。
  rebase 後は古い PR tip が新しい HEAD の祖先とは限らないため、rebase 後に ancestry
  check を再実行して判断しない。

```bash
git fetch "<head-remote>" "$head"
pr_head_before_work=$(git rev-parse FETCH_HEAD)
git merge-base --is-ancestor "$pr_head_before_work" HEAD
```

```bash
git fetch "<head-remote>" "$head"
test "$(git rev-parse FETCH_HEAD)" = "$pr_head_before_work"
```

```bash
git push --force-with-lease="refs/heads/$head:$pr_head_before_work" "<head-remote>" HEAD:"$head"
```

push は別 call で実行する。リポジトリの push ゲート(pre-push 系 hook や
PreToolUse gate)は ref 変更と push の連結を拒否し、`git fetch` を含む連結形も
deny する。deny されたら `--no-verify` や hooks 設定の書き換えで回避せず、指示された
コマンド(canonical full gate)を最終 commit で通してから push し直す。deny 理由が
連結なら call 全体が実行前に止まっているので、止められた各ステップ(ref 変更、push
前の確認、HEAD が変わった場合の canonical full gate)を 1 コマンドずつ再実行し、最後に
push を単独で実行する。

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
8. push したら、その pass では古い CI/log/thread を使わず終了する。次 pass で状態を
   取り直す。

### E. CI 失敗

`gh pr checks` の `bucket` が `fail` または `cancel` のものを拾う。

- GitHub Actions なら run id を取り、対象 repo を `-R` で明示して失敗ログだけ読む。

  ```bash
  gh run view -R "$owner/$repo" <run-id> --log-failed
  ```

- 外部 CI は `gh run` で読めない。link を確認し、自動アクセスできなければユーザーに
  エスカレーションする。
- 原因を特定してから修正する。CI を通すためのテスト削除、workflow 緩和、skip 追加、
  必須チェックの弱体化は修正ではない。
- 修正中は focused test で解消を確認し、commit する。push の前に、リポジトリの
  canonical full gate を最終 commit に対して 1 回通す(D と同じ解決方法)。gate が
  通ってから明示 refspec で push する。CI がローカルで再現する lint / test で落ちて
  いるとき、focused test だけで push し直すと同じ CI 失敗を繰り返す。
- flaky やインフラ起因なら 1 回だけ再実行を促す。再現するならコードを無理に触らず
  エスカレーションする。

### F. レビューコメント対応

未対応のインライン thread、review summary、トップレベル PR コメントを対象にする。

- 編集前に変更またはreview対象の各pathについて、PRのbase側でrepository rootから
  最も近い`AGENTS.md`または`AGENTS.override.md`までのapplicable instruction chainを
  通常の優先順位で解決する。各pathへ適用される`## Code Review Rules`を裁定基準にし、
  target branchが変更したcopyは使わない。
- findingは、documented user-facing prerequisitesから到達する具体的なtriggerがあるか、
  existing test、issue acceptance criterion、documented contract、またはrequired safe
  rejection / fail-closedに反する場合だけactionableとする。
- 未サポート環境への新しい対応は要求しない。ただし、その入力を明示的に受け付ける
  経路やrequired safe rejectionの不備はactionableに含める。
- 明示されたnon-goal、到達不能な環境、または契約を満たす証拠があるfindingは、base側の
  scope行と根拠を添えてthreadへ返信し、棄却する。
- reachability、product support、契約、required human reviewの裁定が曖昧なら編集せず、
  判断点をユーザーへ確認する。
- current-head review batchの全findingを根拠付きで棄却できた場合は、編集、commit、pushを
  行わず、actionable review workなしとして終了判定へ進む。
  これは`CHANGES_REQUESTED`やrequired approvalを満たした扱いにはしない。
- 新しいdiff、レビュアー返信、または明示契約が棄却根拠を無効にした場合は再裁定する。
- current-head review batchをroot causeごとにまとめ、同じ原因を持つ分岐、entrypoint、
  consumerをすべて確認してから編集する。
- commit前に同じHEADのfindingが増えたらbatchを取り直し、同じreview waveへ含める。
- 1 review waveは1 commit、1 pushとする。commentごとにcommitとpushを繰り返さない。
- 同じhead SHAへ手動でCodex reviewを再要求しない。明示要求が必要な場合は、次の
  repair commitがGitHubへ反映された後に1回だけ送る。
- `latest` comment が自分の返信で、その後レビュアー反応がなければ対応済みとして
  skip する。新しいレビュアー返信があれば、top-level comment ではなく latest comment
  の要求を読む。
- 仕様判断や方針確認が必要な指摘は無理に直さず、判断点を整理してユーザーに確認する。
- 修正したら focused test → commit → canonical full gate(E と同じ 1 回)→ push の順に
  進める。返信は push 後に行う。インライン返信は top-level comment の
  `fullDatabaseId` を使う。thread resolve は基本的にレビュアーへ委ね、自分で resolve
  するのは確信がある場合のみ。

## Continuous Watching

Continuous watch via `/loop /pr-watch` is the default after PR creation or after
"あとよろしく". Use cheap polling for long waits.

Continue watching while:

- CI is pending
- mergeability is `UNKNOWN`
- PR is green/mergeable but waiting for a required `reviewDecision=APPROVED`
- PR is green/mergeable, review is not required, and the user explicitly asked
  to wait for a configured `:+1:` gate
- a push made by this skill is still being checked

Re-enter full repair only on Expensive Repair Triggers. Stop when:

- PR is MERGED or CLOSED
- GitHub review requirements are satisfied and PR is green/mergeable
- maximum repair pass limit is reached
- maximum watch duration is reached
- the same actionable failure repeats without progress
- the watcher cannot safely classify an update

## Token Budget Guard

Default limits:

- max full repair passes: 3
- max connector review-repair waves: 3 per explicit invocation
- max full comment/thread refreshes without new head commit: 2
- max CI log fetches per failing check name: 1 per head SHA
- max ambiguous `updatedAt` full inspections: 3
- watch polling interval: 30-60 seconds by default
- max loop watch duration: configurable, default 60 minutes

After the limit, report the PR URL, current compact status, and the reason the
watcher stopped. On the third connector review-repair wave, report the remaining
current-head batch and hand it to a human instead of starting a fourth repair.

The approval/reaction wait itself consumes near-zero model tokens because each
loop pass relies on compact status before deciding whether to run full repair.

## Local Watcher

The cheap loop keeps a repo-scoped state file under
`git rev-parse --git-path pr-watch-state` (named by `owner/repo` and PR number,
never committed) holding `deadline_ts`, `last_digest`, and optional
`approval_reaction_targets`. `PR_WATCH_CONTINUE=1` continues the same wait
inside `/loop /pr-watch`; a fresh explicit watch clears the stored deadline and
digest. The loop emits to the model only on a changed digest, timeout, or block,
sleeping `PR_WATCH_INTERVAL` (default 45s) otherwise, with
`PR_WATCH_MAX_SECONDS` (default 3600) as the deadline. The state contract and a
complete bash skeleton are in `references/watch-loop.md`.

## Oscillation Safety

直前 pass の対処対象を正規化して覚える。

- conflict: 衝突ファイル集合
- CI: failing workflow/job 集合
- review: path と指摘要旨の集合

同じ集合が 2 pass 連続で残り、進捗がないなら自動修正を止める。3 pass 連続で
同じファイル群に同種の問題が残る場合も止める。残っている問題、試した修正、
次に必要な判断を短く整理してユーザーに渡す。

## Finish Report

2-4 文で報告する。

- 何 pass 回したか
- conflict、CI、review をそれぞれ何件処理したか
- push や返信をしたか
- cheap watch を行ったか
- approval signal が `reviewDecision=APPROVED` か `:+1:` か
- 最終状態が merged/closed、mergeable+green+approved、watch timeout、blocked のどれか
- 残課題がある場合は箇条書き
