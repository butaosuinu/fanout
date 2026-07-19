# アーキテクチャテストツールの調査と採用の経緯(2026-07)

層ルールの CI 強制は [godep-cruiser](https://github.com/butaosuinu/godep-cruiser)
の archtest で行う(2026-07 採用)。ルール正典は
`internal/arch/godep-cruiser.json`、runner と実ディレクトリ検査は
`internal/arch/arch_test.go`。同月の初回調査では外部ツールを不採用として
手書きテストを継続したが、その再評価条件「未使用例外の自動失効に相当する
機能を持つツールが現れた」を godep-cruiser v0.3.0 の baseline 機能が満たした
ため、再評価して置き換えた。前半は初回調査(不採用)の記録、後半の
「再評価と採用」が現行の決定。

## 初回調査(不採用・履歴)

### 前提

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

### 調査結果

| ツール | 状態(2026-07) | 層方向 | stdlib 制約 | 判定 |
|---|---|---|---|---|
| [go-arch-lint](https://github.com/fe3dback/go-arch-lint) v1.16.0 | 514★・活発 | ○ | ×(`mayDependOn` / `canUse` は component / vendor のみ。stdlib は常時許可) | `TestCorePurity` を表現できない。依存許可を空にした component は stdlib のみ許可になるため `TestToolsStdlibOnly` は置換可能(scanner は `_test.go` 含む全 `.go` を parse) |
| [arch-go](https://github.com/arch-go/arch-go) v2.1.2 | 266★・活発 | ○ | ○ | マッチした全ルールを AND 適用し、上書き・除外機構がない(arch-go リポジトリの `internal/verifications/dependencies/verifications.go` で確認)。`core/agent` 等のサブパッケージ例外を表現できない。パッケージを `Tests: false` で読むため `_test.go` の import も検査対象外 |
| [depguard](https://github.com/OpenPeeDeeP/depguard) v2 | golangci-lint 同梱 | ○ | ○(`$gostd`。`$` 末尾で完全一致指定可) | 候補中で表現力が最も高く、追加依存もない。それでも部分置換にとどまる(「検討した代替案」参照) |
| GoArchTest v0.1.0 / go-arctest / archtest / cht-go-lint | 未成熟または停滞 | — | — | 対象外 |
| gomodguard | 活発 | ×(module 単位のみ) | — | 対象外 |

### 置換可能性の内訳

テストごとの置換可能性。○ = ほぼ等価に置換可、△ = 劣化置換(欠ける性質を注記)、
× = 表現不可。前提として、arch-go と depguard は build システム経由でパッケージを
読むため、build tag や OS suffix で現在の GOOS/GOARCH から外れるファイルを
検査しない(現行 `scanRepo` は build constraint を評価せず全 `.go` を parse
する)。go-arch-lint は自前 scanner で全 `.go` を parse する。

| 現行テスト | go-arch-lint | arch-go | depguard |
|---|---|---|---|
| `TestLayerImportDirection` | △ 方向は `_test.go` 込みで書けるが、`legacyDirectionAllowlist` の自動失効がない。ファイル単位の例外は `excludeFiles` で当該ファイルを丸ごと未検査にする形になり、そのファイル内の他の違反も見えなくなる | △ `_test.go`(`Tests: false`)と build 対象外を検査しないため、`path_test.go` の例外は不要になる代わりにテスト全部が層方向の検査外。自動失効もない | △ 方向は書けるが自動失効がなく、build 対象外を検査しない。`files` glob はファイル単位なので例外の粒度は保てる |
| `TestCorePurity` | × stdlib 制限機構がない | × 例外の上書き不可 | △ `$` 完全一致・`files` 否定 glob・`$test` で denylist と例外を書けるが、未使用例外の自動失効がない |
| `TestToolsStdlibOnly` | ○ 依存許可を空にした component は stdlib のみ許可 | △ 非テスト・build 対象のみ | △ Strict + `$gostd` で書けるが build 対象のみ |
| `TestPackageMainOnlyInCmd` | △ `cmd/...` の被 import 禁止は可。package main 配置(package 節の検査)は import linter の範囲外 | △ 同左 | △ 同左 |
| `TestInternalTreeShape` | × 実ディレクトリ検査(非 Go ファイル含む)は import 解析の範囲外 | × 同左 | × 同左 |
| `TestAllPackagesClassified` | ○ component 未所属ファイルを検出(component glob の整備が前提) | △ coverage 閾値 100% で近似 | × `files` glob 外のパッケージは素通し |
| `TestExplicitLayerMapIsCurrent` | △ 0 件に解決される component glob は既定でエラー、component 未所属ファイルは警告になるため、設定の stale はおおむね fail-closed(Go ファイルを失った空ディレクトリ等の差は残る) | × 設定側の stale エントリ検出はない | × 同左 |
| `TestScanSanity` | △ 0 件解決 glob のエラーと未所属警告が空回りを兼ねて検出 | × coverage はロード済みパッケージだけが分母のため、ツリーごと欠落しても残りで 100% になれる(internal/・cmd/・tools/ 各 >0 という現行契約を検出できない) | × `files` glob が空振りしても検出されない |

`tools/reviewrisk` の docsync(`docs/architecture.ja.md` のパッケージ表 ↔
`rules.go`)はアーキテクチャリンターの守備範囲外で、どの案でも手書き維持。

### 判断(当時)

不採用。理由:

1. 置換範囲が狭い。上の表のとおり ○ は最多の go-arch-lint でも 2 テストにとどまり、核心の `TestLayerImportDirection` と `TestCorePurity` はどの候補でも劣化置換(自動失効の喪失、`_test.go`・build 対象外の非検査、例外の表現不可のいずれか)になる。手書きテストは全候補で複数残るため、ルール系統が Go map と設定ファイルの 2 系統に分裂してドリフトのリスクだけ増える
2. arch-go は例外セマンティクスを表現できない。broad ルール側から例外パッケージを除外列挙して模倣すると、新規パッケージが自動でルールに入る現行の性質が壊れる
3. 依存最小主義と合わない。必須依存を git/tmux/gh(+ live 実行時に選んだ agent CLI)に絞り、lint は pinned golangci-lint と shellcheck だけという方針に対し、go-arch-lint / arch-go はバイナリ pin か go.mod のテスト依存を増やす(golangci-lint 同梱の depguard は増やさないが、置換範囲は前項と「検討した代替案」のとおり)。現状の CI 追加コストはゼロ(`go test ./...` に同梱)
4. `internal/arch` は review class H・reviewrisk S4(触れたら critical)の保護対象。ルールを YAML へ移すとガード定義が S4 の監視対象外に出て、`rules.go` / `signals.go` の保護面拡張が別途必要になる
5. 現行テストの失敗メッセージは修正方法まで明示しており、YAML の宣言性に置き換える利得がない

検討した代替案も不採用:

- ルール表のデータファイル(YAML 等)外出し — Go map は型検査・enforcement との同居・S4 保護をそのまま得られる。外出しはパーサ追加と保護面分裂のコストしかない
- depguard の再有効化 — stdlib 制約の表現力は候補中で最も高い。`$` 末尾の完全一致で「`net` 禁止・`net/url` 許可」を再現でき、`files` の否定 glob と `$test` で `core/agent` / `core/planspec` の例外もテスト込みの検査も書け、pinned golangci-lint 同梱で追加依存もない。それでも不採用にする: 上の表のとおり自動失効・実ディレクトリ検査・package main 配置・空回り検出を持たず、`files` glob に載らない新規パッケージを黙って素通しする(`TestAllPackagesClassified` 相当の fail-closed がない)。部分置換で 2 系統分裂が残る点は他ツールと同じで、#191 の「ノイズ」判断を覆す利得がない

### 再評価の条件(当時)

いずれかを満たしたら再調査する、としていた。

- arch-go が複数ルール一致とサブパッケージ例外のセマンティクスを文書化・保証した
- **未使用例外の自動失効に相当する機能を持つツールが現れた** — godep-cruiser
  v0.3.0 の baseline がこれを満たし、下の再評価につながった
- `internal/arch` のルール追加が続き、手書き維持が負担になった(当時 491 行で安定)

## 再評価と採用(2026-07・現行の決定)

godep-cruiser v0.3.0(dependency-cruiser の Go 移植・同作者)を精査した結果、
初回調査の不採用理由がすべて解消されていたため採用した。

| 初回の不採用理由 | godep-cruiser での状況 |
|---|---|
| 未使用例外の自動失効がない | baseline の stale エントリは重大度に関係なく常時エラー。違反が消えるとエントリ削除を強制する |
| `_test.go`・build 対象外を検査しない | 全 `.go` を parse(build constraint 非評価)。skip 規則は `testdata`/`vendor`/`.`/`_` 接頭辞(旧 `scanRepo` との差は vendor の skip のみ。repo に vendor は無い) |
| stdlib 個別禁止を表現できない | `to.path` のアンカー付き正規表現 + `dependencyTypes: ["stdlib"]` で「`net` 禁止・`net/url` 許可」の完全一致が書ける |
| サブパッケージ例外を表現できない | 正規表現プレフィックスで親ディレクトリ継承を表現(`core/agent`・`core/planspec`) |
| バイナリ pin か依存追加が要る | `archtest.Check` で `go test ./...` に同梱。CI 追加コストゼロ。test 依存 1 行で、コンパイル対象に入る third-party 推移依存はゼロ(runtime は stdlib のみ。ただし go.mod 要求は上がる — 運用上の注意を参照) |
| package main 配置は範囲外 | `from.packageName` の source-only ルールで強制(import ゼロのファイルにも発火) |
| ルールが S4 保護の外に出る | ルール正典 `godep-cruiser.json` と baseline を `internal/arch/` 配下に置き、reviewrisk のプレフィックス保護をそのまま受ける |
| 失敗メッセージの修正誘導が失われる | ルールの `comment` を err レポーターが `fix:` 行として出力する |

置換の内訳(旧 8 テストの行き先):

| 旧テスト | 行き先 |
|---|---|
| `TestLayerImportDirection` | allowed マトリクス(fail-closed)+ 層ごとの forbidden 補集合ルール(修正誘導 comment 付き)の二段構え。`legacyDirectionAllowlist` は baseline へ |
| `TestCorePurity` | `core-purity-stdlib{,-agent,-planspec}` + `core-no-third-party` |
| `TestToolsStdlibOnly` | `tools-stdlib-only` |
| `TestPackageMainOnlyInCmd` | `package-main-location` + `no-import-cmd` の 2 ルール |
| `TestAllPackagesClassified` | allowed の fail-closed + `no-bare-tree-root-files`(cmd/・tools/・層ルート直下の bare `.go` 禁止)+ 手書き `TestInternalTreeShape` |
| `TestExplicitLayerMapIsCurrent` | 廃止(explicitLayers map 自体が消滅。設定側の staleness 検出は baseline の stale エラーだけが残る) |
| `TestInternalTreeShape` | 手書きで維持(internal/ 直下の実ディレクトリ検査・非 Go ファイル含む — import 解析の範囲外) |
| `TestScanSanity` | 手書き後継 `TestScanTreesPresent`(scanner と同じ skip 規則で 3 ツリーの空回りを検出) |

このほか手書きの guard テストを 2 本追加した: `TestRuleSeveritiesPinned`
(severity 省略が warn に落ちて archtest が fail しなくなる事故の防止)と
`TestPurityDenylistConsistent`(3 重に手書きされた core denylist のドリフト
防止)。

意図的な差分は 3 つ:

1. cgo(`import "C"`)の禁止。旧テストは core / tools とも cgo を素通し
   していたが、`unresolved` 型を禁止対象に含めたため両方で落ちる(微強化)
2. スキャン範囲が internal/・cmd/・tools/ の 3 ツリーから repo 全体に拡大。
   3 ツリー外に置かれた新規 Go ツリーも fail-closed で検査される(旧テストは
   素通し)。副作用として、repo 内のどこかに parse できない `.go` が置かれる
   とテスト自体が失敗する
3. scanner は `vendor/` を skip する(旧 `scanRepo` は skip しない。repo に
   vendor は無いので現状は無差)

置き換え時の等価性検証として、ルールごとに違反を 1 つ注入して期待ルール名で
失敗することを確認した(例外境界の `net/url`・`core/agent` の `os`、
`_test.go` の純度除外、baseline の stale 化、bare ファイル検出を含む)。

運用上の注意:

- godep-cruiser の version bump は層ガードの実体変更に相当する。go.mod だけの
  差分でも reviewrisk は critical を付けないため、bump PR は内容を人間が
  確認する
- godep-cruiser 側の `tool golang.org/x/vuln/cmd/govulncheck` directive の
  go.mod 要求が MVS で伝播し、fanout の govulncheck ツールチェーン
  (`make vuln`)も x/vuln v1.3.0 -> v1.6.0 等へ上がった。コンパイル対象は
  増えないが、脆弱性スキャナの実行バージョンが変わる点は bump 時も同様に
  意識する
- `godep-cruiser.json` は厳格 parse(未知キー拒否)のため `$schema` キーは
  書けない。ルールの意図は `comment` フィールドに置く
- baseline へ新規エントリを足すのは旧 `legacyDirectionAllowlist` と同じく
  原則禁止。既存エントリは違反解消時に stale エラーが削除を強制する
- 二段構えの代償として、方向の変更は allowed と forbidden 補集合の 2 箇所
  編集になる(片方だけ直すとどちらかが fail-closed で落ちるので、ドリフトは
  沈黙しない)。例外の baseline も 2 エントリ 1 組で増減する

## 参考

- `internal/arch/godep-cruiser.json` — 層ルールの正典(godep-cruiser ルール定義)
- `internal/arch/arch_test.go` — archtest runner と実ディレクトリ検査
- `docs/architecture.ja.md` — 層の責務と依存ルール、depguard 不採用の記述
- `.golangci.yml` ヘッダ — 不採用 linter 一覧。この文書はその arch テスト版
