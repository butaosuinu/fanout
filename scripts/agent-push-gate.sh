#!/usr/bin/env bash
# fanout push gate — PreToolUse hook for Claude Code and Codex.
#
# Denies `git push` of a branch tip that `make check` has not validated. Every
# pushed source ref is resolved to its commit in the repository the push
# actually targets (payload cwd, adjusted for `cd` segments and `git -C`), and
# each one must equal the per-worktree marker
# $(git rev-parse --git-dir)/fanout-check-passed, written by a successful
# `make check` on a clean tree (Makefile check-marker). Deletions, tag-to-tag
# pushes, and --dry-run stay ungated.
#
# Contract (PreToolUse hook, identical for both agents): stdin is the
# tool-call JSON ({"tool_name":"Bash","tool_input":{"command":…},"cwd":…}).
# Exit 0 allows the command; exit 2 + stderr denies it and feeds the message
# back to the agent. Any other exit is a non-blocking error (fail open), which
# is why this script uses `set -u` without `-e`.
#
# Detection is a staged shell heuristic, not a shell: strip_shell_noise
# (agent-hooks-lib.sh) removes quoted spans and heredoc bodies and splits on
# separators, then each simple command is word-scanned for `git … push`.
# Unknown constructs must degrade toward a false deny, never a false allow.
# Escape hatch: FANOUT_SKIP_PUSH_CHECK=1 — exported, or as an assignment
# prefixing the push command itself (a mention elsewhere in the command does
# not count). If the command cannot be extracted at all but the raw payload
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

deny() {
  {
    echo "fanout push gate: この push は make check 未検証のため拒否しました。"
    echo "$1"
    echo "clean tree で make check を成功させてから push し直してください (成功時に marker が書かれます)。"
    echo "緊急回避: FANOUT_SKIP_PUSH_CHECK=1 git push ..."
  } >&2
  exit 2
}

# parse_push_segment SEGMENT — return 0 when SEGMENT is a `git … push`
# command. Sets SEG_ARGS (words after `push`, newline-separated), SEG_BYPASS
# (a FANOUT_SKIP_PUSH_CHECK=1 assignment prefixes this command), and
# SEG_GIT_C (newline-separated values of `git -C` in order).
parse_push_segment() {
  SEG_ARGS=""
  SEG_BYPASS=0
  SEG_GIT_C=""
  # shellcheck disable=SC2086 # word splitting is the tokenizer here
  set -- $1
  while [ $# -gt 0 ]; do
    case "$1" in
    FANOUT_SKIP_PUSH_CHECK=1) SEG_BYPASS=1 ;;
    [A-Za-z_]*=*) ;;
    env | command | exec | nohup) ;;
    # Shell control words: `if git push …; then` must still gate the push.
    if | then | elif | else | fi | do | done | while | until | ! | time) ;;
    *) break ;;
    esac
    shift
  done
  [ "${1:-}" = "git" ] || return 1
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
    -C)
      shift
      [ $# -gt 0 ] || return 1
      SEG_GIT_C="${SEG_GIT_C}${1}"$'\n'
      ;;
    -c | --git-dir | --work-tree | --namespace | --config-env)
      shift
      [ $# -gt 0 ] || return 1
      ;;
    -*) ;;
    *) break ;;
    esac
    shift
  done
  [ "${1:-}" = "push" ] || return 1
  shift
  SEG_ARGS="$(printf '%s\n' "$@")"
  return 0
}

# abs_dir BASE PATH — PATH absolutized against BASE (no existence check).
abs_dir() {
  case "$2" in
  /*) printf '%s' "$2" ;;
  *) printf '%s/%s' "$1" "$2" ;;
  esac
}

base_dir="$(resolve_project_dir "$input")"
segments="$(strip_shell_noise "$cmd")"

cmd_bypass=0
seg_dir="$base_dir" # follows `cd` segments so later pushes gate the right repo

while IFS= read -r seg; do
  case "$seg" in *[![:space:]]*) ;; *) continue ;; esac

  # shellcheck disable=SC2086
  set -- $seg
  if [ "${1:-}" = "export" ] && [ "${2:-}" = "FANOUT_SKIP_PUSH_CHECK=1" ]; then
    cmd_bypass=1
    continue
  fi
  if [ "${1:-}" = "cd" ] || [ "${1:-}" = "pushd" ]; then
    if [ -n "${2:-}" ]; then
      seg_dir="$(abs_dir "$seg_dir" "$(unsentinel "$2")")"
    else
      seg_dir="$HOME"
    fi
    continue
  fi

  parse_push_segment "$seg" || continue
  [ "$SEG_BYPASS" = "1" ] && continue
  [ "$cmd_bypass" = "1" ] && continue

  push_dir="$seg_dir"
  while IFS= read -r c_path; do
    [ -n "$c_path" ] || continue
    push_dir="$(abs_dir "$push_dir" "$(unsentinel "$c_path")")"
  done <<<"$SEG_GIT_C"

  gated=1
  tags_flag=0
  all_flag=0
  deletions=0
  non_flag=0
  skip_next=0
  tag_kw=0
  refspecs=()
  while IFS= read -r w; do
    [ -n "$w" ] || continue
    if [ "$skip_next" = "1" ]; then
      skip_next=0
      continue
    fi
    case "$w" in
    --delete | -d) gated=0 ;;
    --dry-run | -n) gated=0 ;; # side-effect-free probe push
    --all | --branches | --mirror) all_flag=1 ;;
    --tags) tags_flag=1 ;;
    -o | --push-option | --receive-pack | --exec | --repo) skip_next=1 ;;
    \>* | [0-9]\>* | \<*)
      case "$w" in *\> | *\<) skip_next=1 ;; esac
      ;;
    :*) deletions=$((deletions + 1)) ;;
    -*) ;;
    tag)
      if [ "$non_flag" -ge 1 ]; then tag_kw=1; else non_flag=$((non_flag + 1)); fi
      ;;
    *)
      non_flag=$((non_flag + 1))
      if [ "$non_flag" -ge 2 ]; then
        if [ "$tag_kw" = "1" ]; then
          refspecs+=("refs/tags/$w")
          tag_kw=0
        else
          refspecs+=("$w")
        fi
      fi
      ;;
    esac
  done <<<"$SEG_ARGS"
  [ "$gated" = "1" ] || continue

  # A detected push whose repository cannot be resolved (a `cd` to a missing
  # path, an unexpanded variable) is unverifiable — fail closed.
  if ! git -C "$push_dir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    deny "push 先リポジトリを解決できませんでした ($push_dir)。変数や存在しないパスを経由した push は検証できないため安全側で拒否します。"
  fi
  recorded="$(marker_sha "$push_dir")"

  required=()
  if [ "$all_flag" = "1" ]; then
    # --all / --branches / --mirror push every local branch tip.
    while IFS= read -r tip; do
      [ -n "$tip" ] || continue
      required+=("$tip")
    done < <(git -C "$push_dir" for-each-ref refs/heads --format='%(objectname)' 2>/dev/null | sort -u)
    if [ "${#required[@]}" -eq 0 ]; then
      deny "--all/--mirror push の branch tip を列挙できませんでした ($push_dir)。"
    fi
  elif [ "${#refspecs[@]}" -eq 0 ]; then
    if [ "$deletions" -gt 0 ] || [ "$tags_flag" = "1" ]; then
      continue # deletion-only push or pure `git push --tags [remote]`
    fi
    required+=("HEAD")
  else
    for spec in "${refspecs[@]}"; do
      src="${spec%%:*}"
      src="${src#+}"
      dst=""
      case "$spec" in *:*) dst="${spec#*:}" ;; esac
      tag_ref=""
      case "$src" in
      refs/tags/*) tag_ref="$src" ;;
      *)
        if git -C "$push_dir" rev-parse -q --verify "refs/tags/$src" >/dev/null 2>&1; then
          tag_ref="refs/tags/$src"
        fi
        ;;
      esac
      if [ -n "$tag_ref" ]; then
        case "$dst" in
        "" | refs/tags/*) continue ;; # release flow: tag pushed as a tag
        esac
      fi
      required+=("$src")
    done
    [ "${#required[@]}" -eq 0 ] && continue
  fi

  for src in "${required[@]}"; do
    sha="$(git -C "$push_dir" rev-parse -q --verify "$src^{commit}" 2>/dev/null)" || sha=""
    if [ -z "$sha" ]; then
      deny "push 対象 $src を $push_dir で解決できませんでした (marker 照合不能のため安全側で拒否)。"
    fi
    if [ "$sha" != "$recorded" ]; then
      deny "push 対象 $src ($sha) に対する marker が見つからないか一致しません (marker: ${recorded:-なし}, repo: $push_dir)。"
    fi
  done
done <<<"$segments"

exit 0
