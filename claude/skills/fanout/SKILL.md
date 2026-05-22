---
name: fanout
description: Spawn one dmux pane per OPEN sub-issue of a GitHub parent issue, or per OPEN item of a GitHub Projects v2 board (Project URL), via the fanout CLI. Use when the user is working in a dmux pane and wants to parallelize a parent issue's children or a Project's Todo column across independent git worktrees/agent sessions.
---

# fanout

## Synopsis

```
fanout <parent-issue|project-url>
       [--agent <name>] [--limit <N>] [--only <list>] [--skip <list>]
       [--include <list>] [--unblocked-only] [--project-status <name>]
       [--name <NUM>=<slug>[|<display>[|<branch>]]]
       [--session <tmux-session>] [--sleep <seconds>]
       [--popup-timeout <seconds>] [--dry-run] [--debug]
fanout <parent-issue> --status      # JSON status of fanned children, no side effects
```

**Do not run `fanout --help`, `fanout -h`, or `which fanout`.** This SKILL.md is the source-of-truth for the CLI surface — every flag above is documented under "Running" below, and the binary path is `/Users/butaosuinu/.local/bin/fanout` (also stated in the next paragraph). Probing the CLI directly wastes a tool call and adds nothing.

`fanout <parent-issue-or-project-url>` enumerates either a GitHub parent issue's OPEN sub-issues *or* a GitHub Projects v2 board's OPEN items, and for each child asks dmux to create a new pane with its own git worktree and an agent CLI started with a briefing that points at `/tmp/fanout-<repo>-<N>.md`. The caller's pane is not modified, so this is safe to invoke from inside an agent session that is itself running in a dmux pane.

The positional argument selects the mode: an integer (or `#N`) means **issue mode**; a URL of the form `https://github.com/(users|orgs)/<owner>/projects/<num>` means **project mode**. The two modes share everything downstream of child enumeration — briefing generation, `[fanout #N]` idempotency, `--include` / `--only` / `--skip` / `--unblocked-only` / `--name` / `--limit`, dmux popup interception — only the children come from a different source.

The CLI lives at `/Users/butaosuinu/.local/bin/fanout`; source and docs are in `/Users/butaosuinu/fanout/`.

## When to invoke

Good fits:

- The user is in a dmux-managed pane on a parent issue that has OPEN sub-issues, and asks (explicitly or implicitly) to parallelize the children.
- The user is in a dmux-managed pane and asks to fan out the OPEN issues of a GitHub Projects v2 board (often phrased as "Todo 列を並列展開" / "fan out my project board"), supplying the Project URL.
- The user types `/fanout` or mentions "fan out" / "並列展開".

Do not invoke unprompted just because an issue has sub-issues. Pane creation is visible and the user has to close each pane manually if they change their mind — suggest first, wait for a "yes", and prefer routing through the `/fanout` slash command so there is one consistent entry point.

## Pre-flight

Before running the real command:

1. **Prerequisites** — `gh`, `jq`, `tmux`, `pgrep`, and the `gh-sub-issue` extension must be installed. `fanout` validates these on startup and fails with install hints, so you can rely on its error output rather than re-checking.
2. **Live dmux session** — `tmux list-sessions -F '#{session_name} #{session_id}'` and look for any session whose `@dmux_controller_pid` option is set and alive. If none, tell the user to `cd <target-repo> && dmux` first.
3. **Agent name is required** — dmux v5.8.1 still always opens its agent-choice popup after the prompt popup, even when only one agent is enabled (v5.8.1 added `singleAgentChoicePopup.js` but only for other code paths; `selectAgentsForPaneCreation` still calls `launchAgentChoicePopup`). `fanout` drives it by injecting the agent name into the popup's result file. If you're invoking `/fanout` from inside a dmux-managed agent pane (the usual case), `fanout` auto-detects the caller's agent from `dmux.config.json` and you don't need any flag. From a plain shell outside dmux, pass `--agent <name>` or `fanout` will fail fast before touching the TUI.
4. **Body scan for implicit children** — **Issue mode only — skip this step entirely when the positional argument is a Project URL.** Project items are the source-of-truth in project mode; the Project has no parent body, and Project descriptions often reference epic / context issues that are *not* intended as children — running this scan there would push noise into `--include`. In issue mode, `fanout` itself only treats two things as children: issues returned by the Sub-issues API, and parent-body rows that match `^\s*-\s+\[[ xX]\] ... #N`. Parent issues in the wild often *describe* their children via prose instead, and those references must be surfaced to the user and forwarded as `--include`.
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
   6. Forward the accepted numbers as `--include A,B,C` to both the confirmation dry-run in step 5 and the real run.
   7. If no candidates are found, skip straight to step 5 with no `--include`.
5. **Generate pane names** — dmux's default `generateSlug()` (`dist/utils/slug.js`) calls OpenRouter if `OPENROUTER_API_KEY` is set, falls back to a local `claude --no-interactive --max-turns 1` invocation (5s timeout), else to `dmux-<timestamp>`. The `displayName` that shows in the dmux pane border defaults to the slug. The git branch name defaults to `branchPrefix + slug`. You already have each target's title and body in conversation, so generate names here — no extra tool call, no OpenRouter dependency — and forward them via `--name`:
   1. For each target issue (post-`--only`/`--skip`/`--include`/dedup-against-already-fanned, i.e. the final target set the dry-run reports), produce:
      - `slug-hint` — 2–4 kebab-case words summarizing the intent, e.g. `fix-login-timeout`, `update-docs-ja`, `cleanup-worktree`. Start with a letter or digit; only `[a-z0-9-]`. This front-loads the one-line prompt so dmux's slug LLM call echoes it; the resulting worktree directory will match the hint in the vast majority of cases. Keep it specific enough to be unique within the parent's children.
      - `display-name` — ≤40 characters, human-readable (Japanese or English OK, mixed is fine). Used for the dmux pane border title. This is what the user *sees* when switching panes, so favor clarity over brevity.
      - `branch-name` *(optional, dmux v5.8.1+)* — exact git branch name to create. Generate this only if the user's team has a branch-naming convention worth enforcing (`feat/issue-<N>-foo`, `bugfix/<slug>`, `release/v2.0`, etc.), or if `branchPrefix + slug-hint` would collide with something. fanout puts this into the newPanePopup payload as `branchName` and dmux uses it as `branchNameOverride`, completely bypassing the LLM-derived default. Skip this segment when the default is fine — over-specifying it is noise.
   2. Forward as `--name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]` — one flag per target, repeatable. Any of the three pipe-separated segments may be empty as long as at least one is non-empty. Examples: `--name 17=fix-login-timeout` (slug only), `--name 17=|Fix login timeout` (display only), `--name 17=fix-x|Fix X|feat/issue-17-x` (all three), `--name 17=||release/v2.0` (branch only).
   3. Do **not** ask the user to confirm the names — the skill runs auto-name → immediate fanout. Still include the generated names in the dry-run summary (step 6) so the user can see and course-correct before the real run if they want to.
   4. If the user runs `/fanout` with explicit `--name` flags of their own, respect those and don't override — merge so skill-generated names fill the gaps.
6. **Dry-run** — run `fanout <N-or-URL> --dry-run <forwarded-flags>` (including any `--include` from step 4 and `--name` from step 5) and show the user: the mode banner (issue / project) the CLI prints, how many children, their titles, the briefing paths, and the generated `<slug-hint> / <display-name>` pair per issue. In project mode also surface any "cross-repo item skipped" warnings — those items are intentionally excluded from fan-out. This is the confirmation step for the targets themselves (not the names). `--debug` is available if the user wants to see the popup-intercept steps on the real run.

cwd does not matter. `fanout` discovers dmux via tmux session options (`@dmux_controller_pid`, `@dmux_control_pane`, `@dmux_config_path`, `@dmux_project_root`). Do not `cd` before invoking.

## Running

- **Default**: `fanout <N-or-URL> --dry-run` → summarize → ask user to confirm → `fanout <N-or-URL>`.
- **Bypass**: if the user's invocation carries `--go`, skip the confirmation and run directly.
- **Forward extra flags** (`--agent`, `--limit`, `--only`, `--skip`, `--include`, `--unblocked-only`, `--project-status`, `--name`, `--session`, `--sleep`, `--popup-timeout`) verbatim to both the dry-run and the real run. Strip `--go` before forwarding — it is the slash command's own flag, not a `fanout` flag.
- `--only <list>` / `--skip <list>` take a comma-separated list of issue numbers (e.g. `--only 4,7,8,10`). They are mutually exclusive. `--only` numbers not in the parent's OPEN child set are warned and ignored by the CLI — if the user names issues that aren't children, relay that warning instead of silently retrying.
- `--include <list>` takes a comma-separated list of issue numbers to force-add to the children set when the Sub-issues API and parent-body task-list scan don't surface them (e.g. `--include 123,456`). This is the channel for numbers produced by the "Body scan for implicit children" step above. Numbers that end up CLOSED or don't exist are warned and skipped by the CLI. Combines cleanly with `--only`/`--skip` (included first, then filtered).
- `--unblocked-only` defers children whose blockers are still OPEN (blockers are parsed from the child body's `## Blocked by` section, a `(blocked by #X, #Y)` trailer on the parent's task-list row, or the `blocked` label as a weak signal). Prefer this over hand-maintained `--only` wave lists when the parent has explicit blocker annotations — a periodic rerun of the same command walks Wave 1 → 2 → … as blocker PRs merge. In project mode the parent-row trailer source is unavailable (no parent body), so blockers come only from the child body section and the `blocked` label.
- `--project-status <name>` (**project mode only**) filters Projects v2 items by their `Status` single-select field. Default is `Todo` — so `fanout <project-url>` with no other flags fans out only the Todo column. Pass `--project-status all` to disable the filter and include every OPEN item in the Project. Pass any single Status value (e.g. `--project-status "In Progress"`, `--project-status Backlog`) to target that column; the value is matched against the Project's Status field options case-sensitively. If the Project has no `Status` field at all, `fanout` warns and falls back to all OPEN items. Empty values are rejected (`--project-status ""` is an error). Ignored when the positional arg is an issue number — accepted on the command line but unused.
- `--name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]` is the channel for the names generated in the "Generate pane names" step. Repeatable, one per target. Slug-hint must be kebab-case (`[a-z0-9-]`, starting with alnum). Display-name is free-form (≤80 chars after dmux's sanitization). Branch-name (optional, dmux v5.8.1+) is a git branch name that bypasses dmux's `branchPrefix + slug` default — fanout writes it as `branchName` into the newPanePopup payload, which dmux's `createPane()` consumes as `branchNameOverride`. Any segment may be empty as long as at least one is non-empty (`--name 17=fix-x` slug only, `--name 17=|Disp` display only, `--name 17=||feat/x` branch only). See the skill section above for why slug-hint front-loading matters, how the display-name write is a two-file edit (dmux.config.json for in-session tmux-title, worktree-metadata.json for dmux-restart survival), and when to bother generating a branch-name override.

## Project mode notes

- **URL shape** — the CLI matches `^https://github\.com/(users|orgs)/<owner>/projects/<num>([/?].*)?$`. Both user-owned and organization-owned Projects v2 boards are supported, and any trailing `/views/<n>` segment or `?filterQuery=...` query string is preserved verbatim — the CLI extracts only the `users|orgs`, `<owner>`, and `<num>` it needs. Anything else is rejected at arg-parse time.
- **Source of truth** — children come from the Project's `items` node via GraphQL (`gh api graphql`), all pages, in board order. The Sub-issues API and parent-body scan are **not** consulted in project mode. The parent body (which doesn't exist for a Project) is not read. In project mode the CLI also skips the `gh-sub-issue` extension dependency check, so a missing extension is not a blocker.
- **`--project-status` filtering** — see the `## Running` section above. The default is `Todo`, which mirrors the common "queue everything I'm planning to start" workflow. Use `--project-status all` for a full fan-out, or a single explicit value for any other column.
- **`gh` scope** — Projects v2 GraphQL requires the `read:project` scope. If `fanout` exits with an authorization failure on the `projectV2` query (`HTTP 401` / `Resource not accessible by integration`), tell the user to run `gh auth refresh -s read:project` and retry. The default `repo` scope alone is not sufficient.
- **Cross-repo items are skipped** — items whose `content.repository.nameWithOwner` does not match the dmux project_root repo are warned and skipped. fanout's briefing / worktree paths assume a single repo (`/tmp/fanout-<repo>-<N>.md`, worktrees branched off the project_root's `main`), so cross-repo items would create panes pointing at the wrong checkout. Surface the warning rather than retrying.
- **`--include` in project mode** is allowed but rarely needed — the Project itself already defines the set. Reach for it only when the user explicitly wants to force-add an issue not currently on the board.
- **Idempotency** — `[fanout #N]` detection is keyed on child issue number, so running issue mode and project mode on the same child issue never creates a duplicate pane. Re-invocation of the same project URL after some items are fanned out only acts on the still-unfanned ones.

## After running

- Relay the `created / skipped / deferred (blocked) / deferred (--limit) / failed` summary.
- The caller's pane is untouched. Continue working on the parent issue's own scope in the current session.
- Re-invocation is idempotent: already-fanned issues are detected via the `[fanout #<N> of #<parent>]` prompt prefix in `dmux.config.json` and skipped. The parent annotation also lets `fanout --status <parent>` filter to a single parent's children when one dmux session has fanned multiple parents.

## Optional: wait-and-continue

Use this **only** when the user has indicated "fanout the children, then wait until every child PR merges and continue parent-scope work" — typical triggers are `/fanout … --wait`, an explicit "child PR が全部マージされたら統合まで進めて" / "wait for all child PRs to merge, then resume" instruction, or equivalent intent. Do not start a wait loop unprompted.

The wait loop runs **after** the real fanout run has succeeded (status=0, at least one pane created/skipped/deferred). Skip entirely if the run was a `--dry-run`.

1. Continue any parent-scope work that doesn't depend on the children's merged output.
2. When you reach a phase that requires the children's merged output, poll status via `ScheduleWakeup` with the autonomous-loop sentinel:

   ```
   ScheduleWakeup({
     delaySeconds: <see cadence below>,
     prompt: "<<autonomous-loop-dynamic>>",
     reason: "polling fanout --status #<PARENT> for all_merged"
   })
   ```

   On each wake-up, run `! fanout --status <PARENT>` and read `summary.all_merged` from the JSON.
   - **Cadence:** `summary.pending` is many → 1200–1800s; near completion (≤2 pending) → drop to ≤270s to stay inside the 5-minute prompt-cache window. Avoid 300s (cache miss without amortization).
   - `prs: []` on a child means the PR isn't open yet — treat as pending, never merged.
3. When `summary.all_merged == true`: stop scheduling wake-ups, then in the parent worktree run `git fetch origin main && git merge --ff-only origin/main`, run integration tests as appropriate for the project, and proceed with parent-issue close-out per the user's intent.
4. If the user intervenes mid-loop (any new prompt), drop the loop and follow the new instruction.

`--status` exit codes:
- `2` — cannot enumerate (config / session missing, bad invocation). Stop and report.
- `3` — `gh` API failed. Stop and report; the user may need to refresh `gh auth`.
- `0` with `summary.total == 0` — nothing has been fanned out under that parent (or every fanned pane was already torn down). Don't loop on this; tell the user.

## Failure mapping

When `fanout` exits non-zero, point the user at `/Users/butaosuinu/fanout/README.md` Troubleshooting. Common cases:

- `no active dmux session found` — user needs to `cd <repo> && dmux` first.
- `multiple dmux sessions active` — rerun with `--session <name>` (list via `tmux list-sessions -F '#{session_name}'`).
- `timed out after 60s waiting for config.json to grow` — a popup-intercept stage failed or the dmux TUI has a stray modal open. Ask the user to rerun with `--debug` to see which popup didn't appear, press `Esc` in the dmux pane until the list view is visible, then retry. On slow machines, increase `--sleep`.
- `popup did not appear within <N>s` (e.g. `agentChoicePopup did not appear within 20s`) — dmux took longer than the popup-intercept window to open the popup. On large worktrees where dmux creates the worktree between popups, increase with `--popup-timeout 45` (or higher). The default is 20s.
- `no agent resolved` — the caller isn't in a dmux-managed pane and no `--agent` was passed. Ask the user which agent to launch and retry with `--agent <name>`.
- `gh sub-issue list failed` — install `gh extension install yahsan2/gh-sub-issue` or run `gh auth status`.
- `no sub-issues on #<N>` is not a failure; fanout exits 0.
- Project mode `HTTP 401` / `Resource not accessible by integration` against `projectV2` — the user's `gh` token lacks `read:project`. Tell them to run `gh auth refresh -s read:project` and rerun.
- Project mode `no items in Project (after status/repo filter). nothing to do.` is not a failure; fanout exits 0.

## Non-goals

- Do not attempt to modify the dmux TUI state beyond running `fanout`. The script already sends one `Escape` as best-effort recovery; anything more is outside scope.
- Do not rewrite or wrap the `fanout` script. The approved interface is the CLI as-is.
- Do not assume an HTTP API on dmux. The CLI drives dmux via `tmux send-keys` because dmux v5.6.3 does not ship the documented HTTP API yet.
