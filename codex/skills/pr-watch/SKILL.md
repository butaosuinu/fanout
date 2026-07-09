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
This also works when `.git` is a linked-worktree file.

### Event handling

The helper emits one compact `key=value` line only for:

- `event=change`: inspect the compact fields; repair, finish, or continue.
- `event=blocked`: report `reason` and stop until the prerequisite changes.
- `event=timeout`: report the last compact status and stop.

No output means the snapshot was unchanged. Do not re-read full PR context.
For the same wait, call `wait` again with `PR_WATCH_CONTINUE=1`.

Enter the repair loop on:

- `mergeable=CONFLICTING` or `merge_state=DIRTY`
- `merge_state=BEHIND` when branch protection requires current base
- `checks_fail>0` or `checks_cancel>0`
- `review=CHANGES_REQUESTED`
- a changed head SHA
- an ambiguous `updatedAt` change that compact metadata cannot classify
- CI settling after this skill pushed, when final verification is still due

Stay in the cheap loop for pending CI, `mergeable=UNKNOWN`, unchanged status,
human approval, configured `:+1:`, or a review request with no known actionable
comment. For an ambiguous update, inspect latest event/comment metadata first;
fetch bodies only when it likely contains actionable work.

Treat `checks_reported=false` as unknown, not green. After a push, keep polling
until checks appear. A repository with no CI may finish without checks only
after its workflow and branch-protection configuration confirms none are
expected.

Finish only with `mergeable=MERGEABLE` and `merge_state=CLEAN|HAS_HOOKS`.
`BLOCKED`, `UNSTABLE`, `DRAFT`, `BEHIND`, `DIRTY`, and `UNKNOWN` are not
completion states even when checks and review fields otherwise look ready.

## Repair pass

Before each repair step, tell the user in one sentence what will be checked or
changed. Read only the relevant part of the [repair playbook](references/repair-playbook.md).

1. Refresh PR metadata, checks, unresolved threads, latest reviews, and paginated
   top-level comments as needed.
2. Reconfirm the PR head branch, author, push remote, and saved remote head SHA.
3. Handle conflict/base drift, failing CI, then actionable review feedback.
4. Run focused tests. Do not weaken tests, required checks, or workflows.
5. Commit intentionally and push with an explicit refspec. Use guarded
   `--force-with-lease=<ref>:<saved-sha>` only for an authorized history rewrite.
6. Reply to review feedback only after the fix is pushed. In this repository,
   write automatic review comments in Japanese.
7. After any push, discard old logs/thread state and return to a fresh cheap
   snapshot on the new head.

Repair ownership includes safe fixes that are clearly implied by CI or review.
Stop and ask for user judgment when behavior is ambiguous, the required access is
missing, a conflict needs semantic product judgment, or external CI cannot be
inspected.

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

## Limits and stop conditions

Default limits:

- 3 full repair passes
- 2 full comment/thread refreshes without a new head commit
- 1 CI log fetch per failing check name and head SHA
- 3 ambiguous-update full inspections
- 60 minutes of foreground watch

Normalize each pass's conflict files, failing jobs, and review paths. Stop when
the same actionable set repeats twice without progress, the same problem recurs
across three passes, a limit is reached, or an update cannot be classified
safely. Do not keep polling after `event=blocked` or `event=timeout`.

## Finish report

Report in 2-4 sentences:

- repair pass count and conflict / CI / review counts
- whether commits, pushes, replies, and cheap watch occurred
- approval source (`reviewDecision=APPROVED`, configured `:+1:`, or not required)
- final state: terminal, `mergeable=MERGEABLE` +
  `merge_state=CLEAN|HAS_HOOKS` + green + approved, timeout, or blocked

List remaining work only when the result is not complete.

## Never

- Create a PR, enable auto-merge, or broaden the user's authorization.
- Continue a pass using CI logs or review state fetched before a push.
- Treat EYES, clean prose, or an unconfigured reaction as literal `:+1:`.
- Resolve uncertain review threads on the reviewer's behalf.
- Claim background monitoring after the Codex session ends.
