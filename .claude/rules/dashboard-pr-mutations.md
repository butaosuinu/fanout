---
paths:
  - "internal/ui/dashboard/**"
  - "internal/app/prmerge/**"
  - "internal/infra/ghissue/**"
  - "web/src/features/merge/**"
  - "web/src/features/diff/**"
  - "web/src/transport/**"
---

# Dashboard PR mutations

These files implement or feed the dashboard's two mutation endpoints,
`POST /api/pr/merge` and `POST /api/pr/delete-branch`. The handlers
(`internal/ui/dashboard/merge.go`, `deletebranch.go`) and `internal/app/prmerge`
are class H (human review); the other paths keep the class
`docs/architecture.ja.md` assigns them (`ghissue` and `web/src/transport` M,
the web features A apart from `diff.ts`). Read
`docs/dashboard-pr-mutations.ja.md` before changing any of them; when a change
moves an invariant, update that document in the same PR.

The invariants a local edit breaks most easily:

- Exactly two mutations, each scoped to one pull request. They never touch the
  local tree, local refs, worktrees, `.fanout/state.json`, or pane input, and
  never pass `--admin`, `--auto`, or `--delete-branch` to `gh`.
- The client names number, head SHA, and base; all three are re-read live and
  the SHA goes to GitHub as `--match-head-commit`. Review approval and CI are
  deliberately not gates.
- `gh pr merge` exiting 0 is not a merge. The outcome is confirmed against
  GitHub, and an unreadable outcome holds the PR in
  `<git-common-dir>/fanout/merge-claims.json`, the endpoint's only local write.
- The branch delete fences on the head SHA GitHub reports on the live read,
  never on the SHA the client named.
- Two gaps are deliberate (the base check reads the local remote-tracking ref;
  the worktree is pinned by commit-and-clean only). Read why they are open
  before closing either.
