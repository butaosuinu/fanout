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

1. An explicit argument from `/fanout plan <arg>` or the user's message. If
   `<arg>` is a file path, use that file. Otherwise, check whether
   `.fanout/plans/<arg>.json` exists in the target repository; if it does, use
   `<arg>` as the saved plan slug and skip spec authoring. If the explicit
   argument resolves to neither a file nor a saved plan slug, stop and report
   the missing path/slug instead of rediscovering another source plan. The
   file may be a finished implementation plan or a raw request prompt — the
   fanout TUI's plan fan-out checkbox writes the prompt to a file verbatim.
   Decompose either the same way; ask a clarifying question only when the
   request is too vague to split into tasks. The file may also be an
   issue-sourced coordinator briefing written by the TUI's issue mode: it
   carries the issue number, title, and body plus a "Fan-out instructions"
   section that names the default `--agent` for tasks. Follow those
   instructions — set `plan.source` to reference the issue, never invent
   GitHub issue numbers, and honor its Refs #N / Closes #N discipline (task
   PRs say "Refs #N", never "Closes #N"; after the live fan-out, comment on
   the issue with the plan slug and task list).
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
- `task.slug`: optional final worktree slug. Prefer omitting it; default slugs
  are plan-qualified. If set, fanout uses it exactly, so it must be globally
  unique under `.fanout/worktrees`, not merely unique within this plan. Use the
  same policy as fanout's `--name` slug hints: 2-4 lowercase kebab words,
  starts with alnum, and contains only `[a-z0-9-]`. Prefix it with `plan.slug`
  when that helps avoid collisions, for example `launch-plan-base-types`.

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
    "source": "path-or-conversation-label"
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
otherwise let fanout resolve the repository default branch, use the current
local branch in repos without `origin`, or let the user pass `--base-branch`.
Do not add an agent field to the JSON schema; per-task agent assignment belongs
only in repeatable CLI flags such as `--agent base-types=codex`.

## CLI Surface

Run from the target repository worktree. Task creation and dry-run modes need
`git` and `tmux 3.3+`; `gh` is optional for `--unblocked-only` blocker completion
checks, and unavailable PR lookups are treated as incomplete dependencies.
Read/lifecycle action modes need `git` but not tmux; `--status` and `--cleanup`
also need `gh`. Use `--agent claude` unless the user
supplied `--agent` or `FANOUT_AGENT`; repeat `--agent task-id=name` for
per-task overrides.

Forward only the flags supported by the current `fanout plan` implementation:

- `--agent <name|task-id=name>`
- `--dry-run`
- `--limit <N>`
- `--only <task-id[,id...]>`
- `--skip <task-id[,id...]>`
- `--unblocked-only`
- `--team`
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

When task context clearly favors a different supported agent, merge explicit
per-task overrides into the command with `--agent <task-id>=<name>`. Supported
agents in this build are `claude` and `codex`; do not emit `gemini`. Use
`claude` for broad refactors or large cross-file work, `codex` for focused
edits, bug fixes, tests, and review follow-up, and fall back to the global
default for docs-heavy or ambiguous tasks.

If the spec contains any non-empty `blocked_by` lists, include
`--unblocked-only` by default so dependent tasks are deferred until their
blockers are complete. Omit it only when the user explicitly asks to launch all
waves together.

Do not forward issue/project-mode-only flags to `fanout plan`: `--include`,
`--name`, `--project-status`, `--post-dashboard`, `--popup-timeout`, or
`--codex-plan-mode`. Names belong in the spec (`slug`, `display_name`,
`branch`), and dependencies belong in `blocked_by`.

`--team` is supported in plan mode (see "Sibling coordination" below); it is
not one of the forbidden issue/project-only flags.

Lifecycle hooks are always on and come from user `hooks.json`.

Use read/lifecycle flags only when the user explicitly asks for plan task
status or cleanup, not during initial plan generation:

- `--status`
- `--format <json|table>` (only with `--status`)
- `--close <task-id>`
- `--merge <task-id>`
- `--cleanup`

## Sibling coordination (--team / fanout msg)

`fanout plan --team` opts the plan run into sibling-pane peer messaging, the
same SQLite message bus the issue/project lanes use — it just addresses peers
by **task id** instead of issue number, because plan tasks have no GitHub
issue. It adds a "Coordinating with your sibling panes" section to each task's
briefing and seeds the created task panes into a per-parent peer registry
(best-effort; a registry failure never fails the fan-out). `--team` is
incompatible with the read/lifecycle modes (`--status` / `--close` / `--merge`
/ `--cleanup`).

Suggest it when tasks touch shared files (configs, schemas, lockfiles) or have
ordering nuances beyond what `blocked_by` already encodes; skip it for fully
independent tasks. From inside a task pane, `fanout msg` auto-detects which
task you are (from the tmux pane and `.fanout/state.json`) and which plan you
belong to. Peers are addressed by task id:

- `fanout msg peers` — live sibling roster (task ids).
- `fanout msg inbox [--mark-read]` — unread 1:1 + board messages addressed to you.
- `fanout msg board` — the shared broadcast board.
- `fanout msg send --to <task-id> "<body>"` — 1:1 message to a sibling task.
- `fanout msg post "<body>"` — post to the shared board.

Coordination is pull-based: messages persist and a sibling reads them at its
own checkpoints; nothing nudges a busy pane. The DB is a plaintext SQLite file
under `/tmp` (`0600`, owner-only) — never put secrets in messages. This is
distinct from Claude Code Agent Teams (a Claude-only, single-session feature).

## Run

1. If using a saved plan slug from `.fanout/plans/<slug>.json`, keep that slug
   as the command argument and skip writing a new spec. Otherwise, write
   `/tmp/fanout-plan-<slug>.json`.
2. Run:
   ```bash
   fanout plan <spec-or-slug> --dry-run --agent claude [--agent api-client=codex] <flags>
   ```
3. Summarize the dry-run: plan slug/title, task count, task ids/titles,
   waves, `blocked_by`, generated worktree paths, branch names, and deferred
   rows.
4. Ask for confirmation unless the user passed `--go` or explicitly requested
   immediate execution. If the user passed `--dry-run`, stop after the preview.
5. Run the live command without `--dry-run`:
   ```bash
   fanout plan <spec-or-slug> --agent claude [--agent api-client=codex] <flags>
   ```
6. Return the created/skipped/deferred/failed summary.

After a live run, fanout copies the spec to `.fanout/plans/<slug>.json`; later
runs can re-address it as `fanout plan <slug> ...`. Use `--only <task-id>` or
`--skip <task-id>` for partial reruns. For read-only visibility, use
`fanout plan <slug> --status [--format table]`; for task lifecycle, use
`fanout plan <slug> --merge <task-id>`, `--close <task-id>`, or `--cleanup`.
These task modes address task IDs, not issue numbers.

## Status and Lifecycle

`fanout plan <spec-or-slug> --status` loads the spec and state, then looks up
PRs by branch with `gh pr list --head <branch>` because issue-less tasks have
no GitHub issue closed-by graph. JSON is the default output; `--format table`
adds PR state, CI, type, changed-file count, diff bars, and links.

`--merge <task-id>` fast-forwards the recorded task branch into the project
checkout. `--close <task-id>` removes the recorded task worktree, pane, and
state row. `--cleanup` removes recorded plan task panes whose head branch has a
merged PR. These modes honor `FANOUT_STATE_PATH`.

## Failure Mapping

- Exit 0: success, dry-run success, or nothing to do.
- Exit 1: environment/preflight, unreadable or invalid spec JSON, invalid
  filter values, missing dependencies, agent/runtime setup, worktree creation,
  failed pane launch, git merge failure, worktree cleanup failure, or state
  update failure.
- Exit 2: bad invocation such as missing spec, unknown plan option, or extra
  positional arguments. For `--status`, missing `git`/`gh` preflight
  dependencies return 1, while unreadable spec/state and unusable project root
  return 2; for task lifecycle, an unrecorded task ID returns 2.
- Exit 3: GitHub PR lookup failure in plan `--status` or `--cleanup`.

For common runtime errors: `fanout must be run inside tmux` means batch pane
creation needs tmux; `agent is required` means pass `--agent claude`, set
`FANOUT_AGENT`, or cover every selected task with `--agent task-id=name`;
`unknown plan option` means the flag belongs to another fanout mode; spec
validation errors should be fixed in the JSON and retried.
