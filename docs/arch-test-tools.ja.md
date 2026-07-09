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
| `TestCorePurity` | core の stdlib denylist(`os` / `os/exec` / `net` / `net/http` / `syscall` / `database/sql` / `io/ioutil`。完全一致 — `net/url` は許可)+ third-party 全面禁止。`core/agent`・`core/planspec` に親ディレクトリ継承の例外 |
| `TestToolsStdlibOnly` | tools/ は stdlib のみ(モジュール内 import も禁止) |
| `TestPackageMainOnlyInCmd` | package main は cmd/fanout と tools/ 配下のみ。`cmd/...` の被 import 全面禁止 |
| `TestInternalTreeShape` | internal/ 直下は core/app/infra/ui/arch のみ(import graph でなく実ディレクトリを検査) |
| `TestAllPackagesClassified` / `TestExplicitLayerMapIsCurrent` | 全パッケージの層分類強制と stale エントリの検出 |
| `TestScanSanity` | スキャンの空回り防止(internal/・cmd/・tools/ の 3 ツリーを見ていることの保証) |

depguard は golangci-lint v2 導入(#191)で「コミュニティ合意でノイズ」として
不採用済み(`.golangci.yml` ヘッダに記録)。今回は「良いツールがあれば手書き
テストを置換する」前提で、Go のアーキテクチャテストツールを調査した。

## 調査結果

| ツール | 状態(2026-07) | 層方向 | stdlib 制約 | 判定 |
|---|---|---|---|---|
| [go-arch-lint](https://github.com/fe3dback/go-arch-lint) v1.16.0 | 514★・活発 | ○ | ×(`mayDependOn` / `canUse` は component / vendor のみ。stdlib は常時許可) | `TestCorePurity` を表現できない。方向のみの部分置換 |
| [arch-go](https://github.com/arch-go/arch-go) v2.1.2 | 266★・活発 | ○ | ○ | マッチした全ルールを AND 適用し、上書き・除外機構がない(`internal/verifications/dependencies/verifications.go` で確認)。`core/agent` 等のサブパッケージ例外を表現できない |
| [depguard](https://github.com/OpenPeeDeeP/depguard) v2 | golangci-lint 同梱 | ○ | ○(`$gostd`) | #191 で不採用済み。覆す材料なし |
| GoArchTest v0.1.0 / go-arctest / archtest / cht-go-lint | 未成熟または停滞 | — | — | 対象外 |
| gomodguard | 活発 | ×(module 単位のみ) | — | 対象外 |

## どのツールも置換できないもの

- 未使用例外の自動失効(`legacyDirectionAllowlist` / `explicitLayers` の stale 検出)。手書きガードで最も価値の高い機能
- `TestInternalTreeShape`(import graph でなく実ディレクトリの検査)
- `TestPackageMainOnlyInCmd`
- `TestScanSanity`
- `tools/reviewrisk` の docsync(`docs/architecture.ja.md` のパッケージ表 ↔ `rules.go`)

採用しても最良で 8 テスト中 2 テストの部分置換にとどまる。

## 判断

不採用。理由:

1. 置換範囲が小さい。go-arch-lint は方向のみ、arch-go でも方向 + 純度どまり。残る 6 テストは手書き継続が確定しているため、ルール系統が Go map と YAML の 2 系統に分裂してドリフトのリスクだけ増える
2. arch-go は例外セマンティクスを表現できない。broad ルール側から例外パッケージを除外列挙して模倣すると、新規パッケージが自動でルールに入る現行の性質が壊れる
3. 依存最小主義と合わない。runtime 依存は git/tmux/gh のみ、lint は pinned golangci-lint のみという方針に対し、ツールはバイナリ pin か go.mod のテスト依存を増やす。現状の CI 追加コストはゼロ(`go test ./...` に同梱)
4. `internal/arch` は review class H・reviewrisk S4(触れたら critical)の保護対象。ルールを YAML へ移すとガード定義が S4 の監視対象外に出て、`rules.go` / `signals.go` の保護面拡張が別途必要になる
5. 現行テストの失敗メッセージは修正方法まで明示しており、YAML の宣言性に置き換える利得がない

検討した代替案も不採用:

- ルール表のデータファイル(YAML 等)外出し — Go map は型検査・enforcement との同居・S4 保護をそのまま得られる。外出しはパーサ追加と保護面分裂のコストしかない
- depguard の再有効化 — #191 の判断を覆す材料がない

## 再評価の条件

いずれかを満たしたら再調査する。

- arch-go が複数ルール一致とサブパッケージ例外のセマンティクスを文書化・保証した
- 未使用例外の自動失効に相当する機能を持つツールが現れた
- `internal/arch` のルール追加が続き、手書き維持が負担になった(現状 491 行で安定)

## 参考

- `internal/arch/arch_test.go` — 層ルールの正典実装
- `docs/architecture.ja.md` — 層の責務と依存ルール、depguard 不採用の記述
- `.golangci.yml` ヘッダ — 不採用 linter 一覧。この文書はその arch テスト版
