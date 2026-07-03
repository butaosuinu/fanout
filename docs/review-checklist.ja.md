# PR 前レビューチェックリスト

過去のセッション履歴 102 件・CI 失敗 22 run・codex bot レビュー指摘 170 件の
解析(2026-07、issue #373 のコメント)で繰り返し現れたパターン。PR を作る前に
diff へ自問する。

## 頻出 7 パターン

1. **失敗パスを成功扱いしない** — エラー時に requeue / cleanup / 状態遷移が
   継続するか。early return で後始末を飛ばしていないか。
2. **identity は state と照合してから使う** — pane / worktree を名前や位置で
   信用せず、`.fanout/state.json` の行と突き合わせる。
3. **git diff のエッジケース** — untracked / symlink / C-quoted パス /
   textconv。前提にしている diff 形式が全ケースで成り立つか。
4. **カウンタ・予算のアカウンティング** — 二重計上、枯渇時の挙動、リセット
   条件。
5. **paginate / fetch 完了前に判定しない** — `gh api --paginate` や複数ページ
   取得の途中結果で分岐しない。
6. **表示幅** — マルチバイト・全角は byte 長ではなく表示幅で計算する
   (TUI / 整形出力)。
7. **英日ドキュメント同期** — `README*.md` / `site/content/docs/**` を触ったら
   ペアも更新する。

## 機械チェック

- `make lint` と `make test` を回す。`web/` を触ったら `make lint-web` も。
- dry-run / status 出力を変えたら `FANOUT_GOLDEN_UPDATE=1 make test-tier2` で
  golden を regen して diff を目視する。
