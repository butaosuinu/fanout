# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

## Project Shape

`fanout` is a Go CLI (`cmd/fanout` + `internal/`). `make install` builds it
(`go build ./cmd/fanout`) and places it at `$(BINDIR)/fanout`. `make build-go`
produces the local `./fanout-go` binary the tests exercise; `make test` runs
the Go unit tests plus the bats black-box suite against that binary via
`FANOUT_BIN`; `make lint` is pinned golangci-lint v2 (`.golangci-lint-version`,
config `.golangci.yml`) + shellcheck of the test shims. `make fmt` formats
(gofumpt/goimports), `make fix` runs `go fix` idiom updates (run `make test`
after applying), and `make vuln` runs govulncheck (network; deliberately not
part of `lint`).

The Claude Code integration files (`claude/commands/fanout.md` slash command
and `claude/skills/fanout/SKILL.md` skill) and Codex CLI integration file
(`codex/skills/fanout/SKILL.md`) are bundled in the repo as the source of
truth. `make install` places them under `~/.claude/` and `~/.codex/`. Do not
edit installed copies directly.

The user-facing surface is in `README.md` and `README.ja.md`. Read those before
changing behavior; this file covers repo-local architecture and maintenance
notes.

## Working With fanout

Build the binary with `make build-go` and validate with `make test`.

- Open the console: `make build-go`, then `./fanout-go`. From a plain shell it
  creates or attaches the repository's fanout-managed tmux session; from inside
  tmux it uses the current pane.
- Batch-create child panes: `./fanout-go <parent-issue> --agent claude` from
  inside tmux.
- Verify changes without creating worktrees or panes:
  `./fanout-go <parent-issue> --agent claude --dry-run`.
- Settings (`--auto-pr` / `--no-auto-pr`, `--pr-review-gate` /
  `--no-pr-review-gate`, `--briefing-code-review` /
  `--no-briefing-code-review`, `--agent-teams-hint` /
  `--no-agent-teams-hint`, `--pr-visualization` /
  `--no-pr-visualization`, plus `.fanout/config.json`, user config, and
  `FANOUT_*` env vars) control or reserve generated child briefing switches.
- Black-box tests: `make test` builds `./fanout-go` and runs Go tests plus
  Tier 1 flags/prereqs and Tier 2 dry-run/status goldens. Regenerate Tier 2
  goldens with `FANOUT_GOLDEN_UPDATE=1 make test-tier2` after intentional
  output changes.
- A live end-to-end test needs tmux, an installed agent CLI, and a real GitHub
  parent issue or Project with OPEN child issues.
- Cutting a release: see `RELEASE.md`. Version strings are injected from tags
  via ldflags; no source edit is needed for version bumps.

## Architecture Notes

- `cmd/fanout/main.go` handles parse dispatch, dependency checks, runtime
  resolution, child loading, state loading/locking, and the fail-fast
  `executePlan` loop.
- `cmd/fanout/pane.go` is the creation orchestration: briefing render, naming,
  worktree planning/preparation, tmux split/title/layout, state recording,
  metadata write, and agent launch.
- `cmd/fanout/status.go` reads `.fanout/state.json` and queries GitHub PR state.
  `cmd/fanout/lifecycle.go` implements `--close`, `--merge`, and `--cleanup`
  against recorded state rows.
- `cmd/fanout/dashboard.go` implements the `dashboard` subcommand (dispatched
  before `cliflags.Parse`, like `update`): it starts the read-only web
  dashboard, handles reuse-if-running, token generation, browser open, and
  registers the `prefix + D` tmux keybinding (`bindDashboardKey`, also called
  after a live `executePlan`).
- `cmd/fanout/tui.go` implements the no-argument persistent TUI console. Plain
  shells are relaunched into a deterministic fanout-managed tmux session;
  invocations already inside tmux use the current pane.
- `internal/runtime` resolves the git repository root and the tmux target.
  Batch pane-creation mode must be invoked from inside tmux. By default fanout
  targets the invoking pane; `--session` targets a named tmux session.
- `internal/worktree` owns base branch resolution, refresh, local exclude
  setup, and `git worktree add` under `.fanout/worktrees/<slug>/`.
- `internal/tmuxrun` owns direct tmux operations:
  `split-window -d -h -P -F '#{pane_id}'`, pane titles, tiled layout, agent
  command send, best-effort pane kill during cleanup, `ListPaneIDs` (liveness
  for the dashboard), and `BindDashboardKey`.
- `internal/sessionview` is the shared read-only data layer: it aggregates
  `.fanout/state.json` + tmux liveness + GitHub PR state into a `Snapshot`
  grouped by parent ("Session"). IO is injected via `Collectors` so it is pure
  and unit-testable; both the web dashboard (now) and a future TUI consume the
  same `Build`.
- `internal/dashboard` is the localhost web server: `server.go` (GET-only mux,
  token middleware, SSE), `poller.go` (two-tier state/tmux + throttled gh
  refresh, broadcast on change), `sse.go` (channel hub), `runfile.go`
  (`.fanout/dashboard.json` reuse-if-running), and an embedded `static/` SPA.
- `internal/agent` maps supported agents (`claude`, `codex`) to launch
  commands and validates installed CLIs for live mode.
- `internal/state` owns `.fanout/state.json` plus `.fanout/state.json.lock`.
  The coarse lock covers planning and launching so two fanout invocations do
  not race on the same `(parent, issueNum)` idempotency key.
- `internal/naming` deterministically generates slugs and branch names.
  `--name` may override slug, display name, and branch. The skills generate
  these flags from issue context; the CLI does not call an LLM.
- `internal/ghissue`, `internal/blockers`, `internal/briefing`,
  `internal/settings`, `internal/displayname`, `internal/atomicfs`,
  `internal/log`, `internal/tty`, and `internal/exitcode` hold the remaining
  reusable pieces.

## Behavior Boundaries

- Child enumeration unions GitHub Sub-issues and same-repo parent task-list
  rows. Project mode uses Project items instead. Prose scanning (`Closes #N`,
  `Depends on #N`, Japanese child-reference idioms) belongs in the Claude/Codex
  skills, which forward accepted candidates through `--include`.
- `--unblocked-only` parses blockers from the child body's `## Blocked by`
  section, the parent task-list row trailer `(blocked by #X, #Y)`, and the
  `blocked` label as a weak signal.
- `--status`, `--close`, `--merge`, and `--cleanup` do not inspect old pane
  prompts or external config. They operate on `.fanout/state.json` or
  `FANOUT_STATE_PATH`.
- `fanout dashboard --web` is the one HTTP surface, and it is deliberately
  carved out: a read-only, `127.0.0.1`-bound, GET-only, token-gated localhost
  server that only ever reads state/tmux/gh and never mutates repo or GitHub
  state. The "no HTTP/sockets" guidance elsewhere is about the legacy
  notification path (outbound only); #137/#142 explicitly delegated the Web UI
  decision to dashboard #117, which this implements standalone (no TUI
  dependency — the future TUI just reuses `internal/sessionview`). Keep it
  read-only: do not add mutation endpoints.
- `.fanout/worktrees/<slug>/` directories without a state row are treated as an
  action-mode migration fallback and skipped when their slug matches the child
  this run would create.
- `--sleep` is a rate-limit between successful child launches. It is not a
  retry/backoff knob.

## Things To Be Careful With

- Worktree refresh must preserve user work. If a local base branch is dirty,
  ahead, or diverged, fail rather than forcing it.
- Keep the public TUI entrypoint as no-argument `fanout`; do not reintroduce a
  user-facing `fanout tui` compatibility path.
- Keep the state lock close to live launch behavior. Moving exclude setup or
  lock acquisition can leave dirty `.fanout/state.json.lock` artifacts or
  reintroduce launch races.
- `tmux split-window -P` returns the new pane id synchronously; do not add
  polling around pane creation unless a future tmux path stops returning an id.
- Preserve fail-fast behavior in `executePlan`: stop after the first failed
  child launch.
- When changing dry-run output, update and inspect Tier 2 goldens before
  committing.
- `gh pr create` is gated by the repo's `PreToolUse(Bash)` hook registered in
  `.claude/settings.json`. Run `/post-work-review` before creating a PR, or use
  `FANOUT_SKIP_PR_REVIEW=1` only when the documented escape hatch is intended.
