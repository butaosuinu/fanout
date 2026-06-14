---
name: fanout-plan
description: "Generate and run fanout plan specs from implementation plans. Use when the user invokes `/fanout plan`, asks to fan out a local plan instead of GitHub child issues, or wants an approved plan decomposed into issue-less fanout tasks for `fanout plan`."
---

# fanout-plan

Turn an implementation plan into an issue-less `fanout plan` spec and run it.
The Go CLI does not call an LLM: all decomposition, task naming, and spec JSON
authoring happens in this skill before `fanout plan` deterministically creates
worktrees and panes.

## Plan Discovery

Find the source plan in this order:

1. An explicit path argument from `/fanout plan <path>` or the user's message.
2. The newest `~/.claude/plans/*.md` file by modification time.
3. The current conversation's approved implementation plan.

If more than one candidate is plausible, list the candidates with short labels
and ask the user which one to use. If no plan is available, stop and ask for a
path or an approved plan.

## Decompose

Create tasks that can run in parallel panes:

- Give every task a bounded deliverable and put an acceptance checklist in
  `briefing`.
- Split dependencies into waves. If task B needs task A, set
  `blocked_by: ["task-a"]`; do not rely on task order or `wave` alone.
- Avoid parallel tasks that edit the same files. If two tasks need the same
  file, make one block on the other or combine the work into one bounded task.
- Do not create a catch-all integration task unless there is real integration
  work that cannot stay with the parent/human follow-up.
- Make task titles concrete enough to become pane titles.

Use these naming rules:

- `plan.slug`: lowercase kebab-case, 2-4 words when possible.
- `task.id`: required lowercase kebab-case, 2-4 words, stable across reruns.
- `task.display_name`: optional readable pane title, 40 characters or fewer.
- `task.slug`: optional final worktree slug. Prefer omitting it unless the
  default from title + id is unclear. If set, use the same policy as fanout's
  `--name` slug hints: 2-4 lowercase kebab words, starts with alnum, contains
  only `[a-z0-9-]`, and is unique in the plan.

## Spec JSON

Write the spec to `/tmp/fanout-plan-<plan.slug>.json` with the available file
editing tool. Do not use shell heredocs, `echo >`, or ad-hoc shell redirection
to create the JSON; long briefings and quoting are too fragile.

Schema:

```json
{
  "version": 1,
  "plan": {
    "slug": "launch-plan",
    "title": "Launch plan",
    "source": "path-or-conversation-label",
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

`source`, `base_branch`, `slug`, `display_name`, `branch`, `wave`, and
`blocked_by` are optional except when the dependency graph requires
`blocked_by`. Prefer `plan.base_branch` when the source plan names a base;
otherwise let fanout resolve the repository default branch or let the user pass
`--base-branch`.

## CLI Surface

Run from the target repository worktree. `fanout plan` needs `git` and `tmux`;
`gh` is needed only for `--unblocked-only` blocker completion checks. Use
`--agent claude` unless the user supplied `--agent` or `FANOUT_AGENT`.

Forward only the flags supported by the current `fanout plan` implementation:

- `--agent <name>`
- `--dry-run`
- `--limit <N>`
- `--only <task-id[,id...]>`
- `--skip <task-id[,id...]>`
- `--unblocked-only`
- `--base-branch <branch>`
- `--branch-prefix <prefix>`
- `--no-refresh`
- `--session <tmux-session>`
- `--sleep <seconds>`
- `--debug`
- `--auto-pr` / `--no-auto-pr`
- `--pr-review-gate` / `--no-pr-review-gate`
- `--briefing-code-review` / `--no-briefing-code-review`
- `--agent-teams-hint` / `--no-agent-teams-hint`
- `--pr-visualization` / `--no-pr-visualization`
- `--dashboard-keybind` / `--no-dashboard-keybind`

Use `--dry-run` for preview. Strip `/fanout`'s wrapper-only `--go` before
calling the CLI; it means "skip confirmation", not a fanout flag.

Do not forward issue/project-mode-only flags to `fanout plan`: `--include`,
`--name`, `--project-status`, `--format`, `--post-dashboard`, `--team`,
`--popup-timeout`, `--codex-plan-mode`, `--status`, `--close`, `--merge`, or
`--cleanup`. Names belong in the spec (`slug`, `display_name`, `branch`), and
dependencies belong in `blocked_by`.

## Run

1. Write `/tmp/fanout-plan-<slug>.json`.
2. Run:
   ```bash
   fanout plan /tmp/fanout-plan-<slug>.json --dry-run --agent claude <flags>
   ```
3. Summarize the dry-run: plan slug/title, task count, task ids/titles,
   waves, `blocked_by`, generated worktree paths, branch names, and deferred
   rows.
4. Ask for confirmation unless the user passed `--go` or explicitly requested
   immediate execution. If the user passed `--dry-run`, stop after the preview.
5. Run the live command without `--dry-run`:
   ```bash
   fanout plan /tmp/fanout-plan-<slug>.json --agent claude <flags>
   ```
6. Return the created/skipped/deferred/failed summary.

After a live run, fanout copies the spec to `.fanout/plans/<slug>.json`; later
runs can re-address it as `fanout plan <slug> ...`. Use `--only <task-id>` or
`--skip <task-id>` for partial reruns. In the current plan surface, task
lifecycle flags are not implemented on `fanout plan`; for read-only visibility
use the no-argument `fanout` TUI or dashboard, and do not invent
`fanout plan --status` / `--cleanup` unless the CLI has been updated.

## Failure Mapping

- Exit 0: success, dry-run success, or nothing to do.
- Exit 1: environment/preflight, unreadable or invalid spec JSON, invalid
  filter values, missing dependencies, agent/runtime setup, worktree creation,
  or failed pane launch.
- Exit 2: bad invocation such as missing spec, unknown plan option, or extra
  positional arguments.

For common runtime errors: `fanout must be run inside tmux` means batch pane
creation needs tmux; `agent is required` means pass `--agent claude` or another
supported agent; `unknown plan option` means the flag belongs to another
fanout mode; spec validation errors should be fixed in the JSON and retried.
