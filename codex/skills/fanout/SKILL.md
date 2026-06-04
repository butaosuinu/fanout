---
name: fanout
description: Use the fanout CLI from Codex CLI to spawn one tmux pane per OPEN sub-issue of a GitHub parent issue, or per OPEN item of a GitHub Projects v2 board (Project URL). Use when the user is working in tmux and asks to fan out, parallelize, or split child issues / project items across independent git worktrees/agent sessions.
metadata:
  short-description: Fan out GitHub sub-issues or Projects v2 items into tmux panes
---

# fanout

## Synopsis

```
fanout <parent-issue|project-url>
       [--agent <name>] [--limit <N>] [--only <list>] [--skip <list>]
       [--include <list>] [--unblocked-only] [--project-status <name>]
       [--name <NUM>=<slug>[|<display>[|<branch>]]]
       [--base-branch <branch>] [--branch-prefix <prefix>] [--no-refresh]
       [--session <tmux-session>] [--sleep <seconds>]
       [--popup-timeout <seconds>] [--dry-run] [--debug]
       [--auto-pr|--no-auto-pr] [--pr-review-gate|--no-pr-review-gate]
       [--briefing-code-review|--no-briefing-code-review]
       [--agent-teams-hint|--no-agent-teams-hint]
fanout <parent-issue> --status      # JSON status of fanned children, no side effects
```

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

Use this skill when the user explicitly asks to fan out, parallelize, or split
work for a GitHub parent issue or a GitHub Projects v2 board, including
Japanese phrasing like `並列展開` or "プロジェクトの Todo 列を一気に着手".
Do not invoke fanout just because an issue has sub-issues; pane creation is
visible and the user has to close unwanted panes manually.

Codex does not need a custom slash command for this integration. If the user
asks for `$fanout`, "fan out #123", "fan out this project URL", or similar,
use this workflow directly.

## Pre-Flight

1. Prerequisites are `gh`, `jq`, `git`, `tmux`, and the `gh-sub-issue`
   extension. The CLI validates these on startup, so rely on its error output.
2. fanout must run inside tmux. If it reports `fanout must be run inside
   tmux`, tell the user to start or attach a tmux session first.
3. An agent name is required. Pass `--agent <name>` or set `FANOUT_AGENT`.
   MVP supported agents are `claude` and `codex`.

## Workflow

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
   `--agent`, `--limit`, `--only`, `--skip`, `--include`,
   `--unblocked-only`, `--project-status` (project mode only), `--name`,
   `--base-branch`, `--branch-prefix`, `--no-refresh`, `--session`,
   `--sleep`, `--popup-timeout`, `--debug`, `--auto-pr`,
   `--no-auto-pr`, `--pr-review-gate`, `--no-pr-review-gate`,
   `--briefing-code-review`, `--no-briefing-code-review`,
   `--agent-teams-hint`, and `--no-agent-teams-hint`.
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
   appends `-<NUM>` to slug hints that do not already have that suffix, so
   reruns remain idempotent even if names later change. In issue mode use the
   parent issue context and issue dry-run target set. In project mode use the
   discovery dry-run output from step 5; fetch per-issue body via
   `gh issue view <num> --json body -q .body` only if the title alone is not
   enough to name the pane.
7. Dry-run with `fanout <target> --dry-run <flags>`, summarize the mode
   banner (issue / project), targets, briefing paths, generated names,
   skipped/deferred rows, and warnings (including "cross-repo item skipped"
   in project mode). Summarize the command plan; do not paste every raw
   command unless the user asks for debug detail.
8. After confirmation, run `fanout <target> <flags>` and relay the
   created/skipped/deferred/failed summary.

## Optional: Wait-and-Continue

Temporarily disabled for new direct tmux action runs. Phase 1 does not write
fanout state, and `fanout --status` still reads legacy dmux state, so polling
it cannot observe panes launched through the direct runtime. If the user asks
for wait-and-continue, explain this limitation and do not start a polling loop
until the state-store phase lands.

`--status` exit codes:

- `2` — cannot enumerate children (config / session missing, bad invocation).
  Stop and report.
- `3` — `gh` API call failed. Stop and report; the user may need to refresh
  `gh auth`.
- `0` with `summary.total == 0` — nothing has been fanned out under that parent
  (or every fanned pane was torn down). Tell the user; don't keep polling.

`--status` is read-only and exclusive with all action-bearing flags
(`--agent`, `--limit`, `--only`, `--skip`, `--include`, `--name`,
`--base-branch`, `--branch-prefix`, `--no-refresh`, `--sleep`,
`--popup-timeout`, `--dry-run`, `--unblocked-only`, `--auto-pr`,
`--no-auto-pr`, `--pr-review-gate`, `--no-pr-review-gate`,
`--briefing-code-review`, `--no-briefing-code-review`, `--agent-teams-hint`,
`--no-agent-teams-hint`). Set `DMUX_CONFIG_PATH` to bypass legacy
dmux-session discovery for `--status` (useful after the session has exited).

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
Project's items via GraphQL (`gh api graphql`) instead of the Sub-issues API
+ parent body. The `gh-sub-issue` extension dependency check is skipped in
project mode, so a missing extension is not a blocker for project URLs.
Key points:

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
  the Claude-only Agent Teams hint. Defaults are all on, and these settings
  are Go-implementation only.
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
- **Idempotency** — Phase 1 action mode skips children when the exact
  `.fanout/worktrees/<slug>` directory exists or another generated worktree
  directory ends in `-<issue-number>`. Full state-store idempotency is handled
  by a later phase.

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

- `fanout must be run inside tmux`: start or attach a tmux session and rerun.
- `agent is required`: pass `--agent claude`, `--agent codex`, or set
  `FANOUT_AGENT`.
- `unknown agent`: use one of the supported MVP agents (`claude`, `codex`).
- `agent "<name>" is not installed`: install that CLI or choose another agent.
- `prepare worktree`: inspect the git error; `--no-refresh` can bypass base
  branch refresh only when the stale base is intentional.
- `gh sub-issue list failed`: check `gh auth status` and install
  `gh extension install yahsan2/gh-sub-issue`.
- `no sub-issues on #<N>` is not a failure; fanout exits 0.
- Project mode `HTTP 401` / `Resource not accessible by integration`
  against `projectV2`: the user's `gh` token lacks the `read:project` scope.
  Tell them to run `gh auth refresh -s read:project` and rerun.
- Project mode `no items in Project (after status/repo filter). nothing to
  do.` is not a failure; fanout exits 0.

## Notes

- Action-mode reruns skip children when the exact `.fanout/worktrees/<slug>`
  directory exists or another generated worktree directory ends in
  `-<issue-number>`. `--status` still reads legacy dmux state until the
  state-store phase lands.
- `--unblocked-only` defers children whose blockers are still OPEN and is
  preferred over hand-built wave lists when blocker annotations exist.
- Default project-mode filter is `--project-status Todo`. Use
  `--project-status all` for a full sweep of the board's OPEN items.
- When a created pane runs `codex`, the per-issue briefing requires the agent
  to run `codex review --uncommitted` after implementation/tests and repeat
  review -> fix -> retest -> review until no findings remain before it commits,
  pushes, or opens the PR.
- The action path creates git worktrees itself, then uses detached
  `tmux split-window -t <invoking-pane> -d` and `tmux send-keys` to launch the
  selected agent CLI without moving focus away from the caller pane. `--session`
  is the explicit escape hatch for targeting a different session.
