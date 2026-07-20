# ローカル diff レビューツール統合の予備調査

ステータス: 調査報告(予備調査)。実装なし。2026-07-20 実施。
候補ツールのドキュメント調査に加え、有力 3 ツールを合成リポジトリの
linked worktree + tmux 3.6a(macOS arm64、Node 24)で一時実行して検証した。
実装に進む場合の計画案は本書末尾にあり、issue 化はユーザー判断待ち。

## 背景

fanout の子セッションは `.fanout/worktrees/<slug>/` の worktree で実装を進める。
その成果を人間がレビューする面は現状 GitHub の PR 画面かエディタしかなく、
「エディタにも GitHub にも依存せず、手元で各セッションの diff を読んで
指摘を返す」面が欠けている。本書は、hunk などの既存 diff ビュアー兼
ローカルレビューツールでこの面を埋められるかを調べた記録である。

前提となる設計制約は [pr-review-visualization-v2.ja.md](pr-review-visualization-v2.ja.md)
の横断原則から引き継ぐ: diff 描画は既存ツールへ委譲し fanout は配線に徹する、
専用 Web ビューアは自作しない、必須依存は git/tmux/gh
(+ live 実行時の agent CLI)から増やさない、外部コマンド起動は
allowlist + opt-in なしに出荷しない。

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
- 未追跡ファイルは既定で含まれる(`--exclude-untracked` で除外する側の設計)
- `hunk session` CLI が双方向の還元ループを提供する。検証した往復:
  エージェント側から `session comment add --repo <path>`(TUI に
  Agent note がインライン描画される)、人間が TUI で `c` → Ctrl+S で
  付けたコメントを `session comment list --type user` で取得、
  `session review` で diff 構造 + コメントの統合ペイロード取得、
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
- コメント往復を CLI で検証: `difit comment add '{"type":"thread",
  "filePath":...,"position":{"side":"new","line":N},"body":...}'` で注入、
  `difit comment get` で人間のコメントを整形済みテキストで取得
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
- hunk と違い常駐デーモンを持たないため、レビュー中のエージェントからの
  遠隔操作はできない。還元は「終了時に一括」の一方向

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
起動コマンドの組み立てだけになる。初期 allowlist は hunk、difit、revdiff
の 3 つが妥当で、性格が住み分けている:

- **hunk**: TUI 常駐 + 双方向 session CLI。レビューと修正を並走させる
  使い方の本命。v0.x の若さだけが留意点
- **difit**: GitHub 風 Web UI。ブラウザで読みたい人向けで、
  merge-base 対応が最初から揃っている
- **revdiff**: 依存ゼロの単一バイナリ。Node を入れていない環境でも動く
  最小構成で、exit code による差し戻し分岐が簡潔

任意コマンド文字列をユーザーに書かせる設計(`diffViewerCommand` のような
自由文字列)は採らない。registry 外のコマンドは起動しない。設定キーは
repo config から書けないようにし(`RepoEditable: false`)、リポジトリを
クローンさせるだけで任意コマンドが起動される経路を塞ぐ。

検出と起動は PATH 上のグローバル実行ファイル(`hunk`、`difit`、`revdiff`)
に統一する。試用では npx を使ったが、npx 経由の起動は未導入 package の
ネットワークインストールが走るうえ、`exec.LookPath` による存在判定とも
両立しない。未導入のツールはメニューに出さないかエラーで案内し、
インストール自体はユーザーに委ねる(agent CLI と同じ扱い)。

## 実装計画案(issue 化はユーザー判断待ち)

### Phase 1: 起動導線(MVP)

worktree を選んでビュアーを開けるようにする。

- `internal/core/diffviewer`(新設): registry の純データ(ツール名、
  コマンド、引数組み立て)だけを置く。実行ファイル解決(`exec.LookPath`)は
  infra 側に置く。core の stdlib 純度検査(`internal/arch`)は `os/exec` を
  禁止しており、既存例外は `internal/core/agent` のみ。例外を増やさない
- `cmd/fanout/worktree_action.go`: `promptWorktreeAction` メニューに
  「3. Open diff viewer」を追加。既存の prefix+M ポップアップから到達
- `internal/ui/tui`: `Options` に `LaunchReview` フィールドを追加し、
  キー 1 つ(候補: `R`)を `openSelectedWorktreeShellCmd` と同型で配線。
  `help.go` に 1 行追加
- 起動形態: TUI 系(hunk / revdiff)は `tmuxrun.DisplayPopup`、difit は
  popup ではなく直接のプロセス起動(execx 系)にして stdout の JSON
  (port / pid)を回収し、URL を表示する。port と pid は記録して
  停止導線(または TTL)まで面倒を見る。`DisplayPopup` は子プロセスの
  stdout も終了コードも呼び出し元へ返さないため、popup 側の結果
  (revdiff の exit 10 と `-o` 出力)は `tui_popup.go` の result / done
  一時ファイル方式で回収する。popup 内で外部コマンドを呼ぶため
  同ファイルの PATH forward も通す
- hunk の session 識別: `--repo <path>` は同一 worktree に session が
  複数あると一致エラーになる。起動時に session ID を特定して pane /
  worktree と対応付けて記録し、二重起動は新規 session ではなく既存
  session の `reload` 再利用にする
- base 解決: merge-base を strict に解決する関数を `internal/infra/gitstat`
  に新設し、ビュアーへ SHA で渡す(hunk の two-dot 問題を fanout 側で
  吸収する)。表示統計用の既存 `diffBase` は base を解決できないとき
  `HEAD` へフォールバックする仕様なので流用しない。フォールバックすると
  空 diff を「変更なし」と誤読させるため、解決失敗はエラーにして
  レビューを中止する
- 未追跡ファイル: hunk は既定で含まれ、revdiff は `--untracked` を付ける。
  difit は `--include-untracked` が index を変更するため付けない。
  ただし警告表示だけでは新規ファイルを見ないまま承認できてしまうので、
  未追跡ファイルのある worktree(`git status --porcelain` で機械判定)では
  difit の起動を fail closed にし、hunk / revdiff への切替か、index 変更を
  理解したうえでの手動 `--include-untracked` を案内する
- `internal/infra/settings`: ビュアー選択キーを 1 つ追加
  (string、`RepoEditable: false`)。未設定時は registry の PATH 発見順

### Phase 2: 指摘の還元ループ

人間の指摘を子エージェントに渡す。CLI は転送のみで、LLM 文脈の解釈は
skill / briefing 側に置く(CLI は LLM を呼ばない鉄則の維持)。

- hunk: `session comment list --type user` の出力を該当ペインの受信箱へ
  転送する補助 verb(または skill 手順)。session の指定は Phase 1 で
  記録した session ID を使う(`--repo` 指定は複数 session で壊れる)
- difit: `comment get` の出力を同様に転送(起動時に記録した port を使う)
- revdiff: exit 10 を検知して `-o` の markdown を転送
- 配送経路: `fanout msg send` は SQLite への保存のみで、push 配送
  (`msg watch`、codex bridge)は `--team` セッションにしか存在しない。
  通常ペインへ届けるには、保存に加えて state ゲート付きの
  `fanout msg nudge` を打つか、`--team` を前提にする。人間(レビュアー)
  側の sender identity をどう表すかもこの Phase の設計項目
- v2 構想の案 4(Findings 裁可コンソール)が想定する「d = diff ビュアーへ
  パイプ」導線は、この Phase の自然な続きになる

### Phase 3(任意): dashboard からの導線

web dashboard に difit の URL リンクを出す程度に留める。dashboard の
read-only 境界(GET のみ、mutation なし)は変えない。ビュアー起動のような
副作用は TUI / tmux 側の導線に置いたままにする。

## 不採用の記録

- **本体取り込み(自作)**: 上記 (a) のとおり却下。判断根拠は v2 横断原則
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
- revdiff の還元は終了時一括のため、「レビュー中に随時差し戻す」体験は
  hunk / difit でしか成立しない
