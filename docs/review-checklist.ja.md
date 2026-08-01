# PR 前レビューチェックリスト

セッション履歴 102 件・CI 失敗 22 run・codex bot レビュー指摘の解析
(2026-07 の 170 件 = issue #373 のコメント、2026-08 の 239 件 =
`docs/review-scope.ja.md` のベースライン)で繰り返し現れたパターン。
PR を作る前に diff へ自問する。

## 対象を絞る

指摘の多くは「直すべきか」ではなく「そもそも対象か」で決まる。
`docs/review-scope.ja.md` の対応環境マトリクス外で初めて壊れる挙動は直さない。
SHA-256 リポジトリ、Git 2.38 以前、sparse / split index、`core.ignoreCase` や
`core.precomposeUnicode` の非既定値、Windows がこれに当たる。

## 頻出パターン

1. **失敗パスを成功扱いしない** — エラー時に requeue / cleanup / 状態遷移が
   継続するか。early return で後始末を飛ばしていないか。実測で最多クラス
   (30 件)。
2. **identity は state と照合してから使う** — pane / worktree を名前や位置で
   信用せず、`.fanout/state.json` の行と突き合わせる。
3. **git 出力のパースは NUL 区切り** — path を含む出力を行区切りで読むと、
   空白・改行・C-quoted パス・非 ASCII で壊れる。`ls-files` / `ls-tree` /
   `diff --numstat` / `status` / `worktree list` は `-z` を付けて NUL で割る。
   実測 22 件のクラス。
4. **git diff のエッジケース** — untracked / symlink / textconv / gitlink /
   file から directory への置換。前提にしている diff 形式が全ケースで
   成り立つか。
5. **context と deadline を末端まで渡す** — 総 timeout を持つ処理から呼ぶ
   git / gh / tmux は全て同じ context を受け取るか。途中で
   `context.Background()` に切り替えると打ち切りが効かない。`contextcheck` が
   機械的に見るが、固定 timeout の握り込みは見ない。実測 15 件のクラス。
6. **サイズ上限とバイト境界** — 上限適用の順序(変更確定の前か後か)、
   境界値の等号、上限で切った先の扱い。実測 21 件のクラス。
7. **カウンタ・予算のアカウンティング** — 二重計上、枯渇時の挙動、リセット
   条件。
8. **paginate / fetch 完了前に判定しない** — `gh api --paginate` や複数ページ
   取得の途中結果で分岐しない。
9. **表示幅** — マルチバイト・全角は byte 長ではなく表示幅で計算する
   (TUI / 整形出力)。
10. **英日ドキュメント同期** — `README*.md` / `site/content/docs/**` を触ったら
    ペアも更新する。

## 粒度

指摘密度は PR の大きさに強く相関する。実測では約 300 行の PR が 1 ラウンド
1〜3 指摘で収束したのに対し、+7000 行超の新規サブシステムは 1 PR で 26〜49 指摘・
最大 19 ラウンドかかった。

- 新規パッケージや state machine は、振る舞い単位に割って 1 PR あたり
  数百行規模に収める。`fanout` の親子 issue / `fanout plan` のタスク分割を
  その単位に合わせる。
- 設計ドキュメントを実装粒度で書かない。ADR / spike は決定記録であり、
  書き下すほどレビュアーが整合性を検査する面が増える(676 行の仕様書 1 本で
  35 指摘)。詳細は `docs/review-scope.ja.md`。

## 機械チェック

- 実装中は変更範囲のテストと Linter だけを回す。
- 最終候補を commit したら、`/post-work-review` または `$post-work-review` から `make check` を 1 回通す。
  レビューゲートをバイパスする場合は、PR 作成前に `make check` を直接実行する。
- `make test`、`make lint`、`make lint-web` は失敗の切り分けに使う。
  同じ最終ゲートで個別に重ねて実行しない。
- branch への `git push` は agent hook でゲートされる。clean tree での
  `make check` 成功が marker を書き、push はそのまま通る。deny されたら
  `make check` を通し直す。`--no-verify` での回避は禁止。緊急回避は
  `FANOUT_SKIP_PUSH_CHECK=1`。
- dry-run / status 出力を変えたら `FANOUT_GOLDEN_UPDATE=1 make test-tier2` で
  golden を regen して diff を目視する。

パターンが実態と乖離したら `/session-retro` の再発分類が検出するので、この
文書を更新する。
