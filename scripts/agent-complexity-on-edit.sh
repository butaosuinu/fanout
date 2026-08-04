#!/usr/bin/env bash
# fanout complexity-on-edit — PostToolUse(Edit|Write|MultiEdit) hook for Claude
# Code and Codex.
#
# Measures the complexity of just the edited file and pushes the finding back to
# the agent while it still has the context to fix it. Thresholds come from
# .golangci-complexity.yml (Go) and web/tools/complexity/eslint.config.js (TS) —
# the same files .github/workflows/complexity.yml reads, so local and CI never
# disagree. The advisory pass reuses those configs at 2/3 instead of copying
# numbers: golangci-lint gets a scaled config generated into the repo root,
# ESLint gets FANOUT_COMPLEXITY_ADVISORY=1.
#
# Only complexity this branch actually added is reported. The file is analyzed
# twice — as it is now, and as it was at the merge base — and
# .github/scripts/complexity-diff.mjs keeps the findings that are new or whose
# measured value grew. Filtering by changed lines alone is not enough: the
# complexity linters report at a function's DECLARATION line, so adding an `if`
# to an existing function's body leaves the reported line untouched and the
# finding would vanish. Comparing to the baseline also keeps the original
# requirement intact — touching one line of a 450-line function that did not get
# worse still passes.
#
# Contract (PostToolUse hook, identical for both agents): stdin is the tool-call
# JSON. Exit 0 allows the edit; exit 2 + stderr sends the message back to the
# agent for a fix. Anything else is a non-blocking error, which is why this
# script uses `set -u` without `-e`. Advice rides on stdout as
# hookSpecificOutput.additionalContext so it lands in the agent's context
# without interrupting the edit.
#
# Two stages, on purpose: only threshold breaches block. Findings that merely
# reach 2/3 of a threshold are advice — blocking on everything turns a minor
# overage into an edit loop. The same reasoning caps retries: after
# FANOUT_COMPLEXITY_MAX_RETRIES blocks for one file in one session the block
# degrades to advice, because some complexity is structural and hammering at it
# is worse than shipping it with a reasoned suppression.
#
# This is the speed layer, not the enforcement layer — it can be turned off
# locally. Enforcement lives in .github/workflows/complexity.yml.
#
# Every analyzer output is deleted before its run: the SARIF files live in .git
# across invocations, and accepting a leftover as "this run's result" would send
# an edit back on findings from a previous edit — the opposite of fail-open.
#
# Fails open everywhere: missing golangci-lint binary, missing node, missing
# web/tools/complexity/node_modules, missing config, unresolvable base ref, or a
# tool that exits abnormally all exit 0. A broken hook must never stop work.
# Escape hatch: FANOUT_SKIP_COMPLEXITY=1.
set -u

[ "${FANOUT_SKIP_COMPLEXITY:-}" = "1" ] && exit 0

input="$(cat)"

lib="$(cd "$(dirname "$0")" && pwd)/agent-hooks-lib.sh"
[ -f "$lib" ] || exit 0
# shellcheck source=scripts/agent-hooks-lib.sh
. "$lib"

max_retries="${FANOUT_COMPLEXITY_MAX_RETRIES:-3}"

command -v node >/dev/null 2>&1 || exit 0

cwd_base="$(resolve_project_dir "$input")"
top="$(git -C "$cwd_base" rev-parse --show-toplevel 2>/dev/null)"
[ -n "$top" ] || exit 0
dir="$top"

differ="$dir/.github/scripts/complexity-diff.mjs"
[ -f "$differ" ] || exit 0

# Claude Edit/Write payloads carry tool_input.file_path. Codex apply_patch
# payloads carry the patch text instead; its `*** Add/Update File:` headers
# name every touched path.
files=()
file="$(json_field "$input" file_path)"
if [ -n "$file" ]; then
  files+=("$file")
else
  patch="$(json_field "$input" command)"
  [ -n "$patch" ] || patch="$(json_field "$input" patch)"
  [ -n "$patch" ] || exit 0
  while IFS= read -r line; do
    case "$line" in
    "*** Add File: "* | "*** Update File: "*) files+=("${line#*File: }") ;;
    # rename は移動先を見る。移動元は消えているので測りようがない。
    "*** Move to: "*) files+=("${line#*Move to: }") ;;
    esac
  done <<<"$patch"
fi
[ "${#files[@]}" -gt 0 ] || exit 0

base_ref="$(default_base_ref "$dir")"
[ -n "$base_ref" ] || exit 0
merge_base="$(git -C "$dir" merge-base "$base_ref" HEAD 2>/dev/null)"
[ -n "$merge_base" ] || exit 0

gitdir="$(git -C "$dir" rev-parse --git-dir 2>/dev/null)" || exit 0
case "$gitdir" in
/*) ;;
*) gitdir="$dir/$gitdir" ;;
esac
work="$gitdir/fanout-complexity"
mkdir -p "$work" 2>/dev/null || exit 0

# Analyzer scratch is per invocation. Claude Code can dispatch several Edit calls
# in one turn, and a shared cur.sarif / base.sarif would let one file's analysis
# be compared against another's.
scratch="$(mktemp -d "${TMPDIR:-/tmp}/fanout-complexity.XXXXXX")" || exit 0
trap 'rm -rf "$scratch"' EXIT HUP INT TERM

# Never share `make lint`'s golangci-lint cache: that one runs with .golangci.yml,
# where the complexity linters are disabled, and its cached "no issues" result
# comes back for these runs too — the baseline turns up empty and every existing
# finding then looks like this branch introduced it. The merge-base run gets its
# own cache again (go_base_sarif), because the cache also collides across trees.
export GOLANGCI_LINT_CACHE="$scratch/lint-cache"

# base_path_of RELPATH — the file's path at the merge base, empty when there is
# no usable baseline. A renamed file does not exist there under its new name, and
# reading an empty baseline would report every pre-existing finding as newly
# added. A rename OUT of the excluded set (foo.test.ts -> foo.ts) gets no
# baseline at all: measuring the old test content with product rules would let
# today's overage cancel itself out.
base_path_of() {
  local resolved
  resolved="$(git -C "$dir" -c core.quotePath=false diff --name-status -M --diff-filter=R "$merge_base" |
    awk -F'\t' -v p="$1" '$1 ~ /^R/ && $3 == p { print $2; found = 1; exit } END { if (!found) print p }')"
  if [ "$resolved" != "$1" ]; then
    # rename: 旧パスが今と同じ条件で測れないなら baseline を作らない。
    eligible "$dir/$resolved" || return 1
    [ "${resolved##*.}" = "${1##*.}" ] || return 1
  fi
  printf '%s' "$resolved"
}

# eligible PATH — 0 when the file is in scope. The excluded set is the one
# catalogued in docs/complexity.ja.md; web/tools/complexity/eslint.config.js and
# scripts/complexity-branch.sh carry the same list. Keep the three in step.
#
# Tests are excluded because table-driven Go tests and the large web describe
# blocks are long by design. Generated, vendored, mock and Storybook code does
# not exist in this repository yet — the patterns are here so the gate does not
# misfire the day one appears.
eligible() {
  case "$1" in
  *_test.go | */mock_*.go | *_mock.go | */vendor/*) return 1 ;;
  *.go) return 0 ;;
  esac
  case "$1" in
  "$dir"/web/src/*.test.ts | "$dir"/web/src/*.test.tsx) return 1 ;;
  "$dir"/web/src/*.spec.ts | "$dir"/web/src/*.spec.tsx) return 1 ;;
  "$dir"/web/src/*.stories.ts | "$dir"/web/src/*.stories.tsx) return 1 ;;
  "$dir"/web/src/*.d.ts | "$dir"/web/src/*.gen.ts | "$dir"/web/src/*.gen.tsx) return 1 ;;
  "$dir"/web/src/test/* | "$dir"/web/src/*__mocks__/* | "$dir"/web/src/*generated/*) return 1 ;;
  "$dir"/web/src/*.ts | "$dir"/web/src/*.tsx) return 0 ;;
  esac
  return 1
}

# advisory_config — a copy of .golangci-complexity.yml with every numeric
# threshold scaled to 2/3, regenerated whenever the source config is newer.
# Generated rather than tracked so the block config stays the only place a
# number is written. nestif and dupl are dropped: they have no advisory stage.
#
# It has to sit at the repository root, not in .git: golangci-lint resolves the
# paths it analyzes relative to the config file's directory, so a config
# anywhere else silently analyzes nothing and reports "0 issues". .gitignore
# covers the generated name.
advisory_config() {
  local src="$dir/.golangci-complexity.yml" out="$dir/.golangci-complexity-advisory.yml"
  [ -f "$src" ] || return 1
  if [ ! -f "$out" ] || [ "$src" -nt "$out" ]; then
    awk '
      /^    - (nestif|dupl)([[:space:]]|$)/ { next }
      /^    (nestif|dupl):[[:space:]]*$/ { skip = 1; next }
      skip && /^      / { next }
      { skip = 0 }
      /^      (min-complexity|lines|statements): [0-9]+[[:space:]]*$/ {
        scaled = int(($2 * 2) / 3)
        if (scaled < 1) scaled = 1
        sub(/[0-9]+[[:space:]]*$/, scaled)
      }
      { print }
    ' "$src" >"$out.tmp" 2>/dev/null || return 1
    mv -f "$out.tmp" "$out" 2>/dev/null || return 1
  fi
  printf '%s' "$out"
}

go_bin=""
go_bin_resolved=0
resolve_go_bin() {
  [ "$go_bin_resolved" = "1" ] && return 0
  go_bin="$(golangci_bin "$dir")"
  go_bin_resolved=1
}

# go_sarif CONFIG RELPATH OUT — analyze the working-tree file.
go_sarif() {
  rm -f "$4"
  (cd "$dir" && "$1" run -c "$2" --output.sarif.path="$4" --output.text.path=/dev/null "$3") \
    >/dev/null 2>&1
  [ -f "$4" ]
}

# go_base_sarif CONFIG RELPATH OUT — analyze the merge-base version of the file
# in a throwaway module. The complexity linters are AST-only, so unresolved
# imports do not matter; recreating the same relative path keeps the SARIF URIs
# comparable with the working-tree run.
#
# The throwaway module must live outside the repository: inside it, golangci-lint
# resolves the module/VCS root to the real repo and emits `../../../` prefixed
# URIs that no longer match the working-tree run.
go_base_sarif() {
  local config="$1" rel="$2" out="$3" tree version base_rel
  rm -f "$out"
  tree="$(mktemp -d "${TMPDIR:-/tmp}/fanout-complexity-base.XXXXXX")" || return 1
  mkdir -p "$tree/$(dirname "$rel")" 2>/dev/null || { rm -rf "$tree"; return 1; }
  version="$(awk '/^go /{print $2; exit}' "$dir/go.mod" 2>/dev/null)"
  {
    base_rel="$(base_path_of "$rel")" &&
      git -C "$dir" show "$merge_base:$base_rel" >"$tree/$rel" 2>/dev/null &&
      printf 'module fanoutcomplexitybase\n\ngo %s\n' "${version:-1.21}" >"$tree/go.mod" &&
      cp "$config" "$tree/config.yml" 2>/dev/null &&
      (cd "$tree" && GOLANGCI_LINT_CACHE="$tree/lint-cache" "$go_bin" run -c config.yml \
        --output.sarif.path="$out" --output.text.path=/dev/null "$rel") >/dev/null 2>&1
  }
  rm -rf "$tree"
  [ -f "$out" ]
}

ts_bin="$dir/web/node_modules/.bin/complexity-lint"

# ts_sarif ADVISORY RELPATH OUT — analyze the working-tree file.
ts_sarif() {
  rm -f "$3"
  (cd "$dir/web" && FANOUT_COMPLEXITY_ADVISORY="$1" \
    "$ts_bin" --format @microsoft/eslint-formatter-sarif --output-file "$3" "$2") >/dev/null 2>&1
  [ -f "$3" ]
}

# ts_base_sarif ADVISORY RELPATH OUT — analyze the merge-base version through
# --stdin, which keeps ESLint's config resolution and SARIF URIs identical to
# the working-tree run without materializing a file.
ts_base_sarif() {
  local base_rel
  rm -f "$3"
  base_rel="$(base_path_of "web/$2")" || return 1
  git -C "$dir" show "$merge_base:$base_rel" 2>/dev/null |
    (cd "$dir/web" && FANOUT_COMPLEXITY_ADVISORY="$1" \
      "$ts_bin" --stdin --stdin-filename "$2" --format @microsoft/eslint-formatter-sarif) \
      >"$3" 2>/dev/null
  [ -s "$3" ]
}

# report CURRENT BASE — findings this branch added, one per line.
report() {
  if [ -s "$2" ]; then
    node "$differ" --current "$1" --base "$2" --merge-base "$merge_base" --root "$dir" 2>/dev/null
  else
    node "$differ" --current "$1" --merge-base "$merge_base" --root "$dir" 2>/dev/null
  fi
}

blocking=""
advice=""
blocked_paths=()

for file in "${files[@]}"; do
  case "$file" in
  /*) ;;
  *) file="$cwd_base/$file" ;;
  esac
  [ -f "$file" ] || continue
  # git reports the real path, so resolve symlinked prefixes (macOS /tmp ->
  # /private/tmp) before stripping the root. Otherwise the relative path stays
  # absolute and every `git show <base>:<path>` lookup misses.
  file="$(cd "$(dirname "$file")" 2>/dev/null && pwd -P)/$(basename "$file")"
  [ -f "$file" ] || continue
  eligible "$file" || continue

  found=""
  soft=""
  case "$file" in
  *.go)
    resolve_go_bin
    [ -n "$go_bin" ] || continue
    block_cfg="$dir/.golangci-complexity.yml"
    [ -f "$block_cfg" ] || continue
    rel="${file#"$dir"/}"
    go_sarif "$go_bin" "$block_cfg" "$rel" "$scratch/cur.sarif" || continue
    go_base_sarif "$block_cfg" "$rel" "$scratch/base.sarif" || : >"$scratch/base.sarif"
    found="$(report "$scratch/cur.sarif" "$scratch/base.sarif")"
    if [ -z "$found" ]; then
      adv_cfg="$(advisory_config)" || continue
      go_sarif "$go_bin" "$adv_cfg" "$rel" "$scratch/cur.sarif" || continue
      go_base_sarif "$adv_cfg" "$rel" "$scratch/base.sarif" || : >"$scratch/base.sarif"
      soft="$(report "$scratch/cur.sarif" "$scratch/base.sarif")"
    fi
    ;;
  *)
    [ -x "$ts_bin" ] || continue
    [ -d "$dir/web/tools/complexity/node_modules" ] || continue
    rel="${file#"$dir"/web/}"
    ts_sarif 0 "$rel" "$scratch/cur.sarif" || continue
    ts_base_sarif 0 "$rel" "$scratch/base.sarif" || : >"$scratch/base.sarif"
    found="$(report "$scratch/cur.sarif" "$scratch/base.sarif")"
    if [ -z "$found" ]; then
      ts_sarif 1 "$rel" "$scratch/cur.sarif" || continue
      ts_base_sarif 1 "$rel" "$scratch/base.sarif" || : >"$scratch/base.sarif"
      soft="$(report "$scratch/cur.sarif" "$scratch/base.sarif")"
    fi
    ;;
  esac

  if [ -n "$found" ]; then
    blocking="$blocking$found"$'\n'
    blocked_paths+=("$file")
  fi
  [ -n "$soft" ] && advice="$advice$soft"$'\n'
done

attempts="$work/attempts"
session="$(json_field "$input" session_id)"
[ -n "$session" ] || session="nosession"

# retries_exhausted — true once this session has already been sent back
# max_retries times for every file in this batch.
retries_exhausted() {
  local path key count
  for path in "${blocked_paths[@]}"; do
    key="$session ${path#"$dir"/}"
    count="$(grep -cFx -- "$key" "$attempts" 2>/dev/null)" || count=0
    [ "$count" -ge "$max_retries" ] || return 1
  done
  return 0
}

record_attempts() {
  local path lines
  for path in "${blocked_paths[@]}"; do
    printf '%s %s\n' "$session" "${path#"$dir"/}" >>"$attempts" 2>/dev/null || return 0
  done
  # Bound the log; it lives in .git and is only ever counted, never replayed.
  lines="$(wc -l <"$attempts" 2>/dev/null)" || return 0
  if [ "${lines:-0}" -gt 500 ]; then
    tail -n 200 "$attempts" >"$attempts.tmp" 2>/dev/null &&
      mv -f "$attempts.tmp" "$attempts" 2>/dev/null
  fi
}

# emit_context TEXT — hand TEXT to the agent as PostToolUse additional context.
# Hand-rolled JSON: the hooks deliberately depend on no jq / python3.
emit_context() {
  printf '{"hookSpecificOutput":{"hookEventName":"PostToolUse","additionalContext":"%s"}}\n' \
    "$(printf '%s' "$1" | awk '
      # ORS="" plus the literal two-character "\\n" keeps real newlines out of
      # the JSON string; printf would turn a \n in its format into one.
      BEGIN { ORS = "" }
      { gsub(/\\/, "\\\\"); gsub(/"/, "\\\""); gsub(/\t/, "\\t"); print $0 "\\n" }
    ')"
}

guidance='
下げ方 (数字を通すためだけの機械的な分割は禁止):
- 早期 return とガード節でネストを浅くする
- 意味のある単位でヘルパ関数に切り出す。processDataPart1 / processDataPart2 の
  ような、呼び出し側でしか意味をなさない分割は不可
- 条件分岐の塊はテーブル駆動に置き換える
- React はコンポーネント分割とカスタムフックへの切り出し
構造的にどうしても下げられない場合だけ、理由を書いた抑制コメントを使う:
  Go  //nolint:gocognit // <なぜ下げられないか>
  TS  // eslint-disable-next-line sonarjs/cognitive-complexity -- <なぜ下げられないか>
理由なしの抑制は Go は nolintlint が、TS は PR の抑制コメント監視ジョブが拾う。
しきい値: .golangci-complexity.yml / web/tools/complexity/eslint.config.js'

if [ -n "$blocking" ]; then
  if retries_exhausted; then
    emit_context "fanout complexity: しきい値を超えていますが、このセッションで同じファイルを ${max_retries} 回差し戻したので助言に留めます。構造的に下げられないなら、理由を書いた抑制コメントを添えて先に進んでください。
${blocking}"
    exit 0
  fi
  record_attempts
  {
    echo "fanout complexity gate: この編集で複雑度のしきい値を超えました。"
    printf '%s' "$blocking"
    printf '%s\n' "$guidance"
  } >&2
  exit 2
fi

if [ -n "$advice" ]; then
  emit_context "fanout complexity: しきい値の手前です (ブロックしていません)。今のうちに構造を整えると後で楽になります。
${advice}"
fi

exit 0
