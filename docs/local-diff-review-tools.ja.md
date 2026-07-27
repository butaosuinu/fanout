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
HEAD は同じ status と header を返し、body は返さない。

```ts
type DiffResponse = {
  paneId: string;
  branchName: string;
  baseBranch: string;
  mergeBase: string;
  capturedAt: string;
  files: Array<{
    path: string;
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
バイナリの `additions` と `deletions` は `0`、`binary` は `true` とする。
`collectionLimit` で省略した file の `additions` と `deletions` は `null` とし、
統計を計算しない。
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
`HOME` と `XDG_CONFIG_HOME` は server 所有の空 directory に向け、global
config と global attributes を読ませない。
common config と worktree config は raw file として no-follow で検査する。
repo-local または worktree-local の `core.attributesFile`、
`core.excludesFile`、`core.worktree` と、外部 config を読める
`include`/`includeIf` が 1 件でもあれば 502 にし、指定先を開かない。
section と key は Git と同じく ASCII case-insensitive に照合し、曖昧または
parse できない config も 502 にする。
backend は object database に触れる前に、Git が `GIT_NO_LAZY_FETCH` を
サポートする version であることを確認する。
未対応 version と missing object は remote fetch を試さず 502 にする。

request ごとに mode `0700` の private snapshot directory を server の
temporary root に作る。
merge-base tree は strict に解決した commit の `ls-tree` と `cat-file`、
index は private directory に複製した index file の `ls-files --stage -z` と
`cat-file` から読む。
worktree content は `openat` と `readlinkat` で raw byte として読み、
symlink をたどらない。
各 path component は worktree root の directory file descriptor から
`O_NOFOLLOW` でたどり、root 外へ出る path を拒否する。
target entry は regular file、symlink、gitlink だけを許可する。
gitlink は worktree を開かず commit pointer だけを使い、FIFO、socket、device、
gitlink 以外の directory は content を読む前に 502 にする。
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
worktree 内の `.gitignore` と verified common dir の `info/exclude` は no-follow と
256 KiB 上限で private snapshot に複製する。
実装は immutable な ignore source で directory traversal を prune し、ignored
path を列挙結果へ出さず、metadata 出力上限と対象 file 数に含めない。
prune の実装方式は #577/#578 に委譲する。
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
確定した merge-base side と最終 worktree side の mode と raw content が同じ
candidate path は、`files`、500 files 上限、patch 収集から除外する。
確定後は live worktree、live index、repository attributes を patch または
numstat の入力に使わない。

merge-base tree、複製した index、private snapshot の worktree attributes、
収集時に複製した `.git/info/attributes` の各 source について、候補 path の
`filter` attribute を preflight する。
1 source でも設定されていたら、clean/process filter の command 設定有無に
かかわらず 502 へ fail closed する。
attribute の判定は command 名を得るだけで、driver を起動しない。
実際の diff engine は attributes を参照せず、private snapshot の raw byte
pair と logical path だけを受け取る。
この構造により、preflight 後に live worktree や `.gitattributes` が変わっても
filter command は起動しない。

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
additions と deletions は同じ raw byte pair の hunk から数え、repository-wide
の `git diff --numstat` は呼ばない。

path の列挙結果は NUL 区切りで解析する。
`--no-renames` により rename/copy は delete と add の 2 file として返す。
repository-relative path は snapshot manifest から取得し、patch header の
C-quoted 文字列を path 復元に使わない。
untracked symlink は mode 120000 の link 自体を対象とし、link 先を読まない。
binary 判定は Git attributes に任せず、各 side の raw content を filter なしで
先頭 8 KiB だけ読む。
その範囲に NUL byte がある file を binary とする。
patch は binary でない path ごとに repository-relative path の byte 順で生成し、
完全なファイルブロックを連結する。
symlink は mode 120000 の link 自体、gitlink は commit pointer だけを対象とし、
どちらも参照先の file や nested worktree を読まない。
submodule worktree の untracked/modified content は manifest に入れず、
superproject が記録する gitlink commit の変更だけを表示する。
バイナリと、いずれかの side が 256 KiB(262,144 bytes)を超える tracked または
untracked file は `files` に含めるが diff engine へ渡さない。
`omittedReason` は `binary` または `tooLarge` とする。
`tooLarge` の `additions` と `deletions` は `0` とする。
Git object の size は content を得る前に `cat-file --batch-check`、worktree
regular file の size は `lstat` で確認する。
上限を超えた side は content を読まず、上限以下の regular file だけを
private snapshot に全量保存する。
attribute source が 256 KiB を超えた場合は解析せず 502 にする。

repository-relative path は JSON 化の前に UTF-8 validity を検査する。
不正な path は byte 列を置換して表示せず 502 にする。
complete patch block も UTF-8 validity を検査し、不正なら該当 file を
`binary: true`、`patchIncluded: false`、`omittedReason: "binary"` とする。
Git のエラー出力が UTF-8 として不正な場合は、byte 列を置換せず固定の
`git command failed` message を返す。

1 request の diff 収集には共有の 10 秒 deadline を設定する。
各 subprocess で 10 秒を取り直さず、worktree 検証、private snapshot の収集と
確定、non-Git の stat/raw read、metadata 収集、patch 生成のすべてで残り時間を
使う。
binary probe は 8 KiB/side、diff engine 用の regular-file read は
256 KiB/side を超えない。
deadline 到達時は process group を停止し、partial response を返さず 502 にする。
patch 以外の stdout とすべての stderr は 10 MiB + 1 byte を検出した時点で
process group を停止して 502 にし、buffer も 10 MiB + 1 byte を超えない。

tracked と untracked を合わせた対象数は 500 files を上限とする。
metadata 出力は stream で NUL 区切りを数え、501 file を検出した時点で Git を
停止して 502 にする。
成功 response の `files` は必ず対象全件を含む。

patch の収集上限は 10 MiB(10,485,760 bytes)とする。
各 file の Git stdout は残り byte 数 + 1 byte まで読み、完全な
`diff --git` block を加えると上限を超える場合はその block を加えない。
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
上限以下なら、収集済みの完全な `diff --git` file block を path 順に加えた
各候補を JSON serialize し、body 全体が上限内に収まる最大の先頭部分を返す。
この計算は改行、control character、quote の JSON escape 後の UTF-8 byte 数を
使い、file block の途中では切らない。
上限で除外した file と後続の patch 対象 file は `patchIncluded: false`、
`omittedReason: "responseLimit"` とし、`truncated` を `true` にする。
`responseLimit` は収集済みの完全な file block だけに適用し、`binary`、
`tooLarge`、`collectionLimit` の既存理由を上書きしない。
両上限に該当する file は、先に適用した `collectionLimit` を報告する。
先頭の 1 block も収まらない場合は `patch` を空文字列にする。
body 全体が上限以下なら patch をそのまま返し、10 MiB 収集上限で設定済みの
`truncated` は保持する。
SPA は `truncated` が `true`、またはいずれかの `patchIncluded` が `false` なら、
review 対象が patch に揃っていないことを警告する。

10 秒、500 files、10 MiB、1 MiB、256 KiB は初期値である。
#578 の実装で調整する場合は同じ PR で本書、handler test、MSW fixture を更新し、
実装と wire contract を一致させる。
同じ test で、dirty submodule は無視して gitlink 変更を含めること、filter
command を一度も起動しないこと、非 UTF-8 path は 502、非 UTF-8 content は
`binary` になることを固定する。
preflight 後に live worktree と `.gitattributes` を変更しても filter command を
起動しないこと、256 KiB を超える tracked file も `tooLarge` になることを
同じ test で確認する。
clean と判定した tracked file を収集中に変更した場合は snapshot を取り直すこと、
partial clone の missing object で fetch または credential helper を起動せず
502 にすることも固定する。
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
取り直しになることも固定する。
base=`A`、index=`B`、worktree=`A` と、staged add 後に worktree から削除した
path は最終変更なしとして `files` に含めないことを確認する。
standard ignore を適用しない raw path 集合が 10 MiB を超える worktree でも、
ignore 後の対象が上限内なら 200 と対象 file だけを返すことを確認する。
`collectionLimit` の file は統計が `null` となり、1 MiB response 上限にも
該当する場合は `omittedReason: "collectionLimit"` を保持することを確認する。

handler が生成する次のエラー body は `{"error":"message"}` とし、
`application/json` と `Cache-Control: no-store` を付ける。

- `400 Bad Request`: identity query が欠落または不正で、`issue`/`task` の
  排他指定や worktree-local 行の `source` 必須条件を満たさない
- `404 Not Found`: identity が snapshot の 1 行に定まらない、worktree 記録が
  ない、cleanup 済み、または同じ git common dir の worktree として検証できない
- `502 Bad Gateway`: base/merge-base の strict 解決、filter attribute の検査、
  lazy fetch を使わない object 読み出し、snapshot の確定、unsupported file type、
  diff 収集 timeout、500 files/metadata 出力上限、metadata-only response 上限の
  超過、または diff engine の実行に失敗した

共通 middleware が生成する token 不一致の `403 Forbidden` と GET/HEAD 以外の
`405 Method Not Allowed` は既存どおり `text/plain` とし、上の JSON error
contract には含めない。
ただし `/api/diff` の全 response は middleware error を含めて
`Cache-Control: no-store` を付ける。

### 実装委譲事項

#576 は HTTP request/response の意味論、上限、エラーと dashboard の決定を固定する。
次の実装方式と内部上限は #577/#578 で決める。

- `GIT_INDEX_FILE` などで複製 index を live index から分離して読み出す方法
- common/worktree config の immutable snapshot と fingerprint
- request-private temporary directory を全終了経路で削除する方法
- admin/metadata file の総 byte 数、source 数、file type の内部上限

#577/#578 の実装判断が本書と食い違う場合は、同じ PR で本書、handler test、
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
