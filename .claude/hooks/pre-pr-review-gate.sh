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

# Only Bash tool calls are in scope.
[[ "$tool_name" == "Bash" ]] || allow

# --- regex building blocks (POSIX ERE) ---------------------------------------
# A leading env assignment token:  VAR=val<space>
_assign='[_[:alpha:]][_[:alnum:]]*=[^[:space:]]*[[:space:]]+'
# A flag token, optionally followed by one non-flag value:  -x [value]<space>
_flag='-[^[:space:]]+[[:space:]]+([^-[:space:]][^[:space:]]*[[:space:]]+)?'
# Command boundary + optional `env` wrapper. `gh` must sit at a boundary — start
# of line, or after one of ; | & ( { (which also covers `$( … )` substitution)
# — so a create phrase buried inside a quoted value or another command (e.g.
# `git commit -m "feat: gh pr create gate"`) is NOT gated. A bare backtick is
# intentionally NOT a boundary: gating every markdown inline-code mention would
# be worse than missing rare legacy `` `…` `` substitution. The optional
# `env ` covers `env GH_TOKEN=… gh pr create`.
_boundary='(^|[;&|({])[[:space:]]*(env[[:space:]]+)?'
# `gh [gh-flags] pr [pr-flags] create|new`, ending at a non-word char. Flags may
# appear BOTH between gh and pr (`gh -R o/r pr create`) and between pr and the
# subcommand (`gh pr -R o/r create`). gh resolves `new` to create.
_ghpr="gh[[:space:]]+(${_flag})*pr[[:space:]]+(${_flag})*(create|new)([^[:alnum:]_-]|\$)"

# In scope only if the command actually invokes gh pr create / gh pr new.
grep -Eq "${_boundary}(${_assign})*${_ghpr}" <<<"$cmd" || allow

# Explicit operator override, TIED to the matched gh pr create: the bypass
# assignment must prefix THAT command. So `--body FANOUT_SKIP_PR_REVIEW=1` (a
# value) and `FANOUT_SKIP_PR_REVIEW=1 echo ok; gh pr create` (assignment on a
# different simple command) do NOT bypass. A session-level export is honored.
# (No `--help` exemption: a help invocation merely prints help, so gating it is
# harmless, and any "help anywhere" exemption is itself a bypass vector.)
[[ "${FANOUT_SKIP_PR_REVIEW:-}" == "1" ]] && allow
grep -Eq "${_boundary}(${_assign})*FANOUT_SKIP_PR_REVIEW=1[[:space:]]+(env[[:space:]]+)?(${_assign})*${_ghpr}" <<<"$cmd" && allow

# A directory change BEFORE the matched creation (`cd ../other && gh pr create`)
# means gh runs somewhere other than the payload cwd, so the marker check would
# consult the wrong worktree. Refuse rather than trust the wrong repo's marker.
grep -Eq "(^|[;&|({])[[:space:]]*(cd|pushd)[[:space:]][^;&|]*[;&|].*${_ghpr}" <<<"$cmd" \
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
# necessarily the current HEAD — and the marker only proves one reviewed commit.
# Strip quoted values so a --head inside a --title/--body string is ignored,
# then resolve the named branch and verify ITS tip against the marker. If the
# branch can't be resolved locally (e.g. a cross-fork `owner:branch`), we can't
# prove it was reviewed, so deny.
scrubbed=$(printf '%s' "$cmd" | sed -E "s/\"[^\"]*\"//g; s/'[^']*'//g")
# `|| true`: under `set -e`+`pipefail`, grep finding no --head would exit the hook.
headref=$(grep -oE -- '(^|[[:space:]])(--head|-H)([[:space:]]+|=)[^[:space:]]+' <<<"$scrubbed" \
  | tail -1 | sed -E 's/.*(--head|-H)([[:space:]]+|=)//') || true
if [[ -n "$headref" ]]; then
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
