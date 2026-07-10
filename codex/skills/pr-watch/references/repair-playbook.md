# pr-watch repair playbook

Read only the section needed by the current `event=change`. The cheap watcher is
documented in `../SKILL.md`; this file is for model-owned repair work.

## Contents

- [Resolve the target](#resolve-the-target)
- [Fetch actionable review state](#fetch-actionable-review-state)
- [Authorize the push remote](#authorize-the-push-remote)
- [Guard a history rewrite](#guard-a-history-rewrite)
- [Repair conflict or base drift](#repair-conflict-or-base-drift)
- [Repair CI](#repair-ci)
- [Address review feedback](#address-review-feedback)
- [Refresh after a push](#refresh-after-a-push)

## Resolve the target

With no explicit PR, let `gh` resolve the current branch. Pass a number or URL
only when the user supplied one; never pass an empty string.

```bash
gh pr view ${pr:+"$pr"} \
  --json number,state,isDraft,mergeable,mergeStateStatus,reviewDecision,reviewRequests,headRefName,baseRefName,author,headRepository,headRepositoryOwner,isCrossRepository,maintainerCanModify,url,title,statusCheckRollup \
  --jq '{number,state,isDraft,mergeable,mergeStateStatus,reviewDecision,reviewRequests,headRefName,baseRefName,author,headRepository,headRepositoryOwner,isCrossRepository,maintainerCanModify,url,title,statusCheckRollup}'
```

Compare `headRefName` with `git branch --show-current`. Checkout the PR head or
stop before editing when they differ. Do not push a different local branch over
the PR head.

Read required-check buckets rather than relying on the command exit code;
pending and failing checks can make `gh pr checks` nonzero. Optional checks stay
out of these buckets and CI repair triggers; `mergeStateStatus` still governs
final readiness independently.

```bash
gh pr checks ${pr:+"$pr"} \
  --required \
  --json name,state,bucket,link,workflow \
  --jq 'sort_by(.bucket,.workflow,.name) | .[] | {bucket,workflow,name,state,link}' || true
```

Treat `no required checks reported` as a known empty required-check set. Other
empty or failed responses remain unknown or blocked; do not coerce them to green.

## Fetch actionable review state

Fetch full bodies only after a compact event suggests review work. Paginate
unresolved threads and preserve both the top-level and latest comment.

```bash
gh api graphql --paginate -f query='
  query($owner:String!,$repo:String!,$num:Int!,$endCursor:String){
    repository(owner:$owner,name:$repo){
      pullRequest(number:$num){
        reviewThreads(first:100,after:$endCursor){
          pageInfo{hasNextPage endCursor}
          nodes{
            isResolved path line originalLine diffSide
            topLevel:comments(first:1){
              nodes{fullDatabaseId body diffHunk author{login} createdAt}
            }
            latest:comments(last:1){nodes{author{login} createdAt body}}
          }
        }
      }
    }
  }' -F owner="$owner" -F repo="$repo" -F num="$num"
```

Also inspect latest review summaries and top-level PR comments. `gh pr view
--json comments` is limited, so paginate when comments affect completion or a
blocked decision.

```bash
gh api graphql --paginate -f query='
  query($owner:String!,$repo:String!,$num:Int!,$endCursor:String){
    repository(owner:$owner,name:$repo){
      pullRequest(number:$num){
        latestReviews(first:100){nodes{author{login} state body submittedAt}}
        comments(first:100,after:$endCursor){
          pageInfo{hasNextPage endCursor}
          nodes{databaseId author{login} body createdAt updatedAt}
        }
      }
    }
  }' -F owner="$owner" -F repo="$repo" -F num="$num"
```

Treat a thread as handled when the latest message is this agent's response and
no reviewer replied afterward. A newer reviewer response supersedes the
top-level wording.

## Authorize the push remote

Resolve the authenticated login before any repair that may push.

```bash
me="$(gh api user --jq .login)"
```

Force push is allowed only when the PR author is `$me` and the head repository
owner is `$me` for fork PRs. `maintainerCanModify=true` does not authorize a
history rewrite.

Find or add a remote whose repository is exactly
`headRepository.nameWithOwner`. Do not assume `origin`, especially for forks or
triangular clones. Fetch and push with explicit remote and refspec names.

## Guard a history rewrite

Before editing or rebasing, fetch the PR head, confirm the remote tip is already
contained in local HEAD, and save it.

```bash
git fetch "$head_remote" "$head"
pr_head_before_work="$(git rev-parse FETCH_HEAD)"
git merge-base --is-ancestor "$pr_head_before_work" HEAD
```

If the ancestry check fails, stop. Incorporate the remote-only commit or ask the
user. `--force-with-lease` alone is insufficient because another fetch can move
the local tracking ref.

Immediately before push, fetch again and compare with the saved SHA.

```bash
git fetch "$head_remote" "$head"
test "$(git rev-parse FETCH_HEAD)" = "$pr_head_before_work"
git push \
  --force-with-lease="refs/heads/$head:$pr_head_before_work" \
  "$head_remote" HEAD:"$head"
```

Do not redo the ancestry test after rebase: the old tip need not be an ancestor
of rewritten HEAD. The saved SHA comparison is the concurrency guard.

## Repair conflict or base drift

Use this path for `CONFLICTING`, `DIRTY`, or policy-required `BEHIND`.

1. Preserve a dirty worktree; never discard the user's changes.
2. Run the history-rewrite guard and save `pr_head_before_work`.
3. Resolve the base repository remote from PR metadata; do not assume
   `origin/main`.
4. Fetch the named base branch and rebase onto that exact `FETCH_HEAD`.

```bash
git fetch "$base_remote" "$base"
git rebase FETCH_HEAD
```

Resolve only mechanical conflicts whose intent is clear. For behavioral or
product ambiguity, `git rebase --abort` and report the file/hunk and decision
needed. After tests pass, re-fetch the PR head, compare it with the saved SHA,
and use the qualified lease command above.

`BEHIND` alone is not always a repair trigger. If branch protection does not
require the latest base and GitHub reports mergeable with green checks, avoid a
churn-only rebase.

## Repair CI

For each required-check `fail` or `cancel` bucket, identify the failing provider
and head SHA. For GitHub Actions, inspect failed logs only and always name the
repository.

```bash
gh run view -R "$owner/$repo" RUN_ID --log-failed
```

Read one log per failing check name and head SHA. Reproduce locally when
possible, identify the cause, make the narrow fix, and run focused tests before
the broader repository gate.

Never delete tests, add skips, or weaken required workflows to get green. If the
failure is clearly flaky or infrastructure-owned, request one rerun. Stop if it
recurs. For external CI, follow the check link; if the log is inaccessible,
report the provider and URL as blocked rather than guessing.

Commit the real code fix first, then run the repository's canonical full gate
once against that final commit (prefer an umbrella target such as `make check`
when the project defines one; otherwise resolve it from AGENTS.md, CLAUDE.md,
or the build files). A CI failure that reproduces locally recurs on the next
push unless the full gate passed. Repositories may enforce this with a push
gate; never bypass it with `--no-verify`. Then push with an explicit refspec:

```bash
git push "$head_remote" HEAD:"$head"
```

Use the qualified force-with-lease form only when history was rewritten and the
authorization/remote-tip guards passed.

## Address review feedback

Consider unresolved inline threads, review summaries, and paginated top-level
comments. Separate actionable defects from questions, stale comments, and
requests requiring product judgment.

For an actionable request:

1. Trace the behavior in the current head and confirm the issue.
2. Implement the narrow fix.
3. Run focused tests while editing.
4. Commit, run the repository's canonical full gate once against the final
   commit (same resolution as in Repair CI), then push.
5. Reply after GitHub sees the pushed commit.

Use the top-level inline comment's `fullDatabaseId` to reply in-thread. In this
repository, automatic review replies are Japanese; keep file paths, symbols,
commands, and quoted code unchanged. Usually leave thread resolution to the
reviewer. Resolve it yourself only when the fix is unambiguous and repository
policy permits it.

Do not force a speculative implementation when a reviewer asks for a product or
architecture choice. Summarize the alternatives and ask the user.

## Refresh after a push

After every push:

1. Discard all pre-push CI logs, mergeability, and review snapshots.
2. Run the installed `pr-watch/scripts/watch-pr.sh snapshot` against the new head.
3. Continue with `PR_WATCH_CONTINUE=1 ... wait` while checks settle.
4. Enter another repair pass only for a new actionable event.

The final pass must confirm current-head checks, mergeability, review decision,
and any configured reaction target. EYES or positive prose does not substitute
for a literal configured `:+1:`.
