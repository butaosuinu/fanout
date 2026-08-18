# PR 前レビューチェックリスト

PR を作る前に、変更した契約と失敗経路を diff 全体で確認する。

## 集計の基準

2026-08-01 時点の作成者本人による直近 20 PR では、Codex Code Review
（`chatgpt-codex-connector[bot]`）の独立した top-level inline finding が 173 件、
review summary と人間の返信を含む関連レコードが 405 件あった。
4 PR が 131 finding を占め、Herdr の状態管理と Git およびファイル差分処理の
2 系統で 159 finding（91.9%）を占めた。
同じ commit SHA への review でも新しい finding が出た例があるため、同一 HEAD の反復は
収束の証拠にしない。

issue #373 の旧集計は、セッション履歴 102 件、CI 失敗 22 run、codex bot finding
170 件だった。
対象期間と数え方が違うため、新集計へ加算しない。

- **finding**：connector が投稿した独立した top-level inline comment。
  review summary と返信は含めない。
- **review wave**：明示的な 1 回の `pr-watch` 起動中に、current-head review batch の
  actionable finding を 1 commit、1 push で修正した単位。
  finding を根拠付きで棄却する返信だけなら wave に含めない。
- **same-head request**：HEAD を更新せずに明示的な review を再要求した回数。
- **current-head approval**：最終 HEAD の push 後、次の HEAD へ進む前に観測した
  設定済み actor の `+1`。

## 頻出パターン

1. **状態遷移を表にする**：初回、再試行、期限切れ、完了後に加え、取消、復旧、
   replay で同じ不変条件が成り立つか。
2. **結果不明の副作用を再送しない**：timeout や切断後に外部変更の成否を確認せず、
   同じ mutation を繰り返していないか。
3. **identity と ownership を state から決める**：pane や worktree の名前と位置を
   信用せず、persisted state、binding、fencing を外部操作前に照合する。
4. **全 entrypoint と consumer を追う**：同じ契約を使う別経路、linked worktree、
   synthetic parent、cleanup と recovery を同じ修正に含めたか。
5. **適用される Git とファイルの契約を確認する**：変更した経路について、既存テスト、
   issue の acceptance criteria、明示契約、required safe rejection を満たすか。
   約束していない環境への新しい対応は要求しない。
6. **カウンタと予算を一度だけ計上する**：二重計上、枯渇時の挙動、reset 条件が
   全分岐で一致するか。
7. **paginate と fetch の完了後に判定する**：途中ページや部分取得を全件として扱って
   いないか。
8. **表示幅を byte 長で測らない**：TUI と整形出力で、マルチバイト文字と全角文字の
   表示幅を使っているか。
9. **契約文書を実装と照合する**：`README*.md` と `site/content/docs/**` の英日ペア、
   schema、コマンド例、既定値を正典と突き合わせたか。
10. **失敗を「対象なし」と同一視しない**：外部コマンドの非ゼロ終了や timeout を、
    成功時の「0 件」「完了」と同じ分岐に落としていないか。空出力は終了コードを
    見てから解釈し、意図した fail-open は根拠を明示する。

## 機械チェック

- 実装中は変更範囲のテストと Linter だけを回す。
- 最終候補を commit したら、`/post-work-review` または `$post-work-review` から `make check` を 1 回通す。
  レビューゲートをバイパスする場合は、PR 作成前に `make check` を直接実行する。
- `post-work-review` は現在の target 全体を読み、P0-P2 相当の finding を同根ごとに
  一括で返す。style、推測、既存問題は修正対象にしない。
- finding は変更または review 対象の各 path について、PR の base 側で適用される
  instruction chain と `## Code Review Rules` を通常の優先順位で解決して裁定する。
  到達不能な環境、明示された non-goal、または契約を満たす証拠がある finding は、
  根拠を返信して棄却する。
  全 finding を根拠付きで棄却できれば、修正せず clean として扱う。
- `pr-watch` は completed review と現在 HEAD の未解決指摘を current-head review batch に
  集め、同根の箇所を 1 commit で修正する。同じ SHA へ review を再要求しない。
- `pr-watch` の connector repair-wave counter は明示的な各起動で 0 から始め、1 起動あたり
  最大 3 wave で止める。
  同じ起動内の継続監視は counter を保持し、後の明示的な起動は 0 から始める。
- `make test`、`make lint`、`make lint-web` は失敗の切り分けに使う。
  同じ最終ゲートで個別に重ねて実行しない。
- branch への `git push` は agent hook でゲートされる。clean tree での
  `make check` 成功が marker を書き、push はそのまま通る。deny の理由が
  marker 不一致なら `make check` を通し直す。commit や rebase と連結した
  push は連結だけで deny され、連結された ref 変更も未実行のまま止まる。
  deny された ref 変更 (commit / rebase)、`make check`、push を
  1 コマンドずつに分けて再実行する。
  `--no-verify` での回避は禁止。緊急回避は `FANOUT_SKIP_PUSH_CHECK=1`。
- dry-run / status 出力を変えたら `FANOUT_GOLDEN_UPDATE=1 make test-tier2` で
  golden を regen して diff を目視する。

## 改善の判定

配布後の作成者本人による次の 20 PR を同じ定義で数える。

- same-head request を 0 件にする。
- agent-driven repair を `pr-watch` 1 起動あたり最大 3 wave で止める。
- 1 回の起動内で 1 wave 以内に収束する PR を増やす。

パターンが実態と乖離したら、`/session-retro` の再発分類を基にこの文書を更新する。
