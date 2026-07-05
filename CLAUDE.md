# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

## Project Shape

`fanout` is a Go CLI (`cmd/fanout` + `internal/`) plus a dashboard web UI
(`web/`, React + Vite + TypeScript, pnpm). `make install` builds it and places
it at `$(BINDIR)/fanout`. `make build-go` produces the local `./fanout-go`
binary the tests exercise; it depends on `make build-web`, which bundles
`web/` into `internal/ui/dashboard/static/` for `go:embed` — the bundle is never
committed (only `static/.gitkeep` is tracked; `//go:embed all:static` keeps a
bundle-less checkout compiling without Node, serving a fallback page). `make
test` runs the Go unit tests, the web UI vitest suite (`make test-web`), and
the bats black-box suite against the binary via `FANOUT_BIN`; `make lint` is
pinned golangci-lint v2 (`.golangci-lint-version`, config `.golangci.yml`) +
shellcheck of the test shims (Node-free on purpose; the web lint is
`make lint-web` = oxlint + oxfmt `--check` + tsc, configs `web/.oxlintrc.json`
/ `web/.oxfmtrc.json`). `make fmt` formats Go (gofumpt/goimports),
`make fmt-web` formats `web/src` + `vite.config.ts` (oxfmt, printWidth 100; CSS と web/ 直下の JSON は対象外), `make fix` runs
`go fix` idiom updates (run `make test` after applying), and `make vuln` runs
govulncheck (network; deliberately not part of `lint`).

The Claude Code integration files (`claude/commands/*.md` slash commands and
`claude/skills/*/SKILL.md` skills) and Codex CLI integration files
(`codex/skills/*/SKILL.md`) are bundled in the repo as the source of truth.
`make install` places them under `~/.claude/` and `~/.codex/`. Do not edit
installed copies directly.

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
- Override one child issue's agent with repeatable `--agent NUM=name`; for
  `fanout plan`, use `--agent task-id=name`. Supported agents remain
  `claude` and `codex`.
- Verify changes without creating worktrees or panes:
  `./fanout-go <parent-issue> --agent claude --dry-run`.
- Verify issue-less plan tasks without creating worktrees or panes:
  `./fanout-go plan <spec.json|plan-slug> --agent claude --dry-run`.
- Settings (`--auto-pr` / `--no-auto-pr`, `--pr-review-gate` /
  `--no-pr-review-gate`, `--briefing-code-review` /
  `--no-briefing-code-review`, `--agent-teams-hint` /
  `--no-agent-teams-hint`, `--pr-visualization` /
  `--no-pr-visualization`, plus `.fanout/config.json`, user config, and
  `FANOUT_*` env vars) control generated child briefing switches and the
  dashboard keybinding. Lifecycle hooks are always on and come from user
  `hooks.json`.
- Black-box tests: `make test` builds `./fanout-go` and runs Go tests plus
  Tier 1 flags/prereqs and Tier 2 dry-run/status goldens. Regenerate Tier 2
  goldens with `FANOUT_GOLDEN_UPDATE=1 make test-tier2` after intentional
  output changes.
- A live end-to-end test needs tmux, an installed agent CLI, and a real GitHub
  parent issue or Project with OPEN child issues.
- Cutting a release: see `RELEASE.md`. Version strings are injected from tags
  via ldflags; no source edit is needed for version bumps.

## Architecture Notes

`internal/` is a 4-layer architecture: `core` (pure logic, no process/network/
FS/DB), `app` (use-case orchestration), `infra` (external process/FS/DB), and
`ui` (TUI + web dashboard). Allowed imports: core -> core only; app ->
core/app/infra; infra -> core/infra; ui -> all four; `cmd/fanout` is the
composition root and no package may import `cmd/...`. `internal/arch` enforces
the direction and a core stdlib-purity denylist in CI (depguard is off on
purpose). Canonical reference, the full package table, the Mermaid dependency
diagram, and the PR-review-weight classes (H/M/A) live in
`docs/architecture.ja.md`.

- `cmd/fanout` is the composition root and CLI boundary: `main.go` (the
  first-match-wins dispatch table, ldflags `version`/`commit` — class H),
  `plancmd.go` (`fanout plan` flag parsing/validation; execution lives in
  `app/run`), `status.go` / `lifecycle.go` / `msg.go` (thin dispatch into
  `app/statusreport`, `app/lifecycle`, `app/peermsg`), `dashboard.go`,
  `tui*.go` (the no-argument persistent TUI console wiring: `tui_issue.go`
  issue-mode popup, `tui_launch.go` manual/plan/attach/shell launch — the
  prompt-mode plan fan-out launches one coordinator pane at the project root
  running the fanout-plan skill so `fanout plan`'s git root stays at the repo,
  never Codex Plan Mode), and `tui_popup.go` (self-exec popup subcommands).
  `main.go` / `tui_popup.go` / `tui_launch.go` / `worktree_action.go` /
  `codex_plan_tui.go` are class H; the remaining cmd files (flag validation
  and thin dispatch into app) are class M.
- `internal/core` is pure logic with no process/network/FS/DB access:
  `agent` (supported agent names, CLI validation for live mode — the only
  core packages allowed `os`/`os/exec`), `planspec` (the `fanout plan` JSON
  schema), `naming` (deterministic slug/branch generation), and the
  AI-reviewable `blockers`/`exitcode`/`parentref`/`fanset`/`cliview`.
- `internal/app` orchestrates use cases on top of `core` and `infra`:
  `panelaunch` (pane creation), `lifecycle`, `watch` (the label-watcher
  cycle, pure at the package boundary via `watch.IO`), and `briefing` (the
  prompt text injected into agents) are class H; `sessionview` (the read-only
  `Snapshot` aggregator shared by the web dashboard and a future TUI),
  `panelayout`, `run`, `statusreport`, and `peermsg` are class M; `cliflags`
  is class A.
- `internal/infra` talks to external processes, the filesystem, and the
  team SQLite bus: `state` (`.fanout/state.json` + its lock), `worktree`
  (`git worktree add` under `.fanout/worktrees/<slug>/`), `hooks`,
  `selfupdate`, and `team` (the `--team`/`fanout msg` per-parent SQLite bus:
  `modernc.org/sqlite`, WAL mode, file mode `0600`, DB scoped to
  `/tmp/fanout-<repo>-<parent_key>.db` with `FANOUT_DB_PATH` override; pane
  identity resolves from `.fanout/state.json` with the `[fanout #N of #P]`
  prompt prefix as fallback) are class H; `ghissue`,
  `gitstat`, `tmuxrun` (direct tmux operations), `msgstore`, `notify`,
  `runtime` (git root + tmux target resolution), `settings`, `displayname`,
  and `codexapp` are class M; `atomicfs`, `log`, `tty`, `execx`, `gitroot`,
  and `browser` are class A.
- `internal/ui` holds the TUI (`tui`) and the web dashboard (`dashboard`):
  `server.go` (GET-only mux, token middleware) and `runfile.go` (the tokened
  `.fanout/dashboard.json` reuse/trust gate) are class H; `poller.go`,
  `peek.go` (`GET /api/peek`), `plan.go` (`GET /api/plan`), `sse.go`, and
  `embed.go` are class M. In `tui`, `actions.go` (lifecycle close/merge/
  cleanup wiring and confirmation flow) is class H, rendering/formatting
  (`view.go` / `paneview.go` / `compact.go` / `styles.go`) is class A, and
  the remaining key/form/polling wiring is class M. The dashboard SPA lives
  in `web/` (React + Vite + TS) and bundles into
  `internal/ui/dashboard/static/` via `go:embed`.

The full package table, the Mermaid dependency diagram, the human-must-read
invariant catalog, and the burn-down list of known layering debt are the
canonical reference in `docs/architecture.ja.md`. Rule of thumb: a PR that
touches a class-H package needs human review; a PR touching only class-A
packages can rely on AI review.

## Behavior Boundaries

- Child enumeration unions GitHub Sub-issues and same-repo parent task-list
  rows. Project mode uses Project items instead. Prose scanning (`Closes #N`,
  `Depends on #N`, Japanese child-reference idioms) belongs in the Claude/Codex
  skills, which forward accepted candidates through `--include`.
- `fanout plan` is a separate issue-less lane. It must not overload the
  issue-mode `Plan` / child enumeration path, must not invent GitHub issue
  numbers, and must keep task selection keyed by task IDs. Task dependencies
  are local `blocked_by` IDs whose completion is inferred from merged PRs on
  task branches.
- `--unblocked-only` parses blockers from the child body's `## Blocked by`
  section, the parent task-list row trailer `(blocked by #X, #Y)`, and the
  `blocked` label as a weak signal.
- `--status`, `--close`, `--merge`, and `--cleanup` do not inspect old pane
  prompts or external config. They operate on `.fanout/state.json` or
  `FANOUT_STATE_PATH`. The plan variants load the spec first and then operate
  on `plan:<slug>` task rows.
- `fanout dashboard --web` is the one HTTP surface, and it is deliberately
  carved out: a read-only, `127.0.0.1`-bound, GET-only, token-gated localhost
  server that only ever reads state/tmux/gh and never mutates repo or GitHub
  state. `GET /api/peek` and `GET /api/plan` stay inside that boundary (both
  are a read-only `tmux capture-pane` of a recorded pane; `/api/plan` is
  further gated to panes recorded with `codexPlanMode`), and Google Fonts is
  the single allowed external fetch from the SPA (loaded `no-referrer` so the
  tokened URL never leaks). The "no HTTP/sockets" guidance elsewhere is about the legacy
  notification path (outbound only); #137/#142 explicitly delegated the Web UI
  decision to dashboard #117, which this implements standalone (no TUI
  dependency — the future TUI just reuses `internal/app/sessionview`). Keep it
  read-only: do not add mutation endpoints.
- The label watcher is a TUI-resident, opt-in launcher, not a cron/webhook
  service and not the #107 skill loop. Only user config or environment
  variables may enable `watcher`; repo config may set labels, interval, agent,
  and max sessions but cannot opt a checkout into launching. Its scope is
  repository-wide label discovery (`fanout:auto` -> `fanout:running`) and
  one-shot session launch. #107 remains the skill-led loop for revisiting
  children under a known parent. Because issue bodies become child briefings,
  trigger labels are a prompt-injection boundary.
- `.fanout/worktrees/<slug>/` directories without a state row are treated as an
  action-mode migration fallback and skipped when their slug matches the child
  this run would create.
- `--sleep` is a rate-limit between successful child launches. It is not a
  retry/backoff knob.

## Things To Be Careful With

- Worktree refresh must preserve user work. If a local base branch is dirty,
  ahead, or diverged, fail rather than forcing it.
- `go:embed` snapshots whatever is on disk at build time: after editing
  `web/src`, build via `make build-go` (not raw `go build`) or the binary
  ships a stale bundle. The embedded-asset Go tests skip when the bundle is
  absent — CI's go-unit job runs `make build-web` first so they actually run;
  keep that wiring when touching `.github/workflows/test.yml`.
- Keep the public TUI entrypoint as no-argument `fanout`; do not reintroduce a
  user-facing `fanout tui` compatibility path.
- Keep the state lock close to live launch behavior. Moving exclude setup or
  lock acquisition can leave dirty `.fanout/state.json.lock` artifacts or
  reintroduce launch races. Plan live runs also rely on the same lock while
  copying specs and checking `(plan:<slug>, taskId)` idempotency.
- `tmux split-window -P` returns the new pane id synchronously; do not add
  polling around pane creation unless a future tmux path stops returning an id.
- Preserve fail-fast behavior in `executePlan`: stop after the first failed
  child launch.
- When changing dry-run output, update and inspect Tier 2 goldens before
  committing. Plan changes usually touch both dry-run and status fixtures /
  goldens (`scenario-plan-*`); regenerate with `FANOUT_GOLDEN_UPDATE=1 make
  test-tier2` and review the exact diff.
- Keep plan branch-derived PR lookups aligned across status, cleanup, and
  `--unblocked-only`. If branch generation, task `branch` overrides, or
  `--branch-prefix` behavior changes, update the plan status fixtures and
  docs together.
- Run `make lint` and `make test` before creating a PR (`make lint-web` too
  when `web/` changed) — the top CI failures, Tier 2 golden drift and
  golangci-lint findings, all reproduce locally. Then walk
  `docs/review-checklist.ja.md`; the same review findings recur.
- `gh pr create` is gated by the repo's `PreToolUse(Bash)` hook registered in
  `.claude/settings.json`. Retrying a denied command with nothing changed
  never succeeds — fix the stated cause, then re-run it: complete
  `/post-work-review` (the marker must match HEAD), issue `gh pr create` as a
  standalone command with no `cd`/`pushd`/`env --chdir` chained in (any cwd
  inside the target worktree works), and keep the PR base at the default
  branch. `FANOUT_SKIP_PR_REVIEW=1` is only for the documented escape hatch.

## Test Conventions

Table-driven tests must be readable case-by-case from `go test -v` alone, not
just from the function name. `internal/infra/team/detect_test.go` is the model.

- Give every case a `name` field and wrap the loop in
  `t.Run(tt.name, func(t *testing.T) { ... })`. This makes each case a named
  subtest: `go test -run TestX/case_name` runs one case and failures report
  which case broke.
- `name` describes the behavior or the edge being pinned, not the input echoed
  back — `"trims surrounding whitespace"`, not `"  running  "`.
- Use field-named struct literals (`{name: ..., in: ..., want: ...}`) once a
  case struct has more than three or four fields. Positional literals past that
  are unreadable and break on every field addition.
- Do not key a case table with `map[...]`: iteration order is undefined and the
  keys cannot become subtest names. Use a slice with a `name` field.
- Keep failure messages in the `funcName(input) = got, want` form already used
  across the suite.
- Comment a case line only when its purpose is not obvious from the values
  (boundary, precedence, why this specific input). Do not annotate self-evident
  cases — the `name` already carries them. Preserve provenance comments on
  opaque golden values (e.g. `// captured from real tmux 3.6a`) and the one-line
  comment above a test that states what it guarantees.
- Leave existing loop-variable naming as-is (`cases`/`tc` and `tests`/`tt` both
  occur); do not churn files just to unify it. New tables prefer `tests`/`tt`.

## Documentation Writing

When writing or updating user-facing docs (`README*.md`, `site/content/docs/**`,
`RELEASE.md`, `docs/**` — not code comments or briefing text), suppress
AI-tell phrasing and verbosity. Full ruleset (EN banned-word tables + Japanese
AI-tell catalog + self-checks): `docs/doc-style.ja.md`. Core rules:

- **Match the house style.** Terse, imperative, active voice; define a term once
  and reuse it (no synonym cycling). Sentence case for subheadings. Read the
  neighboring doc and mirror its tone before writing.
- **Lead with the conclusion.** No warm-up openers ("In today's…", "まず最初に")
  and no restating the request. State the thing, then the detail.
- **Cut filler and hedging.** Drop "it's worth noting" / "note that" / 「重要な点
  として」/ 「なお、」連発. EN: `in order to`→`to`, `due to the fact that`→
  `because`, `utilize`/`leverage`→`use`. JA: 「〜することができます」→「〜できます」,
  「〜を行う」→動詞化, 「〜となっています」→「〜です」.
- **Avoid AI-tell vocabulary** (replace with plain words): delve, seamless,
  comprehensive*, game-changer, harness, foster, streamline, robust*, ecosystem*,
  embark, underscore … (*technically-precise uses are fine — the `docs`
  tolerance). JA tells: 機械翻訳調, 過剰な体言止め, 「魅力的な」「シームレスな」
  「〜していきましょう」.
- **No compulsive rule of three.** Use the real count; avoid triads of words.
- **Don't pad.** One bold phrase per section at most. Use `is`/`has`/「です」
  instead of `serves as`/`features`/「〜となっています」. Show specifics — numbers,
  examples, commands.
- **em-dash only as a deliberate aside** (per `README.ja.md`'s `—` usage); never
  as connective filler, and never forced to zero.
- **Keep pairs in sync.** Edit `README.md` → update `README.ja.md`; edit a
  `site/content/docs/*.md` → update its `*.ja.md` counterpart.
- **Self-check before finishing.** Ask whether each paragraph adds one new fact;
  cut the ones that don't (treadmill test). Read it aloud — if it sounds
  uniformly machine-even, vary sentence length. See `docs/doc-style.ja.md`.
