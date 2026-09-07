---
name: fanout
description: Start the fanout persistent TUI console, or spawn one tmux pane per OPEN sub-issue of a GitHub parent issue or Projects v2 board item via the fanout CLI. Use when the user wants fanout's console, or asks to fan out, parallelize, or split issue / Project work across independent git worktrees and agent sessions.
argument-hint: "[parent-issue | project-url | dashboard | plan [path]] [--go] [--wait] [extra fanout flags]"
---

# fanout

`fanout` is a deterministic Go CLI that never calls an LLM. This skill supplies
the judgment (which target, which implicit children, what to name panes) and
the CLI does the enumeration, worktree creation, and pane launch. This file is
the reference for the CLI surface, so there is no need to probe `--help`; invoke
the `fanout` on PATH by its stable name.

## Synopsis

```
fanout                            # start the persistent tmux console
fanout <parent-issue|project-url>
       [--agent <name|NUM=name>] [--limit <N>] [--only <list>] [--skip <list>]
       [--include <list>] [--unblocked-only] [--project-status <name>]
       [--name <NUM>=<slug>[|<display>[|<branch>]]]
       [--base-branch <branch>] [--branch-prefix <prefix>] [--no-refresh]
       [--session <tmux-session>] [--sleep <seconds>]
       [--popup-timeout <seconds>] [--dry-run] [--debug]
       [--auto-pr|--no-auto-pr] [--pr-review-gate|--no-pr-review-gate]
       [--briefing-code-review|--no-briefing-code-review]
       [--agent-teams-hint|--no-agent-teams-hint]
       [--pr-visualization|--no-pr-visualization]
       [--team]
fanout <parent-issue> --status [--format json|table] [--post-dashboard]
fanout <parent-issue> --merge <NUM> | --close <NUM> | --cleanup
fanout dashboard --web              # read-only localhost web dashboard; no parent arg
fanout plan <spec.json|plan-slug>   # issue-less plan lane (fanout-plan skill)
fanout msg <verb> [options] [body...]  # sibling messaging (references/sibling-messaging.md)
fanout --check-update               # read-only version comparison
fanout update                       # replace fanout via install.sh
```

## Modes

The positional argument selects the mode. A bare integer is issue mode; strip a
leading `#` before invoking the CLI. A URL matching
`^https://github\.com/(users|orgs)/<owner>/projects/<num>([/?].*)?$` is project
mode; pass it verbatim (the CLI keeps `/views/<n>` and `?filterQuery=` as-is).
Both modes share everything downstream of child enumeration: briefing
generation, filters, deterministic naming, worktree creation under
`.fanout/worktrees/`, and pane launch with a briefing at
`.fanout/briefings/fanout-<repo>-<N>.md`. The caller's pane is not modified.

No-argument `fanout` starts the persistent console. From a plain shell it
creates or attaches the repository's fanout-managed tmux session; inside tmux it
turns the current pane into the console. The console shows recorded panes with
live tmux and issue/PR status, `n` opens the manual-pane popup for prompt-based
`claude` / `codex` / `opencode` panes, and the label watcher runs inside it when
enabled. Key bindings and TUI details are on the docs site, not here.

`fanout dashboard --web` starts the standalone, 127.0.0.1-bound web dashboard.
It is human-facing: surface it when the user wants to watch or monitor panes;
do not run it as part of the fan-out. The console and a live fan-out also bind
`F12` / `prefix + D` (open it) and `prefix + M` (same-worktree actions) in tmux;
`--no-dashboard-keybind` suppresses those bindings.

`fanout plan` is the issue-less lane. Route plan requests through the
`fanout-plan` skill (`~/.claude/skills/fanout-plan/SKILL.md`), which decomposes
the plan and writes the spec JSON before invoking the CLI.

## Invocation

`/fanout` forwards `$ARGUMENTS`. Resolve them in this order, before any target
resolution:

1. No arguments, or the user asks for the console: run `fanout` with no
   arguments from the target repository worktree and stop. The console needs no
   parent, agent, dry-run, names, or confirmation.
2. First token `dashboard` (ignoring the wrapper flags `--go` / `--wait`):
   forward the dashboard arguments with those two flags stripped (for example
   `fanout dashboard --web --open`) and stop; the dashboard parser rejects
   unknown flags.
3. First token `plan`: hand the remaining arguments to the fanout-plan skill
   with `plan` removed. `--go` stays as that skill's confirmation bypass;
   `--wait` is issue-mode only, so drop it with a note.
4. Version questions: `fanout --check-update` is the read-only check.
   `fanout update` replaces the binary through the repository `install.sh`
   (`--version <tag>` pins a release, `--no-skills` skips integrations; only an
   executable named `fanout` is replaced; exit `0` no-op or updated, `1`
   environment or preflight, `2` bad invocation or incomparable version, `3`
   release lookup failed).
5. Lifecycle flags (`--status`, `--close`, `--merge`, `--cleanup`): resolve the
   target, then run the command directly with no agent, dry-run, or naming.
6. Otherwise this is a pane-creation run. `--go` skips the confirmation and
   `--wait` enables wait-and-continue; both are wrapper flags, never forwarded
   to the CLI. Forward every other flag verbatim to both the dry-run and the
   real run, and add `--agent claude` when neither the user nor `FANOUT_AGENT`
   names an agent (supported: `claude`, `codex`, `opencode`).

Examples: `/fanout 123` (dry-run, confirm, run) · `/fanout 123 --go` ·
`/fanout 123 --limit 3 --agent codex` · `/fanout 123 --agent codex --agent
456=claude` · `/fanout 123 --only 4,7,8,10` · `/fanout 123 --unblocked-only` ·
`/fanout 123 --team` · `/fanout https://github.com/users/<owner>/projects/3`
(Todo column) · `/fanout https://github.com/orgs/acme/projects/12
--project-status "In Progress" --limit 5` · `/fanout plan
/tmp/implementation-plan.md` · `/fanout` (console).

Pane creation is visible and each pane has to be closed by hand, so do not fan
out unprompted because an issue happens to have sub-issues: suggest it and wait
for a yes.

## Pane-creation run

Run from the target repository worktree so `git rev-parse --show-toplevel`
resolves the intended project root; `cd` only when a reliable repo path is
already known from context, otherwise ask the user to invoke `/fanout` from the
right worktree. Batch mode must run inside tmux (the console may start from a
plain shell), and by default it targets the invoking pane with detached splits;
`--session` targets a named session instead. `gh`, `git`, and `tmux 3.3+` are
validated by `fanout` itself with install hints, so rely on its errors.

### 1. Resolve the target

Use any issue ref (`#N` / `N`) or Projects v2 URL in the request or recent
context. If neither is clear, list candidates instead of asking for a pasted
value:

1. `gh issue list --state open --json number,title --limit 100`
2. `gh repo view --json owner -q .owner.login`
3. `gh project list --format json --limit 100` (current user) and
   `gh project list --owner <repo-owner> --format json --limit 100` (repo
   owner, also when the owner is a user); dedupe by URL.
4. If a Project listing fails on auth, scope, or network, say Project
   candidates could not be fully listed, keep the issue candidates, and point
   the user at refreshing `gh` Project access or pasting the URL.
5. Present one combined list (`#<num> <title>`, `<title> (<url>)`) and let the
   user choose. With no candidates, say so and stop, distinguishing "none
   exist" from "Projects could not be listed".

This resolution lives in the skill; the CLI already accepts the resolved
positional argument through `internal/app/cliflags.Parse()`.

### 2. Issue mode only: scan the parent body for implicit children

The CLI treats two things as children: issues from the Sub-issues API and
parent-body rows matching `^\s*-\s+\[[ xX]\] ... #N`. Parents in the wild often
describe children in prose instead, and those reach the CLI only through
`--include`. Skip this step entirely in project mode: Project items are the
source of truth, there is no parent body, and Project descriptions often cite
epic or context issues that are not children.

1. `gh issue view <parent> --json body -q .body`
2. `fanout <parent> --dry-run <forwarded>` once, to learn what the CLI already
   discovers so you do not re-suggest those numbers.
3. Candidates are numbers the body refers to as children: close keywords
   (`Closes #N`, `Fixes #N`, `Resolves #N`, any case; `Closes #1, #2, #3` names
   three), relation wording (`Depends on #N`, `Blocked by #N`, `Related to #N`,
   `See #N`, `Refs #N`), plain bullets (`- #N`, `* #N`, `+ #N`), and Japanese
   idioms (`#N に関連`, `#N を対応`, `#N 対応中`, `#N をブロック`, `#N の子issue`,
   `#N の子タスク`, `#N を修正`, `#N を解決` and near-variants).
4. Exclude `owner/repo#N` cross-repo references (fanout operates on the
   parent's repo only), bare `#N` with no keyword or bullet ("introduced in
   #12" is history), references inside fenced code blocks or blockquotes, the
   parent's own number, and numbers already in the dry-run target list.
5. List remaining candidates with the body line that implied child status and
   ask which to include; with `--go`, print the list and accept them all.
   Forward the accepted numbers as `--include A,B,C` to the confirmation
   dry-run and the real run.

### 3. Project mode only: discover the final targets

Run `fanout <project-url> --dry-run <forwarded>` with all selection flags and
any user-supplied `--name` flags (none generated yet) to learn which items
survived Status, repo, blocker, and limit filtering. This discovery run also
happens under `--go`; it is not the confirmation.

### 4. Name the panes

fanout's default slug is `slugify(title)-<issueNum>`; issue context usually
allows clearer names. For each final target (issue mode: the dry-run target
set; project mode: the discovery output, fetching a body with
`gh issue view <num> --json body -q .body` only when the title is not enough):

- `slug-hint`: 2–4 kebab-case words for the intent (`fix-login-timeout`),
  `[a-z0-9-]` starting with a letter or digit. It becomes the worktree slug
  stem; fanout appends `-<NUM>` when missing, and rerun idempotency comes from
  `.fanout/state.json`, not from the slug.
- `display-name`: 40 characters or fewer, Japanese or English, used as the tmux
  pane title. This is what the user sees when switching panes, so favor
  clarity over brevity.
- `branch-name` (optional): only when the team has a branch convention worth
  enforcing (`feat/issue-<N>-foo`) or `branchPrefix + slug-hint` would collide.

Forward as `--name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]`, one
flag per target; any segment may be empty as long as one is not
(`--name 17=fix-x`, `--name 17=|Fix login timeout`, `--name 17=||release/v2.0`).
Respect user-supplied `--name` flags and fill only the gaps. Choose a per-issue
`--agent NUM=name` only with a clear reason and when the user did not: `claude`
for large refactors and cross-file work, `codex` for focused fixes, tests, and
review follow-up, the default for docs-heavy or ambiguous work, `opencode` when
the user asks for it or wants a different provider. Do not ask the user to
confirm names or agents one by one; they see them in the dry-run summary.

### 5. Dry-run, confirm, run

Run `fanout <target> --dry-run <forwarded>` (with `--include` and `--name`) and
summarize: the mode banner the CLI prints, how many children and their titles,
the briefing paths (preview paths; the live run writes the files), generated
names, worktree paths, and warnings (in project mode, "cross-repo item skipped"
means those items are intentionally excluded). Skip the raw command plan unless
asked. This is the confirmation for the targets. Unless `--go` was passed, wait
for a yes, then run `fanout <target> <forwarded>` and relay the
`created / skipped / deferred (blocked) / deferred (--limit) / failed` summary.
The caller's pane is untouched; keep working on the parent's own scope.

## Flag semantics

- `--only <list>` / `--skip <list>`: comma-separated issue numbers, mutually
  exclusive, applied before `--limit`. `--only` numbers outside the OPEN child
  set are warned and ignored by the CLI; relay that warning rather than
  retrying.
- `--include <list>`: force-add numbers the Sub-issues API and task-list scan
  miss (the channel for step 2). Appended before `--only` / `--skip` filter, so
  `--include 100 --only 4,7,100` works. CLOSED or missing numbers are warned and
  skipped. Rarely needed in project mode, where the board defines the set.
- `--unblocked-only`: defer children whose blockers are still OPEN. Blockers
  come from the child body's `## Blocked by` section, the parent row trailer
  `(blocked by #X, #Y)`, and the `blocked` label as a weak signal; project mode
  has no parent row, so only the other two apply. Prefer it over hand-built
  `--only` wave lists; rerunning the same command walks wave 1 → 2 → … as
  blocker PRs merge.
- `--project-status <name>` (project mode; accepted but unused in issue mode):
  filter items by the `Status` single-select field, case-sensitively. Default
  `Todo`; `all` disables the filter; an empty value is an error; a Project
  without a Status field warns and falls back to all OPEN items.
- `--sleep` (default 4) is the pause between pane creations, not a retry knob.
  `--popup-timeout` (default 20) is deprecated compatibility.
- Briefing settings, all default on, each with a `--no-` form: `--auto-pr` (the
  child must open a PR with `Closes #N`), `--pr-review-gate` (keeps the review
  gate expectation; off adds the Claude-only escape-hatch note),
  `--briefing-code-review` (the Claude-only `/code-review` directive),
  `--agent-teams-hint`, `--pr-visualization` (structured PR body plus gated
  Mermaid guidance). Lifecycle hooks are always on and come from user
  `hooks.json`.
- `--team` (default off): sibling-pane peer messaging. It adds a coordination
  section to each child's briefing and seeds a per-parent peer registry, best
  effort. Codex Plan Mode children get the minimal Plan briefing, and Plan Mode
  disables their team bridge. Suggest it for children that share files or have
  ordering dependencies not encoded as blockers. Verbs, delivery model, and
  security: `~/.claude/skills/fanout/references/sibling-messaging.md`.
- Plan Mode: `childPlanMode` (user config or `FANOUT_CHILD_PLAN_MODE`) governs
  children of every lane and is resolved independently of `--agent`; codex uses
  the app-server Plan Mode controller, claude and opencode their native plan
  modes. `newSessionPlanMode` and `orchestratorPlanMode` are TUI-lane settings
  and do not affect this CLI fan-out. Explicit Claude modes
  (`--permission-mode plan` / `auto`) need Claude Code v2.1.207+; below that
  fanout warns and omits them, and where auto mode is disabled claude falls
  back to `default` and permission prompts show as the TUI's `blocked` state.

## Project mode notes

- Children come from the Project's `items` via GraphQL (`gh api graphql`), all
  pages, in board order. Neither the Sub-issues API nor a parent body is read.
- Projects v2 GraphQL needs the `read:project` scope. On `HTTP 401` /
  `Resource not accessible by integration` against `projectV2`, tell the user
  to run `gh auth refresh -s read:project` and retry; `repo` alone is not
  enough.
- Items whose `content.repository.nameWithOwner` differs from the current repo
  are warned and skipped: briefing and worktree paths assume one repo. Relay
  the warning instead of retrying.
- Idempotency: action mode skips children already recorded in
  `.fanout/state.json` for the same `(parent, issueNum)`, and skips unrecorded
  `.fanout/worktrees/<slug>` directories as a migration fallback when the slug
  matches what this run would create. A child already recorded under another
  parent or Project gets a parent-qualified slug and branch, so the new run has
  its own worktree. The state file is written by atomic temp+rename while
  `.fanout/state.json.lock` is held.

## Label watcher

Use only when the user asks for watcher behavior: repository-wide label
discovery plus one-shot session launch while the console is open. It is not a
scheduler, a webhook, or the #107 known-parent loop (a skill or `/loop` that
keeps revisiting one parent's children and blocker waves).

1. Enable from user config (`~/.config/fanout/config.json` or
   `$XDG_CONFIG_HOME/fanout/config.json`) or with `FANOUT_WATCHER=1`;
   `FANOUT_WATCHER_AGENT` / `watcherAgent` pick the child agent. Repo config
   cannot enable it.
2. Run `fanout` with no arguments and keep the console open; the watcher stops
   with it.
3. Apply `fanout:auto` only to issues the user trusts, children included: their
   bodies become agent briefings, so the label is a prompt-injection boundary.
4. Each cycle swaps `fanout:auto` to `fanout:running`. Issues without OPEN
   children launch as standalone panes under parent `@watch`; issues with OPEN
   children launch as parent fan-outs with `--unblocked-only` under the
   `watcherMaxSessions` budget. Deferred parents are requeued by swapping back
   to `fanout:auto`.
5. `--merge`, `--close`, and `--cleanup` remove `fanout:running` best effort for
   parent fan-outs. Standalone `@watch` panes use the TUI lifecycle keys; the
   CLI parent argument cannot target `@watch` rows. Re-apply `fanout:auto` to
   run a completed item again.

## Optional: wait-and-continue

Only when the user explicitly asks to wait for child PRs and then continue
parent-scope work (`--wait`; issue mode; the live run exited 0).
`fanout --status <PARENT>` reads `.fanout/state.json` (or `FANOUT_STATE_PATH`)
and returns `summary.all_merged` and `summary.blocked`; JSON is the default and
`--format table` is for human review. `--post-dashboard` writes a rollup comment
to GitHub, so use it only on explicit request.

1. Do any parent-scope work that does not need the children's output.
2. When you need it, schedule polling:
   ```
   ScheduleWakeup(prompt: "<<autonomous-loop-dynamic>>", delay_seconds: 300,
                  reason: "polling fanout --status #<PARENT> for all_merged")
   ```
3. On each wake-up run `fanout --status <PARENT>`. `prs: []` on a child means
   pending, never merged.
4. When `summary.all_merged == true`, stop scheduling, then fetch and
   `git merge --ff-only origin/<branch>` the base branch used for the fan-out
   (the forwarded `--base-branch`, else `gh repo view defaultBranchRef`, then
   `origin/HEAD`, then `main`) in the parent worktree, and continue with
   integration and close-out. If the user intervenes, drop the loop.

`--status` exit codes: `2` cannot enumerate children or state (bad invocation,
unreadable state, unusable root; a missing state file is empty, not an error);
`3` `gh` API failed (the user may need `gh auth`); `0` with
`summary.total == 0` means nothing is fanned out under that parent, so report
that instead of looping.

## Failure mapping

On a non-zero exit, point at the README's Troubleshooting section and the
likely fix:

- `fanout must be run inside tmux`: batch mode needs a tmux session; start or
  attach one, or open the console with no-argument `fanout` from a plain shell.
- `agent is required`: pass `--agent claude|codex|opencode`, set `FANOUT_AGENT`,
  or cover every child with `--agent NUM=name`.
- `unknown agent` / `agent "<name>" is not installed`: choose a supported agent
  or install that CLI.
- `prepare worktree`: read the git error; `--no-refresh` bypasses base refresh
  only when a stale base is intentional.
- `sub-issues fetch failed`: `gh auth status`; HTTP 404 means the parent number
  does not exist.
- Project `HTTP 401` / `Resource not accessible by integration`: missing
  `read:project` scope (see above).
- `no sub-issues on #<N>` and `no items in Project (after status/repo filter).
  nothing to do.` are not failures; fanout exits 0.

The CLI is the approved interface: do not wrap or rewrite it, and do not create
worktrees by hand; fanout owns `.fanout/worktrees/<slug>`.
