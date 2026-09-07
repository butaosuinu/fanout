# CLAUDE.md

Guidance for Claude Code in this repository. Read `README.md` / `README.ja.md`
before changing behavior; `docs/architecture.ja.md` is the architecture canon
(package table, dependency diagram, review classes, invariant catalog). This file
holds what every session needs; path-scoped detail lives in `.claude/rules/`.

## Project shape

`fanout` is a Go CLI (`cmd/fanout` + `internal/`) plus a dashboard web UI
(`web/`, React + Vite + TypeScript, pnpm). `make install` builds it into
`$(BINDIR)/fanout` and installs the agent integration files described below.

- `make build-go` produces the local `./fanout-go` the tests exercise, running
  `make build-web` first to bundle `web/` into `internal/ui/dashboard/static/`
  for `go:embed`. The bundle is never committed (only `static/.gitkeep` is);
  `//go:embed all:static` keeps a bundle-less checkout compiling without Node.
  Build through `make build-go`, not raw `go build`, or the bundle goes stale.
- `make test` = Go unit tests + web vitest (`make test-web`) + the bats
  black-box suite against `FANOUT_BIN`. `make lint` = pinned golangci-lint v2
  (`.golangci-lint-version`, `.golangci.yml`) + shellcheck of the test shims,
  Node-free on purpose. `make lint-web` = oxlint + oxfmt `--check` + tsc.
- `make check` is the canonical full local gate: `test`, `lint`, `lint-web`.
- `make fmt` (gofumpt/goimports); `make fmt-web` (oxfmt on `web/src` and
  `vite.config.ts`; CSS and top-level web JSON are out of scope); `make fix`
  (`go fix`, then run `make test`).
- `make vuln` (govulncheck, network) and `make complexity` (what the branch adds
  against its merge base) stay outside `check` on purpose; see Complexity budget.

`claude/commands/*.md`, `claude/skills/*/SKILL.md`, and `codex/skills/*/` are
the source of truth for the agent integrations; `make install` copies them under
`~/.claude/` and `~/.codex/`. Edit the repo copies, never the installed ones. The
checksum-verified release installer alone owns Codex `post-work-review`.

## Working with fanout

- Console: `make build-go`, then `./fanout-go`. From a plain shell it creates or
  attaches the repository's fanout-managed tmux session; inside tmux it uses the
  current pane. No-argument `fanout` is the only TUI entrypoint (no `fanout tui`).
- Batch children: `./fanout-go <parent-issue> --agent claude` from inside tmux.
  `--agent NUM=name` overrides one child (`--agent task-id=name` for
  `fanout plan`). Supported agents: `claude`, `codex`, `opencode`. `--dry-run`
  previews either lane (`<parent-issue>` or `plan <spec.json|plan-slug>`)
  without creating worktrees or panes.
- Settings flags (`--auto-pr`, `--pr-review-gate`, `--briefing-code-review`,
  `--agent-teams-hint`, `--pr-visualization`, each with a `--no-` form), plus
  `.fanout/config.json`, user config, and `FANOUT_*` env vars, control the child
  briefing and the dashboard keybinding. Lifecycle hooks come from user `hooks.json`.
- Black-box tiers: Tier 1 covers flags and prereqs, Tier 2 is dry-run/status
  goldens. After an intentional output change, regenerate with
  `FANOUT_GOLDEN_UPDATE=1 make test-tier2` and read the diff; plan changes
  usually touch both dry-run and status fixtures (`scenario-plan-*`). A live
  end-to-end run needs tmux, an installed agent CLI, and a real GitHub parent
  issue or Project with OPEN children.
- Releases: `RELEASE.md`; versions are injected from tags via ldflags
  (`-X main.version`), so a bump needs no source edit.

## Architecture

`internal/` has four layers: `core` (pure logic, no process/network/FS/DB),
`app` (use-case orchestration), `infra` (external process/FS/DB), and `ui` (TUI
and web dashboard). Imports flow core -> core; app -> core/app/infra; infra ->
core/infra; ui -> all four; nothing imports the composition root `cmd/fanout`.
`internal/arch` enforces this inside `go test` through godep-cruiser rules
(`internal/arch/godep-cruiser.json`; exceptions in `godep-cruiser-baseline.json`
expire as stale errors; depguard is off on purpose). Weakening `internal/arch`
disables every layer guard, and a godep-cruiser version bump changes the guard
even though the diff only touches `go.mod`.

Every package carries a review class: H needs human review, M is mixed, A can
rely on AI review. The class table is `docs/architecture.ja.md`, mirrored in
`tools/reviewrisk/rules.go` (a docsync test fails CI when they diverge) and
turned into a `review:<level>` judgment per PR by `make review-risk`
(`docs/review-risk.ja.md`). Changing a class means editing `rules.go` in the
same PR. `tools/` stays stdlib-only so repo-support code never leaks into the
product.

Runtime differences are capabilities, never a backend-name `switch`.
`internal/core/backend` holds the ports (`Backend`, the `As*` capabilities,
`MutationModel`); `infra/paneruntime` alone constructs a concrete adapter.
`internal/app` and `cmd/fanout` name only the core types and, by
`TestRuntimeVocabulary`, never spell `tmux` or `herdr` in an identifier, import
path, file name, or struct tag; reviewed exceptions sit in
`internal/arch/runtime-vocabulary-allow.json` with a reason.

Agent-state telemetry is the six-value contract `running` / `working` / `plan`
/ `blocked` / `idle` / `done` on the `@fanout_agent_state` pane option,
normalized in `internal/app/sessionview`. `internal/infra/tmuxrun` writes only
`running` / `done`; claude panes are refined by the `--settings` hooks
`internal/core/agent` injects; codexapp's Plan Mode controller reports
`working` / `plan` only on the `thread/settings/update` fallback path, and its
team bridge reports `working` / `idle` / `blocked` across the whole bridged
session. Push lanes never write to pane input except `fanout msg nudge`, which
sends only when the peer's state is `running` / `working` / `plan` / `idle`
(never `blocked`: its focused dialog could take the Enter). Details:
`docs/session-messaging-push.ja.md`.

## Behavior boundaries

- Child enumeration unions GitHub Sub-issues and same-repo parent task-list
  rows; Project mode uses Project items. Prose scanning (`Closes #N`, Japanese
  child idioms) belongs to the skills, which forward candidates via `--include`.
- `fanout plan` is a separate issue-less lane: it does not reuse the issue-mode
  enumeration path, never invents GitHub issue numbers, and keys task selection
  by task id. Dependencies are local `blocked_by` ids, complete when a PR on the
  task branch merges. Branch-derived PR lookups stay aligned across `--status`,
  `--cleanup`, and `--unblocked-only`; a change to branch generation, task
  `branch` overrides, or `--branch-prefix` updates the plan fixtures and docs.
- `--unblocked-only` reads blockers from the child's `## Blocked by` section,
  the parent row trailer `(blocked by #X, #Y)`, and the `blocked` label (weak).
- `--status`, `--close`, `--merge`, and `--cleanup` read `.fanout/state.json`
  (or `FANOUT_STATE_PATH`) only, never old pane prompts or external config. Plan
  variants load the spec, then use `plan:<slug>` rows.
- `fanout dashboard --web` is the one HTTP surface: `127.0.0.1`-bound,
  token-gated, GET-only apart from two mutations scoped to one PR each
  (`POST /api/pr/merge`, `POST /api/pr/delete-branch`); `/api/peek` and
  `/api/plan` are a read-only `capture-pane`, and `/api/plan` answers only for
  plan-mode panes whose recorded agent is `codex`. Google Fonts is the SPA's
  single external fetch, loaded `no-referrer` because the tokened URL carries
  merge authority. The mutation invariants are `docs/dashboard-pr-mutations.ja.md`;
  adding a mutation, widening one, or relaxing its gates needs human review.
- The label watcher is a TUI-resident, opt-in launcher, not a cron/webhook
  service and not the #107 skill loop. Only user config or env can enable it;
  repo config may set labels, interval, agent, and max sessions. Issue bodies
  become briefings, so trigger labels are a prompt-injection boundary.
- `.fanout/worktrees/<slug>/` without a state row is a migration fallback and
  is skipped when its slug matches the child this run would create.
- `--sleep` rate-limits successful child launches; it is not retry/backoff.

## Task scope

The request sets the scope, and the scope is the deliverable. A bug or cleanup
you notice nearby is a follow-up to report in your summary, not a change to
make, unless the requested behavior cannot work without it. Commit tests only
where the task asks for them or neighboring files already keep tests for that
kind of change, sized like those neighbors; scratch checks stay out of the repo.
Report outcomes against this session's tool results: a failed check or a skipped
step is stated, not smoothed over.

## Quality gates

Run focused tests and linters while editing. Once the candidate is committed,
`/post-work-review` owns the one `make check` run for that exact HEAD; do not
add separate full `make lint`, `make test`, or `make lint-web` runs. Then walk
`docs/review-checklist.ja.md`; the same findings recur. Two `PreToolUse(Bash)`
hooks in `.claude/settings.json` gate the way out:

- `git push` to a branch (`scripts/agent-push-gate.sh`; Codex runs it via
  `.codex/hooks.json`) requires the pushed tip to equal the per-worktree marker
  `$(git rev-parse --git-dir)/fanout-check-passed`, which only a successful
  `make check` on a clean tree writes. The gate sees only the pre-execution
  state, so a push chained after anything that can move a ref
  (`git commit … && git push`, a rebase, even `git fetch … && git push`) is
  always denied. On a deny: commit, run `make check`, then push, each as its own
  command. `gh pr create` pushes an unpushed branch itself, so it needs the same
  marker. Branch deletions and tag pushes are ungated; `bash -c '… git push …'`
  and `--mirror` fail closed. Escape hatch: `FANOUT_SKIP_PUSH_CHECK=1`.
- `gh pr create` (`.claude/hooks/pre-pr-review-gate.sh`) requires a completed
  `/post-work-review` (`make check` passed, marker matches HEAD), a standalone
  command with no `cd` / `pushd` / `env --chdir` and no ref-mutating command
  chained in, and the default branch as base. Retrying a denied command
  unchanged never succeeds; fix the stated cause. Escape: `FANOUT_SKIP_PR_REVIEW=1`.

Two `PostToolUse` hooks act on edits: `scripts/agent-format-on-edit.sh` runs the
per-file `golangci-lint fmt` / `oxfmt` fast paths; `scripts/agent-complexity-on-edit.sh`
sends an edit back (exit 2) when lines this branch changed exceed the budget below,
degrades to advice after three blocks on one file, and fails open when a tool is
missing. Escape hatch: `FANOUT_SKIP_COMPLEXITY=1`.

## Complexity budget

New code has a complexity budget (cognitive first, cyclomatic second, nesting
depth capped separately). The numbers live only in `.golangci-complexity.yml`
and `web/tools/complexity/eslint.config.js`; the hook, `make complexity`, and CI
read them. Over budget: flatten with a guard clause, extract a helper that means
something on its own, replace a branch pile with a table, or split a React
component and lift logic into a hook. Splitting a function purely to get under a
number defeats the measurement. A suppression needs a reason:
`//nolint:gocognit // <why>`, or `-- <why>` on the eslint-disable comment. Only
lines past the merge base are judged, and complexity stays outside `make check`
because 10% of existing Go functions are over budget. Rationale:
`docs/complexity.ja.md`.

## Conventions

- Go table-driven tests: `.claude/rules/go-tests.md` loads when you edit a
  `*_test.go`; `internal/infra/team/detect_test.go` is the model.
- Error wrapping: `errs.Wrap` is the shared `defer`-based wrapper every layer
  may import; conventions in `docs/error-handling.ja.md`.
- User-facing docs (`README*.md`, `RELEASE.md`, `docs/**`, `site/content/docs/**`):
  `docs/doc-style.ja.md`, summarized in `.claude/rules/docs-style.md`. Keep EN/JA
  pairs in sync.

## Fragile spots

- Worktree refresh preserves user work: if the local base branch is dirty,
  ahead, or diverged, fail instead of forcing it.
- The state lock covers planning and launch, including plan spec copying and
  `(plan:<slug>, taskId)` idempotency. Moving exclude setup or lock acquisition
  leaves dirty `.fanout/state.json.lock` files or reintroduces launch races.
- `tmux split-window -P` returns the new pane id synchronously, so pane creation
  needs no polling. `executePlan` stops after the first failed child launch.
- The embedded-asset Go tests skip when the web bundle is absent; CI's go-unit
  job runs `make build-web` first, so keep that wiring in `.github/workflows/test.yml`.
