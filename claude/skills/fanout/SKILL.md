---
name: fanout
description: Start the fanout persistent TUI console, or spawn one tmux pane per OPEN sub-issue of a GitHub parent issue / GitHub Projects v2 board item via the fanout CLI. Use when the user wants fanout to manage parallel child work from its TUI console, or explicitly asks to fan out, parallelize, or split issue / Project work across independent git worktrees and agent sessions.
---

# fanout

## Synopsis

```
fanout                            # start the persistent tmux console
fanout <parent-issue|project-url>
       [--agent <name|NUM=name>] [--limit <N>] [--only <list>] [--skip <list>]
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
       [--team]
fanout <parent-issue> --status [--format json|table] [--post-dashboard]
                                      # status of fanned children; optionally post dashboard
fanout <parent-issue> --merge <NUM>
fanout <parent-issue> --close <NUM>
fanout <parent-issue> --cleanup
fanout dashboard --web              # read-only localhost web dashboard (Session view); no parent arg
fanout plan <spec.json|plan-slug>   # issue-less local plan task fan-out (see fanout-plan)
fanout msg <verb> [options] [body...]  # peer messaging between sibling panes (see "Sibling coordination")
fanout --check-update               # Read-only version comparison
fanout update                       # Replace fanout via install.sh
```

`fanout dashboard --web` is a standalone subcommand (no parent argument): it starts a read-only, 127.0.0.1-bound web dashboard that visualizes all fanned-out Sessions live (pane liveness, issue/PR state). It is human-facing — surface it to the user when they ask to "watch"/"monitor" parallel panes, but do not run it as part of the fan-out flow. After a live fan-out, fanout also binds `prefix + D` in tmux to open it; launched panes record their owner project root so the key still opens the right dashboard from agent TUIs such as Codex when tmux reports a stale `pane_current_path`. Pass `--no-dashboard-keybind` to suppress that.

`fanout plan <spec.json|plan-slug>` is the issue-less plan-task lane. If the
user asks to fan out an implementation plan, route through the `fanout-plan`
skill (`~/.claude/skills/fanout-plan/SKILL.md`) so the agent decomposes the
plan and writes the spec JSON before invoking the deterministic CLI. The CLI
does not call an LLM and does not infer tasks from prose.

**Do not run `fanout --help`, `fanout -h`, or `which fanout`.** This SKILL.md is the source-of-truth for the CLI surface — every flag above is documented under "Running" below, and the binary path is `/Users/butaosuinu/.local/bin/fanout` (also stated in the next paragraph). Probing the CLI directly wastes a tool call and adds nothing.

`fanout <parent-issue-or-project-url>` enumerates either a GitHub parent issue's OPEN sub-issues *or* a GitHub Projects v2 board's OPEN items, and for each child creates a new tmux pane with its own git worktree under `.fanout/worktrees/` and an agent CLI started with a briefing that points at `/tmp/fanout-<repo>-<N>.md`. The caller's pane is not modified.

`fanout` with no arguments starts the persistent fanout TUI console. From a plain shell it creates or attaches a deterministic fanout-managed tmux session for the current repository, then runs the console there. From inside tmux it turns the current pane into the console. The console shows `.fanout/state.json` panes with live tmux plus issue/PR status, a `total` / `merged` / `pending` / `blocked` header rollup, and a compact Session navigator that stays fixed on the side or top depending on terminal width; `[` / `]` jump the pane table to the previous / next Session. It lets the user press `n` to open a modal and launch one or more manual prompt-based `claude` / `codex` panes from the same prompt (multi-line prompt input uses `Shift+Enter` for newline, with `Ctrl+J` as a terminal fallback; `Up` / `Down` picks an agent row, `Space` toggles it, `Left` / `Right` changes its count, and `Enter` creates the selected panes). Manual `codex` panes start in Codex Plan Mode; manual `claude` panes start normally. The console exits on `q` without killing the session or child panes. On a selected recorded pane, `c` closes it, `m` fast-forward merges its recorded branch, and `x` cleans up merged/closed siblings for the same parent after confirmation. It also compares consecutive GitHub snapshots and notifies once per transition when a child becomes merged, CI turns failing, or a child becomes waiting on an open blocker; channels are configured through fanout settings. When `watcher` is enabled from user config or the environment, this same TUI runs the label watcher: it looks for trusted `fanout:auto` issues, swaps them to `fanout:running`, and starts one-shot standalone or parent fan-out sessions. Repo config cannot enable the watcher.

The positional argument selects the mode: a bare integer means **issue mode**; a URL of the form `https://github.com/(users|orgs)/<owner>/projects/<num>` means **project mode**. User-facing issue refs like `#N` are accepted by this skill, but strip the leading `#` before invoking the CLI. The two modes share everything downstream of child enumeration — briefing generation, filters, deterministic naming, direct git worktree creation, and tmux pane launch — only the children come from a different source.

The CLI lives at `/Users/butaosuinu/.local/bin/fanout`; source and docs are in `/Users/butaosuinu/fanout/`. Always invoke the stable `fanout` command name.

## When to invoke

Good fits:

- The user asks to start, open, or return to the fanout console / TUI for the current repository.
- The user asks (explicitly or implicitly) to parallelize a parent issue that has OPEN sub-issues.
- The user asks to fan out the OPEN issues of a GitHub Projects v2 board (often phrased as "Todo 列を並列展開" / "fan out my project board"), supplying the Project URL.
- The user asks whether the installed `fanout` binary is up to date; in that case use `fanout --check-update`, not the pane-creation workflow.
- The user asks to update fanout itself; in that case run `fanout update` immediately.
- The user asks to start the fanout console / TUI; in that case run `fanout` with no arguments directly from the target repository worktree, skipping parent resolution, dry-run, pane naming, and agent selection.
- The user asks for the label watcher; in that case use the TUI-only watcher recipe below.
- The user asks to fan out an implementation plan or invokes `/fanout plan`; use the `fanout-plan` skill instead of the issue/Project workflow below.
- The user types `/fanout` or mentions "fan out" / "並列展開".

Do not invoke unprompted just because an issue has sub-issues. Pane creation is visible and the user has to close each pane manually if they change their mind — suggest first, wait for a "yes", and prefer routing through the `/fanout` slash command so there is one consistent entry point.

## Pre-flight

Before running the real command:

1. **Prerequisites** — `gh`, `git`, and `tmux` must be installed. `fanout` validates these on startup and fails with install hints, so you can rely on its error output rather than re-checking.
2. **Resolve the parent target for batch pane creation** — if the user's intent is the TUI console, skip target resolution and run `fanout` with no arguments. Otherwise, first use any issue ref (`#N` or `N`) or Projects v2 URL in the user's request / recent context. If neither is clear, actively list candidates from the current repo/worktree instead of asking for a pasted number/URL:
   1. Run `gh issue list --state open --json number,title --limit 100`.
   2. Get the repo owner login with `gh repo view --json owner -q .owner.login`.
   3. Run Project listing commands with `--limit 100`: `gh project list --format json --limit 100` for the current user's Projects, and `gh project list --owner <repo-owner> --format json --limit 100` for the repo owner's Projects. Run the repo-owner command even when the owner is a user, not only for orgs. Dedupe Projects by URL if the two lists overlap.
   4. If a Project listing command fails due auth/scope/network, warn that Project candidates could not be fully listed, keep any issue candidates, and continue. If the user needs a Project candidate, tell them to refresh `gh` Project access or paste the Project URL.
   5. Present one combined list: issues as `#<num> <title>`, Projects as `<title> (<url>)`, then ask the user to choose one.
   6. If no issue candidates and no Project candidates are available, tell the user there is no OPEN issue or Project target to fan out and stop; if Project listing failed, mention that Project candidates were unavailable rather than claiming none exist.
   7. Resolve the selection to the CLI positional arg: issues become bare digits with any leading `#` removed; Projects become the Project URL from `gh project list`.

   This is skill-side target resolution for non-TTY agent entrypoints. Do not change the Go `fanout` CLI for it; the CLI already accepts the resolved positional arg via `internal/cliflags.Parse()`.
3. **Choose the launch lane** — TUI mode is `fanout` with no arguments; it can start from a plain shell because it creates or attaches the repository's fanout-managed tmux session, and from inside tmux it uses the current pane. Batch pane-creation mode is `fanout <parent-issue|project-url>`; it must be invoked from inside tmux. By default it targets the invoking pane, not the session's currently active pane; `--session` intentionally targets a named session instead. If batch mode reports `fanout must be run inside tmux`, tell the user to start or attach a tmux session first.
4. **Agent name is required for pane creation** — pass `--agent claude` / `--agent codex`, or set `FANOUT_AGENT`. Repeat `--agent NUM=name` to override one child issue. When this skill runs fanout and the user did not provide an agent, add `--agent codex` if the forwarded flags include `--codex-plan-mode`; otherwise add `--agent claude`. `--codex-plan-mode` is valid only when every selected child uses `codex`; it uses Codex app-server to create the child Plan Mode thread, start the initial Plan turn with the fanout prompt, then attach the interactive Codex TUI to that remote session.
5. **Body scan for implicit children** — **Issue mode only — skip this step entirely when the positional argument is a Project URL.** Project items are the source-of-truth in project mode; the Project has no parent body, and Project descriptions often reference epic / context issues that are *not* intended as children — running this scan there would push noise into `--include`. In issue mode, `fanout` itself only treats two things as children: issues returned by the Sub-issues API, and parent-body rows that match `^\s*-\s+\[[ xX]\] ... #N`. Parent issues in the wild often *describe* their children via prose instead, and those references must be surfaced to the user and forwarded as `--include`.
   1. Run `gh issue view <parent> --json body -q .body` to fetch the body.
   2. Also run `fanout <parent> --dry-run <forwarded>` once to see what numbers `fanout` already auto-discovers (the two sources above). Hold on to that list so you don't suggest duplicates.
   3. Read the body and identify issue numbers that are **referred to as children** but aren't in the auto-discovered list. Typical indicators:
      - Close/fix/resolve keywords: `Closes #N`, `Fixes #N`, `Resolves #N` (any case; `Closes #1, #2, #3` is one row referring to three children).
      - Dependency / relation wording: `Depends on #N`, `Blocked by #N`, `Related to #N`, `See #N`, `Refs #N`.
      - Plain bullets without a checkbox: `- #N`, `* #N`, `+ #N`.
      - Japanese idioms: `#N に関連`, `#N を対応`, `#N 対応中`, `#N をブロック`, `#N の子issue`, `#N の子タスク`, `#N を修正`, `#N を解決` and near-variants.
   4. **Exclude** from the candidate list:
      - `owner/repo#N` cross-repo references — `fanout` only operates on the parent's repo.
      - Bare `#N` with no surrounding keyword or bullet prefix (e.g. "introduced in #12", "as noted in #99") — likely a historical reference, not a child.
      - References inside fenced code blocks (```…```) or blockquotes (`> …`) — usually quoted examples, not real children.
      - The parent issue's own number.
      - Numbers that already appear in the dry-run's target list.
   5. If candidates remain, **list them back to the user** with a one-line justification each (quote the body line that implied child status) and ask whether to include them. If `--go` was passed, still print the list (for transparency) but auto-accept.
   6. Forward the accepted numbers as `--include A,B,C` to both the confirmation dry-run in step 8 and the real run.
   7. If no candidates are found, skip straight to step 7 with no `--include`.
6. **Project mode only: discover final targets before naming.** Run `fanout <project-url> --dry-run <forwarded-flags>` from the target repository worktree with all selection flags and any user-supplied `--name` flags, but without newly generated `--name` flags. Use that output to learn which Project items survived Status / repo / blocker / limit filtering. This discovery dry-run still runs when `--go` was passed; it is not the confirmation step.
7. **Generate pane names** — fanout has a deterministic default slug (`slugify(title)-<issueNum>`), but issue context usually allows clearer names, so generate names here when useful and forward them via `--name`:
   1. For each target issue (post-`--only`/`--skip`/`--include`/dedup-against-already-fanned, i.e. the final target set the dry-run reports), produce:
      - `slug-hint` — 2–4 kebab-case words summarizing the intent, e.g. `fix-login-timeout`, `update-docs-ja`, `cleanup-worktree`. Start with a letter or digit; only `[a-z0-9-]`. This controls the worktree slug stem; fanout appends `-<issue-number>` when missing, while rerun idempotency comes from `.fanout/state.json`.
      - `display-name` — ≤40 characters, human-readable (Japanese or English OK, mixed is fine). Used for the tmux pane title. This is what the user *sees* when switching panes, so favor clarity over brevity.
      - `branch-name` *(optional)* — exact git branch name to create. Generate this only if the user's team has a branch-naming convention worth enforcing (`feat/issue-<N>-foo`, `bugfix/<slug>`, `release/v2.0`, etc.), or if `branchPrefix + slug-hint` would collide with something. Skip this segment when the default is fine — over-specifying it is noise.
   2. Forward as `--name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]` — one flag per target, repeatable. Any of the three pipe-separated segments may be empty as long as at least one is non-empty. Examples: `--name 17=fix-login-timeout` (slug only), `--name 17=|Fix login timeout` (display only), `--name 17=fix-x|Fix X|feat/issue-17-x` (all three), `--name 17=||release/v2.0` (branch only).
   3. In issue mode, use the parent issue context and the issue dry-run target set. In project mode, use the discovery dry-run output from step 6; fetch per-issue body via `gh issue view <num> --json body -q .body` only if the title alone is not enough to name the pane.
   4. Also choose a per-issue agent only when there is a clear reason and the user did not already provide `--agent NUM=name`: large refactors normally use `claude`; focused bug fixes and review follow-up normally use `codex`; docs-heavy work should stay on the default agent because Gemini is not supported in this build. Forward choices as repeatable `--agent NUM=name` and summarize them in the dry-run.
   5. Do **not** ask the user to confirm the names — the skill runs auto-name → immediate fanout. Still include the generated names in the dry-run summary (step 8) so the user can see and course-correct before the real run if they want to.
   6. If the user runs `/fanout` with explicit `--name` flags of their own, respect those and don't override — merge so skill-generated names fill the gaps.
8. **Dry-run** — run `fanout <N-or-URL> --dry-run <forwarded-flags>` (including any `--include` from step 5 and `--name` from step 7) and show the user: the mode banner (issue / project) the CLI prints, how many children, their titles, the briefing paths, generated names, worktree paths, and warnings. Treat briefing paths in dry-run output as preview paths; fanout writes the files only during the live run. In project mode also surface any "cross-repo item skipped" warnings — those items are intentionally excluded from fan-out. This is the confirmation step for the targets themselves (not the names).

Run fanout from the target repository worktree so `git rev-parse --show-toplevel` resolves the intended project root. For batch pane creation, run it from inside tmux; for the no-argument TUI, a plain shell is fine.

## Label watcher recipe

Use this only when the user asks for watcher behavior: repository-wide label
discovery and one-shot session launch while the TUI is running. The watcher is
not a scheduler, webhook, or the #107 known-parent skill loop.

1. Enable it from user config (`~/.config/fanout/config.json` or
   `$XDG_CONFIG_HOME/fanout/config.json`) or from the current shell with
   `FANOUT_WATCHER=1`. Use `FANOUT_WATCHER_AGENT` or `watcherAgent` when the
   default TUI agent is not the desired child agent.
2. Run `fanout` with no arguments and keep the TUI open. The watcher stops
   when the TUI exits.
3. Apply `fanout:auto` only when the user trusts the labeled issue and any OPEN
   children it can launch. Those issue bodies become agent briefings, so the
   label is a prompt-injection boundary.
4. On each cycle fanout swaps `fanout:auto` to `fanout:running`. Issues with no
   OPEN children launch as standalone panes under parent `@watch`; issues with
   OPEN children launch as normal parent fan-outs with `--unblocked-only` and
   the `watcherMaxSessions` budget. Deferred parent fan-outs are requeued by
   swapping `fanout:running` back to `fanout:auto`.
5. For parent fan-outs, `--merge`, `--close`, and `--cleanup` remove
   `fanout:running` best-effort. For standalone `@watch` panes, use the TUI
   lifecycle keys; the public CLI parent argument cannot target `@watch` rows.
   To run a completed standalone pane or fully cleaned parent again, add
   `fanout:auto` again.

#107 remains the known-parent loop: a skill or `/loop` keeps revisiting one
parent's children, ready labels, and blocker wave progress. Do not describe the
label watcher as that flow; it discovers labeled issues across the repository
and starts sessions once.

## Running

- **Update check**: if the user's intent is only to check the installed
  `fanout` binary version, run `fanout --check-update` and skip parent
  resolution, tmux pre-flight, dry-run, pane naming, and confirmation. It is
  read-only and creates no panes.
- **Update execution**: if the user's intent is to update the `fanout` binary
  itself, run `fanout update` immediately. The command downloads and runs the
  repository `install.sh`, passing `BIN_DIR=<current binary dir>` and
  `FANOUT_VERSION=<target>` so the installer replaces the same `fanout`
  command and refreshes bundled integrations. Use `--version <tag>` to pin a
  release and `--no-skills` to skip Claude/Codex skill installation. Actual
  replacement is only supported when the resolved executable basename is
  `fanout`. Exit codes: `0` no-op/update, `1` environment or preflight failure,
  `2` bad invocation or incomparable version, `3` latest-release lookup failed.
- **Persistent TUI**: if the user's intent is to start the fanout console, run
  `fanout` with no arguments from the target repository worktree and skip
  parent resolution, batch tmux pre-flight, dry-run, pane naming, and
  confirmation.
  TUI mode does not need a parent issue, Project URL, or `--agent`.
- **Default**: `fanout <N-or-URL> --agent claude --dry-run` → summarize → ask user to confirm → `fanout <N-or-URL> --agent claude`.
- **Bypass**: if the user's invocation carries `--go`, skip the confirmation and run directly.
- **Forward extra flags** (`--agent`, including repeatable `NUM=name` overrides, `--limit`, `--only`, `--skip`, `--include`, `--unblocked-only`, `--project-status`, `--format`, `--post-dashboard`, `--name`, `--base-branch`, `--branch-prefix`, `--no-refresh`, `--session`, `--sleep`, `--popup-timeout`, `--debug`, `--auto-pr`, `--no-auto-pr`, `--pr-review-gate`, `--no-pr-review-gate`, `--briefing-code-review`, `--no-briefing-code-review`, `--agent-teams-hint`, `--no-agent-teams-hint`, `--codex-plan-mode`, `--no-codex-plan-mode`, `--pr-visualization`, `--no-pr-visualization`, `--team`) verbatim to both the dry-run and the real run. Strip `--go` before forwarding — it is the slash command's own flag, not a `fanout` flag. If neither the user nor the environment supplies an agent, add `--agent codex` when `--codex-plan-mode` is present; otherwise add `--agent claude`.
- `--only <list>` / `--skip <list>` take a comma-separated list of issue numbers (e.g. `--only 4,7,8,10`). They are mutually exclusive. `--only` numbers not in the parent's OPEN child set are warned and ignored by the CLI — if the user names issues that aren't children, relay that warning instead of silently retrying.
- `--include <list>` takes a comma-separated list of issue numbers to force-add to the children set when the Sub-issues API and parent-body task-list scan don't surface them (e.g. `--include 123,456`). This is the channel for numbers produced by the "Body scan for implicit children" step above. Numbers that end up CLOSED or don't exist are warned and skipped by the CLI. Combines cleanly with `--only`/`--skip` (included first, then filtered).
- `--unblocked-only` defers children whose blockers are still OPEN (blockers are parsed from the child body's `## Blocked by` section, a `(blocked by #X, #Y)` trailer on the parent's task-list row, or the `blocked` label as a weak signal). Prefer this over hand-maintained `--only` wave lists when the parent has explicit blocker annotations — a periodic rerun of the same command walks Wave 1 → 2 → … as blocker PRs merge. In project mode the parent-row trailer source is unavailable (no parent body), so blockers come only from the child body section and the `blocked` label.
- `--project-status <name>` (**project mode only**) filters Projects v2 items by their `Status` single-select field. Default is `Todo` — so `fanout <project-url>` with no other flags fans out only the Todo column. Pass `--project-status all` to disable the filter and include every OPEN item in the Project. Pass any single Status value (e.g. `--project-status "In Progress"`, `--project-status Backlog`) to target that column; the value is matched against the Project's Status field options case-sensitively. If the Project has no `Status` field at all, `fanout` warns and falls back to all OPEN items. Empty values are rejected (`--project-status ""` is an error). Ignored when the positional arg is an issue number — accepted on the command line but unused.
- `--name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]` is the channel for the names generated in the "Generate pane names" step. Repeatable, one per target. Slug-hint must be kebab-case (`[a-z0-9-]`, starting with alnum) and is normalized to include `-<NUM>` when missing. Display-name is free-form. Branch-name is a git branch name that overrides `branchPrefix + slug`. Any segment may be empty as long as at least one is non-empty (`--name 17=fix-x` slug only, `--name 17=|Disp` display only, `--name 17=||feat/x` branch only).
- `--auto-pr` / `--no-auto-pr` include or omit the child briefing requirement to open a PR with `Closes #N`. `--pr-review-gate` / `--no-pr-review-gate` keep the default PR review-gate expectation or add a Claude-only escape-hatch note when the hook blocks before `/post-work-review`. `--briefing-code-review` / `--no-briefing-code-review` include or omit the Claude-only `/code-review` directive. `--agent-teams-hint` / `--no-agent-teams-hint` include or omit the Claude-only Agent Teams hint. `--pr-visualization` / `--no-pr-visualization` include or omit structured PR-body plus gated Mermaid guidance in auto-PR child briefings. These briefing settings default on.
- Lifecycle hooks are always on and come from user `hooks.json`.
- `--codex-plan-mode` / `--no-codex-plan-mode` apply only when every selected child resolves to `codex`. TUI-created manual `codex` panes use the same Plan Mode path automatically. When enabled, fanout starts a Codex app-server, creates the child Plan Mode thread, starts the initial Plan turn with the child prompt through app-server, and attaches the interactive Codex TUI to that remote session. fanout does not send `/plan` or prompt text through tmux. If Plan turn setup or TUI attach fails, fanout fails that launch before recording state and cleans up the pane/worktree so the child can be retried.
- `--team` opts the run into sibling-pane peer messaging (default off). It adds a "Coordinating with your sibling panes" section to each child's standard briefing and seeds the created panes into a per-parent peer registry. Best-effort — a registry failure never fails the fan-out. Codex Plan Mode children (`--agent codex --codex-plan-mode`) get the minimal Plan-Mode briefing, so the coordination section is skipped for them — they are still seeded and can run `fanout msg`. See "Sibling coordination" below. Suggest it when the children will touch shared files (configs, schemas, lockfiles) or have ordering dependencies that aren't already encoded as blockers; skip it for fully independent children.

## Sibling coordination (--team / fanout msg)

fanned panes are separate agent sessions (not Agent Teams teammates — that is a Claude-only, single-session feature), so they coordinate through a per-parent SQLite message bus rather than shared context. This works the same for `claude` and `codex` panes.

- **Enabling it**: pass `--team` to the fan-out (forwarded like any other flag). The CLI injects a coordination section + shared DB path into each child's standard briefing and seeds the peer registry after the batch launches. (Codex Plan Mode children get the minimal Plan briefing, so the section is skipped for them; they are still seeded and can use `fanout msg`.)
- **Using it from inside a fanned pane**: `fanout msg` auto-detects which child you are (from the tmux pane and `.fanout/state.json`) and which parent you belong to. Verbs:
  - `fanout msg peers` — live sibling roster.
  - `fanout msg inbox [--all] [--mark-read]` — unread 1:1 messages + unread board posts (`--mark-read` drains them).
  - `fanout msg board [--all]` — the shared broadcast board.
  - `fanout msg send --to <N> [--kind K] "<body>"` — 1:1 message to sibling #N.
  - `fanout msg post [--kind K] "<body>"` — post to the shared board.
  - `fanout msg mark-read [--id N ...|--all]` — mark 1:1 messages read (`--all` also advances the board cursor).
  - `fanout msg register` — (re-)register this pane in the roster.
- Common options: `--json` (machine-readable), `--self <N>` / `--parent <ref>` (override pane detection). `kind` is a free-form label (default `note`; no fixed vocabulary). Exit codes: `0` ok, `2` bad invocation, `4` SQLite backend failure.
- Coordination is **pull-based**: messages persist and a sibling reads them at its own checkpoints; `fanout msg` never interrupts a busy pane. The merged CLI has no "nudge". A good rhythm is to check `fanout msg inbox` once after reading the briefing, post a one-line heads-up before editing shared files, and check the inbox once more before opening the PR.
- **Plan mode** — `fanout plan --team` uses the same `fanout msg` surface, but peers are addressed by **task id** (`fanout msg send --to <task-id>`) instead of issue number, because issue-less plan tasks have no `#N`. See the fanout-plan skill.
- **Security**: the DB is a plaintext SQLite file under `/tmp` (`0600`, owner-only). Never put secrets, tokens, or credentials in messages.

## Project mode notes

- **URL shape** — the CLI matches `^https://github\.com/(users|orgs)/<owner>/projects/<num>([/?].*)?$`. Both user-owned and organization-owned Projects v2 boards are supported, and any trailing `/views/<n>` segment or `?filterQuery=...` query string is preserved verbatim — the CLI extracts only the `users|orgs`, `<owner>`, and `<num>` it needs. Anything else is rejected at arg-parse time.
- **Source of truth** — children come from the Project's `items` node via GraphQL (`gh api graphql`), all pages, in board order. The Sub-issues API and parent-body scan are **not** consulted in project mode. The parent body (which doesn't exist for a Project) is not read.
- **`--project-status` filtering** — see the `## Running` section above. The default is `Todo`, which mirrors the common "queue everything I'm planning to start" workflow. Use `--project-status all` for a full fan-out, or a single explicit value for any other column.
- **`gh` scope** — Projects v2 GraphQL requires the `read:project` scope. If `fanout` exits with an authorization failure on the `projectV2` query (`HTTP 401` / `Resource not accessible by integration`), tell the user to run `gh auth refresh -s read:project` and retry. The default `repo` scope alone is not sufficient.
- **Cross-repo items are skipped** — items whose `content.repository.nameWithOwner` does not match the current git repository are warned and skipped. fanout's briefing / worktree paths assume a single repo (`/tmp/fanout-<repo>-<N>.md`, worktrees under the project root), so cross-repo items would create panes pointing at the wrong checkout. Surface the warning rather than retrying.
- **`--include` in project mode** is allowed but rarely needed — the Project itself already defines the set. Reach for it only when the user explicitly wants to force-add an issue not currently on the board.
- **Idempotency** — action mode skips children already recorded in `.fanout/state.json` for the same `(parent, issueNum)` pair, and also skips unrecorded existing `.fanout/worktrees/<slug>` directories as a migration fallback. If the same issue is recorded for another parent, only an existing worktree matching the slug this current run would create is treated as fallback. The state file is written with an atomic temp+rename update while a `.fanout/state.json.lock` file is held for the run. If the same child issue is already recorded for another parent or Project, fanout parent-qualifies the default slug/branch so the new run gets a separate worktree.

## After running

- Relay the `created / skipped / deferred (blocked) / deferred (--limit) / failed` summary.
- The caller's pane is untouched. Continue working on the parent issue's own scope in the current session.
- Re-invocation skips children already recorded in `.fanout/state.json` for the same `(parent, issueNum)`. `fanout --status` reads the same state store.

## Optional: wait-and-continue

Use this only when the user explicitly asks to wait until child PRs merge and
then continue parent-scope work. After the real fanout run succeeds, poll
`fanout --status <PARENT>` from the parent worktree. The command reads
`.fanout/state.json` (or `FANOUT_STATE_PATH`) and returns
`summary.all_merged` plus `summary.blocked` for the recorded children. Use the
default JSON format for automation; `--format table` is for human review of PR
state, CI, diff stats, and links.
Use `--post-dashboard` only when the user explicitly wants a parent issue
rollup comment; it writes to GitHub even though it is attached to `--status`.

1. Continue any parent-scope work that does not depend on the children's merged output.
2. When you reach a phase that requires the children's merged output, poll status via `ScheduleWakeup` with the autonomous-loop sentinel:
   ```
   ScheduleWakeup(
     prompt: "<<autonomous-loop-dynamic>>",
     delay_seconds: 300,
     reason: "polling fanout --status #<PARENT> for all_merged"
   )
   ```
3. On each wake-up, run `fanout --status <PARENT>` and inspect `summary.all_merged`.
4. When `summary.all_merged == true`, stop scheduling wake-ups, then refresh
   and merge the same base branch used for the fanout run in the parent
   worktree. Use the forwarded `--base-branch` when present; otherwise resolve
   fanout's default branch (`gh repo view defaultBranchRef`, then `origin/HEAD`,
   then `main`). Fetch the normalized remote branch and run
   `git merge --ff-only origin/<branch>` (or the equivalent
   `refs/remotes/origin/<branch>`), then proceed with integration tests and
   parent-issue close-out.
5. Treat `prs: []` on a child as pending (PR not yet open), never merged.

`--status` exit codes:
- `2` — cannot enumerate children or state (bad invocation, unreadable or malformed state, unusable project root). A missing state file is treated as empty. Stop and report.
- `3` — `gh` API failed. Stop and report; the user may need to refresh `gh auth`.
- `0` with `summary.total == 0` — nothing has been fanned out under that parent (or every fanned pane was already torn down). Don't loop on this; tell the user.

## Failure mapping

When `fanout` exits non-zero, point the user at `/Users/butaosuinu/fanout/README.md` Troubleshooting. Common cases:

- `fanout must be run inside tmux` — batch pane creation needs a tmux session; start or attach one and rerun, or start the persistent console with no-argument `fanout` from a plain shell.
- `agent is required` — pass `--agent claude`, `--agent codex`, set `FANOUT_AGENT`, or cover every selected child with `--agent NUM=name`.
- `unknown agent` — use one of the supported agents (`claude`, `codex`).
- `agent "<name>" is not installed` — install that CLI or choose another agent.
- `prepare worktree` — inspect the git error; `--no-refresh` can bypass base branch refresh only when the stale base is intentional.
- `sub-issues fetch failed` — run `gh auth status`; an HTTP 404 means the parent issue number does not exist.
- `no sub-issues on #<N>` is not a failure; fanout exits 0.
- Project mode `HTTP 401` / `Resource not accessible by integration` against `projectV2` — the user's `gh` token lacks `read:project`. Tell them to run `gh auth refresh -s read:project` and rerun.
- Project mode `no items in Project (after status/repo filter). nothing to do.` is not a failure; fanout exits 0.

## Non-goals

- Do not rewrite or wrap the `fanout` script. The approved interface is the CLI as-is.
- Do not create extra worktrees manually; fanout owns `.fanout/worktrees/<slug>` creation.
