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
gh pr view --json number,state,isDraft,mergeable,mergeStateStatus,reviewDecision,headRefName,baseRefName,headRepositoryOwner,isCrossRepository,maintainerCanModify,url,title,statusCheckRollup
```

引数で PR 番号 / URL が渡された場合は `gh pr view <番号またはURL> --json …` で対象を固定する。`headRefName` が現在のローカルブランチ名と一致することを確認する（一致しなければ、対象 PR と作業ブランチがずれているのでユーザーに確認）。

主要フィールドの意味:

- `state`: `OPEN` / `MERGED` / `CLOSED`
- `mergeable`: `MERGEABLE` / `CONFLICTING` / `UNKNOWN`（`UNKNOWN` は GitHub が計算中。数十秒後に再取得すれば確定する — この pass では「不明」として扱い、短間隔で再 pass）
- `mergeStateStatus`: `CLEAN` / `DIRTY`（衝突）/ `BEHIND`（ベースが進んでいる）/ `BLOCKED`（必須レビュー・チェック未達）/ `UNSTABLE`（CI が落ち / 進行中だが技術的にはマージ可）/ `HAS_HOOKS` / `DRAFT` / `UNKNOWN`
- `reviewDecision`: `APPROVED` / `CHANGES_REQUESTED` / `REVIEW_REQUIRED` / 空
- `isCrossRepository` / `headRepositoryOwner` / `maintainerCanModify`: PR head が fork にあるか（後述「push 先 remote の解決」で使う）

### push 先 remote の解決（fork 対応・以降の全 push で使う）

C/D/E のどの push もこの解決結果に従う。head ref を `$head`（= `headRefName`）とし、push 先 remote を次で決める:

- `isCrossRepository=false`（head が base リポジトリにある通常ケース）→ 押し先は `origin`。
- `isCrossRepository=true`（head が fork にある）→ `origin` に押すと base 側に別 ref を作るだけで **PR head は変わらない**。`gh pr checkout <num>` で正しい remote と追跡ブランチを設定し、その remote（`gh pr checkout` が作る `<owner>` 由来の remote、または `headRepositoryOwner.login` の fork を指す remote）へ押す。fork に push 権限が無い（`maintainerCanModify=false` かつ自分が head リポジトリの owner でない）なら **force-push せずエスカレーション**。

以降このドキュメントで `<head-remote>` と書いたら、ここで解決した remote を指す。全 push は明示 refspec `<head-remote> HEAD:"$head"` で行う（refspec を省略しない）。

## 1 反復（pass）の手順

各ステップの前に「いま何をするか」を 1 文で宣言してから動く（例:「衝突しているので origin/main へ rebase します」）。長い前置きは不要。

### A. 状態取得

上記 `gh pr view --json …` を 1 回叩いて、この pass の判断材料を揃える。CI の詳細が要るときだけ続けて `gh pr checks --json name,state,bucket,link,workflow` を叩く（`bucket` が `pass`/`fail`/`pending`/`skipping`/`cancel` を返す）。

### B. 終了判定（最初に行う）

対処に入る前に、まず「もう終わっていないか」を見る。無駄な rebase / 修正を避けるため、この判定を pass の先頭に置く:

- `state` が `MERGED` または `CLOSED` → **完了**。終了報告して loop を抜ける。
- `state=OPEN` かつ `reviewDecision=APPROVED` かつ `mergeable=MERGEABLE`（`mergeStateStatus=CLEAN`）かつ CI が全 `pass`/`skipping` かつ未解決レビュースレッド 0 → **やることなし＝実質完了**。「マージ可能・承認済み・グリーンで未解決指摘なし」と報告して loop を抜ける（PR の自動マージはしない。後述「やらないこと」）。
- それ以外 → C 以降で対処対象を洗う。

### C. マージコンフリクト対応（rebase + 自動解決）

`mergeable=CONFLICTING` または `mergeStateStatus` が `DIRTY` / `BEHIND` のとき:

1. 作業ツリーが dirty なら、まず未コミット変更を commit するか stash するかを判断（勝手に捨てない）。
2. ベースを取得して rebase:

   ```bash
   git fetch origin "$base"            # base は baseRefName
   git rebase "origin/$base"
   ```

3. 衝突が出たら、**確信が持てる箇所だけ自動解決**する。確信の基準は「両側の意図が明確で、機械的にどちらか / 両方を残せば正しいと言い切れる」こと（例: import 行の併合、片側が単なる行追加、フォーマット差）。解決したら `git add` → `git rebase --continue`。
4. **意味的に判断が要る衝突は自動解決しない**: 同じ関数を両側で別ロジックに書き換えている、削除 vs 変更がぶつかる、テストの期待値が両側で食い違う等。`git rebase --abort` で安全な状態へ戻し、後述の oscillation セーフティと同じ要領でユーザーに状況を渡してエスカレーションする（どの hunk が・なぜ自動解決できないかを具体的に）。
5. rebase 完了後、push。**push 先は「push 先 remote の解決」で決めた `<head-remote>` の head ref に明示ピン留めする**（`$head` は `headRefName`）:

   ```bash
   git push --force-with-lease "<head-remote>" HEAD:"$head"
   ```

   - 明示 refspec `<head-remote> HEAD:"$head"` にするのは、ローカルの `push.default`（`matching` だと全一致ブランチを push する）や upstream のズレに依存させず、**この PR の head ref だけ**を更新するため。refspec 無しの素の `git push --force-with-lease` は config 次第で意図しないブランチを巻き込みうるので、自律実行では使わない。fork PR では `<head-remote>` が `origin` ではないことに注意（解決手順に従う）。
   - `--force-with-lease` は、自分が最後に見た時点から remote の head が他者に更新されていたら**上書きせず失敗させる**ため。lease 失敗が出たら「他者が同じブランチに push した」とみなし、`git fetch` して取り込み、状況をユーザーに共有してから再評価する（無印 `--force` は使わない）。

`BEHIND` だが衝突なし（ベースが進んだだけ）の場合も、ブランチ保護で「up-to-date 必須」なら同様に rebase + push でベースに追従させる。衝突が無ければ自動解決の判断は不要。

### D. CI 失敗対応

`gh pr checks --json name,state,bucket,link,workflow` で `bucket=fail` のチェックを特定する:

1. 失敗したワークフロー実行のログを精読する。run id は失敗チェックの `link` から辿るか、`gh run list --branch "$head" --json databaseId,conclusion,workflowName` で取得し、**失敗ジョブのみ**を読む:

   ```bash
   gh run view <run-id> --log-failed
   ```

   ログ全文ではなく失敗部分だけを取るのは、出力肥大とトークン浪費を避けるため。
2. 原因を特定して修正する（lint / 型 / テスト / ビルド）。修正前に「CI の何が・なぜ落ちたか」と「何を直すか」を 1〜2 文で宣言。
3. 修正を commit → `git push --force-with-lease "<head-remote>" HEAD:"$head"`（新規コミットを足すだけだが、`push.default` 依存を避けるため push 先は常に「push 先 remote の解決」で決めた `<head-remote>` の head ref に明示ピンする。rebase していないので fast-forward になり lease は通る）。push 後、CI は再実行されるので、この pass では「修正を push した。CI 再実行を待つ」とし、次 pass は短間隔で再確認する。
4. **flaky / インフラ起因が疑われる失敗**（タイムアウト、外部サービス 5xx、無関係なネットワークエラー）はコード修正で直らない。1 回は再実行を促し（`gh run rerun <run-id> --failed` の提案）、それでも再現するならユーザーにエスカレーション。コードに無い原因をコードで延々いじらない。

### E. レビューコメント対応

未対応のレビュー指摘を集めて、コードで対処する:

1. 取得:
   - サマリレビューと PR 全体コメント: `gh pr view --json reviews,comments,latestReviews`
   - **未解決のインラインスレッド**（どの指摘がまだ open か）は GraphQL で `isResolved` を見る:

     ```bash
     gh api graphql -f query='
       query($owner:String!,$repo:String!,$num:Int!){
         repository(owner:$owner,name:$repo){
           pullRequest(number:$num){
             reviewThreads(first:100){
               nodes{ isResolved isOutdated path
                 comments(first:20){ nodes{ databaseId author{login} body } } } } } } }' \
       -F owner="$owner" -F repo="$repo" -F num="$num"
     ```

2. `isResolved=false` のスレッドだけを対象に、指摘内容をコードで対処する。設計判断・仕様確認を伴う指摘（「この方針で良いか」「ここは別実装にすべきでは」等、機械的に直せないもの）は無理に実装せず、その旨を整理してユーザーにエスカレーションする。
3. 修正を commit → `git push --force-with-lease "<head-remote>" HEAD:"$head"`（C/D と同じく push 先は解決済み `<head-remote>` の head ref に明示ピン。素の `git push` は使わない）。
4. **fix を push してから**返信する（順序が逆だと「直した」と言ったのに反映されていない状態を作る）。返信は対応コミットを参照して簡潔に:
   - 個別インラインスレッドへの返信: `gh api repos/$owner/$repo/pulls/$num/comments/<comment_databaseId>/replies -f body="<対応内容と commit>"`
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
- **操作対象は自分の head トピックブランチのみ**。`headRefName` が現在ブランチと一致することを確認してから触る。**他者が作成した PR、`main` / `master` / `release/*` 等の保護ブランチには force-push しない**。
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
