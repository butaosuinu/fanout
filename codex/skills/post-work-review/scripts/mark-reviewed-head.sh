#!/bin/sh
set -eu

die() {
  printf 'post-work-review: %s\n' "$*" >&2
  exit 1
}

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "not inside a Git worktree"
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
      printf 'post_work_review_version=9\n'
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
