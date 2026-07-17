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

case "${1:-}" in
  clear)
    [ "$#" -eq 1 ] || die "usage: $0 clear"
    rm -f "$marker" "$metadata"
    ;;
  mark)
    [ "$#" -eq 4 ] || die "usage: $0 mark <expected-head> <base-branch> <expected-base-head>"
    expected_head=$2
    base=$3
    expected_base_head=$4
    current_head=$(git rev-parse HEAD) || die "cannot resolve HEAD"
    [ "$current_head" = "$expected_head" ] || die "HEAD changed during review"
    [ -z "$(git status --porcelain -uall --ignore-submodules=none)" ] || die "working tree is dirty"

    case "$base" in
      refs/remotes/origin/*) base=${base#refs/remotes/origin/} ;;
      origin/*) base=${base#origin/} ;;
      refs/heads/*) base=${base#refs/heads/} ;;
    esac
    [ -n "$base" ] || die "base branch is empty"
    base_ref="refs/remotes/origin/$base"
    current_base_head=$(git rev-parse --verify "$base_ref^{commit}") || die "cannot resolve $base_ref"
    [ "$current_base_head" = "$expected_base_head" ] || die "base changed during review"

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
      printf 'post_work_review_version=8\n'
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
    die "usage: $0 <clear|mark>"
    ;;
esac
