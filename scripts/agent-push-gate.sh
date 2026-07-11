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
#
# Known accepted limits of the linear scan: subshell scoping and
# short-circuit control flow are not modeled — `(cd /x); git push` gates
# against /x, and a bypass behind `false &&` still counts as set. The Codex
# stop gate and CI remain the backstops for what the heuristic misses; the
# gate exists to stop forgetting, not a deliberate evader (that is what the
# sanctioned FANOUT_SKIP_PUSH_CHECK=1 is for).
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
# (a FANOUT_SKIP_PUSH_CHECK=1 assignment prefixes this command), SEG_GIT_C
# (newline-separated values of `git -C` in order), and SEG_REPO_SWITCH
# (--git-dir/--work-tree, a repo-hopping `env -C`/`-S`, or a push-affecting
# `git -c` override seen — the gate cannot follow those, so the caller fails
# closed).
parse_push_segment() {
  SEG_ARGS=""
  SEG_BYPASS=0
  SEG_GIT_C=""
  SEG_REPO_SWITCH=0
  # shellcheck disable=SC2086 # word splitting is the tokenizer here
  set -- $1
  while [ $# -gt 0 ]; do
    case "$1" in
    FANOUT_SKIP_PUSH_CHECK=1) SEG_BYPASS=1 ;;
    # Environment overrides that re-point git's repository/ref resolution
    # make the push untraceable: fail closed.
    GIT_DIR=* | GIT_WORK_TREE=* | GIT_COMMON_DIR=* | GIT_INDEX_FILE=* | GIT_NAMESPACE=* | GIT_OBJECT_DIRECTORY=* | GIT_CONFIG*=*) SEG_REPO_SWITCH=1 ;;
    [A-Za-z_]*=*) ;;
    env)
      # Consume env's own options so `env -i git push` is still a push.
      # env -C hops directories and env -S splices a command string; the
      # scan cannot follow either, so a push behind them fails closed.
      shift
      while [ $# -gt 0 ]; do
        case "$1" in
        -C | --chdir | -S | --split-string)
          SEG_REPO_SWITCH=1
          shift # the option; its value falls to the shift below
          ;;
        -C* | -S* | --chdir=* | --split-string=*) SEG_REPO_SWITCH=1 ;;
        -u) shift ;;
        --) shift; break ;;
        -*) ;;
        *) break ;;
        esac
        shift
      done
      continue
      ;;
    command | exec | nohup)
      # Consume the wrapper's own options and `--` so `command -- git push`
      # is still a push.
      shift
      while [ $# -gt 0 ]; do
        case "$1" in
        --) shift; break ;;
        -*) shift ;;
        *) break ;;
        esac
      done
      continue
      ;;
    # Shell control words: `if git push …; then` must still gate the push.
    if | then | elif | else | fi | do | done | while | until | ! | time) ;;
    # Leading redirections (`>log git push …`) are valid shell.
    \>* | [0-9]\>* | \<*)
      case "$1" in
      *\> | *\<)
        shift
        [ $# -gt 0 ] || return 1
        ;;
      esac
      ;;
    *) break ;;
    esac
    shift
  done
  # bash >= 4.4 with set -u errors on ${1##…} when $1 is unset (bash 3.2
  # tolerates it); guard the arity first.
  [ $# -gt 0 ] || return 1
  [ "${1##*/}" = "git" ] || return 1
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
    -C)
      shift
      [ $# -gt 0 ] || return 1
      SEG_GIT_C="${SEG_GIT_C}${1}"$'\n'
      ;;
    --git-dir | --work-tree | --git-dir=* | --work-tree=*)
      SEG_REPO_SWITCH=1
      case "$1" in --git-dir | --work-tree)
        shift
        [ $# -gt 0 ] || return 1
        ;;
      esac
      ;;
    -c | --config-env)
      shift
      [ $# -gt 0 ] || return 1
      # An inline config override can redirect what a push sends
      # (remote.*.push / push.* / mirror / pushRemote); fail closed on those.
      if push_affecting_config "$1"; then SEG_REPO_SWITCH=1; fi
      ;;
    -c?*)
      if push_affecting_config "${1#-c}"; then SEG_REPO_SWITCH=1; fi
      ;;
    --config-env=*)
      if push_affecting_config "${1#--config-env=}"; then SEG_REPO_SWITCH=1; fi
      ;;
    --namespace)
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

# push_affecting_config KEY[=VALUE] — config that changes what a push sends.
push_affecting_config() {
  case "$1" in
  push.* | *.push=* | *.push | *.pushurl=* | *.mirror=* | *.mirror | *.pushremote=* | *.pushRemote=*)
    return 0
    ;;
  esac
  return 1
}

# seg_ref_mutating SEGMENT — true when SEGMENT is a git command that can move
# local refs (or HEAD) before a later push in the same tool call. The gate
# resolves refs before the call runs, so a push after one of these cannot be
# validated.
seg_ref_mutating() {
  # shellcheck disable=SC2086 # word splitting is the tokenizer here
  set -- $1
  while [ $# -gt 0 ]; do
    case "$1" in
    [A-Za-z_]*=*) ;;
    env | command | exec | nohup) ;;
    if | then | elif | else | fi | do | done | while | until | ! | time) ;;
    *) break ;;
    esac
    shift
  done
  # bash >= 4.4 with set -u errors on ${1##…} when $1 is unset (bash 3.2
  # tolerates it); guard the arity first.
  [ $# -gt 0 ] || return 1
  [ "${1##*/}" = "git" ] || return 1
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
    -C | -c | --git-dir | --work-tree | --namespace | --config-env)
      shift
      [ $# -gt 0 ] || return 1
      ;;
    -*) ;;
    *) break ;;
    esac
    shift
  done
  case "${1:-}" in
  commit | merge | rebase | cherry-pick | revert | reset | am | pull | switch | checkout | fetch | branch | update-ref) return 0 ;;
  esac
  return 1
}

base_dir="$(resolve_project_dir "$input")"
segments="$(strip_shell_noise "$cmd")"

cmd_bypass=0
ref_mut=0

# First pass — command-wide facts. Substitution bodies are queued after the
# outer segments, so positional scanning would see a `git commit` inside
# `$(…)` only after the push; ref mutation and an exported bypass are
# command-wide either way.
while IFS= read -r seg; do
  case "$seg" in *[![:space:]]*) ;; *) continue ;; esac
  if seg_ref_mutating "$seg"; then
    ref_mut=1
    continue
  fi
  # shellcheck disable=SC2086
  set -- $seg
  if [ "${1:-}" = "export" ] && [ "${2:-}" = "FANOUT_SKIP_PUSH_CHECK=1" ]; then
    cmd_bypass=1
  fi
done <<<"$segments"

seg_dir="$base_dir" # follows `cd` segments so later pushes gate the right repo

while IFS= read -r seg; do
  case "$seg" in *[![:space:]]*) ;; *) continue ;; esac

  # shellcheck disable=SC2086
  set -- $seg
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
  if [ "$SEG_REPO_SWITCH" = "1" ]; then
    deny "--git-dir / --work-tree 経由の push はゲートが対象リポジトリを追跡できないため安全側で拒否します。対象リポジトリ内から通常の git push を実行してください。"
  fi

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
  repo_val_next=0
  tag_kw=0
  remote_w=""
  refspecs=()
  while IFS= read -r w; do
    [ -n "$w" ] || continue
    if [ "$skip_next" = "1" ]; then
      skip_next=0
      continue
    fi
    if [ "$repo_val_next" = "1" ]; then
      repo_val_next=0
      remote_w="$w"
      continue
    fi
    case "$w" in
    --delete | -d) gated=0 ;;
    --dry-run | -n) gated=0 ;; # side-effect-free probe push
    --all | --branches | --mirror) all_flag=1 ;;
    --tags) tags_flag=1 ;;
    -o | --push-option | --receive-pack | --exec) skip_next=1 ;;
    # --repo supplies the repository, so the next positional is a refspec.
    --repo)
      repo_val_next=1
      non_flag=$((non_flag + 1))
      ;;
    --repo=*)
      remote_w="${w#--repo=}"
      non_flag=$((non_flag + 1))
      ;;
    \>* | [0-9]\>* | \<*)
      case "$w" in *\> | *\<) skip_next=1 ;; esac
      ;;
    : | +:) all_flag=1 ;; # bare colon: matching push, every shared branch
    :* | +:*) deletions=$((deletions + 1)) ;;
    -*) ;;
    tag)
      if [ "$non_flag" -ge 1 ]; then
        tag_kw=1
      else
        non_flag=$((non_flag + 1))
        remote_w="$w"
      fi
      ;;
    *)
      non_flag=$((non_flag + 1))
      if [ "$non_flag" -eq 1 ]; then
        remote_w="$w"
      elif [ "$tag_kw" = "1" ]; then
        refspecs+=("refs/tags/$w")
        tag_kw=0
      else
        refspecs+=("$w")
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
    # No explicit refspec: git falls back to remote.<name>.push, then
    # push.default. A configured refspec set, a mirror remote, or `matching`
    # can push tips other than HEAD.
    if [ -z "$remote_w" ]; then
      # Git resolves the implicit remote as branch.<name>.pushRemote,
      # remote.pushDefault, branch.<name>.remote, then origin.
      cur_branch="$(git -C "$push_dir" symbolic-ref --short -q HEAD 2>/dev/null)"
      remote_w="$(git -C "$push_dir" config "branch.${cur_branch:-HEAD}.pushRemote" 2>/dev/null)"
      [ -n "$remote_w" ] || remote_w="$(git -C "$push_dir" config remote.pushDefault 2>/dev/null)"
      [ -n "$remote_w" ] || remote_w="$(git -C "$push_dir" config "branch.${cur_branch:-HEAD}.remote" 2>/dev/null)"
      remote_w="${remote_w:-origin}"
    fi
    cfg_specs="$(git -C "$push_dir" config --get-all "remote.$remote_w.push" 2>/dev/null)"
    if [ "$(git -C "$push_dir" config --bool "remote.$remote_w.mirror" 2>/dev/null)" = "true" ]; then
      # remote.<name>.mirror makes a bare `git push` mirror every ref.
      while IFS= read -r tip; do
        [ -n "$tip" ] || continue
        required+=("$tip")
      done < <(git -C "$push_dir" for-each-ref refs/heads --format='%(objectname)' 2>/dev/null | sort -u)
    elif [ -n "$cfg_specs" ]; then
      while IFS= read -r spec; do
        [ -n "$spec" ] || continue
        refspecs+=("$spec")
      done <<<"$cfg_specs"
    elif [ "$(git -C "$push_dir" config push.default 2>/dev/null)" = "matching" ]; then
      while IFS= read -r tip; do
        [ -n "$tip" ] || continue
        required+=("$tip")
      done < <(git -C "$push_dir" for-each-ref refs/heads --format='%(objectname)' 2>/dev/null | sort -u)
    else
      required+=("HEAD")
    fi
  fi
  if [ "${#required[@]}" -eq 0 ] && [ "${#refspecs[@]}" -gt 0 ]; then
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
        refs/*) ;;                    # explicit non-tag destination: gated
        *) continue ;;                # unqualified dst: git infers refs/tags/ from the tag source
        esac
      fi
      required+=("$src")
    done
    [ "${#required[@]}" -eq 0 ] && continue
  fi

  # Refs are resolved before this tool call runs; a commit/rebase earlier in
  # the same call would move them afterwards, making the validation stale.
  if [ "${#required[@]}" -gt 0 ] && [ "$ref_mut" = "1" ]; then
    deny "同一コマンド内で ref を変更するコマンド (commit / rebase 等) の後に push しています。gate は実行前の状態しか検証できないため拒否します。commit 後に make check を通し、push は単独のコマンドとして実行してください。"
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
