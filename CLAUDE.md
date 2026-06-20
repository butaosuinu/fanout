# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with
code in this repository.

## Project Shape

`fanout` is a Go CLI (`cmd/fanout` + `internal/`) plus a dashboard web UI
(`web/`, React + Vite + TypeScript, pnpm). `make install` builds it and places
it at `$(BINDIR)/fanout`. `make build-go` produces the local `./fanout-go`
binary the tests exercise; it depends on `make build-web`, which bundles
`web/` into `internal/dashboard/static/` for `go:embed` — the bundle is never
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
  `--no-pr-visualization`, `--hooks` / `--no-hooks`, plus
  `.fanout/config.json`, user config, and `FANOUT_*` env vars) control or
  reserve generated child briefing switches and lifecycle hook execution.
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
- `cmd/fanout/plancmd.go` is the issue-less `fanout plan` entrypoint
  dispatched before normal `cliflags.Parse`. It loads a local plan spec or
  `.fanout/plans/<slug>.json`, resolves the base branch, builds task rows,
  applies `--only` / `--skip` / `--limit` / `--unblocked-only`, copies live
  specs into `.fanout/plans/`, and wires plan `--status`, `--close`, `--merge`,
  and `--cleanup`.
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
  grouped by parent ("Session"); pane rows carry wave/blockers, CI status, the
  tmux pane title, and the original prompt. IO is injected via `Collectors`
  (the `LivePanes` collector returns each live pane's current path and title)
  so it is pure and unit-testable; both the web dashboard (now) and a future
  TUI consume the same `Build`.
- `internal/dashboard` is the localhost web server: `server.go` (GET-only mux,
  token middleware, SSE, `Cache-Control: no-store` static serving with a
  fallback page when the bundle is absent), `poller.go` (two-tier state/tmux +
  throttled gh refresh, broadcast on change), `sse.go` (channel hub), `peek.go`
  (`GET /api/peek`, a read-only capture-pane of one recorded pane; also the
  `livePaneView` validation chain `plan.go` reuses), `plan.go`
  (`GET /api/plan`, the last complete `<proposed_plan>` block of a recorded
  Codex Plan Mode pane), and `runfile.go` (`.fanout/dashboard.json`
  reuse-if-running). The SPA itself
  lives in `web/` (React + Vite + TS, PAPER BREEZE light/dark matching the
  docs site): `src/lib/` is the pure logic layer (wire types mirroring
  `sessionview` JSON tags, filter/sort/link builders), `src/hooks/` owns
  transport (`useSnapshot` SSE + polling fallback, `usePeek`, `usePlan`,
  `useTheme`), and
  tests are integration-first (vitest + testing-library + MSW; SSE via a
  FakeEventSource). `make build-web` emits the bundle into `static/`
  (deterministic names `assets/app.js` / `assets/app.css`, never committed).
- `internal/agent` maps supported agents (`claude`, `codex`) to launch
  commands and validates installed CLIs for live mode. The batch lanes accept
  repeatable per-target agent overrides (`NUM=name` for issue/Project children,
  `task-id=name` for `fanout plan`) and validate only selected targets.
- `internal/state` owns `.fanout/state.json` plus `.fanout/state.json.lock`.
  The coarse lock covers planning and launching so two fanout invocations do
  not race on the same `(parent, issueNum)` idempotency key. Issue-less plan
  rows use parent `plan:<slug>`, `issueNum: 0`, and `taskId`; `taskId` is an
  additive key used by plan idempotency and task lifecycle.
- `internal/planspec` owns the pure JSON schema for `fanout plan`: `version`,
  `plan` metadata, task validation, deterministic task slug/branch defaults,
  duplicate/collision checks, and `blocked_by` dependency cycle detection.
- `internal/naming` deterministically generates slugs and branch names.
  `--name` may override slug, display name, and branch. The skills generate
  these flags from issue context; the CLI does not call an LLM.
- `internal/team` + `internal/msgstore` back the `--team` / `fanout msg`
  sibling-coordination feature (parent #68, waves #69–#71). `internal/team`
  owns the per-parent SQLite bus: `db.go` opens it with `modernc.org/sqlite`
  (pure-Go, no external `sqlite3` binary) in WAL mode at file mode `0600` and
  refuses a group/world-readable or foreign-owned file; `path.go` scopes the
  DB to `/tmp/fanout-<repo>-<parent_key>.db` (collapsing leading zeros on
  numeric parents, slugifying Project URLs; `FANOUT_DB_PATH` overrides);
  `detect.go` resolves the invoking pane's identity from `.fanout/state.json`
  by `(parent, issueNum)`, with the `[fanout #N of #P]` prompt prefix
  (`FanoutTagRE`) as a fallback; `registry.go` `UpsertPeer` seeds the roster.
  `internal/msgstore` is the query layer for send/post/inbox/board/mark-read.
  `cmd/fanout/team.go` wires `--team` (briefing roster via `buildTeamContext`
  plus a post-`executePlan` peer seed), and `cmd/fanout/msg.go` is the
  `fanout msg` island. The briefing coordination section is injected
  agent-agnostic into the standard briefing for both `claude` and `codex`
  panes, but `briefing.Render` returns the minimal Codex Plan Mode briefing
  before appending it, so `--codex-plan-mode` children are seeded into the
  registry without the coordination section. It is distinct from Claude Code
  Agent Teams, which is Claude-only and coordinates inside a single session. Messaging is pull-based: nothing in the
  merged code nudges a pane. The `@fanout_agent_state` (`running` / `done`,
  set by the launch wrapper in `internal/tmuxrun`) idle-nudge accelerator is a
  separate, still-unmerged issue (#72) — do not assume an idle gate exists.
- `internal/ghissue`, `internal/blockers`, `internal/briefing`,
  `internal/settings`, `internal/displayname`, `internal/atomicfs`,
  `internal/log`, `internal/tty`, and `internal/exitcode` hold the remaining
  reusable pieces. Plan status and blocked-task completion use
  `ghissue.Runner.PRsForBranch` (`gh pr list --head <branch>`) because plan
  tasks have no issue closed-by graph. `briefing.RenderTask` is the plan-task
  briefing variant; it removes issue-closing footers and asks PR bodies to end
  with `Plan: <slug> / Task: <id>`.

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
- `gh pr create` is gated by the repo's `PreToolUse(Bash)` hook registered in
  `.claude/settings.json`. Run `/post-work-review` before creating a PR, or use
  `FANOUT_SKIP_PR_REVIEW=1` only when the documented escape hatch is intended.

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
