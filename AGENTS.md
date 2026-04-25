# AGENTS.md

This file provides guidance to Codex CLI when working with code in this
repository.

## Project Shape

This is a single-file Bash CLI (`fanout`) plus docs and agent integration
files. There is no build system, test suite, or lint config.

Source-of-truth integration files:

- Claude Code: `claude/commands/fanout.md` and
  `claude/skills/fanout/SKILL.md`, installed under `~/.claude/`.
- Codex CLI: `codex/skills/fanout/SKILL.md`, installed under
  `~/.codex/skills/fanout/`.

Do not edit installed copies under home directories directly. Edit the repo
versions and rerun `make install` or `make link`.

The user-facing surface is documented in `README.md` and `README.ja.md`. Read
the README before changing CLI behavior.

## Working With The Script

- Run it directly with `./fanout <parent-issue>`.
- Validate logic without driving dmux with
  `./fanout <parent-issue> --dry-run`.
- Lint with `shellcheck fanout` when available. Treat quoting warnings as real.
- A live end-to-end test requires tmux, a running dmux session, and a real
  GitHub parent issue with OPEN sub-issues. There is no mock layer.

## Architecture Notes

- Discovery uses tmux session options:
  `@dmux_controller_pid`, `@dmux_control_pane`, `@dmux_config_path`, and
  `@dmux_project_root`.
- Pane creation is driven through dmux's TUI because dmux v5.6.3 does not ship
  the documented HTTP API. fanout sends `Escape` + `n` to the control pane,
  then intercepts dmux popup result files.
- There are two popups per issue: the new-pane prompt and the agent picker.
  fanout injects `{"success":true,"data":"<prompt>"}` into the first and
  `{"success":true,"data":["<agent>"]}` into the second.
- The `[fanout #<NUM>]` prompt prefix is the idempotency primitive. Keep it if
  prompt formatting changes.
- Keep prompts one line. Full issue context belongs in
  `/tmp/fanout-<repo>-<NUM>.md`; dmux also derives slug/worktree names from the
  prompt text.
- Completion is detected by polling `dmux.config.json` for
  `panes[].length` growth. There is no callback from dmux.
- `--include` widens the child set; `--only` and `--skip` narrow it. Prose
  scanning for implicit children lives in the Claude/Codex skills, not in the
  Bash CLI.
- `--name` only threads caller-generated names through. The skills generate
  slug hints and display names from issue context; the CLI does not call an
  LLM for naming.

## Be Careful

- The script assumes the dmux TUI is on the pane-list view. It cannot navigate
  arbitrary modals, editors, or confirmation prompts.
- Popup interception depends on dmux internals: popup script names under
  `dist/components/popups/*Popup.js`, result JSON shaped as
  `{"success":true,"data":...}`, and result files named
  `<tmpdir>/dmux-popup-<digits>.json`.
- `pgrep -f` can match a popup process tree. Keep the Node-process filtering in
  popup lookup logic so killing the wrong wrapper process does not orphan a
  stale popup.
- Agent names must match dmux's enabled agent names exactly. A misspelled
  injected name makes pane creation fail without a useful dmux-side message.
