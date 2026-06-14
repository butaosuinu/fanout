# fanout

[English](README.md) | [日本語](README.ja.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

**Docs site:** <https://butaosuinu.github.io/fanout/> — installation, workflow,
CLI reference and more, in English and Japanese.

`fanout` is a standalone tmux-based console and launcher for parallel issue
work. Run `fanout` with no arguments to open the persistent TUI console for the
current repository; from there you can create manual agent panes, focus existing
child panes, and run close / merge / cleanup actions. The existing
`fanout <parent-issue|project-url>` lane still fans a known target out into one
tmux pane per child, with each pane getting its own git worktree and an agent
CLI launched from a per-issue briefing file.

## Persistent TUI console

Run `fanout` with no arguments to start the persistent console. From a plain
shell it creates a deterministic fanout-managed tmux session for the current
repository, starts the console in that session, and attaches to it. From inside
tmux it turns the current pane into the console.

Typical single-tool flow:

1. Start in the target repository and run `fanout`.
2. Press `n` to create a manual agent pane, or use `fanout <parent>` from tmux
   when you want to seed a whole parent issue / Project batch with existing
   flags.
3. Use `Enter` / `o` to focus a live pane, `c` to close it, `m` to
   fast-forward merge its branch, and `x` to clean up merged or closed siblings.
4. Press `q` to leave the console while keeping the tmux session and child
   panes alive.

The console reads `<git-root>/.fanout/state.json`, checks whether recorded pane
IDs still exist in tmux, and periodically refreshes issue / closed-by PR state
through the same GitHub CLI source used by `fanout <parent> --status`. Each row
also shows the pane worktree's total work size as `+X/-Y` — `git diff
--shortstat` against the merge-base with the recorded base branch, so committed
and uncommitted changes both count (rows recorded before the base branch was
tracked fall back to `origin/HEAD`, then `HEAD`) — and `dirty`/`clean` from
`git status --porcelain`, which flags uncommitted work without agent
instrumentation. Press
`/` to filter the loaded rows in memory with free-text terms or predicates such
as `state:open`, `agent:codex`, and `wave:wave5`. Filtering does not trigger
extra data fetches, and the automatic state / GitHub refresh continues while a
filter is active. For recorded issue parents it also reloads the parent child
set and shows wave / blocker columns using the same `## Blocked by` and
`(blocked by #N)` sources as `--unblocked-only`; blocked children that have not
been fanned yet appear as `deferred` rows, and CLOSED blockers are shown as
resolved. The TUI and web dashboard consume the same Session snapshot model, so
labels, filtering terms, PR/CI summaries, and synthetic row states stay aligned
across both dashboard surfaces. Its header shows `total` / `merged` /
`pending` / `blocked` rollup counts. Press `n` to create a manual agent pane
from a required prompt, selectable
`claude` / `codex` agent, and optional slug. Manual panes use synthetic
`@manual` state entries and appear in the list after launch. Press `Enter` or
`o` on a live row to focus that pane, and press `p` to refresh the read-only
output snapshot shown in the detail panel. Rows whose recorded pane no longer
exists in tmux are marked `stale!` and are skipped by focus/peek actions. Press
`q` to leave the console; the tmux session and child panes are left running.
Select a recorded pane and press `c` to close it, `m` to fast-forward merge its
branch, or `x` to clean up merged/closed siblings for the same parent. Each
lifecycle action asks for confirmation and uses the same core path as the
corresponding `--close`, `--merge`, or `--cleanup` CLI command.
The console compares consecutive GitHub refresh snapshots and notifies once
when a child becomes merged, a PR's latest CI turns failing, or a child becomes
waiting on an open blocker. Notifications default to a terminal bell; settings
can opt into tmux status-line messages, ntfy, or Slack webhook POSTs.

## Existing flag-driven fan-out

When you already know the parent issue or Project URL, `fanout <target>` is the
batch creation lane. It resolves the repository root with
`git rev-parse --show-toplevel`, refreshes the selected base branch, creates
`.fanout/worktrees/<slug>/`, splits the invoking tmux pane with
`tmux split-window`, and starts the selected agent CLI with the one-line
briefing prompt. It records launched panes in `.fanout/state.json`, keyed by
`(parent, issueNum)`, so reruns of the same parent skip children that already
have a recorded fanout pane. This path uses tmux directly and does not require
dmux.

## Plan specs

`fanout plan <spec.json|plan-slug>` is the issue-less batch lane for work that
is already decomposed into local task specs instead of GitHub child issues. A
spec uses `version: 1`, a `plan` object (`slug`, `title`, optional
`base_branch`), and `tasks` with kebab-case `id`, `title`, `briefing`, optional
`slug`, `display_name`, `branch`, `wave`, and `blocked_by` task IDs. A bare
`plan-slug` loads `<git-root>/.fanout/plans/<plan-slug>.json`; live runs copy
the source spec into that directory for later reruns.

Plan panes are recorded under parent `plan:<slug>` with `taskId` and
`issueNum: 0`, so reruns skip task panes already in `.fanout/state.json` or in
`.fanout/worktrees/`. Use `--dry-run` to inspect the git/tmux/agent actions,
`--only` / `--skip` with task IDs, `--limit` to launch a wave subset, and
`--unblocked-only` to defer tasks whose `blocked_by` dependencies do not yet
have a merged PR on their explicit or generated branch. The generated task
briefing avoids issue-closing footers and asks task PRs to end with
`Plan: <slug> / Task: <id>`.
Use `fanout plan <spec|slug> --status [--format json|table]` to inspect task
PR/blocked state, `--close <task-id>` or `--merge <task-id>` for one recorded
task, and `--cleanup` to remove recorded task panes whose head branch has a
merged PR.

## Project mode

In addition to a parent issue number, fanout's positional argument also
accepts a Projects v2 URL — `https://github.com/users/<owner>/projects/<n>`
or `https://github.com/orgs/<org>/projects/<n>`. The canonical
`/views/<id>` suffix and trailing query strings are also accepted, so
copy/paste from the browser address bar works. In this mode children come
from the Project's items instead of a parent issue's Sub-issues +
task-list union.

- **Default filter is `Status == Todo`.** Pass `--project-status "<name>"`
  to pick a different single-select value (e.g. `"In Progress"`), or
  `--project-status all` to disable the filter and include every item
  (Done, no status, etc.).
- **No parent body means no implicit-children salvage.** Phrases like
  `Closes #N`, `Depends on #N`, or Japanese idioms that the bundled
  Claude/Codex skills normally surface from a parent body don't exist
  here — the Project is the source of truth. Use `--include 4,7` to
  force-add anything the Project happens to omit.
- **Single-repo only.** Items whose `content.repository` differs from the
  current git repository root are warned and skipped; fanout still assumes
  one repo per run.
- **Status field missing on the Project?** fanout warns and falls back to
  every item regardless of `--project-status`.
- **Idempotency and `--unblocked-only` work the same way.** Action mode
  skips children recorded in `.fanout/state.json` for the same
  `(parent, issueNum)` pair. As a migration fallback, unrecorded existing
  `.fanout/worktrees/<slug>` directories are also skipped. If the same issue
  is recorded for another parent, only an existing worktree matching the slug
  this current run would create is treated as that migration fallback.
  Blockers in Project mode come only from the child body's `## Blocked by`
  section and the `blocked` label; the `(blocked by #X)` task-list trailer
  doesn't exist without a parent body.

## Installation

The recommended install path is the released Go binary. It installs the stable
`fanout` command plus the bundled Claude/Codex integration files:

```bash
# fanout + Claude/Codex integrations into ~/.local, ~/.claude, ~/.codex
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh

# Binary only
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh -s -- --no-skills

# Custom destination or pinned release tag
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | BIN_DIR=/usr/local/bin sh
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | FANOUT_VERSION=v0.2.0 sh
```

Installed paths:

- `$BIN_DIR/fanout` (default `~/.local/bin/fanout`)
- `$CLAUDE_DIR/commands/fanout.md` (default `~/.claude/commands/fanout.md`)
- `$CLAUDE_DIR/skills/fanout/` (default `~/.claude/skills/fanout/`)
- `$CLAUDE_DIR/skills/fanout-issues/` (default `~/.claude/skills/fanout-issues/`)
- `$CODEX_DIR/skills/fanout/` (default `~/.codex/skills/fanout/`)
- `$CODEX_DIR/skills/fanout-issues/` (default `~/.codex/skills/fanout-issues/`)

`install.sh` detects macOS/Linux and amd64/arm64, downloads
`fanout_<os>_<arch>.tar.gz` from the latest GitHub Release (or
`FANOUT_VERSION`), verifies `SHA256SUMS` when `sha256sum` or `shasum` exists,
and overwrites the same paths on rerun. It never edits shell rc files. Remove
the installed files with:

```bash
curl -fsSL https://raw.githubusercontent.com/butaosuinu/fanout/main/install.sh | sh -s -- --uninstall
```

Confirm `~/.local/bin` is on your `PATH` (`echo $PATH | tr ':' '\n' | grep -F "$HOME/.local/bin"`).
If not, add `export PATH="$HOME/.local/bin:$PATH"` to your shell rc.
Restart any running Codex CLI session after installing or updating skills so it
picks up the new skill files.

### macOS security notes

The curl/wget install path normally does not attach the `com.apple.quarantine`
extended attribute, so the Gatekeeper "cannot verify developer" GUI block
should not appear for the installed binary. If you download the archive through
a browser and macOS quarantines it, remove the attribute after extracting:

```bash
xattr -d com.apple.quarantine /path/to/fanout
```

Apple Silicon requires every executable to carry at least an ad-hoc signature.
The release workflow builds darwin binaries on macOS with Go 1.26, so the Go
linker signs the binary as part of the build. Do not run an external `strip`
after release packaging; it can invalidate the signature. If a local copy is
damaged, ad-hoc re-sign it with:

```bash
codesign -s - /path/to/fanout
```

Developer ID signing and notarization are intentionally out of scope for the
curl distribution path; they can be added later if browser, dmg/pkg, or
managed-Mac distribution becomes a requirement.

### From a checkout

Local Makefile targets install or symlink the Go binary as the stable `fanout`
command plus the bundled integrations:

```bash
make install            # builds Go as $(BINDIR)/fanout + installs integrations
make link               # symlinks Go as $(BINDIR)/fanout + links integrations
make uninstall          # removes installed paths

PREFIX=/usr/local sudo make install     # system-wide Go CLI; overrides BINDIR
CLAUDE_DIR=/path/to/.claude make install # non-default Claude data dir
CODEX_DIR=/path/to/.codex make install   # non-default Codex data dir
```

Building from a checkout needs a **Go toolchain** (Go 1.26+) plus **Node.js
24+ and pnpm 10+**: `make install`, `make link`, and `make build-go` first
build the dashboard web UI (`make build-web`, Vite bundle under `web/`) and
embed it into `go build ./cmd/fanout`. The curl install above ships a prebuilt
binary and needs neither Go nor Node.

## Development

```bash
make test           # Go unit tests + web UI tests + Tier 1 + Tier 2 black-box tests (bats-core required)
make test-tier1     # flag/prereq tests only
make test-tier2     # --dry-run golden tests against fixture scenarios
make test-web       # dashboard web UI tests (vitest)
make lint           # pinned golangci-lint v2 (.golangci.yml) + shellcheck of the test shims
make lint-web       # dashboard web UI lint (oxlint + oxfmt --check + tsc --noEmit)
make fmt            # gofumpt/goimports formatting via golangci-lint fmt
make fmt-web        # dashboard web UI formatting (oxfmt)
make fix            # go fix idiom updates (run make test after applying)
make vuln           # govulncheck (network; deliberately not part of make lint)
make build-web      # build the dashboard web UI bundle into internal/dashboard/static/
make build-go       # build the web bundle + the Go CLI as ./fanout-go
```

bats: `brew install bats-core` on macOS, `apt install bats` on Debian/Ubuntu.
The black-box tiers build `./fanout-go` and exercise it via `FANOUT_BIN`.
Tier 1 locks the CLI surface (error messages + exit codes); Tier 2 locks the
`--dry-run` planning output against fixture scenarios under `tests/fixtures/`.
Regenerate Tier 2 goldens with `FANOUT_GOLDEN_UPDATE=1 make test-tier2` when you
intentionally change dry-run output. Tier 3 (live tmux E2E) stays manual.

The dashboard web UI lives in `web/` (React + Vite + TypeScript, pnpm). The
built bundle is **not committed**; `make build-web` emits it into
`internal/dashboard/static/` where `go:embed` picks it up (a checkout without
the bundle still compiles and serves a "run make build-web" page). For UI work
with hot reload, start a data server and the Vite dev server, which proxies
`/api/*` to it:

```bash
./fanout-go dashboard --web --port 7777 --no-token   # terminal 1
cd web && pnpm install && pnpm dev                   # terminal 2 → http://localhost:5173
```

## Prerequisites

- The default pane-creation flow needs `gh` CLI, `git`, and `tmux`.
  `--status` and `--cleanup` use `gh`/`git`; `--merge` and `--close` use
  `git` (`--close`/`--cleanup` treat an already-missing tmux pane as stale).
  fanout checks the dependencies needed for the selected mode at startup and
  prints install hints on failure. Children can be declared via
  the Sub-issues API, the parent body's task-list (`- [ ] #NUM ...`), or
  both — fanout unions them.
- **Project mode only**: the `gh` CLI must have the `read:project` scope so
  the GraphQL query that lists Project items can succeed. Add it with
  `gh auth refresh -s read:project`. Issue-mode (`fanout <N>`) does not
  need this scope.
- **`--team` / `fanout msg`** use a per-parent SQLite database, but the driver
  is pure-Go and compiled into the binary — there is **no** external `sqlite3`
  install to add.
- Choose the launch lane:
  - TUI mode (`fanout` with no arguments) can be invoked from a plain shell. It
    creates or attaches a fanout-managed tmux session for the current
    repository before starting the console. When invoked from inside tmux, it
    uses the current session and pane.
  - Batch pane-creation mode (`fanout <parent-issue|project-url> ...`) must be
    invoked from inside a tmux session. It creates child panes directly with
    `tmux split-window`, targeting the invoking pane unless `--session` is
    supplied.
- **An agent name must be resolvable**: pass `--agent claude`, `--agent codex`,
  or set `FANOUT_AGENT`. Unknown agents fail before pane creation; in live
  mode, fanout also checks that the agent CLI is installed.
- fanout creates child worktrees under `.fanout/worktrees/<slug>/`. Before
  branching, it refreshes the base branch with `git fetch --quiet --no-tags`
  and a fast-forward update. Use `--base-branch <branch>` to override the base
  (bare local branch names and `origin/<branch>` are supported) or
  `--no-refresh` to skip that refresh. Live runs add `.fanout/worktrees/` to the
  target repo's local `.git/info/exclude` so generated worktrees do not dirty
  `git status`.

## Usage

```
fanout # start the persistent tmux console
fanout <parent-issue|project-url>  # batch pane creation; run from inside tmux
       [--agent <name>] [--limit <N>] [--only <list>] [--skip <list>]
       [--include <list>] [--unblocked-only] [--project-status <name>]
       [--name <NUM>=<slug>[|<display>[|<branch>]]]
       [--base-branch <branch>] [--branch-prefix <prefix>] [--no-refresh]
       [--session <tmux-session>] [--sleep <seconds>]
       [--popup-timeout <seconds>] [--dry-run]
       [--auto-pr|--no-auto-pr] [--pr-review-gate|--no-pr-review-gate]
       [--briefing-code-review|--no-briefing-code-review]
       [--agent-teams-hint|--no-agent-teams-hint]
       [--codex-plan-mode|--no-codex-plan-mode]
       [--pr-visualization|--no-pr-visualization]
       [--team]
fanout <parent-issue> --status [--format json|table] [--post-dashboard]
                                      # status of fanned children; optionally post dashboard
fanout <parent-issue> --merge <NUM> # fast-forward merge a recorded child branch
fanout <parent-issue> --close <NUM> # remove a recorded child worktree/pane
fanout <parent-issue> --cleanup     # remove merged/closed recorded children
fanout dashboard --web              # read-only localhost web dashboard (Session view)
fanout msg <verb> [options] [body...]  # peer messaging between sibling panes (see below)
fanout --check-update               # Compare this binary with the latest release
fanout update                       # Replace this binary + integrations via install.sh
fanout --help
```

The positional accepts either a GitHub issue number (Sub-issues +
task-list mode) or a Projects v2 URL (Project mode; see above).
`--project-status` only applies to Project mode and is ignored otherwise.
`--popup-timeout` is a deprecated compatibility flag from the old runtime and
is accepted but ignored by the direct tmux path.

### Codex Plan Mode

`--codex-plan-mode` is an opt-in launch mode for `--agent codex`. Instead of
running positional `codex "<prompt>"`, fanout starts a Codex app-server for the
child, creates a `plan` collaboration-mode thread, starts the initial turn with
the fanout prompt through that app-server, and attaches an interactive Codex TUI
to the remote session.
The child briefing is also rewritten for Plan Mode: it asks for a
`<proposed_plan>` implementation plan and explicitly forbids file edits,
commits, pushes, and PR creation in that first turn.

This path does not send `/plan` or prompt text through tmux. The pane remains an
interactive Codex TUI session so the user can continue from the Plan Mode
conversation. If the app-server Plan turn setup or TUI attach fails, the launch
fails before state is recorded and fanout cleans up the pane/worktree so the
child can be retried.

### `--status` output

`fanout <parent> --status` is read-only: it reads `.fanout/state.json` to
enumerate children already fanned out under that specific parent, queries
each child through `gh api graphql` against
`repository.issue.closedByPullRequestsReferences(first: 100)` (cursor-
paginated when a child is closed by more than 100 PRs) so the response
carries `state`, `mergedAt`, `reviewDecision`, and the latest commit's CI
rollup when present, and prints one JSON document on stdout by default. Pass
`--format table` for a human-readable overview that adds normalized PR state
(`open`, `draft`, `review-required`, `approved`, `changes-requested`,
`merged`, or `closed`), CI, PR diff bars, changed-file counts,
Conventional-Commit type, and PR links.
Pass `--post-dashboard` to upsert one marker-based comment on the parent issue
with sub-issue number, PR link, PR state, CI, diff size, Conventional-Commit
type, TL;DR, and `Review effort` score for each child PR. The dashboard is built
from machine-readable GitHub data and PR bodies; it does not call an LLM. JSON
mode does not fetch PR diff stats unless `--post-dashboard` is also set, so the
additional review/CI fields come from the same per-issue GraphQL lookup. It does
not require dmux or a live tmux session. Issue-mode parents only — Projects v2
URLs as parent are rejected up-front for the current JSON schema.
In a state file that has fanned multiple parents, children of other parents are
filtered out so `summary.all_merged` reflects only the requested parent. Set
`FANOUT_STATE_PATH` to point directly at a state file when reading from outside
the repository checkout; otherwise fanout reads `<git-root>/.fanout/state.json`.

```json
{
  "parent": 123,
  "children": [
    { "num": 4, "state": "CLOSED",
      "prs": [ { "number": 250, "state": "MERGED",
                 "mergedAt": "2026-05-04T10:00:00Z",
                 "reviewDecision": "APPROVED", "ci": "pass" } ],
      "has_merged_pr": true },
    { "num": 7, "state": "OPEN",
      "prs": [],
      "has_merged_pr": false }
  ],
  "summary": {
    "total":      2,
    "merged":     1,
    "pending":    1,
    "all_merged": false
  }
}
```

`--status` exit codes are a separate lane from the default flow:

- `0` — status emitted (check `summary.all_merged` in JSON mode for the
  actual state).
- `2` — cannot enumerate (bad invocation, unreadable or malformed state file,
  unusable project root, Projects v2 URL as parent). A missing state file is
  treated as an empty state.
- `3` — `gh` API call failed (auth, network, non-existent issue, etc.).

`--post-dashboard` is the only `--status` option that writes to GitHub. It puts
`<!-- fanout:dashboard parent=N -->` at the start of the comment body, finds an
existing marker comment with the paginated GitHub REST comments endpoint, and
updates that exact comment. If no marker comment exists, it creates one with
`gh issue comment --body-file -`.

`--status` is exclusive with all action-bearing flags (`--agent`, `--limit`,
`--only`, `--skip`, `--include`, `--name`, `--base-branch`,
`--branch-prefix`, `--no-refresh`, `--session`, `--sleep`,
`--popup-timeout`, `--dry-run`, `--unblocked-only`, `--close`, `--merge`,
`--cleanup`, `--auto-pr`, `--no-auto-pr`, `--pr-review-gate`,
`--no-pr-review-gate`, `--briefing-code-review`,
`--no-briefing-code-review`, `--agent-teams-hint`, `--no-agent-teams-hint`,
`--codex-plan-mode`, `--no-codex-plan-mode`, `--pr-visualization`,
`--no-pr-visualization`).

### Lifecycle commands

The lifecycle commands operate on entries recorded in `.fanout/state.json`.
They do not discover arbitrary worktrees by scanning the filesystem.

- `fanout <parent> --merge <NUM>` runs
  `git -C <project-root> merge --ff-only <recorded-branch>`. If the merge is
  not a fast-forward, fanout reports the git error and does not start an
  editor or conflict-resolution flow.
- `fanout <parent> --close <NUM>` removes the recorded worktree with
  `git worktree remove <path> --force`, kills the recorded tmux pane when it is
  still present, removes the state entry, and runs `git worktree prune`.
- `fanout <parent> --cleanup` queries the recorded children and closes any
  child whose issue is `CLOSED` or whose closed-by PR list contains a `MERGED`
  PR. Pending children remain recorded.

Like `--status`, these commands honor `FANOUT_STATE_PATH`; otherwise they use
`<git-root>/.fanout/state.json`.

### Dashboard (web UI)

`fanout dashboard --web` starts a **read-only** web dashboard that visualizes
fanout **Sessions** — the panes recorded in `.fanout/state.json`, grouped by
parent issue — and keeps them live in the browser: pane liveness (from
`tmux list-panes`), issue state, and PR merge status (the same data source as
`--status`, reused across every parent in the repo at once). It never mutates
repo or GitHub state, and only ever *reads* tmux. The one intentional tmux side
effect is the convenience `prefix + D` keybinding it registers in your running
tmux server (opt out below).

```
fanout dashboard --web [--port N] [--open] [--no-token] [--no-keybind]
```

- **Always available, on demand.** Launch it from a terminal, or press
  **`prefix + D`** inside tmux: after a live fan-out (and whenever the dashboard
  itself starts) fanout registers that tmux keybinding for you, so any pane can
  pop the dashboard. The key launches the server in a detached
  `fanout-dashboard` window, so it outlives the keypress; a second press just
  reopens the existing URL. fanout records the owning project root on launched
  panes, so the key keeps working from agent TUIs such as Codex even when tmux
  reports a stale `pane_current_path`. Disable the auto-binding with
  `--no-dashboard-keybind` (fan-out) / `--no-keybind` (dashboard), the
  `dashboardKeybind` config key, or `FANOUT_DASHBOARD_KEYBIND=0`.
- **Session table + HUD.** Each pane row shows issue, agent, wave and open
  blockers (from the parent issue graph), branch, diff/dirty, CI status, tmux
  liveness with the current pane title and a `running` / `done` agent-state
  badge (reported live by the pane's launch wrapper; when tmux is unreachable
  it falls back to the launch-time record, which can be stale), and PR state;
  the HUD on top includes repo-wide running and blocked counts.
- **Detail drawer with live peek.** Click a row to open a right-side drawer:
  pane metadata, wave/blockers, worktree, PRs with CI, the original prompt,
  and a *peek* at the pane's recent output (`GET /api/peek`, a read-only
  `tmux capture-pane` refreshed every 5 s while open).
- **Plan view for Codex Plan Mode panes.** For panes launched with
  `--codex-plan-mode`, the drawer adds a *plan* section showing the last
  complete `<proposed_plan>` block in the pane's output (`GET /api/plan`,
  also a read-only `tmux capture-pane`; fetched once on open, with a manual
  refresh button). A long plan can scroll out of the codex TUI's alternate
  screen, in which case the section reports that no plan was found.
- **Structured filtering.** The filter box ANDs free words with
  `state:` / `run:` / `agent:` / `wave:` / `ci:` / `dirty:` / `live:` /
  `issue:` / `pr:` terms — e.g. `agent:claude wave:2 ci:fail run:running`.
  The dropdowns next to the box write the same tokens for you, and active
  terms appear as removable chips.
- **Light/dark themes.** The PAPER BREEZE UI matches the docs site; the header
  toggle persists to `localStorage` (`fanout.theme`) and defaults to your
  `prefers-color-scheme`.
- **localhost only.** The server binds `127.0.0.1` and exposes GET-only
  endpoints (`/api/snapshot`, an SSE `/api/stream`, `/api/peek`, `/api/plan`,
  and the embedded UI). `--port` defaults to `0` (an OS-assigned ephemeral port); the
  chosen URL is printed. The UI's one external request is its Google Fonts
  stylesheet, loaded with a `no-referrer` policy so the tokened dashboard URL
  never leaks.
- **Token by default.** A random token is generated each start and embedded in
  the printed/opened URL, gating `/api/*` so other local users or processes
  cannot read your issue/PR data off the loopback port. Pass `--no-token` on a
  single-user machine to drop it.
- **`--open`** opens the URL in your default browser. The dashboard reuses a
  server that is already running (recorded in `.fanout/dashboard.json`) instead
  of starting a second one.
- **Degrades gracefully.** With `gh` logged out it shows a banner and a
  state-only view; outside tmux it still serves, marking liveness unknown.

Run `fanout dashboard --help` for the full flag list.

### Sibling coordination (peer messaging over SQLite)

When you fan a parent issue out into several panes, those panes are independent
agent sessions that cannot see each other. `--team` plus the `fanout msg`
subcommand give them a lightweight, opt-in way to coordinate — "I'm about to
touch the shared schema", "my branch is `feat/x`, rebase off it", "blocked,
anyone done with #42?" — without a human relaying messages between panes.

**Why this exists (and how it differs from Agent Teams).** Claude Code's Agent
Teams coordinates *teammates inside one session*. fanout's panes are *separate
processes* — each its own `claude`/`codex` session in its own git worktree — so
they need a cross-session channel. peer messaging is that channel: it works the
same for `claude` and `codex` panes, and needs no shared model context.

**What it is.** A per-parent SQLite message bus. Every sibling of the same
parent reaches the same database, which carries two traffic types: a shared
**board** (broadcast to all siblings) and **1:1** messages (addressed to one
issue number). Each message has a free-form `kind` label (default `note`; no
fixed vocabulary — use whatever your team finds useful, e.g. `blocker`,
`heads-up`). The database lives at `/tmp/fanout-<repo>-<parent>.db` (override
with `FANOUT_DB_PATH`).

**Turning it on (`--team`).** Add `--team` to a fan-out run:

```
fanout 123 --team --agent claude
```

This does two things, both best-effort (a registry failure never fails the
fan-out): it appends a "Coordinating with your sibling panes" section — a
launch-time roster plus the shared DB path and a few checkpoints — to each
child's standard briefing, and after the batch launches it seeds the created
panes into the parent's peer registry.

> [!NOTE]
> Codex Plan Mode children (`--agent codex --codex-plan-mode`) receive the
> minimal Plan-Mode briefing instead, so the coordination section is **not**
> added to them. They are still seeded into the registry and can use
> `fanout msg` normally — only the injected briefing section is skipped.

**Using it (`fanout msg`).** Inside any fanned pane, `fanout msg` auto-detects
which child you are (from the tmux pane and `.fanout/state.json`) and which
parent you belong to:

```
fanout msg peers                      # live sibling roster
fanout msg inbox [--mark-read]        # unread 1:1 messages + unread board posts
fanout msg board [--all]              # the shared board
fanout msg send --to 42 "rebase off feat/login before you touch auth.go"
fanout msg post --kind heads-up "editing go.mod — hold lockfile edits"
fanout msg mark-read --all            # drain your inbox + advance the board cursor
fanout msg register                   # (re-)register this pane in the roster
```

Common options across verbs: `--json` (machine-readable output), `--self <N>`
and `--parent <ref>` (override pane detection), and `--dry-run` (write verbs
only — prints the `# would ...` writes and touches nothing; not combinable with
`--json`). Exit codes: `0` success, `2` invalid invocation, `4` SQLite backend
failure.

Coordination is **pull-based**: messages persist in the DB and a sibling reads
them at its own checkpoints (`fanout msg` does not interrupt a busy pane). Keep
messages short and factual.

> [!WARNING]
> The database is a **plaintext** SQLite file under `/tmp`. fanout creates it
> `0600` (owner-only) and refuses to open one that is group/world-readable or
> owned by another user, but `/tmp` is shared scratch space — **do not put
> secrets, tokens, or credentials in messages.** The DB and its briefing
> roster are throwaway; delete `/tmp/fanout-<repo>-<parent>.db*` when done.

Run `fanout msg --help` for the full surface.

### `--check-update`

`fanout --check-update` is read-only. It fetches the latest release tag from
`butaosuinu/fanout`, compares it with the binary's embedded version, and prints
whether an update is available. `fanout check-update` is accepted as the
subcommand form. Local dev builds (`version == "dev"`, including plain
`make build-go`) do not call `gh`; they print a dev-build message and exit 0.

Exit codes:

- `0` — comparison completed, or this is a dev build.
- `2` — the current version or latest tag is not `MAJOR.MINOR.PATCH`
  (optionally prefixed with `v`).
- `3` — `gh release view -R butaosuinu/fanout` failed.

### `update`

`fanout update` replaces the running release binary by invoking the same
`install.sh` path documented under Installation, so OS/arch detection, release
downloads, checksum verification, archive extraction, and Claude/Codex skill
installation stay centralized in one script.

By default it resolves the latest release, compares it with the embedded
version, reports the current binary path (after `EvalSymlinks`), and then runs
the installer immediately. Local dev builds (`version == "dev"`, including
plain `make build-go`) refuse replacement.

Options:

- `--version <tag>` — install a pinned release tag by passing
  `FANOUT_VERSION=<tag>` to `install.sh`.
- `--no-skills` — pass `--no-skills` through to `install.sh`, updating only the
  binary.

Exit codes:

- `0` — update completed, or already up to date.
- `1` — environment/preflight failure such as dev build, no `curl`/`wget`,
  an unwritable binary directory, or a missing option value.
- `2` — unknown option, unexpected argument, or incomparable version strings.
- `3` — latest release lookup failed.

### Settings

The Go implementation can turn six opinionated behaviors on or off (five
briefing toggles plus the dashboard keybinding) and select TUI notification
channels. The deprecated Bash `./fanout` does not support these new flags,
files, or env vars. Boolean defaults are all `true` to preserve existing
behavior; notifications default to `bell`.

Resolution order is: **CLI flag > environment variable > repo config file >
user config file > built-in default**. fanout applies layers in the reverse
order once per run after it resolves the git repository root. The repo config
path is `<project_root>/.fanout/config.json`, where `project_root` is the
parent repository root, not the child worktree. The user config path is
`$XDG_CONFIG_HOME/fanout/config.json`, or `~/.config/fanout/config.json` when
`XDG_CONFIG_HOME` is unset.

```json
{
  "autoPullRequest": false,
  "prReviewGate": true,
  "briefingCodeReview": true,
  "agentTeamsHint": false,
  "prVisualization": true,
  "dashboardKeybind": true,
  "notifications": "bell",
  "ntfyURL": "https://ntfy.sh/my-topic",
  "slackWebhookURL": "https://hooks.slack.com/services/..."
}
```

| Behavior | File key | Env | CLI flags | Default |
|---|---|---|---|---|
| PR auto-creation instruction | `autoPullRequest` | `FANOUT_AUTO_PR` | `--auto-pr` / `--no-auto-pr` | `true` |
| PR review gate note | `prReviewGate` | `FANOUT_PR_REVIEW_GATE` | `--pr-review-gate` / `--no-pr-review-gate` | `true` |
| Claude `/code-review` instruction | `briefingCodeReview` | `FANOUT_BRIEFING_CODE_REVIEW` | `--briefing-code-review` / `--no-briefing-code-review` | `true` |
| Claude Agent Teams hint | `agentTeamsHint` | `FANOUT_AGENT_TEAMS_HINT` | `--agent-teams-hint` / `--no-agent-teams-hint` | `true` |
| Structured PR body and gated Mermaid briefing guidance | `prVisualization` | `FANOUT_PR_VISUALIZATION` | `--pr-visualization` / `--no-pr-visualization` | `true` |
| Dashboard `prefix + D` tmux keybinding | `dashboardKeybind` | `FANOUT_DASHBOARD_KEYBIND` | `--dashboard-keybind` / `--no-dashboard-keybind` | `true` |
| TUI transition notifications | `notifications` | `FANOUT_NOTIFICATIONS` | n/a | `bell` |
| ntfy POST URL | `ntfyURL` | `FANOUT_NTFY_URL` | n/a | unset |
| Slack webhook POST URL | `slackWebhookURL` | `FANOUT_SLACK_WEBHOOK_URL` | n/a | unset |

Boolean environment values accept `1/true/yes/on` and `0/false/no/off`
(case-insensitive). Invalid boolean env values, unknown file keys, and values
with the wrong JSON type are warned and ignored so future settings do not break
older fanout binaries.

`notifications` is a comma- or space-separated selector. Supported values are
`bell`, `tmux`, `ntfy`, `slack`, and `none`. `ntfy` requires `ntfyURL`;
`slack` requires `slackWebhookURL`. Both HTTP channels only send outbound POST
requests and never open inbound sockets. To avoid repository-controlled
exfiltration, repo config may only select `bell`, `tmux`, or `none`; `ntfy`,
`slack`, `ntfyURL`, and `slackWebhookURL` are honored only from user config or
environment variables.

`prVisualization=false` omits the structured PR-body and gated Mermaid guidance
from child briefings. The guidance is only injected when `autoPullRequest` is
also true, because it applies to the PR body the child will open.

`prReviewGate=false` does not forcibly disable child Claude Code hooks. It adds
a note allowing `FANOUT_SKIP_PR_REVIEW=1 gh pr create ...` if the `PreToolUse`
hook blocks PR creation before `/post-work-review`.

### Examples

```bash
# Use Claude for child panes in these examples.
export FANOUT_AGENT=claude

# Fan out all OPEN sub-issues of #123
fanout 123

# Preview the git worktree + tmux commands without executing them
fanout 123 --dry-run

# Cap this invocation to 3 issues; rerun command is printed for the rest
fanout 123 --limit 3

# Fan out only a non-contiguous subset of children (warns and ignores any
# number that is not in the parent's OPEN child set)
fanout 123 --only 4,7,8,10

# Fan out everything except these children; compose with --limit
fanout 123 --skip 6,9 --limit 3

# Force-add children that fanout's auto-detection (Sub-issues API + task-list)
# misses — e.g. issues the parent body only references via `Closes #N`,
# `Depends on #N`, plain bullets, or prose. The bundled Claude/Codex
# integrations fill this in automatically after reading the parent body; use
# it directly when running the CLI outside an agent session. CLOSED/nonexistent
# numbers are warned and skipped. Composes with --only/--skip (include first,
# then filter).
fanout 123 --include 4,7

# Fan out only children whose blockers are all CLOSED. Blockers are read from
# the child body's `## Blocked by` section, a trailing `(blocked by #X, #Y)`
# on the parent's task-list row, or the child's `blocked` label (weak signal,
# logged only). Safe to rerun as blocker PRs merge — drives Wave 1 → 2 → …
# with no manual bookkeeping.
fanout 123 --unblocked-only

# Cap each wave while letting fanout pick the next unblocked batch
fanout 123 --unblocked-only --limit 3

# Name each child's worktree slug stem, pane title, and branch directly. fanout
# appends -<issue-number> to slug stems that do not already have it, while
# rerun idempotency comes from `.fanout/state.json`. The optional 3rd segment
# overrides the generated branch name. Any of the three pipe-separated segments
# may be empty, but at least one must be non-empty. Normally the bundled
# Claude/Codex integrations generate these from issue title/body without any
# extra API call; pass --name yourself to override. Repeatable; one per target.
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'
fanout 123 --name 8='feat-x|Feature X|feat/issue-8-x'   # all three segments
fanout 123 --name 9='||release/v2.0'                    # branch override only

# Override the branch base and generated branch prefix
fanout 123 --base-branch release/v2 --branch-prefix fanout/release/

# Skip base branch refresh before creating worktrees
fanout 123 --no-refresh

# Target a named tmux session instead of the invoking pane
fanout 123 --session work-repo

# Give fanout 8 seconds between creations
fanout 123 --sleep 8

# Choose the agent CLI for child panes
fanout 123 --agent codex

# Start Codex children as app-server Plan Mode sessions with interactive TUI.
fanout 123 --agent codex --codex-plan-mode

# Remove the automatic PR-opening requirement from child briefings for one run
fanout 123 --no-auto-pr

# Disable the Agent Teams hint globally for this shell
export FANOUT_AGENT_TEAMS_HINT=0

# Enable sibling-pane peer messaging for this run (briefing roster + peer
# registry over a per-parent SQLite bus). Then, from inside any fanned pane:
fanout 123 --team --agent claude
fanout msg peers                       # who else is in this fan-out
fanout msg post --kind heads-up "editing go.mod — hold lockfile edits"
fanout msg send --to 4 "rebase off feat/login before touching auth.go"
fanout msg inbox --mark-read           # read + drain messages addressed to me

# Status from .fanout/state.json: default JSON for automation, optional table
# for PR state / CI / diff scans, optional parent dashboard comment.
fanout 123 --status
fanout 123 --status | jq '.summary.all_merged'
fanout 123 --status --format table
fanout 123 --status --post-dashboard

# Fast-forward a recorded child branch into the parent worktree, then remove
# the child worktree/pane after it is no longer needed.
fanout 123 --merge 4
fanout 123 --close 4

# Remove all recorded children whose issue is CLOSED or whose closed-by PR
# list contains a MERGED PR.
fanout 123 --cleanup

# Check whether a released fanout binary is behind the latest GitHub Release.
# Dev builds report that update checks only apply to released versions.
fanout --check-update

# Fan out OPEN issues from a Projects v2 board instead of a parent issue.
# Default filter is Status=Todo; same-repo only. Requires `gh auth refresh
# -s read:project`. See the "Project mode" section above for the full rules.
fanout https://github.com/users/<owner>/projects/<n>

# Pick a different Status column (any single-select value works)
fanout https://github.com/orgs/<org>/projects/<n> --project-status "In Progress"

# Disable the Status filter entirely (include Done / no-status items)
fanout https://github.com/users/<owner>/projects/<n> --project-status all
```

## From inside an agent session

fanout is safe to call from an agent session (Claude Code, Codex, etc.) that
is itself running inside tmux. It only creates NEW panes for children; the
caller's pane is never touched. Pass `--agent` or set `FANOUT_AGENT` so child
panes know which agent CLI to launch.

Recommended integration for Claude Code — these assets are bundled in this
repo under `claude/` and get placed by `make install`:

- **Slash command** → `claude/commands/fanout.md` is installed to
  `~/.claude/commands/fanout.md` and invoked as `/fanout [parent-issue]
  [--go] [extra fanout flags]`. Runs `fanout <N> --dry-run` first, shows
  the target list, and only fires the real command after the user confirms
  (or if `--go` was passed).
- **Skill** → `claude/skills/fanout/SKILL.md` is installed to
  `~/.claude/skills/fanout/SKILL.md` and lets the agent recognize when
  fanout is applicable and suggest `/fanout` rather than invoking
  unprompted. In addition to gating invocation, the skill reads the parent
  body for **implicit** child references that `fanout` itself doesn't parse
  (close keywords like `Closes #N`, dependency/relation wording, plain
  bullets, Japanese idioms), lists the candidates back to the user for
  approval, and forwards the accepted numbers via `--include`.
- **Issue creation skill** → `claude/skills/fanout-issues/SKILL.md` is
  installed to `~/.claude/skills/fanout-issues/SKILL.md` and guides the
  agent when turning a plan into a fanout-ready GitHub parent issue plus
  linked child issues. It creates same-repo children, links them through
  GitHub Sub-issues, mirrors them in the parent task list, and records
  blocker waves in the `## Blocked by` / `(blocked by #N)` shapes that
  `fanout --unblocked-only` understands.

Recommended integration for Codex CLI — the skill is bundled under
`codex/` and gets placed by `make install`:

- **Skill** → `codex/skills/fanout/SKILL.md` is installed to
  `~/.codex/skills/fanout/SKILL.md`. Restart any running Codex session after
  installing. Invoke it by asking Codex to fan out a parent issue (for
  example, "fan out #123") or explicitly with `$fanout`. The skill follows the
  same safety flow as the Claude command: dry-run first, confirm targets, then
  run the real `fanout` command unless the user asked to skip confirmation.
  It also performs the implicit-child scan and generates `--name` flags.
- **Issue creation skill** → `codex/skills/fanout-issues/SKILL.md` is
  installed to `~/.codex/skills/fanout-issues/SKILL.md`. Use it by asking
  Codex to create a fanout-ready GitHub issue tree, decompose a plan into
  parent/child issues, or prepare blocker waves for `fanout --unblocked-only`.
  It mirrors the Claude issue-creation skill: same-repo children, GitHub
  Sub-issues links, parent task-list rows, and `## Blocked by` annotations.

The CLI prerequisites above still apply: start the TUI from the target
repository worktree, and for batch pane creation run from inside tmux, pass
`--agent` or set `FANOUT_AGENT`, and run from the repository whose children
should branch from the selected base.

## What fanout actually does

1. Verifies `gh`, `git`, and `tmux` are installed.
2. Resolves the repository root with `git rev-parse --show-toplevel`, the
   current tmux session with `tmux display-message -p '#{session_name}'`, and
   the invoking pane from `$TMUX_PANE` (or `#{pane_id}` as a fallback).
3. Resolves the agent from `--agent` or `FANOUT_AGENT`; live mode verifies the
   selected agent CLI is installed.
4. Enumerates children by taking the union of two sources (run from the project
   root): (a) the GitHub Sub-issues API
   (`gh api repos/{owner}/{repo}/issues/<N>/sub_issues`) for formally linked
   issues, and (b) GitHub task-list references in the parent body —
   any line matching `^\s*-\s+\[[ xX]\] ... #NUM` contributes `#NUM` (same-repo
   only; `owner/repo#NUM` is skipped). Body-sourced numbers are hydrated via
   `gh issue view`. Only `state == "OPEN"` children are processed.
5. For action-mode idempotency, it reads `.fanout/state.json` and skips
   children whose `(parent, issueNum)` pair is already recorded. It also skips
   unrecorded existing `.fanout/worktrees/<slug>` directories as a migration
   fallback for pre-state runs or interrupted launches. If the same issue is
   recorded for another parent, fanout ignores the other parent's default
   worktree but still skips an existing worktree matching the slug this current
   run would create. If `--unblocked-only` is set, each remaining candidate is also inspected
   for blockers: the child body's `## Blocked by` section (up to the next
   blank line), a trailing `(blocked by #X, #Y)` on the parent's task-list
   row, and the child's `blocked` label (weak signal — logged, not used to
   infer specific blocker numbers). Children with any OPEN blocker are
   reported as `deferred (blocked)` and skipped this run.
6. For each target issue:
   - Writes a briefing to `/tmp/fanout-<repo>-<NUM>.md` with the issue body
     and a short Requirements checklist, filtered through the resolved
     settings above.
   - Resolves the base branch (`gh repo view defaultBranchRef`, then
     `origin/HEAD`, then `main`) unless `--base-branch` is supplied.
   - Refreshes the base branch with `git fetch --quiet --no-tags` and a
     fast-forward update unless `--no-refresh` is supplied.
   - Creates `.fanout/worktrees/<slug>/` with
     `git worktree add -b <branch> <path> <base>`.
   - Creates a tmux child pane without selecting it with
     `tmux split-window -t <invoking-pane> -d -h -P -F '#{pane_id}' -c <worktree> <launch-command>`
     (`--session` uses the supplied session name as the target). The launch
     command runs through a POSIX wrapper and returns to the user's shell after
     the agent exits.
   - Sets the pane title and applies `tmux select-layout tiled`.
   - Sleeps `--sleep` seconds (default 4) before the next one.
7. Prints a summary of created / skipped / deferred / failed counts.

## Troubleshooting

### "fanout must be run inside tmux"

This only applies to batch pane-creation mode (`fanout <parent-issue|project-url>`).
Start or attach a tmux session in the repository worktree, then rerun the batch
command. To open the persistent console from a plain shell, run `fanout` with no
arguments instead.

### "agent is required"

Pass `--agent claude`, `--agent codex`, or set `FANOUT_AGENT`. Unknown agents
fail before pane creation; live mode also fails if the selected CLI is not on
`PATH`.

### "prepare worktree"

The git worktree setup failed. Check the nested git error. Common causes are a
dirty checked-out base branch that cannot be fast-forwarded, a diverged local
base branch, an existing branch name, or a stale/missing remote branch. Use
`--base-branch <branch>` to choose another base branch, including
`origin/<branch>` when you want to branch directly from the remote-tracking ref.
Use `--no-refresh` only when you intentionally want to branch from the current
local base/ref.

### "sub-issues fetch failed"

- Not authenticated: `gh auth status`.
- Parent issue doesn't exist: the Sub-issues API returns HTTP 404 and fanout
  exits 1 — check the issue number.
- Zero linked sub-issues is not an error: fanout exits 0 with
  `no sub-issues on #<parent>`.

### Slug or branch names are not what you want

By default, fanout uses `slugify(title)-<issueNum>` and
`fanout/<slug>`. Use `--name <NUM>=<slug>|<display>|<branch>` to override a
specific issue, or `--branch-prefix <prefix>` to change generated branch
names for the whole run.

### `gh pr create` is denied ("post-work-review が未実施です")

A `PreToolUse(Bash)` hook (`.claude/hooks/pre-pr-review-gate.sh`, registered in
the committed `.claude/settings.json`) blocks `gh pr create` until the current
HEAD has passed `/post-work-review`. Run `/post-work-review` — its final step
records the reviewed commit — then rerun `gh pr create`. To bypass once (e.g.
the PR that first introduces this gate, which would otherwise deny its own
creation), prefix the command: `FANOUT_SKIP_PR_REVIEW=1 gh pr create ...`.
If fanout settings resolve `prReviewGate=false`, child Claude briefings also
carry this bypass permission, but the committed hook itself remains unchanged.

Notes:
- The gate is HEAD-pinned: any new commit re-arms it, so review again before
  the PR. The marker is worktree-local, so fanout's parallel panes don't
  interfere with each other.
- Detection is a simple regex on the command string. Contorted forms (`... &&
  gh pr create`, `xargs gh pr create`) can slip through — acceptable for
  fanout's normal flow.
- `make install` overwrites a same-named global `post-work-review` skill; back
  it up first if you maintain your own copy.

## Design notes

- **One-line prompt only.** The full issue body lives in
  `/tmp/fanout-<repo>-<NUM>.md`; the pane launch prompt stays short and points
  the agent at that briefing.
- **State-store idempotency.** `.fanout/state.json` stores `schemaVersion` plus
  pane rows containing `parent`, `issueNum`, `slug`, `branchName`, `paneId`,
  `agent`, `displayName`, `worktreePath`, `prompt`, and `createdAt`. Writes use
  a sibling temp file plus rename, and live runs hold `.fanout/state.json.lock`
  while planning and launching so parallel invocations cannot both create the
  same `(parent, issueNum)` pane. Existing worktree directories without a state
  row are still treated as already fanned as a migration fallback. If the same
  child issue is already recorded for another parent or Project, default
  slug/branch generation adds a parent token before the issue suffix so the
  second run gets its own worktree instead of colliding with the first one; an
  existing worktree matching the slug this current run would create is still
  skipped for interrupted-launch recovery.
- **Direct tmux IPC.** Pane creation is synchronous because
  `tmux split-window -t <invoking-pane> -d -P -F '#{pane_id}'` returns the new
  pane id directly without selecting the child pane; no popup interception or
  completion polling is needed.

## License

This project is licensed under the MIT License — see [LICENSE](LICENSE) for details.
