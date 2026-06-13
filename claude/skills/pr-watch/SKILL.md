---
name: pr-watch
description: PR を作成したあと、マージコンフリクト・レビューコメント・CI 失敗を監視して自動で対処し続けるループ skill。現在ブランチの PR を gh で特定し、ベース更新による衝突は rebase + 自動解決して force-with-lease push、failing CI はログを読んで修正、レビューの requested changes はコードで対応して返信する — マージ可能 / approve / グリーンになるまで（または解決困難な箇所が出るまで）完全自律で回す。ユーザーが「PR 作ったあと見張って」「コンフリクト直して」「レビュー対応して」「PR を監視 / babysit して」「CI 直して」「PR がマージできる状態まで持っていって」等と言ったとき、PR を作成した直後の文脈、または /pr-watch・/loop /pr-watch が呼ばれたときに必ず使う。「dashboard」「visualization」のような明示語が無くても、PR 作成直後に「あとよろしく」的な依頼が来たらこの skill を呼ぶこと — 衝突・レビュー・CI は時間差で来るので、単発対応より監視ループの方が取りこぼさない。
---

# pr-watch

PR 作成後に発生する 3 種の差し込み — **マージコンフリクト / レビューコメント / CI 失敗** — を、監視ループで完全自律に処理し続ける skill。

## なぜこれが要るか

PR を出した瞬間が終わりではない。ベースブランチが進めば衝突し、レビュアーは数時間後に requested changes を付け、CI は数分かけて落ちる。これらは**時間差で・繰り返し**やってくるので、人間が単発で対応すると「気づいたら conflict のまま放置」「レビュー返したつもりが CI 落ちてた」が起きる。

この skill は、現在ブランチの PR 1 本を対象に「状態を見る → 対処できることを全部やる → push → 少し待つ → また見る」を、PR がマージ可能・approve・グリーンに揃うまで回す**オーケストレーター**。衝突解決・コード修正・返信は自分でやるが、意味的に判断が要る箇所（壊すと困る衝突、設計判断を伴うレビュー指摘）は無理に処理せず人間にエスカレーションする。レビューの良し悪しの判断そのものは `code-review` 系 skill に任せ、ここは「検知して対処してまた見る」の制御に徹する。

## 適用範囲と前提

- **対象**: 現在 git ブランチに紐づく PR 1 本（`gh pr view` で自動特定）。引数で PR 番号 / URL を渡せばそれを対象にする。
- **git リポジトリ外なら早期終了**: `git rev-parse --is-inside-work-tree` が false なら、「ここは git リポジトリではないので pr-watch は使えない」と伝えて終了。
- **gh 未認証なら早期終了**: `gh auth status` が通らなければ `gh auth login` を案内して終了。
- **PR が無いなら何もしない**: `gh pr view` が PR 不在を返したら、「このブランチにはまだ PR が無い。先に PR を作ってから呼んで」と伝えて終了。この skill は PR を**作らない**（作成は別フロー。作成直後にこの skill を呼ぶ運用）。
- **対象は自分の head トピックブランチのみ**: 後述の安全ガードレールの通り、他者の PR や保護ブランチは触らない。

## 全体フロー

```
[ユーザー: /pr-watch or 「PR 見張って」or PR 作成直後]
        │
        ▼
  対象 PR を特定 (gh pr view --json …)
        │
        ▼
 ┌───── 1 pass ────────────────────────────────┐
 │  A. 状態取得                                  │
 │  B. 終了判定 (merged/closed/揃った → 完了)     │
 │  C. コンフリクト対応 (rebase + 自動解決 + push)│
 │  D. CI 失敗対応 (log 精読 → 修正 → push)       │
 │  E. レビューコメント対応 (修正 → push → 返信)  │
 └──────────────┬──────────────────────────────┘
                │ まだ揃っていない
                ▼
   ScheduleWakeup で間隔を選んで再 pass
   (CI 実行中=短間隔 / アイドル=長間隔)
                │
                ▼
   揃った / ユーザー停止 / oscillation 検知 → 終了報告
```

`/loop /pr-watch`（dynamic / self-paced）で回すのが基本。`/pr-watch` 単発でも 1 pass を回し、まだ揃っていなければ継続監視を案内する（後述「ループ制御」）。

## 対象 PR の特定

```bash
gh pr view "$pr" --json number,state,isDraft,mergeable,mergeStateStatus,reviewDecision,headRefName,baseRefName,author,headRepository,headRepositoryOwner,isCrossRepository,maintainerCanModify,url,title,statusCheckRollup
```

引数で PR 番号 / URL が渡されたらそれを `$pr` とし、無引数（現在ブランチの PR を対象）なら `$pr` は空。**この pass の全 gh 呼び出し（`gh pr view "$pr"`・`gh pr checks "$pr"`・GraphQL の番号 `$num`）に必ず `$pr`/`$num` を渡す**。無引数の `gh pr checks` / `gh pr view` は「現在ブランチの PR」を読むので、別ブランチから番号/URL 指定したのに無引数で叩くと**別の PR を監視してしまう**。

`headRefName` が現在のローカルブランチ名と一致することを確認する（不一致ならユーザーに確認）。

主要フィールドの意味:

- `state`: `OPEN` / `MERGED` / `CLOSED`
- `mergeable`: `MERGEABLE` / `CONFLICTING` / `UNKNOWN`（`UNKNOWN` は GitHub が計算中。数十秒後に再取得すれば確定する — この pass では「不明」として扱い、短間隔で再 pass）
- `mergeStateStatus`: `CLEAN` / `DIRTY`（衝突）/ `BEHIND`（ベースが進んでいる）/ `BLOCKED`（必須レビュー・チェック未達）/ `UNSTABLE`（CI が落ち / 進行中だが技術的にはマージ可）/ `HAS_HOOKS` / `DRAFT` / `UNKNOWN`
- `reviewDecision`: `APPROVED` / `CHANGES_REQUESTED` / `REVIEW_REQUIRED` / 空
- `author` / `headRepository` / `headRepositoryOwner` / `isCrossRepository` / `maintainerCanModify`: PR の作者と head の所在（後述「push 先 remote の解決と force-push 認可」で使う）

### push 先 remote の解決と force-push 認可（以降の全 push で使う）

C/D/E のどの push もこの解決に従う。head ref を `$head`（= `headRefName`）とする。

**force-push 認可**: 次を**両方**満たすときだけ force-push 可。どちらか欠けたら force-push せずエスカレーション:

```bash
me=$(gh api user -q .login)
```

1. `headRepositoryOwner.login == $me`（head リポジトリが自分のもの）。
2. PR の `author.login == $me`（自分が開いた PR）。

両方要求するのは、`headRepositoryOwner.login` は **リポジトリの owner であって PR author とは限らない**ため。自分の repo に collaborator がブランチを作って PR を開いた場合、head owner は自分でも author は他人なので、author を見ないと他人のブランチを rewrite してしまう。`maintainerCanModify=true`（fork の「メンテナの編集を許可」）も認可根拠にしない（コミット追記の許可であって履歴 rewrite の許可ではない）。

**push 先 remote `<head-remote>` の解決**: `isCrossRepository` に関わらず、PR の `headRepository`（owner/name・URL）に**実際に一致する local remote**を `git remote -v` から探して使う。`origin` 決め打ちにしないのは、fork レイアウト（`origin`=自分の fork、`upstream`=base repo）では `isCrossRepository=false` でも PR head が base repo 側に居て、`origin` に押すと別 ref を作るだけで PR head が変わらないため。一致する remote が無い（自分の）fork PR は `gh pr checkout <num>` で remote/追跡ブランチを用意してそれを使う。解決できなければエスカレーション。

**ローカル HEAD が PR head を含むか検証**（rebase / force-push の前に必ず）:

```bash
git fetch "<head-remote>" "$head"
git merge-base --is-ancestor FETCH_HEAD HEAD   # PR head が HEAD の祖先か
```

これが false（ローカルが PR head より後ろ / 乖離）なら、remote-tracking 的には `--force-with-lease` が通ってしまい **PR ブランチにしか無いコミットを取りこぼす**恐れがある。その場合は force-push せず、取り込み（`git pull --rebase` 等）かエスカレーションで HEAD を PR head の先端に揃えてから進む。

以降このドキュメントで `<head-remote>` と書いたら、ここで解決した remote を指す。全 push は明示 refspec `<head-remote> HEAD:"$head"` で行う（refspec を省略しない）。認可が取れない / remote 解決できない / HEAD 検証に失敗したケースでは、ここで止めてエスカレーションし、以降の C/D/E の push は行わない。

## 1 反復（pass）の手順

各ステップの前に「いま何をするか」を 1 文で宣言してから動く（例:「衝突しているので origin/main へ rebase します」）。長い前置きは不要。

### A. 状態取得

この pass の判断材料を、**B の終了判定に必要なものまで含めて先に揃える**（B が「CI 全 pass」「未解決スレッド 0」を見るのに、それらを A で取っていないと取りこぼす）。3 つを取る:

1. `gh pr view --json …`（PR 状態）。
2. CI: `gh pr checks "$pr" --json name,state,bucket,link,workflow`（`$pr` を必ず渡す。`bucket` が `pass`/`fail`/`pending`/`skipping`/`cancel`）。**`gh pr checks` は pending チェックがあると exit 8、失敗があると非 0 を返す**ので、exit code を成否判定に使わず、`gh pr checks "$pr" --json … || true` のように**必ず JSON を取り切ってから `bucket` で判断**する（exit 8 をコマンド失敗として扱うと、push→pending の窓でループが止まる）。
3. 未解決レビュースレッド: E-1 の `reviewThreads` GraphQL を**この時点で**叩き、`isResolved=false` の件数（と中身）を持っておく。`reviewDecision` は COMMENTED レビューでは変わらないので、スレッドを実際に数えないと未解決指摘を見落とす。

### B. 終了判定（最初に行う）

対処に入る前に、まず「もう終わっていないか」を見る。無駄な rebase / 修正を避けるため、この判定を pass の先頭に置く:

- `state` が `MERGED` または `CLOSED` → **完了**。終了報告して loop を抜ける。
- `state=OPEN` かつ `reviewDecision=APPROVED` かつ `mergeable=MERGEABLE`（`mergeStateStatus=CLEAN`）かつ CI が全 `pass`/`skipping` かつ **A-3 で数えた未解決レビュースレッドが 0** → **やることなし＝実質完了**。「マージ可能・承認済み・グリーンで未解決指摘なし」と報告して loop を抜ける（PR の自動マージはしない。後述「やらないこと」）。未解決スレッドの 0 判定は必ず A-3 の `reviewThreads` 実取得に基づくこと（`reviewDecision` だけで「指摘なし」と即断しない — COMMENTED スレッドを取りこぼす）。
- それ以外 → C 以降で対処対象を洗う。

### C. マージコンフリクト対応（rebase + 自動解決）

`mergeable=CONFLICTING` または `mergeStateStatus` が `DIRTY` / `BEHIND` のとき:

1. 作業ツリーが dirty なら、まず未コミット変更を commit するか stash するかを判断（勝手に捨てない）。
2. ベースを取得して rebase。**rebase 先は PR の base リポジトリの `$base`（= `baseRefName`）**であって、head（fork）側の同名ブランチではない。fork PR では `origin` が fork を指し base リポジトリは別 remote（`upstream` 等）のことがあるので、base リポジトリを指す remote を `<base-remote>` として解決してから当てる（非 fork なら `origin`。判別に迷うなら base repo の URL から直接 fetch する）:

   ```bash
   git fetch "<base-remote>" "$base"   # 例: git fetch origin main / git fetch upstream main
   git rebase FETCH_HEAD               # いま fetch したベースちょうどに当てる（remote 名のズレに影響されない）
   ```

   `origin/$base` を直書きしないのは、fork 運用だと fork 側の古い・別物のベースに rebase してしまい、real base に対してまだ behind / conflicting な head を push しかねないため。

3. 衝突が出たら、**確信が持てる箇所だけ自動解決**する。確信の基準は「両側の意図が明確で、機械的にどちらか / 両方を残せば正しいと言い切れる」こと（例: import 行の併合、片側が単なる行追加、フォーマット差）。解決したら `git add` → `git rebase --continue`。
4. **意味的に判断が要る衝突は自動解決しない**: 同じ関数を両側で別ロジックに書き換えている、削除 vs 変更がぶつかる、テストの期待値が両側で食い違う等。`git rebase --abort` で安全な状態へ戻し、後述の oscillation セーフティと同じ要領でユーザーに状況を渡してエスカレーションする（どの hunk が・なぜ自動解決できないかを具体的に）。
5. rebase 完了後、push。**push 先は「push 先 remote の解決」で決めた `<head-remote>` の head ref に明示ピン留めする**（`$head` は `headRefName`）:

   ```bash
   git push --force-with-lease "<head-remote>" HEAD:"$head"
   ```

   - 明示 refspec `<head-remote> HEAD:"$head"` にするのは、ローカルの `push.default`（`matching` だと全一致ブランチを push する）や upstream のズレに依存させず、**この PR の head ref だけ**を更新するため。refspec 無しの素の `git push --force-with-lease` は config 次第で意図しないブランチを巻き込みうるので、自律実行では使わない。fork PR では `<head-remote>` が `origin` ではないことに注意（解決手順に従う）。
   - `--force-with-lease` は、自分が最後に見た時点から remote の head が他者に更新されていたら**上書きせず失敗させる**ため。lease 失敗が出たら「他者が同じブランチに push した」とみなし、`git fetch` して取り込み、状況をユーザーに共有してから再評価する（無印 `--force` は使わない）。
6. **rebase + push したら、この pass はここで終了する**。A で取った CI 結果・レビュースレッドは push 前の旧 head SHA に紐づくので、同じ pass のまま D/E に進むと**古いログや outdated なコメントを基に誤修正する**。短間隔の `ScheduleWakeup`（~270s）を入れて次 pass で新しい head SHA の状態を取り直し、その上で D/E を評価する。

`BEHIND` だが衝突なし（ベースが進んだだけ）の場合も、ブランチ保護で「up-to-date 必須」なら同様に rebase + push でベースに追従させる。衝突が無ければ自動解決の判断は不要。いずれも push したらこの pass は終了し、再取得してから先へ進む。

### D. CI 失敗対応

`gh pr checks "$pr" --json name,state,bucket,link,workflow` で **`bucket` が `fail` または `cancel`** のチェックを特定する（`cancel`=キャンセルされた必須チェックを無視すると、B は「全 pass でない」ので完了できず、D が拾わないと永遠に idle/long-poll になる）。`cancel` は原因修正の対象ではなく、まず `gh run rerun <run-id>`（外部 CI なら再実行をユーザーに促す）で回し直し、繰り返すならエスカレーション。`fail` は以下で原因を直す:

1. 失敗チェックが **GitHub Actions の run か外部 CI かをまず判別する**。`gh pr checks --json` の各チェックの `link` が GitHub Actions の run URL（`…/actions/runs/<id>…`）か、`workflow` が埋まっているものだけが Actions。Actions なら失敗 run のログを **失敗ジョブのみ**精読する（run id は `link` から、または `gh run list --branch "$head" --json databaseId,conclusion,workflowName` で取得）:

   ```bash
   gh run view <run-id> --log-failed
   ```

   ログ全文ではなく失敗部分だけを取るのは、出力肥大とトークン浪費を避けるため。

   **外部 CI（Buildkite / CircleCI / Jenkins 等、`link` が Actions の run でないチェック）は `gh run` で辿れない**。`gh run view` を当てに行かず、チェックの `link` URL を開いて原因を読むか、ログに自動アクセスできなければ「外部 CI `<name>` が失敗。`<link>` を確認してほしい」とユーザーにエスカレーションする。
2. 原因を特定して修正する（lint / 型 / テスト / ビルド）。修正前に「CI の何が・なぜ落ちたか」と「何を直すか」を 1〜2 文で宣言。
3. 修正を commit → `git push --force-with-lease "<head-remote>" HEAD:"$head"`（新規コミットを足すだけだが、`push.default` 依存を避けるため push 先は常に「push 先 remote の解決」で決めた `<head-remote>` の head ref に明示ピンする。rebase していないので fast-forward になり lease は通る）。push 後、CI は再実行されるので、この pass では「修正を push した。CI 再実行を待つ」とし、次 pass は短間隔で再確認する。
4. **flaky / インフラ起因が疑われる失敗**（タイムアウト、外部サービス 5xx、無関係なネットワークエラー）はコード修正で直らない。1 回は再実行を促し（`gh run rerun <run-id> --failed` の提案）、それでも再現するならユーザーにエスカレーション。コードに無い原因をコードで延々いじらない。

### E. レビューコメント対応

未対応のレビュー指摘を集めて、コードで対処する。対象は**インラインスレッドだけではない** — 次の 2 系統を両方拾う:

1. 取得:
   - **サマリ／トップレベルの requested changes**: `gh pr view "$pr" --json latestReviews,comments,reviewDecision`。レビュアーが inline ではなくレビュー本文やトップレベルコメントだけで変更を求めると、GitHub は **未解決 `reviewThread` を作らない**。`reviewDecision=CHANGES_REQUESTED` なのにインラインスレッドが 0、というケースがあるので、`latestReviews` の `state=CHANGES_REQUESTED` の本文や、対応を要求する PR コメント本文も**対処すべきレビュー作業として扱う**（これを見ないと「直す対象が無い」と誤判断して永遠に idle になる）。**判断には `latestReviews`（レビュアーごとの現在状態。承認・dismiss 済みは反映される）を使い、歴史的な `reviews` 全件は使わない** — 後で approve / dismiss された古い `CHANGES_REQUESTED` を蒸し返して対処対象にしてしまうため。
   - **未解決のインラインスレッド**（どの指摘がまだ open か）は GraphQL で `isResolved` を見る:

     ```bash
     gh api graphql --paginate -f query='
       query($owner:String!,$repo:String!,$num:Int!,$endCursor:String){
         repository(owner:$owner,name:$repo){
           pullRequest(number:$num){
             reviewThreads(first:100, after:$endCursor){
               pageInfo{ hasNextPage endCursor }
               nodes{ isResolved isOutdated path
                 comments(first:20){ nodes{ databaseId author{login} body } } } } } } }' \
       -F owner="$owner" -F repo="$repo" -F num="$num"
     ```

     **必ず全ページを辿る**（`--paginate` + `pageInfo.endCursor`）。`first:100` の 1 ページだけ見ると、スレッドが 100 を超える PR で後ろのページの未解決スレッドを取りこぼし、B が「未解決 0」と誤判定して完了してしまう。

     各スレッドについて **先頭（top-level）コメントの `databaseId`**（= `comments.nodes[0].databaseId`）を控えておく。返信エンドポイント（E-4）はこの top-level ID しか受け付けないため。

2. `isResolved=false` のスレッド **および 1 で拾ったサマリ／トップレベルの requested-change 本文**を対象に、指摘内容をコードで対処する。インラインスレッドが 0 でも `reviewDecision=CHANGES_REQUESTED` が残っているなら、レビュー本文側に対処対象があるとみなして拾う。

   **ただし「自分（watcher）が既に対応・返信済み」のスレッドは再処理しない**。resolve はレビュアーに委ねる方針（E-5）なので、対応済みスレッドも次 pass まで `isResolved=false` のまま見える。スレッドの**最新コメントが自分の返信**で、それ以降にレビュアーの新規コメントが無いなら「対応済み・レビュアー待ち」とみなして skip する（スレッドごとに「自分が最後に返信した commit / 時刻」を記憶し、レビュアーが新たに反応＝新規コメント追加 or resolve するまで触らない）。これをやらないと同じ修正・返信を毎 pass 重複させ、oscillation に陥る。

   設計判断・仕様確認を伴う指摘（「この方針で良いか」「ここは別実装にすべきでは」等、機械的に直せないもの）は無理に実装せず、その旨を整理してユーザーにエスカレーションする。
3. 修正を commit → `git push --force-with-lease "<head-remote>" HEAD:"$head"`（C/D と同じく push 先は解決済み `<head-remote>` の head ref に明示ピン。素の `git push` は使わない）。
4. **fix を push してから**返信する（順序が逆だと「直した」と言ったのに反映されていない状態を作る）。返信は対応コミットを参照して簡潔に:
   - 個別インラインスレッドへの返信は **スレッド先頭コメントの `databaseId`**（E-1 で控えた `comments.nodes[0].databaseId`）に対して行う。返信コメントの ID を渡すと reply エンドポイントは 404 を返すので、必ず top-level ID を使う: `gh api repos/$owner/$repo/pulls/$num/comments/<top_level_databaseId>/replies -f body="<対応内容と commit>"`
   - 全体への一言: `gh pr comment "$num" --body "<要約>"`
5. **スレッドの resolve は確信があるときだけ**。基本はレビュアーに委ねる（自分で resolve すると、レビュアーが再確認する前に閉じてしまう）。

C/D/E のどれも該当しなければ、この pass は「対処対象なし」。終了判定 B に戻って完了か継続待ちかを決める。

## ループ制御とポーリング間隔

この skill は**外部イベント待ちの長期ループ**。`/loop /pr-watch`（interval なしの dynamic mode）で回し、`ScheduleWakeup` で自分のペースを決めるのが基本形。

間隔の選び方（Anthropic prompt cache は約 5 分 TTL。これを跨ぐと次回はキャッシュミスになる）:

- **CI が実行中 / push 直後で再実行待ち / `mergeable=UNKNOWN`（GitHub 計算中）** → 短間隔 `~270s`。数分で変わる状態を追うので、キャッシュ窓に収める。
- **対処対象が無く、人間レビュー待ちでアイドル** → 長間隔 `~1200–1800s`（20–30 分）。すぐ変わらないものを 5 分ごとに見てもキャッシュを焼くだけ。
- **300s ちょうどは選ばない**（キャッシュミスを払うのに元が取れない最悪値）。短くするなら 270s、長くするなら 1200s 以上。

1 pass で actionable が無いときの間隔は「**いま何を待っているか**」で決める。間隔ポリシーと矛盾させないこと:

- **CI が実行中 / push 直後で再実行待ち / `mergeable=UNKNOWN`** → まだ状態が動くので**短間隔（~270s）**。actionable が無くても長間隔に送らない（落ちたらすぐ拾うため）。
- **CI も green・衝突なし・新規レビューも無く、人間レビュー待ちで完全アイドル** → **長間隔（~1200–1800s）**。

「今は対処対象なし（待っているもの: CI 実行中 / 人間レビュー など）。約 N 分後に再確認する」と 1 文報告してから、上記で選んだ間隔の `ScheduleWakeup` を入れる。完了（終了判定 B）したら `ScheduleWakeup` を入れずにループを終える。

`/loop` を使わず `/pr-watch` 単発で呼ばれた場合は、1 pass を回して現状を報告し、まだ揃っていなければ「継続監視するなら `/loop /pr-watch` で回してください」と案内する（単発呼び出しで延々と待ち続けない）。

## Oscillation セーフティ（暴走・無限 force-push 防止）

完全自律 + force-push なので、同じ問題を直しきれずに繰り返すと無限ループ・無駄な履歴改変になる。これを防ぐため、各 pass で**直前 pass の対処対象を正規化リストで記憶**する:

- コンフリクト: 衝突ファイル群 `<path>` の集合
- CI: 失敗ジョブ `<workflow>/<job>` の集合
- レビュー: 未解決スレッド `<path>:<指摘要旨1行>` の集合

そして:

- **2 pass 連続で同一集合かつ無進捗**（同じファイルが衝突し続ける / 同じ CI が落ち続ける / 同じ指摘が未解決のまま）なら、自動修正に入らず AskUserQuestion で 3 択を出す:

  > pr-watch が 2 回連続で同じ問題を解消できていません: [概要]。
  > (a) この項目は無視して残りの監視を続ける
  > (b) 一旦停止する（自分で手を入れたい）
  > (c) 別アプローチでもう一度だけ試す

- **完全一致でなくても、3 pass 連続で同じファイル群に同種の問題が出続ける**なら同様に状況共有して判断を仰ぐ（文言だけ変わって本質が同じケースの保険）。

「上限なしで揃うまで回す」運用でも、この振動検知だけは必ず効かせる。

## 安全ガードレール

- **全 push は解決済み `<head-remote>` の head ref に明示ピンする**（`git push [--force-with-lease] <head-remote> HEAD:<headRefName>`）。C の rebase 後・D の CI 修正・E のレビュー修正のいずれも対象。refspec 無しの素の push（`push.default` 依存で別ブランチを巻き込みうる）や無印 `--force` は使わない。fork PR では `<head-remote>` が `origin` でない／push 権限が無いことがあるので、解決手順で確認できなければ force-push せずエスカレーション。lease 失敗 = 他者更新ありとみなし、fetch して再評価し、状況をユーザーへ。
- **操作対象は自分の head トピックブランチのみ**。`headRefName` が現在ブランチと一致することを確認してから触る。**force-push の認可は「head リポジトリ owner == 今の認証ユーザー（`gh api user -q .login`）」かつ「PR `author` == 同ユーザー」の両方で判断する**（「push 先 remote の解決と force-push 認可」と同一基準）。owner だけで判断すると、自分が owner の repo に collaborator が作ったブランチを rewrite してしまう。`maintainerCanModify=true`（fork PR で「メンテナの編集を許可」）も認可根拠にしない — 履歴 rewrite の許可ではないので、他者 fork に force-push するとガードレール違反になる。**他者が作成した PR、`main` / `master` / `release/*` 等の保護ブランチには force-push しない**。
- **衝突を片側採用で機械的に潰さない**。確信が持てる hunk だけ自動解決し、意味的判断が要る箇所は abort してエスカレーション（C-4）。
- **リポジトリのレビューゲートを尊重する**。`gh pr create` を `.claude/hooks/` 等でゲートしているリポジトリ（この repo の `pre-pr-review-gate.sh` 等）では、この skill は PR を作らないのでゲート対象外だが、push する fix にも品質基準を勝手に下げない。エスケープハッチ（`FANOUT_SKIP_PR_REVIEW` 等）を無断で使わない。
- **CI を直すために CI 設定（ワークフロー yaml）を緩めて通す、テストを消して通す等の「通すための改竄」をしない**。原因を直すのが目的。設定変更が本当に必要なら理由を添えてユーザーに確認。

## 終了報告

ループ終了時（完了 / ユーザー停止 / oscillation 検知のいずれか）に 2〜3 文で:

- 何 pass 回したか
- 衝突 / CI / レビューを各何件処理したか（rebase 回数、push 回数、返信したスレッド数）
- 最終状態（`MERGED` / approve+green で揃った / ユーザー判断で停止 / oscillation で残った項目）
- 残課題があれば箇条書き（自動解決できなかった衝突、設計判断が要るレビュー指摘、flaky CI など）

長い総括は不要。ユーザーは PR を見れば中身は分かる。

## やらないこと

- **PR の作成**: この skill は作成後の監視・対処担当。作成は別フロー。
- **PR の自動マージ**: 「揃った」状態に持っていくところまで。明示的に「揃ったらマージして」と指示されない限り `gh pr merge` はしない（マージは不可逆で、タイミング・方式の判断が要る）。
- **他者 PR・保護ブランチへの force-push**。
- **レビュー指摘の良し悪し判定そのもの**: レビューの本質判断は `code-review` 系 skill の領分。ここは検知と対処と返信の制御。
- **「通すための」CI 改竄 / テスト削除 / ゲート回避**。
- **メモリへの新規エントリ追加**: この skill 自体がルールの保管庫。

## トラブルシューティング

- **`gh` 未認証**: `gh auth status` 失敗 → `gh auth login` を案内して終了。
- **`mergeable=UNKNOWN` が続く**: GitHub がマージ可否を計算中。短間隔（~270s）で再 pass すれば確定する。確定前に rebase を急がない。
- **rebase が複雑で自動解決不能**: `git rebase --abort` で安全に戻し、どの hunk が・なぜ駄目かを添えてユーザーへ（C-4）。
- **`--force-with-lease` が reject**: 他者が同ブランチに push 済み。`git fetch` で取り込み、状況をユーザーに共有してから再評価。勝手に `--force` で踏み潰さない。
- **CI ログが巨大**: `gh run view <run-id> --log-failed` で失敗ジョブのみ取得。全文は読まない。
- **CI が flaky / インフラ起因**: コード修正で直らない。1 回 `gh run rerun <run-id> --failed` を促し、再現するならエスカレーション（D-4）。
- **対象 PR がドラフト（`isDraft=true`）**: レビュー / マージ前提が揃わない。衝突・CI の追従はしてよいが、「ドラフトのままなので approve/マージ判定はスキップ」と明示する。
