# アーキテクチャテストツールの調査と不採用の決定(2026-07)

外部のアーキテクチャテストツールは採用しない。層ルールの CI 強制は
`internal/arch/arch_test.go` の手書きテストを継続する。この文書は 2026-07 の
調査に基づく決定記録で、再評価の条件を末尾に置く。

## 前提

層ルールの正典実装は `internal/arch/arch_test.go`(491 行・stdlib のみ・
`go test ./...` に同梱)。8 テストが次を強制する。

| テスト | 強制する内容 |
|---|---|
| `TestLayerImportDirection` | 層方向(core→core、app→core/app/infra、infra→core/infra、ui/cmd→全層)。`legacyDirectionAllowlist` の例外は未使用になると失効エラー |
| `TestCorePurity` | core の非テストファイルに stdlib denylist(7 パッケージ・完全一致。一覧の正典は `docs/architecture.ja.md` の層表)+ third-party 禁止を適用(`_test.go` は対象外)。`core/agent`・`core/planspec` に親ディレクトリ継承の例外 |
| `TestToolsStdlibOnly` | tools/ は stdlib のみ(モジュール内 import も禁止) |
| `TestPackageMainOnlyInCmd` | package main は cmd/fanout と tools/ 配下のみ。`cmd/...` の被 import 全面禁止 |
| `TestInternalTreeShape` | internal/ 直下は core/app/infra/ui/arch のみ(import graph でなく実ディレクトリを検査) |
| `TestAllPackagesClassified` / `TestExplicitLayerMapIsCurrent` | 全パッケージの層分類強制と stale エントリの検出 |
| `TestScanSanity` | スキャンの空回り防止(internal/・cmd/・tools/ の 3 ツリーを見ていることの保証) |

depguard は golangci-lint v2 導入(#191)で「コミュニティ合意でノイズ」として
不採用済み(`.golangci.yml` ヘッダに記録)。今回は「良いツールがあれば手書き
テストを置換する」前提で、depguard も含めて Go のアーキテクチャテストツールを
調査した。

## 調査結果

| ツール | 状態(2026-07) | 層方向 | stdlib 制約 | 判定 |
|---|---|---|---|---|
| [go-arch-lint](https://github.com/fe3dback/go-arch-lint) v1.16.0 | 514★・活発 | ○ | ×(`mayDependOn` / `canUse` は component / vendor のみ。stdlib は常時許可) | `TestCorePurity` を表現できない。依存許可を空にした component は stdlib のみ許可になるため `TestToolsStdlibOnly` は置換可能(scanner は `_test.go` 含む全 `.go` を parse) |
| [arch-go](https://github.com/arch-go/arch-go) v2.1.2 | 266★・活発 | ○ | ○ | マッチした全ルールを AND 適用し、上書き・除外機構がない(arch-go リポジトリの `internal/verifications/dependencies/verifications.go` で確認)。`core/agent` 等のサブパッケージ例外を表現できない。パッケージを `Tests: false` で読むため `_test.go` の import も検査対象外 |
| [depguard](https://github.com/OpenPeeDeeP/depguard) v2 | golangci-lint 同梱 | ○ | ○(`$gostd`。`$` 末尾で完全一致指定可) | 候補中で表現力が最も高く、追加依存もない。それでも部分置換にとどまる(「検討した代替案」参照) |
| GoArchTest v0.1.0 / go-arctest / archtest / cht-go-lint | 未成熟または停滞 | — | — | 対象外 |
| gomodguard | 活発 | ×(module 単位のみ) | — | 対象外 |

## どのツールも置換できないもの

- 未使用例外の自動失効(`legacyDirectionAllowlist` / `explicitLayers` の stale 検出)。手書きガードで最も価値の高い機能
- `TestCorePurity` の例外(`core/agent`・`core/planspec`)。arch-go は denylist 本体を書けても、例外を持つパッケージだけ緩める手段がない
- `TestInternalTreeShape`(import graph でなく実ディレクトリの検査)
- `TestPackageMainOnlyInCmd` の package main 配置検査。`cmd/...` の被 import 禁止は import ルールとして表現できるが、package 節の検査は import linter の範囲外
- `TestScanSanity`
- `tools/reviewrisk` の docsync(`docs/architecture.ja.md` のパッケージ表 ↔ `rules.go`)

完全に置換できるのは go-arch-lint による `TestToolsStdlibOnly` 1 つ(依存許可を
空にした component は stdlib のみ許可になり、scanner は `_test.go` を含む全
`.go` を parse する)。層方向も go-arch-lint なら `_test.go` 込みで表現できるが、
stale allowlist の自動失効がない。arch-go はさらに狭く、`packages.Load` の
build 対象 import だけを見るため、`_test.go` に加えて build tag や OS suffix で
現在の GOOS/GOARCH から外れるファイルも検査されない(現行 `scanRepo` は build
constraint を評価せず全 `.go` を parse する)。

## 判断

不採用。理由:

1. 置換範囲が小さい。完全置換できるのは go-arch-lint による `TestToolsStdlibOnly` 1 つで、層方向は stale allowlist の自動失効を失う劣化置換、core 純度はどのツールも例外を表現できない(arch-go は `_test.go` と build 対象外ファイルも検査しない)。stdlib 制約まで書ける depguard でも部分置換にとどまる(「検討した代替案」参照)。残る 7 テストの手書きは消えないため、ルール系統が Go map と YAML の 2 系統に分裂してドリフトのリスクだけ増える
2. arch-go は例外セマンティクスを表現できない。broad ルール側から例外パッケージを除外列挙して模倣すると、新規パッケージが自動でルールに入る現行の性質が壊れる
3. 依存最小主義と合わない。必須依存を git/tmux/gh(+ live 実行時に選んだ agent CLI)に絞り、lint は pinned golangci-lint と shellcheck だけという方針に対し、go-arch-lint / arch-go はバイナリ pin か go.mod のテスト依存を増やす(golangci-lint 同梱の depguard は増やさないが、置換範囲は前項と「検討した代替案」のとおり)。現状の CI 追加コストはゼロ(`go test ./...` に同梱)
4. `internal/arch` は review class H・reviewrisk S4(触れたら critical)の保護対象。ルールを YAML へ移すとガード定義が S4 の監視対象外に出て、`rules.go` / `signals.go` の保護面拡張が別途必要になる
5. 現行テストの失敗メッセージは修正方法まで明示しており、YAML の宣言性に置き換える利得がない

検討した代替案も不採用:

- ルール表のデータファイル(YAML 等)外出し — Go map は型検査・enforcement との同居・S4 保護をそのまま得られる。外出しはパーサ追加と保護面分裂のコストしかない
- depguard の再有効化 — 候補の中では表現力が最も高い。`$` 末尾の完全一致で「`net` 禁止・`net/url` 許可」を再現でき、`files` の否定 glob と `$test` で `core/agent` / `core/planspec` の例外もテスト込みの検査も書け、pinned golangci-lint 同梱で追加依存もない。それでも不採用にする: 未使用例外の自動失効・実ディレクトリ検査・package main 配置・空回り防止は表現できず、`files` glob に載らない新規パッケージを黙って素通しする(`TestAllPackagesClassified` 相当の fail-closed がない)。部分置換で 2 系統分裂が残る点は他ツールと同じで、#191 の「ノイズ」判断を覆す利得がない

## 再評価の条件

いずれかを満たしたら再調査する。

- arch-go が複数ルール一致とサブパッケージ例外のセマンティクスを文書化・保証した
- 未使用例外の自動失効に相当する機能を持つツールが現れた
- `internal/arch` のルール追加が続き、手書き維持が負担になった(現状 491 行で安定)

## 参考

- `internal/arch/arch_test.go` — 層ルールの正典実装
- `docs/architecture.ja.md` — 層の責務と依存ルール、depguard 不採用の記述
- `.golangci.yml` ヘッダ — 不採用 linter 一覧。この文書はその arch テスト版
