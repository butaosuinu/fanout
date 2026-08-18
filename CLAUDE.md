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
/ `web/.oxfmtrc.json`). `make check` is the canonical full local gate and runs
`test`, `lint`, and `lint-web`. `make fmt` formats Go (gofumpt/goimports),
`make fmt-web` formats `web/src` + `vite.config.ts` (oxfmt, printWidth 100; CSS と web/ 直下の JSON は対象外), `make fix` runs
`go fix` idiom updates (run `make test` after applying), and `make vuln` runs
govulncheck (network; deliberately not part of `lint`). `make complexity` reports
what the branch adds against its merge base (configs `.golangci-complexity.yml` /
`web/tools/complexity/eslint.config.js`); like `vuln` it is deliberately outside
`check` — see the Complexity Budget section.

The Claude Code integration files (`claude/commands/*.md` slash commands and
`claude/skills/*/SKILL.md` skills) and Codex CLI integration files
(`codex/skills/*/` skill resources) are
bundled in the repo as the source of truth. `make install` places the Claude
files and non-gate Codex skills under the matching `~/.claude/` and
`~/.codex/` directories. The checksum-verified release installer alone owns
Codex `post-work-review`. Do not edit installed copies directly.

The user-facing surface is in `README.md` and `README.ja.md`. Read those before
changing behavior; this file covers repo-local architecture and maintenance
notes.

## Working With fanout

Build the binary with `make build-go`. Use focused tests while editing and
`make check` for the final full local gate.

- Open the console: `make build-go`, then `./fanout-go`. From a plain shell it
  creates or attaches the repository's fanout-managed tmux session; from inside
  tmux it uses the current pane.
- Batch-create child panes: `./fanout-go <parent-issue> --agent claude` from
  inside tmux.
- Override one child issue's agent with repeatable `--agent NUM=name`; for
  `fanout plan`, use `--agent task-id=name`. Supported agents remain
  `claude`, `codex`, and `opencode`.
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
the direction and a core stdlib-purity denylist in CI via godep-cruiser rules
(`internal/arch/godep-cruiser.json` is the rule canon, run by `archtest`
inside `go test`; known exceptions live in `godep-cruiser-baseline.json` and
auto-expire as stale errors; depguard is off on purpose) and is itself class
H — weakening it disables every layer guard, and a godep-cruiser version bump
changes the guard's substance even though the diff only touches go.mod.
Canonical reference, the full package table, the Mermaid dependency diagram,
and the PR-review-weight classes (H/M/A) live in `docs/architecture.ja.md`.

- `cmd/fanout` is the composition root and CLI boundary: `main.go` (the
  first-match-wins dispatch table, ldflags `version`/`commit` — class H),
  `plancmd.go` (`fanout plan` flag parsing/validation; execution lives in
  `app/run`), `status.go` / `lifecycle.go` / `msg.go` (thin dispatch into
  `app/statusreport`, `app/lifecycle`, `app/peermsg`), `dashboard.go`,
  `tui*.go` (the no-argument persistent TUI console wiring: `tui_issue.go`
  issue-mode popup, `tui_launch.go` manual/plan/attach/shell launch — the
  plan fan-out (prompt mode's checkbox, and issue mode's for a single issue)
  launches one coordinator pane at the project root running the fanout-plan
  skill so `fanout plan`'s git root stays at the repo; claude/codex
  coordinators follow `newSessionPlanMode`), and `tui_popup.go` (self-exec
  popup subcommands).
  `main.go` / `tui_popup.go` / `tui_launch.go` / `worktree_action.go` /
  `codex_plan_tui.go` / `codex_team_tui.go` / `tui_restore.go` /
  `tui_watch.go` are class H; the
  remaining cmd files (flag validation and thin dispatch into app) are
  class M.
- `internal/core` is pure logic with no process/network/FS/DB access:
  `agent` (supported agent names, CLI validation for live mode; allowed
  `os`/`os/exec` in the purity allowlist), `planspec` (the `fanout plan` JSON
  schema; allowed `os` for spec loading), `naming` (deterministic slug/branch
  generation; identity-deciding, class M with `parentref`/`fanset`), and the
  AI-reviewable `exitcode`/`cliview`/`errs` (`blockers` is class M: it drives
  --unblocked-only launch selection and wave computation). `errs.Wrap` is the
  shared `defer`-based error wrapper every layer may import; the conventions
  around it (named returns, first-defer registration, stating an identity once)
  are in `docs/error-handling.ja.md`.
- `internal/app` orchestrates use cases on top of `core` and `infra`:
  `panelaunch` (pane creation), `lifecycle`, `watch` (the label-watcher
  cycle, pure at the package boundary via `watch.IO`), `agentprocess`
  (matching a saved launch's argv against the live agent process), `briefing`
  (the prompt text injected into agents), and `prmerge` (target selection and
  preflight behind the dashboard's merge button) are class H; `sessionview` (the read-only
  `Snapshot` aggregator shared by the web dashboard and a future TUI),
  `run`, `statusreport`, `peermsg`, and `cliflags` (flag
  validation that decides main's lifecycle branches) are class M.
- `internal/infra` talks to external processes, the filesystem, and the
  team SQLite bus: `state` (`.fanout/state.json` + its lock), `worktree`
  (`git worktree add` under `.fanout/worktrees/<slug>/`), `hooks`,
  `selfupdate`, and `team` (the `--team`/`fanout msg` per-parent SQLite bus:
  `modernc.org/sqlite`, WAL mode, file mode `0600`, DB scoped to
  `/tmp/fanout-<repo>-<parent_key>.db` with `FANOUT_DB_PATH` override; pane
  identity resolves from `.fanout/state.json` with the `[fanout #N of #P]`
  prompt prefix as fallback), `settings` (the safety gate that blocks
  repo config from enabling the watcher or notification targets),
  `paneruntime` (the one package allowed to name a concrete runtime adapter:
  selection inputs, precedence, construction, self-exec registry), and
  `herdrrun` are class H;
  `ghissue` (GitHub reads and mutations: label swaps, dashboard comments,
  PR merges),
  `gitstat`, `tmuxrun` (direct tmux operations), `tmuxbackend` (the adapter
  from the backend contract to `tmuxrun`), `msgstore`, `notify`,
  `runtime` (git root + tmux target resolution), `displayname`, `codexapp`,
  and `atomicfs` (the shared write path for state.json and the tokened
  dashboard.json) and `gitroot` (project/state-root resolution input) are class M; `log`,
  `tty`, `execx`, `browser`, and `backendtest` (the in-process fake of the
  core backend contract; test-only, never linked into the binary) are class A.
- `internal/ui` holds the TUI (`tui`) and the web dashboard (`dashboard`):
  `server.go` (the route mux — GET-only reads plus the single POST carve-out —
  and the token/same-origin middleware), `merge.go` and `deletebranch.go` (the two
  mutation handlers: PR selection, preflight, and gh failure mapping), `runfile.go` (the tokened
  `.fanout/dashboard.json` reuse/trust gate), `diff.go` (stable row identity,
  read-only worktree patch delivery, and request-wide limits), and `peek.go` /
  `plan.go` (the capture-pane validation chain) are class
  H; `poller.go`, `sse.go`, and `embed.go` are class M. In `tui`, `actions.go` (lifecycle close/merge/
  cleanup wiring and confirmation flow) is class H, rendering/formatting
  (`view.go` / `compact.go` / `styles.go`) is class A, and
  the remaining key/form/polling wiring is class M. The dashboard SPA lives in `web/` (React + Vite + TS,
  split into `src/app`, `src/features/*`, `src/transport`, `src/ui`,
  `src/shared`, `src/styles`; `index.html`'s no-referrer/external-fetch policy
  is class H, `src/transport` is class M — as are `src/shared/github.ts`
  (href-safety boundary) and `src/features/diff/diff.ts` (hostile-patch
  parse/render limits) — the rest is class A) and bundles into
  `internal/ui/dashboard/static/` via `go:embed`.

The full package table, the Mermaid dependency diagram, the human-must-read
invariant catalog, and the burn-down list of known layering debt are the
canonical reference in `docs/architecture.ja.md`. Rule of thumb: a PR that
touches a class-H package needs human review; a PR touching only class-A
packages can rely on AI review.

- Runtime differences are expressed as capabilities, never as a backend-name
  `switch`. `internal/core/backend` holds the ports: `Backend` plus the
  optional `As*` capabilities (decoration, liveness stamping, layout, popup
  host, restore, console), and `MutationModel` (`MutationAtomic` /
  `MutationJournaled`) picks the launch lane. `internal/app` and `cmd/fanout`
  name only those core types — never `tmuxrun` / `tmuxbackend` / `herdrrun`,
  which godep-cruiser's `app-no-runtime-adapters` /
  `cmd-no-runtime-adapters` forbid outside test files — and construct through
  `infra/paneruntime`. `TestRuntimeVocabulary` extends that to naming: those
  two trees must not spell `tmux` or `herdr` in an identifier, import path,
  file name, or struct tag. Comments and prose string literals are exempt,
  but a literal whose whole value IS a runtime name (`"tmux"` / `"herdr"`,
  the shape an equality branch needs, even via a neutral constant) is
  checked; reviewed exceptions live in
  `internal/arch/runtime-vocabulary-allow.json` with a reason, pinned to
  (file, occurrence count), and an entry that matches nothing or overcounts
  fails as stale. `internal/ui` still imports `tmuxrun`
  directly and is not covered yet.
- Agent-state telemetry is a cross-cutting contract on the `@fanout_agent_state`
  tmux pane option, carrying running/working/plan/blocked/idle/done. The launch
  wrapper in `internal/infra/tmuxrun` brackets every agent run with
  running/done; launch-time `--settings` hooks injected by `internal/core/agent`
  refine claude panes to working/blocked/idle; and the Codex Plan Mode
  controller in `internal/infra/codexapp` reports working/plan around the
  fanout-driven initial turn (only on the `thread/settings/update`-unsupported
  fallback path; the seed path hands the prompt to the interactive TUI and is
  unobservable by design), and the codex team bridge in the same package
  reports working/idle/blocked across the whole bridged session (turn
  lifecycle and approval requests, not just injected turns). Messages persist to the
  SQLite bus and are read by pull (`inbox` / `board`) or by the per-agent push
  lanes (`--team` only; see `docs/session-messaging-push.ja.md`):
  `fanout msg watch` — a blocking follower that marks messages read on emit —
  feeds claude panes via the Monitor tool, and the codex team bridge injects
  unread rows into an idle `turn/start`. `fanout msg send` only persists the
  message; the separate `fanout msg nudge` verb pushes an inbox hint to the
  recipient pane, and only when `shouldNudge`
  (`internal/app/peermsg`) allows its state — `running` / `working` / `plan` /
  `idle` qualify (matching the Behavior Boundaries list below). The allowlist
  never includes blocked — a blocked pane shows a permission/input dialog and
  the nudge's Enter could activate the focused control.

`tools/reviewrisk` turns that H/M/A canon into an automated `review:<level>`
judgment on every PR (`make review-risk` runs it locally; see
`docs/review-risk.ja.md`). Changing a package's review class means updating
`tools/reviewrisk/rules.go` in the same PR, or a docsync test fails CI.
`tools/` sits outside `internal/`/`cmd/` and `internal/arch` pins it to
stdlib-only imports, so repo-support code stays isolated from the product.

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
  carved out: a `127.0.0.1`-bound, token-gated localhost server. Every read
  endpoint is GET-only and mutates nothing: `GET /api/snapshot`, `/api/stream`,
  `/api/peek`, `/api/plan`, and `/api/diff` (the last reads the recorded
  worktree through a stable snapshot row identity without requiring a live pane;
  `/api/peek` and `/api/plan` are a read-only `tmux capture-pane` of a recorded
  pane, and `/api/plan` is further gated to plan-mode panes whose recorded agent
  is `codex`). Google Fonts is the single allowed external fetch from the SPA
  (loaded `no-referrer` so the tokened URL never leaks). The "no HTTP/sockets"
  guidance elsewhere is about the legacy notification path (outbound only);
  #137/#142 explicitly delegated the Web UI decision to dashboard #117, which
  this implements standalone (no TUI dependency — the future TUI just reuses
  `internal/app/sessionview`).
- There are exactly two mutation endpoints, each scoped to one GitHub pull
  request: `POST /api/pr/merge` changes that PR's merge state, and
  `POST /api/pr/delete-branch` removes a merged PR's remote head ref. They are
  separate for the same reason GitHub's own UI separates them — deleting the
  branch is a button that appears *after* the merge — and the split is also what
  keeps them simple: deleting a ref is idempotent, so none of the merge's
  never-repeat-an-ambiguous-mutation machinery applies to it. Neither endpoint
  changes anything else. It
  never touches the local working tree, local git refs, worktrees,
  `.fanout/state.json`, or pane input, and it never passes `--admin`, `--auto`,
  or `--delete-branch` to `gh` (that last one would try to delete a local branch
  fanout has checked out in a linked worktree). The client names the PR number,
  head SHA, and base branch it rendered (GitHub can retarget a PR without moving
  its head, so the SHA alone does not pin where the merge lands); both are
  re-read live immediately before the merge, because the snapshot is up to one
  poll stale; the server requires that PR to still be on the
  addressed snapshot row and forwards the SHA as `--match-head-commit`, so a PR
  that moved between render and click is refused by GitHub instead of merged
  blind. merged/closed/draft/CONFLICTING are refused with 409, but review
  approval and CI status are deliberately not gates — enforcing branch
  protection is GitHub's job, and duplicating it would kill the button in a
  repository that requires neither. POST requests pass `postOnly` +
  `sameOriginOnly` (exact `Host` match pins DNS rebinding, `Origin` must match
  when present, non-JSON `Content-Type` is 415) before `requireToken`. Adding a
  second mutation endpoint, widening this one's blast radius, or relaxing those
  gates each require human review. `--no-token` refuses merges outright: the
  loopback port is reachable by every local process, so the route closes rather
  than sit behind a vacuous token check. The branch to delete comes only from the
  PR's own head ref in the base repository, and it is deleted only while the ref
  still points at the PR's head SHA — as GitHub reports it on the live read, never
  as the client named it, since fencing on a client-chosen SHA only proves the
  client can name the ref's current tip — and no other open PR in this repository
  is built on it (two PRs can share one head branch with different bases, so one
  merge does not finish that branch; a `--limit`-capped listing is refused rather
  than read as "nobody else uses it"). A fork head, an unknown head ref, a moved ref, or a second open PR
  drops the delete and reports why, so a cleanup precondition never
  vetoes the merge itself. `gh pr merge` exiting 0 is not proof of a merge — a
  merge-queue base enqueues and returns success — so the result is confirmed
  against GitHub before anything is reported merged or deleted, and an
  unconfirmable merge fails closed. Every row requires the PR's base repository to be this
  one before it can be merged — `Fixes owner/repo#N` closes issues across
  repositories, so a row's PR list is not proof the PR lives here. Issue-less
  rows (plan tasks, `@manual`) additionally find their PRs by head branch NAME,
  which a fork can collide with, so those rows also require the head repository
  and head ref to match; issue rows attribute PRs through the closing-PR link and
  keep accepting fork PRs. A merge whose outcome cannot be read comes back as
  unknown rather than as an error, and the pull request is then held in
  `.fanout/merge-claims.json` — cleared only when a poll shows the PR merged or
  closed, since time is not evidence about an outcome; deleting the entry is the
  documented manual way out — so another tab, a reload, or a dashboard restart
  cannot fire a second one either — that file is the endpoint's only local write.
  The hold is taken *before* gh runs and released once the outcome is known,
  because the answer that never arrives is the one that would have told us to
  write it; a crash mid-merge therefore leaves the same evidence a lost response
  does, and a claims file that cannot be written — or cannot be read — refuses
  the merge outright rather than running it with a guard that would not survive a
  restart. Only a missing file means "no holds": reading a corrupt one as empty
  would lose an unresolved merge and let the next reservation overwrite the only
  record of it.
  A queued merge is held the same way but has a second ending: it records that
  what `gh pr merge` produced on a queue-required base — an armed auto-merge, or
  an entry in the merge queue — can be taken away again, leaving the PR open with
  nothing pending, which no merged/closed check can ever satisfy. A poll has to
  have seen that pending state before its absence counts, since the snapshot from
  before the click shows nothing pending either. The claim on an unreadable
  outcome never takes that exit — that merge may already have happened. A send
  failure that leaves an auto-merge armed is likewise treated as landed rather
  than retryable: only this command could have armed it. The whole
  read-check-reserve sequence runs under a lock on the claims file, because two
  dashboards can run against one repository and an atomic write makes each write
  indivisible, not the decision around it. The
  diff toolbar additionally pins the PR it opened with — number, head, and base —
  and requires the patch on screen to be comparable with what the merge would
  bring in: the PR's head must be the commit `/api/diff` read, that read must be
  of a clean worktree (the patch shows uncommitted work the merge would not
  carry), and its merge base must be a commit the remote already has (`MergeBase`
  prefers the local base branch, so an unpushed commit there drops everything
  after it out of the patch). A push that lands while you are reading, a retarget
  that never moves the head, and a worktree that lags the remote all block the
  merge instead of quietly merging commits the displayed patch does not contain.
  Two gaps in that promise are deliberate and stay documented rather than closed.
  The base check reads the local remote-tracking ref, so a base branch that was
  force-pushed on GitHub since the last fetch is judged against stale local
  state; requiring the tracking ref to equal GitHub's live base would instead
  block every merge whenever the base merely moved, which is most of the time.
  And the worktree is only pinned by commit-and-clean across a collection, so an
  edit made and reverted inside that window can leave the patch showing work that
  no longer exists — that direction shows more than the merge carries, never
  less, which is the direction the fence exists to prevent. Errors that
  provably precede the send — the rate-limit gate — stay plain retryable
  failures instead. The OID fence on the delete is not atomic — GitHub has no
  conditional ref delete — so it catches a push that already landed, not one that
  lands in the round trip after a confirmed merge. Note that the dashboard URL now carries merge authority, not
  just read access: treat it accordingly.
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
- The `@fanout_agent_state` vocabulary is the 6-value contract `running` /
  `working` / `plan` / `blocked` / `idle` / `done`, normalized in
  `internal/app/sessionview` (unknown or forged values fall to `""`); it drives
  the TUI/web state glyphs and badges (`internal/ui/tui`, web) and the `run:`
  filter. The launch wrapper in `internal/infra/tmuxrun` writes only `running` /
  `done`; the richer values come from claude launch hooks and the codexapp
  controllers (Plan Mode / team bridge — see the telemetry note above).
  `fanout msg nudge` (`internal/app/peermsg`) is the only push that
  writes to tmux input: it send-keys a hint only when the peer can take queued
  input (`running` / `working` / `plan` / `idle`), and is a no-op for `blocked`
  (a focused permission dialog), `done`, and unset. The `--team` push delivery
  lanes (`fanout msg watch` under claude's Monitor tool, the codex team
  bridge) never write to tmux input — see `docs/session-messaging-push.ja.md`.
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
- Run focused checks while editing, then commit the candidate. The final
  `/post-work-review` pass owns one `make check` run for that exact HEAD; do not
  duplicate it with separate full `make lint`, `make test`, or `make lint-web`
  runs. Then walk `docs/review-checklist.ja.md`; the same review findings recur.
- `gh pr create` is gated by the repo's `PreToolUse(Bash)` hook registered in
  `.claude/settings.json`. Retrying a denied command with nothing changed
  never succeeds — fix the stated cause, then re-run it: complete
  `/post-work-review` (`make check` must pass and the marker must match HEAD),
  then issue `gh pr create` as a standalone command with no
  `cd`/`pushd`/`env --chdir` and no ref-mutating command (`git commit`,
  rebase) chained in (any cwd
  inside the target worktree works), and keep the PR base at the default
  branch. `FANOUT_SKIP_PR_REVIEW=1` is only for the documented escape hatch.
- `git push` to a branch is gated the same way (`scripts/agent-push-gate.sh`,
  a second `PreToolUse(Bash)` hook; Codex runs it via `.codex/hooks.json`):
  the pushed tip must equal the per-worktree marker
  `$(git rev-parse --git-dir)/fanout-check-passed`, which only a successful
  `make check` on a clean tree writes. When denied, commit the candidate, run
  `make check`, then push again — do not reach for `--no-verify` or retry
  unchanged. Never chain the push after a command that can move a ref in the
  same Bash call (`git commit … && git push`, rebase-then-push, even
  `git fetch … && git push`): the gate verifies only the pre-execution state
  and always denies the chained form, so run each step — the `make check`
  that stamps the new HEAD included — as its own command. Branch deletions
  and tag pushes stay ungated; `gh pr create` (gh pushes an unpushed branch
  itself) requires the same marker, and forms
  the gate cannot trace (`bash -c '… git push …'`, `--mirror`) fail closed.
  Escape hatch: `FANOUT_SKIP_PUSH_CHECK=1`. Edits are auto-formatted by a
  `PostToolUse` hook (`scripts/agent-format-on-edit.sh`, per-file
  `golangci-lint fmt` / `oxfmt` fast paths only).
- A second `PostToolUse` hook (`scripts/agent-complexity-on-edit.sh`) measures
  the edited file's complexity and can send the edit back with exit 2. It only
  judges lines this branch changed, degrades to advice after three blocks for
  the same file in one session, and fails open when a tool or config is
  missing. Escape hatch: `FANOUT_SKIP_COMPLEXITY=1`. See the Complexity Budget
  section below; it is the speed layer, not the enforcement layer.

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

## Complexity Budget

New code has a complexity budget. Cognitive complexity is the primary metric,
cyclomatic the secondary one, and nesting depth is capped separately because
cyclomatic complexity does not weight nesting — a flat switch and a five-deep
defensive `if` score the same without it. Full rationale, distributions, and the
existing-debt list: `docs/complexity.ja.md`.

- Go (non-test): cognitive 12, cyclomatic 10, function body 32 lines / 32
  statements, `nestif` 5, `dupl` 100. Source: `.golangci-complexity.yml`.
- TypeScript: cognitive 7 (`.ts`) / 8 (`.tsx`), cyclomatic 8 / 10, function
  length 60 / 80 lines, statements 10 / 12, nesting depth 3, params 3, nested
  callbacks 3. Source: `web/tools/complexity/eslint.config.js`. `.tsx` is looser
  on cyclomatic only, because JSX `&&` and ternaries inflate it mechanically.
- Write the numbers in those two files and nowhere else. The `PostToolUse` hook,
  `make complexity`, and `.github/workflows/complexity.yml` all read them, and
  the advisory tier (2/3 of each threshold) is derived at runtime.
- Over budget? Reach for an early return or a guard clause to flatten nesting,
  extract a helper that means something on its own, replace a branch pile with a
  table, or — in React — split the component and lift logic into a custom hook.
- Splitting a function purely to get under a number is prohibited. A
  `processDataPart1` / `processDataPart2` pair that only makes sense at the call
  site is worse than the long function it replaced, and it defeats the point of
  measuring.
- Suppression needs a stated reason: `//nolint:gocognit // <why>` or
  `// eslint-disable-next-line sonarjs/cognitive-complexity -- <why>`. Reasonless
  suppressions are caught by `nolintlint` on the Go side and by the PR
  suppression-watch job on the TS side. Do not reach for one to make a check pass.
- Only new code is judged. Every layer scopes to the merge base, so pre-existing
  findings in `internal/ui/tui/update.go` or
  `web/src/features/diff/DiffOverlay.tsx` never block an edit. Do not switch any
  layer to a whole-tree scan: 10% of existing non-test Go functions are over
  budget, and `make check` would stop writing the push-gate marker.
- Complexity is deliberately not part of `make lint` or `make check` for that
  same reason. Use `make complexity` to see what your branch adds.

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
