#!/bin/sh

# Cheap, foreground PR polling for the pr-watch skill. Keep this helper
# read-only: repair work belongs to the model-owned repair loop.
set -eu

usage() {
  cat >&2 <<'EOF'
usage: watch-pr.sh <snapshot|wait|reset> --repo OWNER/REPO --pr N [--reaction-target TARGET ...]

TARGET is one of:
  issue
  issue_comment:ID
  review_comment:ID
EOF
  exit 2
}

command_name="${1:-}"
case "$command_name" in
  snapshot|wait|reset) shift ;;
  *) usage ;;
esac

repo=""
pr=""
reaction_targets=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo)
      [ "$#" -ge 2 ] || usage
      repo="$2"
      shift 2
      ;;
    --pr)
      [ "$#" -ge 2 ] || usage
      pr="$2"
      shift 2
      ;;
    --reaction-target)
      [ "$#" -ge 2 ] || usage
      case "$2" in
        issue) ;;
        issue_comment:*)
          reaction_id="${2#issue_comment:}"
          case "$reaction_id" in ''|0|*[!0-9]*) usage ;; esac
          ;;
        review_comment:*)
          reaction_id="${2#review_comment:}"
          case "$reaction_id" in ''|0|*[!0-9]*) usage ;; esac
          ;;
        *) usage ;;
      esac
      if [ -z "$reaction_targets" ]; then
        reaction_targets="$2"
      else
        reaction_targets="$reaction_targets
$2"
      fi
      shift 2
      ;;
    *) usage ;;
  esac
done

case "$repo" in
  ''|/*|*/|*/*/*) usage ;;
  */*)
    repo_owner="${repo%%/*}"
    repo_name="${repo#*/}"
    case "$repo_owner$repo_name" in *[!A-Za-z0-9._-]*) usage ;; esac
    ;;
  *) usage ;;
esac
case "$pr" in
  ''|*[!0-9]*|0) usage ;;
esac

emit_blocked() {
  reason="$1"
  status="${2:-1}"
  printf 'event=blocked repo=%s pr=%s reason=%s status=%s\n' "$repo" "$pr" "$reason" "$status"
  exit 1
}

command -v git >/dev/null 2>&1 || emit_blocked git_missing 127
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || emit_blocked not_in_git_repo 1

state_dir="$(git rev-parse --path-format=absolute --git-path pr-watch-state 2>/dev/null)" ||
  emit_blocked state_path_failed 1
repo_hash="$(printf '%s\n' "$repo" | git hash-object --stdin 2>/dev/null)" ||
  emit_blocked state_key_failed 1
repo_key="$(printf '%s\n' "$repo" | sed 's/[^A-Za-z0-9._-]/-/g')"
state_file="$state_dir/$repo_key-$(printf '%.12s' "$repo_hash")-$pr.state"

if [ "$command_name" = reset ]; then
  rm -f "$state_file"
  exit 0
fi

command -v gh >/dev/null 2>&1 || emit_blocked gh_missing 127

case "${PR_WATCH_INTERVAL:-45}" in
  ''|*[!0-9]*) emit_blocked invalid_interval 2 ;;
esac
case "${PR_WATCH_MAX_SECONDS:-3600}" in
  ''|*[!0-9]*) emit_blocked invalid_max_seconds 2 ;;
esac

actor_re="${PR_WATCH_PLUS1_ACTOR_RE:-}"
if [ -n "$actor_re" ]; then
  set +e
  printf '\n' | grep -Eq -- "$actor_re" >/dev/null 2>&1
  actor_re_status=$?
  set -e
  [ "$actor_re_status" -le 1 ] || emit_blocked invalid_actor_filter 2
fi

mkdir -p "$state_dir" || emit_blocked state_dir_failed 1
chmod 700 "$state_dir" 2>/dev/null || :
umask 077

last_digest=""
deadline_ts=""
if [ "${PR_WATCH_CONTINUE:-0}" = 1 ] && [ -f "$state_file" ]; then
  deadline_ts="$(sed -n 's/^deadline_ts=//p' "$state_file" | sed -n '1p')"
  last_digest="$(sed -n 's/^digest=//p' "$state_file" | sed -n '1p')"
fi

now_ts="$(date +%s)"
case "$deadline_ts" in
  ''|*[!0-9]*) deadline_ts=$((now_ts + ${PR_WATCH_MAX_SECONDS:-3600})) ;;
esac
if [ "${PR_WATCH_CONTINUE:-0}" != 1 ]; then
  rm -f "$state_file"
  last_digest=""
  deadline_ts=$((now_ts + ${PR_WATCH_MAX_SECONDS:-3600}))
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/pr-watch.XXXXXX")" || emit_blocked temp_dir_failed 1
state_tmp="$state_dir/.watch-state.$$.tmp"
cleanup() {
  rm -f "$tmp_dir/pr.out" "$tmp_dir/pr.err" "$tmp_dir/checks.out" "$tmp_dir/checks.err" \
    "$tmp_dir/pr.fields" "$tmp_dir/checks.sorted" "$tmp_dir/reactions.out" "$tmp_dir/reaction.out" \
    "$tmp_dir/reaction.err" "$state_tmp"
  rmdir "$tmp_dir" 2>/dev/null || :
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

write_state() {
  new_digest="$1"
  {
    printf 'deadline_ts=%s\n' "$deadline_ts"
    printf 'digest=%s\n' "$new_digest"
  } >"$state_tmp" || emit_blocked state_write_failed 1
  mv "$state_tmp" "$state_file" || emit_blocked state_write_failed 1
}

fetch_snapshot() {
  : >"$tmp_dir/pr.out"
  : >"$tmp_dir/pr.err"
  set +e
  gh pr view -R "$repo" "$pr" \
    --json number,state,isDraft,mergeable,mergeStateStatus,reviewDecision,reviewRequests,updatedAt,headRefOid,url \
    --jq '[.number,.state,(.isDraft|tostring),.mergeable,.mergeStateStatus,(.reviewDecision // "NONE"),((.reviewRequests // [])|length),.updatedAt,.headRefOid,.url] | @tsv' \
    >"$tmp_dir/pr.out" 2>"$tmp_dir/pr.err"
  pr_status=$?
  set -e
  [ "$pr_status" -eq 0 ] || emit_blocked pr_snapshot_failed "$pr_status"
  [ -s "$tmp_dir/pr.out" ] || emit_blocked pr_snapshot_empty 1

  awk -F '	' '
    NR == 1 && NF == 10 {
      for (field = 1; field <= 10; field++) print $field
      next
    }
    { invalid = 1 }
    END { if (NR != 1 || invalid) exit 1 }
  ' "$tmp_dir/pr.out" >"$tmp_dir/pr.fields" ||
    emit_blocked pr_snapshot_invalid 1
  {
    IFS= read -r snap_number &&
      IFS= read -r snap_state &&
      IFS= read -r snap_draft &&
      IFS= read -r snap_mergeable &&
      IFS= read -r snap_merge_state &&
      IFS= read -r snap_review &&
      IFS= read -r snap_review_requests &&
      IFS= read -r snap_updated &&
      IFS= read -r snap_head &&
      IFS= read -r snap_url
  } <"$tmp_dir/pr.fields" || emit_blocked pr_snapshot_invalid 1
  [ -n "$snap_review" ] || snap_review=NONE
  [ "$snap_number" = "$pr" ] || emit_blocked pr_snapshot_mismatch 1

  : >"$tmp_dir/checks.out"
  : >"$tmp_dir/checks.err"
  set +e
  gh pr checks -R "$repo" "$pr" --json name,bucket,state,workflow,link \
    --jq 'sort_by(.bucket,.name,.workflow,.link,.state) | .[] | [.bucket,.name,.state,.workflow,.link] | @tsv' \
    >"$tmp_dir/checks.out" 2>"$tmp_dir/checks.err"
  checks_status=$?
  set -e
  checks_reported=true
  if [ ! -s "$tmp_dir/checks.out" ]; then
    checks_reported=false
    if [ "$checks_status" -ne 0 ] && ! grep -qi 'no checks reported' "$tmp_dir/checks.err"; then
      emit_blocked checks_snapshot_failed "$checks_status"
    fi
  fi
  LC_ALL=C sort "$tmp_dir/checks.out" >"$tmp_dir/checks.sorted"

  check_pass="$(awk -F '	' '$1 == "pass" { count++ } END { print count + 0 }' "$tmp_dir/checks.sorted")"
  check_pending="$(awk -F '	' '$1 == "pending" { count++ } END { print count + 0 }' "$tmp_dir/checks.sorted")"
  check_fail="$(awk -F '	' '$1 == "fail" { count++ } END { print count + 0 }' "$tmp_dir/checks.sorted")"
  check_cancel="$(awk -F '	' '$1 == "cancel" || $1 == "cancelled" { count++ } END { print count + 0 }' "$tmp_dir/checks.sorted")"
  check_skip="$(awk -F '	' '$1 == "skipping" { count++ } END { print count + 0 }' "$tmp_dir/checks.sorted")"

  : >"$tmp_dir/reactions.out"
  if [ -n "$reaction_targets" ]; then
    while IFS= read -r target; do
      [ -n "$target" ] || continue
      case "$target" in
        issue) reaction_path="/repos/$repo/issues/$pr/reactions?content=%2B1&per_page=100" ;;
        issue_comment:*)
          reaction_id="${target#issue_comment:}"
          reaction_path="/repos/$repo/issues/comments/$reaction_id/reactions?content=%2B1&per_page=100"
          ;;
        review_comment:*)
          reaction_id="${target#review_comment:}"
          reaction_path="/repos/$repo/pulls/comments/$reaction_id/reactions?content=%2B1&per_page=100"
          ;;
      esac
      : >"$tmp_dir/reaction.out"
      : >"$tmp_dir/reaction.err"
      set +e
      gh api --paginate "$reaction_path" \
        --jq '.[] | [.user.login,.created_at] | @tsv' \
        >"$tmp_dir/reaction.out" 2>"$tmp_dir/reaction.err"
      reaction_status=$?
      set -e
      [ "$reaction_status" -eq 0 ] || emit_blocked reaction_snapshot_failed "$reaction_status"
      awk -F '	' -v target="$target" 'NF { print target "\t" $1 "\t" $2 }' \
        "$tmp_dir/reaction.out" >>"$tmp_dir/reactions.out"
    done <<EOF
$reaction_targets
EOF
  fi
  LC_ALL=C sort -o "$tmp_dir/reactions.out" "$tmp_dir/reactions.out"
  reaction_count="$(awk 'END { print NR + 0 }' "$tmp_dir/reactions.out")"
  reaction_match=0
  if [ -n "$actor_re" ]; then
    reaction_match="$(
      PR_WATCH_PLUS1_ACTOR_RE="$actor_re" \
        awk -F '	' \
          '$2 ~ ENVIRON["PR_WATCH_PLUS1_ACTOR_RE"] { count++ } END { print count + 0 }' \
          "$tmp_dir/reactions.out"
    )"
  fi

  digest="$(
    {
      printf 'pr\t'
      cat "$tmp_dir/pr.out"
      printf 'checks\n'
      printf 'reported\t%s\n' "$checks_reported"
      cat "$tmp_dir/checks.sorted"
      printf 'targets\n%s\n' "$reaction_targets"
      printf 'actor\n%s\n' "$actor_re"
      printf 'reactions\n'
      cat "$tmp_dir/reactions.out"
    } | git hash-object --stdin
  )" || emit_blocked digest_failed 1
}

emit_status() {
  event_name="$1"
  review_value="$snap_review"
  plus1=false
  [ "$reaction_match" -gt 0 ] && plus1=true
  printf 'event=%s repo=%s pr=%s state=%s draft=%s mergeable=%s merge_state=%s review=%s review_requests=%s checks_reported=%s checks_pass=%s checks_pending=%s checks_fail=%s checks_cancel=%s checks_skip=%s reactions=%s reaction_match=%s plus1=%s head=%s updated=%s url=%s digest=%s' \
    "$event_name" "$repo" "$pr" "$snap_state" "$snap_draft" "$snap_mergeable" "$snap_merge_state" \
    "$review_value" "$snap_review_requests" "$checks_reported" "$check_pass" "$check_pending" "$check_fail" \
    "$check_cancel" "$check_skip" "$reaction_count" "$reaction_match" "$plus1" \
    "$snap_head" "$snap_updated" "$snap_url" "$digest"
  if [ "$event_name" = timeout ]; then
    printf ' deadline=%s' "$deadline_ts"
  fi
  printf '\n'
}

emit_change() {
  emit_status change
}

merge_state_is_ready() {
  [ "$snap_merge_state" = CLEAN ] || [ "$snap_merge_state" = HAS_HOOKS ]
}

baseline_is_actionable() {
  [ "$checks_reported" = false ] ||
    [ "$snap_state" != OPEN ] ||
    [ "$snap_mergeable" = CONFLICTING ] ||
    [ "$snap_merge_state" = DIRTY ] ||
    [ "$snap_merge_state" = BEHIND ] ||
    [ "$snap_review" = CHANGES_REQUESTED ] ||
    [ "$check_fail" -gt 0 ] ||
    [ "$check_cancel" -gt 0 ] ||
    { [ "$snap_draft" = false ] && [ "$snap_mergeable" = MERGEABLE ] &&
      merge_state_is_ready &&
      [ "$check_pending" -eq 0 ] &&
      { [ "$snap_review" = APPROVED ] ||
        { [ "$snap_review" = NONE ] && [ "$snap_review_requests" -eq 0 ]; }; }; }
}

while :; do
  fetch_snapshot
  current_ts="$(date +%s)"
  if [ "$current_ts" -ge "$deadline_ts" ]; then
    rm -f "$state_file"
    emit_status timeout
    exit 124
  fi

  if [ -z "$last_digest" ]; then
    write_state "$digest"
    last_digest="$digest"
    if [ "$command_name" = snapshot ] || baseline_is_actionable; then
      emit_change
      exit 0
    fi
  elif [ "$digest" != "$last_digest" ]; then
    write_state "$digest"
    emit_change
    exit 0
  elif [ "$command_name" = snapshot ]; then
    exit 0
  fi

  sleep "${PR_WATCH_INTERVAL:-45}"
done
