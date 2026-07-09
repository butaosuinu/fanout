---
name: fanout-plan
description: "Decompose an implementation plan into an issue-less fanout spec, preview it, and launch isolated task panes. Use for `$fanout-plan`, `fanout plan`, `/fanout plan`, or requests to fan out an approved or proposed plan."
---

# fanout-plan

Turn one implementation plan into a versioned JSON spec, then let
`fanout plan` create the worktrees and panes. Perform all decomposition,
dependency design, naming, and spec authoring in this skill; the Go CLI does
not infer tasks from prose.

## Resolve the source plan

Use the first applicable source:

1. Resolve an explicit argument from `$fanout-plan <arg>`,
   `/fanout plan <arg>`, or the user's message.
   - Use an existing file path as the source.
   - If `.fanout/plans/<arg>.json` exists, treat `<arg>` as a saved plan slug
     and skip new spec authoring.
   - Stop on an explicit missing path or slug. Do not silently choose another
     source.
   - Accept either a finished plan or a raw prompt file from the TUI. Ask only
     when the raw request is too vague to split safely.
   - The file may also be an issue-sourced coordinator briefing from the TUI's
     issue mode: the issue number, title, and body plus a "Fan-out
     instructions" section naming the default `--agent` for tasks. Follow
     those instructions — set `plan.source` to the issue, never invent GitHub
     issue numbers, and honor its Refs #N / Closes #N discipline (task PRs say
     "Refs #N", never "Closes #N"; after the live fan-out, comment the plan
     slug and task list on the issue).
2. Use the current conversation's approved implementation plan.
3. Otherwise use the newest `<proposed_plan>...</proposed_plan>` block in the
   current conversation.

Do not scan Claude plan directories or unrelated local files. If multiple
conversation candidates remain plausible, label them briefly and ask the user
to choose. If none exists, request a path or a proposed plan.

## Decompose the work

- Give each task one bounded deliverable and an acceptance checklist in its
  `briefing`.
- Separate dependency waves with `blocked_by`. Never rely on array order or
  `wave` alone.
- Avoid overlapping file ownership. Combine tasks or block one on the other
  when both must edit the same file.
- Keep integration in the owning task or parent session unless it is a real,
  independently bounded deliverable.
- Use concrete titles that work as pane labels.

Apply these names:

- `plan.slug`: lowercase kebab-case, preferably 2–4 words.
- `task.id`: required, stable lowercase kebab-case, preferably 2–4 words.
- `task.display_name`: optional, readable, preferably at most 40 characters.
- `task.slug`: optional exact worktree slug. Prefer omission so fanout creates
  a plan-qualified slug. When set, make it globally unique under
  `.fanout/worktrees`, start it with an alphanumeric, and use only lowercase
  alphanumerics and hyphens.

## Author the spec

Write a new spec to `/tmp/fanout-plan-<plan.slug>.json` with the available file
editing tool. Do not use shell heredocs, `echo >`, or ad-hoc redirection for
multiline JSON.

Use this schema:

```json
{
  "version": 1,
  "plan": {
    "slug": "launch-plan",
    "title": "Launch plan",
    "source": "conversation-approved-plan",
    "base_branch": "main"
  },
  "tasks": [
    {
      "id": "base-types",
      "title": "Define base types",
      "briefing": "## Goal\n...\n\n## Scope\n- ...\n\n## Acceptance checklist\n- [ ] ...",
      "slug": "launch-plan-base-types",
      "display_name": "Base types",
      "branch": "feat/base-types",
      "wave": "1",
      "blocked_by": []
    }
  ]
}
```

Require `version`, `plan.slug`, `plan.title`, `tasks`, and each task's `id`,
`title`, and `briefing`. Treat `source`, `base_branch`, `slug`,
`display_name`, `branch`, `wave`, and `blocked_by` as optional, except that
real dependencies require `blocked_by`.

Prefer `plan.base_branch` when the source names a base. Otherwise let fanout
resolve the repository default, use the current branch in a repository without
`origin`, or honor `--base-branch`. Never put agent assignments in the JSON;
use repeatable CLI `--agent task-id=name` flags.

## Build the command

Run from the target repository worktree. Preserve a user-supplied `--agent`
or `FANOUT_AGENT`; otherwise use `--agent codex`. Preserve explicit per-task
overrides. Do not infer Claude or Codex from task size, breadth, file count, or
work type. Add an override only for a user choice or a provider-specific
constraint.

Forward only supported creation flags:

- `--agent <name|task-id=name>`
- `--dry-run`
- `--limit <N>`, `--only <task-id[,id...]>`, `--skip <task-id[,id...]>`
- `--unblocked-only`
- `--team`
- `--base-branch <branch>`, `--branch-prefix <prefix>`, `--no-refresh`
- `--session <tmux-session>`, `--sleep <seconds>`, `--debug`
- `--auto-pr` / `--no-auto-pr`
- `--pr-review-gate` / `--no-pr-review-gate`
- `--briefing-code-review` / `--no-briefing-code-review`
- `--agent-teams-hint` / `--no-agent-teams-hint`
- `--pr-visualization` / `--no-pr-visualization`
- `--dashboard-keybind` / `--no-dashboard-keybind`

When any task has `blocked_by`, add `--unblocked-only` by default. Omit it only
when the user explicitly requests all waves at once.

Do not forward issue/Project-only `--include`, `--name`, `--project-status`,
`--post-dashboard`, `--popup-timeout`, or `--codex-plan-mode`. Treat
user-facing `--go` as a wrapper instruction and strip it before invoking the
CLI.

Use status and lifecycle flags only for an explicit follow-up:
`--status`, `--format json|table`, `--close <task-id>`,
`--merge <task-id>`, or `--cleanup`.

## Preview and run

1. Reuse a saved plan slug directly, or finish writing the temporary spec.
2. Run `fanout plan <spec-or-slug> --dry-run <agent-and-other-flags>`.
3. Summarize the plan slug/title, task ids/titles, dependency waves,
   `blocked_by`, generated worktrees/branches, skipped tasks, and deferred
   tasks.
4. Stop when the user requested a dry-run.
5. Unless the user explicitly passed `--go` or requested immediate execution,
   ask for confirmation.
6. Run the identical command without `--dry-run`.
7. Report the created/skipped/deferred/failed summary.

After a live run, address the copied spec as
`.fanout/plans/<slug>.json` or `fanout plan <slug>`. Use `--only` or `--skip`
for partial reruns.

## Status and lifecycle

- Read state with
  `fanout plan <spec-or-slug> --status [--format table]`. fanout finds PRs by
  recorded branch because plan tasks have no GitHub closed-by graph.
- Fast-forward a task with `--merge <task-id>`.
- Remove one task pane/worktree/state row with `--close <task-id>`.
- Remove recorded tasks whose head PR merged with `--cleanup`.

These modes honor `FANOUT_STATE_PATH` and address task IDs, not issue numbers.
Status and cleanup require `gh`; creation requires tmux. Keep `--team` out of
read and lifecycle modes.

## Coordinate siblings

Use `--team` when requested or accepted for shared files and ordering nuances.
It seeds a best-effort SQLite roster and adds coordination instructions to
each task briefing. Address peers by task ID:

- `fanout msg peers`
- `fanout msg inbox [--mark-read]`
- `fanout msg board`
- `fanout msg send --to <task-id> "<body>"`
- `fanout msg post "<body>"`
- `fanout msg nudge <task-id>`

Treat messages as pull-based. `send` and `post` persist data; `nudge` is a
separate best-effort tmux hint and may safely no-op. Never store secrets in
the plaintext owner-only database under `/tmp`.

## Map failures

- Exit `0`: success, dry-run success, or nothing to do.
- Exit `1`: environment, spec validation, dependency, agent, worktree, launch,
  merge, cleanup, or state failure.
- Exit `2`: bad invocation, unreadable status input, unusable project root, or
  unrecorded lifecycle task.
- Exit `3`: GitHub PR lookup failure during status or cleanup.

For `fanout must be run inside tmux`, start or attach tmux. For
`agent is required`, provide a default or cover every task with an override.
For `unknown plan option`, remove the flag from this lane. Fix spec validation
errors in JSON and rerun the same preview.
