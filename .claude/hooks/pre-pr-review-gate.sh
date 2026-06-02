#!/usr/bin/env bash
# fanout PR-review gate — PreToolUse(Bash) hook.
#
# Blocks `gh pr create` (and its `new` alias) unless the current HEAD has been
# signed off by /post-work-review. The skill writes the reviewed commit SHA to
# a worktree-local marker; this hook allows the PR only when that marker matches
# HEAD. HEAD moving forward auto-stales the marker, forcing re-review. The
# marker lives at $(git rev-parse --git-dir)/post-work-review-passed —
# per-worktree (.git/worktrees/<name>/...), so fanout's parallel panes never
# cross-contaminate.
#
# Contract (Claude Code PreToolUse hook): stdin is the tool-call JSON; allow =
# exit 0 with no stdout; deny = print {"hookSpecificOutput":{…,"permissionDecision":
# "deny",…}} to stdout and exit 0.
#
# This is a best-effort regex gate, not a shell parser. It deliberately does NOT
# follow indirect execution (eval, xargs, sh -c "<string>"), and it verifies the
# LOCAL reviewed commit only (no network) — a repo behind its remote is the
# user's responsibility to fetch. `--head`/`-H` (PR from another branch) and any
# command whose text merely contains the trigger near a shell operator are
# handled by the FANOUT_SKIP_PR_REVIEW=1 escape hatch.
set -euo pipefail

payload="$(cat)"

# --- regex building blocks (POSIX ERE; no jq needed, defined early so the
#     jq-missing fallback can reuse them) --------------------------------------
# VAR=val<space>
_assign='[_[:alpha:]][_[:alnum:]]*=[^[:space:]]*[[:space:]]+'
# -x [value]<space>  (a flag, optionally followed by one non-flag value)
_flag='-[^[:space:]]+[[:space:]]+([^-[:space:]][^[:space:]]*[[:space:]]+)?'
# Direct wrappers / shell keywords that can precede `gh` on the same simple
# command (env/command/time/…, then/do/else). NOT eval/xargs (indirect).
_wrap='(env|command|builtin|exec|time|nice|nohup|setsid|stdbuf|then|do|else)[[:space:]]+'
# A run of prefix tokens before `gh`: assignments, wrappers, or wrapper flags.
_pre="(${_assign}|${_wrap}|${_flag})*"
# Command boundary: start of (normalized) line, or after ; | & ( { (covers
# `$( … )`). A bare backtick is intentionally NOT a boundary (markdown mentions).
_bound='(^|[;&|({])[[:space:]]*'
# gh [gh-flags] pr [pr-flags] create|new, ending at a non-word char.
_ghpr="gh[[:space:]]+(${_flag})*pr[[:space:]]+(${_flag})*(create|new)([^[:alnum:]_-]|\$)"

# jq dependency: FAIL CLOSED if missing. `gh` ships its own jq (gojq) so system
# jq can be absent; without it we can't parse the payload, and PreToolUse non-2
# exits are non-blocking (would silently allow). So: honor only a bypass that is
# tied to the gh pr create (exported env, or the token prefixing the command —
# NOT a `--body FANOUT_SKIP_PR_REVIEW=1` value), then deny anything that looks
# like a PR creation. Matched coarsely against the raw payload.
if ! command -v jq >/dev/null 2>&1; then
  [[ "${FANOUT_SKIP_PR_REVIEW:-}" == "1" ]] && exit 0
  grep -Eq "FANOUT_SKIP_PR_REVIEW=1[[:space:]]+${_pre}${_ghpr}" <<<"$payload" && exit 0
  if grep -Eq "$_ghpr" <<<"$payload"; then
    printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"jq が見つからないため PR レビューゲートが状態を判定できません。jq をインストールするか、export FANOUT_SKIP_PR_REVIEW=1 / FANOUT_SKIP_PR_REVIEW=1 gh pr create ... で回避してください。"}}'
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

# Normalize newlines to ';' so line-based grep treats the whole command as one
# unit and boundaries work across line breaks (`cd ../other\ngh pr create`).
cmdn=$(printf '%s' "$cmd" | tr '\n\r' ';;')

# In scope only if the command actually invokes gh pr create / gh pr new.
grep -Eq "${_bound}${_pre}${_ghpr}" <<<"$cmdn" || allow

# Explicit operator override, TIED to the matched gh pr create: the bypass
# assignment must prefix THAT command. So `--body FANOUT_SKIP_PR_REVIEW=1` (a
# value) and `FANOUT_SKIP_PR_REVIEW=1 echo ok; gh pr create` (assignment on a
# different simple command) do NOT bypass. A session-level export is honored.
[[ "${FANOUT_SKIP_PR_REVIEW:-}" == "1" ]] && allow
grep -Eq "${_bound}${_pre}FANOUT_SKIP_PR_REVIEW=1[[:space:]]+${_pre}${_ghpr}" <<<"$cmdn" && allow

# A directory change BEFORE the matched creation (`cd ../other && gh pr create`,
# incl. across normalized newlines) means gh runs somewhere other than the
# payload cwd, so the marker check would consult the wrong worktree. Refuse.
grep -Eq "${_bound}(${_wrap})*(cd|pushd)[[:space:]][^;&|]*[;&|].*${_ghpr}" <<<"$cmdn" \
  && deny "cd/pushd を伴う gh pr create はゲートが対象リポジトリのレビュー状態を判定できません。
対象リポジトリに移動してから /post-work-review → gh pr create を実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."

# `--head`/`-H` builds the PR from another branch, whose reviewed state a local
# hook cannot reliably prove (local/remote divergence, stale refs). Detect a
# real flag from the quote-stripped command (a --head inside a --title/--body
# value disappears with its quotes and is correctly ignored) and refuse.
scrubbed=$(printf '%s' "$cmdn" | sed -E "s/\"[^\"]*\"//g; s/'[^']*'//g")
grep -Eq -- '(^|[[:space:]])(--head|-H)([[:space:]]|=)' <<<"$scrubbed" \
  && deny "gh pr create --head/-H は現在の HEAD ではなく指定ブランチから PR を作成します。
対象ブランチのレビュー状態をローカルのゲートでは確認できないため block します。
対象ブランチを checkout し /post-work-review → (--head なしで) gh pr create を実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."

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
