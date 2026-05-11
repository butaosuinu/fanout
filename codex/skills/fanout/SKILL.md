---
name: fanout
description: Use the fanout CLI from Codex CLI to spawn one dmux pane per OPEN sub-issue of a GitHub parent issue, or per OPEN item of a GitHub Projects v2 board (Project URL). Use when the user is working in a dmux pane and asks to fan out, parallelize, or split child issues / project items across independent git worktrees/agent sessions.
metadata:
  short-description: Fan out GitHub sub-issues or Projects v2 items into dmux panes
---

# fanout

`fanout <parent-issue-or-project-url>` enumerates either a GitHub parent
issue's OPEN sub-issues *or* a GitHub Projects v2 board's OPEN items, and
asks dmux to create one new pane per child. Each pane gets its own git
worktree and an agent CLI prompt that points at
`/tmp/fanout-<repo>-<N>.md`. The caller's pane is not modified.

The positional argument selects the mode: an integer (or `#N`) means
**issue mode**; a URL of the form
`https://github.com/(users|orgs)/<owner>/projects/<num>` means **project
mode**. Both modes share everything downstream of child enumeration
(briefing generation, idempotency, filters, dmux popup interception); only
the source of children differs.

The CLI is normally installed at `~/.local/bin/fanout`; source and docs are in
this repository. Codex discovers this skill from `~/.codex/skills/fanout`.

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
   shapes are accepted; pass whichever matches to the CLI verbatim as the
   positional argument:
   - **Issue mode** — integer or `#N` (e.g. `#42`).
   - **Project mode** — Projects v2 URL matching
     `^https://github\.com/(users|orgs)/[^/]+/projects/\d+/?$`.

   If there is no clear issue number or Project URL, ask which parent issue
   or Project to fan out.
2. Forward user-supplied fanout flags verbatim:
   `--agent`, `--limit`, `--only`, `--skip`, `--include`,
   `--unblocked-only`, `--status` (project mode only), `--name`, `--session`,
   `--sleep`, `--popup-timeout`, and `--debug`.
3. If the user asked to skip confirmation (`--go`, "go ahead", "run it now"),
   strip `--go` before calling the CLI and run the real command after the
   pre-flight name/include preparation. Otherwise dry-run first and ask for
   confirmation before the real run.
4. **Issue mode only — skip in project mode.** Scan the parent body for
   implicit children that the CLI does not parse: fetch it with
   `gh issue view <parent> --json body -q .body`, and compare against
   `fanout <parent> --dry-run <flags>` target output. In project mode the
   Project items are the source-of-truth and there is no parent body to scan.
5. For each final target issue, generate a pane name unless the user already
   supplied `--name` for that number. Forward one repeatable
   `--name <NUM>=<slug-hint>|<display-name>` flag per target. In project mode
   pull each target's number and title from the dry-run output; fetch
   per-issue body via `gh issue view <num> --json body -q .body` only if the
   title alone is not enough to name the pane.
6. Dry-run with `fanout <target> --dry-run <flags>`, summarize the mode
   banner (issue / project), targets, briefing paths, generated names,
   skipped/deferred rows, and warnings (including "cross-repo item skipped"
   in project mode). Do not dump raw `tmux send-keys` lines unless the user
   asks for debug detail.
7. After confirmation, run `fanout <target> <flags>` and relay the
   created/skipped/deferred/failed summary.

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
+ parent body. Key points:

- **URL shape** — `https://github.com/users/<owner>/projects/<num>` and
  `https://github.com/orgs/<owner>/projects/<num>` (optional trailing `/`)
  are both accepted. Anything else is rejected at arg-parse time.
- **`--status` filter** — Project items have a single-select `Status` field.
  Default is `--status Todo` (so `fanout <url>` fans out only the Todo
  column). Pass `--status all` to disable the filter and include every OPEN
  item, or any single Status value (e.g. `--status "In Progress"`) for that
  column. The match is case-sensitive against the Project's option labels.
  If the Project has no `Status` field, fanout warns and falls back to all
  OPEN items. `--status` is silently ignored in issue mode.
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
  containing only `[a-z0-9-]`, such as `fix-login-timeout`.
- `display-name`: readable pane title, Japanese or English OK, ideally
  40 characters or fewer.

Forward as `--name <NUM>=<slug-hint>|<display-name>`. If the user supplied a
name for a number, respect it and fill only missing numbers.

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
- Project mode `no items on Project <url>` (or "no items matching --status
  <name>") is not a failure; fanout exits 0.

## Notes

- Reruns are idempotent: existing panes are detected by the `[fanout #N]`
  prompt prefix in `dmux.config.json`. This holds across modes — fanning out
  the same child via both an issue parent and a Project URL never creates a
  duplicate pane.
- `--unblocked-only` defers children whose blockers are still OPEN and is
  preferred over hand-built wave lists when blocker annotations exist.
- Default project-mode filter is `--status Todo`. Use `--status all` for a
  full sweep of the board's OPEN items.
- The CLI intentionally drives dmux through tmux popup result-file
  interception because dmux v5.6.3 does not ship the documented HTTP API.
