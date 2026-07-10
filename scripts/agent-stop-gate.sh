#!/usr/bin/env bash
# fanout stop gate — Codex-only Stop-hook backstop for the push gate.
#
# Codex's PreToolUse interception is documented as incomplete ("only the
# simple ones"), so a push can slip past scripts/agent-push-gate.sh. This hook
# runs at turn end: if the committed HEAD was never validated by `make check`,
# it runs the gate and, on failure, blocks the stop (exit 2) so the agent
# fixes the findings before finishing. Claude Code does not register this hook
# — its Bash interception is complete, the push gate alone covers it.
#
# Cost control: the gate is skipped when the tree is dirty (uncommitted work
# cannot reach CI), when the marker already matches HEAD (the normal
# make-check-before-push flow), and when HEAD is on the origin default branch
# (read-only sessions). stop_hook_active guards against continuation loops.
# Escape hatch: FANOUT_SKIP_STOP_GATE=1.
set -u

input="$(cat)"

if grep -Eq '"stop_hook_active"[[:space:]]*:[[:space:]]*true' <<<"$input"; then
  exit 0
fi
[ "${FANOUT_SKIP_STOP_GATE:-}" = "1" ] && exit 0

lib="$(cd "$(dirname "$0")" && pwd)/agent-hooks-lib.sh"
[ -f "$lib" ] || exit 0
# shellcheck source=scripts/agent-hooks-lib.sh
. "$lib"

dir="$(resolve_project_dir "$input")"
cd "$dir" 2>/dev/null || exit 0
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || exit 0
command -v make >/dev/null 2>&1 || exit 0
[ -f Makefile ] || exit 0

# Uncommitted work cannot be pushed; do not burn minutes on mid-work stops.
[ -n "$(git status --porcelain -uall 2>/dev/null)" ] && exit 0

head="$(head_sha "$dir")" || exit 0
[ -n "$head" ] || exit 0

# Already validated: `make check` wrote the marker for this exact commit.
[ "$(marker_sha "$dir")" = "$head" ] && exit 0

# Read-only sessions on the default branch: CI already validated this commit.
default_ref="$(git symbolic-ref -q --short refs/remotes/origin/HEAD 2>/dev/null)"
if [ -z "$default_ref" ]; then
  for candidate in origin/main origin/master; do
    if git rev-parse -q --verify "$candidate" >/dev/null 2>&1; then
      default_ref="$candidate"
      break
    fi
  done
fi
if [ -n "$default_ref" ] && git merge-base --is-ancestor HEAD "$default_ref" 2>/dev/null; then
  exit 0
fi

gitdir="$(git rev-parse --git-dir)"
case "$gitdir" in
/*) log="$gitdir/fanout-check.log" ;;
*) log="$dir/$gitdir/fanout-check.log" ;;
esac

if make check >"$log" 2>&1; then
  exit 0
fi

{
  echo "fanout stop gate: HEAD $head は make check に失敗しています。指摘を修正し、commit して、make check が通ってから終了してください。"
  echo "ログ全文: $log (末尾 150 行を以下に表示)"
  echo "-----"
  tail -n 150 "$log"
} >&2
exit 2
