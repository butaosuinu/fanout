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

Run the metadata-only gate before fetching body content:

```bash
"$watcher" review-probe --repo "$owner/$repo" --pr "$num"
```

The probe emits counts and fixed-size digests for top-level comments, latest
reviews, and unresolved threads. It never emits `body`, `bodyText`, or
`diffHunk`. Unresolved-thread digests include fully paginated metadata for every
reply. On a fresh invocation, fetch a surface's bodies only when its count is
non-zero. During the same foreground run, skip body fetches for an already
audited surface digest. If the probe reports `reason=review_probe_changed`, run
it again; if it blocks or fails, do not treat the surface as empty.

For a non-empty, unaudited unresolved-thread surface, first paginate unresolved
thread IDs.

```bash
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
  }' -F owner="$owner" -F repo="$repo" -F num="$num"
```

For each unresolved `thread_id`, paginate every comment body. Keep
`fullDatabaseId` for an inline reply through the REST endpoint.

```bash
gh api graphql --paginate -f query='
  query($threadId:ID!,$endCursor:String){
    node(id:$threadId){
      ... on PullRequestReviewThread{
        id
        comments(first:100,after:$endCursor){
          totalCount
          pageInfo{hasNextPage endCursor}
          nodes{id fullDatabaseId body diffHunk author{login} createdAt updatedAt}
        }
      }
    }
  }' -F threadId="$thread_id"
```

For a non-empty, unaudited latest-review surface, paginate review summaries.

```bash
gh api graphql --paginate -f query='
  query($owner:String!,$repo:String!,$num:Int!,$endCursor:String){
    repository(owner:$owner,name:$repo){
      pullRequest(number:$num){
        latestReviews(first:100,after:$endCursor){
          totalCount
          pageInfo{hasNextPage endCursor}
          nodes{id author{login} state body submittedAt updatedAt commit{oid}}
        }
      }
    }
  }' -F owner="$owner" -F repo="$repo" -F num="$num"
```

For a non-empty, unaudited top-level-comment surface, paginate PR comments.
`gh pr view --json comments` is limited and cannot replace this query.

```bash
gh api graphql --paginate -f query='
  query($owner:String!,$repo:String!,$num:Int!,$endCursor:String){
    repository(owner:$owner,name:$repo){
      pullRequest(number:$num){
        comments(first:100,after:$endCursor){
          totalCount
          pageInfo{hasNextPage endCursor}
          nodes{id author{login} body createdAt updatedAt}
        }
      }
    }
  }' -F owner="$owner" -F repo="$repo" -F num="$num"
```

Before classifying any fetched body, validate its metadata against the probe
that selected the fetch:

1. Require one stable `totalCount` per paginated connection, non-empty unique
   node IDs, and an aggregate node count equal to `totalCount`.
2. Project the body results into the same tab-separated rows as `review-probe`.
   Sort top-level comments as `id, author, createdAt, updatedAt`; sort reviews as
   `id, author, state, nullable submittedAt, updatedAt, commit OID`. Use an empty
   author, nullable value, or commit OID when GraphQL returns null. For threads,
   sort unresolved rows as `thread ID, false, path, line, originalLine,
   diffSide, comment total` and comment rows as `thread ID, comment ID, author,
   createdAt, updatedAt`. Prefix the two sets with `thread` and `comment`, then
   concatenate them in that order.
3. Hash those rows with `git hash-object`. Require each body-derived surface
   digest to equal the `top_digest`, `reviews_digest`, or `threads_digest` that
   selected the fetch. For threads, validate the complete all-thread connection
   before filtering resolved nodes and validate every full comment connection.

Then rerun `review-probe`. Use the bodies only when its `head` and combined
`fingerprint` still match the values that selected the fetch. GraphQL errors,
null PR data, duplicate or missing nodes, digest mismatches, and incomplete
pagination are blocked states, not zero-item results.

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
needed. After tests pass, run the repository's canonical full gate once
against the rebased HEAD (same resolution as in Repair CI) — auto-resolved
hunks that break the build surface here, not on CI. Then re-fetch the PR
head, compare it with the saved SHA, and use the qualified lease command
above.

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

For Codex connector feedback, first identify the saved current head SHA and a
submitted review record for that SHA. Fetch all currently visible actionable
threads, review summaries, and top-level comments for that head and treat them
as one **current-head review batch**. Do not begin from an individual
notification while the completed review record is unavailable. If another
current-head finding appears before commit, rebuild the batch before editing.

### Track repair waves for this invocation

Start a process-local connector repair-wave count at zero for each explicit
invocation. Retain it only in the same foreground run, including
`PR_WATCH_CONTINUE=1` continuations. Do not persist it or reconstruct it from PR
history. Increment it once only after an actionable current-head review batch
causes an edit, one intentional commit, and one push. A decline-only reply is
not a repair wave. A later explicit invocation starts from zero. Do not start a
fourth repair wave in one invocation.

### Triage findings

Read `## Code Review Rules` from the PR base, not a copy changed by the target.
A finding is actionable only when a concrete trigger is reachable under
documented user-facing prerequisites, or it violates an existing test, issue
acceptance criterion, documented contract, or required safe rejection or
fail-closed behavior. Do not request new support for an unpromised environment.
An explicitly accepted unsupported input and its required safe rejection remain
in scope.

Reject a finding only with concrete evidence that the trigger is unreachable,
the behavior is an explicit non-goal, or the applicable contract is already
satisfied. Reply with the base-side scope rule and that evidence. If every
finding in the current-head batch is rejected, do not edit, commit, or push;
continue readiness checks with no actionable review work. This does not satisfy
`CHANGES_REQUESTED` or a required approval. Reconsider a rejection when a new
diff, reviewer reply, or explicit contract invalidates its rationale. Ask the
user when reachability, product support, contract scope, or required human
review is ambiguous.

Cluster the batch by root cause. Confirm every affected branch, entrypoint, and
consumer, then fix the confirmed occurrences together. One review wave produces
one intentional commit and one push; it does not produce one commit per comment.

For an actionable request:

1. Trace the behavior in the current head and confirm the issue plus its
   same-root-cause occurrences.
2. Implement the narrow complete fix for the whole cluster.
3. Run focused tests while editing.
4. Commit, run the repository's canonical full gate once against the final
   commit (same resolution as in Repair CI), then push.
5. Reply after GitHub sees the pushed commit.

Use the top-level inline comment's `fullDatabaseId` to reply in-thread. In this
repository, automatic review replies are Japanese; keep file paths, symbols,
commands, and quoted code unchanged. Usually leave thread resolution to the
reviewer. Resolve it yourself only when the fix is unambiguous and repository
policy permits it.

Do not manually request another Codex review for the same head SHA. When an
explicit request is required, send it once after the next repair commit is
visible on GitHub. Stop after three connector review-repair waves per explicit
invocation; report the remaining batch and require human follow-up instead of
starting a fourth.

Do not force a speculative implementation when a reviewer asks for a product or
architecture choice. Summarize the alternatives and ask the user.

## Refresh after a push

After every push:

1. Discard all pre-push CI logs, mergeability, and review snapshots.
2. Run the installed `pr-watch/scripts/watch-pr.sh snapshot` against the new head.
3. Run `review-probe`; fetch body content only for non-empty unaudited digests.
4. Continue with `PR_WATCH_CONTINUE=1 ... wait` while checks settle.
5. Enter another repair pass only for a new actionable event.

The final pass must confirm current-head checks, mergeability, review decision,
the latest review-probe fingerprint, and any configured reaction target. Reuse
body classification when every non-empty surface digest is already audited.
EYES or positive prose does not substitute for a literal configured `:+1:`.
