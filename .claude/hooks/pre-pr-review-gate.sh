#!/usr/bin/env bash
# fanout PR-review gate — PreToolUse(Bash) hook.
#
# Blocks `gh pr create` (and its `new` alias) unless the commit the PR is built
# from has been signed off by /post-work-review. The skill writes the reviewed
# commit SHA to a worktree-local marker; this hook allows the PR only when the
# marker matches the head commit (the current HEAD, or the --head branch tip).
# The marker lives at $(git rev-parse --git-dir)/post-work-review-passed —
# per-worktree (.git/worktrees/<name>/...), so fanout's parallel panes never
# cross-contaminate.
#
# Contract (Claude Code PreToolUse hook): stdin is the tool-call JSON; allow =
# exit 0 with no stdout; deny = print {"hookSpecificOutput":{…,"permissionDecision":
# "deny",…}} to stdout and exit 0.
#
# Best-effort gate, NOT a full shell parser. Out of scope (handled by the
# FANOUT_SKIP_PR_REVIEW=1 escape hatch): indirect execution (eval, xargs,
# sh -c "<string>"); and it verifies LOCAL refs only — no network — so a repo
# behind its remote is the user's responsibility to fetch.
set -euo pipefail

payload="$(cat)"

# --- regex building blocks (POSIX ERE; defined early so the jq-missing
#     fallback can reuse them) --------------------------------------------------
_assign='[_[:alpha:]][_[:alnum:]]*=[^[:space:]]*[[:space:]]+'        # VAR=val<sp>
_flag='-[^[:space:]]+[[:space:]]+([^-[:space:]][^[:space:]]*[[:space:]]+)?' # -x [val]<sp>
_wrap='(env|command|builtin|exec|time|nice|nohup|setsid|stdbuf|then|do|else)[[:space:]]+'
_pre="(${_assign}|${_wrap}|${_flag})*"          # prefix tokens before gh
_bound='(^|[;&|({])[[:space:]]*'                # command boundary (not backtick)
_path='([^[:space:];&|(){}]*/)?'                # optional path prefix: /usr/bin/gh, ./gh
_ghpr="${_path}gh[[:space:]]+(${_flag})*pr[[:space:]]+(${_flag})*(create|new)([^[:alnum:]_-]|\$)"

# jq dependency: FAIL CLOSED if missing. `gh` ships its own jq (gojq) so system
# jq can be absent; without it we can't parse the payload, and PreToolUse non-2
# exits are non-blocking (would silently allow). Honor only a bypass tied to the
# gh pr create (exported env, or token prefixing the command — NOT a --body
# value), then deny anything that looks like a PR creation.
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
# Quote-stripped copy: flags buried in a --title/--body *value* are ignored.
scrubbed=$(printf '%s' "$cmdn" | sed -E "s/\"[^\"]*\"//g; s/'[^']*'//g")

# In scope only if the command actually invokes gh pr create / gh pr new.
grep -Eq "${_bound}${_pre}${_ghpr}" <<<"$cmdn" || allow

# `gh pr create --help`/`-h` only prints help — exempt it, but SCOPED to the
# matched command (same segment, on the quote-stripped text) so neither a
# --help in another command (`echo --help && gh pr create`) nor one inside a
# quoted value (`--title "fix --help"`) flips a real creation to allow.
grep -Eq "${_bound}${_pre}${_ghpr}[^;&|]*(--help|-h)([^[:alnum:]_-]|\$)" <<<"$scrubbed" && allow

# Explicit operator override, TIED to the matched gh pr create (the assignment
# must prefix THAT command). A --body value or an assignment on a different
# simple command does NOT bypass. A session-level export is honored.
[[ "${FANOUT_SKIP_PR_REVIEW:-}" == "1" ]] && allow
grep -Eq "${_bound}${_pre}FANOUT_SKIP_PR_REVIEW=1[[:space:]]+${_pre}${_ghpr}" <<<"$cmdn" && allow

# A directory change BEFORE the matched creation makes gh run elsewhere than the
# payload cwd, so the marker check would consult the wrong worktree. Refuse for
# both `cd`/`pushd` and `env -C`/`--chdir`.
DIRMSG="ディレクトリ変更を伴う gh pr create はゲートが対象リポジトリのレビュー状態を判定できません。
対象リポジトリに移動してから /post-work-review → gh pr create を実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
grep -Eq "${_bound}(${_wrap})*(cd|pushd)[[:space:]][^;&|]*[;&|].*${_ghpr}" <<<"$cmdn" && deny "$DIRMSG"
grep -Eq "${_bound}(${_assign}|${_wrap})*(-C|--chdir)([[:space:]]|=)[^;&|]*${_ghpr}" <<<"$cmdn" && deny "$DIRMSG"

# Outside a git work tree (or unreadable cwd / detached-but-no-HEAD): fall back
# to allow — better to under-lock than to wrongly block where we can't reason.
cd "$cwd" 2>/dev/null || allow
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || allow

head=$(git rev-parse HEAD 2>/dev/null) || allow
marker="$(git rev-parse --git-dir)/post-work-review-passed"
read_marker() { [[ -f "$marker" ]] && cat "$marker"; }

# Tokenize the command respecting quotes (xargs does NOT execute, and uses the
# original $cmd so real newlines split tokens). Extract the --head/--base values
# as whole tokens, so a flag mentioned inside a quoted --title/--body is never a
# standalone token and is correctly ignored.
# (read loop, not `mapfile` — macOS ships bash 3.2 which lacks mapfile.)
toks=()
while IFS= read -r _tk; do toks+=("$_tk"); done < <(printf '%s' "$cmd" | xargs -n1 2>/dev/null)
headref=""; baseref=""
for ((i=0; i<${#toks[@]}; i++)); do
  case "${toks[i]}" in
    --head)   headref="${toks[i+1]:-}";;
    --head=*) headref="${toks[i]#--head=}";;
    -H=*)     headref="${toks[i]#-H=}";;
    -H)       headref="${toks[i+1]:-}";;
    -H?*)     headref="${toks[i]#-H}";;
    --base)   baseref="${toks[i+1]:-}";;
    --base=*) baseref="${toks[i]#--base=}";;
    -B=*)     baseref="${toks[i]#-B=}";;
    -B)       baseref="${toks[i+1]:-}";;
    -B?*)     baseref="${toks[i]#-B}";;
  esac
done

# --base other than the repository default branch changes the diff range that
# /post-work-review verified (it reviews against the default base). Deny a
# non-default base; be lenient only when the default can't be resolved locally.
if [[ -n "$baseref" ]]; then
  defbr=$(git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null | sed 's#^origin/##') || defbr=""
  if [[ -n "$defbr" && "$baseref" != "$defbr" ]]; then
    deny "gh pr create --base ($baseref) は既定ブランチ ($defbr) と異なり、レビューした diff 範囲と PR の diff 範囲がずれます。
既定ブランチを base にするか、対象 base に対して /post-work-review し直してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
  fi
fi

# Determine the commit the PR head will be built from. --head/-H selects a
# branch other than the current HEAD; verify ITS tip. Because gh builds a --head
# PR from the (already-pushed) remote branch, also refuse if the local ref
# diverges from origin/<branch> — the local tip we'd check could be stale.
if [[ -n "$headref" ]]; then
  target=$(git rev-parse --verify "${headref}^{commit}" 2>/dev/null) || target=""
  [[ -z "$target" ]] && deny "gh pr create --head ($headref) のブランチをローカルで解決できません。
対象ブランチを checkout/fetch してから実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
  origintip=$(git rev-parse --verify "refs/remotes/origin/${headref}^{commit}" 2>/dev/null) || origintip=""
  [[ -n "$origintip" && "$origintip" != "$target" ]] && deny "gh pr create --head ($headref) のローカル ref が origin/$headref と一致しません (push 前 / fetch 前)。
PR は push 済みのリモートブランチから作成されるため、push/fetch して同期してから実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
else
  target="$head"
fi

[[ -n "$target" && "$(read_marker)" == "$target" ]] && allow

deny "post-work-review が未実施です。先に /post-work-review を実行してください。
完了時に skill が現在の HEAD($head)を $marker に記録します。
(codex companion 未検出の場合は Pass 2 はスキップされ、Pass 1 通過で marker が書かれます)
/post-work-review が使えない場合は repo で make install (または make link) を実行してください。
完了後に gh pr create を再実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
