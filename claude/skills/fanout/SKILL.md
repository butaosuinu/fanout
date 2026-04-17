---
name: fanout
description: Spawn one dmux pane per OPEN sub-issue of a GitHub parent issue via the fanout CLI. Use when the user is working in a dmux pane on a parent issue and wants to parallelize its children across independent git worktrees/agent sessions.
---

# fanout

`fanout <parent-issue>` enumerates a GitHub parent issue's OPEN sub-issues and, for each one, asks dmux to create a new pane with its own git worktree and an agent CLI started with a briefing that points at `/tmp/fanout-<repo>-<N>.md`. The caller's pane is not modified, so this is safe to invoke from inside an agent session that is itself running in a dmux pane.

The CLI lives at `/Users/butaosuinu/.local/bin/fanout`; source and docs are in `/Users/butaosuinu/fanout/`.

## When to invoke

Good fits:

- The user is in a dmux-managed pane on a parent issue that has OPEN sub-issues, and asks (explicitly or implicitly) to parallelize the children.
- The user types `/fanout` or mentions "fan out" / "並列展開".

Do not invoke unprompted just because an issue has sub-issues. Pane creation is visible and the user has to close each pane manually if they change their mind — suggest first, wait for a "yes", and prefer routing through the `/fanout` slash command so there is one consistent entry point.

## Pre-flight

Before running the real command:

1. **Prerequisites** — `gh`, `jq`, `tmux`, `pgrep`, and the `gh-sub-issue` extension must be installed. `fanout` validates these on startup and fails with install hints, so you can rely on its error output rather than re-checking.
2. **Live dmux session** — `tmux list-sessions -F '#{session_name} #{session_id}'` and look for any session whose `@dmux_controller_pid` option is set and alive. If none, tell the user to `cd <target-repo> && dmux` first.
3. **Agent name is required** — dmux v5.6.3 always opens its agent-choice popup after the prompt popup, even when only one agent is enabled, and `fanout` drives it by injecting the agent name into the popup's result file. If you're invoking `/fanout` from inside a dmux-managed agent pane (the usual case), `fanout` auto-detects the caller's agent from `dmux.config.json` and you don't need any flag. From a plain shell outside dmux, pass `--agent <name>` or `fanout` will fail fast before touching the TUI.
4. **Dry-run** — run `fanout <N> --dry-run <forwarded-flags>` and show the user: how many children, their titles, the briefing paths. This is the confirmation step. `--debug` is available if the user wants to see the popup-intercept steps on the real run.

cwd does not matter. `fanout` discovers dmux via tmux session options (`@dmux_controller_pid`, `@dmux_control_pane`, `@dmux_config_path`, `@dmux_project_root`). Do not `cd` before invoking.

## Running

- **Default**: `fanout <N> --dry-run` → summarize → ask user to confirm → `fanout <N>`.
- **Bypass**: if the user's invocation carries `--go`, skip the confirmation and run directly.
- **Forward extra flags** (`--agent`, `--limit`, `--session`, `--sleep`) verbatim to both the dry-run and the real run. Strip `--go` before forwarding — it is the slash command's own flag, not a `fanout` flag.

## After running

- Relay the `created / skipped / deferred / failed` summary.
- The caller's pane is untouched. Continue working on the parent issue's own scope in the current session.
- Re-invocation is idempotent: already-fanned issues are detected via the `[fanout #<N>]` prompt prefix in `dmux.config.json` and skipped.

## Failure mapping

When `fanout` exits non-zero, point the user at `/Users/butaosuinu/fanout/README.md` Troubleshooting. Common cases:

- `no active dmux session found` — user needs to `cd <repo> && dmux` first.
- `multiple dmux sessions active` — rerun with `--session <name>` (list via `tmux list-sessions -F '#{session_name}'`).
- `timed out after 60s waiting for config.json to grow` — a popup-intercept stage failed or the dmux TUI has a stray modal open. Ask the user to rerun with `--debug` to see which popup didn't appear, press `Esc` in the dmux pane until the list view is visible, then retry. On slow machines, increase `--sleep`.
- `no agent resolved` — the caller isn't in a dmux-managed pane and no `--agent` was passed. Ask the user which agent to launch and retry with `--agent <name>`.
- `gh sub-issue list failed` — install `gh extension install yahsan2/gh-sub-issue` or run `gh auth status`.
- `no sub-issues on #<N>` is not a failure; fanout exits 0.

## Non-goals

- Do not attempt to modify the dmux TUI state beyond running `fanout`. The script already sends one `Escape` as best-effort recovery; anything more is outside scope.
- Do not rewrite or wrap the `fanout` script. The approved interface is the CLI as-is.
- Do not assume an HTTP API on dmux. The CLI drives dmux via `tmux send-keys` because dmux v5.6.3 does not ship the documented HTTP API yet.
