---
description: Fan out a parent GitHub issue's OPEN sub-issues into parallel dmux panes with git worktrees via the fanout CLI.
argument-hint: "[parent-issue] [--go] [extra fanout flags]"
---

Invoke the `fanout` CLI to spawn one dmux pane per OPEN sub-issue of a parent GitHub issue. See the `fanout` skill (`~/.claude/skills/fanout/SKILL.md`) for context on when and why to use this.

Arguments: `$ARGUMENTS`

## Steps

1. **Resolve the parent issue number** from `$ARGUMENTS`:
   - First token matching `^#?\d+$` → that is `N`.
   - If none: scan the user's opening message / recent context for a `#\d+` reference and use the first match.
   - Still nothing: ask the user which parent issue to fan out.
2. **Detect `--go`** in the remaining arguments. Strip it out — it is this command's own bypass flag, not a `fanout` flag. The rest of the arguments are forwarded verbatim to `fanout`.
3. **Dry-run first** (unless `--go` was passed):
   - Run `fanout <N> --dry-run <forwarded>` via Bash. cwd is irrelevant; do NOT `cd` first.
   - Summarize the output: number of targets, child issue numbers and titles, briefing file paths under `/tmp/fanout-<repo>-<N>.md`.
   - Do not dump the raw `tmux send-keys` lines — they are long and noisy.
   - Ask the user to confirm.
4. **Execute**:
   - Run `fanout <N> <forwarded>` via Bash.
   - Relay the `created / skipped / deferred / failed` summary.
5. **On failure**: consult `/Users/butaosuinu/fanout/README.md` Troubleshooting and surface the most likely fix. Common cases:
   - dmux not running → tell the user to `cd <target-repo> && dmux` in another shell.
   - Multiple dmux sessions alive → rerun with `--session <name>`.
   - 60s wait-for-new-pane timeout or `popup did not appear within <N>s` → a popup-intercept stage failed; ask the user to rerun with `--debug`, press `Esc` in the dmux pane, and retry. If specifically `agentChoicePopup did not appear within 20s` on a large worktree, suggest bumping `--popup-timeout 45` (or higher).
   - `no agent resolved` → the caller isn't in a dmux-managed pane; rerun with `--agent <name>`.
   - Missing `gh-sub-issue` extension → `gh extension install yahsan2/gh-sub-issue`.

## Notes

- `fanout` never touches the caller's pane; the agent keeps working on the parent issue in the current session.
- Rerun is safe; idempotency is handled by the `[fanout #<N>]` prompt prefix.
- Default flags the CLI already applies: `--sleep 4`, `--popup-timeout 20`. Pass `--sleep 8` or higher on slow machines. Pass `--popup-timeout 45` (or higher) when dmux is slow to open the agent-choice popup on large worktrees.
- `fanout` auto-detects the calling pane's agent from `dmux.config.json` and injects it into dmux's agent-choice popup via popup-result-file interception. Do not pass `--agent` yourself; only pass it when the user explicitly wants to override (e.g. spawn children under a different agent than the parent pane) or when the caller isn't in a dmux-managed pane.
- If the user asks why this is complicated: dmux v5.6.3 renders both the prompt input and the agent picker via `tmux display-popup` (separate tmux clients that `send-keys` cannot reach), so fanout intercepts each popup's `<tmpdir>/dmux-popup-*.json` result file. See `/Users/butaosuinu/fanout/README.md` ("Why this looks weird") for details. `--debug` exposes the intercept steps.

## Examples

- `/fanout 123` — dry-run preview for parent issue #123, then real run after confirmation.
- `/fanout 123 --go` — skip confirmation, run immediately.
- `/fanout 123 --limit 3 --agent codex` — only the first 3 children, override the auto-detected agent and force the picker to `codex`.
- `/fanout` (no args in a session that started with "work on #456") — extract `456` from context and proceed.
