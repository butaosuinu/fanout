# PR review risk 判定(tools/reviewrisk)

`tools/reviewrisk` は PR の変更パスから `docs/architecture.ja.md` の H/M/A
レビュークラスを引き、5 段階の risk level を機械的に判定する repo 専用ツール。
CI(`review-risk.yml`)が判定結果を `review:<level>` ラベルと sticky コメントで
PR に反映する。判定は advisory — マージはブロックしない。

## レベル定義

| Level | 条件 | ガイダンス |
|---|---|---|
| none | 変更ファイルが全て NONE クラス | レビュー不要(docs のみ)。CI green でマージ可 |
| low | 最大クラスが A | AI レビュー(`/code-review`)で可 |
| medium | 最大クラスが M | `/code-review` + M ファイルを人間が斜め読み |
| high | 最大クラスが H、または S9/S10 発火 | 人間レビュー必須。AI は補助 |
| critical | S1-S8 のいずれか発火 | 人間精読必須(検証系・ガード系への接触) |

## 判定の仕組み

判定の芯は `classifyPath` 一つ。ファイルパスを 1 本ずつ次の優先順で評価する。

1. `fileRules` の完全一致(`docs/architecture.ja.md` のファイル粒度の行、
   go.mod や Makefile など doc に行を持たない extra ファイルの両方を含む)
2. Go テストペアリング: `foo_test.go` は `foo.go` の file rule を継承する。
   ただしパッケージの prefix rule がその file rule より重ければそちらを採る
   — テストをパッケージのクラスより軽く落とすことはしない
3. web テスト上書き: `web/src/` 配下の `*.test.ts(x)` と `web/src/test/**` は
   常に A(`docs/architecture.ja.md` の「tests は A」の行に従う。hooks/lib の
   M より優先する非対称)
4. `prefixRules` の longest-prefix wins
5. どれにも一致しない → unclassified → **fail-closed で high**(S9)

rename(R)は新旧パスの両方を classifyPath に通し、重い方のクラスで判定する。
H パッケージから M/A へ `git mv` しても軽く扱わない。

`fileRules` と `prefixRules` は `Source` フィールドで由来を持つ。
`SourceDocTable` は `docs/architecture.ja.md` のパッケージ表に対応する行、
`SourceExtra` は doc に行のない補完ルール(go.mod / tests/ / .github/ など)。
doc パッケージ表が正典で、`SourceExtra` は正典が触れていないパスを埋める側 —
どちらのソースも classifyPath の評価順では区別しない。

### 意図的な非対称

`cmd/fanout/` と `internal/ui/tui/` には catch-all prefix rule がある
(それぞれ M)。`internal/ui/dashboard/` 直下と `web/` 直下には置いていない —
doc がそれぞれのファイルを全列挙しているため、新規ファイルは unclassified に
落ちて high になる。doc とルール表の同時更新を強制するための設計で、埋め忘れ
ではない。

doc 上 `view.go` / `compact.go` / `styles.go` の A 行は「ほか」付きの開放集合
として書かれているが、ルール表はこの 3 ファイルに閉じている。未列挙の描画
ファイルは A と推測せず M の catch-all に落とす。新しい描画ファイルを足す
たびに rules.go の更新を要求する安全側の判断。

## エスカレーションシグナル

パス分類だけでなく、diff の中身も grep して判定を上に振る。全シグナルは
`--format markdown` / `--format json` の理由リストに出る。

### critical に直結(S1-S8)

| ID | 名前 | 条件 |
|---|---|---|
| S1 | test-deleted | `*_test.go` / `*.bats` / `web/src/**/*.test.*` の削除(D)。rename でテスト形状が失われる場合も含む |
| S2 | measure-deleted | `tests/{golden,fixtures,bin}/**` の削除(D)。rename で測定対象外へ移す場合も含む |
| S3 | skip-added | 追加行に `\bt\.(Skip\|Skipf\|SkipNow)\(`、vitest の skip 形(`.skip(` / `.skipIf(` / `.skip.` 連鎖 / `skip: true` / `xit(` / `xdescribe(` / `xtest(`。対象は `*.test.ts(x)` と `web/src/test/**`)、bats(`tests/bats/**` の `.bats` と `.bash`)の `^\s*skip\b` |
| S4 | guard-modified | `internal/arch/` の変更 |
| S5 | review-gate-modified | `.claude/` と post-work-review gate(`codex/tools/post-work-review*` / `codex/skills/post-work-review/` / `claude/skills/post-work-review/`)の変更 |
| S6 | risk-tool-modified | `tools/reviewrisk/` または `review-risk.yml` の変更 |
| S7 | installer-modified | `install.sh` の変更 |
| S8 | ci-workflow-deleted | `.github/workflows/` 配下の削除(D)。rename で配下外へ移す場合も含む |

### high に押し上げ(S9-S10)

`S9 unclassified-path` は classifyPath が一致しなかったパス。fail-closed の
実装そのもの。`S10 invariant-hit` は `docs/architecture.ja.md` の人間必見の
不変条件に出てくる文字列の追加・削除行を grep する: `requireToken` /
`127.0.0.1` / `__tui-new-pane-popup` / `__tui-help-popup` / `__codex-plan-tui` /
`main.version`。`FANOUT_[A-Z_]{2,}` は、その名前が削除行に現れ、かつ**同じ
ファイルの**追加行に同名が現れない場合だけ発火する(ファイル単位の集合差)。
同一ファイル内の行移動・再インデントは誤検知しないが、別ファイルにコメントや
フィクスチャとして同名を足しても実参照の削除は隠せない。env 変数参照の追加は
日常的だが既存参照の削除はシェル/CI/doc 側の呼び出し元を壊しうる。`.md` ファイルは S10 の対象外。散文が不変条件の文字列を引用しても(この文書や
architecture.ja.md の不変条件カタログ自体、インストール手順の
`FANOUT_VERSION=` 例)、それは不変条件へのコード変更ではない。

### 1 段バンプ(S11)

`S11 large-diff` は NONE 以外のファイルの追加+削除行数の合計が 800 行超、
またはファイル数が 30 超。low か medium のときだけ +1 する(high/critical を
更に押し上げはしない)。閾値はどちらも名前付き定数。

### 集約順序

```
base   = max(levelForClass(f) for f in files)
level  = max(base, high) if S9 or S10 fired else base
level  = level + 1       if S11 fired and level in {low, medium}
level  = critical        if any of S1..S8 fired
```

Files は path 昇順、Reasons は (Level 降順, Signal, File) 順でソートする —
同じ diff は常に同じ出力になる。

## ローカル実行

```
go run ./tools/reviewrisk [--base <ref>] [--format text|markdown|json] [--fail-at <level>]
```

- `--base` の既定値は `origin/main`(無ければ `main`)。比較起点は
  `git merge-base <ref> HEAD`。そこから**作業ツリー**までの差分を見るので、
  未コミットの変更も判定に入る(未追跡ファイルは git diff に出ないため対象外)。
  CI は clean checkout なので `merge-base..HEAD` と同じ結果になる。
- git は全て read-only。base 解決の `rev-parse --verify` と `merge-base`、
  それに diff 3 本 — `diff --name-status -M`(ファイル一覧と rename 検出)、
  `diff --numstat -M`(行数。rename は併合パスを新パスへ正規化して計上)、
  `diff -U0`(grep 対象の追加/削除行)。いずれも `core.quotepath=off` で
  非 ASCII パスをそのまま出す。
- 終了コードは既定で常に 0(advisory)。`--fail-at <level>` を指定したときだけ、
  判定結果がその level 以上なら 1 を返す。git 実行失敗や flag 不正などの
  運用エラーは 2。
- `--format markdown` は sticky コメントの本文そのもの: 先頭に
  `<!-- review-risk -->` マーカー、理由リスト、ファイル別クラス表を
  `<details>` に畳んだもの。

`make review-risk` はこの CLI を素の設定で実行する薄いラッパ。

## CI 挙動

`review-risk.yml` は `pull_request` イベントで走り、fork からの PR には
走らない(`head.repo.full_name == github.repository` ガード)。checkout は
`persist-credentials: false` — judge ステップは PR 側の Go コードを実行するため、
書き込み token を `.git/config` に残さない(gh を呼ぶステップだけが env 経由で
token を受け取る)。

1. PR が `tools/reviewrisk/` か `review-risk.yml` 自身を変更している場合
   (rename で外へ移す場合も pathspec 判定で含む)、PR 側ツールの判定は信用せず
   (S6 の安全弁を PR 側が緩められるため)、workflow が fail-closed で critical
   に固定する。それ以外は `--format json` と `--format markdown` を実行し、
   `jq -r .level` で level を取り出す。
2. `review:none` / `review:low` / `review:medium` / `review:high` /
   `review:critical` の 5 ラベルを冪等に作成し(既存なら作成コマンドが失敗する
   ので無視する)、PR に付いている `review:*` ラベルのうち現在の level 以外を
   外し、現在の level を付ける。
3. PR コメントを `<!-- review-risk -->` マーカーで検索し、あれば内容を
   置き換え、なければ新規投稿する(sticky コメント)。

判定はブランチ保護ルールに足さない。CI green と level ラベルは別軸で、
critical でも CI が通ればマージ自体は可能 — 人間が読むかどうかの判断材料を
渡すだけの advisory 運用。

## LLM の位置づけ

ツールは LLM を一切呼ばない。`docs/pr-review-visualization-v2.ja.md` の
「CLI は LLM を呼ばない」という横断原則をそのまま踏襲する。medium 以上の
ガイダンス文が `/code-review` を指すのも次アクションの指針であって、ツール
自身が判断や要約を LLM に投げているわけではない。

## `docs/pr-review-visualization-v2.ja.md` 案 1 との関係

案 1(親 #175、子 #176-179)は fanout CLI 汎用の risk lane 構想で、任意の repo
に riskPaths glob や CI 結果を設定して使う想定だった。`tools/reviewrisk` は
それとは別物 — この repo の H/M/A 正典をそのままルール化した repo-local な
先行実装で、fanout 本体には同梱しない。案 1 の一般化が要るときは、この実装の
ルール表・シグナル設計をリファレンスにできる。

## v2 候補(未実装)

- `go/ast` で H パッケージの exported シグネチャ変更を検出し、変更なしなら
  シグナルを弱める。
- コメントのみの変更(diff 全行が `//` / docstring)を客観的に判定し、
  de-escalation の材料にする。

どちらも「機械的シグナルの追加」であって LLM 判断は導入しない。実装は
この文書に記録するだけで、v1 のスコープには含めない。

## rules.go を変更するとき

クラスを追加・変更したら `tools/reviewrisk/rules.go` のルール表を必ず
同時更新する。`docsync_test.go` が `docs/architecture.ja.md` のパッケージ表と
`rules.go` の食い違いを CI で落とす — 手順は
`docs/architecture.ja.md` の「新規パッケージの追加手順」を参照。
