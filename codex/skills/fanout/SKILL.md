---
name: fanout
description: Use the fanout CLI from Codex CLI to spawn one dmux pane per OPEN sub-issue of a GitHub parent issue, or per OPEN item of a GitHub Projects v2 board (Project URL). Use when the user is working in a dmux pane and asks to fan out, parallelize, or split child issues / project items across independent git worktrees/agent sessions.
metadata:
  short-description: Fan out GitHub sub-issues or Projects v2 items into dmux panes
---

# fanout

## Synopsis

```
fanout <parent-issue|project-url>
       [--agent <name>] [--limit <N>] [--only <list>] [--skip <list>]
       [--include <list>] [--unblocked-only] [--project-status <name>]
       [--name <NUM>=<slug>[|<display>[|<branch>]]]
       [--session <tmux-session>] [--sleep <seconds>]
       [--popup-timeout <seconds>] [--dry-run] [--debug]
fanout <parent-issue> --status      # JSON status of fanned children, no side effects
```

**Do not probe the CLI** with `fanout --help`, `fanout -h`, or
`which fanout`. This SKILL.md is the source-of-truth for the CLI surface —
every flag above is documented in the sections below, and the binary path
is normally `~/.local/bin/fanout` (see the next paragraph). Calling `--help`
or `which` just to "verify" the surface wastes a tool call and adds nothing.

`fanout <parent-issue-or-project-url>` enumerates either a GitHub parent
issue's OPEN sub-issues *or* a GitHub Projects v2 board's OPEN items, and
asks dmux to create one new pane per child. Each pane gets its own git
worktree and an agent CLI prompt that points at
`/tmp/fanout-<repo>-<N>.md`. The caller's pane is not modified.

The positional argument selects the mode: a bare integer means **issue mode**;
a URL of the form
`https://github.com/(users|orgs)/<owner>/projects/<num>` means **project
mode**. Both modes share everything downstream of child enumeration
(briefing generation, idempotency, filters, dmux popup interception); only
the source of children differs. User-facing issue refs like `#N` are accepted
by this skill, but strip the leading `#` before invoking the CLI.

The CLI is normally installed at `~/.local/bin/fanout`; source and docs are in
this repository. Codex discovers this skill from `~/.codex/skills/fanout`.
Always invoke the stable `fanout` command name. If this installation should
prefer the Go implementation, the repository's `make install-go-default` or
`make link-go-default` target places the Go binary at that same path; do not
probe for or call `fanout-go` directly from this workflow.

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

1. Prerequisites are `gh`, `jq`, `tmux`, `pgrep`, and the `gh-sub-issue`
   extension. The CLI validates these on startup, so rely on its error output.
2. A live dmux session must exist. If fanout reports `no active dmux session
   found`, tell the user to run `cd <repo> && dmux` first.
3. An agent name must be resolvable. From inside a dmux-managed Codex pane,
   fanout auto-detects it from `dmux.config.json`. From a plain shell, pass
   `--agent <name>`.
4. dmux's TUI should be on the pane-list view with no modal open. fanout sends
   one `Esc` as best-effort recovery, but it cannot exit arbitrary editors or
   confirmation prompts.

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
   2. Run `gh project list --format json` for the current user's Projects.
   3. If the current repo is org-owned, get the org with
      `gh repo view --json owner -q .owner.login` and also run
      `gh project list --owner <org> --format json`.
   4. Present one combined list: issues as `#<num> <title>`, Projects as
      `<title> (<url>)`. Dedupe Projects by URL if current-user and org
      results overlap, then ask the user to choose one.
   5. If both lists are empty, tell the user there is no OPEN issue or Project
      target to fan out and stop.
   6. Resolve the selection to the CLI positional arg: issues become bare
      digits with any leading `#` removed; Projects become the Project URL
      from `gh project list`.

   This is skill-side target resolution for non-TTY agent entrypoints. Do not
   change the Go `fanout` CLI for it; the CLI already accepts the resolved
   positional arg via `internal/cliflags.Parse()`.
2. Forward user-supplied fanout flags verbatim:
   `--agent`, `--limit`, `--only`, `--skip`, `--include`,
   `--unblocked-only`, `--project-status` (project mode only), `--name`,
   `--session`, `--sleep`, `--popup-timeout`, and `--debug`.
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
5. For each final target issue, generate a pane name unless the user already
   supplied `--name` for that number. Forward one repeatable
   `--name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]` flag per
   target. The 3rd segment (branch-name, dmux v5.8.1+) is optional and
   should be filled in only when the team has a branch-naming convention
   worth enforcing (e.g. `feat/issue-<N>-foo`, `release/v2.0`); otherwise
   leave it empty so dmux's default `branchPrefix + slug` applies. In
   project mode pull each target's number and title from the dry-run
   output; fetch per-issue body via `gh issue view <num> --json body
   -q .body` only if the title alone is not enough to name the pane.
6. Dry-run with `fanout <target> --dry-run <flags>`, summarize the mode
   banner (issue / project), targets, briefing paths, generated names,
   skipped/deferred rows, and warnings (including "cross-repo item skipped"
   in project mode). Do not dump raw `tmux send-keys` lines unless the user
   asks for debug detail.
7. After confirmation, run `fanout <target> <flags>` and relay the
   created/skipped/deferred/failed summary.

## Optional: Wait-and-Continue

Use this workflow only when the user has explicitly asked to "wait until every
child PR merges and then continue parent-scope work" (Japanese: `子 PR が全部
マージされたら統合まで進めて` or similar). Do not start it unprompted.

Codex CLI does not provide a built-in scheduler, so polling is driven by the
user (or an external cron / shell loop). The pattern:

1. After the real fanout run has succeeded, continue any parent-scope work that
   doesn't depend on the children's merged output.
2. Periodically rerun `fanout --status <PARENT>`. Inspect the JSON; the key
   field is `summary.all_merged`.
3. When `summary.all_merged == true`, run
   `git fetch origin main && git merge --ff-only origin/main` in the parent
   worktree and proceed with integration tests and parent-issue close-out.
4. Treat `prs: []` on a child as pending (PR not yet open), never merged.

`--status` exit codes:

- `2` — cannot enumerate children (config / session missing, bad invocation).
  Stop and report.
- `3` — `gh` API call failed. Stop and report; the user may need to refresh
  `gh auth`.
- `0` with `summary.total == 0` — nothing has been fanned out under that parent
  (or every fanned pane was torn down). Tell the user; don't keep polling.

`--status` is read-only and exclusive with all action-bearing flags
(`--agent`, `--limit`, `--only`, `--skip`, `--include`, `--name`, `--sleep`,
`--popup-timeout`, `--dry-run`, `--unblocked-only`). Set `DMUX_CONFIG_PATH`
to bypass live-dmux-session discovery (useful after the session has exited).

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
- **`gh` scope** — Projects v2 GraphQL needs `read:project` on top of `repo`.
  If fanout reports an authorization failure on `projectV2`
  (`HTTP 401` / `Resource not accessible by integration`), instruct the
  user to run `gh auth refresh -s read:project` and rerun.
- **Cross-repo items are skipped** — items whose
  `content.repository.nameWithOwner` does not match the dmux project_root
  repo are warned and skipped. Briefings and worktrees assume a single repo;
  cross-repo items would land in the wrong checkout. Surface the warning to
  the user rather than retrying.
- **`--include` is allowed but rarely needed** — the Project already
  defines the set. Use it only when the user wants to force-add an issue
  that isn't on the board.
- **`--unblocked-only`** still applies. In project mode the parent-row
  trailer source is unavailable, so blockers come only from the child body's
  `## Blocked by` section and the `blocked` label.
- **Idempotency** — `[fanout #N]` detection is keyed on child issue number,
  so the same child is never fanned out twice even if reached via both an
  issue parent and a Project URL.

## Pane Names

dmux's default slug generator may call OpenRouter or a local
`claude --no-interactive` fallback just to name each pane. Since Codex already
has issue context during this workflow, generate names in conversation and
pass them to fanout.

For each target issue:

- `slug-hint`: 2-4 lowercase kebab-case words, starting with an alnum and
  containing only `[a-z0-9-]`, such as `fix-login-timeout`. Controls the
  worktree directory name (dmux's slug LLM echoes it).
- `display-name`: readable pane title, Japanese or English OK, ideally
  40 characters or fewer.
- `branch-name` *(optional, dmux v5.8.1+)*: exact git branch name to
  create. Use this only when the team has a branch-naming convention worth
  enforcing (e.g. `feat/issue-<N>-foo`, `release/v2.0`); otherwise leave
  it empty and dmux will use `branchPrefix + slug`. When supplied, fanout
  writes it as `branchName` in the newPanePopup payload, which dmux's
  `createPane()` consumes as `branchNameOverride`.

Forward as `--name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]`.
Any segment may be empty as long as at least one is non-empty
(`--name 17=fix-x` slug only, `--name 17=|Disp` display only,
`--name 17=||feat/x` branch only). If the user supplied a name for a
number, respect it and fill only missing segments.

## Failure Mapping

When fanout exits non-zero, use the README troubleshooting section and surface
the likely next action:

- `no active dmux session found`: start dmux with `cd <repo> && dmux`.
- `multiple dmux sessions active`: rerun with `--session <name>`.
- `timed out after 60s waiting for config.json to grow`: make sure the dmux
  pane is on the list view, press `Esc`, and retry with `--debug`.
- `agentChoicePopup did not appear within 20s`: on slow or large worktrees,
  retry with `--popup-timeout 45` or higher.
- `no agent resolved`: rerun from inside a dmux-managed agent pane or pass
  `--agent <name>`.
- `gh sub-issue list failed`: check `gh auth status` and install
  `gh extension install yahsan2/gh-sub-issue`.
- `no sub-issues on #<N>` is not a failure; fanout exits 0.
- Project mode `HTTP 401` / `Resource not accessible by integration`
  against `projectV2`: the user's `gh` token lacks the `read:project` scope.
  Tell them to run `gh auth refresh -s read:project` and rerun.
- Project mode `no items in Project (after status/repo filter). nothing to
  do.` is not a failure; fanout exits 0.

## Notes

- Reruns are idempotent: existing panes are detected by the
  `[fanout #N of #<parent>]` prompt prefix in `dmux.config.json` — `<parent>`
  is the issue number in issue mode and the Projects v2 URL in project mode.
  Older panes from pre-#35 fanouts may carry the legacy `[fanout #N]` form
  without parent annotation; both shapes satisfy idempotency. Idempotency
  scopes to the requested parent, so fanning out the same child via both an
  issue parent and a Project URL creates one pane per parent (not a single
  shared pane).
- `--unblocked-only` defers children whose blockers are still OPEN and is
  preferred over hand-built wave lists when blocker annotations exist.
- Default project-mode filter is `--project-status Todo`. Use
  `--project-status all` for a full sweep of the board's OPEN items.
- When a created pane runs `codex`, the per-issue briefing requires the agent
  to run `codex review --uncommitted` after implementation/tests and repeat
  review -> fix -> retest -> review until no findings remain before it commits,
  pushes, or opens the PR.
- The CLI intentionally drives dmux through tmux popup result-file
  interception because dmux v5.8.1 still does not ship the documented HTTP
  API (an `apiActionHandler` skeleton exists in `dist/adapters/` but no
  transport is wired up). When dmux ships the real API, fanout will be able
  to collapse the intercept down to a single `POST /api/panes`.
