# コード複雑度の自動チェック

エージェントが生成したコードは、深く入れ子になった防御的 if と、抽象化より
コピーを選ぶ重複に流れやすい。それを 3 層で捕まえる。

| 層 | 実体 | 効き方 |
|---|---|---|
| 予防 | `CLAUDE.md` / `AGENTS.md` の Complexity Budget | 書く前に方針を渡す |
| 即時是正 | `scripts/agent-complexity-on-edit.sh`(PostToolUse hook) | 編集直後に差し戻す。ローカルで無効化できるので**強制力はない** |
| ゲート | `.github/workflows/complexity.yml` | PR で新規の違反を落とす。強制力はここだけ |

3 層とも見るのは「その branch が新しく持ち込んだ複雑度」だけ。既存コードは非テスト
Go 関数の 10% がしきい値を超えており、全量スキャンにすると全 PR が落ちる。

判定は変更行ではなく **merge base との結果差分**で行う。complexity 系の linter は
違反位置を関数の**宣言行**として報告するため、変更行での絞り込みだけだと、既存関数の
宣言行を触らずに本体へ `if` を足したケースを取りこぼす (実測で gocognit の finding が
丸ごと落ちた)。merge base 側で同じ解析を回し、新しく現れたか値が悪化した finding だけを
残す。450 行の既存関数を 1 行触っただけで落ちない、という要件も同時に満たす。

## しきい値

数値のソースは 2 ファイルだけ。ここ以外に書くと「ローカルでは通るのに CI で
落ちる」が必ず起きる。

- Go: `.golangci-complexity.yml`
- TypeScript: `web/tools/complexity/eslint.config.js`

### Go(非テスト)

| 指標 | キー | 値 | 根拠(2026-08 時点の実測分布での位置) |
|---|---|---:|---|
| 認知的複雑度(主) | `gocognit.min-complexity` | 12 | p90=12。p50=3 / p75=6 / p95=19 / max=215 |
| 循環的複雑度(副) | `gocyclo.min-complexity` | 10 | p90=10。p95=13 / max=154 |
| 関数長 | `funlen.lines` / `.statements` | 32 / 32 | 本体行数 p90=32、文数 p90=32。max は 518 行 / 495 文。`ignore-comments: false` を明示 — v2.12.2 の既定は `true` で、コメント込みの実測値から採ったしきい値が実質的に緩くなる |
| ネスト | `nestif.min-complexity` | 5 | 既定値。AST 実測の最大ネスト深さは 4 で、目標の 3〜4 に既に収まっている |
| 重複 | `dupl.threshold` | 100 | 既定 150 では 1 組しか出ず、緩すぎた |
| 未使用の抑制 | `nolintlint.allow-unused: false` | — | 効いていない `//nolint:gocognit` 等を検出する。`.golangci.yml` 側は複雑度 linter が無効なので、あちらの `nolintlint` はこれらの directive を見ない (実測で素通しを確認) |

### TypeScript

`.tsx` は循環的複雑度だけ緩める。JSX の `&&` と三項演算子が分岐として機械的に
数えられるため(実測 p99: `.tsx` 23 / `.ts` 13)。認知的複雑度では逆に `.tsx` の
ほうが低い(p99: 14 / 18)— ロジックが hooks と lib に寄っているから。それでも
`.tsx` を `.ts` より厳しくはしない。

| 指標 | rule | `.ts` | `.tsx` | 根拠 |
|---|---|---:|---:|---|
| 認知的複雑度(主) | `sonarjs/cognitive-complexity` | 7 | 8 | `.ts` は p90=7。`.tsx` は p90=5 だが `.ts` より厳しくしないため p95=8 |
| 循環的複雑度(副) | `complexity` | 8 | 10 | `.ts` p90=7 の直上。`.tsx` は JSX ぶん +2 |
| 関数長 | `max-lines-per-function` | 60 | 80 | `.ts` p95=38 / p99=83。`.tsx` は React コンポーネント本体(p95=61)を考慮 |
| 文数 | `max-statements` | 10 | 12 | `.ts` p90=7 / p95=11、`.tsx` p90=4 |
| ネスト深さ | `max-depth` | 3 | 3 | 実測 max は `.ts` 3 / `.tsx` 2。導入時点で違反ゼロ |
| 引数 | `max-params` | 3 | 3 | 実測 max 3 / 2。違反ゼロ |
| コールバック入れ子 | `max-nested-callbacks` | 3 | 3 | 実測 max 3 / 3。違反ゼロ |
| 重複 | `sonarjs/no-identical-functions` | 有効 | 有効 | 既定(3 行以上)。違反ゼロ |

助言しきい値はブロック値の 2/3 で、hook が実行時に導出する。数値を別ファイルへ
複製しないこと。Go は `.golangci-complexity.yml` を awk で読んで
`.golangci-complexity-advisory.yml`(gitignore 済み)を生成し、TypeScript は
`FANOUT_COMPLEXITY_ADVISORY=1` で同じ config を再解釈する。

## 除外対象

**この表が正典。**実装は 3 箇所にあり、どれも同じ集合を持つ。片方だけ増やさないこと。

| 対象 | 効かせている場所 |
|---|---|
| `**/*_test.go` | `.golangci-complexity.yml` の `run.tests: false`、hook の `eligible()` |
| `mock_*.go` / `*_mock.go` / `vendor/` | `.golangci-complexity.yml` の `exclusions.paths`、hook の `eligible()` |
| `Code generated ... DO NOT EDIT` | `.golangci-complexity.yml` の `exclusions.generated: lax` |
| `.dmux/` `.fanout/` `.cache/` `tests/tmp/` | `.golangci-complexity.yml` の `exclusions.paths` |
| `web/src/**/*.test.ts(x)` | `eslint.config.js` の `EXCLUDED`、hook の `eligible()`、`complexity-branch.sh` の `TS_EXCLUDE` |
| `web/src/**/*.spec.ts(x)` | 同上。vitest は既定で `.spec` も収集する |
| `web/src/test/**` | 同上 |
| `web/src/**/*.stories.ts(x)` | 同上 (Storybook) |
| `web/src/**/__mocks__/**` | 同上 |
| `web/src/**/*.d.ts` `*.gen.ts(x)` `generated/**` | 同上 (生成コード) |

Go / TS とも、生成コード・ベンダリングされたコード・モック・Storybook は**今この
リポジトリに 1 つも無い**(`//go:generate` 0 件、`Code generated ... DO NOT EDIT`
0 件、`vendor/` なし、`__mocks__` なし、`*.stories.tsx` なし)。それでもパターンを
先に入れてあるのは、最初の 1 つが生まれた瞬間にゲートが誤爆しないようにするため。

## TypeScript スタックが隔離されている理由

`typescript-eslint` は TypeScript 7.0 でハードエラーになる
(`typescript-eslint does not support TS 7.0`、upstream は
typescript-eslint#10940)。`web/` は `tsc --noEmit` のために `typescript@7` を
使い続けるので、ESLint 一式だけを `web/tools/complexity/` という独立した
pnpm workspace パッケージに閉じ、そこだけ `typescript@6` を直接依存に持たせて
いる。pnpm の `overrides` では peer 解決を変えられないため、パッケージ分離が
唯一の手段。

この隔離パッケージの `typescript` を 7.x に上げると ESLint 全体が動かなくなる。
typescript-eslint が TS 7 に対応したら統合できる。

通常の lint は今までどおり oxlint が担当する(`make lint-web`)。oxlint には
認知的複雑度と重複コード検出のルールが無いため、複雑度だけ ESLint に寄せてある。

## CI の挙動

`complexity.yml` は `pull_request` で 2 job 走る。

- **Added complexity** — 判定の実体は `scripts/complexity-branch.sh` で、
  `make complexity` と同じもの。Go は作業ツリー全体と `git archive` で取り出した
  merge base 側をそれぞれ解析し、TypeScript は変更された `.ts` / `.tsx` だけを
  現在と `--stdin` 経由の merge base 版で解析する。両者を
  `.github/scripts/complexity-diff.mjs` で突き合わせ、悪化した finding だけを残す。
  結果は SARIF で code scanning に上げ、残れば job を落とす。
- **Suppression watch** — この PR で追加された `//nolint` と `eslint-disable` を
  sticky コメントで可視化する。**初期は警告のみで job を落とさない**。頻度を
  観測してからブロックへ昇格させる。未使用の抑制は Go は
  `.golangci-complexity.yml` の `nolintlint`、TS は ESLint の
  `reportUnusedDisableDirectives` が拾い、どちらも Added complexity 側で落ちる。

`fetch-depth: 0` が要るので `test.yml`(shallow checkout の 5 job)には相乗り
させず独立させてある。差分ベースで PR に判定を出すワークフローは
`review-risk.yml` が前例。

**golangci-lint の `--new-from-merge-base` / `--new-from-rev` は使っていない。**
実測で、コミット済みの差分に対してすら違反を全部落としてしまう環境があり
(`diff: 3/0`)、ゲートが黙って無効化される事故のほうが高くつく。hook 側では別の
理由でも使えない — これらのフラグはコミット済みリビジョン間の差分しか見ず、hook が
相手にする未コミットの編集は最初から視界に入らない。

**golangci-lint のキャッシュは実行ごとに分ける。** 木をまたいでキャッシュが衝突し、
`make lint` (`.golangci.yml`、複雑度 linter 無効) と共有するとベースラインが丸ごと
`0 issues` になり、現在と merge base で共有すると 2 回目の結果が 568 件から 10 件へ落ちる
(いずれも実測)。空のベースラインは既存違反を全部「新規」に化けさせるので、
`complexity-branch.sh` はそれを検出したら fail closed で止まる。

**merge base の木はリポジトリの外へ展開する。** 中に置くと golangci-lint が
module / VCS root として外側のリポジトリを見つけ、SARIF の uri が `../../../cmd/...` に
なってベースラインとの突き合わせが全部外れる。

**判定対象はこのゲートが持つルールだけ。** `complexity-diff.mjs` の `OWNED_RULES` に
無い finding は捨てる。ESLint は未登録ルールの disable コメント
(`react-hooks/exhaustive-deps` など) を error で返すので、素通しにすると正当な
コメントを 1 行足しただけでゲートが落ちる。

**`dupl` と `no-identical-functions` は件数で比べる。** これらのメッセージ先頭の数字は
指標ではなくソース行番号で、値として比べると既存の重複より前に行を挿入しただけで
悪化に見える。他のルールの指標値もメッセージ中の位置がばらばらなので、
`VALUE_PATTERNS` でルール別に抽出する — 「最初の数字」だと
`` `if len(nums) == 0` has complex nested blocks (complexity: 5) `` から 0 を拾う。

**リネームは旧パスへ寄せる。** ファイルパスだけでなくメッセージ本文にも効かせる
(`dupl` は相方のパスを本文に書くので、片方を `git mv` すると相方まで新規扱いになる)。
ただし「今と同じ条件で測れない場所」からの rename ではベースラインを作らない —
除外対象からの移動 (`foo.test.ts` -> `foo.ts`)、`web/src` 外からの移動、拡張子の
変更 (`.tsx` -> `.ts`) が該当する。旧内容を別の条件で測ると、いま超過している分まで
「既存」として相殺されてしまう。

**ESLint の exit 1 は「違反あり」であって失敗ではない。** `&&` で繋ぐと、ベース側に
既存違反があるファイルほどベースラインを捨てることになり、この仕組みの主目的を
取りこぼす。SARIF が生成されたかどうかで判定する。

**未使用の抑制コメントは SARIF の `results` に入らない。** ESLint は
`invocations[].toolConfigurationNotifications` に置くので、そちらも読んで
`eslint-unused-disable` として finding に混ぜる。

## identity の限界 (意図的なトレードオフ)

ベースラインとの突き合わせは「ファイル・ルール・数字を除いたメッセージ」を鍵にする。
行番号を鍵に含めないのは、含めると 1 行挿入するたびに既存 finding が全部「新規」に
化けるため。その代償として次の 2 つは取りこぼす。

- **同一ファイル内の匿名関数の相殺**: base の `Arrow function has too many statements`
  を消して別の場所に新しい違反関数を足すと、base の値が新しい値を吸収する。
- **関数名の変更**: しきい値超過済みの `Foo` を本体そのままで `Bar` に改名すると、
  メッセージ中の関数名が変わるため新規違反として残る。

どちらも「行番号を鍵に入れて誤検知を量産する」よりましと判断した。SonarQube の
ような issue tracking (コードハッシュによる追跡) を持ち込めば解消できるが、この
規模の仕組みには重すぎる。

required status checks は現状 1 つも設定されていない。ゲートに強制力を持たせる
には GitHub 上で ruleset に追加する必要がある。

## ローカル実行

```
make complexity                      # この branch が増やした複雑度(CI と同じ判定)
COMPLEXITY_BASE=origin/dev make complexity
```

終了コードは 0 = 新規の違反なし、1 = 新規の違反あり、2 = 解析器の異常。違反と
解析失敗は必ず区別する — 混ぜるとゲートが黙って無効化される。判定は working tree を
終点に取るので、commit 前でも未コミットの変更を見る。

`make check` には**入れていない**。既存コードの 10% が違反しているため、全量
スキャンをゲートに繋ぐと `check-marker` が書かれず push が全面的に止まる。

hook を一時的に黙らせるには `FANOUT_SKIP_COMPLEXITY=1`。差し戻し回数の上限は
`FANOUT_COMPLEXITY_MAX_RETRIES`(既定 3)。

## 段階的な引き下げ

初期値は既存コードの p90 に置いた。目標(認知的複雑度 15 前後・関数長 80 行前後)
より既に厳しいので、しきい値を下げていく運用ではなく、**既存のホットスポットを
減らす**運用になる。

1. `golangci-lint run -c .golangci-complexity.yml` を引数なしで回して全違反を出す
   (ベースライン比較を挟まない)。`cd web && pnpm run complexity` が TS 側。
2. 上位から 1 つずつ潰す。触るついでに下げるのが基本で、複雑度改善だけの PR は
   レビュー負荷に見合わない。
3. 違反がしきい値の周辺まで減ったら、`.golangci-complexity.yml` と
   `eslint.config.js` の数値を 1 段下げてこの表を更新する。
4. 抑制コメントの件数が増えているようなら、しきい値ではなく方針を疑う。
   Suppression watch job をブロックへ昇格させる判断材料もそこ。

## 導入時点で超過していた箇所(2026-08)

修正せずに残してある。負債として把握するための一覧。

| 対象 | 違反数 | 突出しているもの |
|---|---:|---|
| Go 認知的複雑度 > 12 | 167 関数(10.0%) | `internal/ui/tui/update.go` の `(model).Update` = 215 |
| Go 循環的複雑度 > 10 | 172 関数(8.2%) | 同上 = 154 |
| Go 関数長 > 32 行 | 207 関数(9.9%) | 同上 = 518 行 |
| Go nestif ≥ 5 | 17 箇所 | `internal/infra/gitstat/gitstat.go:357` = 9、最大 16 |
| Go dupl(threshold 100) | 9 箇所 | `internal/app/run/plancmd.go` ⇄ `internal/app/run/report.go` |
| `.ts` 認知的複雑度 > 7 | 7 関数 | `web/src/features/filter/filter.ts:47` = 52(循環的複雑度も 69) |
| `.tsx` 認知的複雑度 > 8 | 5 関数 | `web/src/features/filter/FilterDropdown.tsx:123` = 16 |
| `.ts` / `.tsx` 関数長 | 5 / 8 関数 | `web/src/features/diff/DiffOverlay.tsx:178` = 450 行 |
| `.ts` / `.tsx` 文数 | 10 / 4 関数 | `App.tsx:131` と `DiffOverlay.tsx:178` = 各 50 |
| TS のネスト深さ・引数・コールバック入れ子 | 0 | — |

Go 側の上位は `internal/ui/tui/update.go` の bubbletea `Update` ループに集中して
いる。`.golangci.yml` が `funlen` / `gocognit` / `cyclop` を不採用にした理由も
これで、判断自体は今も妥当。差分ベースにすることで、その塊を触らずに新しい
コードだけ縛れるようにした。
