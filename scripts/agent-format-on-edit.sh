#!/usr/bin/env bash
# fanout format-on-edit — PostToolUse(Edit|Write|MultiEdit) hook for Claude
# Code and Codex.
#
# Formats just the edited file, mirroring `make fmt` (pinned golangci-lint
# fmt for Go) and `make fmt-web` (oxfmt for web/src + vite.config.ts; CSS and
# web/-root JSON stay untouched). Fast paths only: the hook never downloads a
# tool — when the pinned golangci-lint binary or web/node_modules is absent it
# exits silently and the normal make targets pick the formatting up later.
# Always exits 0 so an edit is never interrupted by formatting.
set -u

input="$(cat)"

lib="$(cd "$(dirname "$0")" && pwd)/agent-hooks-lib.sh"
[ -f "$lib" ] || exit 0
# shellcheck source=scripts/agent-hooks-lib.sh
. "$lib"

cwd_base="$(resolve_project_dir "$input")"
# The session may run from a subdirectory; the version pin, web/ tree, and
# .cache fallback all live at the repository root, but relative edit paths
# resolve against the payload cwd.
dir="$cwd_base"
top="$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null)"
[ -n "$top" ] && dir="$top"

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
    esac
  done <<<"$patch"
fi
[ "${#files[@]}" -gt 0 ] || exit 0

go_bin=""
go_bin_resolved=0

for file in "${files[@]}"; do
  case "$file" in
  /*) ;;
  *) file="$cwd_base/$file" ;;
  esac
  [ -f "$file" ] || continue
  case "$file" in
  *.go)
    if [ "$go_bin_resolved" = "0" ]; then
      go_bin="$(golangci_bin "$dir")"
      go_bin_resolved=1
    fi
    [ -n "$go_bin" ] || continue
    "$go_bin" fmt "$file" >/dev/null 2>&1 || true
    ;;
  "$dir"/web/vite.config.ts | "$dir"/web/src/*.ts | "$dir"/web/src/*.tsx | "$dir"/web/src/*.js | "$dir"/web/src/*.jsx)
    [ -d "$dir/web/node_modules" ] || continue
    command -v pnpm >/dev/null 2>&1 || continue
    rel="${file#"$dir"/web/}"
    (cd "$dir/web" && pnpm exec oxfmt "$rel") >/dev/null 2>&1 || true
    ;;
  esac
done

exit 0
