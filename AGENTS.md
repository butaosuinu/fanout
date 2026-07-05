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

- Claude Code: `claude/commands/*.md` and `claude/skills/*/SKILL.md`,
  installed under `~/.claude/`.
- Codex CLI: `codex/skills/*/SKILL.md`, installed under `~/.codex/skills/`.

Do not edit installed copies under home directories directly. Edit the repo
versions and rerun `make install` or `make link`.

The user-facing surface is documented in `README.md` and `README.ja.md`. Read
the README before changing CLI behavior.

## Codex PR Language

When Codex creates a PR in this repository, write the PR description/body in
Japanese. Keep machine-readable tokens such as `TL;DR`, `Review effort: <0-5>`,
file paths, commands, code identifiers, and required footer lines like
`Closes #N` unchanged.

When Codex runs or posts automatic review comments for a PR in this repository,
write those review comments in Japanese. Keep file paths, line numbers, symbol
names, command names, and quoted code unchanged.

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
- The dashboard web UI (`web/`) lints with `make lint-web` (oxlint + oxfmt
  `--check` + `tsc --noEmit`; configs `web/.oxlintrc.json` /
  `web/.oxfmtrc.json`) and formats with `make fmt-web` (oxfmt, printWidth
  100; scope is `web/src` + `vite.config.ts` — CSS and web/ root JSON are
  excluded). Keep `make lint` Node-free; web checks stay in `lint-web`.
- Run `git config blame.ignoreRevsFile .git-blame-ignore-revs` once per clone
  so bulk-formatting commits stay out of `git blame`.

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
  `tui*.go` (no-argument console wiring; the prompt-mode plan fan-out
  launches one coordinator pane at the project root so `fanout plan`'s git
  root stays at the repo, never Codex Plan Mode), and `tui_popup.go`
  (self-exec popup subcommands). `main.go` / `tui_popup.go` /
  `tui_launch.go` are class H; the remaining cmd files (flag validation and
  thin dispatch into app) are class M.
- `internal/core` is pure logic with no process/network/FS/DB access:
  `agent` (supported agent names, CLI validation for live mode — the only
  core packages allowed `os`/`os/exec`), `planspec` (the `fanout plan` JSON
  schema), `naming` (deterministic slug/branch generation), and the
  AI-reviewable `blockers`/`exitcode`/`parentref`/`fanset`/`cliview`.
- `internal/app` orchestrates use cases on top of `core` and `infra`:
  `panelaunch`, `lifecycle`, `watch` (the label-watcher cycle, pure at the
  package boundary via `watch.IO`), and `briefing` (the prompt text injected
  into agents) are class H; `sessionview` (the read-only `Snapshot`
  aggregator shared by the web dashboard and a future TUI), `panelayout`,
  `run`, `statusreport`, and `peermsg` are class M; `cliflags` is class A.
- `internal/infra` talks to external processes, the filesystem, and the team
  SQLite bus: `state` (`.fanout/state.json` + its lock), `worktree` (`git
  worktree add` under `.fanout/worktrees/<slug>/`), `hooks`, `selfupdate`,
  and `team` (the `--team`/`fanout msg` per-parent SQLite bus:
  `modernc.org/sqlite`, WAL mode, file mode `0600`, DB scoped to
  `/tmp/fanout-<repo>-<parent_key>.db` with `FANOUT_DB_PATH` override; pane
  identity resolves from `.fanout/state.json` with the `[fanout #N of #P]`
  prompt prefix as fallback) are class H; `ghissue`, `gitstat`,
  `tmuxrun` (direct tmux operations), `msgstore`, `notify`, `runtime` (git
  root + tmux target resolution), `settings`, `displayname`, and `codexapp`
  are class M; `atomicfs`, `log`, `tty`, `execx`, `gitroot`, and `browser`
  are class A.
- `internal/ui` holds the TUI (`tui`) and the web dashboard (`dashboard`):
  `server.go` (GET-only mux, token middleware) and `runfile.go` (the tokened
  `.fanout/dashboard.json` reuse/trust gate) are class H; `poller.go`,
  `peek.go` (`GET /api/peek`), `plan.go` (`GET /api/plan`), `sse.go`, and
  `embed.go` are class M. In `tui`, `actions.go` (lifecycle close/merge/
  cleanup wiring and confirmation flow) is class H, rendering/formatting is
  class A, and the remaining key/form/polling wiring is class M.

Rule of thumb: a PR that touches a class-H package needs human review; a PR
touching only class-A packages can rely on AI review.

- Runtime discovery (`internal/infra/runtime`) resolves the git repo root with
  `git rev-parse --show-toplevel`, requires the caller to be inside tmux for
  batch pane creation, and targets the invoking pane (`$TMUX_PANE`) unless
  `--session` is supplied. No-argument TUI mode can start from a plain shell by
  creating or attaching a fanout-managed tmux session for the repository.
- Pane creation is direct: `worktree.Prepare` creates
  `.fanout/worktrees/<slug>/`, `tmuxrun.SplitPaneWithAgentCommand` runs
  `tmux split-window -d -h -P -F '#{pane_id}'`, and
  `tmuxrun.BuildPaneLaunchCommand` launches the selected agent through a POSIX
  wrapper while leaving the user's shell after exit.
- `--agent` or `FANOUT_AGENT` is required. `internal/core/agent` validates supported
  agent names and, in live mode, verifies the corresponding CLI is installed.
- Idempotency is stored in `.fanout/state.json` under `(parent, issueNum)`.
  Live runs hold `.fanout/state.json.lock` while planning and launching so
  parallel invocations cannot create the same child twice.
- Keep prompts one line (`oneLinePrompt`). Full issue context belongs in
  `/tmp/fanout-<repo>-<NUM>.md` (`internal/app/briefing`).
- `--include` widens the child set; `--only` and `--skip` narrow it
  (`fanset.FilterOnlySkip`; child enumeration in `mergeExtraChildren` via
  `internal/infra/ghissue`). Prose scanning for implicit children lives in the
  Claude/Codex skills, not in the CLI.
- `--unblocked-only` filters out issues with OPEN blockers (`splitBlocked` +
  `internal/core/blockers`); `--limit` overflow lands in `Plan.LimitDeferred`.
- `--name` threads caller-generated names through
  (`cliflags.parseNameArg` -> `NameOverride`). Slug hints become deterministic
  worktree slugs, display names become tmux pane titles, and branch overrides
  bypass the generated `branchPrefix + slug` name. The skills generate these
  from issue context; the CLI does not call an LLM for naming.
- `--status`, `--close`, `--merge`, and `--cleanup` operate from
  `.fanout/state.json`; set `FANOUT_STATE_PATH` to point at a specific state
  file outside the repository checkout.
- `internal/app/watch` owns one repository watcher cycle behind the no-argument
  TUI. It lists `watcherTriggerLabel` issues, swaps them to
  `watcherRunningLabel`, launches standalone panes for issues without OPEN
  children, and launches normal parent fan-outs for issues with OPEN children.
  The watcher is opt-in from user config or `FANOUT_WATCHER` only; repo config
  cannot enable it. Keep this separate from #107's skill-led loop for
  revisiting children under a known parent, and treat trigger labels as a
  prompt-injection boundary because issue bodies become briefings.
- `--team` (`cmd/fanout/team.go`) and the `fanout msg` subcommand
  (`cmd/fanout/msg.go`) are sibling-pane peer messaging over a per-parent
  SQLite bus. `internal/infra/team` owns the DB (`modernc.org/sqlite`, pure-Go — no
  external `sqlite3` binary; file `0600`/owner-only) scoped to
  `/tmp/fanout-<repo>-<parent>.db` (`FANOUT_DB_PATH` overrides), plus pane
  identity detection from `.fanout/state.json` (the `[fanout #N of #P]` prompt
  prefix is a fallback) and the peer roster; `internal/infra/msgstore` is the
  send/post/inbox/board/mark-read query layer. The briefing coordination
  section is shared by `claude` and `codex` panes — distinct from Claude-only
  Agent Teams. Messaging is pull-based; there is no idle nudge in the merged
  code (that is the separate, unmerged #72).

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
- Walk `docs/review-checklist.ja.md` before creating a PR — the top CI
  failures and review findings all recur (`make lint` / `make test` as
  described above).
- `go:embed` snapshots whatever is on disk at build time: after editing
  `web/src`, build via `make build-go`, not raw `go build`, or the binary
  ships a stale bundle.
- Preserve fail-fast in `executePlan`: stop after the first failed child
  launch.
- Commit all fixes first, then run the post-work-review gate (the installed
  driver, invoked per `codex/skills/post-work-review`) against that final
  HEAD before `gh pr create` — the marker is tied to the exact commit
  reviewed, so committing anything afterward invalidates it.

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
