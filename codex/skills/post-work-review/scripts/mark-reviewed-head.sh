#!/bin/sh
set -eu

die() {
  printf 'post-work-review: %s\n' "$*" >&2
  exit 1
}

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "not inside a Git worktree"
case "$0" in
  /*) script_path=$0 ;;
  *) die "helper must be invoked by absolute path" ;;
esac
[ ! -L "$script_path" ] || die "helper path must not be a symlink"
script_dir=$(dirname "$script_path")
script_dir_logical=$(CDPATH='' cd "$script_dir" && pwd -L) || die "cannot resolve helper directory"
script_dir_physical=$(CDPATH='' cd "$script_dir" && pwd -P) || die "cannot resolve helper directory"
[ "$script_dir_logical" = "$script_dir_physical" ] || die "helper path must not traverse a symlink"
script_path="$script_dir_physical/$(basename "$script_path")"
repo_root=$(git rev-parse --show-toplevel) || die "cannot resolve repository root"
repo_root=$(CDPATH='' cd "$repo_root" && pwd -P) || die "cannot resolve repository root"
[ "$(pwd -P)" = "$repo_root" ] || die "helper must run from the repository root"
case "$script_path" in
  "$repo_root" | "$repo_root"/*)
    die "helper must be installed outside the reviewed repository"
    ;;
esac

for bootstrap_path in "$repo_root/AGENTS.md" "$repo_root/AGENTS.override.md" "$repo_root/.codex"; do
  [ ! -L "$bootstrap_path" ] ||
    die "Codex bootstrap paths must not be symlinks: $bootstrap_path"
done
agents_index=$(git ls-files --stage -- \
  ':(icase,glob)AGENTS.md' \
  ':(icase,glob)AGENTS.override.md' \
  ':(icase,glob)**/AGENTS.md' \
  ':(icase,glob)**/AGENTS.override.md') || die "cannot inspect repository AGENTS files"
case "$agents_index" in
  120000\ * | *'
'120000\ *) die "repository AGENTS files must not be symlinks" ;;
esac
unsupported_codex=$(git ls-files --cached --others -- \
  ':(icase,glob).codex' \
  ':(icase,glob).codex/**' \
  ':(icase,glob)**/.codex' \
  ':(icase,glob)**/.codex/**' \
  ':(exclude,glob).codex' \
  ':(exclude,glob).codex/**') || die "cannot inspect repository .codex paths"
[ -z "$unsupported_codex" ] ||
  die "case-variant or nested repository .codex paths are unsupported: $unsupported_codex"
if [ -d "$repo_root/.codex" ]; then
  codex_index=$(git ls-files --stage -- \
    ':(glob).codex' ':(glob).codex/**') || die "cannot inspect repository .codex directory"
  case "$codex_index" in
    120000\ * | *'
'120000\ *) die "repository .codex files must not be symlinks" ;;
  esac
fi
project_config="$repo_root/.codex/config.toml"
if [ -e "$project_config" ] && [ ! -f "$project_config" ]; then
  die "repository .codex/config.toml must be a regular file"
fi
if [ -f "$project_config" ]; then
  config_issue=$(LC_ALL=C awk '
    BEGIN {
      sq = sprintf("%c", 39); dq = "\""; bs = "\\"
      triple_sq = sq sq sq; triple_dq = dq dq dq
      mode = "code"
    }
    function reset_key() { bare = ""; dynamic = escaped = 0 }
    function report(issue) { print issue; reported = 1; exit }
    {
      line = $0; length_ = length(line); pos = 1
      if (mode == "code") reset_key()
      while (pos <= length_) {
        char = substr(line, pos, 1)
        if (mode != "code") {
          quote = mode == "multiline_basic" ? dq : sq
          if (mode == "multiline_basic" && char == bs) { pos += 2; continue }
          if (char != quote) { pos++; continue }
          run = 1
          while (pos + run <= length_ && substr(line, pos + run, 1) == quote) run++
          if (run >= 3) mode = "code"
          pos += run
          continue
        }
        if (char ~ /[A-Za-z0-9_-]/) {
          bare = bare char; pos++; continue
        }
        if (bare == "model_instructions_file" ||
            bare == "project_doc_fallback_filenames") {
          dynamic = 1
        }
        bare = ""
        if (char == "#") break
        if (char == dq || char == sq) {
          triple = char == dq ? triple_dq : triple_sq
          if (substr(line, pos, 3) == triple) {
            mode = char == dq ? "multiline_basic" : "multiline_literal"
            pos += 3; continue
          }
          quote = char; text = ""; quote_escaped = 0; pos++
          while (pos <= length_) {
            char = substr(line, pos, 1)
            if (quote == dq && char == bs) {
              quote_escaped = 1; pos += 2; continue
            }
            if (char == quote) break
            text = text char; pos++
          }
          if (pos > length_) report("ambiguous")
          next_pos = pos + 1
          while (next_pos <= length_ &&
                 (substr(line, next_pos, 1) == " " ||
                  substr(line, next_pos, 1) == "\t")) next_pos++
          following = substr(line, next_pos, 1)
          if (following == "." || following == "=") {
            if (quote_escaped) escaped = 1
            else if (text == "model_instructions_file" ||
                     text == "project_doc_fallback_filenames") {
              dynamic = 1
            }
          }
          pos++; continue
        }
        if (char == "=") {
          if (escaped) report("escaped")
          if (dynamic) report("dynamic")
          reset_key()
        } else if (index(",{}[]", char)) reset_key()
        pos++
      }
      if (mode == "code") reset_key()
    }
    END { if (!reported && mode != "code") print "ambiguous" }
  ' "$project_config") || die "cannot inspect repository .codex/config.toml"
  case "$config_issue" in
    dynamic)
      die "repository .codex/config.toml uses unsupported dynamic instruction sources"
      ;;
    escaped)
      die "repository .codex/config.toml uses unsupported escaped keys"
      ;;
    ambiguous)
      die "cannot safely inspect repository .codex/config.toml"
      ;;
    "") ;;
    *) die "cannot inspect repository .codex/config.toml" ;;
  esac
fi

git_dir=$(git rev-parse --absolute-git-dir) || die "cannot resolve Git directory"
marker="$git_dir/post-work-review-passed"
metadata="$marker.meta"

load_target() {
  expected_head=$1
  base=$2
  expected_base_head=$3
  current_head=$(git rev-parse HEAD) || die "cannot resolve HEAD"
  [ "$current_head" = "$expected_head" ] || die "HEAD changed during review"

  case "$base" in
    refs/remotes/origin/*) base=${base#refs/remotes/origin/} ;;
    origin/*) base=${base#origin/} ;;
    refs/heads/*) base=${base#refs/heads/} ;;
  esac
  [ -n "$base" ] || die "base branch is empty"
  base_ref="refs/remotes/origin/$base"
  current_base_head=$(git rev-parse --verify "$base_ref^{commit}") || die "cannot resolve $base_ref"
  [ "$current_base_head" = "$expected_base_head" ] || die "base changed during review"
  bootstrap_base=$(git merge-base "$base_ref" "$current_head") ||
    die "cannot resolve trusted bootstrap base"
}

guard_bootstrap_instructions() {
  set -- \
    ':(icase,glob)AGENTS.md' \
    ':(icase,glob)AGENTS.override.md' \
    ':(icase,glob)**/AGENTS.md' \
    ':(icase,glob)**/AGENTS.override.md' \
    ':(icase,glob).codex' \
    ':(icase,glob).codex/**' \
    ':(icase,glob)**/.codex' \
    ':(icase,glob)**/.codex/**' \
    ':(icase,glob)codex/skills/post-work-review' \
    ':(icase,glob)codex/skills/post-work-review/**'

  index_state=$(git ls-files -v -- "$@") || die "cannot inspect Git index flags"
  if printf '%s\n' "$index_state" | LC_ALL=C grep -Eq '^[a-zS] '; then
    die "Git index uses unsupported assume-unchanged or skip-worktree flags"
  else
    index_status=$?
    [ "$index_status" -eq 1 ] || die "cannot inspect Git index flags"
  fi

  git diff --quiet --no-ext-diff --ignore-submodules=none \
    "$bootstrap_base" "$current_head" -- "$@" ||
    die "candidate changes Codex bootstrap instructions or the post-work-review gate; use a trusted-checkout or human review"
  git diff --quiet --no-ext-diff --ignore-submodules=none -- "$@" ||
    die "worktree changes Codex bootstrap instructions or the post-work-review gate; use a trusted-checkout or human review"
  git diff --cached --quiet --no-ext-diff --ignore-submodules=none -- "$@" ||
    die "worktree changes Codex bootstrap instructions or the post-work-review gate; use a trusted-checkout or human review"
  [ -z "$(git ls-files --others -- "$@")" ] ||
    die "worktree adds Codex bootstrap instructions or post-work-review gate files; use a trusted-checkout or human review"

  candidate_gitlinks=$(git diff --raw --no-renames --no-ext-diff --ignore-submodules=none \
    "$bootstrap_base" "$current_head" --) || die "cannot inspect candidate submodule changes"
  case "$candidate_gitlinks" in
    *":160000 "* | *" 160000 "*)
      die "candidate changes submodules; use a trusted-checkout or human review"
      ;;
  esac

  worktree_gitlinks=$(git diff --raw --no-renames --no-ext-diff --ignore-submodules=none --) ||
    die "cannot inspect worktree submodule changes"
  cached_gitlinks=$(git diff --cached --raw --no-renames --no-ext-diff --ignore-submodules=none --) ||
    die "cannot inspect staged submodule changes"
  case "$worktree_gitlinks$cached_gitlinks" in
    *":160000 "* | *" 160000 "*)
      die "worktree changes submodules; use a trusted-checkout or human review"
      ;;
  esac
}

case "${1:-}" in
  clear)
    [ "$#" -eq 1 ] || die "usage: $0 clear"
    rm -f "$marker" "$metadata"
    ;;
  guard)
    [ "$#" -eq 4 ] || die "usage: $0 guard <expected-head> <base-branch> <expected-base-head>"
    load_target "$2" "$3" "$4"
    guard_bootstrap_instructions
    ;;
  mark)
    [ "$#" -eq 4 ] || die "usage: $0 mark <expected-head> <base-branch> <expected-base-head>"
    load_target "$2" "$3" "$4"
    [ -z "$(git status --porcelain -uall --ignore-submodules=none)" ] || die "working tree is dirty"
    guard_bootstrap_instructions

    umask 077
    diff_file=$(mktemp "${TMPDIR:-/tmp}/post-work-review-diff.XXXXXX") ||
      die "cannot create temporary diff"
    marker_tmp=$(mktemp "$marker.tmp.XXXXXX") || {
      rm -f "$diff_file"
      die "cannot create marker temporary file"
    }
    metadata_tmp=$(mktemp "$metadata.tmp.XXXXXX") || {
      rm -f "$diff_file" "$marker_tmp"
      die "cannot create metadata temporary file"
    }
    trap 'rm -f "$diff_file" "$marker_tmp" "$metadata_tmp"' EXIT HUP INT TERM

    git diff --no-ext-diff --no-textconv --ignore-submodules=none --no-color \
      --binary "$base_ref...$current_head" -- >"$diff_file" ||
      die "cannot build review diff"
    diff_hash=$(git hash-object "$diff_file") || die "cannot hash review diff"
    printf '%s\n' "$current_head" >"$marker_tmp"
    {
      printf 'post_work_review_version=13\n'
      printf 'head=%s\n' "$current_head"
      printf 'base=%s\n' "$base"
      printf 'base_head=%s\n' "$current_base_head"
      printf 'bootstrap_base=%s\n' "$bootstrap_base"
      printf 'diff_hash=%s\n' "$diff_hash"
    } >"$metadata_tmp"

    rm -f "$marker" "$metadata"
    mv "$metadata_tmp" "$metadata"
    mv "$marker_tmp" "$marker"
    rm -f "$diff_file"
    trap - EXIT HUP INT TERM
    ;;
  *)
    die "usage: $0 <clear|guard|mark>"
    ;;
esac
