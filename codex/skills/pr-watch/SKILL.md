---
name: pr-watch
description: Use from Codex CLI to monitor an existing pull request after creation and autonomously address merge conflicts, failing CI, and review comments until it is mergeable, green, and approved or genuinely blocked. Use when the user says PR を見張って, コンフリクト直して, CI 直して, レビュー対応して, PR がマージできる状態まで, babysit this PR, or after PR creation says あとよろしく.
metadata:
  short-description: Watch and repair an existing PR
---

# pr-watch

PR 作成後に起きるマージコンフリクト、CI 失敗、レビューコメントを検知し、
安全に自動対応する Codex 用 workflow。

Claude 版の `pr-watch` は `/loop` と `ScheduleWakeup` を前提にする。Codex 版では
バックグラウンド監視を装わない。通常は 1 pass を実行して現状と対応結果を報告する。
ユーザーが「継続して見張って」と明示した場合だけ、同じセッション内で短い polling
loop を回す。人間レビュー待ちなど長時間アイドルになる状態では、何を待っているかを
報告して止まる。

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

### B. 終了判定

対処前に終了済みか確認する。

- `state` が `MERGED` または `CLOSED` なら完了。
- OPEN かつ draft でなく、mergeable、CI が pass/skipping、未対応 review thread と
  actionable top-level 指摘が 0、承認済みまたはレビュー不要なら完了。
- `mergeable=UNKNOWN` は GitHub 計算中として短い待ち候補にする。
- draft、reviewer 待ち、CI pending は完了扱いにしない。対処対象がなければ何を
  待っているか報告する。

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

```bash
git push --force-with-lease "<head-remote>" HEAD:"$head"
```

無印 `--force`、refspec なしの `git push --force-with-lease`、保護ブランチや他者 PR
への force push は使わない。

### D. コンフリクトと Base Drift

`mergeable=CONFLICTING`、`mergeStateStatus=DIRTY`、または strict required checks で
`BEHIND` の場合に対応する。

1. dirty tree なら、勝手に捨てず、commit するか stash するかを判断する。
2. PR の base repository に一致する remote から base branch を fetch する。fork や
   triangular clone があるので `origin/main` 決め打ちはしない。
3. `git rebase FETCH_HEAD` で今 fetch した base に rebase する。
4. import 併合など確信できる hunk だけ自動解決する。意味的判断が必要な衝突は
   `git rebase --abort` して、具体的な hunk と理由を添えてエスカレーションする。
5. rebase 完了後に `git push --force-with-lease "<head-remote>" HEAD:"$head"`。
6. push したら、その pass では古い CI/log/thread を使わず終了する。必要なら次 pass で
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
- 修正後は focused test を実行し、commit して、明示 refspec で push する。
- flaky やインフラ起因なら 1 回だけ再実行を促す。再現するならコードを無理に触らず
  エスカレーションする。

### F. レビューコメント対応

未対応のインライン thread、review summary、トップレベル PR コメントを対象にする。

- `latest` comment が自分の返信で、その後レビュアー反応がなければ対応済みとして
  skip する。
- 新しいレビュアー返信があれば、top-level comment ではなく latest comment の要求を読む。
- 仕様判断や方針確認が必要な指摘は無理に直さず、判断点を整理してユーザーに確認する。
- 修正したら test、commit、push の順に進める。
- 返信は push 後に行う。インライン返信は top-level comment の `fullDatabaseId` を使う。
- thread resolve は基本的にレビュアーへ委ねる。自分で resolve するのは確信がある場合のみ。

## Continuous Watching

ユーザーが継続監視を明示した場合だけ、pass の後に状態に応じて待つ。

- CI 実行中、push 直後、`mergeable=UNKNOWN`: 数分待って再 pass。
- 人間レビュー待ちで CI green、衝突なし、未対応指摘なし: 長時間待ちになるため、
  状態を報告して停止する。

Codex セッションを終了すると監視は続かない。バックグラウンドで見張っているように
表現しない。

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
- GitHub の古い thread や push 前の CI log を根拠に、push 後も同じ pass で修正を続けない。

## Finish Report

2-4 文で報告する。

- 何 pass 回したか
- conflict、CI、review をそれぞれ何件処理したか
- push や返信をしたか
- 最終状態が merged/closed、mergeable+green、reviewer 待ち、CI 待ち、blocked のどれか
- 残課題がある場合は箇条書き
