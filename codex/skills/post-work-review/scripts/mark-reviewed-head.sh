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
if [ -d "$repo_root/.codex" ]; then
  codex_symlink=$(find "$repo_root/.codex" -type l -print -quit) ||
    die "cannot inspect repository .codex directory"
  [ -z "$codex_symlink" ] || die "repository .codex files must not be symlinks: $codex_symlink"
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
}

guard_bootstrap_instructions() {
  set -- \
    ':(glob)AGENTS.md' \
    ':(glob)AGENTS.override.md' \
    ':(glob)**/AGENTS.md' \
    ':(glob)**/AGENTS.override.md' \
    ':(glob).codex' \
    ':(glob).codex/**' \
    ':(glob)**/.codex' \
    ':(glob)**/.codex/**'

  git diff --quiet --no-ext-diff --ignore-submodules=none \
    "$base_ref...$current_head" -- "$@" ||
    die "candidate changes Codex bootstrap instructions; use a trusted-checkout or human review"
  git diff --quiet --no-ext-diff --ignore-submodules=none -- "$@" ||
    die "worktree changes Codex bootstrap instructions; use a trusted-checkout or human review"
  git diff --cached --quiet --no-ext-diff --ignore-submodules=none -- "$@" ||
    die "worktree changes Codex bootstrap instructions; use a trusted-checkout or human review"
  [ -z "$(git ls-files --others -- "$@")" ] ||
    die "worktree adds Codex bootstrap instructions; use a trusted-checkout or human review"
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
      printf 'post_work_review_version=10\n'
      printf 'head=%s\n' "$current_head"
      printf 'base=%s\n' "$base"
      printf 'base_head=%s\n' "$current_base_head"
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
