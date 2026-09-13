# Cheap watch loop: state file contract and skeleton

The watcher keeps a repo-scoped local state file under the git metadata path
returned by `git rev-parse --git-path pr-watch-state`. The filename includes the
target `owner/repo` and PR number, so watching `#123` in another repository
cannot reuse this repository's `#123` state. The file is local and is never
committed. `.git` is not always a directory: linked worktrees use a `.git` file
that points at the real gitdir, which is why the path comes from `git rev-parse`.

State JSON keys:

- `deadline_ts`: epoch seconds when the current watch expires
  (`PR_WATCH_MAX_SECONDS`, default 3600).
- `last_digest`: the compact digest of the last observed state.
- `approval_reaction_targets` (optional): entries with `kind` `issue`,
  `issue_comment`, or `review_comment` and an `id`, when configured `:+1:`
  targets are in use.

`PR_WATCH_CONTINUE=1` means Claude is continuing the same cheap wait inside
`/loop /pr-watch` and the stored `deadline_ts` / `last_digest` stay valid. A
fresh user-invoked watch clears both and issues a new deadline; an already
expired stored deadline is cleared before polling so a later explicit watch does
not time out immediately.

The loop emits output to the model only when the digest changes (approval
signal, a failing or cancelled check, a merge conflict, `reviewDecision`
becoming `CHANGES_REQUESTED`, a new `headRefOid`, an ambiguous update that needs
classification) or on timeout / blocked. Unchanged digests sleep
`PR_WATCH_INTERVAL` (default 45 seconds) and poll again.

## Skeleton

```bash
state_dir="$(git rev-parse --git-path pr-watch-state)"
mkdir -p "$state_dir"
repo_key="$(printf '%s\n' "$owner/$repo" | tr '/:' '--')"
state_file="$state_dir/$repo_key-$num.json"
state_json="{}"
if [ -f "$state_file" ]; then
  state_json="$(cat "$state_file")"
fi

now_ts="$(date +%s)"
max_seconds="${PR_WATCH_MAX_SECONDS:-3600}"
deadline_ts="$(printf '%s\n' "$state_json" | jq -r '.deadline_ts // empty')"
if [ "${PR_WATCH_CONTINUE:-0}" != "1" ] ||
   [ -z "$deadline_ts" ] ||
   [ "$deadline_ts" -le "$now_ts" ]; then
  state_json="$(printf '%s\n' "$state_json" | jq 'del(.deadline_ts, .last_digest)')"
  deadline_ts=$((now_ts + max_seconds))
fi
last_digest="$(printf '%s\n' "$state_json" | jq -c '.last_digest // empty')"

while :; do
  pr_tmp="$(mktemp)"
  gh pr view -R "$owner/$repo" "$num" \
    --json number,state,isDraft,mergeable,mergeStateStatus,reviewDecision,reviewRequests,updatedAt,headRefOid,url,reactionGroups \
    --jq '{number,state,isDraft,mergeable,mergeStateStatus,reviewDecision,reviewRequests,updatedAt,headRefOid,url,reactionGroups}' > "$pr_tmp"
  pr_status=$?
  pr_json="$(cat "$pr_tmp")"
  rm -f "$pr_tmp"
  if [ "$pr_status" -ne 0 ] || [ -z "$pr_json" ]; then
    jq -cn --argjson status "$pr_status" \
      '{event:"blocked", reason:"pr_snapshot_failed", status:$status}'
    break
  fi

  checks_tmp="$(mktemp)"
  checks_err="$(mktemp)"
  gh pr checks -R "$owner/$repo" "$num" \
    --json name,bucket,state,workflow,link \
    --jq 'group_by(.bucket) | map({bucket: .[0].bucket, count: length, checks: map({name,state,workflow,link}) | sort_by(.name,.workflow,.link,.state)})' > "$checks_tmp" 2> "$checks_err"
  checks_status=$?
  checks_json="$(cat "$checks_tmp")"
  checks_error="$(cat "$checks_err")"
  rm -f "$checks_tmp" "$checks_err"
  if [ -z "$checks_json" ]; then
    if [ "$checks_status" -ne 0 ]; then
      if printf '%s\n' "$checks_error" | grep -qi 'no checks reported'; then
        checks_json='[{"bucket":"pending","count":0,"checks":[],"reason":"checks_not_reported_yet"}]'
      else
        jq -cn --argjson status "$checks_status" --arg error "$checks_error" \
          '{event:"blocked", reason:"checks_snapshot_failed", status:$status, error:$error}'
        break
      fi
    else
      checks_json="[]"
    fi
  fi

  reaction_targets="$(
    printf '%s\n' "$state_json" |
      jq -c '.approval_reaction_targets // []'
  )"
  reaction_status_file="$(mktemp)"
  reaction_error_file="$(mktemp)"
  printf '0' > "$reaction_status_file"
  approval_reactions="$(
    printf '%s\n' "$reaction_targets" | jq -c '.[]' |
      while IFS= read -r target; do
        kind="$(printf '%s\n' "$target" | jq -r '.kind')"
        id="$(printf '%s\n' "$target" | jq -r '.id // empty')"
        case "$kind" in
          issue) path="/repos/$owner/$repo/issues/$num/reactions?content=%2B1&per_page=100" ;;
          issue_comment) path="/repos/$owner/$repo/issues/comments/$id/reactions?content=%2B1&per_page=100" ;;
          review_comment) path="/repos/$owner/$repo/pulls/comments/$id/reactions?content=%2B1&per_page=100" ;;
          *) continue ;;
        esac
        reaction_tmp="$(mktemp)"
        if ! gh api --paginate "$path" > "$reaction_tmp" 2>> "$reaction_error_file"; then
          printf '1' > "$reaction_status_file"
          rm -f "$reaction_tmp"
          break
        fi
        jq --arg kind "$kind" --arg id "$id" \
          '.[] | {target_kind: $kind, target_id: $id, login: .user.login, created_at}' \
          "$reaction_tmp"
        rm -f "$reaction_tmp"
      done |
      jq -s '.'
  )"
  if [ "$(cat "$reaction_status_file")" != "0" ]; then
    reaction_error="$(cat "$reaction_error_file")"
    rm -f "$reaction_status_file" "$reaction_error_file"
    jq -cn --arg error "$reaction_error" \
      '{event:"blocked", reason:"reaction_snapshot_failed", error:$error}'
    break
  fi
  rm -f "$reaction_status_file" "$reaction_error_file"
  if [ -z "$approval_reactions" ]; then
    approval_reactions="[]"
  fi

  digest="$(
    jq -cn \
      --argjson pr "$pr_json" \
      --argjson checks "$checks_json" \
      --argjson approvalReactions "$approval_reactions" \
      '{
        state: $pr.state,
        draft: $pr.isDraft,
        mergeable: $pr.mergeable,
        mergeStateStatus: $pr.mergeStateStatus,
        reviewDecision: $pr.reviewDecision,
        reviewRequests: ($pr.reviewRequests // []),
        headRefOid: $pr.headRefOid,
        updatedAt: $pr.updatedAt,
        reactionGroups: ($pr.reactionGroups // []),
        approvalReactions: $approvalReactions,
        checks: $checks
      }'
  )"

  if [ "$(date +%s)" -ge "$deadline_ts" ]; then
    jq -cn --argjson digest "$digest" '{event:"timeout", digest:$digest}'
    printf '%s\n' "$state_json" |
      jq 'del(.deadline_ts, .last_digest)' > "$state_file"
    break
  fi

  if [ "$digest" = "$last_digest" ]; then
    sleep "${PR_WATCH_INTERVAL:-45}"
    continue
  fi

  jq -cn \
    --argjson state "$state_json" \
    --argjson digest "$digest" \
    --argjson deadline "$deadline_ts" \
    '$state + {last_digest: $digest, deadline_ts: $deadline}' > "$state_file"
  last_digest="$digest"

  # Emit only the changed digest. The model decides whether to finish,
  # continue the cheap wait, or enter full repair.
  printf '%s\n' "$digest"
  break
done
```
