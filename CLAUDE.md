# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project shape

Single-file Bash script (`fanout`) plus its design doc (`fanout.README.md`). No build system, no test suite, no lint config. `claude` (the 200MB Mach-O binary at the repo root) is unrelated to this project — leave it alone.

The Claude Code integration files (`claude/commands/fanout.md` slash command and `claude/skills/fanout/SKILL.md` skill) are bundled in the repo as the source-of-truth. `make install` places them under `~/.claude/`. Don't edit copies in `~/.claude/` directly — edit the repo versions and rerun `make install` (or use `make link` during development).

The user-facing surface (CLI flags, prerequisites, troubleshooting) is in `fanout.README.md`. Read it before changing behavior; this file covers only what's not obvious from the README.

## Working with the script

- Run it: `./fanout <parent-issue>` (it's executable; no install step).
- Verify changes without driving dmux: `./fanout <parent-issue> --dry-run`. The dry-run path prints every `tmux send-keys` invocation with `%q` quoting and the would-be briefing size, so use it as the primary way to validate logic changes that don't need a live dmux.
- Lint: `shellcheck fanout` (no config file; treat `SC2086`-style warnings as real — the script intentionally quotes everything because prompts and titles can contain spaces and shell metacharacters).
- A live end-to-end test needs a running dmux session in tmux and a real GitHub parent issue with OPEN sub-issues; there is no mock layer.

## Architecture notes that span the script

- **No HTTP, no sockets.** All IPC with dmux goes through (a) tmux session options for discovery (`@dmux_controller_pid`, `@dmux_control_pane`, `@dmux_config_path`, `@dmux_project_root`) and (b) `tmux send-keys` against the control pane. This is intentional — dmux v5.6.3 doesn't ship the HTTP API its docs describe. See `fanout.README.md` ("Why this looks weird") before proposing a refactor toward `POST /api/panes`.
- **Idempotency primitive: the `[fanout #<NUM>]` prompt prefix.** The script grep-detects already-fanned issues by reading `panes[].prompt` from `dmux.config.json` (jq `capture` against `^\[fanout #(?<n>[0-9]+)\]`). Anything that changes the prompt format must keep this prefix or migrate the detection logic in lockstep — otherwise a rerun creates duplicate panes. The membership test (`fanned_set`) is space-sentinel-wrapped on purpose so `42` doesn't match `142`.
- **One-line prompt only.** ink-text-input in dmux treats Enter as submit, so the actual briefing lives in `/tmp/fanout-<repo>-<NUM>.md` and the prompt is just `[fanout #N] <title>: read <path> and begin.`. Don't try to send multi-line prompts via `tmux send-keys`.
- **Pane-creation completion is detected by polling `dmux.config.json`'s `panes[].length`** (`wait_for_new_pane`, 60s timeout, 0.5s poll). There is no callback from dmux; if you change the create flow, keep some monotonic indicator to wait on.
- **`--sleep` is rate-limit, not retry.** It's the gap between successful creations to let dmux finish the worktree-creation phase before the next `n` is sent. It is not a retry/backoff knob.
- **Failure mode is fail-fast.** The main loop `break`s on the first `create_pane_for_issue` failure rather than continuing — assumption is that one failure usually means the dmux TUI is wedged (modal stuck, agent picker on wrong item) and continuing would create garbage. Preserve this unless you also add per-issue recovery.

## Things to be careful with

- The script assumes the dmux TUI is on the pane-list view. It sends one `Escape` per attempt as best-effort recovery, but cannot unstick `$EDITOR` or confirm dialogs. Don't add features that assume you can navigate arbitrary modals.
- `tmux send-keys -l` sends bytes literally — UTF-8 in issue titles can garble on terminals with aggressive remaps. Keep that in mind when changing the prompt template.
- The `--agent <name>` popup-navigation logic only sends the agent name's first letter. It is best-effort; don't promise it works for two agents whose names start with the same letter.
