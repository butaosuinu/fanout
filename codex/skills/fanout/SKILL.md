---
name: fanout
description: Use the fanout CLI from Codex CLI to spawn one dmux pane per OPEN sub-issue of a GitHub parent issue. Use when the user is working in a dmux pane on a parent issue and asks to fan out, parallelize, or split child issues across independent git worktrees/agent sessions.
metadata:
  short-description: Fan out GitHub sub-issues into dmux panes
---

# fanout

`fanout <parent-issue>` enumerates a GitHub parent issue's OPEN sub-issues and
asks dmux to create one new pane per child. Each pane gets its own git
worktree and an agent CLI prompt that points at
`/tmp/fanout-<repo>-<N>.md`. The caller's pane is not modified.

The CLI is normally installed at `~/.local/bin/fanout`; source and docs are in
this repository. Codex discovers this skill from `~/.codex/skills/fanout`.

## When To Use

Use this skill when the user explicitly asks to fan out, parallelize, or split
work for a GitHub parent issue, including Japanese phrasing like `並列展開`.
Do not invoke fanout just because an issue has sub-issues; pane creation is
visible and the user has to close unwanted panes manually.

Codex does not need a custom slash command for this integration. If the user
asks for `$fanout`, "fan out #123", or similar, use this workflow directly.

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

1. Resolve the parent issue number from the user's request or recent context.
   If there is no clear issue number, ask which parent issue to fan out.
2. Forward user-supplied fanout flags verbatim:
   `--agent`, `--limit`, `--only`, `--skip`, `--include`,
   `--unblocked-only`, `--name`, `--session`, `--sleep`,
   `--popup-timeout`, and `--debug`.
3. If the user asked to skip confirmation (`--go`, "go ahead", "run it now"),
   strip `--go` before calling the CLI and run the real command after the
   pre-flight name/include preparation. Otherwise dry-run first and ask for
   confirmation before the real run.
4. Scan the parent body for implicit children that the CLI does not parse:
   fetch it with `gh issue view <parent> --json body -q .body`, and compare
   against `fanout <parent> --dry-run <flags>` target output.
5. For each final target issue, generate a pane name unless the user already
   supplied `--name` for that number. Forward one repeatable
   `--name <NUM>=<slug-hint>|<display-name>` flag per target.
6. Dry-run with `fanout <parent> --dry-run <flags>`, summarize targets,
   briefing paths, generated names, skipped/deferred rows, and warnings. Do
   not dump raw `tmux send-keys` lines unless the user asks for debug detail.
7. After confirmation, run `fanout <parent> <flags>` and relay the
   created/skipped/deferred/failed summary.

## Implicit Child Scan

fanout itself only detects children from the Sub-issues API and parent-body
task-list rows shaped like `- [ ] #N`. During pre-flight, identify child-like
references that should be forwarded via `--include`.

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

## Notes

- Reruns are idempotent: existing panes are detected by the `[fanout #N]`
  prompt prefix in `dmux.config.json`.
- `--unblocked-only` defers children whose blockers are still OPEN and is
  preferred over hand-built wave lists when blocker annotations exist.
- The CLI intentionally drives dmux through tmux popup result-file
  interception because dmux v5.6.3 does not ship the documented HTTP API.
