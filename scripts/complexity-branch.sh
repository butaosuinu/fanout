#!/usr/bin/env bash
# fanout complexity-branch — この branch が merge base から新しく持ち込んだ複雑度を
# 報告する。`make complexity` と .github/workflows/complexity.yml の共通実体。
#
# 判定は「変更行かどうか」ではなく「merge base の同じ解析結果より悪化したか」。
# complexity 系の linter は違反位置を関数の宣言行として報告するため、行フィルタだけ
# では既存関数の本体に if を足したケースを取りこぼす (実測で gocognit の finding が
# 丸ごと落ちた)。ベースライン比較なら、450 行の既存関数を 1 行触っただけで落ちない
# という元々の要件も同時に満たす。
#
# 使い方: scripts/complexity-branch.sh [BASE_REF]
#   BASE_REF 既定 origin/main。COMPLEXITY_SARIF_DIR に SARIF を残す (CI が拾う)。
#
# 終了コード: 0 = 新規の違反なし / 1 = 新規の違反あり / 2 = 解析器の異常。
# 違反と解析失敗は必ず区別する。混ぜるとゲートが黙って無効化される。
set -uo pipefail

base_ref="${1:-origin/main}"

root="$(git rev-parse --show-toplevel 2>/dev/null)" || {
  echo "complexity: git リポジトリではありません" >&2
  exit 2
}
cd "$root" || exit 2

out="${COMPLEXITY_SARIF_DIR:-$root/.cache/complexity}"
mkdir -p "$out" || exit 2
# COMPLEXITY_SARIF_DIR は外から渡せる入力なので、この下を再帰削除しない
# (COMPLEXITY_SARIF_DIR=$HOME で home が飛ぶ)。消すのは自分が書く固定名だけで、
# glob も使わない。キャッシュのような「消したいディレクトリ」は mktemp に置く。
rm -f "$out"/go.sarif "$out"/go-base.sarif "$out"/go.log "$out"/ts.log

scratch="$(mktemp -d "${TMPDIR:-/tmp}/fanout-complexity.XXXXXX")" || exit 2
trap 'rm -rf "$scratch"' EXIT HUP INT TERM

merge_base="$(git merge-base "$base_ref" HEAD 2>/dev/null)" || {
  echo "complexity: $base_ref との merge base を解決できません" >&2
  exit 2
}

# merge base からのリネーム表 (新パス -> 旧パス)。core.quotePath は明示的に切る:
# 既定の true だと `web/src/画面/Foo.ts` が `"web/src/\347\224\273..."` として
# 出力され、拡張子フィルタからもパス突き合わせからも黙って落ちる。リネーム先は merge base に
# 存在しないので、旧パスへ寄せないと baseline が空になり既存違反が全部新規に化ける。
renames="$out/renames.tsv"
git -c core.quotePath=false diff --name-status -M --diff-filter=R "$merge_base" | awk -F'\t' 'NF>=3 {print $3 "\t" $2}' >"$renames" 2>/dev/null || : >"$renames"

# base_path_of NEWPATH — merge base 側のパス。リネームが無ければそのまま返す。
base_path_of() {
  awk -F'\t' -v p="$1" '$1 == p { print $2; found = 1; exit } END { if (!found) print p }' "$renames"
}

differ="$root/.github/scripts/complexity-diff.mjs"
[ -f "$differ" ] || {
  echo "complexity: $differ がありません" >&2
  exit 2
}

findings=""

# --- Go ----------------------------------------------------------------------
# golangci-lint の終了コード: 0 = 指摘なし、1 = 指摘あり、それ以外は解析器の異常。
#
# キャッシュは実行ごとに分ける。golangci-lint のキャッシュは木をまたいで衝突し、
# 同じキャッシュを共有すると 2 回目の木の結果が 568 件から 10 件まで落ちる (実測)。
run_golangci() {
  local cwd="$1" config="$2" sarif="$3" cache="$4" status
  (cd "$cwd" && GOLANGCI_LINT_CACHE="$cache" "$GOLANGCI_LINT_BIN" run -c "$config" \
    --output.sarif.path="$sarif" --output.text.path=/dev/null ./...) >"$out/go.log" 2>&1
  status=$?
  if [ "$status" -gt 1 ]; then
    echo "complexity: golangci-lint が異常終了しました (exit $status)" >&2
    sed -n '1,40p' "$out/go.log" >&2
    return 2
  fi
  [ -f "$sarif" ] || {
    echo "complexity: golangci-lint が SARIF を出力しませんでした" >&2
    return 2
  }
  return 0
}

GOLANGCI_LINT_BIN="${GOLANGCI_LINT_BIN:-golangci-lint}"
command -v "$GOLANGCI_LINT_BIN" >/dev/null 2>&1 || [ -x "$GOLANGCI_LINT_BIN" ] || {
  echo "complexity: golangci-lint が見つかりません ($GOLANGCI_LINT_BIN)" >&2
  exit 2
}

# `make lint` のキャッシュも共有しない。あちらは .golangci.yml で走るので、
# 複雑度 linter が無効な結果が再利用されてしまう。
run_golangci "$root" "$root/.golangci-complexity.yml" "$out/go.sarif" "$scratch/lint-cache" || exit 2

# merge base 側は git archive で取り出す。git worktree は使わない — fanout 自身が
# worktree を管理しており、隠し worktree を登録すると status や cleanup が混乱する。
#
# 展開先はリポジトリの外に置く。中に置くと golangci-lint が module/VCS root として
# 外側のリポジトリを見つけ、SARIF の uri が `../../../cmd/...` になってベースライン
# との突き合わせが全部外れる (実測)。
base_tree="$scratch/base-tree"
mkdir -p "$base_tree" || exit 2
# ベースラインを作れなければ fail closed。変更行フィルタへ退避すると、この仕組みの
# 主目的である「既存関数の本体だけ複雑化」を取りこぼしたままゲートが通ってしまう。
if ! git archive "$merge_base" | tar -x -C "$base_tree" 2>/dev/null ||
  ! cp "$root/.golangci-complexity.yml" "$base_tree/" 2>/dev/null ||
  ! run_golangci "$base_tree" "$base_tree/.golangci-complexity.yml" "$out/go-base.sarif" "$scratch/base-lint-cache"; then
  echo "complexity: Go のベースラインを作れませんでした" >&2
  exit 2
fi
go_base_arg=(--base "$out/go-base.sarif")
# --merge-base は常に渡す: ベースライン比較でもリネーム検出に要る。
go_base_arg+=(--merge-base "$merge_base")

# 空のベースラインは「既存違反ゼロのリポジトリ」でも起きる正常な状態なので、
# それ自体をエラーにはしない (エラーにすると最初の 1 件を足した PR が必ず落ちる)。
# ただし解析が空振りしたときの症状でもあるので警告は出す。空振りの主因だった
# キャッシュ共有は run_golangci で実行ごとに分けて潰してある。
counts="$(node -e '
  const fs = require("node:fs");
  const n = (f) => JSON.parse(fs.readFileSync(f, "utf8")).runs.reduce((a, r) => a + (r.results ?? []).length, 0);
  console.log(n(process.argv[1]), n(process.argv[2]));
' "$out/go.sarif" "$out/go-base.sarif" 2>/dev/null)" || counts="0 0"
if [ "${counts% *}" -gt 0 ] && [ "${counts#* }" -eq 0 ]; then
  echo "complexity: 警告 — Go のベースラインが空です。既存違反ゼロなら正常ですが、" >&2
  echo "            解析が空振りしている兆候でもあります ($out/go.log を確認)" >&2
fi

go_findings="$(node "$differ" --current "$out/go.sarif" "${go_base_arg[@]}" --root "$root")" || exit 2
[ -n "$go_findings" ] && findings="$findings$go_findings"$'\n'

# --- TypeScript --------------------------------------------------------------
# 変更ファイルの終点は working tree (HEAD ではない)。commit 前の `make complexity`
# でも未コミットの変更を見るため。CI では tree が clean なので HEAD と等しい。
ts_bin="$root/web/node_modules/.bin/complexity-lint"
# 正典は docs/complexity.ja.md の「除外対象」表。hook の eligible() と
# web/tools/complexity/eslint.config.js の EXCLUDED が同じ集合を持つ。
TS_EXCLUDE='\.(test|spec|stories)\.tsx?$|\.d\.ts$|\.gen\.tsx?$|^web/src/test/|/__mocks__/|/generated/' 
# 未追跡ファイルも含める。git diff は列挙しないので、新しいコンポーネントを
# git add 前に検査すると TypeScript が丸ごとスキップされてしまう。
changed="$( {
  git -c core.quotePath=false diff --name-only --diff-filter=ACMR "$merge_base" -- web/src
  git -c core.quotePath=false ls-files --others --exclude-standard -- web/src
} | sort -u | grep -E '\.tsx?$' | grep -vE "$TS_EXCLUDE" || true)"

if [ -n "$changed" ] && [ -x "$ts_bin" ]; then
  while IFS= read -r path; do
    [ -n "$path" ] || continue
    rel="${path#web/}"
    safe="$(printf '%s' "$rel" | tr '/.' '__')"
    rm -f "$out/ts-$safe.sarif" "$out/ts-$safe-base.sarif"
    (cd "$root/web" && "$ts_bin" --format @microsoft/eslint-formatter-sarif \
      --output-file "$out/ts-$safe.sarif" "$rel") >"$out/ts.log" 2>&1
    status=$?
    if [ "$status" -gt 1 ]; then
      echo "complexity: ESLint が異常終了しました ($rel, exit $status)" >&2
      sed -n '1,40p' "$out/ts.log" >&2
      exit 2
    fi
    [ -f "$out/ts-$safe.sarif" ] || continue
    # ESLint の exit 1 は「違反あり」であって失敗ではない。&& で繋ぐと、ベース側に
    # 既存違反があるファイルほどベースラインを捨てて変更行フィルタに退避してしまい、
    # この仕組みの主目的である「既存関数の本体だけ複雑化」を取りこぼす。
    ts_base_arg=()
    base_path="$(base_path_of "$path")"
    # rename 元が「今と同じ条件で測れない場所」なら baseline を作らない。旧内容を
    # 別の条件 (テスト扱いだった / src 外だった / .tsx のしきい値だった) で測ると、
    # いま超過している分まで「既存」として相殺されてしまう。
    if printf '%s\n' "$base_path" | grep -Eq "$TS_EXCLUDE" ||
      [ "${base_path#web/src/}" = "$base_path" ] ||
      [ "${base_path##*.}" != "${path##*.}" ]; then
      base_path=""
    fi
    if [ -n "$base_path" ] && git cat-file -e "$merge_base:$base_path" 2>/dev/null; then
      git show "$merge_base:$base_path" |
        (cd "$root/web" && "$ts_bin" --stdin --stdin-filename "$rel" \
          --format @microsoft/eslint-formatter-sarif) >"$out/ts-$safe-base.sarif" 2>/dev/null
      if [ ! -s "$out/ts-$safe-base.sarif" ]; then
        # merge base に在るのにベースラインが作れないのは解析の失敗。変更行
        # フィルタへ退避すると本体だけの複雑化を取りこぼすので fail closed。
        echo "complexity: $rel のベースラインを作れませんでした" >&2
        exit 2
      fi
      ts_base_arg=(--base "$out/ts-$safe-base.sarif")
    fi
    ts_base_arg+=(--merge-base "$merge_base")
    ts_findings="$(node "$differ" --current "$out/ts-$safe.sarif" "${ts_base_arg[@]}" --root "$root")" || exit 2
    [ -n "$ts_findings" ] && findings="$findings$ts_findings"$'\n'
  done <<<"$changed"
elif [ -n "$changed" ]; then
  echo "complexity: web/node_modules が無いため TypeScript は未検査です" >&2
  exit 2
fi

# 対象が 0 件でも空の ESLint run を残す。code scanning の alert は tool 単位で
# 突き合わせるので、run ごと消えると前の push で出た TypeScript の alert が
# 解消済みにならない。
if [ ! -f "$out/ts-empty.sarif" ]; then
  cat >"$out/ts-empty.sarif" <<'SARIF'
{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"ESLint","rules":[]}},"results":[]}]}
SARIF
fi

# --- 判定 --------------------------------------------------------------------
if [ -n "$findings" ]; then
  printf '%s' "$findings"
  count="$(printf '%s' "$findings" | grep -c . || true)"
  echo "complexity: この branch が新しく持ち込んだ違反 ${count} 件 (base $base_ref)" >&2
  exit 1
fi

echo "complexity: 新規の違反なし (base $base_ref)" >&2
exit 0
