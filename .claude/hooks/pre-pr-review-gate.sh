#!/usr/bin/env bash
# fanout PR-review gate — PreToolUse(Bash) hook (thin wrapper).
#
# Blocks `gh pr create` / `gh pr new` unless the commit the PR is built from has
# been signed off by /post-work-review. The real decision is made by the
# companion parser pre-pr-review-gate.py, which uses a proper shell tokenizer
# (shlex) so command words are distinguished from quoted argument values and
# shell structure (operators, if/while/until/case, `!`, wrappers, command
# substitution) is understood rather than pattern-matched.
#
# Contract (Claude Code PreToolUse hook): stdin is the tool-call JSON; allow =
# exit 0 with no stdout; deny = print {"hookSpecificOutput":{…,"permissionDecision":
# "deny",…}} to stdout and exit 0.
#
# This wrapper exists so that a missing python3 FAILS CLOSED: PreToolUse non-2
# exits are non-blocking (would silently allow), so if python3 is unavailable we
# deny anything that looks like a PR creation instead. Escape hatch:
# FANOUT_SKIP_PR_REVIEW=1 gh pr create ...
set -euo pipefail

payload="$(cat)"

if command -v python3 >/dev/null 2>&1; then
  parser="$(cd "$(dirname "$0")" && pwd)/pre-pr-review-gate.py"
  if [ -f "$parser" ]; then
    printf '%s' "$payload" | python3 "$parser" || true
    exit 0
  fi
fi

# --- fail-closed fallback (no python3): honor only an exported bypass, then
#     deny anything that coarsely looks like a PR creation. ---------------------
[ "${FANOUT_SKIP_PR_REVIEW:-}" = "1" ] && exit 0
if grep -Eq 'gh[[:space:]]+([^[:space:]]+[[:space:]]+)*pr[[:space:]]+([^[:space:]]+[[:space:]]+)*(create|new)([^[:alnum:]_-]|$)' <<<"$payload"; then
  printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"python3 が見つからないため PR レビューゲートが状態を判定できません。python3 をインストールするか、export FANOUT_SKIP_PR_REVIEW=1 してから実行してください。"}}'
fi
exit 0
