---
name: fanout-issues
description: "Use from Codex CLI to create fanout-ready GitHub issue trees: a parent issue plus OPEN child issues linked with GitHub Sub-issues and mirrored in the parent task list. Use when the user asks Codex to decompose work into parent/child GitHub issues, prepare issues for the fanout CLI, create sub-issues for parallel dmux panes, or encode blocker waves for fanout --unblocked-only."
metadata:
  short-description: Create fanout-ready GitHub issue trees
---

# fanout-issues

Build GitHub issue hierarchies that `fanout` can consume without extra cleanup.
The output should work through both supported discovery paths: GitHub's
Sub-issues relationship and same-repo task-list rows in the parent body.

## When To Use

Use this skill when the user asks Codex to create a parent issue with child
issues, decompose a plan into GitHub issues, prepare work for `$fanout`, or
model blocker waves for `fanout --unblocked-only`.

Issue creation is a live GitHub mutation. Unless the user explicitly asked to
create the issues immediately, show the planned parent title, child list,
dependency graph, labels/assignees, and target repo before creating anything.

## Preconditions

- Use the repository the user is working in unless they provide `--repo OWNER/REPO` or name another repo.
- Verify `gh` is authenticated and `gh sub-issue --help` works. If the extension is missing, tell the user to install `gh extension install yahsan2/gh-sub-issue`.
- Keep all child issues in the same repo as the parent. `fanout` skips cross-repo task-list references.
- Preserve user-provided labels, assignees, milestones, and projects when they are relevant to both parent and children.

## Decompose

Create children that can run in parallel dmux panes:

- Give every child a clear, bounded deliverable and an acceptance checklist.
- Avoid children that all need to edit the same files unless the dependency is explicit.
- Split dependencies into waves. If B needs A, mark B as blocked by A instead of asking the agent to infer ordering.
- Do not create a catch-all "integration" child unless there is real integration work the parent cannot own.
- Keep issue titles specific enough to become readable pane names.

Use this child body shape:

```markdown
## Goal
One or two sentences describing the outcome.

## Scope
- In scope item
- In scope item

## Acceptance criteria
- [ ] Observable criterion
- [ ] Tests/docs/verification expectation

## Notes
Important context, links, or constraints.

## Blocked by
None
```

For blocked children, replace `None` with same-repo issue references:

```markdown
## Blocked by
- #123
- #124
```

Use this parent body shape:

```markdown
## Goal
Describe the full outcome.

## Fanout children
- [ ] #CHILD Child title
- [ ] #BLOCKED Blocked child title (blocked by #CHILD)

## Coordination notes
- Base branch/ref:
- Shared constraints:
- Suggested command after creation: $fanout #PARENT --unblocked-only
```

The `## Fanout children` rows must be same-repo task-list rows in the form
`- [ ] #N Title`. Append `(blocked by #A, #B)` to blocked rows so
`fanout --unblocked-only` can defer them even before child bodies are hydrated.

## Create Issues

Prefer `gh issue create` plus `gh sub-issue add` over `gh sub-issue create`
because `gh issue create --body-file` handles multiline bodies reliably.
Write bodies to temporary Markdown files first instead of embedding large
Markdown strings in shell commands.

Recommended sequence:

```bash
repo="OWNER/REPO"

parent_url=$(gh issue create -R "$repo" \
  --title "Parent title" \
  --body-file /tmp/fanout-parent.md)
parent_num="${parent_url##*/}"

child_url=$(gh issue create -R "$repo" \
  --title "Child title" \
  --body-file /tmp/fanout-child-1.md)
child_num="${child_url##*/}"

gh sub-issue add "$parent_num" "$child_num" -R "$repo"
```

For an existing parent, fetch and preserve its current body before appending or
replacing only the fanout section:

```bash
gh issue view "$parent_num" -R "$repo" --json body -q .body > /tmp/fanout-parent-existing.md
gh issue edit "$parent_num" -R "$repo" --body-file /tmp/fanout-parent-final.md
```

For an existing child, do not recreate it. Link it:

```bash
gh sub-issue add "$parent_num" "$child_num" -R "$repo"
```

## Update Parent

After every child number is known, edit the parent body so it mirrors the
GitHub Sub-issues relationship:

- List every OPEN child under `## Fanout children`.
- Keep rows unchecked until the child issue is actually complete.
- Add `(blocked by #N)` trailers for blocked children.
- Include the suggested `$fanout #PARENT --unblocked-only` command when any blockers exist; otherwise `$fanout #PARENT` is enough.
- Preserve unrelated existing parent text.

## Validate

Run:

```bash
gh sub-issue list "$parent_num" -R "$repo" --json number,title,state
gh issue view "$parent_num" -R "$repo" --json body -q .body
```

Confirm:

- Every intended child appears in `gh sub-issue list` with `state` open.
- The parent body has a same-repo task-list row for every child.
- Blocked children have both child-body `## Blocked by` references and parent-row `(blocked by #N)` trailers.
- No cross-repo `owner/repo#N` row is required for fanout discovery.

If a dmux session is already running and the user wants an end-to-end check,
run `fanout <parent> --dry-run` from any cwd. Otherwise do not require dmux
just to validate issue creation.
