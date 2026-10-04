# ダッシュボードの PR mutation 不変条件

`fanout dashboard --web` が持つ 2 本の mutation endpoint、`POST /api/pr/merge` と
`POST /api/pr/delete-branch` の不変条件カタログ。実装は `internal/ui/dashboard/merge.go`
/ `deletebranch.go` と `internal/app/prmerge`(class H)、GitHub 呼び出しは
`internal/infra/ghissue`(class M。review class は `docs/architecture.ja.md` の
パッケージ表が正典で、本書は class を変えない)。
セキュリティ面の要約と読み取り endpoint の GET-only 規約は `docs/architecture.ja.md` の
「人間必見の不変条件カタログ」にある。本書は endpoint 単位の完全版で、不変条件を変える
PR は本書を同じ PR で更新する。

## 2 本の endpoint と分割の理由

- mutation は 2 本だけで、どちらも GitHub の PR 1 件に閉じる。`POST /api/pr/merge` は
  その PR の merge 状態だけを変え、`POST /api/pr/delete-branch` はマージ済み PR の
  remote head ref だけを消す。
- 分けてあるのは GitHub の UI と同じ理由。branch 削除はマージの後に現れるボタンで、
  ref 削除は冪等なので、merge 側の「曖昧な mutation を二度撃たない」機構が一切要らない。
  分割が両者を単純に保つ。
- どちらもそれ以外を変えない。ローカル作業ツリー、ローカル git ref、worktree、
  `.fanout/state.json`、ペイン入力には触れず、`gh` に `--admin` / `--auto` /
  `--delete-branch` を渡さない。`--delete-branch` はローカル branch も消しにいき、
  linked worktree で checkout 済みの子 branch では失敗する。
- mutation endpoint の追加、既存 2 本の作用範囲の拡大、入口 gate の緩和は人間レビュー
  対象。

## リクエストの照合

- クライアントは描画した PR 番号、head SHA、base branch を名指しする。GitHub は head を
  動かさずに PR を retarget できるので、SHA だけでは merge の着地先が固定できない。
- snapshot は最大 1 poll 古いので、head と base は merge 直前に GitHub から再読する。
- サーバは、その PR が指定された snapshot 行にまだ載っていることを要求し、SHA を
  `--match-head-commit` として渡す。描画とクリックの間に動いた PR は GitHub が拒否し、
  盲目的にマージされない。
- merged / closed / draft / CONFLICTING は 409 で拒否する。GitHub が既に merge を受け付け
  ている PR(誰かが armed した auto-merge、または merge queue 投入済み)も 409。二度目の
  要求で早くマージされることはない。
- レビュー承認と CI は意図的に gate にしない。branch protection の強制は GitHub の役目で、
  二重実装すると保護ルールの無い repository でボタンが永久に死ぬ。
- live read は `gh pr view --json` ではなく GraphQL を使う。merge queue は GraphQL でしか
  見えず、「GitHub が既にこの merge を受け付けている」はフェンスが見なければならない
  状態そのもの。

## 入口 middleware と token

- POST は `postOnly` → `sameOriginOnly` → `requireToken` の順に通す。`sameOriginOnly` は
  `Host` の完全一致で DNS rebinding を塞ぎ、`Origin` は存在時に自 origin と完全一致を
  要求し、`Content-Type` が JSON でなければ 415 を返す。
- `--no-token` では merge を拒否する。loopback ポートは同一マシンの全プロセスから届く
  ので、空の token 検査の後ろに mutation を置くより経路を閉じる。
- ダッシュボード URL は閲覧権限ではなく merge 実行権限を運ぶ。そのつもりで扱う。

## 行が PR を所有していることの確認

- どの行も、PR の base repository がこの repository であることを merge の前提にする。
  `Fixes owner/repo#N` は repository をまたいで issue を閉じるので、行の PR 一覧は PR が
  ここにある証拠にならない。
- issue-less の行(plan task、`@manual`)は head branch の名前で PR を見つける。名前は fork
  と衝突しうるので、これらの行は head repository と head ref の一致も要求する。issue 行は
  closing-PR link で PR を帰属させ、fork PR を受け入れ続ける。
- merge と branch 削除の両方が、行が PR を主張した根拠(repository 付きの closing-issue
  link、または head branch)を再検査する。本文から closing keyword を消す、別 repository の
  同番号 issue へ retarget する、head branch を rename する、のどれもコミットを動かさずに
  主張を落とすため。何も閉じない PR は素通しせず拒否する。
- closing-issue link は全ページを歩く。行の issue が 2 ページ目以降にあることがある。
  ページは別々の読み取りで、1 つの snapshot ではない。

## merge-claims.json による保持

- 結果が読めなかった merge はエラーではなく unknown として返し、その PR を
  `<git-common-dir>/fanout/merge-claims.json` に保持する。別タブ、リロード、ダッシュボード
  再起動のどれからも二発目は撃てない。このファイルが endpoint 唯一のローカル書き込み。
- 保持は poll が PR の merged または closed を示したときだけ解除する。時間の経過は結果の
  証拠ではない。手動で外す方法はファイルからエントリを削除すること。
- 保持は `gh` を実行する前に取り、結果が判明したら解除する。結果を見てから書く設計では、
  保持が必要になる「応答が届かない」ケースそのものを守れない。merge 途中のクラッシュ
  も、失われた応答と同じ証拠を残す。
- claims file が書けない、または読めないときは merge を拒否する。再起動を生き残らない
  guard で走らせない。
- 「保持なし」を意味するのはファイルが無いときだけ。壊れたファイルを空として読むと、
  未解決の merge を失い、次の保持がその唯一の記録を上書きする。
- read → check → reserve の一連は claims file の lock の下で行う。1 つの repository に
  2 つのダッシュボードが走りうるし、atomic write は個々の書き込みを不可分にするだけで、
  その周りの判断は守らない。
- file と lock は worktree 自身の `.fanout` ではなく repository 共通の fanout ディレクトリ
  (git common dir。Herdr intent journal と同じ)に置く。ダッシュボードは全 linked
  worktree の session を列挙するので、兄弟 worktree で起動したダッシュボードも同じ PR を
  表示し、片方の worktree に書いた保持はもう片方には存在しないことになる。

## queued merge の第二の終端

- queue 必須の base では、`gh pr merge` が作るもの(armed な auto-merge、または merge
  queue のエントリ)は後から取り消されうる。PR は open のまま何も pending でない状態に
  なり、merged / closed の検査では永久に解除できない。queued merge も同じ方法で保持し、
  この第二の終端を記録する。
- 「pending が無い」を数えるには、保持の後に始まった GitHub 読み取りが必要。クリック前の
  読み取りも pending 無しを示すから。その読み取りが snapshot の `ghRefreshedAt`。
  クライアントも必要とする。snapshot は数秒ごとにローカル state から GitHub 抜きで
  再構築されるため。
- snapshot 内の PR の全コピーを参照する。1 つの PR は複数の行に載り、issue 側と branch 側
  の fetch は別の時刻に着地する。
- 結果が読めなかった merge の保持は、この終端で解除しない。その merge は既に起きて
  いるかもしれない。
- 送信失敗が auto-merge を armed のまま残したなら、retryable ではなく landed として扱う。
  それを arm できたのはこのコマンドだけ。
- 送信より前に起きたと証明できるエラー(rate-limit gate)は、普通の retryable な失敗のまま。

## `gh pr merge` の exit 0 は証拠ではない

merge queue 必須の base では queue 投入で成功終了する。merged / deleted と報告する前に
結果を GitHub で確認し、確認できない merge は fail closed。

## branch 削除の OID フェンス

- 削除する branch は base repository にある PR 自身の head ref からだけ決める。
- ref が PR の head SHA を指している間だけ消す。その SHA は live read で GitHub が報告した
  もので、クライアントが名指ししたものではない。クライアントが選んだ SHA でフェンスする
  と、「クライアントが ref の現在の tip を名乗れるか」の確認に化ける。
- リクエスト本文の head SHA は行の echo であって、コミットの自由な選択ではない。branch の
  現在の tip を名乗れば live check と OID フェンスの両方を満たし、merge 後に push された
  作業まで巻き込んで消せてしまう。
- 同じ head branch に立つ他の open PR がこの repository に無いことを要求する。base 違いで
  2 本立てられるので、片方をマージしてもその branch は終わっていない。`--limit` で打ち
  切られた一覧は「他に誰も使っていない」と読まずに拒否する。
- fork の head、不明な head ref、動いた ref、2 本目の open PR は削除を落として理由を報告
  する。cleanup の前提条件が merge そのものを拒否することはない。
- OID フェンスは原子的ではない。GitHub に条件付き ref 削除は無い。既に着地した push は
  捕まえるが、merge 確認後の往復の間に着地する push は捕まえない。
- ref path の各セグメントは percent-encode する。`feature/#123` のような合法な ref が
  fragment で切れ、その 404 が「既に無い」と誤認されるのを防ぐ。

## diff toolbar の pin

- diff toolbar は開いたときの PR(番号、head、base)を pin し、画面の patch が merge で
  入るものと比較可能であることを要求する。
  - PR の head は `/api/diff` が読んだコミットである。
  - その読み取りは clean な worktree のものである。patch は merge が運ばない未コミットの
    作業を映すから。
  - merge base は remote が既に持つコミットである。`MergeBase` はローカルの base branch を
    優先するので、そこに未 push のコミットがあると、それ以降が patch から抜け落ちる。
- 読んでいる間に着地する push、head を動かさない retarget、remote に遅れた worktree は
  いずれも merge をブロックする。表示された patch に含まれないコミットを黙ってマージ
  しない。

## 意図的に残す 2 つのギャップ

閉じずに文書化して残す。

1. base の検査はローカルの remote-tracking ref を読む。最後の fetch 以降に GitHub 側で
   force-push された base は、古いローカル状態で判定される。tracking ref と GitHub の live
   base の一致を要求すると、base が動くたび(ほぼ常時)に全 merge がブロックされる。
2. worktree は 1 回の収集の間、コミットと clean 状態でしか pin されない。その窓の中で
   編集して戻した変更は、もう存在しない作業を patch に見せうる。この方向は merge が運ぶ
   より多く見せるだけで、少なく見せることはない。フェンスが防ぐのは後者。

## stacked PR

- GitHub の native stacked PR では、途中の層をマージすると下の未マージの層もまとめて
  マージされる。公式 docs は、REST の同期 merge API と GraphQL の `mergePullRequest`
  (`gh pr merge` が使う)を stacked PR 非対応とし、API からは非同期 merge API
  (`merge-async`)を使うよう求めている。`merge-async` は下の open な層ごとマージする。
- 手で積んだ stack(base が別の PR の head)では、途中の層は trunk ではなく下の層の
  branch へマージされる。下の層はその変更をレビューされないまま運ぶ。
- どちらの形でも、下の層が未マージの間は途中の層のマージを 409 `stack_below_unmerged`
  で拒否する。判定は merge 直前の live 読み取りで行う。snapshot の `PRRef.Stack` は web の
  無効化理由にだけ使い、サーバの判定にも保持の解放にも使わない。
  - 手積み: live の base を head に持つ open PR がこの repository にあるか。REST の
    `head=<owner>:<branch>` で引き、fork の同名 branch は数えない。`gh pr list --head main`
    は fork の `main` からの PR も返し(microsoft/vscode で 58 件、2026-10-04)、一覧の上限で
    拒否に倒れて main へのマージが全部止まる。
  - native: live の stack 読み取りで、自分より下の層に未マージ(close を含む)が無いか。
    読み取りが返さなかった層(entries は 100 層まで)は未マージとして数える。
- base が default branch なら手積みの検査をしない。stack は trunk の上に積むもので、
  trunk から出た open PR(main を develop へ同期する PR)を数えると main へのマージが
  全部止まる。
- stack の読み取りに失敗したら拒否する。他の PR を巻き込まないことを証明できない
  merge は撃たない。例外は schema に stack のフィールドが無い応答(`undefinedField`)で、
  stack の無いサーバとして扱う。preview の改名も同じに見えるが、native stack の途中層は
  下の層の head を base にするので、手積みの検査が拒否する。
- default branch 以外の長寿命 branch を base にする PR も塞ぐ。`develop` → `main` の
  release PR が open の間は、`develop` へのマージが止まる。GitHub 上で stack の層と区別
  できないため。
- web は同じ述語を snapshot で評価し、理由付きで無効化する。どの行にも載らない下の層は
  web から見えず、サーバの live 読み取りが 409 で返す。逆に snapshot は default branch を
  持たないので、issue 行が closing-issue link で trunk を head に持つ open PR を載せて
  いると、web はサーバより広く塞ぐ。押せなくなるだけで、誤ってマージはしない。
- 下の層がマージ済みでも branch が残っていれば、手積みの上の層の base はその branch の
  まま。そこでマージすると trunk には入らない。この状態は検査しない(長寿命 branch の
  マージ済み PR と区別できない)。base の付け替えは、下の層の branch 削除で GitHub が行う。

## stacked PR の branch 削除

- native stack は、下の層のマージ時点で上の層の base を trunk へ付け替える(#823 の
  マージ 2 秒後に #826 の AutomaticBaseChangeSucceededEvent、head 削除はその後)。
  削除の時点で上の層はもうその branch を base にしていない。
- 手積みの次の層は、下の層がマージされても base が残る。GitHub は、マージ済み PR の head
  branch を消すと、それを base にする open PR を付け替えるとしている。REST の ref 削除
  (この endpoint の経路)でも同じかは未確認で、付け替わらなければ PR は close される。
- そのため、削除する branch を base にする open PR がこの repository にある間は、削除を
  409 `branch_is_base` で拒否する。一覧は `--limit` で打ち切られても、載った行は全部
  数える。

## 表示側(mutation の不変条件ではない)

- TUI の PR 列は、native stack の層の位置を `⧉ 2/3` で出す。stack は dashboard の poller と
  同じ読み取り(`GH.PRStacks`)を wave 間隔ごとに行い、表示にだけ使う。
- ドロワーの stack map は、他の行の名前からその行のドロワーへ切り替える。
