# AGENTS.md

This file provides guidance to Codex CLI when working with code in this
repository.

## Project Shape

`fanout` is a Go CLI (`cmd/fanout` + `internal/`); `make install` builds it
(`go build ./cmd/fanout`) and installs it at `$(BINDIR)/fanout`. `make build-go`
produces the local `./fanout-go` binary; `make test` runs the Go unit tests plus
the bats black-box suite against it (via `FANOUT_BIN`); `make lint` is
go vet + gofmt + a shellcheck of the test shims.

Source-of-truth integration files:

- Claude Code: `claude/commands/fanout.md` and
  `claude/skills/fanout/SKILL.md`, installed under `~/.claude/`.
- Codex CLI: `codex/skills/fanout/SKILL.md`, installed under
  `~/.codex/skills/fanout/`.

Do not edit installed copies under home directories directly. Edit the repo
versions and rerun `make install` or `make link`.

The user-facing surface is documented in `README.md` and `README.ja.md`. Read
the README before changing CLI behavior.

## Working With fanout

`fanout` is a Go CLI. Build with `make build-go` (output `./fanout-go`) and
validate with `make test`. `cmd/fanout` holds the command flow; `internal/`
holds the reusable packages (see the architecture notes below).

- Run it: `make build-go`, then `./fanout-go <parent-issue>`.
- Validate logic without driving dmux by appending `--dry-run`, e.g.
  `./fanout-go <parent-issue> --dry-run`.
- Lint with `make lint` (go vet + gofmt + a shellcheck of the bats test shims).
  Treat shellcheck quoting warnings on the shims as real.
- Run `make test` (Go unit tests + Tier 1/Tier 2 bats against `./fanout-go`)
  before relying on a change.
- A live end-to-end test requires tmux, a running dmux session, and a real
  GitHub parent issue with OPEN sub-issues. There is no mock layer.

## Architecture Notes

The package map: `cmd/fanout` is the command flow (`main.go` `executePlan`
loop, `pane.go` `createPaneForIssue`, `plan.go` filtering, `status.go`,
`report.go`). `internal/` holds `cliflags`, `ghissue`, `dmuxconfig`,
`dmuxsession`, `popup`, `blockers`, `displayname`, `briefing`, `tmuxctl`, plus
`atomicfs`/`log`/`tty`/`exitcode`.

- Discovery (`internal/dmuxsession`) uses tmux session options:
  `@dmux_controller_pid`, `@dmux_control_pane`, `@dmux_config_path`, and
  `@dmux_project_root`.
- Pane creation is driven through dmux's TUI because dmux does not ship the
  documented HTTP API. `internal/tmuxctl` sends `Escape` + `n` to the control
  pane, then `internal/popup` (`popup.FindNew` → `popup.WriteResult`)
  intercepts dmux popup result files.
- There are two popups per issue: the new-pane prompt and the agent picker.
  `popup.MakeNewPanePayload` injects `{"success":true,"data":"<prompt>"}` into
  the first and `popup.MakeAgentPayload` injects
  `{"success":true,"data":["<agent>"]}` into the second.
- The `[fanout #<NUM>]` prompt prefix is the idempotency primitive
  (`dmuxconfig.Config.FannedNumbersForParent`, regex `fanoutPrefixRE`). Keep it
  if prompt formatting changes.
- Keep prompts one line (`oneLinePrompt`). Full issue context belongs in
  `/tmp/fanout-<repo>-<NUM>.md` (`internal/briefing`); dmux also derives
  slug/worktree names from the prompt text.
- Completion is detected by `waitForNewPane` polling `dmux.config.json` for
  `panes[].length` growth. There is no callback from dmux.
- `--include` widens the child set; `--only` and `--skip` narrow it
  (`filterOnlySkip`; child enumeration in `mergeExtraChildren` via
  `internal/ghissue`). Prose scanning for implicit children lives in the
  Claude/Codex skills, not in the CLI.
- `--unblocked-only` filters out issues with OPEN blockers (`splitBlocked` +
  `internal/blockers`); `--limit` overflow lands in `Plan.LimitDeferred`.
- `--name` only threads caller-generated names through
  (`cliflags.parseNameArg` → `NameOverride`; applied by `popup.MakeNewPanePayload`
  for the branch name and `displayname.ApplyAll` for the display name). The
  skills generate slug hints and display names from issue context; the CLI does
  not call an LLM for naming.

## Be Careful

- fanout assumes the dmux TUI is on the pane-list view. It cannot navigate
  arbitrary modals, editors, or confirmation prompts.
- Popup interception depends on dmux internals: popup script names under
  `dist/components/popups/*Popup.js`, result JSON shaped as
  `{"success":true,"data":...}`, and result files named
  `<tmpdir>/dmux-popup-<digits>.json`.
- `pgrep -f` can match a popup process tree. Keep `popup.FindNew`'s
  `ps -o comm=` Node-process filter so killing the wrong wrapper process does
  not orphan a stale popup.
- Agent names must match dmux's enabled agent names exactly. A misspelled
  injected name makes pane creation fail without a useful dmux-side message.
