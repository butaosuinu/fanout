---
title: CLI Reference
linkTitle: CLI Reference
description: "Every command form, flag, environment variable and exit code — the complete fanout surface."
weight: 50
kanji: 引
yomi: reference
---

## Command forms

```text
fanout # start the persistent tmux console
fanout <parent-issue|project-url>
       [--agent <name>] [--limit <N>] [--only <list>] [--skip <list>]
       [--include <list>] [--unblocked-only] [--project-status <name>]
       [--name <NUM>=<slug>[|<display>[|<branch>]]]
       [--base-branch <branch>] [--branch-prefix <prefix>] [--no-refresh]
       [--session <tmux-session>] [--sleep <seconds>]
       [--popup-timeout <seconds>] [--dry-run] [--debug]
       [--auto-pr|--no-auto-pr] [--pr-review-gate|--no-pr-review-gate]
       [--briefing-code-review|--no-briefing-code-review]
       [--agent-teams-hint|--no-agent-teams-hint]
       [--codex-plan-mode|--no-codex-plan-mode]
       [--pr-visualization|--no-pr-visualization]
fanout <parent-issue> --status [--format json|table] [--post-dashboard]
                                      # status of fanned children; optionally post dashboard
fanout <parent-issue> --merge <NUM> # fast-forward merge a recorded child branch
fanout <parent-issue> --close <NUM> # remove a recorded child worktree/pane
fanout <parent-issue> --cleanup     # remove merged/closed recorded children
fanout dashboard --web              # read-only localhost web dashboard (Session view)
fanout --check-update               # Compare this binary with the latest release
fanout update                       # Replace this binary + integrations via install.sh
fanout --help
```

## Positional argument

The positional argument accepts either a GitHub issue number (Sub-issues + task-list mode) or a Projects v2 URL (Project mode) — `https://github.com/users/<owner>/projects/<n>` or `https://github.com/orgs/<org>/projects/<n>`; the canonical `/views/<id>` suffix and trailing query strings are also accepted, so copy/paste from the browser address bar works.

`--project-status` only applies to Project mode and is ignored in issue mode.

## Child-selection flags

| Flag | Argument | Description |
|---|---|---|
| `--limit` | `<N>` | Cap how many children to enqueue this run. The remainder is printed with a rerun command. |
| `--only` | `<list>` | Comma-separated issue numbers to fan out, e.g. `--only 4,7,8,10`. Numbers not present in the OPEN child set are warned and ignored; fanout never widens the search to arbitrary issues. Cannot be combined with `--skip`. Applied before `--limit`. |
| `--skip` | `<list>` | Comma-separated issue numbers to exclude, e.g. `--skip 6,9`. Everything else in the OPEN child set is fanned out. Cannot be combined with `--only`. Applied before `--limit`. |
| `--include` | `<list>` | Force-add issue numbers that the Sub-issues API + parent task-list scan misses. Intended for the bundled Claude/Codex integrations, which read the parent body for implicit child references and forward the accepted numbers here. Numbers that end up CLOSED or don't exist are warned and skipped. |
| `--unblocked-only` | — | Only fan out children whose blockers are all CLOSED. Children with any OPEN blocker are reported as deferred in the final summary. Safe to rerun as blocker PRs merge. |
| `--project-status` | `<name\|all>` | Project mode only: restrict to Project items whose single-select `Status` field equals `<name>`. Default: `Todo`. Pass `all` to disable the filter. |

`--include` adds its numbers to the child set first, then `--only`/`--skip` filter the result, and `--limit` caps what remains. How these flags drive a wave-by-wave fan-out loop is covered in [Workflow]({{< relref "/docs/workflow" >}}).

```bash
fanout 123 --skip 6,9 --limit 3
fanout 123 --include 4,7
fanout 123 --unblocked-only --limit 3
```

## Naming and branch flags

| Flag | Argument | Description |
|---|---|---|
| `--name` | `<NUM>=<slug>[\|<display>[\|<branch>]]` | Override the default naming for issue `<NUM>`. Repeatable (once per issue). The three pipe-separated segments set the worktree slug stem, the tmux pane title and the branch name; any segment may be empty, but at least one must be non-empty. fanout appends `-<NUM>` to slug stems that do not already carry it. |
| `--base-branch` | `<branch>` | Branch to refresh and branch child worktrees from. Bare local branch names and `origin/<branch>` are supported. Default: the GitHub default branch, then `origin/HEAD`, then `main`. |
| `--branch-prefix` | `<prefix>` | Prefix for generated branch names. Default: `fanout/`. |
| `--no-refresh` | — | Skip the `git fetch` + fast-forward refresh of the base branch before creating child worktrees. |

```bash
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'
fanout 123 --name 8='feat-x|Feature X|feat/issue-8-x'   # all three segments
fanout 123 --name 9='||release/v2.0'                    # branch override only
fanout 123 --base-branch release/v2 --branch-prefix fanout/release/
```

## Run-control flags

| Flag | Argument | Description |
|---|---|---|
| `--agent` | `<name>` | Agent CLI to launch in child panes: `claude` or `codex`. Required unless `FANOUT_AGENT` is set. Unknown agents fail before pane creation; live mode also checks that the agent CLI is installed. |
| `--session` | `<tmux-session>` | Target a named tmux session instead of the invoking pane. fanout itself must still be invoked from inside tmux. |
| `--sleep` | `<seconds>` | Pause between successful pane creations. Default: `4`. A rate limit between launches, not a retry knob. |
| `--dry-run` | — | Print the git worktree, tmux split-window and agent launch commands without executing them. |
| `--debug` | — | Enable extra diagnostic logging. |

## Settings flags

These paired switches toggle fanout's opinionated behaviors for one run; a CLI flag always wins over the environment-variable and config-file layers. What each behavior actually injects — and the full resolution order — is documented in [Settings]({{< relref "/docs/settings" >}}).

| Flag | Argument | Description |
|---|---|---|
| `--auto-pr` / `--no-auto-pr` | — | Include or omit the child-briefing requirement to open a PR with `Closes #N` after tests pass. Default: on. |
| `--pr-review-gate` / `--no-pr-review-gate` | — | Keep the default PR review-gate expectation, or add a Claude briefing note allowing `FANOUT_SKIP_PR_REVIEW=1 gh pr create ...` if the hook blocks PR creation. Default: on. |
| `--briefing-code-review` / `--no-briefing-code-review` | — | Include or omit the Claude-only `/code-review` briefing instruction. Default: on. |
| `--agent-teams-hint` / `--no-agent-teams-hint` | — | Include or omit the Claude-only Agent Teams hint in child briefings. Default: on. |
| `--codex-plan-mode` / `--no-codex-plan-mode` | — | For `--agent codex`, start the initial Plan turn through Codex app-server and attach an interactive Codex TUI instead of positional `codex "<prompt>"`. Default: off. Details in [Agent Integrations]({{< relref "/docs/agents" >}}). |
| `--pr-visualization` / `--no-pr-visualization` | — | Include or omit structured PR-body plus gated Mermaid guidance in auto-PR child briefings. Default: on. |
| `--dashboard-keybind` / `--no-dashboard-keybind` | — | Register (or skip) the tmux `prefix + D` keybinding after a live fan-out, so the read-only web dashboard can be opened from any pane. Default: on. |

## Read and lifecycle modes

### `--status`

`fanout <parent> --status` is read-only: it enumerates the children recorded for that parent in `.fanout/state.json` (or `FANOUT_STATE_PATH`), queries each one through `gh api graphql` for issue state plus closed-by PR merge/review/CI status, and prints one JSON document on stdout by default.

- `--format <json|table>` — output format, default `json`. The table format adds normalized PR state (`open`, `draft`, `review-required`, `approved`, `changes-requested`, `merged`, `closed`), CI, PR diff bars, changed-file counts, Conventional-Commit type and PR links.
- `--post-dashboard` — upsert one marker-based rollup comment on the parent issue, aggregating child PR links, PR state, CI, diff size, Conventional-Commit type, TL;DR and Review effort score from machine-readable PR data. This is the only `--status` option that writes to GitHub.

Issue-mode parents only — Projects v2 URLs as parent are rejected up-front.

```bash
fanout 123 --status
fanout 123 --status | jq '.summary.all_merged'
fanout 123 --status --format table
fanout 123 --status --post-dashboard
```

`--status` is exclusive with all action-bearing flags (`--agent`, `--limit`, `--only`, `--skip`, `--include`, `--name`, `--base-branch`, `--branch-prefix`, `--no-refresh`, `--session`, `--sleep`, `--popup-timeout`, `--dry-run`, `--unblocked-only`, `--close`, `--merge`, `--cleanup`, `--auto-pr`, `--no-auto-pr`, `--pr-review-gate`, `--no-pr-review-gate`, `--briefing-code-review`, `--no-briefing-code-review`, `--agent-teams-hint`, `--no-agent-teams-hint`, `--codex-plan-mode`, `--no-codex-plan-mode`, `--pr-visualization`, `--no-pr-visualization`).

### `--merge` / `--close` / `--cleanup`

The lifecycle commands operate on entries recorded in `.fanout/state.json`; they do not discover arbitrary worktrees by scanning the filesystem. Like `--status`, they honor `FANOUT_STATE_PATH`.

- `fanout <parent> --merge <NUM>` runs `git -C <project-root> merge --ff-only <recorded-branch>`. If the merge is not a fast-forward, fanout reports the git error and does not start an editor or conflict-resolution flow.
- `fanout <parent> --close <NUM>` removes the recorded worktree with `git worktree remove <path> --force`, kills the recorded tmux pane when it is still present, removes the state entry, and runs `git worktree prune`.
- `fanout <parent> --cleanup` closes every recorded child whose issue is `CLOSED` or whose closed-by PR list contains a `MERGED` PR. Pending children remain recorded.

```bash
fanout 123 --merge 4
fanout 123 --close 4
fanout 123 --cleanup
```

## Subcommands

### `fanout dashboard`

```text
fanout dashboard --web [--port N] [--open] [--no-token] [--no-keybind]
```

Starts the read-only localhost web dashboard: bound to `127.0.0.1`, GET-only, token-gated, visualizing fanout Sessions (the recorded panes grouped by parent) with live pane liveness, issue state and PR merge status.

| Flag | Argument | Description |
|---|---|---|
| `--port` | `N` | Port to bind. Default: `0` (an OS-assigned ephemeral port); the chosen URL is printed. |
| `--open` | — | Open the URL in the default browser. Reuses a server that is already running (recorded in `.fanout/dashboard.json`) instead of starting a second one. |
| `--no-token` | — | Drop the random per-start token that gates `/api/*`. For single-user machines. |
| `--no-keybind` | — | Skip registering the tmux `prefix + D` keybinding when the dashboard starts. |

Run `fanout dashboard --help` for the full flag list.

### `fanout update`

```text
fanout update [--version <tag>] [--no-skills]
```

Replaces the running release binary plus the bundled Claude/Codex integrations through the same `install.sh` path documented in [Installation]({{< relref "/docs/installation" >}}). `--version <tag>` installs a pinned release tag by passing `FANOUT_VERSION=<tag>` to `install.sh`; `--no-skills` updates only the binary. Local dev builds refuse replacement.

### `fanout check-update`

```bash
fanout --check-update
fanout check-update
```

Read-only: fetches the latest release tag from `butaosuinu/fanout`, compares it with the binary's embedded version, and prints whether an update is available. `fanout check-update` is the accepted subcommand form of `--check-update`. Local dev builds (`version == "dev"`) do not call `gh`; they print a dev-build message and exit 0.

## Environment variables

| Variable | Description |
|---|---|
| `FANOUT_AGENT` | Default agent for child panes when `--agent` is not passed. |
| `FANOUT_STATE_PATH` | Point `--status` and the lifecycle commands directly at a state file instead of `<git-root>/.fanout/state.json`. |
| `FANOUT_AUTO_PR` | Environment layer for the PR auto-creation instruction (`autoPullRequest`). |
| `FANOUT_PR_REVIEW_GATE` | Environment layer for the PR review-gate note (`prReviewGate`). |
| `FANOUT_BRIEFING_CODE_REVIEW` | Environment layer for the Claude `/code-review` instruction (`briefingCodeReview`). |
| `FANOUT_AGENT_TEAMS_HINT` | Environment layer for the Claude Agent Teams hint (`agentTeamsHint`). |
| `FANOUT_PR_VISUALIZATION` | Environment layer for the structured PR-body and gated Mermaid guidance (`prVisualization`). |
| `FANOUT_DASHBOARD_KEYBIND` | Environment layer for the dashboard `prefix + D` tmux keybinding (`dashboardKeybind`). |
| `FANOUT_SKIP_PR_REVIEW` | One-shot bypass of the PR review-gate hook: prefix `gh pr create` with `FANOUT_SKIP_PR_REVIEW=1`. See [Troubleshooting]({{< relref "/docs/troubleshooting" >}}). |

The boolean settings variables accept `1/true/yes/on` and `0/false/no/off`, case-insensitive. Invalid values are warned and ignored. They sit between CLI flags and the config files in the settings resolution order.

## Exit codes

The default fan-out flow exits `0` on success (including "no children, nothing to do"), `1` on a prerequisite or environment problem, and `2` on bad invocation. Three modes have their own exit-code lanes:

### `--status`

| Exit code | Meaning |
|---|---|
| `0` | status emitted — check `summary.all_merged` in JSON mode for the actual state |
| `2` | cannot enumerate: bad invocation, unreadable or malformed state file, unusable project root, or a Projects v2 URL as parent. A missing state file is treated as an empty state |
| `3` | `gh` API call failed (auth, network, non-existent issue, etc.) |

### `--check-update`

| Exit code | Meaning |
|---|---|
| `0` | comparison completed, or this is a dev build |
| `2` | the current version or latest tag is not `MAJOR.MINOR.PATCH` (optionally `v`-prefixed) |
| `3` | `gh release view -R butaosuinu/fanout` failed |

### `update`

| Exit code | Meaning |
|---|---|
| `0` | update completed, or already up to date |
| `1` | environment/preflight failure: dev build, no `curl`/`wget`, unwritable binary directory, missing option value |
| `2` | unknown option, unexpected argument, or incomparable version strings |
| `3` | latest release lookup failed |

## Deprecated flags

| Flag | Argument | Description |
|---|---|---|
| `--popup-timeout` | `<seconds>` | Deprecated compatibility flag from the old runtime. Accepted but ignored by the direct tmux path. |
