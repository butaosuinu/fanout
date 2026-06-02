#!/usr/bin/env bash
# fanout PR-review gate — PreToolUse(Bash) hook.
#
# Blocks `gh pr create` unless the commit the PR will be built from has been
# signed off by /post-work-review. The skill writes the reviewed commit SHA to
# a worktree-local marker; this hook allows the PR only when that marker matches
# the target commit. HEAD (or the --head branch tip) moving forward auto-stales
# the marker, forcing a re-review. The marker lives at
# $(git rev-parse --git-dir)/post-work-review-passed — per-worktree
# (.git/worktrees/<name>/...), so fanout's parallel panes never cross-contaminate.
#
# Contract (Claude Code PreToolUse hook): stdin is the tool-call JSON; allow =
# exit 0 with no stdout; deny = print {"hookSpecificOutput":{…,"permissionDecision":
# "deny",…}} to stdout and exit 0.
#
# This is a best-effort regex gate, not a shell parser. It deliberately does NOT
# try to follow indirect execution (eval, xargs, sh -c "<string>"); those, and
# any command whose text merely contains the trigger near a shell operator, are
# documented limitations handled by the FANOUT_SKIP_PR_REVIEW=1 escape hatch.
set -euo pipefail

payload="$(cat)"

# jq dependency: FAIL CLOSED if missing. `gh` ships its own jq (gojq) so the
# system `jq` can genuinely be absent; without it we cannot parse the payload.
# Since PreToolUse non-2 exits are non-blocking (would silently allow), deny
# anything that looks like a PR creation and tell the user how to proceed.
if ! command -v jq >/dev/null 2>&1; then
  grep -Eq 'FANOUT_SKIP_PR_REVIEW=1' <<<"$payload" && exit 0
  if grep -Eq 'gh[[:space:]]+([^[:space:]]+[[:space:]]+)*pr[[:space:]]+([^[:space:]]+[[:space:]]+)*(create|new)([^[:alnum:]_-]|$)' <<<"$payload"; then
    printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"jq が見つからないため PR レビューゲートが状態を判定できません。jq をインストールするか、FANOUT_SKIP_PR_REVIEW=1 gh pr create ... で回避してください。"}}'
  fi
  exit 0
fi

tool_name=$(jq -r '.tool_name // ""' <<<"$payload")
cmd=$(jq -r '.tool_input.command // ""' <<<"$payload")
cwd=$(jq -r '.cwd // ""' <<<"$payload")

allow() { exit 0; }
deny()  {
  jq -nc --arg r "$1" \
    '{hookSpecificOutput:{hookEventName:"PreToolUse",permissionDecision:"deny",permissionDecisionReason:$r}}'
  exit 0
}

[[ "$tool_name" == "Bash" ]] || allow

# Normalize newlines to ';' so the line-based grep sees the whole command as one
# unit and command boundaries work across line breaks (e.g. a `cd` on one line
# and `gh pr create` on the next).
cmdn=$(printf '%s' "$cmd" | tr '\n\r' ';;')

# --- regex building blocks (POSIX ERE) ---------------------------------------
# VAR=val<space>
_assign='[_[:alpha:]][_[:alnum:]]*=[^[:space:]]*[[:space:]]+'
# -x [value]<space>  (a flag, optionally followed by one non-flag value)
_flag='-[^[:space:]]+[[:space:]]+([^-[:space:]][^[:space:]]*[[:space:]]+)?'
# Direct command wrappers / shell keywords that can precede `gh` on the same
# simple command: env/command/time/nice/…, and then/do/else for compound forms
# (`if …; then gh pr create`). NOT eval/xargs — indirect execution is out of scope.
_wrap='(env|command|builtin|exec|time|nice|nohup|setsid|stdbuf|then|do|else)[[:space:]]+'
# A run of prefix tokens before `gh`: assignments, wrappers, or wrapper flags.
_pre="(${_assign}|${_wrap}|${_flag})*"
# Command boundary. `gh` must sit at start of (a normalized) line, or after one
# of ; | & ( { (which also covers `$( … )` substitution). A bare backtick is
# intentionally NOT a boundary, so markdown inline-code mentions in commit
# messages / PR comments are not gated.
_bound='(^|[;&|({])[[:space:]]*'
# `gh [gh-flags] pr [pr-flags] create|new`, ending at a non-word char. Flags may
# appear both between gh and pr (`gh -R o/r pr create`) and between pr and the
# subcommand (`gh pr -R o/r create`). gh resolves `new` to create.
_ghpr="gh[[:space:]]+(${_flag})*pr[[:space:]]+(${_flag})*(create|new)([^[:alnum:]_-]|\$)"

# In scope only if the command actually invokes gh pr create / gh pr new.
grep -Eq "${_bound}${_pre}${_ghpr}" <<<"$cmdn" || allow

# Explicit operator override, TIED to the matched gh pr create: the bypass
# assignment must prefix THAT command. So `--body FANOUT_SKIP_PR_REVIEW=1` (a
# value) and `FANOUT_SKIP_PR_REVIEW=1 echo ok; gh pr create` (assignment on a
# different simple command) do NOT bypass. A session-level export is honored.
# (No --help exemption: a help invocation merely prints help, so gating it is
# harmless, while any "help anywhere" exemption is itself a bypass vector.)
[[ "${FANOUT_SKIP_PR_REVIEW:-}" == "1" ]] && allow
grep -Eq "${_bound}${_pre}FANOUT_SKIP_PR_REVIEW=1[[:space:]]+${_pre}${_ghpr}" <<<"$cmdn" && allow

# A directory change BEFORE the matched creation (`cd ../other && gh pr create`,
# including across normalized newlines) means gh runs somewhere other than the
# payload cwd, so the marker check would consult the wrong worktree. Refuse
# rather than trust the wrong repo's marker.
grep -Eq "${_bound}(${_wrap})*(cd|pushd)[[:space:]][^;&|]*[;&|].*${_ghpr}" <<<"$cmdn" \
  && deny "cd/pushd を伴う gh pr create はゲートが対象リポジトリのレビュー状態を判定できません。
対象リポジトリに移動してから /post-work-review → gh pr create を実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."

# Outside a git work tree (or unreadable cwd / detached-but-no-HEAD) we
# deliberately fall back to allow — better to under-lock than to wrongly
# block a PR in an environment the gate can't reason about.
cd "$cwd" 2>/dev/null || allow
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || allow

head=$(git rev-parse HEAD 2>/dev/null) || allow
marker="$(git rev-parse --git-dir)/post-work-review-passed"

# `gh pr create --head <branch>` (or -H) builds the PR from THAT branch, not
# necessarily the current HEAD — so verify the branch's tip against the marker.
# Detect a *real* --head flag from the quote-stripped command (a --head buried
# inside a --title/--body value disappears with its quotes and is correctly
# ignored); but read the VALUE from the raw command, where it may itself be
# quoted (`--head "other-branch"`). Unresolvable / cross-fork heads -> deny.
scrubbed=$(printf '%s' "$cmdn" | sed -E "s/\"[^\"]*\"//g; s/'[^']*'//g")
if grep -Eq -- '(^|[[:space:]])(--head|-H)([[:space:]]|=)' <<<"$scrubbed"; then
  # shellcheck disable=SC1003
  headref=$(grep -oE -- '(--head|-H)([[:space:]]+|=)("[^"]*"|'\''[^'\'']*'\''|[^[:space:]]+)' <<<"$cmdn" \
    | tail -1 | sed -E 's/^(--head|-H)([[:space:]]+|=)//; s/^"//; s/"$//; s/^'\''//; s/'\''$//') || true
  target=$(git rev-parse --verify "${headref}^{commit}" 2>/dev/null) || target=""
  [[ -n "$target" && -f "$marker" && "$(cat "$marker")" == "$target" ]] && allow
  deny "gh pr create --head ($headref) は現在の HEAD ではなく対象ブランチから PR を作成しますが、
そのブランチ先端がレビュー済み (marker 一致) であることを確認できません。
対象ブランチを checkout して /post-work-review → gh pr create を実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
fi

[[ -f "$marker" && "$(cat "$marker")" == "$head" ]] && allow

deny "post-work-review が未実施です。先に /post-work-review を実行してください。
完了時に skill が現在の HEAD($head)を $marker に記録します。
(codex companion 未検出の場合は Pass 2 はスキップされ、Pass 1 通過で marker が書かれます)
/post-work-review が使えない場合は repo で make install (または make link) を実行してください。
完了後に gh pr create を再実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
