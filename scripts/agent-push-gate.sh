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
# Known accepted limits of the linear scan (by design — the gate exists to
# stop forgetting, not a deliberate evader; that is what the sanctioned
# FANOUT_SKIP_PUSH_CHECK=1 is for, and the Codex stop gate and CI remain the
# backstops):
# - subshell scoping and short-circuit control flow are not modeled —
#   `(cd /x); git push` gates against /x, and a bypass behind `false &&`
#   still counts as set (lexical order of exports is likewise ignored).
# - a script fed to a shell via stdin (`bash <<EOF … EOF`) is not scanned.
# - config that only re-points the REMOTE of a validated tip
#   (`-c branch.<name>.remote=…`) is not tracked: the pushed commit itself
#   was validated.
#
# Wrapper/option coverage is intentionally broad but not exhaustive: the
# scanner transparently steps over env (incl. -C/-S/-u/--unset), command,
# exec, nohup, and timeout (with its DURATION), treats bash/sh/eval/env -S
# splices and `--git-dir`/`--work-tree`/`--namespace`/GIT_* repo switches as
# fail-closed, and flags ref-moving git subcommands before a same-call push.
# A genuinely novel wrapper is a false-allow gap the backstops cover, not a
# security boundary.
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
    env | */env)
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
        -u | --unset) shift ;; # takes a value (the next word)
        --unset=*) ;;
        --) shift; break ;;
        -*) ;;
        *) break ;;
        esac
        shift
      done
      continue
      ;;
    timeout | */timeout)
      # timeout [OPTION]... DURATION COMMAND …: skip options and DURATION so
      # `timeout 30 git push` is still a push.
      shift
      while [ $# -gt 0 ]; do
        case "$1" in
        -s | --signal | -k | --kill-after) shift; [ $# -gt 0 ] && shift ;;
        --) shift; [ $# -gt 0 ] && shift; break ;;
        -*) shift ;;
        *) shift; break ;; # DURATION operand
        esac
      done
      continue
      ;;
    command | exec | nohup | */nohup)
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
    --namespace | --namespace=*)
      # --namespace re-points refs under refs/namespaces/<name>/: untraceable.
      SEG_REPO_SWITCH=1
      case "$1" in --namespace)
        shift
        [ $# -gt 0 ] || return 1
        ;;
      esac
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
# include.path can pull any of the other keys in from a file, so it counts.
push_affecting_config() {
  case "$1" in
  push.* | *.push=* | *.push | *.pushurl=* | *.mirror=* | *.mirror | *.pushremote=* | *.pushRemote=* | remote.pushdefault=* | remote.pushDefault=* | include.path=* | includeIf.* | includeif.*)
    return 0
    ;;
  esac
  return 1
}

# seg_inner_shell_push SEGMENT — true when SEGMENT hands a command string to
# an inner shell (`bash -c …` / `sh -lc …`) or to `env -S '…'` that mentions
# a git push. The scanner cannot follow the spliced string, so the caller
# fails closed.
seg_inner_shell_push() {
  local has_c=0 w joined
  # shellcheck disable=SC2086 # word splitting is the tokenizer here
  set -- $1
  while [ $# -gt 0 ]; do
    case "$1" in
    [A-Za-z_]*=*) ;;
    command | exec | nohup | time | */nohup | */time) ;;
    if | then | elif | else | fi | do | done | while | until | !) ;;
    timeout | */timeout)
      shift
      while [ $# -gt 0 ]; do
        case "$1" in
        -s | --signal | -k | --kill-after) shift; [ $# -gt 0 ] && shift ;;
        --) shift; [ $# -gt 0 ] && shift; break ;;
        -*) shift ;;
        *) shift; break ;;
        esac
      done
      continue
      ;;
    env | */env)
      shift
      while [ $# -gt 0 ]; do
        case "$1" in
        -S | --split-string)
          has_c=1
          shift
          break # the spliced string follows; keep it for the scan below
          ;;
        -S* | --split-string=*)
          has_c=1
          break # value embedded in the token; keep it for the scan below
          ;;
        -u | -C | --chdir)
          shift
          [ $# -gt 0 ] && shift
          ;;
        --)
          shift
          break
          ;;
        -*) shift ;;
        *) break ;;
        esac
      done
      continue
      ;;
    *) break ;;
    esac
    shift
  done
  if [ "$has_c" = "0" ]; then
    [ $# -gt 0 ] || return 1
    case "${1##*/}" in
    bash | sh | zsh | dash | ksh)
      shift
      for w in "$@"; do
        case "$w" in
        -*c*) has_c=1 ;;
        esac
      done
      ;;
    eval)
      # eval executes its (usually quoted) arguments in the current shell.
      shift
      has_c=1
      ;;
    *) return 1 ;;
    esac
  fi
  [ "$has_c" = "1" ] || return 1
  joined="$(unsentinel "$*")"
  case "$joined" in
  *git*push* | *gh*pr*create* | *gh*pr*new*) return 0 ;;
  esac
  return 1
}

# seg_gh_pr_create SEGMENT — true when SEGMENT is a `gh pr create|new`. gh
# pushes the current branch itself when it is not fully pushed, so PR
# creation is a push path (Codex has no other gate on it; for Claude this
# runs alongside the pre-pr-review gate, whose flow already produced the
# marker).
seg_gh_pr_create() {
  local pos1="" pos2=""
  SEG_GH_UNSAFE=0
  # shellcheck disable=SC2086 # word splitting is the tokenizer here
  set -- $1
  while [ $# -gt 0 ]; do
    case "$1" in
    [A-Za-z_]*=*) ;;
    env | */env)
      shift
      while [ $# -gt 0 ]; do
        case "$1" in
        -C | --chdir | -S | --split-string)
          # gh would run in a directory (or via a splice) the gate cannot
          # follow: the caller fails closed.
          SEG_GH_UNSAFE=1
          shift
          [ $# -gt 0 ] && shift
          ;;
        -C* | -S* | --chdir=* | --split-string=*)
          SEG_GH_UNSAFE=1
          shift
          ;;
        -u)
          shift
          [ $# -gt 0 ] && shift
          ;;
        --)
          shift
          break
          ;;
        -*) shift ;;
        *) break ;;
        esac
      done
      continue
      ;;
    command | exec | nohup | time | */nohup | */time) ;;
    timeout | */timeout)
      shift
      while [ $# -gt 0 ]; do
        case "$1" in
        -s | --signal | -k | --kill-after) shift; [ $# -gt 0 ] && shift ;;
        --) shift; [ $# -gt 0 ] && shift; break ;;
        -*) shift ;;
        *) shift; break ;;
        esac
      done
      continue
      ;;
    if | then | elif | else | fi | do | done | while | until | !) ;;
    *) break ;;
    esac
    shift
  done
  [ $# -gt 0 ] || return 1
  [ "${1##*/}" = "gh" ] || return 1
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
    -R | --repo)
      shift
      [ $# -gt 0 ] || return 1
      ;;
    -*) ;;
    *)
      if [ -z "$pos1" ]; then
        pos1="$1"
      elif [ -z "$pos2" ]; then
        pos2="$1"
        break
      fi
      ;;
    esac
    shift
  done
  [ "$pos1" = "pr" ] || return 1
  case "$pos2" in
  create | new) return 0 ;;
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
    command | exec | nohup | */nohup) ;;
    if | then | elif | else | fi | do | done | while | until | ! | time) ;;
    env | */env)
      # Consume env's options so `env -C repo git commit` is still a mutation.
      shift
      while [ $# -gt 0 ]; do
        case "$1" in
        -C | --chdir | -S | --split-string | -u | --unset) shift; [ $# -gt 0 ] && shift ;;
        -C* | -S* | --chdir=* | --split-string=* | --unset=*) shift ;;
        --) shift; break ;;
        -*) shift ;;
        *) break ;;
        esac
      done
      continue
      ;;
    timeout | */timeout)
      shift
      while [ $# -gt 0 ]; do
        case "$1" in
        -s | --signal | -k | --kill-after) shift; [ $# -gt 0 ] && shift ;;
        --) shift; [ $# -gt 0 ] && shift; break ;;
        -*) shift ;;
        *) shift; break ;;
        esac
      done
      continue
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
  # config, tag, and symbolic-ref do not move branch tips, but they change
  # what a later push in the same call sends (push refspecs, a re-pointed tag
  # source, or a switched HEAD symref).
  commit | merge | rebase | cherry-pick | revert | reset | am | pull | switch | checkout | fetch | branch | update-ref | config | tag | symbolic-ref) return 0 ;;
  esac
  return 1
}

base_dir="$(resolve_project_dir "$input")"
segments="$(strip_shell_noise "$cmd")"

cmd_bypass=0
cmd_repo_switch=0
ref_mut=0

# First pass — command-wide facts. Substitution bodies are queued after the
# outer segments, so positional scanning would see a `git commit` inside
# `$(…)` only after the push; ref mutation and exported state are
# command-wide either way.
while IFS= read -r seg; do
  case "$seg" in *[![:space:]]*) ;; *) continue ;; esac
  if seg_ref_mutating "$seg"; then
    ref_mut=1
    continue
  fi
  # shellcheck disable=SC2086
  set -- $seg
  if [ "${1:-}" = "export" ]; then
    shift
    for w in "$@"; do
      case "$w" in
      FANOUT_SKIP_PUSH_CHECK=1) cmd_bypass=1 ;;
      # Exported repo-pointing env vars redirect every later git call.
      GIT_DIR=* | GIT_WORK_TREE=* | GIT_COMMON_DIR=* | GIT_INDEX_FILE=* | GIT_NAMESPACE=* | GIT_OBJECT_DIRECTORY=* | GIT_CONFIG*=*) cmd_repo_switch=1 ;;
      esac
    done
  fi
done <<<"$segments"

seg_dir="$base_dir" # follows `cd` segments so later pushes gate the right repo
dir_stack=()        # pushd/popd

while IFS= read -r seg; do
  case "$seg" in *[![:space:]]*) ;; *) continue ;; esac

  if seg_inner_shell_push "$seg"; then
    deny "インナーシェル (bash -c / sh -c) 経由の push はゲートが検証できないため拒否します。git push を直接実行してください。"
  fi

  if seg_gh_pr_create "$seg"; then
    if [ "$SEG_GH_UNSAFE" = "1" ]; then
      deny "env -C / -S 経由の gh pr create はゲートが対象リポジトリを追跡できないため拒否します。対象リポジトリ内から直接 gh pr create を実行してください。"
    fi
    gh_dir="$seg_dir"
    if git -C "$gh_dir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
      gh_head="$(head_sha "$gh_dir")"
      if [ -n "$gh_head" ] && [ "$(marker_sha "$gh_dir")" != "$gh_head" ]; then
        deny "gh pr create は未 push の branch を自動 push します。HEAD $gh_head は make check 未検証です。"
      fi
    fi
    continue
  fi

  # shellcheck disable=SC2086
  set -- $seg
  if [ "${1:-}" = "cd" ]; then
    if [ -n "${2:-}" ]; then
      seg_dir="$(abs_dir "$seg_dir" "$(unsentinel "$2")")"
    else
      seg_dir="$HOME"
    fi
    continue
  fi
  if [ "${1:-}" = "pushd" ]; then
    if [ -n "${2:-}" ]; then
      dir_stack+=("$seg_dir")
      seg_dir="$(abs_dir "$seg_dir" "$(unsentinel "$2")")"
    elif [ "${#dir_stack[@]}" -gt 0 ]; then
      # bare pushd swaps the top two directory-stack entries
      top_idx=$((${#dir_stack[@]} - 1))
      swap="${dir_stack[$top_idx]}"
      dir_stack[top_idx]="$seg_dir"
      seg_dir="$swap"
    fi
    continue
  fi
  if [ "${1:-}" = "popd" ]; then
    if [ "${#dir_stack[@]}" -gt 0 ]; then
      top_idx=$((${#dir_stack[@]} - 1))
      seg_dir="${dir_stack[$top_idx]}"
      unset "dir_stack[$top_idx]"
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
  mirror_flag=0
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
    --all | --branches) all_flag=1 ;;
    --mirror) mirror_flag=1 ;;
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

  # --mirror sends every ref (backups, remotes, notes), which the marker
  # cannot vouch for: fail closed.
  if [ "$mirror_flag" = "1" ] && [ "$gated" = "1" ]; then
    deny "--mirror push は refs/heads 以外の全 ref も送るため gate で検証できません。"
  fi

  required=()
  if [ "$all_flag" = "1" ]; then
    # --all / --branches push every local branch tip.
    while IFS= read -r tip; do
      [ -n "$tip" ] || continue
      required+=("$tip")
    done < <(git -C "$push_dir" for-each-ref refs/heads --format='%(objectname)' 2>/dev/null | sort -u)
    if [ "${#required[@]}" -eq 0 ]; then
      deny "--all push の branch tip を列挙できませんでした ($push_dir)。"
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
      deny "remote.$remote_w.mirror=true の push は全 ref を送るため gate で検証できません。"
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
        # An unqualified <dst> can expand to an existing remote branch, so a
        # tag source with a non-refs/tags destination stays gated.
        *) ;;
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
  if [ "${#required[@]}" -gt 0 ] && [ "$cmd_repo_switch" = "1" ]; then
    deny "同一コマンド内で GIT_DIR / GIT_WORK_TREE 等が export されており、push 先リポジトリを追跡できません。export せずに対象リポジトリ内から git push を実行してください。"
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
