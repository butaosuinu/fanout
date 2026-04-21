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
4. **Body scan for implicit children** — `fanout` itself only treats two things as children: issues returned by the Sub-issues API, and parent-body rows that match `^\s*-\s+\[[ xX]\] ... #N`. Parent issues in the wild often *describe* their children via prose instead, and those references must be surfaced to the user and forwarded as `--include`.
   1. Run `gh issue view <parent> --json body -q .body` to fetch the body.
   2. Also run `fanout <parent> --dry-run <forwarded>` once to see what numbers `fanout` already auto-discovers (the two sources above). Hold on to that list so you don't suggest duplicates.
   3. Read the body and identify issue numbers that are **referred to as children** but aren't in the auto-discovered list. Typical indicators:
      - Close/fix/resolve keywords: `Closes #N`, `Fixes #N`, `Resolves #N` (any case; `Closes #1, #2, #3` is one row referring to three children).
      - Dependency / relation wording: `Depends on #N`, `Blocked by #N`, `Related to #N`, `See #N`, `Refs #N`.
      - Plain bullets without a checkbox: `- #N`, `* #N`, `+ #N`.
      - Japanese idioms: `#N に関連`, `#N を対応`, `#N 対応中`, `#N をブロック`, `#N の子issue`, `#N の子タスク`, `#N を修正`, `#N を解決` and near-variants.
   4. **Exclude** from the candidate list:
      - `owner/repo#N` cross-repo references — `fanout` only operates on the parent's repo.
      - Bare `#N` with no surrounding keyword or bullet prefix (e.g. "introduced in #12", "as noted in #99") — likely a historical reference, not a child.
      - References inside fenced code blocks (```…```) or blockquotes (`> …`) — usually quoted examples, not real children.
      - The parent issue's own number.
      - Numbers that already appear in the dry-run's target list.
   5. If candidates remain, **list them back to the user** with a one-line justification each (quote the body line that implied child status) and ask whether to include them. If `--go` was passed, still print the list (for transparency) but auto-accept.
   6. Forward the accepted numbers as `--include A,B,C` to both the confirmation dry-run in step 5 and the real run.
   7. If no candidates are found, skip straight to step 5 with no `--include`.
5. **Dry-run** — run `fanout <N> --dry-run <forwarded-flags>` (including any `--include` from step 4) and show the user: how many children, their titles, the briefing paths. This is the confirmation step. `--debug` is available if the user wants to see the popup-intercept steps on the real run.

cwd does not matter. `fanout` discovers dmux via tmux session options (`@dmux_controller_pid`, `@dmux_control_pane`, `@dmux_config_path`, `@dmux_project_root`). Do not `cd` before invoking.

## Running

- **Default**: `fanout <N> --dry-run` → summarize → ask user to confirm → `fanout <N>`.
- **Bypass**: if the user's invocation carries `--go`, skip the confirmation and run directly.
- **Forward extra flags** (`--agent`, `--limit`, `--only`, `--skip`, `--include`, `--session`, `--sleep`, `--popup-timeout`) verbatim to both the dry-run and the real run. Strip `--go` before forwarding — it is the slash command's own flag, not a `fanout` flag.
- `--only <list>` / `--skip <list>` take a comma-separated list of issue numbers (e.g. `--only 4,7,8,10`). They are mutually exclusive. `--only` numbers not in the parent's OPEN child set are warned and ignored by the CLI — if the user names issues that aren't children, relay that warning instead of silently retrying.
- `--include <list>` takes a comma-separated list of issue numbers to force-add to the children set when the Sub-issues API and parent-body task-list scan don't surface them (e.g. `--include 123,456`). This is the channel for numbers produced by the "Body scan for implicit children" step above. Numbers that end up CLOSED or don't exist are warned and skipped by the CLI. Combines cleanly with `--only`/`--skip` (included first, then filtered).

## After running

- Relay the `created / skipped / deferred / failed` summary.
- The caller's pane is untouched. Continue working on the parent issue's own scope in the current session.
- Re-invocation is idempotent: already-fanned issues are detected via the `[fanout #<N>]` prompt prefix in `dmux.config.json` and skipped.

## Failure mapping

When `fanout` exits non-zero, point the user at `/Users/butaosuinu/fanout/README.md` Troubleshooting. Common cases:

- `no active dmux session found` — user needs to `cd <repo> && dmux` first.
- `multiple dmux sessions active` — rerun with `--session <name>` (list via `tmux list-sessions -F '#{session_name}'`).
- `timed out after 60s waiting for config.json to grow` — a popup-intercept stage failed or the dmux TUI has a stray modal open. Ask the user to rerun with `--debug` to see which popup didn't appear, press `Esc` in the dmux pane until the list view is visible, then retry. On slow machines, increase `--sleep`.
- `popup did not appear within <N>s` (e.g. `agentChoicePopup did not appear within 20s`) — dmux took longer than the popup-intercept window to open the popup. On large worktrees where dmux creates the worktree between popups, increase with `--popup-timeout 45` (or higher). The default is 20s.
- `no agent resolved` — the caller isn't in a dmux-managed pane and no `--agent` was passed. Ask the user which agent to launch and retry with `--agent <name>`.
- `gh sub-issue list failed` — install `gh extension install yahsan2/gh-sub-issue` or run `gh auth status`.
- `no sub-issues on #<N>` is not a failure; fanout exits 0.

## Non-goals

- Do not attempt to modify the dmux TUI state beyond running `fanout`. The script already sends one `Escape` as best-effort recovery; anything more is outside scope.
- Do not rewrite or wrap the `fanout` script. The approved interface is the CLI as-is.
- Do not assume an HTTP API on dmux. The CLI drives dmux via `tmux send-keys` because dmux v5.6.3 does not ship the documented HTTP API yet.
