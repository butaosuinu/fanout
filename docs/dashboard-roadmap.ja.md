# Web ダッシュボード開発ロードマップ

2026-08-15 の判定。web ダッシュボード系 4 epic(#675 技術スタック導入 / #659
自動再起動 / #628 行コメントレビュー / #321 Wails アプリ化。子 issue 計 29 本・
全 OPEN)の優先順位と並列 wave 計画をここに置き、実装詳細は各 issue に書く。
全体の開発方針は [roadmap.ja.md](roadmap.ja.md)。同じ判定を図示した人間向け
ビジュアル版が [assets/dashboard-roadmap.html](assets/dashboard-roadmap.html)
(GitHub 上ではソース表示になるため、checkout してブラウザで開く)。

## 優先順位

| 順位 | epic | 子 | 判定根拠 |
|---|---|---|---|
| 1 | #675 技術スタック導入(jotai / TanStack Router / net/http 整理 / tygo) | #676-683 | #628 の着手ゲート(S-1〜S-3 と F-1〜F-3 の完了)であり、今後の画面追加すべての土台。S / F の 2 トラックで並行できる |
| 2 | #659 ダッシュボード自動再起動 | #655-658 | 2026-08-14 実測の運用痛点(install 後も旧バンドルを配信し続け、手動入れ替えは 6 手順)。4 子と小さく `web/` を触らないため #675 と並走できる。#657 の restart 導線は #658 の前提(blocked-by 登録済み)。#321 の #316 も #659 完了後の着手が望ましい(本書の判断。#316 の blocked-by には未登録) |
| 3 | #628 diff 行コメント双方向レビュー | #518, #663-671 | 機能価値は最大だが着手ゲートが #675 側にある。#668 の POST carve-out は #676 の route テーブルへの 1 行追加になる設計で、待つ方が総工数が下がる |
| 4 | #321 Wails ネイティブアプリ化 | #314-320 | オプション配布(本体に同梱しない)で緊急性がない。Wails v3 alpha と notarization($99/年)のリスクを抱える。#314 が `cmd/fanout/dashboard.go` で #655 / #657 と交差し、#316 のライフサイクル管理は #659 の URL 不変+レジストリ完了後の方が手戻りが少ない |

## 並列性

epic ごとに主戦場が分かれているのが並走の根拠。

- #675-S(#676 → #678 → #680): `internal/ui/dashboard/` に routes.go / static.go
  を新設し server.go を縮小する。cmd 側は触らない。ただし #680 だけは生成物の
  反映で `web/src/transport/`(generated/ 新設 + types.ts の façade 化)と
  Makefile(`gen-types` ターゲット)にも触れる
- #675-F(#677 → #679 → #681 → #682 → #683): `web/src` と
  `web/package.json` + lockfile(#677 / #682 が依存を追加)
- #659: #655 = runfile.go + `cmd/fanout/dashboard.go`、#656 = infra レジストリ
  (atomicfs)。`web/` なし
- #628: Go は core / app / infra の新設パッケージ中心。web は features 側の
  store 新設(#664)から始め、App 層への組み込みは #667 以降
- #321: `app/` 独立 go.mod でほぼ隔離。本体を触るのは #314 のみ

rebase 前提ペア — 並走できるが、先にマージされた側に他方が rebase する:

- #655 ↔ #676: 同一パッケージの別ファイル。server.go の lifecycle 部で軽く交差する
- #664 ↔ #682: どちらも `web/src`。#682 は app 層、#664 は features 側で領域は分かれる
- #680 ↔ #681: どちらも `web/src`(#680 は transport、#681 は features/app)
- #658 ↔ #680: どちらも Makefile にターゲットを足す(#658 は install 末尾の
  restart 呼び出し、#680 は `gen-types`)
- #667 ↔ #683: どちらも DiffOverlay の開閉まわり(#667 は Escape 階層と再 mount、
  #683 は開状態の URL 直列化)

着手ゲート:

- #628 は S-1〜S-3(#676 / #678 / #680)と F-1〜F-3(#677 / #679 / #681)の完了後
  (#675 の Coordination notes に明記。#628 側には書かれていない)
- #321 は #659 完了後。#315 spike の結果次第で中止もあり得る
- この 2 つの epic 間ゲートは blocked-by に登録されていない(登録済みなのは各
  epic 内の依存のみ)。各親 issue の Suggested command を無条件に打つとゲート前
  でも起動されるため、着手時期は本書の wave 表に従う
- class H に触れる PR は人間レビュー必須。#655(runfile)/ #657 / #676(route
  表)/ #668(POST carve-out)に加え、#678(requireToken の 403)・#663
  (reviewstore 新設)・#666(main.go dispatch)・#670(briefing)・#680
  (peek/plan/diff の rename)も H 面に触れる。Makefile / `web/package.json` /
  CI workflow を触る PR(#658 / #677 / #682 / #315 / #319)も reviewrisk 上は
  H 判定になる。この列挙は例示で、正は PR ごとの reviewrisk(`make
  review-risk`)

## Wave 計画

最大 4 セッション並列。wave の起動は親 epic ごとに `/fanout #<parent>
--unblocked-only` を 1 回ずつ(2 epic にまたがる wave 1〜5 は 2 回)。epic 内の
blocked-by が wave 内の起動対象を絞る。

| phase | wave | 並列起動 | 備考 |
|---|---|---|---|
| 1 | 1 | #676 + #677 + #655 + #656 | 3 トラック同時開始 |
| 1 | 2 | #678 + #679 + #657 | |
| 1 | 3 | #680 + #681 + #658 | 完了で #628 の着手ゲートが開く |
| 2 | 4 | #518 + #663 + #664 + #682 | #664 ↔ #682 は rebase 前提 |
| 2 | 5 | #665 + #666 + #667 + #683 | |
| 3 | 6 | #668 | 不変条件改訂の単独 wave。人間レビュー必須 |
| 3 | 7 | #669 + #670 | 双方向レビューの機能一式が完了 |
| 3 | 8 | #671 | 任意。#669/#670 の実運用フィードバック後に Go/No-Go(issue 本文の取り決め) |
| 4 | A〜D | #314 → #315 → #316 + #317 + #318 → #319 + #320 | #659 完了後・任意。#315 spike で Go/No-Go |

phase 2 で #628 wave1 と #675-F の残り(#682 / #683)を並走させるのは、#664 が
store 新設で App 層をほぼ触らないため。#667 以降の UI 組み込みは #682 の Router
導入後に乗る。

#628 本文の wave 記載(wave 3 = #668 + #669)は #669 の blocked-by(#668 完了が
条件)と両立しないため、本書は #668 を単独 wave に置いた。wave 6〜8 は本書が正。
