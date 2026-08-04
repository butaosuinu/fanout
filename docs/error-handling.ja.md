# Go エラーハンドリングの書き方(2026-08)

`if err != nil` の**数**は減らせない。減らせるのは 1 箇所あたりのノイズと、
エラーが読み手に渡す情報量だ。この文書はそのための手法と、この repo での実測を
定める。

## 前提: 言語側の解決はない

Go チームは 2025-06-03 の
[On | No syntactic support for error handling](https://go.dev/blog/error-syntax)
で、エラーハンドリング構文の言語変更を今後追求しないと宣言した。`check`/`handle`、
`try`、`?` は全て不採用で、構文に関する提案は調査せずクローズされる。同記事が
代わりに挙げたのが、意味のあるメッセージを足すこと・`cmp.Or`・Rob Pike の
["Errors are values"](https://go.dev/blog/errors-are-values) で、以下はそれを
この repo に当てたもの。

## 実測: 何が苦痛なのか

非テストコードの `if err != nil` は 755 箇所。うち約 303 箇所(40%)が
`return X{}, err` の素通しだった。

| 層 | `if err != nil` | 素通し |
|---|---|---|
| `internal/infra` | 429 | 191 |
| `cmd/fanout` | 149 | 70 |
| `internal/app` | 116 | 22 |
| `internal/ui` | 48 | 12 |
| `internal/core` | 13 | 8 |

ただし**素通しそのものは欠陥ではない**。`internal/infra/execx` は
`"git diff --shortstat HEAD: <stderr>"` の形でコマンドと stderr を含むエラーを
作るので、その直上での素通しは何も失っていない。問題は、素通しがどの
worktree・どのファイルについて起きたかを落とすときだけだ。

## 1. `defer errs.Wrap` — 既定の手法

関数の識別子(パス・issue 番号・ペイン ID)を全 return 経路に一度で足す。

```go
func (r Runner) WorktreePatch(path, baseRef string) (_ Patch, err error) {
	defer errs.Wrap(&err, "worktree patch %q", path)

	path, err = r.resolveWorktreePath(path)
	if err != nil {
		return Patch{}, err
	}
	...
}
```

`internal/core/errs.Wrap` は `*errp == nil` で即 return するので、成功パスの
コストは比較 1 回。`%w` で包むため `errors.Is` / `errors.AsType` は貫通する。

規約は 3 つ。

- **戻り値は名前付きにする。** 値側は `_` で潰す(`(_ Patch, err error)`)。
  `defer` は戻り値の代入後に走るので、`if` ブロック内で `:=` によりシャドウ
  された `err` を返しても正しく包まれる。
- **関数の最初の `defer` として登録する。** `defer` は LIFO なので、最初に
  登録したものが最後に走り、後続の cleanup defer が `err` に代入した
  `Close` 失敗まで包める。
- **`go vet` の printf 検査が効く。** `Wrap` は printf ラッパとして推論される
  ので、`errs.Wrap(&err, "%d", "str")` は vet が落とす。書式引数を疑わなくてよい。

**引数は defer 登録時に評価される。** `WorktreePatch` は直後に `path` を絶対パス
へ書き換えるが、メッセージに載るのは呼び出し側が渡した元の `path` になる。
どちらを見せたいかは意識して選ぶこと。

### 効かないこと

`defer errs.Wrap` は `if err != nil` の**数を減らさない**。減るのは各サイトの
`fmt.Errorf(...)` であって、`if` そのものではない。行数は `defer` の分だけ
むしろ増える。数を減らしたいなら 3 と 5 を使う。

## 2. 識別子は 1 回だけ

`defer errs.Wrap` を足したら、その関数の内側のメッセージから同じ識別子を消す。

```go
// before
return FileStat{}, fmt.Errorf("parse untracked numstat for %q: %w", rel, err)
// after — "untracked file %q" は defer が付ける
return FileStat{}, fmt.Errorf("parse numstat: %w", err)
```

呼び出し側が既に識別子を包んでいる非公開ヘルパには、自前の `Wrap` を足さない。
`gitstat.mergeBaseTreeEntry` は `replacementPatch` / `replacementChanged` からしか
呼ばれず、両者とも `file.Path` を包むので、自分では `rel` を出さない。二重に出す
と 2 つの別ファイルの話に読める。

## 3. `cmp.Or` — 互いに独立した呼び出し

```go
diffOut, diffErr := r.git("-C", path, "diff", "--shortstat", base)
statusOut, statusErr := r.git("-C", path, "status", "--porcelain")
if err := cmp.Or(diffErr, statusErr); err != nil {
	return Stat{}, err
}
```

チェックが 2 個から 1 個に減る。**代償は両方が必ず実行されること**。前段が
失敗したら後段を打ち切りたい場合、後段が前段の結果に依存する場合、後段が高価な
場合(ネットワーク、`gh` の rate limit)には使わない。

## 4. `errors.AsType[T]`(Go 1.26)

新規コードは `errors.As` ではなくこちらを使う。宣言 → 検査の 2 段が 1 行になり、
変数のスコープも `if` の中に収まる。

```go
// before
var exitErr *exec.ExitError
if errors.As(err, &exitErr) { ... }

// after
if exitErr, ok := errors.AsType[*exec.ExitError](err); ok { ... }
```

既存の 11 箇所(`herdrrun` / `execx` / `selfupdate` / `codexapp` / `tmuxrun` /
`core/backend`)の置換は触ったついでで進める。

## 5. 検査を 1 回に畳む

同じエラーを返す分岐が並んだら、値だけを分岐させて検査を後ろに出す。
`gitstat.MergeBase` は 3 分岐が同じ `validate base ref %q` を返していた。

```go
var checkRef []string
switch {
case strings.HasPrefix(baseRef, "refs/"):
	checkRef = []string{baseRef}
	...
}
if checkRef != nil {
	if _, err := r.git(append([]string{"-C", path, "check-ref-format"}, checkRef...)...); err != nil {
		return "", fmt.Errorf("validate base ref %q: %w", baseRef, err)
	}
}
```

同じ発想の上位版が「そもそも `error` を返さない」だ。`gitstat.parseShortStat` は
失敗しようがないので error を返さない。パーサを書くときは、まず戻り値から
`error` を落とせないか確かめる。

## 適用しない場面

- **メッセージ規約がサイトごとに違う一群を、共通ヘルパで畳もうとしない。**
  `ghissue` の「`gh` を叩いて parse する」形は 6 箇所あるが、パーサによって
  自分で `parse gh ...` を名乗るものと呼び出し側が名乗るものが混在し、
  decode 失敗と意味的失敗が同じ戻り値に乗る。共通化すると前置きが二重になるか、
  パーサ 5 本の書き直しが要る。5 チェックの削減では釣り合わない。
- **中間値で分岐する連鎖に sticky error runner を入れない。** エラーを溜める
  ラッパ(`Errors are values`)が効くのは、連続する独立した呼び出しだけ。
  `gitstat` は `git` の出力を毎回パーサへ渡して分岐するので、評価した上で
  見送った。

## 採用しない手法

- **`panic`/`recover` による `try()` / `must()`。** パッケージ内部で完結する
  パーサに限れば標準ライブラリにも前例があるが、API 境界を越えさせない。
  制御フローが `go vet` と linter から見えなくなる。
- **`wrapcheck` / `err113`。** `.golangci.yml` が「コミュニティ合意でノイズ」
  として不採用にしている。判断を覆さない。
- **コード生成によるラッパ。** 差分がレビューできなくなる。

## パイロットの実測(`internal/infra/gitstat`)

1〜5 を `gitstat.go`(1091 行)に適用した結果。

| 指標 | before | after |
|---|---|---|
| エラー検査 | 87 | 84 |
| `fmt.Errorf` | 72 | 62 |
| 行数 | 1091 | 1105 |

**行は増える。**`defer errs.Wrap` 6 本と import 2 行の分だ。得たのは、
`WorktreePatch` 配下の全エラーが worktree パスを持つようになったこと、
`%q` の重複が 10 箇所消えたこと、`MergeBase` の三重複が 1 本になったこと。
数字で殴れる改善ではないので、**行数の削減を目的にこの手法を使わないこと**。

`internal/infra/ghissue` も候補にしたが見送った。26 箇所が最大 2
チェック/関数に分散し、`fmt.Errorf` は全て別内容で識別子の重複がない。
既に S/N は高い。

## エディタで畳む

コードを変えずに視界から消す手段。効果はどの手法より大きい。

- GoLand: [Code Folding](https://www.jetbrains.com/help/go/code-folding-settings.html)
  設定、または [Go Error Folds](https://plugins.jetbrains.com/plugin/22486-go-error-folds)
- VS Code: [iferrblocks](https://marketplace.visualstudio.com/items?itemName=rstuven.iferrblocks)
- Neovim: [goerr-nvim](https://github.com/Snyssfx/goerr-nvim)

Zed の対応は未確認。
