---
name: fanout
description: Use the fanout CLI from Codex CLI to start the persistent TUI console, or spawn one tmux pane per OPEN sub-issue of a GitHub parent issue / GitHub Projects v2 board item. Use when the user wants fanout to manage parallel child work from its TUI console, or asks to fan out, parallelize, or split child issues / project items across independent git worktrees and agent sessions.
metadata:
  short-description: Open the fanout TUI or fan out GitHub child work
---

# fanout

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
       [--codex-plan-mode|--no-codex-plan-mode]
       [--pr-visualization|--no-pr-visualization]
       [--team]
fanout <parent-issue> --status [--format json|table] [--post-dashboard]
                                      # status of fanned children; optionally post dashboard
fanout <parent-issue> --merge <NUM> # fast-forward merge a recorded child branch
fanout <parent-issue> --close <NUM> # remove a recorded child worktree/pane
fanout <parent-issue> --cleanup     # remove merged/closed recorded children
fanout dashboard --web              # read-only localhost web dashboard (Session view); no parent arg
fanout plan <spec.json|plan-slug>   # issue-less local plan task fan-out (see fanout-plan)
fanout msg <verb> [options] [body...]  # peer messaging between sibling panes (see Notes)
fanout --check-update               # Read-only version comparison
fanout update                       # Replace fanout via install.sh
```

`fanout dashboard --web` is a standalone subcommand (no parent argument): a read-only, 127.0.0.1-bound web dashboard that visualizes all fanned-out Sessions live (pane liveness, issue/PR state). It is human-facing — surface it when the user wants to watch/monitor parallel panes, not as part of the fan-out flow. After a live fan-out, fanout also binds `prefix + D` in tmux to open it; launched panes record their owner project root so the key still opens the right dashboard from agent TUIs such as Codex when tmux reports a stale `pane_current_path`. `--no-dashboard-keybind` suppresses the binding.

`fanout plan <spec.json|plan-slug>` is the issue-less plan-task lane. If the
user asks to fan out an implementation plan, use the `fanout-plan` skill
(`~/.codex/skills/fanout-plan/SKILL.md`) so Codex decomposes the plan and
writes the spec JSON before invoking the deterministic CLI. The CLI does not
call an LLM and does not infer tasks from prose.

**Do not probe the CLI** with `fanout --help`, `fanout -h`, or
`which fanout`. This SKILL.md is the source-of-truth for the CLI surface —
every flag above is documented in the sections below, and the binary path
is normally `~/.local/bin/fanout` (see the next paragraph). Calling `--help`
or `which` just to "verify" the surface wastes a tool call and adds nothing.

`fanout <parent-issue-or-project-url>` enumerates either a GitHub parent
issue's OPEN sub-issues *or* a GitHub Projects v2 board's OPEN items, and
creates one new tmux pane per child. Each pane gets its own git worktree under
`.fanout/worktrees/` and an agent CLI prompt that points at
`/tmp/fanout-<repo>-<N>.md`. The caller's pane is not modified.

`fanout` with no arguments starts the persistent fanout TUI console. From a
plain shell it creates or attaches a deterministic fanout-managed tmux session
for the current repository, then runs the console there. From inside tmux it
turns the current pane into the console. The console shows `.fanout/state.json`
panes with live tmux plus issue/PR status, a `total` / `merged` / `pending` /
`blocked` header rollup, lets the user press `n` to launch a manual
prompt-based `claude` / `codex` pane, and exits on `q` without killing the
session or child panes.
On a selected recorded pane, `c` closes it, `m` fast-forward merges its recorded
branch, and `x` cleans up merged/closed siblings for the same parent after
confirmation.
The TUI also compares consecutive GitHub snapshots and notifies once per
transition when a child becomes merged, CI turns failing, or a child becomes
waiting on an open blocker; channels are configured through fanout settings.

The positional argument selects the mode: a bare integer means **issue mode**;
a URL of the form
`https://github.com/(users|orgs)/<owner>/projects/<num>` means **project
mode**. Both modes share everything downstream of child enumeration
(briefing generation, filters, deterministic naming, direct git worktree
creation, and tmux pane launch); only the source of children differs.
User-facing issue refs like `#N` are accepted by this skill, but strip the
leading `#` before invoking the CLI.

The CLI is normally installed at `~/.local/bin/fanout`; source and docs are in
this repository. Codex discovers this skill from `~/.codex/skills/fanout`.
Always invoke the stable `fanout` command name.

## When To Use

Use this skill when the user asks to start the fanout console / TUI, or when
the user explicitly asks to fan out, parallelize, or split work for a GitHub
parent issue or a GitHub Projects v2 board, including Japanese phrasing like
`並列展開` or "プロジェクトの Todo 列を一気に着手".
Also use it when the user asks whether the installed `fanout` binary is up to
date; that path uses `fanout --check-update` instead of pane creation. If the
user asks to update fanout itself, run `fanout update` immediately.
If the user asks to start the fanout console / TUI, run `fanout` with no
arguments directly from the target repository worktree; skip parent resolution,
dry-run, pane naming, and agent selection.
If the user asks to fan out an implementation plan, run `$fanout-plan` instead
of the issue/Project workflow below.
Do not invoke fanout just because an issue has sub-issues; pane creation is
visible and the user has to close unwanted panes manually.

Codex does not need a custom slash command for this integration. If the user
asks for `$fanout`, "fan out #123", "fan out this project URL", or similar,
use this workflow directly.

## Pre-Flight

1. Prerequisites are `gh`, `git`, and `tmux`. The CLI validates these on
   startup, so rely on its error output.
2. Choose the launch lane. TUI mode is `fanout` with no arguments; it can start
   from a plain shell because it creates or attaches the repository's
   fanout-managed tmux session, and from inside tmux it uses the current pane.
   Batch pane-creation mode is `fanout <parent-issue|project-url>` and must run
   inside tmux. If batch mode reports `fanout must be run inside tmux`, tell
   the user to start or attach a tmux session first.
3. An agent name is required for pane creation. Pass `--agent <name>` or set
   `FANOUT_AGENT`; repeat `--agent NUM=name` to override one child issue.
   Supported agents are `claude` and `codex`.
4. `--codex-plan-mode` is valid only when every selected child resolves to
   `codex` after per-issue overrides. It uses Codex app-server to create the
   child Plan Mode thread, start the initial Plan turn with the fanout prompt,
   then attach the interactive Codex TUI to that remote session.

## Workflow

If the user's intent is only to check the installed `fanout` binary version, run
`fanout --check-update` and skip the rest of this workflow. It is read-only,
creates no panes, and does not require pane-creation pre-flight, parent
resolution, dry-run, pane naming, or confirmation.

If the user's intent is to update the `fanout` binary itself, run
`fanout update` immediately. It downloads and runs the repository `install.sh`,
passing `BIN_DIR=<current binary dir>` and `FANOUT_VERSION=<target>` so the
installer replaces the same `fanout` command and refreshes bundled
integrations. Use `--version <tag>` to pin a release and `--no-skills` to skip
Claude/Codex skill installation. Actual replacement is only supported when the
resolved executable basename is `fanout`. Exit codes: `0` no-op/update, `1`
environment or preflight failure, `2` bad invocation or incomparable version,
`3` latest-release lookup failed.

If the user's intent is to start the persistent TUI console, run `fanout` with
no arguments from the target repository worktree and skip the rest of this
workflow. TUI mode does not need a parent issue, Project URL, `--agent`,
dry-run, generated pane names, or confirmation.

1. Resolve the parent target from the user's request or recent context. Two
   shapes are accepted; identify which one matches and pass the normalized
   form to the CLI as the positional argument:
   - **Issue mode** — the user may type a bare integer (`42`) or an
     issue ref (`#42`). The CLI only accepts bare digits, so **strip the
     leading `#`** before invoking `fanout`.
   - **Project mode** — Projects v2 URL matching
     `^https://github\.com/(users|orgs)/[^/]+/projects/\d+([/?].*)?$`.
     Pass the URL verbatim, including any trailing `/views/<n>` segment
     or `?filterQuery=...` query string — the CLI extracts what it needs
     and ignores the rest.

   If neither is clear, actively list candidates from the current repo/worktree
   instead of asking for a pasted number/URL:
   1. Run `gh issue list --state open --json number,title --limit 100`.
   2. Get the repo owner login with
      `gh repo view --json owner -q .owner.login`.
   3. Run Project listing commands with `--limit 100`:
      `gh project list --format json --limit 100` for the current user's
      Projects, and
      `gh project list --owner <repo-owner> --format json --limit 100` for
      the repo owner's Projects. Run the repo-owner command even when the
      owner is a user, not only for orgs. Dedupe Projects by URL if the two
      lists overlap.
   4. If a Project listing command fails due auth/scope/network, warn that
      Project candidates could not be fully listed, keep any issue candidates,
      and continue. If the user needs a Project candidate, tell them to refresh
      `gh` Project access or paste the Project URL.
   5. Present one combined list: issues as `#<num> <title>`, Projects as
      `<title> (<url>)`, then ask the user to choose one.
   6. If no issue candidates and no Project candidates are available, tell the
      user there is no OPEN issue or Project target to fan out and stop; if
      Project listing failed, mention that Project candidates were unavailable
      rather than claiming none exist.
   7. Resolve the selection to the CLI positional arg: issues become bare
      digits with any leading `#` removed; Projects become the Project URL
      from `gh project list`.

   This is skill-side target resolution for non-TTY agent entrypoints. Do not
   change the Go `fanout` CLI for it; the CLI already accepts the resolved
   positional arg via `internal/cliflags.Parse()`.
2. Forward user-supplied fanout flags verbatim:
   `--agent` (including repeatable `NUM=name` overrides), `--limit`, `--only`, `--skip`, `--include`,
   `--unblocked-only`, `--project-status` (project mode only), `--format`,
   `--post-dashboard`, `--name`, `--base-branch`, `--branch-prefix`,
   `--no-refresh`, `--session`, `--sleep`, `--popup-timeout`, `--debug`, `--auto-pr`,
   `--no-auto-pr`, `--pr-review-gate`, `--no-pr-review-gate`,
   `--briefing-code-review`, `--no-briefing-code-review`,
   `--agent-teams-hint`, `--no-agent-teams-hint`,
   `--codex-plan-mode`, `--no-codex-plan-mode`, `--pr-visualization`,
   `--no-pr-visualization`, and `--team`.
   If neither the user nor the environment supplies an agent, add
   `--agent codex` because the direct tmux runtime requires an explicit
   agent name.
3. If the user asked to skip confirmation (`--go`, "go ahead", "run it now"),
   strip `--go` before calling the CLI and run the real command after the
   pre-flight name/include preparation. Here `--go` means "go ahead now"; it
   does not select the Go implementation. Otherwise dry-run first and ask for
   confirmation before the real run.
4. **Issue mode only — skip in project mode.** Scan the parent body for
   implicit children that the CLI does not parse: fetch it with
   `gh issue view <parent> --json body -q .body`, and compare against
   `fanout <parent> --dry-run <flags>` target output. In project mode the
   Project items are the source-of-truth and there is no parent body to scan.
5. **Project mode only:** discover final targets before naming by running
   `fanout <project-url> --dry-run <flags>` from the target repository worktree
   with all selection flags and any user-supplied `--name` flags, but without
   newly generated `--name` flags. Use that output to learn which Project items
   survived Status / repo / blocker / limit filtering. This discovery dry-run
   still runs when the user asked to skip confirmation; it is not the
   confirmation step.
6. For each final target issue, generate a pane name unless the user already
   supplied `--name` for that number. Forward one repeatable
   `--name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]` flag per
   target. The 3rd segment (branch-name) is optional and
   should be filled in only when the team has a branch-naming convention
   worth enforcing (e.g. `feat/issue-<N>-foo`, `release/v2.0`); otherwise
   leave it empty so fanout's default `branchPrefix + slug` applies. fanout
   appends `-<NUM>` to slug hints that do not already have that suffix; rerun
   idempotency comes from `.fanout/state.json`. In issue mode use the
   parent issue context and issue dry-run target set. In project mode use the
   discovery dry-run output from step 5; fetch per-issue body via
   `gh issue view <num> --json body -q .body` only if the title alone is not
   enough to name the pane.
   Also choose a per-issue agent only when there is a clear reason and the
   user did not already provide `--agent NUM=name`: large refactors normally
   use `claude`; focused bug fixes and review follow-up normally use `codex`;
   docs-heavy work should stay on the default agent because Gemini is not
   supported in this build. Forward choices as repeatable `--agent NUM=name`
   and summarize them in the dry-run.
7. Dry-run with `fanout <target> --dry-run <flags>`, summarize the mode
   banner (issue / project), targets, briefing paths, generated names,
   skipped/deferred rows, and warnings (including "cross-repo item skipped"
   in project mode). Summarize the command plan; do not paste every raw
   command unless the user asks for debug detail.
8. After confirmation, run `fanout <target> <flags>` and relay the
   created/skipped/deferred/failed summary.

## Optional: Wait-and-Continue

Use this only when the user explicitly asks to wait until child PRs merge and
then continue parent-scope work. After the real fanout run succeeds, poll
`fanout --status <PARENT>` from the parent worktree. The command reads
`.fanout/state.json` (or `FANOUT_STATE_PATH`) and returns
`summary.all_merged` plus `summary.blocked` for the recorded children. Use the
default JSON format for automation; `--format table` is for human review of PR
state, CI, diff stats, and links.
Use `--post-dashboard` only when the user explicitly wants a parent issue
rollup comment; it writes to GitHub even though it is attached to `--status`.

1. Continue any parent-scope work that does not depend on the children's merged output.
2. Periodically rerun `fanout --status <PARENT>`. Inspect `summary.all_merged`.
3. When `summary.all_merged == true`, refresh and merge the same base branch
   used for the fanout run in the parent worktree. Use the forwarded
   `--base-branch` when present; otherwise resolve fanout's default branch
   (`gh repo view defaultBranchRef`, then `origin/HEAD`, then `main`). Fetch the
   normalized remote branch and run `git merge --ff-only origin/<branch>` (or the
   equivalent `refs/remotes/origin/<branch>`), then proceed with integration
   tests and parent-issue close-out.
4. Treat `prs: []` on a child as pending (PR not yet open), never merged.

`--status` exit codes:

- `2` — cannot enumerate children or state (bad invocation, unreadable or malformed state, unusable project root). A missing state file is treated as empty.
  Stop and report.
- `3` — `gh` API call failed. Stop and report; the user may need to refresh
  `gh auth`.
- `0` with `summary.total == 0` — nothing has been fanned out under that parent
  (or every fanned pane was torn down). Tell the user; don't keep polling.

`--status` is read-only unless `--post-dashboard` is explicitly set, and is
exclusive with all action-bearing flags
(`--agent`, `--limit`, `--only`, `--skip`, `--include`, `--name`,
`--base-branch`, `--branch-prefix`, `--no-refresh`, `--session`, `--sleep`,
`--popup-timeout`, `--dry-run`, `--unblocked-only`, `--close`, `--merge`,
`--cleanup`, `--auto-pr`, `--no-auto-pr`, `--pr-review-gate`,
`--no-pr-review-gate`, `--briefing-code-review`, `--no-briefing-code-review`,
`--agent-teams-hint`, `--no-agent-teams-hint`, `--codex-plan-mode`,
`--no-codex-plan-mode`, `--pr-visualization`, `--no-pr-visualization`). Set
`FANOUT_STATE_PATH` to
read a specific state file outside the repository checkout.

## Implicit Child Scan

**Issue mode only — skip this section entirely when the positional argument
is a Project URL.** Project items are the source-of-truth in project mode and
the Project has no parent body; running this scan there would push unrelated
context issues into `--include`.

In issue mode, fanout itself only detects children from the Sub-issues API
and parent-body task-list rows shaped like `- [ ] #N`. During pre-flight,
identify child-like references that should be forwarded via `--include`.

Include candidates with strong child signals:

- Close/fix/resolve keywords: `Closes #N`, `Fixes #N`, `Resolves #N`.
- Dependency or relation wording: `Depends on #N`, `Blocked by #N`,
  `Related to #N`, `See #N`, `Refs #N`.
- Plain bullets: `- #N`, `* #N`, `+ #N`.
- Japanese wording such as `#N に関連`, `#N を対応`, `#N 対応中`,
  `#N をブロック`, `#N の子issue`, `#N の子タスク`, `#N を修正`,
  or `#N を解決`.

Exclude cross-repo refs (`owner/repo#N`), bare historical references with no
child signal, references inside fenced code blocks or blockquotes, the parent
issue itself, and numbers already present in the dry-run target list.

If candidates remain, list each with a one-line reason and ask which to include
unless the user explicitly requested a no-confirmation run. Pass accepted
numbers as `--include A,B,C` to both dry-run and execution.

## Project Mode

When the positional argument is a Projects v2 URL, fanout enumerates the
Project's items via GraphQL (`gh api graphql`) instead of the Sub-issues
API + parent body. Key points:

- **URL shape** — the CLI matches
  `^https://github\.com/(users|orgs)/<owner>/projects/<num>([/?].*)?$`.
  User-owned and organization-owned boards are both accepted, and any
  trailing `/views/<n>` segment or `?filterQuery=...` query string is
  preserved verbatim (the CLI extracts only `users|orgs`, `<owner>`,
  `<num>`). Anything else is rejected at arg-parse time.
- **`--project-status` filter** — Project items have a single-select
  `Status` field. Default is `--project-status Todo` (so `fanout <url>`
  fans out only the Todo column). Pass `--project-status all` to disable
  the filter and include every OPEN item, or any single Status value
  (e.g. `--project-status "In Progress"`) for that column. The match is
  case-sensitive against the Project's option labels. If the Project has
  no `Status` field, fanout warns and falls back to all OPEN items.
  Empty values are rejected (`--project-status ""` errors). Accepted but
  unused in issue mode.
- **Briefing settings flags** — `--auto-pr` / `--no-auto-pr` include or
  omit the child briefing requirement to open a PR with `Closes #N`;
  `--pr-review-gate` / `--no-pr-review-gate` keep the default PR review-gate
  expectation or add a Claude-only escape-hatch note when the hook blocks
  before `/post-work-review`; `--briefing-code-review` /
  `--no-briefing-code-review` include or omit the Claude-only `/code-review`
  directive; `--agent-teams-hint` / `--no-agent-teams-hint` include or omit
  the Claude-only Agent Teams hint; `--pr-visualization` /
  `--no-pr-visualization` include or omit structured PR-body plus gated Mermaid
  guidance in auto-PR child briefings. Defaults are all on, and these settings
  are Go-implementation only.
- `--codex-plan-mode` / `--no-codex-plan-mode` apply only when every selected
  child resolves to `codex`.
  When enabled, fanout starts a Codex app-server, creates the child Plan Mode
  thread, starts the initial Plan turn with the child prompt through app-server,
  and attaches the interactive Codex TUI to that remote session. fanout does not
  send `/plan` or prompt text through tmux. If Plan turn setup or TUI attach
  fails, fanout fails that launch before recording state and cleans up the
  pane/worktree so the child can be retried.
- **`gh` scope** — Projects v2 GraphQL needs `read:project` on top of `repo`.
  If fanout reports an authorization failure on `projectV2`
  (`HTTP 401` / `Resource not accessible by integration`), instruct the
  user to run `gh auth refresh -s read:project` and rerun.
- **Cross-repo items are skipped** — items whose
  `content.repository.nameWithOwner` does not match the current git repository
  are warned and skipped. Briefings and worktrees assume a single repo;
  cross-repo items would land in the wrong checkout. Surface the warning to
  the user rather than retrying.
- **`--include` is allowed but rarely needed** — the Project already
  defines the set. Use it only when the user wants to force-add an issue
  that isn't on the board.
- **`--unblocked-only`** still applies. In project mode the parent-row
  trailer source is unavailable, so blockers come only from the child body's
  `## Blocked by` section and the `blocked` label.
- **Idempotency** — action mode skips children already recorded in
  `.fanout/state.json` for the same `(parent, issueNum)` pair, and also skips
  unrecorded existing `.fanout/worktrees/<slug>` directories as a migration
  fallback. If the same issue is recorded for another parent, only an existing
  worktree matching the slug this current run would create is treated as
  fallback. The state file is written with an atomic temp+rename update while a
  `.fanout/state.json.lock` file is held for the run. If the same child issue
  is already recorded for another parent or Project, fanout parent-qualifies
  the default slug/branch so the new run gets a separate worktree.

## Pane Names

fanout has a deterministic default slug (`slugify(title)-<issueNum>`), but
Codex often has enough issue context to choose a clearer slug/display name.
Generate names in conversation and pass them to fanout.

For each target issue:

- `slug-hint`: 2-4 lowercase kebab-case words, starting with an alnum and
  containing only `[a-z0-9-]`, such as `fix-login-timeout`. Controls the
  worktree slug stem; fanout appends `-<issue-number>` when missing.
- `display-name`: readable pane title, Japanese or English OK, ideally
  40 characters or fewer.
- `branch-name` *(optional)*: exact git branch name to create. Use this only
  when the team has a branch-naming convention worth enforcing
  (e.g. `feat/issue-<N>-foo`, `release/v2.0`); otherwise leave it empty and
  fanout will use `branchPrefix + slug`.

Forward as `--name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]`.
Any segment may be empty as long as at least one is non-empty
(`--name 17=fix-x` slug only, `--name 17=|Disp` display only,
`--name 17=||feat/x` branch only). If the user supplied a name for a
number, respect it and fill only missing segments.

## Failure Mapping

When fanout exits non-zero, use the README troubleshooting section and surface
the likely next action:

- `fanout must be run inside tmux`: batch pane creation needs a tmux session;
  start or attach one and rerun, or start the persistent console with
  no-argument `fanout` from a plain shell.
- `agent is required`: pass `--agent claude`, `--agent codex`, set
  `FANOUT_AGENT`, or cover every selected child with `--agent NUM=name`.
- `unknown agent`: use one of the supported agents (`claude`, `codex`).
- `agent "<name>" is not installed`: install that CLI or choose another agent.
- `prepare worktree`: inspect the git error; `--no-refresh` can bypass base
  branch refresh only when the stale base is intentional.
- `sub-issues fetch failed`: check `gh auth status`; an HTTP 404 means the
  parent issue number does not exist.
- `no sub-issues on #<N>` is not a failure; fanout exits 0.
- Project mode `HTTP 401` / `Resource not accessible by integration`
  against `projectV2`: the user's `gh` token lacks the `read:project` scope.
  Tell them to run `gh auth refresh -s read:project` and rerun.
- Project mode `no items in Project (after status/repo filter). nothing to
  do.` is not a failure; fanout exits 0.

## Notes

- Action-mode reruns skip children already recorded in `.fanout/state.json`
  for the same `(parent, issueNum)`. `--status` reads the same state store.
- `--unblocked-only` defers children whose blockers are still OPEN and is
  preferred over hand-built wave lists when blocker annotations exist.
- Default project-mode filter is `--project-status Todo`. Use
  `--project-status all` for a full sweep of the board's OPEN items.
- Use repeatable `--agent NUM=name` for per-child overrides; do not emit
  `gemini` because this build supports only `claude` and `codex`.
- When a created pane runs `codex`, the per-issue briefing requires the agent
  to run `codex review --uncommitted` after implementation/tests and repeat
  review -> fix -> retest -> review until no findings remain before it commits,
  pushes, or opens the PR. The review command should be treated as one
  blocking shell command: while it is running, do not open, resume, or inspect
  any Review Session and do not run `/codex:status` or other polling commands;
  wait for the command to exit, then read the final output once.
- The action path creates git worktrees itself, then uses detached
  `tmux split-window -t <invoking-pane> -d` with a shell launch command to start
  the selected agent CLI without moving focus away from the caller pane. The
  command runs through a POSIX wrapper and returns to the user's shell after the
  agent exits. `--session` is the explicit escape hatch for targeting a
  different session.
- `--team` (forwarded like any other flag, default off) opts the run into
  sibling-pane peer messaging: it adds a "Coordinating with your sibling panes"
  section to each child's standard briefing and seeds the created panes into a
  per-parent peer registry (best-effort — registry failures never fail the
  fan-out). Codex Plan Mode children (`--codex-plan-mode`) get the minimal Plan
  briefing, so the section is skipped for them — they are still seeded and can
  run `fanout msg`. Suggest it when children touch shared files or have ordering
  dependencies.

## Sibling coordination (--team / fanout msg)

Fanned panes are separate agent sessions that coordinate through a per-parent
SQLite message bus. This is unrelated to Claude Code Agent Teams (Claude-only,
single-session) and works the same for `codex` and `claude` panes.

- Enable with `--team` on the fan-out. Inside a fanned pane, `fanout msg`
  auto-detects which child you are (from the tmux pane + `.fanout/state.json`)
  and which parent you belong to.
- Verbs: `peers` (live roster), `inbox [--all] [--mark-read]` (unread 1:1 +
  board), `board [--all]`, `send --to <N> [--kind K] "<body>"`,
  `post [--kind K] "<body>"`, `mark-read [--id N ...|--all]`, `register`.
- Common options: `--json`, `--self <N>` / `--parent <ref>` (override
  detection), `--dry-run` (write verbs only; prints `# would ...`, not
  combinable with `--json`). `kind` is a free-form label (default `note`).
  Exit codes: `0` ok, `2` bad invocation, `4` SQLite backend failure.
- Coordination is pull-based — messages persist and siblings read them at their
  own checkpoints; there is no nudge. The DB is a plaintext SQLite file under
  `/tmp` (`0600`, owner-only): never put secrets in messages.
