# AGENTS.md

This file provides guidance to Codex CLI when working with code in this
repository.

## Project Shape

`fanout` is a Go CLI (`cmd/fanout` + `internal/`); `make install` builds it
(`go build ./cmd/fanout`) and installs it at `$(BINDIR)/fanout`. `make build-go`
produces the local `./fanout-go` binary; `make test` runs the Go unit tests plus
the bats black-box suite against it (via `FANOUT_BIN`); `make lint` is the
pinned golangci-lint v2 (`.golangci-lint-version`, config `.golangci.yml`) plus
a shellcheck of the test shims. `make fmt` formats (gofumpt/goimports),
`make fix` runs `go fix` idiom updates (run `make test` after applying), and
`make vuln` runs govulncheck.

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

`fanout` is a standalone git worktree + tmux pane + agent launcher. Build with
`make build-go` (output `./fanout-go`) and validate with `make test`.

- Open the TUI console: `make build-go`, then `./fanout-go`. From a plain
  shell it creates or attaches the repository's fanout-managed tmux session;
  from inside tmux it uses the current pane.
- Batch-create child panes: `./fanout-go <parent-issue> --agent claude` from
  inside tmux.
- Validate logic without creating worktrees or panes by appending `--dry-run`,
  e.g. `./fanout-go <parent-issue> --agent claude --dry-run`.
- Lint with `make lint` (pinned golangci-lint v2 + a shellcheck of the bats
  test shims). Treat shellcheck quoting warnings on the shims as real.
- Run `make test` (Go unit tests + Tier 1/Tier 2 bats against `./fanout-go`)
  before relying on a change.
- A live end-to-end test requires tmux, an agent CLI, and a real GitHub parent
  issue or Project with OPEN child issues. There is no mock layer.

## Lint / Format Conventions

- Discard an error on purpose with `_ =` plus a comment stating why it is safe
  to ignore; never leave a call silently unchecked.
- `make lint` is the source of truth. Editor integrations may run a different
  golangci-lint version; the pinned version (`.golangci-lint-version`) wins.
- To bump golangci-lint, edit `.golangci-lint-version` (the Makefile and the
  CI lint job both read it) and fix any new findings in the same PR.
- Run `git config blame.ignoreRevsFile .git-blame-ignore-revs` once per clone
  so bulk-formatting commits stay out of `git blame`.

## Architecture Notes

The package map: `cmd/fanout` is the command flow (`main.go` dispatch and
`executePlan`, `tui.go` no-argument console launch, `pane.go` creation
orchestration, `plan.go` filtering, `status.go`, `lifecycle.go`, `report.go`).
`internal/` holds `agent`,
`cliflags`, `ghissue`, `runtime`, `worktree`, `tmuxrun`, `state`, `naming`,
`blockers`, `displayname`, `briefing`, plus `atomicfs`/`log`/`tty`/`exitcode`.

- Runtime discovery (`internal/runtime`) resolves the git repo root with
  `git rev-parse --show-toplevel`, requires the caller to be inside tmux for
  batch pane creation, and targets the invoking pane (`$TMUX_PANE`) unless
  `--session` is supplied. No-argument TUI mode can start from a plain shell by
  creating or attaching a fanout-managed tmux session for the repository.
- Pane creation is direct: `worktree.Prepare` creates
  `.fanout/worktrees/<slug>/`, `tmuxrun.SplitPaneWithAgentCommand` runs
  `tmux split-window -d -h -P -F '#{pane_id}'`, and
  `tmuxrun.BuildPaneLaunchCommand` launches the selected agent through a POSIX
  wrapper while leaving the user's shell after exit.
- `--agent` or `FANOUT_AGENT` is required. `internal/agent` validates supported
  agent names and, in live mode, verifies the corresponding CLI is installed.
- Idempotency is stored in `.fanout/state.json` under `(parent, issueNum)`.
  Live runs hold `.fanout/state.json.lock` while planning and launching so
  parallel invocations cannot create the same child twice.
- Keep prompts one line (`oneLinePrompt`). Full issue context belongs in
  `/tmp/fanout-<repo>-<NUM>.md` (`internal/briefing`).
- `--include` widens the child set; `--only` and `--skip` narrow it
  (`filterOnlySkip`; child enumeration in `mergeExtraChildren` via
  `internal/ghissue`). Prose scanning for implicit children lives in the
  Claude/Codex skills, not in the CLI.
- `--unblocked-only` filters out issues with OPEN blockers (`splitBlocked` +
  `internal/blockers`); `--limit` overflow lands in `Plan.LimitDeferred`.
- `--name` threads caller-generated names through
  (`cliflags.parseNameArg` -> `NameOverride`). Slug hints become deterministic
  worktree slugs, display names become tmux pane titles, and branch overrides
  bypass the generated `branchPrefix + slug` name. The skills generate these
  from issue context; the CLI does not call an LLM for naming.
- `--status`, `--close`, `--merge`, and `--cleanup` operate from
  `.fanout/state.json`; set `FANOUT_STATE_PATH` to point at a specific state
  file outside the repository checkout.

## Be Careful

- fanout must run inside tmux for batch pane-creation mode. No-argument TUI
  mode may start from a plain shell. `--status` and lifecycle commands do not
  require a live tmux pane unless they need to kill a recorded pane during
  cleanup.
- Worktree creation refreshes the base branch by default. If a checked-out base
  branch is dirty or diverged, `worktree.Prepare` should fail rather than
  clobbering local work.
- The `.fanout/state.json` lock deliberately covers planning and launching, not
  just the final write. Do not narrow the critical section without adding a
  race-safe replacement for `(parent, issueNum)` idempotency.
- When changing dry-run output, update Tier 2 goldens with
  `FANOUT_GOLDEN_UPDATE=1 make test-tier2` and review the diff.
