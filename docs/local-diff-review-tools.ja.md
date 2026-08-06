# ローカル diff レビューツール統合の予備調査

ステータス: 調査報告(予備調査) + dashboard diff ビューア決定記録。
予備調査は 2026-07-20、dashboard の決定は 2026-07-27。実装なし。
候補ツールのドキュメント調査に加え、有力 3 ツールを合成リポジトリの
linked worktree + tmux 3.6a(macOS arm64、Node 24)で一時実行して検証した。
外部ツール連携と dashboard diff ビューアの実装計画は本書末尾に記録する。

## 背景

fanout の子セッションは `.fanout/worktrees/<slug>/` の worktree で実装を進める。
その成果を人間がレビューする面は現状 GitHub の PR 画面かエディタしかなく、
「エディタにも GitHub にも依存せず、手元で各セッションの diff を読んで
指摘を返す」面が欠けている。本書は、hunk などの既存 diff ビュアー兼
ローカルレビューツールでこの面を埋められるかを調べた記録である。

前提となる設計制約は [pr-review-visualization-v2.ja.md](pr-review-visualization-v2.ja.md)
の横断原則から引き継ぐ: diff 描画は既存ツールへ委譲し fanout は配線に徹する、
独立した専用 Web ビューアと mutation を伴うレビュー UI は作らない、
外部コマンド起動は allowlist + opt-in なしに出荷しない。
既存の read-only dashboard SPA に置く表示専用パネルは例外とし、diff 描画は
既存ライブラリへ委譲する。

## 評価軸

fanout の使い方から、次を評価軸にした。

- **worktree 適合**:linked worktree(`.git` がファイル)で動くか
- **base 指定**:merge-base 基準の diff(gitstat と同じ意味論)を表示できるか。
  base branch が先行した checkout で子の変更だけを見るために必須。
  未追跡ファイル(エージェントが新規作成したファイル)を落とさないことも
  ここに含める
- **tmux 適合**:ペインまたは `display-popup` 内で描画と操作が成立するか
- **還元ループ**:人間が付けた指摘を、ペインで生きている子エージェント
  (書いた本人)へ機械的に渡せるか
- **依存とライセンス**:ランタイム要件、配布形態、ライセンス、活発度

## 候補一覧

| ツール | 形態 | レビュー機能 | エージェント連携 | 依存 | ライセンス / 活発度 |
|---|---|---|---|---|---|
| [hunk](https://github.com/modem-dev/hunk) | TUI | 行コメント(TUI 内入力) | `hunk session` CLI で双方向 | Node 18+(npm `hunkdiff` / brew) | MIT / 7.3k stars、v0.17.3(2026-07) |
| [difit](https://github.com/yoshiko-pg/difit) | ローカル Web(GitHub 風) | 行コメント(ブラウザ) | `difit comment` CLI で双方向 | Node 21+(npx) | MIT / 活発、v5.0.8 |
| [revdiff](https://github.com/umputun/revdiff) | TUI | 行注釈(TUI 内入力) | 終了時に markdown 出力 + exit 10 | なし(Go 単一バイナリ、brew / deb / rpm) | MIT / 693 stars、v1.11.1(2026-07) |
| [critique](https://github.com/remorses/critique) | TUI | 記載なし | agent 向け review コマンド | Bun 必須(npx 非推奨) | MIT / 1.2k stars、最終 2026-04 |
| delta / diffnav | pager / navigator | なし(表示のみ) | なし | 単一バイナリ | 表示委譲先として v2 構想に掲載済み |

diffty、diffity(ローカル Web 系)は difit と同型で規模が小さいため、
個別評価を省いた。critique は「レビューコメントを残す」機能が確認できず、
ランタイムに Bun を要求するため試用対象から外した。delta と diffnav は
表示専用で今回の「指摘を返す」要件を満たさないが、v2 構想の
pager 委譲先としての位置づけは変わらない。

## 試用結果

合成リポジトリに base 先行コミットを積んだ linked worktree を作り、
hunk、difit、revdiff を隔離 tmux ソケットのペイン(200x50)で実測した。
3 ツールとも linked worktree で問題なく動作した。

### hunk

- `npx hunkdiff diff <target>` で起動。描画は 3 候補中もっともリッチで、
  サイドバー、split 表示、シンタックスハイライトが tmux ペイン内で成立
- **`hunk diff main` は two-dot 比較**。base 先行分が Deleted として混入した。
  merge-base の SHA を target に渡せば子の変更だけになることを確認済みで、
  fanout 側は gitstat の base 解決をそのまま渡せばよい
- 未追跡ファイルは既定で含まれる(`--exclude-untracked` で除外する側の
  設計)。ただし対象 repo の `.hunk/config.toml` が `exclude_untracked` を
  上書きでき、repo 側から未追跡ファイルを隠せる点は統合時の検査対象
- `hunk session` CLI が双方向の還元ループを提供する。検証した往復:
  エージェント側から `session comment add --repo <path>`(TUI に
  Agent note がインライン描画される)、人間が TUI で `c` → Ctrl+S で
  付けたコメントを `session comment list --type user` で取得、
  `session review` で diff 構造 + コメントの統合ペイロード取得
  (本文まで含めるには `--include-notes` が要る。既定は件数のみ)、
  `session navigate` / `session reload` で表示中セッションの遠隔操作
- セッションはデーモン経由で `--repo <path>` により識別され、tmux の
  pane id も保持する。worktree 単位で並ぶ fanout のモデルと相性がよい

### difit

- `npx difit . main --merge-base --no-open --background` で起動。
  **`--merge-base` をネイティブに持ち**、コミット済みと未コミットの変更を
  GitHub 風 Web UI で表示した(試用リポジトリに未追跡ファイルは
  なかった)。未追跡ファイルは既定で含まれず、`--include-untracked` は
  内部で `git add --intent-to-add` を実行して index を変更する
  (dist ソースで確認)。読み取り専用でない点に注意が要る
- `--background` は `{"port":4966,"url":...,"pid":...}` の JSON を返す。
  port は空き状況で変わり、サーバーは keep-alive で残留するため、
  fanout 側で JSON の port と pid を保持して comment CLI(`--port` 必須)
  への再利用と停止に使う前提になる
- サーバーは認証なしで全 API を公開する。特に `/api/open-in-editor` は
  POST body の `editor.command` / `argsTemplate` を子プロセスとして
  実行する(dist ソースで確認)。127.0.0.1 バインドは OS ユーザー間の
  隔離にならないため、共有ホストでは別ユーザーが port 走査からこの
  endpoint 経由で fanout ユーザー権限の任意コマンド実行に到達できる
- コメント往復を CLI で検証: `difit comment add '{"type":"thread",
  "filePath":...,"position":{"side":"new","line":N},"body":...}' --port 4966`
  で注入、`difit comment get --port 4966` で人間のコメントを整形済み
  テキストで取得(どちらも `--port` 必須)
- ブラウザを開く分だけ「端末内で完結」から一歩外れる。なお difit の
  HTTP サーバーは外部ツール自身のプロセス(127.0.0.1)であり、
  fanout が HTTP 面を増やすわけではない

### revdiff

- 単一バイナリで、リリースから取得してそのまま動いた。ランタイム依存なし
- `revdiff <merge-base SHA>` で子の変更だけを表示(未コミット分も含む。
  未追跡ファイルは `--untracked` 指定時のみで、index は変更しない)
- 人間が `a` で行注釈を付けて `q` で終了すると、`-o` 指定の markdown
  (`## ファイル:行` + 本文)が書き出され、`--exit-code-on-annotations` で
  **exit 10** が返る。「注釈が付いたら差し戻す」分岐を終了コードだけで
  組める
- `--annotations` で前回出力を再読込する round-trip、`--description-file` で
  タスク文脈を情報ポップアップに表示する口もある。fanout が briefing の
  要約を渡す使い方が考えられる
- 注釈を保存して終了すると、`-o` とは別に既定で
  `~/.config/revdiff/history/` へレビュー対象の diff 全体と注釈が
  自動保存される。checkout の外にソースのコピーが残る挙動なので、
  統合時は `--history-dir` の管理が要る(後述)
- 常駐デーモンを持たないため、エージェント側から取得や操作を駆動する
  CLI はない。ただし終了時一括に限られるわけでもない: `O` キーで注釈を
  終了せずに `-o` 先へ書き出し(flush)でき、`R` で diff を再読込できる
  ので、flush→エージェント修正→再読込の反復は人間のキー操作起点で回せる

## 統合形態の比較

**(a) fanout 本体へ取り込む**: 却下。diff 描画とレビュー UI の自作は
v2 横断原則「表示は委譲、fanout は配線」に反し、依存最小主義とも衝突する。
試用した 3 ツールはいずれも活発に保守されており、自作で上回る見込みもない。

**(b) オプション連携**: 推奨。agent CLI(claude / codex)と同じ扱いで、
ツールが PATH にあれば起動でき、なければその機能だけがエラーになる。
必須依存は増えない。既存の統合レール(後述)に乗り、fanout 側の実装は
コマンド組み立てと起動だけで済む。

**(c) ドキュメントのみ**: 「`fanout` の shell 起動(`A` キー)で worktree に
入り、手で `npx difit .` を打つ」というレシピは今日でも書ける。
ただし merge-base 解決とコメントの還元ループが手作業のまま残るので、
(b) の実装までの暫定と位置づける。

## 推奨

(b) のオプション連携を推奨する。対応ツールは 1 つに絞らず、
**既知ツールの registry(allowlist)+ ユーザー設定での選択**にする。
agent registry(`internal/core/agent`)と同型であり、複数対応の増分は
起動コマンドの組み立てだけになる。初期 allowlist は hunk と revdiff の
2 つにする:

- **hunk**: TUI 常駐 + 双方向 session CLI。レビューと修正を並走させる
  使い方の本命。v0.x の若さだけが留意点
- **revdiff**: 依存ゼロの単一バイナリ。Node を入れていない環境でも動く
  最小構成で、exit code による差し戻し分岐が簡潔

difit(GitHub 風 Web UI)はブラウザで読みたい人向けの有力候補だが、
初期 allowlist からは外して保留にする。試用結果に書いたとおり、
認証なしの `/api/open-in-editor` が POST body のコマンドを実行するため、
共有ホストでは fanout が起動したサーバーがそのまま他 OS ユーザーからの
任意コマンド実行面になる。全 `/api/*` を起動ごとのトークンでゲートする
fanout dashboard と同じ基準を満たさない。upstream に認証か endpoint
無効化の口が入った時点で再評価する(それまでユーザーが手で
`npx difit` を使うことは妨げない)。

任意コマンド文字列をユーザーに書かせる設計(`diffViewerCommand` のような
自由文字列)は採らない。registry 外のコマンドは起動しない。設定キーは
repo config から与えられないようにして、リポジトリをクローンさせるだけで
viewer 選択を差し込まれる経路を塞ぐ。現行 settings の `RepoEditable` は
保存時(`SaveEditable`)の検査にしか使われないため、これだけでは実行時の
repo config 読込を止められない点に注意(実装詳細は Phase 1 に記す)。

検出と起動は PATH 上のグローバル実行ファイル(`hunk`、`revdiff`)に
統一する。試用では npx を使ったが、npx 経由の起動は未導入 package の
ネットワークインストールが走るうえ、`exec.LookPath` による存在判定とも
両立しない。未導入のツールはメニューに出さないかエラーで案内し、
インストール自体はユーザーに委ねる(agent CLI と同じ扱い)。

## Dashboard diff ビューア

#575 では既存の web dashboard SPA に、Session worktree の diff を読む
表示専用パネルを追加する。
Drawer から全幅パネルを開き、merge-base 基準の committed、uncommitted、
untracked の変更を syntax highlight と light/dark テーマ付きで表示する。
dashboard の 127.0.0.1 bind、起動ごとの token、GET-only、mutation なしという
境界は変えない。

これは独立した専用 Web ビューアを新設する判断ではない。
CodeSee の教訓が退けるのは、コードベース全体の常設地図を別製品へ分離し、
既存のレビュー面から利用者を移す設計である。
既存 dashboard に worktree 単位の一時的な diff 表示を足す本件は該当しない。
サーバーは patch を配信するだけとし、描画はライブラリへ委譲する。

### 描画ライブラリ

`@pierre/diffs` v1.2.x を採用する。
Apache-2.0 で、git patch を直接入力でき、Shiki による syntax highlight と
テーマを標準で扱い、1.x の安定版に達しているためである。
patch 入力 API または Shadow DOM のテーマ連携が実装を阻む場合は、
`@git-diff-view/react` v0.1.x へ切り替える。
こちらは patch 入力と React 描画を満たすが、v0.1.x のため第 1 候補にはしない。

次の候補は採らない。

- `diff2html`: patch から HTML 文字列を生成するため、後述の敵性入力規約と
  衝突する
- `react-diff-view`: syntax highlight に refractor の自前配線が必要
- `@codemirror/merge` と Monaco DiffEditor: old/new の全文を要求する
  2 文書エディタであり、patch の閲覧には過剰

### SPA 側の描画方針

描画量は DOM node の総量予算ではなく仮想化で有界にする。
file 列は `@pierre/diffs` の `<Virtualizer>` で包み、画面外の file は高さだけ確保した
placeholder(shadow root + div 1 個)にする。
描画 node 数が patch のサイズではなくビューポートに比例するため、契約上限の
500 files や 26 万行の file でも初期表示が破綻しない。
実測では 16 files / 50,000 px の diff が 2,400 node に収まる。

初期状態は全 file を展開する。
描画行数 1,000 行以上の file だけを折りたたみ、展開してもハイライトは切らない。
複数 file を同時に展開したままにできる。
仮想化では有界にならない「file 全体に走る処理」だけを内容量で抑える:
Shiki のトークン化は総文字数 150,000 字または 20,000 行を超えたら切り、
行内 word 差分は 30,000 字を超えたら切る。
文字数と行数の両方が要る。どちらか一方では内容量を測れないため:

- 行数では測れない。1 行に何千字も詰めた高密度 patch は、66 行でも 65,000 字に
  できる(交互トークンだと Shiki が 1 文字 1 span まで出す)。
- 文字数では測れない。74,000 行 × 1 字は 74,000 字にしかならず、文字数の上限に
  掛からない。ライブラリ自身の `tokenizeMaxLength`(既定 100,000、比較対象は行数)
  にも掛からないので、展開した瞬間に main thread が止まる。

どちらも契約内(1 file 256 KiB)で作れる。

`.diff-files` は素の block flow にする。
flex の `gap` や margin を入れると virtualizer が持つ file ごとの top と実際の位置が
ずれ、遠い file ほど描画位置が狂う(実測: `gap: 14px` × 16 file で約 270 px)。
`diffs-container` はカスタム要素で既定が `display: inline` のため `block` にする。
これを外すと placeholder の高さが効かず全 file が潰れる。

サイドバーは patch を持つ file だけを、同じディレクトリごとにまとめて並べ、
クリックで本文の該当 file へ飛ぶ。
patch に含まれなかった file(バイナリ・サイズ超過・上限)は警告帯の直下に
`<details>` で常時出す — サイドバーは本文が狭いと畳まれるので、そこにしか
無いと「どの file がなぜレビューできないか」が狭い画面で丸ごと消える。
この一覧は自分の高さを 30vh で止めて中でスクロールさせる。契約上限の 500 file
がすべて省略になりうるので、伸び放題にすると固定高の column flex の中で本文
(`min-height:0`)が 0 まで潰れる(実測: 500 件で本文 0px)。
path の byte 順では `a/b.ts` < `a/c/d.ts` < `a/e.ts` のように同一ディレクトリが
連続しないため、連続塊ではなく Map で束ねる。
飛び先の索引を作るときは patch 側の path を生へ戻してから照合する
(`unquoteGitPath`)。
不正な UTF-8 byte の置換規則はサーバー(Go)に合わせて 1 byte につき 1 個の
U+FFFD にする — WHATWG の `TextDecoder` は不正な列をまとめて 1 個にするので、
素で復号すると `files[].path` と key が食い違う。git は `core.quotePath`(既定 on)で非 ASCII や制御文字を
C 形式にエスケープするが、サーバーの `files[].path` は生のままなので、
そのままでは非 ASCII のファイルが飛べない。
`parsePatchFiles` は外側の `"` だけ剥がしてエスケープは残すので、引用符の有無で
分岐してはいけない — テストは実パーサを通すこと(一度これで正規化が丸ごと
効いていなかった)。
ジャンプはスクロールだけでは成立しない。
これは次の virtualizer の性質による。
`VirtualizedFileDiff` は自身の scroll 内オフセットを一度しか計算せず、更新するのは
`onRender(dirty)` の `dirty` 経路 — つまり root か content container の resize を
検知したときだけである。
通常のスクロールでは前後の file が実描画で伸縮してもオフセットが古いままになり、
行は生成されているのに画面外へ置かれて空白に見える。
実機では 21 files / 181,000 px の diff で、30 か所中 27 か所の着地点が
ほぼ全画面の空白になった。

修復手段は 1 つに統一する。
root(スクロールコンテナ)の border-box 高さを 1px 動かして resize を検知させ、
全 file のオフセットを取り直させる(以下 nudge)。
DOM を作り直さないので安く、スクロール中に毎フレーム走らせられる。
スクロール中の空白、サイドバーからのジャンプ、全画面とコンパクトの切替は
すべてこれで直す。
ジャンプと切替では高さ確定後の次フレームで位置を合わせ直す。

空白の判定は「見えている帯に実際の行があるか」だけで、行は文書順なので先頭と末尾の
矩形から描画済み範囲が分かる。
sticky なファイル名ヘッダは数えない(空白の画面でもヘッダだけは貼り付いて見えるため)。
折りたたみ中の file は行が無くて当然なので対象外。
発火は scroll イベントの rAF スロットルと、停止後 120 ms の押さえ。

**`FileDiff` を `key` で作り直してはいけない。**
React 側が要素を持つ(`isContainerManaged`)ため、ライブラリの `cleanUp` は
shadow root の `[data-placeholder]` と `[data-virtualizer-buffer]` を残したまま
参照だけ捨てる。
作り直した instance は残骸の上に重ねて描くので、file の高さがそのまま倍になる。
実測では 21 files の diff で 1 回の作り直しだけで全 file が 2 倍になり、
文書長が 92,098 px → 184,042 px に膨らんで広範囲が空白のまま復帰しなくなった。
テストは `diffs-container` の要素 identity が保たれることで固定する。

あわせて先読み量(`overscrollSize`)を既定の 1,000 px から 3,000 px へ上げる。
1,000 px は 1 フレームで消費されてしまい、高速スクロールで描画が追いつかない。
定常状態の DOM は 2,422 → 5,679 node に増えるが、仮想化の効果は保たれている。

実測(21 files / 92,098 px の diff を、1 フレームあたりの移動量ごとに連続スクロールし、
移動中の最大空白と停止後を測る):

| 速度 | 移動中の最大空白 | 空白フレーム | 停止後 |
|---|---|---|---|
| 600 px/frame | 0 px | 0 | 0 px |
| 1,200 px/frame | 0 px | 0 | 0 px |
| 2,400 px/frame | 766 px | 4/39 | 0 px |
| 6,000 px/frame | 766 px | 10/16 | 0 px |

修復前は 1,200 px/frame で 40 フレーム中 4 フレームが 400 px 超の空白になり、
2 秒待っても解消しなかった。
2,400 px/frame(= 144,000 px/秒)以上はスクロールバーを一気に投げた場合に相当し、
移動中の一時的な空白は描画スループットの限界で残る。
ただしどの速度でも停止すれば解消する。

ファイル名ヘッダは `stickyHeader` で上端に固定する(GitHub の files changed と同じ)。
長い行は `overflow: "wrap"` で折り返し、file ごとの横スクロールバーを出さない。

並べ方は auto / split / stack の 3 状態で、ヘッダのボタン 1 個が
auto -> split -> stack を巡回する(`fanout.diffLayout`、キー無しが auto)。
auto は本文領域の幅で決める: `AUTO_SPLIT_MIN_PX`(1,000px)未満なら stack。
ファイル一覧 288px を引いた残りを 2 面に割っても片側 350px 前後は残る値。
幅の取得に `ResizeObserver` は使わない — タブが非表示のあいだ配信が止まるうえ、
パネル幅は「全画面 / 1,100px 以下ならビューポート幅、それ以外はグリップで決めた幅」
として計算できる(1,100px は `web/src/styles/responsive.css` の @media と同期)。
切り替えは `options` 経由で、file を作り直さない。

片側しかない file(新規追加・削除)は `data-diff-type="single"` になり既定では全幅に
広がるので、`unsafeCSS` で追加のみを右半分、削除のみを左半分へ寄せる。
これは split のときだけ渡す。
stack も `data-diff-type="single"` になるので、渡したままだと本文が半分幅に潰れる。
`unsafeCSS` に入れるのは固定文字列だけで、patch 由来の値は混ぜない。

テーマは light 用と dark 用を別々に選ぶ。
`@pierre/diffs` は未登録のテーマ名を `@pierre/theming` の shiki カタログから動的
`import()` で解決するので、SPA 側は名前の文字列だけを持てばよく、テーマ本体は
選ばれたものだけが遅延 chunk になる。
未知の名前は `resolveTheme` が throw するため、localStorage から読んだ値は必ず
allowlist で検証してから渡す。

設定モーダルの見本は固定 patch を本番と同じ `<FileDiff>` に食わせて描く。
自前で配色を再現しないので、選んだテーマの見え方と本文が必ず一致する。
狭い 2 カラムに収めるため `diffStyle: "unified"` と `disableFileHeader` を渡すが、
`hunkSeparators: "simple"` は渡さない(v1.2.12 では本文が一切描かれなくなる)。
見本も `@pierre/diffs` を引くので、設定を開くまでロードしない遅延 chunk に置く。

### 全画面とコンパクトの 2 表示

diff ビュアーは全画面のモーダルと、詳細ドロワーの左隣に立てるコンパクトパネルを
切り替えられる。
**既定はコンパクト** — 一覧や詳細を見ながら差分を追えるほうが導線として自然で、
全画面はそこから広げる操作にする。
`fanout.diffView` はキー無しがコンパクト、`"full"` だけを保存する。
コンパクトは詳細と diff を並べて見るための表示なので、モーダルを降りる:
背面を `inert` にせず `aria-modal` も立てない(リストもドロワーも触れる)。
ただし 1,100px 以下ではコンパクトも CSS が nav 下の全幅パネルにするため、背面は
一切見えない。この帯は全画面と同じく「覆っている」扱いにする — 覆っているのに
非モーダルだと、見えない背面へ Tab が抜け、隠れた peek の 5 秒ポーリングも続く。
判定は `lib/diffView.ts` の `coversBackground` に 1 つだけ置き、オーバーレイ側
(inert と `aria-modal`)と App 側(peek の停止)が同じ答えを使う。App は実寸を
知らないので、オーバーレイから通知を受ける。

覆っているかはモードだけでは決まらない。パネルは右端がドロワーの左端に貼り付き、
越えた分は左へ伸びるので、1,100px を超えていても一覧が 1px も残らない配置がある
(1,200px の画面・ドロワー 840px・パネル 760px なら右端は 440px、パネルは
x=0-760 を占め、一覧の 0-360 は完全に隠れる)。CSS と同じ式で右端を求めて判定する。

覆っているかを本文幅の代わりに使わないこと。`panelWidthFor` は別に持つ —
上の配置ではパネルの実幅は 760px のままなので、ビューポート幅で auto を判定すると
狭い本文を左右 2 面に割り、container query でサイドバーまで消える。
グリップも同じ理由でこの帯には出さない(CSS が幅を固定するので動かせない)。

diff オーバーレイは lazy chunk なので、解決を待つあいだ Suspense の fallback が
Escape を持つ(`DiffPending`)。空 fallback にすると、その窓での Escape は
Drawer だけを閉じて `diffTarget` が残り、chunk が解決した瞬間に「閉じたはず」の
diff が出てくる(表セル起点では Escape 自体が効かない)。

Escape は「いま居るもの」を閉じる。オーバーレイは document の capture 段で
受けるが、背面を覆っていないコンパクト表示では、フォーカスが自分の中にあるとき
だけ引き取る。capture は React の handler より先に走るので、無条件に閉じると
背面で開いている popup(フィルタの dropdown 等)の Escape を横取りし、1 回の
キーで 2 層が同時に閉じる。

モーダルを閉じたあとのフォーカス復帰は、モーダル自身の cleanup ではなく App の
effect でやる。起点が diff の中のボタンだと、モーダルの cleanup 時点では diff が
まだ `inert`(sibling の effect cleanup は後)で、実ブラウザは `focus()` を
拒否する(実測: スタイル確定後は activeElement が body のまま)。親の effect は
子より後に走るので、App なら inert 解除の後になる。

設定モーダルが上にある間、diff オーバーレイは自分で `inert` になる
(`suppressed` prop)。設定側から `#diff-overlay` を遮らないのは、diff が lazy
chunk で、解決前に設定を開くとまだ要素が無く、後から inert 無しで mount されて
自分に focus を移してしまうため。mount 順に依存しない形にする。

背面を遮る `inert` は参照数で持つ(`lib/inert.ts`)。全画面 diff の上に設定
モーダルを重ねると `#root` の所有者が 2 つになり、素朴に付け外しすると
「後に開いたほうが閉じたときに前のぶんまで外す」「diff が snapshot から消えて
先に unmount され、設定が開いたまま背面が開放される」のどちらでも背面へ
Tab できてしまう。

パネルの右端はドロワーの左端に合わせる。
ドロワーは幅可変で選択が無ければ存在しないので、`ResizeObserver` で実測して
CSS 変数(`--diff-anchor-right`)へ落とす。
幅は左端のグリップでドラッグできる(`fanout.diffWidth` に保存)。
上限はビューポート幅の 95% で、ドロワーの左端では止めない:
`right: max(0px, min(var(--diff-anchor-right), calc(100vw - var(--diff-w))))` として、
左端が画面外へ出るところから右へずれてドロワーに被さっていく。
止めてしまうと、ドロワーを開いている間だけ広げられる幅が頭打ちになる。
右に 5% 残すのは、全画面ではなく「パネル」だと分かる帯を残すため。

ドロワーと同じ右アンカーなので、intent と rendered を分ける幅ロジックは
`usePanelWidth` に共通化し、`useDrawerWidth` / `useDiffWidth` はその薄い包み。
描画できる上限だけが違う: ドロワーは main 列に 360px を残す、diff パネルは
ビューポート幅の 95%。

ファイル一覧を出すかはビューポートではなく本文領域の幅で決める
(コンパクトは画面が広くてもパネルは狭い)。
`.diff-main` を `container-type: inline-size` にして container query で判定する。
閾値は一覧の 288px に本文の必要幅を足した値で、split は左右に割るので 490px、
stack は 1 列なので 372px(解決後の並べ方は `data-layout` で見る)。
container query は詳細度を足さないので、ルールは `.diff-sidebar` より後ろに置く。

### 一覧からの導線

Session リストの diff 列は、行 identity(`diffQuery`)を組めるなら常にリンクに
する。summary が解析できない(gitstat の一時失敗で `-` や自由文になる)場合も
リンクは残す — 復旧後に手で開き直す導線が消えるため。
`diffSummary` と `/api/diff` は同じ収集(後述の `--numstat -z --find-renames`)
を共有するので、行数そのものは一致する。
それでも行数で「差分なし」とは判定しない — binary だけ / mode だけ /
pure rename の変更は両方で `+0/-0` になり、commit 済みなら `clean` にもなるが、
`/api/diff` はそれらを全部レビュー対象として返す。開けないほうが害が大きい。
詳細ドロワーの「変更を表示」も同じ条件。

### キーボードと支援技術

`aria-modal` を名乗るあいだは Tab を自前で折り返す(`useFocusTrap`)。背面を
`inert` にしても、末尾から Tab / 先頭から Shift+Tab はブラウザ UI へ抜ける。
背面を覆っていないコンパクト表示では折り返さない — そこは背面へ出てよい。
折り返しの境界は「実際に見えている」要素だけで決める。サイドバーは container
query で `display:none` になるので、非表示のボタンを境界にすると最後に見えている
要素からの Tab が回らない。可視性は `checkVisibility` に聞く(`offsetParent` や
`getClientRects` は fixed 配置やレイアウトを持たない環境で当てにならない)。

「自分が最前面のモーダルになった」瞬間にオーバーレイへフォーカスを引き取る。
covering になったとき(ウィンドウを 1,100px 以下へ縮めた、コンパクトから全画面へ
切り替えた)も、上の設定モーダルが閉じて抑止が解けたときも同じ。判定に
`suppressed` を含めること — lazy chunk の解決待ちに設定を開くと、mount 時点で
covering かつ suppressed になり、covering だけを見ていると遷移が起きない。

覆っているあいだは背面の document スクロールも止める(`lib/scrollLock.ts`)。
オーバーレイは `position:fixed` でスクロールコンテナではなく、`inert` も scroll を
ロックしないので、ヘッダ上のホイールや `.diff-body` 端からのチェーンが背面の一覧を
動かしてしまう(閉じたときに読んでいた位置が変わっている)。`.diff-body` には
`overscroll-behavior: contain` も入れる。設定モーダルも単独で開くので同じロックを
持ち、重なっても壊れないよう inert と同じく参照数で管理する。背面が見える
コンパクト表示では止めない — そこは背面を触るための表示。

モーダルを閉じたあとのフォーカス復帰は、diff 側も App の effect が担う。diff の
lazy chunk が解決する前に対象 pane が snapshot から消えると `DiffOverlay` は一度も
mount されず、その経路(Suspense fallback が消えるだけ)では overlay の cleanup が
走らないため。

フォーカスの復帰先は最後に必ず Nav の歯車へ落とす。起点はいくらでも消える
(diff を開いた Drawer を先に閉じた、フィルタ変更で起点行が消えた、対象 pane が
snapshot から消えて diff ごと unmount された)ので、存続する要素を末尾に置かないと
どこにも戻らない。

ボタンの accessible name は対象を一意に含める。`変更を表示` だけだと同じ統計の
行が並んで区別できず、`折りたたむ` だけだと全 file のボタンが同名になり、
basename だけだと `src/index.ts` と `test/index.ts` が衝突する。
テストのセレクタも同様に注意する — `/折りたたむ$/` は「すべて折りたたむ」にも
一致するので、セパレータ込みで絞る。

### ボタンはアイコン + ツールチップ

diff ビュアーのボタン(再取得・並べ方・表示モード・テーマ設定・閉じる・
すべて展開 / 折りたたむ・file ごとの展開)はすべてアイコン 1 個にする。
狭いコンパクト表示でもヘッダが 1 行に収まる。
ラベルは `aria-label` と、CSS で出すツールチップ(`.tip` + `data-tip`)に同じ
文字列を渡す。
`title` は使わない — ネイティブのツールチップと重なって 2 個出る。
file ごとの展開ボタンは `renderHeaderMetadata` 経由で shadow root の中に slot
されるが、ツールチップは light DOM 側の CSS で出るのでクリップされない
(実機で確認済み)。
行数は「大きい file だ」という情報なのでボタンの外にテキストで残す。
表示モードの切替は状態表示ではなく「次にする操作」を名乗るアクションボタンなので、
`aria-pressed` は付けない(ラベルと意味が食い違う)。
3 状態を巡回する並べ方ボタンは、現在値と押した結果の両方をラベルに載せる
(`レイアウト: 自動(クリックで左右 2 面)`)。
アイコンは全画面 / コンパクトが斜めの外向き・内向き矢印、すべて展開 /
折りたたむが上下の外向き・内向き矢印。
並べ方は枠を縦線で割れば split、横線なら stack、auto は「幅で決まる」ことを
破線で示しつつ向きは解決後に合わせる(破線だけでは 15px で split と見分けが弱い)。

### 敵性入力

worktree 由来の path、hunk、patch と git のエラー文は敵性入力として扱う。
従来の「テキストノードのみ」という規約を、React のテキストノードまたは
入力のエスケープを構造的に保証する描画に限る規約へ広げる。
patch 由来の HTML 文字列や `dangerouslySetInnerHTML` は使わない。
描画ライブラリの更新時も、タグを含む patch が DOM 要素として注入されないことを
テストで固定する。

### `GET /api/diff` wire contract

リクエストは次のいずれかとし、値は URL encode する。

```text
GET /api/diff?parent=<parent>&issue=<issueNum>[&source=<sourceKey>]
GET /api/diff?parent=<parent>&task=<taskId>[&source=<sourceKey>]
```

`issue` と `task` は片方だけを指定する。
正の GitHub issue 行では `parent` + `issue`、plan task、`@manual`、負の
synthetic issue 行では `parent` + `issue`/`task` + `source` を行 identity
とする。
`source` は `/api/snapshot` の `sourceKey` をそのまま使い、worktree-local な
行では必須、GitHub issue 行では省略する。
サーバーは最新 snapshot の全行と identity を完全一致させ、0 件または複数件なら
diff を返さない。
tmux の再起動後に再利用される `paneId` は検索キーに使わない。
これにより tmux と Herdr のどちらも、pane の生死に関係なく記録済み
worktree を選べる。
worktree path と base ref はクライアントから受け取らず、token gate 通過後に
一致した行の記録からサーバーが解決する。
記録された base が `HEAD`、相対 rev、または対象 child branch 自身へ解決される
場合は #516 の strict merge-base 解決に従って拒否し、フォールバックしない。
GET の成功時は `application/json` と `Cache-Control: no-store` を付けて
次の全フィールドを返す。
HEAD は identity と記録済み worktree の存在まで検証し、Git を実行しない。
成功時は GET と同じ `application/json` と `Cache-Control: no-store`、
`200 OK` を返し、body は返さない。

```ts
type DiffResponse = {
  paneId: string;
  branchName: string;
  baseBranch: string;
  mergeBase: string;
  capturedAt: string;
  files: Array<{
    path: string;
    oldPath?: string;
    additions: number | null;
    deletions: number | null;
    binary: boolean;
    patchIncluded: boolean;
    omittedReason: "" | "binary" | "tooLarge" | "collectionLimit" | "responseLimit";
  }>;
  patch: string;
  truncated: boolean;
  totalBytes: number;
};
```

`capturedAt` は UTC の RFC 3339、`mergeBase` は strict に解決した commit SHA
とする。
`paneId` は一致した行の backend-native ID であり、tmux `%N` に限定しない。
`files` は後述する 500 files 上限内の全件を返し、空の場合も `null` ではなく
`[]` を返す。
rename は 1 file とし、`path` に移動先、`oldPath` に merge-base 側のパスを
入れる。rename でない file では `oldPath` を省略する。
バイナリの `additions` と `deletions` は `0`、`binary` は `true` とする。
`collectionLimit` で省略した file の `additions` と `deletions` は `null` とし、
統計を計算しない。
`tooLarge` で省略した file は、git が同じ numstat pass で算出済みの
`additions` と `deletions` をそのまま返す — 予算超過なのは patch 本文だけで、
ここを `0` に潰すと Session リストの合計と食い違う。
ただし未追跡かつ 256 KiB 超の file はサイズ判定で早期に打ち切り、numstat を
走らせないので `0` とする。
完全なファイルブロックが応答の `patch` にある場合だけ `patchIncluded` を
`true` にし、`omittedReason` は空文字列にする。
含まれない場合は `patchIncluded` を `false` にし、理由を `binary`、
`tooLarge`、`collectionLimit`、`responseLimit` のいずれかで返す。
`patch` は `diff --git` で始まるファイルブロックを連結した git patch であり、
HTML fragment として解釈しない。
`totalBytes` は 10 MiB 収集上限内で得た完全なファイルブロックの合計 byte 数で
あり、後段の 1 MiB 応答上限を適用する前の値とする。
`truncated` は 10 MiB 収集上限または 1 MiB 応答上限で 1 file 以上を省略した
場合に `true` とする。
`binary` または `tooLarge` だけで省略した場合は `false` のままとし、
`files[].omittedReason` で欠落を示す。

#### #577 の v1 収集方式

#577 の `gitstat.WorktreePatch` は strict に解決した `mergeBaseSHA` を基準に、
live worktree から read-only の Git command で差分を収集する。
tracked file の統計と path は
`git diff --numstat -z --find-renames <mergeBaseSHA> --`、patch は file ごとの
`git diff --find-renames <mergeBaseSHA> -- <path>` から得る。
どちらも `--ignore-submodules=none` を指定し、
repository または user の `diff.ignoreSubmodules` で gitlink を隠さない。
gitlink patch は `--submodule=short` で形式を固定する。
`--find-renames` と `-l0` は明示する — `diff.renames` は repository 設定で
無効化も copies 化もでき、`diff.renameLimit` は候補が増えると exhaustive
detection を黙って打ち切る。既定に任せると同じ worktree の行数が環境で変わる。
rename の patch は pathspec に移動元と移動先の両方を渡して取る。
片側だけでは git が追加として描き、`files[]` が約束した rename と食い違う。
`a` → `a/b` のように 2 つの path が入れ子になる rename は、祖先側の
子孫除外 pathspec が反対側を飲み込むため exact pathspec で切り出せない。
この対を検出したら収集全体を `--no-renames` でやり直し、その worktree の
rename を削除と追加の 2 file として返す。
`Runner.Worktree`(Session リストと詳細の `+X/-Y`)はこの収集を共有するので、
一覧と diff ビュアーの行数は乖離しない — 縮退したときも両方が同時に縮退する。
未追跡 file の統計は file ごとに git process を起こすが、`Runner.Worktree` は
dashboard の 2 秒 tick から呼ばれる。`UntrackedStatCache` が結果を保持し、
変わっていない file を数え直さない(実測: 未追跡 500 file で初回 4.1 秒 →
以降 36 ミリ秒)。
鍵は stat ではなく内容のハッシュにする — size も mtime も in-place の
書き換えを生き延びる(`cp -p` は同サイズの file の時刻をナノ秒まで復元する)
ので、stat を鍵にすると変更を取り逃がす。
ただし内容は git の text/binary 判定の唯一の入力ではない。`.gitattributes`
(worktree 内・`$GIT_DIR/info`・user file)、`core.bigFileThreshold`、
`diff.<driver>.binary` はいずれも同じ内容の判定を変え、この集合は閉じない。
入力を列挙して鍵に足す方式は取らず、entry を一定時間で失効させる。
乖離は「file 自体が変わるまで無期限」ではなく TTL に有界になる。
2 秒 tick なら file ごとの git process は依然として 9 割以上減る。
git は鍵を作った後に file を読み直すので、計測後に鍵を取り直して
変わっていた場合はキャッシュしない(その pass の統計は返す)。
cleanup された worktree は二度と巡回されないので、一定時間巡回されなかった
worktree ごと捨てる — Session の作成と cleanup は通常の運用ループであり、
放置すると常駐 TUI / poller のメモリが増え続ける。
上限を worktree 数にしないのは、監視対象が上限を超えると次に必要な entry から
順に追い出して全 miss になり、直したはずの飢餓が再発するため。
collector を tick ごとに作り直すとキャッシュも作り直されるので、
web の `poller` と TUI の `model` はどちらも `sessionview.GitWorktreeStat` を
1 度だけ構築して使い回す。
untracked file は `git ls-files --others --exclude-standard -z` で列挙する。
入れ子の checkout は git が末尾スラッシュ付きの directory entry(`sub/`)1 つに
畳むので、これは両サーフェスとも skip する — 中身は別 repository のもので
diff 対象がなく、file として扱うと収集全体が失敗して当該 worktree の行が
毎 poll エラーになる。列挙した file は
`/dev/null` に対する file ごとの `git diff --no-index` で統計と patch を得る。
index から削除した tracked path と同名の untracked file は 1 file に統合する。
同じ file type の replacement は merge-base blob を repository 外の一時 file に置き、
final worktree side との `git diff --no-index` から単一 patch block を得る。
一時 directory は canonical worktree の parent に sibling として作り、
`TMPDIR` は使わない。
file type が変わる replacement は削除、追加の 2 block を 1 file group とする。
この一時 file は immutable な merge-base side だけを保持し、
worktree または index の snapshot isolation は #593 に委譲する。
tracked path に `SKIP_WORKTREE` または `assume-unchanged` が設定されている場合は、
worktree entry の有無にかかわらず live index の stage 0 blob size を
content の読み出し前に検査し、この index side を patch に使う。
skip-worktree/sparse-directory entry の immutable な最終 side の生成は #593 に委譲する。
`--no-index` の exit status は `0` と「差分あり」の `1` を成功とする。
`--numstat` の additions と deletions がともに `-` の file は binary と判定する。
tracked と untracked の各 file は path 順に並べる。
いずれかの side が 256 KiB(262,144 bytes)を超える file と binary file は
`files` に残し、patch から省略する。

#577 の zero-value `gitstat.Runner` は 10 秒、500 files、10 MiB 収集、
1 MiB response、32 MiB 累積 budget と `collectionLimit`/`responseLimit` を
適用しない。
#578 の endpoint は同じ request context と `MaxFiles` / `MaxPatchBytes` を
`WorktreePatch` へ渡し、500 files と 10 MiB を収集中に止める。
file 上限は未追跡 file を計測する前に判定する — 1 file につき git process が
1 つ起きるので、明らかに上限超過の要求が全件ぶんのコストを払ってから
弾かれることがないようにする。tracked と untracked の両方にある path は
1 file に畳まれ、内容が同じなら消えることもあるので、取りうる最小の件数でも
上限を超えるときだけ弾く。
1 MiB response 上限と省略処理も #578 の endpoint が担う。

#### #593 に委譲する snapshot isolation

以下は #593 が `gitstat.WorktreePatch` の snapshot isolation を強化する際の
目標状態であり、
#577 の v1 acceptance criteria には含めない。
private index、NUL probe、収集後の再検証、live worktree を入力にしない patch
生成と repository-wide `--numstat` の廃止は #593 で実装する。
#593 は前節の wire contract と上限値を変えずに導入する。

一致した snapshot 行の `WorktreePath` はそのまま信用しない。
server project root と path を symlink 解決して canonicalize し、project root
から取得した `git worktree list --porcelain -z` に exact top-level として
含まれることと、両者の canonical git common dir が同じことを確認する。
子 directory、symlink alias、同じ branch 名を持つ別 checkout は拒否する。
project root の worktree registry と common dir 配下の admin record から、
対象 top-level に対応する per-worktree gitdir を一意に決める。
対象で得た `git rev-parse --absolute-git-dir` はその gitdir と exact match
させ、index と `HEAD` は検証済み gitdir からだけ読む。
worktree の `.git` file または directory と admin directory は symlink を拒否し、
device、inode、`.git` file の digest を記録する。
この検証は snapshot 収集の開始時と確定時に繰り返し、記録された
`branchName` と worktree の現在 branch、per-worktree gitdir の identity を
一致させる。

開始時に worktree の `HEAD` commit SHA と、strict に解決した base の full
refname と commit SHA を記録し、この 2 SHA から merge-base を計算する。
確定時に同じ gitdir の `HEAD` と同じ base ref を解決し直し、どちらかの SHA が
変わっていたら snapshot を捨てて 1 回だけ取り直す。
同じ 2 SHA から merge-base も再計算し、開始時の SHA と一致させる。
common dir の `info/grafts` は存在すれば 502 とし、開かない。
shallow file は no-follow で snapshot に複製し、開始時と確定時の
device、inode、digest を一致させる。
存在しない場合も absence を記録し、途中で作成されたら不一致とする。
ancestry を読む Git command は request-private な grafts absence と shallow
snapshot だけを使い、live common dir の `info/grafts` と shallow file を
参照しない。
shallow file が存在しない場合も immutable な空の shallow input を使う。
この分離を安全に構成できない場合は merge-base 計算前に 502 とする。
後述する changed-path の再取得と合わせて、同じ request で取り直す回数は
1 回までとし、再び変化した場合は 502 にする。

すべての Git 呼び出しは `LC_ALL=C`、`GIT_OPTIONAL_LOCKS=0`、
`GIT_LITERAL_PATHSPECS=1`、`GIT_CONFIG_NOSYSTEM=1`、
`GIT_CONFIG_GLOBAL=/dev/null`、`GIT_ATTR_NOSYSTEM=1`、
`GIT_NO_LAZY_FETCH=1`、`GIT_NO_REPLACE_OBJECTS=1`、
`GIT_TERMINAL_PROMPT=0` で実行する。
継承した `GIT_*` は消去し、この allowlist と Git が返した repository path
だけを設定し直す。
repository-facing Git command の `GIT_DIR` は検証済み per-worktree gitdir、
`GIT_WORK_TREE` は検証済み canonical top-level に固定する。
request-private な `--no-index` diff engine では `GIT_DIR` と `GIT_WORK_TREE` を
設定しない。
`git --no-pager` と `-c core.fsmonitor=false` を使い、pager と fsmonitor を
起動しない。
`HOME` と `XDG_CONFIG_HOME` は server 所有の空 directory に向け、
repository-facing Git command に live system/global config と system/global
attributes を読ませない。
server 起動時に system-scoped config と user-scoped global config の relevant key を
server 所有の immutable snapshot に固定する。
対象は `core.autocrlf`、`core.eol`、`core.fileMode`、`core.symlinks`、
`core.ignoreCase`、`core.precomposeUnicode`、`core.excludesFile`、
`core.attributesFile`、`safe.directory` とする。
system/global config source を安全に確定または parse できない場合と、
`include`/`includeIf` がある場合は 502 とし、include 先を開かない。
system/global の `core.excludesFile` は参照先を no-follow、256 KiB 上限で同じ
snapshot に複製し、安全に複製できなければ 502 にする。
`core.excludesFile` が未指定の場合は、server 起動時の trusted `XDG_CONFIG_HOME`、
またはその未設定時の `HOME/.config` から Git 既定の `git/ignore` を解決する。
既定 file は同じ no-follow、256 KiB 上限で snapshot に複製し、absence も記録する。
既定 path を安全に確定または複製できない場合は 502 にする。
Git が system/global attribute source として解決する file は no-follow、
256 KiB 上限で同じ snapshot に複製し、安全に確定または複製できなければ 502 にする。
server 起動後は live system/global config、excludes file、attribute source を
再読込しない。
protected scope の `safe.directory` は複数値を保持して評価する。
snapshot が `*` または対象 path を信頼していた場合も、repository-facing Git
command へ渡す値は検証済み canonical project root と canonical worktree path
だけに置き換え、`*` と対象外 path は渡さない。
最初の repository-facing Git command に必要な project root は server が保持する
dashboard の project identity から確定し、request の path は使わない。
snapshot が対象 path を信頼していない場合は Git の ownership 拒否を維持する。
common config と worktree config は raw file として no-follow で検査する。
repo-local または worktree-local の `core.attributesFile`、
`core.excludesFile`、`core.worktree` と、外部 config を読める
`include`/`includeIf` が 1 件でもあれば 502 にし、指定先を開かない。
section と key は Git と同じく ASCII case-insensitive に照合し、曖昧または
parse できない config も 502 にする。
検査に使った common/worktree config は immutable snapshot とし、
repository-facing Git command は live config を再読込せず同じ snapshot だけを
使う。
固定方法は #593 に委譲する。
repository object format は同じ immutable config snapshot に固定する。
`sha1` だけを許可し、`sha256` と不明な format は object 読み出しと patch 生成の
前に 502 にする。
backend は object database に触れる前に、Git が `GIT_NO_LAZY_FETCH` を
サポートする version であることを確認する。
未対応 version と missing object は remote fetch を試さず 502 にする。
strict ref 解決、merge-base、tree 列挙、content 読み出しで使う commit、tree、
blob は、Git object header と content から SHA-1 を再計算して期待 object ID と
一致させる。
不一致 object の内容と、それをたどった Git command の結果は使わず 502 にする。
object traversal を検証済み immutable input に固定できない場合も 502 にする。

request ごとに mode `0700` の private snapshot directory を server の
temporary root に作る。
merge-base tree は strict に解決した commit の `ls-tree` と `cat-file`、
index は private directory に複製した index file の `ls-files --stage -z` と
`cat-file` から読む。
複製した index に stage 0 以外の entry が 1 件でもあれば、stage 1/2/3 の
いずれも選択せず manifest 作成前に 502 とする。
複製した index に split-index の link extension がある場合は 502 とし、
live `sharedindex.<hash>` を開かない。
worktree content は `openat` と `readlinkat` で raw byte として読み、
symlink をたどらない。
各 path component は worktree root の directory file descriptor から
`O_NOFOLLOW` でたどり、root 外へ出る path を拒否する。
target entry は regular file、symlink、gitlink だけを許可する。
worktree に entry がない gitlink は複製 index の commit pointer を最終 side に
使う。
gitlink path が no-follow で空かつ `.git` entry なしと確認できる directory の
場合も、未初期化 submodule として複製 index の commit pointer を最終 side に使う。
空判定と directory の `lstat` fingerprint は manifest に記録し、snapshot 確定時に
同じ directory file descriptor から再検証する。
directory に `.git` を含む entry がある場合、または空と安全に確認できない場合は
nested worktree を開かず 502 にする。
これにより、unstaged の submodule `HEAD` 変更を index pointer だけで差分なしに
しない。
merge-base で regular file か symlink だった path が worktree で directory の
場合は、merge-base entry の削除と standard ignore 適用後の配下 untracked file の
追加に分ける。
複製 index だけに同名 entry がある場合は parent path の削除を生成せず、配下の
最終 file だけを追加候補にする。
directory 自体は content source、candidate、`files` に含めない。
安全に分解できない directory と FIFO、socket、device は content を読む前に
502 にする。
regular file は `O_NONBLOCK|O_NOFOLLOW` を付けて `openat` し、直後の `fstat` で
regular file のままであることと `lstat` の device、inode、mode が一致することを
確認する。
symlink は `readlinkat` の後に `lstat` を取り直し、device、inode、mode の一致を
確認する。
worktree の path を content source として Git に渡さない。

candidate path は merge-base entry、複製した index entry、index stat と raw
`lstat` の差、standard ignore 適用後の untracked path の和から決める。
index stat と一致した tracked path は clean とみなし、racy な stat は changed
として扱う。
merge-base と index は mode と object ID で比較する。
manifest の logical mode は immutable config snapshot の effective value に従う。
`core.fileMode=false` では tracked regular file の executable bit の差を index
side に正規化して mode-only change にせず、untracked regular file の logical
mode も 100644 にする。
`core.symlinks=false` では index mode 120000 の symlink を表す worktree regular
file を logical mode 120000 とし、その raw content を link target として扱う。
安全に正規化できない entry は 502 にする。
`core.ignoreCase=true` では untracked traversal path を index path set と
Git-compatible に case-fold 比較し、一致する path を untracked として加えない。
case-fold 後に複数の path が衝突する場合は 502 にする。
macOS で `core.precomposeUnicode=true` の場合だけ、traversal path を
Git-compatible に precompose してから index path との照合、candidate identity、
ignore pattern 照合に使う。
precompose 後に複数の path が衝突する場合は 502 にする。
macOS 以外ではこの config 値にかかわらず path の byte identity を維持する。
複製した index に skip-worktree または sparse-directory entry がある場合、
worktree に path がないことを削除として扱わない。
immutable index の content を最終 side に使うか、安全に判定できなければ 502 に
する。
選択は #593 に委譲する。
server 起動時に複製した system/global excludes、worktree 内の `.gitignore`、
verified common dir の `info/exclude` は Git の precedence で適用する。
後者 2 source は no-follow と 256 KiB 上限で private snapshot に複製する。
実装は immutable な ignore source で directory traversal を prune し、ignored
path を列挙結果へ出さず、metadata 出力上限と対象 file 数に含めない。
`core.ignoreCase=true` では同じ Git-compatible な case-fold をすべての ignore
source の pattern 照合にも適用する。
prune の実装方式は #593 に委譲する。
検証済み worktree root の `.git` entry は entry 自体だけを開かずに prune する。
descendant directory に `.git` entry がある場合は、同じ directory の他 entry を
収集する前に immutable な merge-base/index manifest と突合し、marker が nested
repository を表すか安全に検証する。
検証は Git command を起動せず、nested config、object、worktree content を読まない
範囲に限る。
空の file/directory、unsupported file type、外部 path、内部上限超過などで
nested repository と安全に確認できない marker は 502 とする。
enclosing directory 自体または全 descendant に tracked entry がなく、tracked
file/symlink から directory への置換でもない場合だけ、純粋な untracked nested
repository として enclosing directory 全体を prune する。
tracked entry または旧 path の削除が 1 件でも重なる場合は、変更を隠さず 502 に
する。
prune した directory と全 descendant は candidate path、対象 file 数、metadata、
patch に含めない。
`git add -N` を含む index/worktree 変更コマンドは呼ばない。

snapshot manifest には各 side の logical path、mode、size、object ID または
raw content、worktree の `lstat` fingerprint を記録する。
開始時の tracked changed-path 集合と untracked path 集合は、NUL 区切りの
byte 列として保存する。
収集後は複製した index の全 tracked entry に同じ stat 判定を再適用し、
tracked changed-path 集合を作り直す。
index file の digest、tracked changed-path 集合、untracked path 集合、
manifest に含めた worktree path、attribute source、ignore source、shallow file の
fingerprint を開始時と比較する。
開始時と一致しなければ private snapshot を捨て、前述の合計 1 回以内で
取り直す。
再び変化した場合は 502 にする。
一致した時点で snapshot を確定して `capturedAt` を記録する。
確定した merge-base side と最終 worktree side は後述の変換後に mode と
canonical content を比較する。
両方が同じ candidate path は `files`、500 files 上限、patch 収集から除外する。
確定後は live worktree、live index、repository attributes を patch または
numstat の入力に使わない。

server 起動時に複製した system/global attributes、current worktree の
`.gitattributes`、収集時に複製した `.git/info/attributes` の各 source を Git の
precedence で適用し、候補 path の `filter` attribute を preflight する。
current worktree に必要な `.gitattributes` がない場合だけ複製 index の同じ path
へ fallback し、merge-base tree の attribute source は適用しない。
`core.ignoreCase=true` では同じ Git-compatible な case-fold をすべての
attribute source の pattern 照合にも適用する。
regular-file side で 1 source でも設定されていたら、clean/process filter の
command 設定有無にかかわらず 502 へ fail closed する。
attribute の判定は command 名を得るだけで、driver を起動しない。
実際の diff engine は attributes を参照せず、private snapshot から後述の変換を
適用した canonical byte pair と logical path だけを受け取る。
この構造により、preflight 後に live worktree や `.gitattributes` が変わっても
filter command は起動しない。
同じ preflight で、system、global、common、worktree の precedence を反映した
`core.autocrlf`、`core.eol` と、候補 path の `text`、legacy `crlf`、`eol`、
`working-tree-encoding`、`ident` attribute を immutable input から評価する。
legacy `crlf` は Git-compatible な `text`/`eol` の意味へ正規化する。
raw byte pair と通常の Git diff の内容が異なる変換は、外部 command を起動せず
各 side に Git と同じ方向で再現するか、502 にする。
attribute と config による content 変換は logical mode が regular file の side
だけに適用する。
mode 120000 は merge-base blob または `readlinkat` の raw target、mode 160000 は
commit pointer を content identity とし、filter、改行、encoding、`ident` 変換を
適用しない。
変換後の byte pair を canonical content とし、diff engine、binary 判定、
additions/deletions、synthetic object ID の共通入力にする。
変換を安全に再現できない場合は binary 判定と object ID 計算の前に 502 とし、
raw byte pair を成功 response に使わない。
再現と fail closed の選択は #593 に委譲する。

tracked と untracked の patch は確定した snapshot から file ごとに生成する。
Git を diff engine に使う場合は repository の外にある request-private
directory で `--no-index` を使い、live worktree、repository path、object
database、index、attributes を渡さない。
`--no-index` の exit status は `0` と「差分あり」の `1` を成功、`2` 以上を
失敗として扱う。
`--no-ext-diff`、`--no-textconv`、`--no-color`、`--no-renames`、`--text`、
`--full-index`、`--unified=3`、`--inter-hunk-context=0`、
`--diff-algorithm=myers`、`--no-indent-heuristic` を固定する。
backend は logical path から `diff --git`、`---`、`+++` header を組み立て、
path を C-quote する。
mode と object ID は manifest から `old mode`、`new mode`、
`new file mode`、`deleted file mode`、`index` header に反映する。
regular file、symlink、gitlink の file type bits が変わる path は、単一 block の
`old mode`/`new mode` にせず、同じ path の deleted-file block と new-file block
に分ける。
その path は `files` では 1 件とし、patch は 2 block を削除、追加の順に連結して、
`additions` と `deletions` は両 block の合計とする。
gitlink の object ID は commit pointer を使い、patch hunk の各 side は
`Subproject commit <40-hex-oid>\n` という標準 pseudo-content から組み立てる。
bare SHA を request-private `--no-index` の入力にしない。
worktree canonical content の object ID は Git blob と同じ SHA-1 で計算し、
request-private `--no-index` が出力した `index` header を信用しない。
additions と deletions は同じ canonical byte pair の hunk から数え、repository-wide
の `git diff --numstat` は呼ばない。

path の列挙結果は NUL 区切りで解析する。
`--no-renames` を固定するのは request-private な `--no-index` が常に 1 対の
file しか見ないためで、rename を 2 file に割る意味ではない。
rename の連結は manifest 側の `--find-renames` 付き numstat 収集から決め、
backend が logical path から `rename from` / `rename to` header を組み立てて
1 file group として返す。ここを 2 file に割ると Session リストの行数と
食い違う(#620 で一本化した不変条件)。
repository-relative path は snapshot manifest から取得し、patch header の
C-quoted 文字列を path 復元に使わない。
untracked symlink は mode 120000 の link 自体を対象とし、link 先を読まない。
regular-file path の `diff` attribute は immutable source から binary probe より
先に評価する。
unset の `-diff` は binary、値なしで set された `diff` は text とし、
`diff=<driver>` は driver を起動せず 502 にする。
`diff` が unspecified の場合だけ、各 side の canonical content の先頭
8000 bytes に NUL byte があるかで binary を判定する。
patch は binary でない path ごとに repository-relative path の byte 順で生成する。
以下では 1 path 分の patch を file group と呼ぶ。
通常の file group は完全な `diff --git` block 1 個、file type change は前述の
削除・追加 block 2 個で構成し、収集上限と response 上限では不可分に扱う。
patch は完全な file group を path 順に連結する。
symlink は mode 120000 の link 自体、gitlink は commit pointer だけを対象とし、
どちらも参照先の file や nested worktree を読まない。
未初期化の submodule は superproject index の gitlink commit だけを使う。
初期化済みの submodule は nested `HEAD` や content を無視して成功させず、
前述の 502 にする。
バイナリと、いずれかの raw side または canonical side が
256 KiB(262,144 bytes)を超える changed file は `files` に含めるが diff engine へ
渡さない。
untracked file、削除、mode change、または複製 index と一致する stat から
merge-base と最終 side の差を content 読み出しなしに確定できる tracked file だけを
`tooLarge` として成功 response に含める。
tracked regular file の stat が複製 index と一致しないか racy で、上限内で
content identity を確認できない場合は、stat-only candidate を変更として返さず
502 にする。
`omittedReason` は `binary` または `tooLarge` とする。
`tooLarge` は `binary: false` とし、`additions` と `deletions` は `0` とする。
Git object の size は content を得る前に `cat-file --batch-check`、worktree
regular file の size は `lstat` で確認する。
上限を超えた side は content を読まず、上限以下の regular file だけを
private snapshot に全量保存する。
raw side が上限以下でも、各変換の出力は 262,144 bytes + 1 で打ち切る。
canonical side が上限を超えた時点で `tooLarge` とし、binary 判定、diff、
additions/deletions、synthetic object ID の計算を行わない。
attribute source が 256 KiB を超えた場合は解析せず 502 にする。

repository-relative path は JSON 化の前に UTF-8 validity を検査する。
不正な path は byte 列を置換して表示せず 502 にする。
complete patch block も UTF-8 validity を検査し、不正なら該当 file を
`binary: true`、`patchIncluded: false`、`omittedReason: "binary"` とする。
Git のエラー出力が UTF-8 として不正な場合は、byte 列を置換せず固定の
`git command failed` message を返す。

#### #578 の request-wide 上限

GET 1 request の `WorktreePatch` には共有の 10 秒 deadline を設定する。
各 Git subprocess で 10 秒を取り直さず、metadata 収集と patch 生成で同じ
request context の残り時間を使う。
deadline 到達時は実行中の Git process を停止し、partial response を返さず
502 にする。
HEAD は Git を実行しない。

#593 の private snapshot 導入後は、worktree 検証、non-Git の stat/raw read、
snapshot の収集と確定、process group の停止、bounded stdout/stderr も同じ
deadline に含める。
binary probe は 8000 bytes/side、変換出力と diff engine 用の regular-file read は
256 KiB/side を超えない。
#593 は次の累積 budget を private snapshot と同時に実装する。
#578 の v1 には適用しない。
1 request で raw side の読み出しと canonical side の変換出力に使う累積 byte
budget は、全 file/side 合計で 32 MiB(33,554,432 bytes)とする。
per-side 上限で `tooLarge` とした content は読まず、この budget に含めない。
raw content を解放または deduplicate しても読み出した byte を差し引かず、
canonical output も同じ budget へ加算する。
既知の raw size 合計だけで budget を超える場合は content を保存する前に 502 とし、
変換中に超える場合は binary 判定、diff、統計、OID 計算を止めて 502 にする。
patch 以外の stdout とすべての stderr は 10 MiB + 1 byte を検出した時点で
process group を停止して 502 にし、buffer も 10 MiB + 1 byte を超えない。

tracked と untracked を合わせた対象数は 500 files を上限とする。
merge-base tree と index の raw metadata entry 数にはこの上限を適用しない。
raw metadata は共有 deadline の範囲で最後まで join する。
#593 の導入後は、同 issue で決める内部 byte 上限も適用する。
ignore、nested repository の prune、mode と canonical content の比較を終え、
成功 response の `files` に残る変更 path が 501 件になった時点で 502 にする。
上限超過で canonical content を作らない `tooLarge` も変更 path として数える。
成功 response の `files` は必ず対象全件を含む。

patch の収集上限は 10 MiB(10,485,760 bytes)とする。
各 file group の diff engine stdout は残り byte 数 + 1 byte まで読み、完全な
file group を加えると上限を超える場合は、その group の block を 1 個も加えない。
その file と後続の patch 対象 file は `patchIncluded: false`、
`omittedReason: "collectionLimit"` とし、`truncated` を `true` にする。
同じ file の `additions` と `deletions` は `null` とする。
以後の patch subprocess は起動しない。
patch buffer は 10 MiB + 1 byte を超えない。
各 diff engine の regular-file 入力も before/after 各 256 KiB 以下であり、
巨大 tracked file、巨大 untracked file、sparse file の content は渡さない。

成功 response の固定上限は、JSON serialization 後の body 全体で
1 MiB(1,048,576 bytes)とし、クエリによる変更は許さない。
`files` 全件、空の `patch`、その他の field を最終形で serialize した
metadata-only body が上限を超える場合は、`files` を省略せず 502 にする。
上限以下なら、収集済みの完全な file group を path 順に 1 つずつ加えた各候補を
JSON serialize し、body 全体が上限内に収まる最大の先頭部分を返す。
この計算は改行、control character、quote の JSON escape 後の UTF-8 byte 数を
使い、file group とその中の block の途中では切らない。
上限で除外した file と後続の patch 対象 file は `patchIncluded: false`、
`omittedReason: "responseLimit"` とし、`truncated` を `true` にする。
`responseLimit` は収集済みの完全な file group だけに適用し、`binary`、
`tooLarge`、`collectionLimit` の既存理由を上書きしない。
両上限に該当する file は、先に適用した `collectionLimit` を報告する。
先頭の 1 file group も収まらない場合は `patch` を空文字列にする。
body 全体が上限以下なら patch をそのまま返し、10 MiB 収集上限で設定済みの
`truncated` は保持する。
SPA は `truncated` が `true`、またはいずれかの `patchIncluded` が `false` なら、
review 対象が patch に揃っていないことを警告する。

10 秒、500 files、10 MiB、1 MiB、256 KiB は #578 v1 の初期値である。
#578 の実装で調整する場合は同じ PR で本書、handler test、MSW fixture を更新し、
実装と wire contract を一致させる。
32 MiB は #593 の初期値である。

#593 は次の snapshot isolation test を追加する。
#593 の test では、未初期化の submodule は gitlink 変更を含め、初期化済みの
submodule は nested `HEAD` の変更有無にかかわらず 502 になることを固定する。
未初期化 submodule の path が欠落または空 directory の場合は index pointer を
使い、空 directory に entry または `.git` marker が現れた場合は 502 にする。
filter command を一度も起動しないこと、非 UTF-8 path は 502、非 UTF-8 content は
`binary` になることも固定する。
preflight 後に live worktree と `.gitattributes` を変更しても filter command を
起動しないこと、raw content が 256 KiB 以下でも変換後の canonical content が
256 KiB を超える tracked file は binary/OID を計算せず `tooLarge` になることを
同じ test で確認する。
全 file/side の raw read と canonical output の累積が 32 MiB を超える場合は、
content の保存または変換を止めて 502 になることを確認する。
per-side 上限で `tooLarge` とした content は読まず、累積 budget だけを理由に
metadata-only 表示を 502 にしないことを確認する。
clean と判定した tracked file を収集中に変更した場合は snapshot を取り直すこと、
partial clone の missing object で fetch または credential helper を起動せず
502 にすることも固定する。
commit、tree、blob の content と期待 object ID が一致しない場合は 502 となり、
不一致 content を patch に使わないことを確認する。
FIFO、socket、device は blocking read の前に 502 とし、JSON escape と 500 files
の metadata を含む成功 body が 1 MiB を超えないことを確認する。
snapshot 収集中の `HEAD`/base ref 変更と per-worktree gitdir の差し替えを
検出すること、repo-local `core.attributesFile` と config include は 502 に
なることも固定する。
変更のある regular file で `git diff --no-index` が exit 1 を返しても、
handler は 200 と patch を返すことを確認する。
repo-local `core.excludesFile` と `core.worktree` は 502 にし、
`info/exclude` の変更は snapshot を取り直すことを確認する。
replace ref は無視し、legacy graft は 502、shallow boundary の変更は snapshot の
取り直しになること、ancestry command は live grafts/shallow を読まないことも
固定する。
base=`A`、index=`B`、worktree=`A` と、staged add 後に worktree から削除した
path は最終変更なしとして `files` に含めないことを確認する。
tracked regular file または symlink を同名 directory と配下の untracked file に
置き換えた場合は、旧 path の削除と新規 file の追加に分けることを確認する。
standard ignore を適用しない raw path 集合が 10 MiB を超える worktree でも、
ignore 後の対象が上限内なら 200 と対象 file だけを返すことを確認する。
tracked entry が 500 件を超えても最終変更が 500 files 以下なら 200 とし、
最終変更 path が 501 件なら 502 になることを確認する。
`collectionLimit` の file は統計が `null` となり、1 MiB response 上限にも
該当する場合は `omittedReason: "collectionLimit"` を保持することを確認する。
config snapshot の確定後に live config を差し替えても Git command が再読込しない
ことと、非実行型の改行/encoding 変換を無視した全行差分を返さないことを確認する。
skip-worktree と sparse-directory entry は削除として返さず、安全に最終 side を
決められない場合は 502 になることを確認する。
root の `.git` entry と純粋な untracked nested repository は候補、対象 file 数、
metadata、patch のすべてから除外し、nested repository は enclosing directory
全体を prune することを確認する。
空の `.git` file/directory など nested repository と確認できない marker は、
enclosing directory を黙って prune せず 502 になることを確認する。
tracked subtree に `.git` entry を追加した場合と、tracked file を `.git` entry の
ある directory に置き換えた場合は、tracked 変更を隠さず 502 になることを確認する。
system または global だけに設定した `core.autocrlf` と `core.eol` も改行変換へ
反映し、system/global excludes に一致する path は candidate path に含めないことを
確認する。
`core.excludesFile` 未指定時も既定の global `git/ignore` に一致する path は
candidate path に含めないことを確認する。
system/global config と excludes file を server 起動後に差し替えても再読込しない
ことを確認する。
system/global attributes だけに設定した `filter` は command を起動せず 502 とし、
`text`、legacy `crlf`、`eol`、`working-tree-encoding`、`ident` も変換または
502 になることを確認する。
merge-base にだけ残る `.gitattributes` は current worktree の判定へ混ぜず、
current worktree で欠落する source だけ複製 index へ fallback することを確認する。
legacy `crlf` による改行正規化を適用し、raw content の差だけで全行差分にしない
ことを確認する。
`working-tree-encoding` で UTF-16 から変換できる file は raw NUL byte を理由に
binary とせず、canonical content の text hunk を返すことを確認する。
mode 120000 の symlink target は `text`、`eol`、`working-tree-encoding`、`ident`
に一致しても raw bytes を canonical content とし、target の CRLF 差分を消さない
ことを確認する。
worktree side の synthetic object ID と additions/deletions は同じ canonical
content から計算することを確認する。
binary probe は 0-origin offset 7999 の NUL を binary、offset 8000 の NUL を
text と判定することを確認する。
`-diff` は NUL のない regular file も binary、値なしの `diff` は NUL のある
regular file も text とし、`diff=<driver>` は driver を起動せず 502 になることを
確認する。
tracked file が 256 KiB を超え、stat だけが複製 index と異なる場合は、
変更と確定できないまま `tooLarge` にせず 502 になることを確認する。
gitlink patch は bare SHA ではなく
`Subproject commit <40-hex-oid>` の削除・追加 hunk を返すことを確認する。
regular file、symlink、gitlink 間の file type change は同じ path の
deleted-file block と new-file block に分かれ、`files` は 1 件、統計は両 block の
合計になることを確認する。
その 2 block は 10 MiB 収集上限と 1 MiB response 上限の境界で分割せず、
両方を含めるか両方を省略することを確認する。
`core.fileMode=false` の chmod は差分にせず、executable bit のある untracked
regular file は logical mode 100644 とすることを確認する。
`core.symlinks=false` の symlink 表現は logical mode 120000 として扱うことを
確認する。
`core.ignoreCase=true` では index path と case-fold 一致する traversal path を
untracked add にせず、case だけが異なる ignore pattern も適用し、衝突時は 502 に
なることを確認する。
同じ case-fold を system/global/current/index fallback の attribute pattern
照合にも適用し、case だけが異なる `diff`、`text`、`working-tree-encoding`
などを見落とさないことを確認する。
macOS で `core.precomposeUnicode=true` の場合は NFC/NFD が異なる clean path を
untracked add にせず、precompose 後の衝突は 502 になることを確認する。
macOS 以外では同じ config でも path の byte identity を維持することを確認する。
protected scope の `safe.directory` が対象 path または `*` を信頼する shared
checkout は exact canonical path だけを Git command へ渡して処理でき、対象外の
path は信頼しないことを確認する。
split index は 502 となり、`sharedindex.<hash>` を開かないことを確認する。
modify/delete と add/add の未解決 conflict は stage 1/2/3 の内容を選ばず 502 に
なることを確認する。
SHA-256 repository は 502 とし、SHA-1 repository の synthetic worktree side は
Git blob と同じ 40 桁の object ID になることを確認する。

handler が生成する次のエラー body は `{"error":"message"}` とし、
`application/json` と `Cache-Control: no-store` を付ける。

- `400 Bad Request`: identity query が欠落または不正で、`issue`/`task` の
  排他指定や worktree-local 行の `source` 必須条件を満たさない
- `404 Not Found`: identity が snapshot の 1 行に定まらない、worktree 記録が
  ない、cleanup 済み、または同じ git common dir の worktree として検証できない
- `502 Bad Gateway`: base/merge-base の strict 解決、filter/diff attribute の検査、
  system/global config/excludes/attributes、config/attribute 由来の変換、
  mode/case/path の正規化、`safe.directory` の限定、split/sparse index、
  未解決 conflict、tracked subtree と重なる nested repository、submodule、
  object format の安全な確定、
  object ID と content の不一致、live grafts/shallow を使わない ancestry、
  lazy fetch を使わない object 読み出し、snapshot の確定、unsupported file type、
  diff 収集 timeout、32 MiB snapshot input 上限、
  500 files/metadata 出力上限、metadata-only response 上限の超過、または
  diff engine の実行に失敗した

共通 middleware が生成する token 不一致の `403 Forbidden` と GET/HEAD 以外の
`405 Method Not Allowed` は既存どおり `text/plain` とし、上の JSON error
contract には含めない。
ただし `/api/diff` の全 response は middleware error を含めて
`Cache-Control: no-store` を付ける。

### 実装委譲事項

#576 は HTTP request/response の意味論、上限、エラーと dashboard の決定を固定する。
#577 は前述の v1 収集方式と file ごとの 256 KiB 上限を実装する。
#578 は 10 秒、500 files、10 MiB、1 MiB の request-wide 上限、response への
省略処理、handler と dashboard を実装する。
#593 は次の snapshot isolation と内部の強化を実装する。

- `GIT_INDEX_FILE` などで複製 index を live index から分離して読み出す方法
- common/worktree config の immutable snapshot、fingerprint、Git command への固定
- system/global config、excludes、attributes の server-lifetime snapshot、fingerprint
- ancestry command を private grafts/shallow input と検証済み object view に固定する方法
- commit/tree/blob content の object ID を再検証する方法
- protected scope の `safe.directory` を exact canonical path に限定して渡す方法
- `core.fileMode`、`core.symlinks`、`core.ignoreCase`、
  `core.precomposeUnicode` を manifest と path 照合へ反映する方法
- unmerged entry、split-index extension、repository object format の検出方法
- skip-worktree/sparse-directory entry の最終 side を immutable index から作る方法
- current `.gitattributes` の複製、index fallback、`core.ignoreCase` の pattern
  照合を固定する方法
- file/symlink から directory への置換と nested repository を traversal で分ける方法
- nested `.git` marker を command 実行なしに検証する方法
- `diff` attribute の binary override、file type change の 2 block 分解、
  gitlink pseudo-content を生成する方法
- 非実行型の改行/encoding 変換と canonical content を再現するか fail closed に
  するかの選択
- request-private temporary directory を全終了経路で削除する方法
- admin/metadata file の総 byte 数、source 数、file type の内部上限
- 32 MiB の snapshot input 上限、bounded stdout/stderr、process group cancellation

#577/#578/#593 の実装判断が本書と食い違う場合は、同じ PR で本書、handler test、
MSW fixture を更新して wire contract と実装を一致させる。

### レビューコメントの将来計画

表示専用の境界を保ったまま、指摘を返す導線は次の 3 択を残す。

1. difit の認証または危険な endpoint の無効化が実現した後、保留を解除して
   dashboard から difit URL へ移す
2. TUI の還元ループ #518-521 に委譲する
3. dashboard とは別プロセスの opt-in mutation サーバーを追加する

2 を推奨する。
dashboard で diff を読み、指摘は TUI または GitHub PR で付ける。
1 は difit の保留条件を満たすまで採れず、3 は別の認証、寿命管理、mutation
境界を増やすため当面採らない。

## 実装計画

### Phase 1: 起動導線(MVP)

worktree を選んでビュアーを開けるようにする。

- `internal/core/diffviewer`(新設): registry の純データ(ツール名、
  コマンド、引数組み立て)だけを置く。実行ファイル解決(`exec.LookPath`)は
  infra 側に置く。core の stdlib 純度検査(`internal/arch`)は `os/exec` を
  禁止しており、既存例外は `internal/core/agent` のみ。例外を増やさない
- `cmd/fanout/worktree_action.go`: `promptWorktreeAction` メニューに
  「3. Open diff viewer」を追加。既存の prefix+M ポップアップから到達。
  ただしこのメニュー自身が `display-popup -E` の中で動いており、
  tmux 3.6a では popup 内から `display-popup` を呼んでも既存 popup の
  属性変更として扱われ、新しいコマンドは起動しない。action popup を
  終了してから呼び出し元クライアントで viewer popup を開くか、
  現 popup プロセス内で viewer を直接 exec する
- `internal/ui/tui`: `Options` に `LaunchReview` フィールドを追加し、
  キー 1 つ(候補: `R`)を `openSelectedWorktreeShellCmd` と同型で配線。
  `help.go` に 1 行追加
- 起動形態: hunk / revdiff とも `tmuxrun.DisplayPopup` で開く。
  `DisplayPopup` は子プロセスの stdout も終了コードも呼び出し元へ
  返さないため、popup 側の結果(revdiff の exit 10 と `-o` 出力)は
  `tui_popup.go` の result / done 一時ファイル方式で回収する。popup 内で
  外部コマンドを呼ぶため同ファイルの PATH forward も通す
- hunk の session 識別: `--repo <path>` は同一 worktree に session が
  複数あると一致エラーになる。起動時に session ID を特定して pane /
  worktree と対応付けて記録し、二重起動は新規 session ではなく既存
  session の `reload` 再利用にする
- revdiff の自動履歴: 既定の `~/.config/revdiff/history/` 保存は
  checkout 外にソースの diff を永続化する。`--history-dir` を fanout
  管理の一時領域へ向けて worktree cleanup と一緒に消すか、永続保存を
  明示的な opt-in にする
- base 解決: merge-base を strict に解決する関数を `internal/infra/gitstat`
  に新設し、ビュアーへ SHA で渡す(hunk の two-dot 問題を fanout 側で
  吸収する)。表示統計用の既存 `diffBase` は base を解決できないとき
  `HEAD` へフォールバックする仕様なので流用しない。フォールバックすると
  空 diff を「変更なし」と誤読させるため、解決失敗はエラーにして
  レビューを中止する
- 未追跡ファイル: revdiff は `--untracked` を付ける。hunk は既定で
  含まれるが、対象 repo の `.hunk/config.toml`(`exclude_untracked =
  true`)が既定を上書きして隠せる。issue 本文と同様に repo 内容は
  信頼境界の外なので、起動時に `git ls-files --others
  --exclude-standard` の結果(`git status --porcelain` は
  `status.showUntrackedFiles=no` の環境で `??` を出さないため使わない)
  と `session review` のファイル集合を照合し、欠落があれば fail closed
  にする。新規ファイルを見ないままの承認を許さないのがこの軸の要件
- `internal/infra/settings`: ビュアー選択キーを 1 つ追加
  (string、`RepoEditable: false`)。未設定時は registry の PATH 発見順。
  `RepoEditable: false` は保存時検査のみなので、実行時読込の
  `repoOverrides` にもこのキーの明示除外を追加し、repo config に直接
  書かれた値が user config を上書きしないことをテストで固定する

### Phase 2: 指摘の還元ループ

人間の指摘を子エージェントに渡す。CLI は転送のみで、LLM 文脈の解釈は
skill / briefing 側に置く(CLI は LLM を呼ばない鉄則の維持)。

- hunk: `session comment list --type user` の出力を該当ペインの受信箱へ
  転送する補助 verb(または skill 手順)。session の指定は Phase 1 で
  記録した session ID を使う(`--repo` 指定は複数 session で壊れる)
- difit(保留から戻した場合): fanout 管理の子プロセスとして stdin を
  閉じた非対話 foreground 起動(`--no-open`)にし、出力から port を
  読み取って記録する。`--background` は port 競合時に `Port ... is busy`
  行が JSON より先に出るのに親が子 stdout の最初の非空行しか返さず、
  回収が壊れて detached サーバーが残るため使わない。未追跡ファイルの
  ある worktree では起動を fail closed にする(対話プロンプトの既定
  Enter が `git add --intent-to-add` で index を変更するため、対話に
  入らせない)。還元は `comment get --port <記録した port> --format json`
  から人間の未送信 message だけを抽出して転送する(text 形式は author を
  省略し、エージェント注入分も混ざるため、注入時に author / ID を付けて
  区別し、送信済み管理を持つ)
- revdiff: exit 10 を検知して `-o` の markdown を転送。セッションを
  終了しない随時還元は、人間の `O` flush で更新される `-o` ファイルを
  watch して転送し、修正後は `R` 再読込で受ける
- 配送経路: `fanout msg send` は SQLite への保存のみで、push 配送
  (`msg watch`、codex bridge)は `--team` セッションにしか存在しない。
  通常ペインへ届けるには、保存に加えて state ゲート付きの
  `fanout msg nudge` を打つか、`--team` を前提にする。人間(レビュアー)
  側の sender identity をどう表すかもこの Phase の設計項目
- v2 構想の案 4(Findings 裁可コンソール)が想定する「d = diff ビュアーへ
  パイプ」導線は、この Phase の自然な続きになる

### Phase 3: dashboard diff ビューア(#575)

既存の web dashboard SPA に表示専用パネルを追加し、上記の
`GET /api/diff` から worktree の patch を読む。
dashboard の read-only 境界(GET のみ、mutation なし)は変えない。
difit URL への導線は前提にせず、difit の保留記録も変更しない。
指摘の還元は TUI の #518-521 または GitHub PR に委譲する。

## 不採用の記録

- **本体取り込み(自作)**: 上記 (a) のとおり却下。判断根拠は v2 横断原則
- **difit の初期 allowlist 入り**: 保留。認証なしの `/api/open-in-editor`
  が POST body のコマンドを実行するため(試用結果を参照)、fanout が
  起動したサーバーが共有ホストで任意コマンド実行面になる。upstream で
  認証か endpoint 無効化が可能になれば再評価する
- **critique**: Bun を追加ランタイムとして要求し、レビューコメント機能も
  確認できなかった
- **delta / diffnav**: 表示専用のため本件の主候補にならない。v2 構想の
  pager 委譲先としての採用余地はそのまま
- **自由文字列でのビュアーコマンド設定**: registry 外の任意コマンド起動に
  つながるため採らない

## 残る不確実性

- hunk は v0.x で CLI 面が変わりうる。採用時は briefing / skill 側に
  バージョン前提を書くより、fanout 側の組み立てを registry に閉じ込めて
  追従点を 1 箇所にする
- `display-popup` 内での TUI 描画は、ヘッドレス検証ではクライアントを
  用意できず未実測(ペイン内描画は 3 ツールとも実証済み)。実装時に
  実機確認する
- revdiff はエージェント側から取得を駆動できないため、随時還元は人間の
  `O` flush 起点になる。エージェント駆動の取得まで求めるなら hunk を選ぶ
  (difit にも同等の口があるが、前述のとおり保留)
