#!/usr/bin/env bash
# fanout PR-review gate — PreToolUse(Bash) hook.
#
# Blocks `gh pr create` unless the current HEAD has been signed off by
# /post-work-review. The skill writes the reviewed commit SHA to a
# worktree-local marker; this hook allows the PR only when that marker
# matches HEAD. HEAD moving forward auto-stales the marker, forcing a
# re-review. The marker lives at $(git rev-parse --git-dir)/post-work-review-passed
# which is per-worktree (.git/worktrees/<name>/...), so fanout's parallel
# panes never cross-contaminate each other's review state.
#
# Contract (Claude Code PreToolUse hook):
#   - stdin is the tool-call JSON ({tool_name, tool_input.command, cwd, ...}).
#   - To allow: exit 0 with no stdout.
#   - To deny: print {"hookSpecificOutput":{...,"permissionDecision":"deny",...}}
#     to stdout and exit 0.
#
# Go port note: the fanout CLI (cmd/fanout + internal/*) is dependency-free,
# but this hook stays bash + jq like the other repo hooks — jq is assumed
# present (gh CLI already needs it), so no new dependency is introduced.
#
# Escape hatch: FANOUT_SKIP_PR_REVIEW=1 gh pr create ...
set -euo pipefail

payload="$(cat)"
tool_name=$(jq -r '.tool_name // ""' <<<"$payload")
cmd=$(jq -r '.tool_input.command // ""' <<<"$payload")
cwd=$(jq -r '.cwd // ""' <<<"$payload")

allow() { exit 0; }
deny()  {
  jq -nc --arg r "$1" \
    '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"deny",permissionDecisionReason:$r}}'
  exit 0
}

# Only Bash tool calls that actually run `gh pr create` are in scope.
# `gh pr new` is gh's built-in alias for `gh pr create` (gh resolves it to the
# same command), so gate it too. Parent/inherited flags may appear between
# `pr` and the subcommand (e.g. `gh pr -R owner/repo create`), so allow a run
# of flag tokens (each optionally followed by one non-flag value) before
# `create`/`new`. Restricting the gap to flag-shaped tokens keeps the match on
# the subcommand position, so `gh pr comment --body "...create..."` and
# `gh pr edit 5 --title create` are NOT gated.
[[ "$tool_name" == "Bash" ]] || allow
grep -Eq '(^|[^[:alnum:]_-])gh[[:space:]]+pr[[:space:]]+(-[^[:space:]]+[[:space:]]+([^-[:space:]][^[:space:]]*[[:space:]]+)?)*(create|new)([[:space:]]|$)' <<<"$cmd" || allow
# `gh pr create --help` / `-h` is harmless; never gate it. Match only the
# tokenized flag — a branch/value ending in `-h` (e.g. `--head fix-h`) or a
# word merely containing `--help` must NOT slip the gate.
grep -Eq -- '(^|[[:space:]])(--help|-h)([[:space:]]|$)' <<<"$cmd" && allow
# Explicit operator override. The hook runs as its own process *before* the
# command, so an inline `FANOUT_SKIP_PR_REVIEW=1 gh pr create ...` (the
# documented escape hatch) sets that var only for `gh`, never for us — we must
# also scan the command string. A session-level `export` is honored too.
[[ "${FANOUT_SKIP_PR_REVIEW:-}" == "1" ]] && allow
grep -Eq '(^|[[:space:]])FANOUT_SKIP_PR_REVIEW=1([[:space:]]|;|$)' <<<"$cmd" && allow

# Outside a git work tree (or unreadable cwd / detached-but-no-HEAD) we
# deliberately fall back to allow — better to under-lock than to wrongly
# block a PR in an environment the gate can't reason about.
cd "$cwd" 2>/dev/null || allow
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || allow

head=$(git rev-parse HEAD 2>/dev/null) || allow
marker="$(git rev-parse --git-dir)/post-work-review-passed"
[[ -f "$marker" && "$(cat "$marker")" == "$head" ]] && allow

deny "post-work-review が未実施です。先に /post-work-review を実行してください。
完了時に skill が現在の HEAD($head)を $marker に記録します。
(codex companion 未検出の場合は Pass 2 はスキップされ、Pass 1 通過で marker が書かれます)
/post-work-review が使えない場合は repo で make install (または make link) を実行してください。
完了後に gh pr create を再実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
