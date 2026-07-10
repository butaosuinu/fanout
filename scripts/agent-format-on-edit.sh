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

file="$(json_field "$input" file_path)"
[ -n "$file" ] || exit 0

dir="$(resolve_project_dir "$input")"
# The session may run from a subdirectory; the version pin, web/ tree, and
# .cache fallback all live at the repository root.
top="$(git -C "$dir" rev-parse --show-toplevel 2>/dev/null)"
[ -n "$top" ] && dir="$top"
case "$file" in
/*) ;;
*) file="$dir/$file" ;;
esac
[ -f "$file" ] || exit 0

# trusted_cache_root DIR — refuse a symlinked or foreign-owned shared cache
# before executing a binary out of it (same checks as the Makefile's
# prepare-dev-cache; the hook runs with the agent user's privileges).
trusted_cache_root() {
  local owner
  [ -L "$1" ] && return 1
  [ -d "$1" ] || return 1
  owner="$(stat -f '%u' "$1" 2>/dev/null || stat -c '%u' "$1" 2>/dev/null)" || return 1
  [ "$owner" = "$(id -u)" ]
}

case "$file" in
*.go)
  [ -f "$dir/.golangci-lint-version" ] || exit 0
  version="$(tr -d '[:space:]' <"$dir/.golangci-lint-version")"
  # Same resolution order as the Makefile: explicit override, local shared
  # cache (owner-validated), then the repo-local .cache the CI branch uses.
  bin="${GOLANGCI_LINT_BIN:-}"
  if [ -z "$bin" ] || [ ! -x "$bin" ]; then
    cache_root="${FANOUT_DEV_CACHE_DIR:-/tmp/fanout-dev-cache-$(id -u)}"
    if trusted_cache_root "$cache_root"; then
      bin="$cache_root/tools/golangci-lint-$version"
    else
      bin=""
    fi
  fi
  if [ -z "$bin" ] || [ ! -x "$bin" ]; then
    bin="$dir/.cache/tools/golangci-lint-$version"
  fi
  [ -x "$bin" ] || exit 0
  "$bin" fmt "$file" >/dev/null 2>&1 || true
  ;;
"$dir"/web/vite.config.ts | "$dir"/web/src/*.ts | "$dir"/web/src/*.tsx | "$dir"/web/src/*.js | "$dir"/web/src/*.jsx)
  [ -d "$dir/web/node_modules" ] || exit 0
  command -v pnpm >/dev/null 2>&1 || exit 0
  rel="${file#"$dir"/web/}"
  (cd "$dir/web" && pnpm exec oxfmt "$rel") >/dev/null 2>&1 || true
  ;;
esac

exit 0
