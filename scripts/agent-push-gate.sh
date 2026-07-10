#!/usr/bin/env bash
# fanout push gate — PreToolUse hook for Claude Code and Codex.
#
# Denies `git push` to a branch unless the per-worktree marker
# $(git rev-parse --git-dir)/fanout-check-passed matches HEAD. The marker is
# written by a successful `make check` on a clean tree (Makefile check-marker),
# so a denied push means the pushed tip was never validated: run `make check`,
# then push again. Branch deletions and tag pushes stay ungated.
#
# Contract (PreToolUse hook, identical for both agents): stdin is the
# tool-call JSON ({"tool_name":"Bash","tool_input":{"command":…},"cwd":…}).
# Exit 0 allows the command; exit 2 + stderr denies it and feeds the message
# back to the agent. Any other exit is a non-blocking error (fail open), which
# is why this script uses `set -u` without `-e`.
#
# Detection is a staged shell heuristic, not a full tokenizer: quoted spans
# are stripped first so `git push` inside commit messages or --body text does
# not trigger the gate, then each pipeline segment is word-scanned for a real
# `git … push` command. Escape hatch: FANOUT_SKIP_PUSH_CHECK=1 (exported or
# inline). If the command cannot be extracted at all but the raw payload
# mentions `git push` in a Bash call, the gate fails closed.
set -u

[ "${FANOUT_SKIP_PUSH_CHECK:-}" = "1" ] && exit 0

input="$(cat)"

lib="$(cd "$(dirname "$0")" && pwd)/agent-hooks-lib.sh"
[ -f "$lib" ] || exit 0
# shellcheck source=scripts/agent-hooks-lib.sh
. "$lib"

cmd="$(json_field "$input" command)"
if [ -z "$cmd" ]; then
  if grep -Eq '"tool_name"[[:space:]]*:[[:space:]]*"Bash"' <<<"$input" &&
    grep -q 'git push' <<<"$input"; then
    echo "fanout push gate: Bash コマンドを解析できませんでしたが git push を含むため拒否します (fail closed)。単純な形の git push コマンドで再実行してください。" >&2
    exit 2
  fi
  exit 0
fi

case "$cmd" in
*FANOUT_SKIP_PUSH_CHECK=1*) exit 0 ;;
esac

# Strip quoted spans, then split on pipeline/list separators. Stripping only
# removes text, so it cannot fabricate a `git push` that was not already a
# command word (adjacent-token merges collapse into whitespace).
stripped="$(printf '%s' "$cmd" | sed -e "s/'[^']*'/ /g" -e 's/"[^"]*"/ /g')"
segments="$(printf '%s' "$stripped" | tr '|;&' '\n')"

# seg_push_args SEGMENT — if SEGMENT is a `git … push` command, print the
# words after `push` (one per line) and return 0.
seg_push_args() {
  # shellcheck disable=SC2086 # word splitting is the tokenizer here
  set -- $1
  while [ $# -gt 0 ]; do
    case "$1" in
    [A-Za-z_]*=*) shift ;; # env assignment prefix
    env | command | exec | nohup) shift ;;
    *) break ;;
    esac
  done
  [ "${1:-}" = "git" ] || return 1
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
    -C | -c | --git-dir | --work-tree | --namespace | --config-env)
      shift
      [ $# -gt 0 ] && shift
      ;;
    -*) shift ;;
    *) break ;;
    esac
  done
  [ "${1:-}" = "push" ] || return 1
  shift
  printf '%s\n' "$@"
}

# all_refspecs_are_tags DIR REFSPECS — release flows push existing tags; a
# push whose every source ref resolves under refs/tags/ stays ungated.
all_refspecs_are_tags() {
  local dir="$1" spec src
  shift
  [ $# -gt 0 ] || return 1
  for spec in "$@"; do
    src="${spec%%:*}"
    src="${src#+}"
    git -C "$dir" rev-parse -q --verify "refs/tags/$src" >/dev/null 2>&1 || return 1
  done
  return 0
}

needs_marker=0
while IFS= read -r seg; do
  [ -n "${seg// /}" ] || continue
  push_args="$(seg_push_args "$seg")" || continue
  gated=1
  non_flag=0
  deletions=0
  refspecs=()
  while IFS= read -r w; do
    [ -n "$w" ] || continue
    case "$w" in
    --delete | -d | --tags | --follow-tags) gated=0 ;;
    :*) deletions=$((deletions + 1)) ;; # delete refspec: no source ref
    -*) ;;
    *)
      non_flag=$((non_flag + 1))
      [ "$non_flag" -ge 2 ] && refspecs+=("$w")
      ;;
    esac
  done <<<"$push_args"
  [ "$gated" = "1" ] || continue
  if [ "${#refspecs[@]}" -gt 0 ]; then
    dir_for_tags="$(resolve_project_dir "$input")"
    all_refspecs_are_tags "$dir_for_tags" "${refspecs[@]}" && continue
  elif [ "$deletions" -gt 0 ]; then
    continue # deletion-only push (git push origin :branch)
  fi
  needs_marker=1
done <<<"$segments"

[ "$needs_marker" = "1" ] || exit 0

dir="$(resolve_project_dir "$input")"
git -C "$dir" rev-parse --is-inside-work-tree >/dev/null 2>&1 || exit 0
head="$(head_sha "$dir")" || exit 0
[ -n "$head" ] || exit 0
recorded="$(marker_sha "$dir")"

if [ "$recorded" != "$head" ]; then
  {
    echo "fanout push gate: この push は make check 未検証のため拒否しました。"
    echo "HEAD $head に対する marker が見つからないか一致しません (marker: ${recorded:-なし})。"
    echo "clean tree で make check を成功させてから push し直してください (成功時に marker が書かれます)。"
    echo "緊急回避: FANOUT_SKIP_PUSH_CHECK=1 git push ..."
  } >&2
  exit 2
fi

exit 0
