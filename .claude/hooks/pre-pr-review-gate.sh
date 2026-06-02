#!/usr/bin/env bash
# fanout PR-review gate — PreToolUse(Bash) hook.
#
# Blocks `gh pr create` (and its `new` alias) unless the commit the PR is built
# from has been signed off by /post-work-review. The skill writes the reviewed
# commit SHA to a worktree-local marker; this hook allows the PR only when the
# marker matches the head commit (current HEAD, or the --head branch tip). The
# marker lives at $(git rev-parse --git-dir)/post-work-review-passed —
# per-worktree, so fanout's parallel panes never cross-contaminate.
#
# Contract (Claude Code PreToolUse hook): stdin is the tool-call JSON; allow =
# exit 0 with no stdout; deny = print {"hookSpecificOutput":{…,"permissionDecision":
# "deny",…}} to stdout and exit 0.
#
# Best-effort gate, NOT a full shell parser. Out of scope (use the
# FANOUT_SKIP_PR_REVIEW=1 escape hatch): indirect execution (eval, xargs,
# sh -c "<string>"); cross-fork `--head owner:branch`; and it verifies LOCAL
# refs only (no network). Bash 3.2 compatible (macOS ships 3.2): no `mapfile`.
set -euo pipefail

payload="$(cat)"

# --- regex building blocks (POSIX ERE; defined early for the jq-missing path) -
_assign='[_[:alpha:]][_[:alnum:]]*=[^[:space:]]*[[:space:]]+'
_flag='-[^[:space:]]+[[:space:]]+([^-[:space:]][^[:space:]]*[[:space:]]+)?'
_wrap='(env|command|builtin|exec|time|nice|nohup|setsid|stdbuf|then|do|else)[[:space:]]+'
_pre="(${_assign}|${_wrap}|${_flag})*"
_bound='(^|[;&|({])[[:space:]]*'
_path='([^[:space:];&|(){}]*/)?'
_ghpr="${_path}gh[[:space:]]+(${_flag})*pr[[:space:]]+(${_flag})*(create|new)([^[:alnum:]_-]|\$)"

# jq dependency: FAIL CLOSED if missing. `gh` ships its own jq (gojq) so system
# jq can be absent; PreToolUse non-2 exits are non-blocking (would silently
# allow). In this degraded mode we honor ONLY an exported bypass (a payload-text
# bypass can't be trusted without parsing) and deny anything PR-creation-shaped.
if ! command -v jq >/dev/null 2>&1; then
  [[ "${FANOUT_SKIP_PR_REVIEW:-}" == "1" ]] && exit 0
  if grep -Eq "$_ghpr" <<<"$payload"; then
    printf '%s\n' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"jq が見つからないため PR レビューゲートが状態を判定できません。jq をインストールするか、export FANOUT_SKIP_PR_REVIEW=1 してから実行してください。"}}'
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

# Normalize newlines to ';' (line breaks are command separators), then strip the
# CONTENTS of quoted spans. All boundary-sensitive regex checks run on this
# scrubbed string so that a separator/flag/bypass-token *inside* a quoted
# argument value (title/body) cannot inject a fake command boundary. Command
# words like `gh pr create` are never quoted, so a real invocation still shows.
cmdn=$(printf '%s' "$cmd" | tr '\n\r' ';;')
scrubbed=$(printf '%s' "$cmdn" | sed -E "s/\"[^\"]*\"//g; s/'[^']*'//g")

# In scope only if the command actually invokes gh pr create / gh pr new.
grep -Eq "${_bound}${_pre}${_ghpr}" <<<"$scrubbed" || allow

# `gh pr create --help`/`-h` only prints help — exempt, SCOPED to the matched
# command's own segment (so neither help in another command nor help inside a
# quoted value flips a real creation to allow).
grep -Eq "${_bound}${_pre}${_ghpr}[^;&|]*(--help|-h)([^[:alnum:]_-]|\$)" <<<"$scrubbed" && allow

# Explicit operator override, TIED to the matched gh pr create (assignment must
# prefix THAT command). On scrubbed so a --body value can't carry the token.
[[ "${FANOUT_SKIP_PR_REVIEW:-}" == "1" ]] && allow
grep -Eq "${_bound}${_pre}FANOUT_SKIP_PR_REVIEW=1[[:space:]]+${_pre}${_ghpr}" <<<"$scrubbed" && allow

# A directory change BEFORE the matched creation makes gh run elsewhere than the
# payload cwd, so the marker check would consult the wrong worktree. Refuse for
# cd/pushd and env -C/--chdir (git -C, which doesn't change the shell cwd, is
# intentionally not caught).
DIRMSG="ディレクトリ変更を伴う gh pr create はゲートが対象リポジトリのレビュー状態を判定できません。
対象リポジトリに移動してから /post-work-review → gh pr create を実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
grep -Eq "${_bound}(${_wrap})*(cd|pushd)[[:space:]][^;&|]*[;&|].*${_ghpr}" <<<"$scrubbed" && deny "$DIRMSG"
grep -Eq "${_bound}(${_assign}|${_wrap})*(-C|--chdir)([[:space:]]|=)[^;&|]*${_ghpr}" <<<"$scrubbed" && deny "$DIRMSG"

# Outside a git work tree (or unreadable cwd / detached-but-no-HEAD): allow.
cd "$cwd" 2>/dev/null || allow
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || allow

head=$(git rev-parse HEAD 2>/dev/null) || allow
marker="$(git rev-parse --git-dir)/post-work-review-passed"
read_marker() { [[ -f "$marker" ]] && cat "$marker"; }

# Tokenize respecting quotes (xargs does NOT execute; read loop for bash 3.2).
# Then locate the `create`/`new` token of THIS gh-pr-create command and read
# only ITS flags (up to the next separator token) so a --head/--base in a
# different command isn't attributed to the PR creation.
toks=()
while IFS= read -r _tk; do toks+=("$_tk"); done < <(printf '%s' "$cmd" | xargs -n1 2>/dev/null)
ntok=${#toks[@]}
ci=-1; i=0
while [ $i -lt "$ntok" ]; do
  case "${toks[i]}" in
    gh|*/gh)
      j=$((i+1))
      while [ $j -lt "$ntok" ]; do case "${toks[j]}" in -*) j=$((j+1));; *) break;; esac; done
      if [ $j -lt "$ntok" ] && [ "${toks[j]}" = pr ]; then
        k=$((j+1))
        while [ $k -lt "$ntok" ]; do case "${toks[k]}" in -*) k=$((k+1));; *) break;; esac; done
        if [ $k -lt "$ntok" ]; then case "${toks[k]}" in create|new) ci=$k; break;; esac; fi
      fi ;;
  esac
  i=$((i+1))
done
headref=""; baseref=""
if [ $ci -ge 0 ]; then
  i=$((ci+1))
  while [ $i -lt "$ntok" ]; do
    case "${toks[i]}" in
      ';'|'&'|'&&'|'|'|'||'|')'|'}') break;;
      --head)   headref="${toks[i+1]:-}";;
      --head=*) headref="${toks[i]#--head=}";;
      -H)       headref="${toks[i+1]:-}";;
      -H=*)     headref="${toks[i]#-H=}";;
      -H?*)     headref="${toks[i]#-H}";;
      --base)   baseref="${toks[i+1]:-}";;
      --base=*) baseref="${toks[i]#--base=}";;
      -B)       baseref="${toks[i+1]:-}";;
      -B=*)     baseref="${toks[i]#-B=}";;
      -B?*)     baseref="${toks[i]#-B}";;
    esac
    i=$((i+1))
  done
fi

# If no explicit --base, gh uses branch.<current>.gh-merge-base when set, before
# the repository default. Honor that so a configured non-default base is gated.
if [ -z "$baseref" ]; then
  _cur=$(git rev-parse --abbrev-ref HEAD 2>/dev/null) || _cur=""
  [ -n "$_cur" ] && { baseref=$(git config --get "branch.${_cur}.gh-merge-base" 2>/dev/null) || baseref=""; }
fi

# A base other than the repository default branch changes the diff range that
# /post-work-review verified. Deny a non-default base; lenient when the default
# can't be resolved locally.
if [ -n "$baseref" ]; then
  defbr=$(git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null | sed 's#^origin/##') || defbr=""
  if [ -n "$defbr" ] && [ "$baseref" != "$defbr" ]; then
    deny "gh pr create の base ($baseref) が既定ブランチ ($defbr) と異なり、レビューした diff 範囲と PR の diff 範囲がずれます。
既定ブランチを base にするか、対象 base に対して /post-work-review し直してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
  fi
fi

# Determine the commit the PR head will be built from. --head/-H selects another
# branch; verify ITS tip. owner:branch (cross-fork) can't be verified locally.
if [ -n "$headref" ]; then
  case "$headref" in
    *:*) deny "gh pr create --head ($headref) は別フォーク (owner:branch) を指しており、ローカルではレビュー状態を確認できません。
対象ブランチを checkout して /post-work-review するか、緊急回避してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..." ;;
  esac
  target=$(git rev-parse --verify "${headref}^{commit}" 2>/dev/null) || target=""
  [ -z "$target" ] && deny "gh pr create --head ($headref) のブランチをローカルで解決できません。
対象ブランチを checkout/fetch してから実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
  origintip=$(git rev-parse --verify "refs/remotes/origin/${headref}^{commit}" 2>/dev/null) || origintip=""
  [ -n "$origintip" ] && [ "$origintip" != "$target" ] && deny "gh pr create --head ($headref) のローカル ref が origin/$headref と一致しません (push 前 / fetch 前)。
PR は push 済みのリモートブランチから作成されるため、同期してから実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
else
  target="$head"
fi

[ -n "$target" ] && [ "$(read_marker)" == "$target" ] && allow

deny "post-work-review が未実施です。先に /post-work-review を実行してください。
完了時に skill が現在の HEAD($head)を $marker に記録します。
(codex companion 未検出の場合は Pass 2 はスキップされ、Pass 1 通過で marker が書かれます)
/post-work-review が使えない場合は repo で make install (または make link) を実行してください。
完了後に gh pr create を再実行してください。
緊急回避: FANOUT_SKIP_PR_REVIEW=1 gh pr create ..."
