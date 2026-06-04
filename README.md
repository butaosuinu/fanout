# fanout

[English](README.md) | [日本語](README.ja.md)

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

Fans a GitHub parent issue's OPEN sub-issues out into one dmux pane per child.
Each pane gets its own git worktree and an agent CLI launched with a prompt
that points at a per-issue briefing file.

## Why this looks weird (dmux HTTP API investigation, popup interception)

`dmux`'s docs describe an HTTP API (`POST /api/panes`, etc.) as the obvious
ingress for a tool like this. When I investigated, I found that **the current
npm-published dmux (v5.8.1, re-verified) still does not ship the HTTP server**:

- `dist/**/*.js` has no HTTP routes, no `express`/`fastify`/`http.createServer`,
  no `.listen(` outside a port-probe utility.
- `dist/server/` contains only `embedded-assets.js` (a frontend bundle).
- `dist/adapters/apiActionHandler.js` exists alongside `tuiActionHandler.js`
  (v5.8.1 added the split) and exposes `actionResultToAPIResponse()` plus a
  callback registry, but there is no transport layer wired up — it's a
  skeleton, not a server.
- `utils/generated-agents-doc.js` references `curl http://localhost:$DMUX_SERVER_PORT/api/panes/...`
  but nothing in `dist` sets `DMUX_SERVER_PORT`. The feature is documented in
  `context/API.md` on the `main` branch but not yet shipped.

`tmux send-keys` isn't enough either. dmux's new-pane prompt and agent-choice
dialog are both rendered via `tmux display-popup -E 'node <script> <resultFile>'`
(see `dist/utils/popup.js`). A display-popup runs its child command in a
separate tmux client with its own pty; it is not a pane and cannot be
addressed by `send-keys -t <pane>`. Typing into `%0` while a popup is open
just fills `%0`'s buffer behind the popup — the user never sees the text,
and dmux discards it when the popup closes. That's why earlier approaches
appeared to "work" (the popup would eventually open) but never delivered the
prompt.

The shipped workaround is **popup result-file interception**. Each dmux
popup is told a `<tmpdir>/dmux-popup-<timestamp>.json` path (typically
`/tmp/` on Linux, `/var/folders/.../T/` on macOS) where the popup
writes its user-entered answer; dmux reads that file when the popup child
exits. fanout triggers the popup by send-keys'ing `Escape n`, then uses
`pgrep` + `ps` to locate the popup process and its resultFile, atomically
writes the desired JSON payload (`{"success":true,"data":"<prompt>"}` for
the prompt popup, `{"success":true,"data":["<agent>"]}` for the picker),
and kills the popup process. `display-popup -E` closes the popup on child
exit, dmux reads the file we wrote, and pane creation proceeds as if a
human had answered. When dmux eventually ships the HTTP API, fanout
can collapse back to `POST /api/panes` in a page.

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
  dmux `@dmux_project_root` repo are warned and skipped; fanout still
  assumes one repo per run.
- **Status field missing on the Project?** fanout warns and falls back to
  every item regardless of `--project-status`.
- **Idempotency, `[fanout #N]` detection, and `--unblocked-only` work
  identically.** Blockers in this mode come only from the child body's
  `## Blocked by` section and the `blocked` label; the
  `(blocked by #X)` task-list trailer doesn't exist without a parent body.

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
The release workflow builds darwin binaries on macOS with Go 1.23, so the Go
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

Building from a checkout needs a **Go toolchain** (Go 1.23+): `make install`,
`make link`, and `make build-go` all run `go build ./cmd/fanout`. The curl
install above ships a prebuilt binary and needs no Go.

## Development

```bash
make test           # Go unit tests + Tier 1 + Tier 2 black-box tests (bats-core required)
make test-tier1     # flag/prereq tests only
make test-tier2     # --dry-run golden tests against fixture scenarios
make lint           # go vet + gofmt + shellcheck of the test shims
make build-go       # build the Go CLI as ./fanout-go
```

bats: `brew install bats-core` on macOS, `apt install bats` on Debian/Ubuntu.
The black-box tiers build `./fanout-go` and exercise it via `FANOUT_BIN`.
Tier 1 locks the CLI surface (error messages + exit codes); Tier 2 locks the
`--dry-run` planning output against fixture scenarios under `tests/fixtures/`.
Regenerate Tier 2 goldens with `FANOUT_GOLDEN_UPDATE=1 make test-tier2` when you
intentionally change dry-run output. Tier 3 (live dmux E2E) stays manual.

## Prerequisites

- `gh` CLI, `jq`, `tmux`, `pgrep`, and the `gh-sub-issue` extension
  (`gh extension install yahsan2/gh-sub-issue`). fanout checks these at
  startup and prints install hints on failure. Children can be declared via
  the Sub-issues API, the parent body's task-list (`- [ ] #NUM ...`), or
  both — fanout unions them.
- **Project mode only**: the `gh` CLI must have the `read:project` scope so
  the GraphQL query that lists Project items can succeed. Add it with
  `gh auth refresh -s read:project`. Issue-mode (`fanout <N>`) does not
  need this scope.
- A running dmux session on this machine: `cd <repo> && dmux`. fanout discovers
  it by scanning tmux sessions for the `@dmux_controller_pid` option and
  checking that the PID is alive.
- **An agent name must be resolvable**: either `--agent <name>` is given, or
  the caller's pane is itself a dmux-managed pane so fanout can auto-detect
  from `dmux.config.json` (`.panes[].paneId` matched against `$TMUX_PANE`).
  dmux v5.8.1 still always opens the agent-choice popup after the prompt
  popup, even when only one agent is enabled (the new `singleAgentChoicePopup`
  exists only for other code paths), so fanout needs a name to inject into
  it. Invoking through the bundled Claude/Codex integration from inside
  an agent session works out of the box; calling `fanout` from a plain shell
  requires `--agent`.
- **The dmux TUI must be on the pane-list view** (no modal / no prompt open)
  when fanout runs. fanout sends one `Escape` before each pane-creation
  sequence to recover from stray popups, but cannot unstick an interactive
  $EDITOR or a confirm dialog.
- **HEAD of the repo should be the base you want children built on**. dmux's
  TUI does not let external callers specify a base ref per pane; the worktree
  branches off whatever HEAD resolves to when dmux creates it. Do
  `git checkout <target>` before calling fanout if the parent issue expects
  something other than the default branch.

## Usage

```
fanout <parent-issue|project-url>
       [--agent <name>] [--limit <N>] [--only <list>] [--skip <list>]
       [--include <list>] [--unblocked-only] [--project-status <name>]
       [--name <NUM>=<slug>[|<display>[|<branch>]]]
       [--session <tmux-session>] [--sleep <seconds>]
       [--popup-timeout <seconds>] [--dry-run]
       [--auto-pr|--no-auto-pr] [--pr-review-gate|--no-pr-review-gate]
       [--briefing-code-review|--no-briefing-code-review]
       [--agent-teams-hint|--no-agent-teams-hint]
fanout <parent-issue> --status      # JSON status of fanned children, no side effects
fanout --check-update               # Compare this binary with the latest release
fanout --help
```

The positional accepts either a GitHub issue number (Sub-issues +
task-list mode) or a Projects v2 URL (Project mode; see above).
`--project-status` only applies to Project mode and is ignored otherwise.

### `--status` output

`fanout <parent> --status` is read-only: it reads `dmux.config.json` to
enumerate children already fanned out under that specific parent (panes
whose prompt starts with `[fanout #N of #<parent>]`), queries each child
through a single `gh api graphql` call against
`repository.issue.closedByPullRequestsReferences(first: 100)` (cursor-
paginated when a child is closed by more than 100 PRs) so the response
carries `state`/`mergedAt` directly, and prints one JSON document on
stdout. Issue-mode parents only —
Projects v2 URLs as parent are rejected up-front (panes for project items
carry the URL in their prefix, which `--status`'s strict filter doesn't
address). In a session that has fanned multiple parents, children of
other parents are filtered out so `summary.all_merged` reflects only the
requested parent. Old-format panes that predate this feature
(`[fanout #N]` without parent annotation) are excluded; the next
non-status `fanout <parent>` run rewrites those panes' prompts in place
to add the parent annotation, so a subsequent `--status` picks them up
automatically (no need to delete and re-fan). Set `DMUX_CONFIG_PATH` to
point directly at a `dmux.config.json` when the dmux session has already
exited.

```json
{
  "parent": 123,
  "children": [
    { "num": 4, "state": "CLOSED",
      "prs": [ { "number": 250, "state": "MERGED",
                 "mergedAt": "2026-05-04T10:00:00Z" } ],
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

- `0` — JSON emitted (check `summary.all_merged` for the actual state).
- `2` — cannot enumerate (bad invocation, missing/unreadable
  `dmux.config.json`, no active dmux session, Projects v2 URL as parent).
- `3` — `gh` API call failed (auth, network, non-existent issue, etc.).

`--status` is exclusive with all action-bearing flags (`--agent`, `--limit`,
`--only`, `--skip`, `--include`, `--name`, `--sleep`, `--popup-timeout`,
`--dry-run`, `--unblocked-only`, `--auto-pr`, `--no-auto-pr`,
`--pr-review-gate`, `--no-pr-review-gate`, `--briefing-code-review`,
`--no-briefing-code-review`, `--agent-teams-hint`, `--no-agent-teams-hint`).
The bundled Claude Code skill drives a `ScheduleWakeup`-based polling loop on
top of this when the user opts in via `/fanout … --wait`.

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

### Settings

The Go implementation can turn four opinionated briefing behaviors on or off.
The deprecated Bash `./fanout` does not support these new flags, files, or env
vars. Defaults are all `true` to preserve existing behavior.

Resolution order is: **CLI flag > environment variable > repo config file >
user config file > built-in default**. fanout applies layers in the reverse
order once per run after it discovers dmux's project root. The repo config path
is `<project_root>/.fanout/config.json`, where `project_root` is the dmux
session's parent repository root, not the child worktree. The user config path
is `$XDG_CONFIG_HOME/fanout/config.json`, or `~/.config/fanout/config.json`
when `XDG_CONFIG_HOME` is unset.

```json
{
  "autoPullRequest": false,
  "prReviewGate": true,
  "briefingCodeReview": true,
  "agentTeamsHint": false
}
```

| Behavior | File key | Env | CLI flags | Default |
|---|---|---|---|---|
| PR auto-creation instruction | `autoPullRequest` | `FANOUT_AUTO_PR` | `--auto-pr` / `--no-auto-pr` | `true` |
| PR review gate note | `prReviewGate` | `FANOUT_PR_REVIEW_GATE` | `--pr-review-gate` / `--no-pr-review-gate` | `true` |
| Claude `/code-review` instruction | `briefingCodeReview` | `FANOUT_BRIEFING_CODE_REVIEW` | `--briefing-code-review` / `--no-briefing-code-review` | `true` |
| Claude Agent Teams hint | `agentTeamsHint` | `FANOUT_AGENT_TEAMS_HINT` | `--agent-teams-hint` / `--no-agent-teams-hint` | `true` |

Environment values accept `1/true/yes/on` and `0/false/no/off`
(case-insensitive). Invalid env values, unknown file keys, and non-boolean
file values are warned and ignored so future settings do not break older
fanout binaries.

`prReviewGate=false` is the one setting that cannot forcibly disable the child
Claude Code hook, because fanout only creates the pane through dmux's popup
path and cannot inject child environment variables. Instead, Claude briefings
include a note allowing `FANOUT_SKIP_PR_REVIEW=1 gh pr create ...` if the
`PreToolUse` hook blocks PR creation before `/post-work-review`.

### Examples

```bash
# Fan out all OPEN sub-issues of #123
fanout 123

# Preview what would happen, don't actually drive dmux
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

# Name each child's worktree slug, pane title, and branch directly. The
# slug-hint is front-loaded into the one-line prompt so dmux's slug LLM
# echoes it as the worktree directory name (dmux's slug LLM otherwise calls
# OpenRouter or the local `claude --no-interactive` fallback per pane). The
# display-name is written post-creation into both dmux.config.json (for the
# live tmux pane border) and the worktree's .dmux/worktree-metadata.json (so
# it survives dmux restarts). The optional 3rd segment is a branch-name
# override (dmux v5.8.1+): when present, fanout puts it into the newPanePopup
# payload as `branchName` and dmux's createPane() uses it as
# `branchNameOverride`, bypassing the default `branchPrefix + slug`. Any of
# the three pipe-separated segments may be empty, but at least one must be
# non-empty. Normally the bundled Claude/Codex integrations generate these
# from issue title/body without any extra API call; pass --name yourself to
# override. Repeatable; one per target.
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'
fanout 123 --name 8='feat-x|Feature X|feat/issue-8-x'   # all three segments
fanout 123 --name 9='||release/v2.0'                    # branch override only

# Pick a specific session when you have multiple dmux instances alive
fanout 123 --session work-repo

# Give dmux 8 seconds between creations (useful on slow machines)
fanout 123 --sleep 8

# Wait longer for each dmux popup to appear (useful when worktree creation
# between popups is slow on large repos; default 20s)
fanout 123 --popup-timeout 45

# Override the auto-detected agent (e.g. spawn children under a different
# agent than the parent pane). Normally you don't need this — fanout reads
# the caller's .panes[].agent from dmux.config.json.
fanout 123 --agent codex

# Remove the automatic PR-opening requirement from child briefings for one run
fanout 123 --no-auto-pr

# Disable the Agent Teams hint globally for this shell
export FANOUT_AGENT_TEAMS_HINT=0

# Read-only JSON status: who's fanned out, what state each child is in, and
# whether their closed-by PRs have merged. No side effects. Pipe into jq for
# scripting; the bundled /fanout --wait skill drives a wait-and-continue loop
# on top of this.
fanout 123 --status
fanout 123 --status | jq '.summary.all_merged'

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
is itself running in a dmux pane. It discovers dmux via tmux session options,
not via `$TMUX` or cwd, and it only creates NEW panes for children — the
caller's pane is never touched.

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

The CLI prerequisites above still apply: the dmux session must be alive,
the TUI must be on the pane-list view, and only one agent should be
enabled (or `--agent` passed). See **Prerequisites** and **Troubleshooting**
for details.

## What fanout actually does

1. Verifies `gh`, `jq`, `tmux`, `gh-sub-issue` are installed.
2. Enumerates tmux sessions. A session is considered dmux-managed iff
   `@dmux_controller_pid` is set and the PID is alive.
3. Reads the session's `@dmux_control_pane`, `@dmux_config_path`,
   `@dmux_project_root` options to locate the TUI's pane, the
   `.dmux/dmux.config.json` file, and the repo root.
4. Enumerates children by taking the union of two sources (run from the project
   root): (a) `gh sub-issue list <parent>` for issues formally linked via the
   Sub-issues API, and (b) GitHub task-list references in the parent body —
   any line matching `^\s*-\s+\[[ xX]\] ... #NUM` contributes `#NUM` (same-repo
   only; `owner/repo#NUM` is skipped). Body-sourced numbers are hydrated via
   `gh issue view`. Only `state == "OPEN"` children are processed.
5. For idempotency, it scans `dmux.config.json`'s `panes[].prompt` for any
   existing prompt starting with `[fanout #<NUM>]` and skips those issues.
   If `--unblocked-only` is set, each remaining candidate is also inspected
   for blockers: the child body's `## Blocked by` section (up to the next
   blank line), a trailing `(blocked by #X, #Y)` on the parent's task-list
   row, and the child's `blocked` label (weak signal — logged, not used to
   infer specific blocker numbers). Children with any OPEN blocker are
   reported as `deferred (blocked)` and skipped this run.
6. For each target issue:
   - Writes a briefing to `/tmp/fanout-<repo>-<NUM>.md` with the issue body
     and a short Requirements checklist, filtered through the resolved
     settings above.
   - Sends `Escape` and `n` to the control pane, which triggers dmux's
     new-pane popup (a `tmux display-popup` child, not an inline modal).
   - Finds the popup's node process with `pgrep -f 'newPanePopup.js'`,
     reads its `<tmpdir>/dmux-popup-*.json` resultFile path from `ps -o args=`,
     atomically writes `{"success":true,"data":"[fanout #<NUM>] <TITLE>: read /tmp/fanout-<repo>-<NUM>.md and begin."}`,
     and kills the popup process so dmux reads the injected answer.
   - Repeats the intercept for the agent-choice popup that dmux launches
     next (writes `{"success":true,"data":["<agent>"]}`), using the agent
     resolved via `--agent` or auto-detected from the calling pane.
   - Polls `dmux.config.json` until `panes[].length` increases (timeout 60s).
   - Sleeps `--sleep` seconds (default 4) before the next one.
7. Prints a summary of created / skipped / deferred / failed counts.

## Troubleshooting

### "no active dmux session found"

You haven't run `dmux` yet in a tmux session, or the controller process died.
Check: `tmux show-options -v -t <session> @dmux_controller_pid`. If empty,
the session never hosted dmux. If non-empty but `kill -0 <pid>` fails, dmux
crashed — restart it.

### "multiple dmux sessions active"

Pass `--session <name>`. List them with
`tmux list-sessions -F '#{session_name}'`.

### Pane creation times out ("timed out after 60s waiting for config.json to grow")

The TUI probably wasn't on the pane-list view when fanout fired the key
sequence, or popup interception failed. Check whether:

- A popup (confirm dialog, agent picker, etc.) is stuck — press `Esc` in the
  dmux pane until it returns to the list, then rerun.
- dmux is genuinely slow (cold clone, huge repo, npm install hook). Increase
  `--sleep` and retry; the wait-for-new-pane timeout is 60s per issue.
- Rerun with `--debug` to see which intercept stage failed. Common cases:
  - `newPanePopup did not appear within 20s` — dmux didn't react to `n`,
    usually because another popup was already on screen. Send `Esc` manually
    and rerun.
  - `agentChoicePopup did not appear within 20s` — dmux closed the first
    popup but the agent-choice popup didn't follow within the window. On
    slow machines or very large worktrees the gap can exceed the default
    — retry with `--popup-timeout 45` (or higher). If it still never
    appears, check that your dmux settings actually enable at least one
    agent.
- You upgraded dmux past v5.6.x and the popup script names or the result
  JSON shape changed. Inspect `~/.../dmux/dist/utils/popup.js` and
  `dist/components/popups/shared/PopupWrapper.js`; the intercept in fanout
  assumes `{"success":true,"data":...}`. Raise an issue if dmux changed it.

### "gh sub-issue list failed"

- No `gh-sub-issue` extension: `gh extension install yahsan2/gh-sub-issue`.
- Not authenticated: `gh auth status`.
- Parent issue doesn't exist or has no sub-issues tagged via the extension:
  fanout exits 0 with `no sub-issues on #<parent>`.

### Panes end up with ugly auto-generated slugs or OpenRouter burns tokens

dmux's `dist/utils/slug.js` computes the branch/worktree slug by asking an LLM
for "1-2 word kebab-case slug for this prompt" every time a pane is created.
It tries OpenRouter first (requires `OPENROUTER_API_KEY`), then falls back to
`claude --no-interactive --max-turns 1` (5s timeout, costs tokens), then to
`dmux-<timestamp>` if both fail. Two ways to control this:

- Pass `--name <NUM>=<slug-hint>` per issue. fanout front-loads the hint into
  the one-line prompt so the slug LLM echoes it. The hint must be kebab-case
  (`[a-z0-9-]`, starting with alnum) — that's the shape the slug sanitizer
  expects. The bundled Claude/Codex integrations generate these automatically
  from issue title/body using in-conversation reasoning (no extra API call).
- For the **branch name** (separate from the worktree slug since dmux v5.8.1),
  use the optional 3rd `--name` segment: `--name <NUM>=<slug>|<display>|<branch>`.
  fanout writes the branch into the newPanePopup payload as `branchName` and
  dmux's `createPane()` consumes it as `branchNameOverride`, completely
  bypassing the `branchPrefix + slug` default. The worktree directory still
  follows the slug, so the slug-hint trick above is still useful for that
  axis. Branch override is the right knob when team naming conventions
  matter (`feat/issue-N-foo`, `release/v2.0`, etc.).
- If you want dmux to stop calling OpenRouter entirely, `unset
  OPENROUTER_API_KEY` before `cd <repo> && dmux`. dmux will fall through to
  the local Claude CLI fallback; combine with `--name` to keep it
  deterministic. There's no dmux flag to disable the slug LLM entirely — the
  only way to fully bypass LLM slug generation through the popup-intercept
  path is to front-load the slug as a hint.

The display-name (what shows in the dmux pane border) is a separate axis:
fanout writes `panes[].displayName` in `dmux.config.json` and merges
`displayName` into `<worktree>/.dmux/worktree-metadata.json` after each pane
is created, and dmux's `enforcePaneTitles` (5-30s poll) pushes it into the
tmux pane title within that window. Across dmux restarts the worktree-metadata
copy is what survives (via `reopenWorktree`).

### Prompts show junk in the dmux TUI

The prompt text is now injected via the popup resultFile, not via
`send-keys -l`, so UTF-8 titles round-trip cleanly through dmux. If you
still see garbled characters, check that `jq` on the caller's side produces
valid JSON (`echo "<title>" | jq -Rs` should return a quoted string with
escapes) and that `dmux.config.json` stores it unchanged. Use `--dry-run`
to print the exact JSON that would be written.

### `.gitignore` got a `.dmux/` line you didn't write

That's dmux itself doing it on startup (seen as soon as `dmux --help` runs in
a repo directory). Not a fanout bug.

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

- **One-line prompt only.** ink-text-input in the dmux TUI treats Enter as
  submit, so multi-line prompts would submit too early. fanout stores the
  full briefing in `/tmp/fanout-<repo>-<NUM>.md` and tells the agent to read
  it. This also keeps the prompt short enough that dmux's `slug()` — which
  keys the worktree directory name — stays reasonable.
- **The `[fanout #NUM of #PARENT]` tag is the idempotency primitive.**
  Because dmux persists the prompt verbatim into `dmux.config.json`, fanout
  can detect previously-created panes by grepping for this prefix. The
  parent annotation also lets `fanout --status <parent>` filter to one
  parent's children in a session that has fanned multiple parents. Older
  panes carry the legacy `[fanout #NUM]` form (no parent annotation) and
  still satisfy idempotency; the next non-status `fanout <parent>` run
  auto-rewrites those prompts in place to add the parent annotation so
  `--status` picks them up. Delete the pane (and its worktree) via the
  dmux TUI if you want fanout to recreate it from scratch.
- **IPC paths in play.** Discovery uses tmux session options
  (`@dmux_controller_pid`, `@dmux_control_pane`, `@dmux_config_path`,
  `@dmux_project_root`). Pane-creation is driven by writing to dmux's
  popup resultFiles (`<tmpdir>/dmux-popup-*.json`) after locating the popup
  process via `pgrep` + `ps -o args=`. No HTTP, no sockets, no named
  pipes — this is intentionally ugly; it's what the current dmux surface
  area allows.
- **Rate limiting via `--sleep`.** dmux's `usePaneCreation` uses a bounded
  parallel queue internally, but from the TUI side you can only open one
  "new pane" dialog at a time. The sleep gives dmux time to finish the
  worktree-creation phase before the next `n` is sent.

## License

This project is licensed under the MIT License — see [LICENSE](LICENSE) for details.
