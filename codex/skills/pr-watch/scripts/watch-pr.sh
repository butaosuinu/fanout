#!/bin/sh

# Cheap, foreground PR polling for the pr-watch skill. Keep this helper
# read-only: repair work belongs to the model-owned repair loop.
set -eu

usage() {
  cat >&2 <<'EOF'
usage: watch-pr.sh <snapshot|wait|reset|review-probe> --repo OWNER/REPO --pr N [--reaction-target TARGET ...]

TARGET is one of:
  issue
  issue_comment:ID
  review_comment:ID
EOF
  exit 2
}

command_name="${1:-}"
case "$command_name" in
  snapshot|wait|reset|review-probe) shift ;;
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
if [ "$command_name" = review-probe ] && [ -n "$reaction_targets" ]; then
  usage
fi

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

if [ "$command_name" = review-probe ]; then
  probe_tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/pr-watch-probe.XXXXXX")" ||
    emit_blocked temp_dir_failed 1
  # Invoked indirectly by the EXIT trap.
  # shellcheck disable=SC2317,SC2329
  probe_cleanup() {
    rm -f "$probe_tmp_dir/counts.before" "$probe_tmp_dir/counts.after" \
      "$probe_tmp_dir/comments.raw" "$probe_tmp_dir/comments.unsorted" \
      "$probe_tmp_dir/comments.sorted" "$probe_tmp_dir/reviews.raw" \
      "$probe_tmp_dir/reviews.unsorted" "$probe_tmp_dir/reviews.sorted" \
      "$probe_tmp_dir/threads.raw" "$probe_tmp_dir/threads.unsorted" \
      "$probe_tmp_dir/threads.all" "$probe_tmp_dir/threads.sorted" \
      "$probe_tmp_dir/thread-comments.raw" "$probe_tmp_dir/thread-comments.expected" \
      "$probe_tmp_dir/thread-comments.unsorted" "$probe_tmp_dir/thread-comments.sorted" \
      "$probe_tmp_dir/threads.fingerprint" \
      "$probe_tmp_dir/fingerprint"
    rmdir "$probe_tmp_dir" 2>/dev/null || :
  }
  trap probe_cleanup EXIT
  trap 'exit 129' HUP
  trap 'exit 130' INT
  trap 'exit 143' TERM

  fetch_probe_counts() {
    probe_counts_out="$1"
    set +e
    # GraphQL and jq variables must remain literal for gh.
    # shellcheck disable=SC2016
    gh api graphql -f query='
      query($owner:String!,$repo:String!,$num:Int!){
        repository(owner:$owner,name:$repo){
          pullRequest(number:$num){
            headRefOid
            updatedAt
            comments(first:1){totalCount}
            latestReviews(first:1){totalCount}
            reviewThreads(first:1){totalCount}
          }
        }
      }' -F owner="$repo_owner" -F repo="$repo_name" -F num="$pr" \
      --jq '.data.repository.pullRequest | [.headRefOid,.updatedAt,.comments.totalCount,.latestReviews.totalCount,.reviewThreads.totalCount] | @tsv' \
      >"$probe_counts_out" 2>/dev/null
    probe_counts_status=$?
    set -e
    [ "$probe_counts_status" -eq 0 ] || emit_blocked review_probe_counts_failed "$probe_counts_status"
    awk -F '\t' '
      NR == 1 && NF == 5 && $1 != "" && $2 != "" &&
        $3 ~ /^[0-9]+$/ && $4 ~ /^[0-9]+$/ && $5 ~ /^[0-9]+$/ { next }
      { invalid = 1 }
      END { if (NR != 1 || invalid) exit 1 }
    ' "$probe_counts_out" || emit_blocked review_probe_counts_invalid 1
  }

  normalize_probe_connection() {
    probe_raw="$1"
    probe_unsorted="$2"
    probe_sorted="$3"
    awk -F '\t' '
      $1 == "total" && $2 ~ /^[0-9]+$/ {
        if (!saw_total) {
          total = $2
          saw_total = 1
        } else if ($2 != total) {
          invalid = 1
        }
        next
      }
      $1 == "node" {
        if (NF < 3 || $2 == "" || seen_node[$2]) {
          invalid = 1
          next
        }
        seen_node[$2] = 1
        sub(/^node\t/, "")
        print
        nodes++
        next
      }
      { invalid = 1 }
      END {
        if (!saw_total || invalid || nodes + 0 != total + 0) exit 1
      }
    ' "$probe_raw" >"$probe_unsorted" || emit_blocked review_probe_incomplete 1
    LC_ALL=C sort "$probe_unsorted" >"$probe_sorted" || emit_blocked review_probe_sort_failed 1
  }

  validate_probe_comments() {
    awk -F '\t' '
      NF != 4 || $1 == "" || $3 == "" || $4 == "" { invalid = 1 }
      END { if (invalid) exit 1 }
    ' "$1" || emit_blocked review_probe_incomplete 1
  }

  validate_probe_reviews() {
    awk -F '\t' '
      NF != 6 || $1 == "" || $3 == "" || $4 == "" || $5 == "" { invalid = 1 }
      END { if (invalid) exit 1 }
    ' "$1" || emit_blocked review_probe_incomplete 1
  }

  normalize_probe_thread_comments() {
    probe_expected="$1"
    probe_raw="$2"
    probe_unsorted="$3"
    probe_sorted="$4"
    awk -F '\t' '
      NR == FNR {
        if (NF != 2 || $1 == "" || $2 !~ /^[0-9]+$/ || $1 in expected) {
          invalid = 1
        } else {
          expected[$1] = $2
        }
        next
      }
      $1 == "total" && NF == 3 && $2 in expected && $3 ~ /^[0-9]+$/ {
        if (!saw_total[$2]) {
          total[$2] = $3
          saw_total[$2] = 1
        } else if ($3 != total[$2]) {
          invalid = 1
        }
        next
      }
      $1 == "node" && NF == 6 && $2 in expected && $3 != "" && $5 != "" && $6 != "" {
        key = $2 SUBSEP $3
        if (seen_node[key]) invalid = 1
        seen_node[key] = 1
        nodes[$2]++
        sub(/^node\t/, "")
        print
        next
      }
      { invalid = 1 }
      END {
        for (id in expected) {
          if (!saw_total[id] || total[id] != expected[id] || nodes[id] + 0 != total[id] + 0) {
            invalid = 1
          }
        }
        if (invalid) exit 1
      }
    ' "$probe_expected" "$probe_raw" >"$probe_unsorted" ||
      emit_blocked review_probe_thread_comments_incomplete 1
    LC_ALL=C sort "$probe_unsorted" >"$probe_sorted" || emit_blocked review_probe_sort_failed 1
  }

  fetch_probe_comments() {
    set +e
    # GraphQL and jq variables must remain literal for gh.
    # shellcheck disable=SC2016
    gh api graphql --paginate -f query='
      query($owner:String!,$repo:String!,$num:Int!,$endCursor:String){
        repository(owner:$owner,name:$repo){
          pullRequest(number:$num){
            comments(first:100,after:$endCursor){
              totalCount
              pageInfo{hasNextPage endCursor}
              nodes{id author{login} createdAt updatedAt}
            }
          }
        }
      }' -F owner="$repo_owner" -F repo="$repo_name" -F num="$pr" \
      --jq '.data.repository.pullRequest.comments as $c | (["total",($c.totalCount|tostring)] | @tsv), ($c.nodes[] | ["node",.id,(.author.login // ""),.createdAt,.updatedAt] | @tsv)' \
      >"$probe_tmp_dir/comments.raw" 2>/dev/null
    probe_comments_status=$?
    set -e
    [ "$probe_comments_status" -eq 0 ] || emit_blocked review_probe_comments_failed "$probe_comments_status"
    normalize_probe_connection "$probe_tmp_dir/comments.raw" \
      "$probe_tmp_dir/comments.unsorted" "$probe_tmp_dir/comments.sorted"
    validate_probe_comments "$probe_tmp_dir/comments.sorted"
  }

  fetch_probe_reviews() {
    set +e
    # GraphQL and jq variables must remain literal for gh.
    # shellcheck disable=SC2016
    gh api graphql --paginate -f query='
      query($owner:String!,$repo:String!,$num:Int!,$endCursor:String){
        repository(owner:$owner,name:$repo){
          pullRequest(number:$num){
            latestReviews(first:100,after:$endCursor){
              totalCount
              pageInfo{hasNextPage endCursor}
              nodes{id author{login} state submittedAt updatedAt commit{oid}}
            }
          }
        }
      }' -F owner="$repo_owner" -F repo="$repo_name" -F num="$pr" \
      --jq '.data.repository.pullRequest.latestReviews as $c | (["total",($c.totalCount|tostring)] | @tsv), ($c.nodes[] | ["node",.id,(.author.login // ""),.state,(.submittedAt // ""),.updatedAt,(.commit.oid // "")] | @tsv)' \
      >"$probe_tmp_dir/reviews.raw" 2>/dev/null
    probe_reviews_status=$?
    set -e
    [ "$probe_reviews_status" -eq 0 ] || emit_blocked review_probe_reviews_failed "$probe_reviews_status"
    normalize_probe_connection "$probe_tmp_dir/reviews.raw" \
      "$probe_tmp_dir/reviews.unsorted" "$probe_tmp_dir/reviews.sorted"
    validate_probe_reviews "$probe_tmp_dir/reviews.sorted"
  }

  fetch_probe_thread_comments() {
    : >"$probe_tmp_dir/thread-comments.raw"
    awk -F '\t' '{ print $1 "\t" $7 }' "$probe_tmp_dir/threads.sorted" \
      >"$probe_tmp_dir/thread-comments.expected" ||
      emit_blocked review_probe_thread_comments_invalid 1
    if [ ! -s "$probe_tmp_dir/thread-comments.expected" ]; then
      : >"$probe_tmp_dir/thread-comments.sorted"
      return
    fi

    while IFS="$probe_tab" read -r probe_thread_id probe_thread_comments_total; do
      [ -n "$probe_thread_id" ] || emit_blocked review_probe_thread_comments_invalid 1
      set +e
      # GraphQL and jq variables must remain literal for gh.
      # shellcheck disable=SC2016
      gh api graphql --paginate -f query='
        query($threadId:ID!,$endCursor:String){
          node(id:$threadId){
            ... on PullRequestReviewThread{
              id
              comments(first:100,after:$endCursor){
                totalCount
                pageInfo{hasNextPage endCursor}
                nodes{id author{login} createdAt updatedAt}
              }
            }
          }
        }' -F threadId="$probe_thread_id" \
        --jq '.data.node as $t | $t.comments as $c | (["total",$t.id,($c.totalCount|tostring)] | @tsv), ($c.nodes[] | ["node",$t.id,.id,(.author.login // ""),.createdAt,.updatedAt] | @tsv)' \
        >>"$probe_tmp_dir/thread-comments.raw" 2>/dev/null
      probe_thread_comments_status=$?
      set -e
      [ "$probe_thread_comments_status" -eq 0 ] ||
        emit_blocked review_probe_thread_comments_failed "$probe_thread_comments_status"
      [ "$probe_thread_comments_total" -ge 0 ] ||
        emit_blocked review_probe_thread_comments_invalid 1
    done <"$probe_tmp_dir/thread-comments.expected"

    normalize_probe_thread_comments "$probe_tmp_dir/thread-comments.expected" \
      "$probe_tmp_dir/thread-comments.raw" "$probe_tmp_dir/thread-comments.unsorted" \
      "$probe_tmp_dir/thread-comments.sorted"
  }

  fetch_probe_threads() {
    set +e
    # GraphQL and jq variables must remain literal for gh.
    # shellcheck disable=SC2016
    gh api graphql --paginate -f query='
      query($owner:String!,$repo:String!,$num:Int!,$endCursor:String){
        repository(owner:$owner,name:$repo){
          pullRequest(number:$num){
            reviewThreads(first:100,after:$endCursor){
              totalCount
              pageInfo{hasNextPage endCursor}
              nodes{
                id isResolved path line originalLine diffSide
                threadComments:comments(first:1){totalCount}
              }
            }
          }
        }
      }' -F owner="$repo_owner" -F repo="$repo_name" -F num="$pr" \
      --jq '.data.repository.pullRequest.reviewThreads as $c | (["total",($c.totalCount|tostring)] | @tsv), ($c.nodes[] | ["node",.id,(.isResolved|tostring),.path,(.line // ""),(.originalLine // ""),(.diffSide // ""),(.threadComments.totalCount|tostring)] | @tsv)' \
      >"$probe_tmp_dir/threads.raw" 2>/dev/null
    probe_threads_status=$?
    set -e
    [ "$probe_threads_status" -eq 0 ] || emit_blocked review_probe_threads_failed "$probe_threads_status"
    normalize_probe_connection "$probe_tmp_dir/threads.raw" \
      "$probe_tmp_dir/threads.unsorted" "$probe_tmp_dir/threads.all"
    awk -F '\t' '
      NF != 7 || $1 == "" || ($2 != "true" && $2 != "false") || $3 == "" ||
        $7 !~ /^[0-9]+$/ {
        invalid = 1
        next
      }
      $2 == "false" { print }
      END { if (invalid) exit 1 }
    ' "$probe_tmp_dir/threads.all" >"$probe_tmp_dir/threads.unsorted" ||
      emit_blocked review_probe_threads_invalid 1
    LC_ALL=C sort "$probe_tmp_dir/threads.unsorted" \
      >"$probe_tmp_dir/threads.sorted" || emit_blocked review_probe_sort_failed 1
    fetch_probe_thread_comments
  }

  fetch_probe_counts "$probe_tmp_dir/counts.before"
  probe_tab="$(printf '\t')"
  IFS="$probe_tab" read -r probe_head probe_updated probe_comments_total probe_reviews_total \
    probe_threads_total <"$probe_tmp_dir/counts.before" || emit_blocked review_probe_counts_invalid 1

  : >"$probe_tmp_dir/comments.sorted"
  : >"$probe_tmp_dir/reviews.sorted"
  : >"$probe_tmp_dir/threads.all"
  : >"$probe_tmp_dir/threads.sorted"
  : >"$probe_tmp_dir/thread-comments.sorted"
  if [ "$probe_comments_total" -gt 0 ]; then
    fetch_probe_comments
  fi
  if [ "$probe_reviews_total" -gt 0 ]; then
    fetch_probe_reviews
  fi
  if [ "$probe_threads_total" -gt 0 ]; then
    fetch_probe_threads
  fi

  fetch_probe_counts "$probe_tmp_dir/counts.after"
  probe_counts_before="$(sed -n '1p' "$probe_tmp_dir/counts.before")"
  probe_counts_after="$(sed -n '1p' "$probe_tmp_dir/counts.after")"
  if [ "$probe_counts_before" != "$probe_counts_after" ]; then
    printf 'event=change repo=%s pr=%s reason=review_probe_changed\n' "$repo" "$pr"
    exit 0
  fi

  probe_comments_count="$(awk 'END { print NR + 0 }' "$probe_tmp_dir/comments.sorted")"
  probe_reviews_count="$(awk 'END { print NR + 0 }' "$probe_tmp_dir/reviews.sorted")"
  probe_all_threads_count="$(awk 'END { print NR + 0 }' "$probe_tmp_dir/threads.all")"
  probe_threads_count="$(awk 'END { print NR + 0 }' "$probe_tmp_dir/threads.sorted")"
  [ "$probe_comments_count" -eq "$probe_comments_total" ] || {
    printf 'event=change repo=%s pr=%s reason=review_probe_changed\n' "$repo" "$pr"
    exit 0
  }
  [ "$probe_reviews_count" -eq "$probe_reviews_total" ] || {
    printf 'event=change repo=%s pr=%s reason=review_probe_changed\n' "$repo" "$pr"
    exit 0
  }
  [ "$probe_all_threads_count" -eq "$probe_threads_total" ] || {
    printf 'event=change repo=%s pr=%s reason=review_probe_changed\n' "$repo" "$pr"
    exit 0
  }

  {
    awk '{ print "thread\t" $0 }' "$probe_tmp_dir/threads.sorted"
    awk '{ print "comment\t" $0 }' "$probe_tmp_dir/thread-comments.sorted"
  } >"$probe_tmp_dir/threads.fingerprint" || emit_blocked review_probe_digest_failed 1

  probe_comments_digest="$(git hash-object "$probe_tmp_dir/comments.sorted" 2>/dev/null)" ||
    emit_blocked review_probe_digest_failed 1
  probe_reviews_digest="$(git hash-object "$probe_tmp_dir/reviews.sorted" 2>/dev/null)" ||
    emit_blocked review_probe_digest_failed 1
  probe_threads_digest="$(git hash-object "$probe_tmp_dir/threads.fingerprint" 2>/dev/null)" ||
    emit_blocked review_probe_digest_failed 1
  {
    printf 'head\t%s\n' "$probe_head"
    printf 'updated\t%s\n' "$probe_updated"
    printf 'top_comments\t%s\t%s\n' "$probe_comments_count" "$probe_comments_digest"
    printf 'reviews\t%s\t%s\n' "$probe_reviews_count" "$probe_reviews_digest"
    printf 'unresolved_threads\t%s\t%s\n' "$probe_threads_count" "$probe_threads_digest"
  } >"$probe_tmp_dir/fingerprint" || emit_blocked review_probe_digest_failed 1
  probe_fingerprint="$(git hash-object "$probe_tmp_dir/fingerprint" 2>/dev/null)" ||
    emit_blocked review_probe_digest_failed 1

  printf 'event=review_probe repo=%s pr=%s head=%s updated=%s top_comments=%s top_digest=%s reviews=%s reviews_digest=%s unresolved_threads=%s threads_digest=%s fingerprint=%s\n' \
    "$repo" "$pr" "$probe_head" "$probe_updated" "$probe_comments_count" \
    "$probe_comments_digest" "$probe_reviews_count" "$probe_reviews_digest" \
    "$probe_threads_count" "$probe_threads_digest" "$probe_fingerprint"
  exit 0
fi

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
  gh pr checks -R "$repo" "$pr" --required --json name,bucket,state,workflow,link \
    --jq 'sort_by(.bucket,.name,.workflow,.link,.state) | .[] | [.bucket,.name,.state,.workflow,.link] | @tsv' \
    >"$tmp_dir/checks.out" 2>"$tmp_dir/checks.err"
  checks_status=$?
  set -e
  checks_reported=true
  if [ ! -s "$tmp_dir/checks.out" ]; then
    if [ "$checks_status" -ne 0 ]; then
      if grep -qi 'no required checks reported' "$tmp_dir/checks.err"; then
        : # An empty required-check set is known and does not block completion.
      elif grep -qi 'no checks reported' "$tmp_dir/checks.err"; then
        checks_reported=false
      else
        emit_blocked checks_snapshot_failed "$checks_status"
      fi
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

required_checks_are_ready() {
  [ "$checks_reported" = true ] &&
    [ "$check_pending" -eq 0 ] &&
    [ "$check_fail" -eq 0 ] &&
    [ "$check_cancel" -eq 0 ]
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
      required_checks_are_ready &&
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
