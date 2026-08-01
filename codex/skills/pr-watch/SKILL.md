---
name: pr-watch
description: "Actively watch and repair an existing GitHub PR until it is mergeable, green, and approved. Use for PR を見張って, CI/コンフリクト/レビュー対応, あとはよろしく, babysit/autofix this PR, /autofix-pr, or when work must continue until a configured :+1: arrives."
---

# pr-watch

既存 PR を foreground で監視し、対応可能な conflict、CI failure、review request を
修正する。監視中だけ動作し、Codex セッション終了後も監視が続くとは表現しない。

## Contract

PR 作成後または「あとはよろしく」では、次の状態まで repair と watch を所有する。

- PR が OPEN、non-draft、`mergeable=MERGEABLE` で、`mergeStateStatus` が
  `CLEAN` または `HAS_HOOKS`
- required checks が pass / skipping
- actionable な unresolved review work がない
- review 不要、または `reviewDecision=APPROVED`

ユーザーが literal `:+1:` を停止条件にした場合は、その対象と actor の反応まで待つ。
ただし `:+1:` は soft signal であり、`REVIEW_REQUIRED` や
`CHANGES_REQUESTED` を満たさない。MERGED / CLOSED は terminal として終了し、その状態を
報告する。auto-merge は明示指示がない限り行わない。

## Two loops

1. **Repair loop**: model-owned。必要な log、thread、diff、source を読み、修正、test、
   commit、push、reply を行う。
2. **Watch loop**: shell-owned。compact status だけを読み、同じ digest を抑止する。

通常の流れは次のとおり。

```text
repair pass -> cheap watch -> actionable event -> repair pass -> cheap watch -> completion
```

approval / reaction 待ちで full thread、comment、diff、CI log を繰り返し取得しない。

## Start

1. git repository 内か確認し、`gh auth status` を通す。
2. 引数の PR 番号 / URL、または current branch の PR を解決する。PR がなければ終了する。
3. `gh pr view` で author、head repository、head/base branch を確認する。
4. local branch と PR head が違う場合は push せず、正しい head を checkout する。
5. 自分が作成し、push 権限を持つ topic branch だけを repair 対象にする。
6. fresh compact snapshot の直後に review metadata probe を実行する。compact PR
   fields だけで「コメントなし」または完了と判定しない。

Target resolution、full review fetch、rebase、CI、review reply の具体的なコマンドは
[repair playbook](references/repair-playbook.md) を必要なときだけ読む。

## Cheap watcher

実装は [scripts/watch-pr.sh](scripts/watch-pr.sh)。`jq` は不要で、GitHub への write は
行わない。

```bash
watcher="${CODEX_DIR:-${CODEX_HOME:-$HOME/.codex}}/skills/pr-watch/scripts/watch-pr.sh"

# One compact snapshot. A fresh invocation starts a new deadline/digest.
"$watcher" snapshot --repo OWNER/REPO --pr N --reaction-target issue

# Continue the same wait and emit only change, blocked, or timeout.
PR_WATCH_CONTINUE=1 "$watcher" wait --repo OWNER/REPO --pr N --reaction-target issue

# Probe comment/review presence and fixed-size metadata digests without bodies.
"$watcher" review-probe --repo OWNER/REPO --pr N

# Remove only this repo + PR state.
"$watcher" reset --repo OWNER/REPO --pr N
```

Options and environment:

- Repeat `--reaction-target issue`, `issue_comment:ID`, or `review_comment:ID`.
  Pass the identical target set to `snapshot` and every continued `wait`.
- Set `PR_WATCH_PLUS1_ACTOR_RE` to the allowed actor login ERE. Without it,
  reactions are status only and cannot satisfy the configured `:+1:` gate.
- `PR_WATCH_INTERVAL` defaults to 45 seconds.
- `PR_WATCH_MAX_SECONDS` defaults to 3600 seconds.
- Set `PR_WATCH_CONTINUE=1` only for the same foreground wait. Omit it for a new
  user-invoked watch so stale digest/deadline state is cleared.

The helper stores only digest/deadline state below
`git rev-parse --git-path pr-watch-state`, scoped by GitHub repo and PR number.
This also works when `.git` is a linked-worktree file. `review-probe` is
stateless: it writes neither watcher state nor comment bodies.

### Event handling

The helper emits one compact `key=value` line only for:

- `event=change`: inspect the compact fields; repair, finish, or continue.
- `event=review_probe`: apply the metadata gate below.
- `event=change reason=review_probe_changed`: the PR changed during the probe;
  rerun `review-probe` before making a completion decision.
- `event=blocked`: report `reason` and stop until the prerequisite changes.
- `event=timeout`: report the last compact status and stop.

No watcher output means the compact snapshot was unchanged. Do not re-read body
content when the current review fingerprint was already audited. For the same
wait, call `wait` again with `PR_WATCH_CONTINUE=1`.

### Review metadata gate

Run `review-probe` after a fresh snapshot, after a changed head or `updatedAt`,
and immediately before completion. It first reads only `totalCount`; non-empty
surfaces are then reduced to sorted metadata digests. Its output never contains
`body`, `bodyText`, or `diffHunk`.

For unresolved threads, the digest includes every reply's ID, author, and
timestamps. The helper fully paginates that metadata so edits to intermediate
replies cannot reuse an older audit.

- If `top_comments=0`, `reviews=0`, and `unresolved_threads=0`, mark that exact
  `head` + `fingerprint` current without fetching bodies.
- On a fresh user invocation, treat every non-empty surface as unaudited. Fetch
  bodies only for surfaces whose count is non-zero.
- During the same foreground run, remember each audited surface digest. Fetch
  bodies only when its digest changes and its count remains non-zero. Do not
  persist body or audit state across a fresh invocation.
- For every fetched body surface, validate a stable `totalCount`, a complete
  aggregate node count, non-empty unique node IDs, and the exact metadata fields
  used by `review-probe`. Accept the bodies only when the body-derived surface
  digest matches the digest that selected the fetch. For unresolved threads,
  validate both the all-thread connection and every full comment connection.
- After that validation, rerun `review-probe`. Accept the audit only when `head`
  and `fingerprint` still match the values that selected those bodies.
- A blocked, partial, null, or internally changing probe is never evidence for
  zero actionable comments.

Enter the repair loop on:

- `mergeable=CONFLICTING` or `merge_state=DIRTY`
- `merge_state=BEHIND` when branch protection requires current base
- `checks_fail>0` or `checks_cancel>0`
- `review=CHANGES_REQUESTED`
- a changed head SHA
- a changed `updatedAt` where any non-empty surface digest is not already audited
- CI settling after this skill pushed, when final verification is still due

Stay in the cheap loop for pending CI, `mergeable=UNKNOWN`, unchanged status,
human approval, configured `:+1:`, or a review request with no known actionable
comment. Use `review-probe` to classify a review-related update; never fetch body
content merely because compact PR metadata changed.

The helper polls only GitHub-required checks; optional checks do not enter its
digest or CI repair triggers. `no required checks reported` is a known empty
required-check set. Treat a generic `checks_reported=false` as unknown, not
green, and after a push keep polling while it remains false. Completion requires
`checks_reported=true` (including a known empty required-check set), zero
pending/failing/cancelled required checks, and a ready merge state.

Finish with `mergeable=MERGEABLE` and `merge_state=CLEAN|HAS_HOOKS`.
`BLOCKED`, `UNSTABLE`, `DRAFT`, `BEHIND`, `DIRTY`, and `UNKNOWN` are not
completion states even when required checks and review fields otherwise look
ready.

## Repair pass

Before each repair step, tell the user in one sentence what will be checked or
changed. Read only the relevant part of the [repair playbook](references/repair-playbook.md).

1. Refresh PR metadata and checks, then run `review-probe`. Fetch unresolved
   thread, latest-review, or paginated top-level-comment bodies only for a
   non-empty surface whose digest is not current.
2. Reconfirm the PR head branch, author, push remote, and saved remote head SHA.
3. Handle conflict/base drift, failing CI, then actionable review feedback.
4. Run focused tests while editing. Do not weaken tests, required checks, or
   workflows.
5. Commit intentionally. Before each push, run the repository's canonical full
   gate once against the final commit (prefer an umbrella target such as
   `make check` when the project defines one; otherwise resolve it from
   AGENTS.md, CLAUDE.md, or the build files). Then push with an explicit
   refspec. Use guarded `--force-with-lease=<ref>:<saved-sha>` only for an
   authorized history rewrite.
6. Reply to review feedback only after the fix is pushed. In this repository,
   write automatic review comments in Japanese.
7. After any push, discard old logs/thread classification and return to a fresh
   snapshot plus `review-probe` on the new head.

Repair ownership includes safe fixes that are clearly implied by CI or review.
Stop and ask for user judgment when behavior is ambiguous, the required access is
missing, a conflict needs semantic product judgment, or external CI cannot be
inspected.

### Triage review findings before fixing

Do not fix every finding. When the repository declares a supported-environment
matrix and non-goals, sort each finding by two tests: **is it reachable inside
that matrix, and is it outside the explicit non-goals?** Reachability alone is
not enough — the non-goals are reachable by construction, so a single-axis rule
would make every one of them a required fix.

Resolve the matrix from the repository's agent instruction file (here, the
`Automated PR Review Scope` section of `AGENTS.md`) **at the PR base**, never
from the branch's own copy. A PR that edits its own scope document could
otherwise reply "out of scope" to a genuinely actionable finding and end repair.

| Verdict | Action |
| --- | --- |
| Reachable and not a non-goal | Fix it. Severity (P1 / P2) does not downgrade it |
| Unreachable, or an explicit non-goal | Do not fix. Reply in the thread with the rationale and the scope line that applies |
| Ambiguous, or fixing exceeds the PR's scope | Stop and ask the user. Do not decide this alone |

Fixing silently and closing silently are both prohibited — an unrecorded verdict
comes back as the same finding next round.

Never defer a reachable defect on your own authority. Splitting one out to a
follow-up issue is the user's call, and until they make it the PR stays
incomplete rather than completing with a known in-matrix defect.

When a round contains only out-of-scope findings, reply and end review repair
without editing. Fixing them adds code, which widens the surface the next
review enumerates, and the rounds stop converging.

## Push safety

- Force push only when the PR author is the authenticated user and the head
  repository/branch is writable by that user.
- `maintainerCanModify=true` is not authorization for history rewrite.
- Match the remote to `headRepository.nameWithOwner`; do not assume `origin`.
- Fetch the PR head before work, verify it is already contained in local HEAD,
  and save its SHA.
- Fetch again immediately before push. If the remote SHA moved, do not push;
  incorporate it or escalate.
- Never use plain `--force` or an unqualified `--force-with-lease`.
- Never overwrite another author's PR, a protected branch, or a fork with
  unclear ownership.
- Never bypass a repository push gate (a pre-push hook or a PreToolUse deny)
  with `--no-verify` or by rewriting the hooks configuration. A denied push
  means the pushed tip has not passed the repository gate; run the canonical
  full gate on the final commit and push again.

## Limits and stop conditions

Default limits:

- 3 full repair passes
- 2 full review/comment/thread body refreshes for the same metadata fingerprint
- 1 CI log fetch per failing check name and head SHA
- 3 ambiguous-update full inspections
- 60 minutes of foreground watch

Normalize each pass's conflict files, failing jobs, and review paths. Stop when
the same actionable set repeats twice without progress, the same problem recurs
across three passes, a limit is reached, or an update cannot be classified
safely. Do not keep polling after `event=blocked` or `event=timeout`.

An automated reviewer may re-review on every push. When each fix produces a
fresh finding set, the repeat detector never fires and the loop runs forever.
Stop and hand the state to the user when review findings keep arriving new
across three passes and a high share of them triage as out of scope.

## Finish report

Report in 2-4 sentences:

- repair pass count and conflict / CI / review counts
- whether commits, pushes, replies, and cheap watch occurred
- approval source (`reviewDecision=APPROVED`, configured `:+1:`, or not required)
- final state: terminal, `mergeable=MERGEABLE` +
  `merge_state=CLEAN|HAS_HOOKS` + required checks ready + approved, timeout, or
  blocked

List remaining work only when the result is not complete.

## Never

- Create a PR, enable auto-merge, or broaden the user's authorization.
- Continue a pass using CI logs or review state fetched before a push.
- Treat EYES, clean prose, or an unconfigured reaction as literal `:+1:`.
- Resolve uncertain review threads on the reviewer's behalf.
- Re-trigger an automated review by hand (`@codex review` and the like). Each
  trigger re-enumerates the whole changed surface and adds a round. Only do it
  when the user explicitly asks.
- Claim background monitoring after the Codex session ends.
