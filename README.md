# fanout

[English](README.md) | [日本語](README.ja.md)

Fans a GitHub parent issue's OPEN sub-issues out into one dmux pane per child.
Each pane gets its own git worktree and an agent CLI launched with a prompt
that points at a per-issue briefing file.

## Why this looks weird (dmux HTTP API investigation, popup interception)

`dmux`'s docs describe an HTTP API (`POST /api/panes`, etc.) as the obvious
ingress for a tool like this. When I investigated, I found that **the current
npm-published dmux (v5.6.3) does not ship the HTTP server**:

- `dist/**/*.js` has no HTTP routes, no `express`/`fastify`/`http.createServer`,
  no `.listen(` outside a port-probe utility.
- `dist/server/` contains only `embedded-assets.js` (a frontend bundle).
- `utils/generated-agents-doc.js` references `curl http://localhost:$DMUX_SERVER_PORT/api/panes/...`
  but there is nothing in `dist` that sets `DMUX_SERVER_PORT` — the feature is
  documented in `context/API.md` on the `main` branch but not yet shipped.

`tmux send-keys` isn't enough either. dmux's new-pane prompt and agent-choice
dialog are both rendered via `tmux display-popup -E 'node <script> <resultFile>'`
(see `dist/utils/popup.js`). A display-popup runs its child command in a
separate tmux client with its own pty; it is not a pane and cannot be
addressed by `send-keys -t <pane>`. Typing into `%0` while a popup is open
just fills `%0`'s buffer behind the popup — the user never sees the text,
and dmux discards it when the popup closes. That's why earlier versions of
this script appeared to "work" (the popup would eventually open) but never
delivered the prompt.

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
human had answered. When dmux eventually ships the HTTP API, this script
can collapse back to `POST /api/panes` in a page.

## Installation

fanout ships as a single Bash script plus its Claude Code integration
files (slash command + skill). All three are placed in one shot via the
`Makefile`:

```bash
make install        # copies CLI + command + skill into ~/.local and ~/.claude
make link           # symlinks the same three paths at the checkout (use while hacking)
make uninstall      # removes all three

PREFIX=/usr/local sudo make install     # system-wide CLI; overrides BINDIR to $PREFIX/bin
CLAUDE_DIR=/path/to/.claude make install # non-default Claude data dir
```

Installed paths:

- `$(BINDIR)/fanout` (default `~/.local/bin/fanout`)
- `$(CLAUDE_DIR)/commands/fanout.md` (default `~/.claude/commands/fanout.md`)
- `$(CLAUDE_DIR)/skills/fanout/SKILL.md` (default `~/.claude/skills/fanout/SKILL.md`)

`make install` is stable — delete the repo and the copies still work.
`make link` points at the checkout, so edits take effect immediately and
`git pull` is enough to update. Either target creates the parent
directories if they don't exist.

Confirm `~/.local/bin` is on your `PATH` (`echo $PATH | tr ':' '\n' | grep -F "$HOME/.local/bin"`).
If not, add `export PATH="$HOME/.local/bin:$PATH"` to your shell rc.

## Development

```bash
make test           # Tier 1 — flag/prereq black-box tests (bats-core required)
make lint           # shellcheck fanout + test shims
```

bats: `brew install bats-core` on macOS, `apt install bats` on Debian/Ubuntu.
Tier 1 covers the CLI surface (error messages + exit codes) that we commit to
keeping stable across future rewrites. Tier 2 (`--dry-run` golden) is being
added in a follow-up PR; Tier 3 (live dmux E2E) stays manual.

## Prerequisites

- `gh` CLI, `jq`, `tmux`, `pgrep`, and the `gh-sub-issue` extension
  (`gh extension install yahsan2/gh-sub-issue`). fanout checks these at
  startup and prints install hints on failure. Children can be declared via
  the Sub-issues API, the parent body's task-list (`- [ ] #NUM ...`), or
  both — fanout unions them.
- A running dmux session on this machine: `cd <repo> && dmux`. fanout discovers
  it by scanning tmux sessions for the `@dmux_controller_pid` option and
  checking that the PID is alive.
- **An agent name must be resolvable**: either `--agent <name>` is given, or
  the caller's pane is itself a dmux-managed pane so fanout can auto-detect
  from `dmux.config.json` (`.panes[].paneId` matched against `$TMUX_PANE`).
  dmux v5.6.3 always opens the agent-choice popup after the prompt popup,
  even when only one agent is enabled, so fanout needs a name to inject
  into it. Invoking `/fanout` from inside an agent session works out of the
  box; calling `fanout` from a plain shell requires `--agent`.
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
fanout <parent-issue> [--agent <name>] [--limit <N>] [--only <list>] [--skip <list>]
                     [--include <list>] [--unblocked-only]
                     [--name <NUM>=<slug>[|<display>]]
                     [--session <tmux-session>] [--sleep <seconds>]
                     [--popup-timeout <seconds>] [--dry-run]
fanout --help
```

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
# `Depends on #N`, plain bullets, or prose. The /fanout skill fills this in
# automatically after reading the parent body; use it directly when running
# the CLI outside a Claude Code session. CLOSED/nonexistent numbers are
# warned and skipped. Composes with --only/--skip (include first, then filter).
fanout 123 --include 4,7

# Fan out only children whose blockers are all CLOSED. Blockers are read from
# the child body's `## Blocked by` section, a trailing `(blocked by #X, #Y)`
# on the parent's task-list row, or the child's `blocked` label (weak signal,
# logged only). Safe to rerun as blocker PRs merge — drives Wave 1 → 2 → …
# with no manual bookkeeping.
fanout 123 --unblocked-only

# Cap each wave while letting fanout pick the next unblocked batch
fanout 123 --unblocked-only --limit 3

# Name each child's branch/worktree and pane title directly, bypassing
# dmux's default slug generation (which otherwise calls OpenRouter or the
# local `claude --no-interactive` fallback per pane). The slug-hint is
# front-loaded into the one-line prompt so dmux's slug LLM echoes it as the
# actual slug/branch; the display-name is written post-creation into both
# dmux.config.json (for the live tmux pane border) and the worktree's
# .dmux/worktree-metadata.json (so it survives dmux restarts). Normally the
# /fanout skill generates these from issue title/body without any extra
# API call; pass --name yourself to override. Repeatable; one per target.
fanout 123 --name 4=fix-login-timeout --name 7='update-docs|Docs update'

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
```

## From inside an agent session

fanout is safe to call from an agent session (Claude Code, Codex, etc.) that
is itself running in a dmux pane. It discovers dmux via tmux session options,
not via `$TMUX` or cwd, and it only creates NEW panes for children — the
caller's pane is never touched.

Recommended integration for Claude Code — both assets are bundled in this
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
     and a short Requirements checklist.
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
  expects. The `/fanout` Claude Code skill generates these automatically from
  issue title/body using in-conversation reasoning (no extra API call).
- If you want dmux to stop calling OpenRouter entirely, `unset
  OPENROUTER_API_KEY` before `cd <repo> && dmux`. dmux will fall through to
  the local Claude CLI fallback; combine with `--name` to keep it
  deterministic. There's no dmux flag to disable slug-LLM entirely — the
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

## Design notes

- **One-line prompt only.** ink-text-input in the dmux TUI treats Enter as
  submit, so multi-line prompts would submit too early. fanout stores the
  full briefing in `/tmp/fanout-<repo>-<NUM>.md` and tells the agent to read
  it. This also keeps the prompt short enough that dmux's `slug()` — which
  keys the worktree directory name — stays reasonable.
- **The `[fanout #NUM]` tag is the idempotency primitive.** Because dmux
  persists the prompt verbatim into `dmux.config.json`, fanout can detect
  previously-created panes by grepping for this prefix. Delete the pane (and
  its worktree) via the dmux TUI if you want fanout to recreate it.
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
