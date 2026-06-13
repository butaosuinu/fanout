---
name: pr-watch
description: PR 作成後のマージコンフリクト、CI 失敗、レビューコメントを Codex が監視・対処するための skill。現在ブランチまたは指定 PR の状態を gh で特定し、ベース更新による衝突は安全に rebase + 自動解決して force-with-lease push、failing CI はログを読んで原因を修正、requested changes や未解決 review thread はコードで対応して返信する。ユーザーが「PR 作ったあと見張って」「PR を監視して」「コンフリクト直して」「CI 直して」「レビュー対応して」「マージできる状態まで持っていって」「babysit this PR」などと頼んだとき、または PR 作成直後に「あとよろしく」と依頼したときに使う。
---

# pr-watch

PR 作成後に発生する差し込み、つまり **マージコンフリクト / CI 失敗 / レビューコメント** を、Codex が現在のターンで安全に検知して対処する workflow。

Claude 版の `/loop` と `ScheduleWakeup` は Codex には無い。バックグラウンドで監視し続けるとは言わず、現在の Codex ターンで 1 pass 以上を実行する。CI pending や review 待ちのように外部イベント待ちになったら、短い bounded polling が有効な場合だけ待ち、それ以外は現状と次に再実行すべきタイミングを報告して終了する。

## 対象と前提

- 対象は現在 git ブランチに紐づく PR 1 本。ユーザーが PR 番号または URL を指定した場合はそれを対象にする。
- git リポジトリ外なら早期終了する: `git rev-parse --is-inside-work-tree`。
- `gh` 未認証なら早期終了し、`gh auth login` を案内する: `gh auth status`。
- 現在ブランチに PR が無ければ何もしない。この skill は PR を作らない。
- 対象 PR の head branch と現在のローカル branch が一致しない場合は、先に `gh pr checkout <pr>` などで対象 head を checkout する。対象外の HEAD を PR branch に push してはいけない。
- 他者の PR、protected branch、意味的判断が必要な conflict には force-push しない。

## まず取得する状態

PR を指定する場合だけ `$pr` を gh の位置引数に渡す。空文字を渡すと `gh` の「現在ブランチの PR」解決を壊すので、シェルでは `${pr:+"$pr"}` を使う。

```bash
gh pr view ${pr:+"$pr"} --json number,state,isDraft,mergeable,mergeStateStatus,reviewDecision,reviewRequests,headRefName,baseRefName,author,headRepository,headRepositoryOwner,isCrossRepository,maintainerCanModify,url,title,statusCheckRollup
gh pr checks ${pr:+"$pr"} --json name,state,bucket,link,workflow || true
```

`gh pr checks` は pending で exit 8、failure で non-zero になることがある。exit code ではなく JSON の `bucket` (`pass` / `fail` / `pending` / `skipping` / `cancel`) を見る。

未解決 review thread は GraphQL で取る。`reviewDecision` だけでは COMMENTED review や unresolved thread を見落とす。

```bash
gh api graphql --paginate -f query='
  query($owner:String!,$repo:String!,$num:Int!,$endCursor:String){
    repository(owner:$owner,name:$repo){
      pullRequest(number:$num){
        reviewThreads(first:100, after:$endCursor){
          pageInfo{ hasNextPage endCursor }
          nodes{
            isResolved isOutdated path line originalLine diffSide
            topLevel: comments(first:1){ nodes{ fullDatabaseId body diffHunk } }
            latest: comments(last:1){ nodes{ author{ login } createdAt body } }
          }
        }
      }
    }
  }' -F owner="$owner" -F repo="$repo" -F num="$num"
```

トップレベルコメントと review summary も取る。

```bash
gh pr view ${pr:+"$pr"} --json latestReviews,comments,reviewDecision
```

`comments` は 100 件制限なので、コメントが多い PR では GraphQL pagination で issue comments も全ページ取得してから完了判定する。

## 終了判定

対処に入る前に、もう完了していないかを見る。

- `state` が `MERGED` または `CLOSED` なら完了。
- `state=OPEN`、`isDraft=false`、`mergeable=MERGEABLE`、必須 CI が `pass` または `skipping`、未対応 review thread / review summary / top-level request が 0、承認シグナルがあるなら完了。PR の自動マージはしない。
- `reviewDecision=APPROVED` なら承認済み。レビュー必須でない repo で `reviewDecision` が空かつ `reviewRequests` が空なら、承認待ちは不要とみなせる。
- `mergeStateStatus=BEHIND` は repo の branch protection が up-to-date を必須にするかで扱いが変わる。`requiresStrictStatusChecks` が取れない場合は安全側に倒し、BEHIND を rebase 対象にする。
- `isDraft=true` なら完了にしない。conflict / CI の追従はしてよいが、ready 化待ちとして報告する。

## Push 先 remote と認可

以降の push は必ず対象 PR の head ref に明示ピンする。

```bash
git push --force-with-lease "<head-remote>" HEAD:"$head"
```

`<head-remote>` は PR の `headRepository.nameWithOwner` に一致する local remote を `git remote -v` から探して決める。`origin` 決め打ちはしない。remote が無い場合は `gh repo view <nameWithOwner> --json url -q .url` などで URL を取り、名前付き remote を追加して fetch する。raw URL へ直接 `--force-with-lease` push しない。remote-tracking ref が無く、lease が期待通り働かない。

force-push してよいのは次を両方満たすときだけ。

- `gh api user -q .login` が PR の `author.login` と一致する。
- head branch に push 権限がある。同一 repo PR なら可。fork PR なら `headRepositoryOwner.login` が自分のときだけ可。

`maintainerCanModify=true` は履歴 rewrite の根拠にしない。他者 fork や他者 author の PR に force-push しない。

rebase / force-push の前に、remote PR head が現在 HEAD の祖先か確認する。

```bash
git fetch "<head-remote>" "$head"
git merge-base --is-ancestor FETCH_HEAD HEAD
```

false なら、PR branch に自分のローカルに無い commit がある。force-push せず、取り込みまたはユーザーへのエスカレーションで止める。

## Conflict 対応

`mergeable=CONFLICTING` または `mergeStateStatus` が `DIRTY` / strict required な `BEHIND` のときに実行する。

1. 作業ツリーが dirty なら、未コミット変更を勝手に捨てない。必要なら commit / stash してから進める。
2. PR の base repository に一致する `<base-remote>` を解決し、base branch を fetch する。fork / triangular clone があるので `origin/$base` 決め打ちはしない。
3. `git fetch "<base-remote>" "$base"` の直後に `git rebase FETCH_HEAD` する。
4. conflict が出たら、import 併合、片側の単純追加、format 差など、両側の意図が明確な hunk だけ自動解決する。
5. 同じ関数を両側で別ロジックに変更している、削除 vs 変更、期待値が食い違うなど意味的判断が必要な conflict は自動解決しない。`git rebase --abort` で戻し、どの hunk が判断不能かを報告する。
6. rebase 完了後に `git push --force-with-lease "<head-remote>" HEAD:"$head"`。
7. rebase + push 後は、同じ pass の古い CI / review 情報を使って D/E に進まない。状態を取り直す。

## CI 失敗対応

`gh pr checks` の `bucket` が `fail` または `cancel` の check を対象にする。`cancel` はまず再実行できるか確認し、繰り返すならエスカレーションする。

GitHub Actions の run なら失敗ジョブだけ読む。

```bash
gh run view -R "$owner/$repo" "<run-id>" --log-failed
```

`-R "$owner/$repo"` は必須。PR URL 指定や fork / triangular checkout で、gh の default repository が対象 PR と違うことがある。

外部 CI の link は `gh run view` では読めない。link を開ける環境なら原因を確認し、自動アクセスできなければユーザーに link と check 名を渡して止める。

修正の流れ:

1. 何が落ちているか、何を直すかを 1-2 文で報告する。
2. lint / type / test / build failure の原因をコードで直す。
3. テストを削除する、workflow を緩める、品質 gate を無断で bypass する、といった「通すための改竄」はしない。
4. 修正を commit し、`git push --force-with-lease "<head-remote>" HEAD:"$head"`。
5. push 後は CI が再実行される。bounded polling できるなら短く再確認し、できなければ「push 済み、CI 再実行待ち」と報告する。

flaky / infra 起因が疑われる failure はコードをいじらない。1 回の rerun 提案または実行に留め、再現するならエスカレーションする。

## Review 対応

対象は inline thread だけではない。次を両方見る。

- GraphQL の `reviewThreads` で `isResolved=false` の thread。
- `latestReviews` と top-level `comments` の具体的な requested changes。COMMENTED review にも actionable な依頼が含まれることがある。

古い指摘を蒸し返さない。inline thread では `latest` comment が自分の返信で、その後レビュアーの新規返信が無いなら対応済みとして skip する。summary / top-level でも、自分が当該依頼へ返信済みで、その後の新規反応が無いなら skip する。

実装の流れ:

1. 未対応指摘を正規化して一覧化する。`path`, `line`, top-level body, latest body, diff hunk を読む。
2. 設計判断や仕様確認が必要な指摘は無理に直さず、判断点を具体化してユーザーへ渡す。
3. 修正できる指摘はコードで対処し、テストを実行する。
4. commit して `git push --force-with-lease "<head-remote>" HEAD:"$head"`。
5. push 後に返信する。先に「直した」と返信してから push してはいけない。

inline thread への返信は thread 先頭コメントの `fullDatabaseId` を使う。

```bash
gh api "repos/$owner/$repo/pulls/$num/comments/$top_level_id/replies" -f body="<対応内容と commit>"
```

全体コメントが必要なら repository を明示する。

```bash
gh pr comment -R "$owner/$repo" "$num" --body "<要約>"
```

thread の resolve は基本的にレビュアーに委ねる。確信があり、repo の慣習に合うときだけ行う。

## Bounded Polling

Codex では hidden background monitoring をしない。現在のターンで完了まで進める余地があるときだけ、短い bounded polling を使う。

- push 直後、CI pending、`mergeable=UNKNOWN`: 数分以内に変わる可能性があるため、短く待って再取得してよい。
- green で conflict なし、未対応指摘なし、人間レビュー待ち: 長時間待ちになるため、現状を報告して終了する。
- 同じ CI / conflict / review 指摘が繰り返し失敗する: 無限に push しない。

ユーザーが明示的に「待ち続けて」と言っていても、Codex セッション内で実行できる範囲を超える background 監視は約束しない。必要なら、次回 `pr-watch` を再実行する条件を具体的に伝える。

## Oscillation セーフティ

完全自律 + force-push なので、同じ問題を繰り返すと無駄な履歴改変になる。pass ごとに次を記録する。

- conflict: 衝突ファイルの集合
- CI: 失敗 job の集合
- review: 未対応 thread の `path` と要旨

2 pass 連続で同一集合かつ無進捗なら自動修正を止める。3 pass 連続で同じファイル群に同種の問題が出続ける場合も止める。残った項目、試した修正、次の選択肢を短く報告する。

## やらないこと

- PR 作成。この skill は作成後の監視と対処用。
- 明示指示なしの自動マージ。
- 他者 PR / protected branch への force-push。
- 無印 `--force`。
- refspec なしの push。
- CI を通すためのテスト削除、workflow 緩和、gate bypass。
- 意味的判断が必要な conflict / review 指摘の勝手な解決。
- installed copy (`~/.codex/skills/pr-watch`) の直接編集。source-of-truth は repo の `codex/skills/pr-watch/SKILL.md`。

## 終了報告

最後に 2-3 文で報告する。

- 何を処理したか: rebase / CI fix / review fix / reply / push。
- 最終状態: merged / closed / mergeable + green / CI pending / reviewer wait / unresolved blocker。
- 残課題がある場合は、どの項目に人間判断が必要か。

長い総括は不要。PR URL、push した commit、実行した検証コマンドだけを高信号で添える。
