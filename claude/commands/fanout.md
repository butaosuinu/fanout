---
description: Fan out a parent GitHub issue's OPEN sub-issues — or a GitHub Projects v2 board's OPEN items — into parallel tmux panes with git worktrees via the fanout CLI.
argument-hint: "[parent-issue | project-url] [--go] [extra fanout flags]"
---

Invoke the `fanout` CLI to spawn one tmux pane per OPEN sub-issue of a parent GitHub issue, or per OPEN item of a GitHub Projects v2 board. See the `fanout` skill (`~/.claude/skills/fanout/SKILL.md`) for context on when and why to use this.

Arguments: `$ARGUMENTS`

If the user is only asking to check or update the `fanout` binary itself, stop
this pane-creation workflow. Use `fanout --check-update` for read-only version
checks, or `fanout update` for immediate replacement via install.sh.

If the user is explicitly asking to start the persistent fanout TUI / console,
stop this pane-creation workflow and run `fanout` with no arguments from the
target repository worktree. TUI mode does not need a parent issue, Project URL,
`--agent`, dry-run, generated pane names, or confirmation.

## Steps

0. **Dashboard subcommand short-circuit (before any target resolution).** For detection only, look at `$ARGUMENTS` with the wrapper-only flags `--go`/`--wait` ignored — but do NOT mutate `$ARGUMENTS` itself; Steps 2/3 still need to see `--go`/`--wait` for non-dashboard calls. If, ignoring those wrapper flags, the first token is `dashboard` (`/fanout dashboard ...`, `/fanout --go dashboard`, etc.): do NOT resolve a parent target, add an agent, or run the dry-run/name-generation path. Forward the dashboard arguments with `--go`/`--wait` stripped (e.g. `fanout dashboard --web --open`) — the `dashboard` parser rejects unknown flags like `--go`/`--wait` — and stop here; it starts the standalone read-only localhost web dashboard, which takes no parent argument. Otherwise, leave `$ARGUMENTS` untouched and continue to Step 1.

1. **Resolve the parent target** from `$ARGUMENTS`. Two input shapes are accepted; the first matching token wins:
   - **Issue mode** — first token matching `^#?\d+$` → that integer is `N`. **Strip the leading `#` if present** before invoking `fanout`; the CLI only accepts bare digits in issue mode and rejects `#42`.
   - **Project mode** — first token matching `^https://github\.com/(users|orgs)/[^/]+/projects/\d+([/?].*)?$` → pass the URL to the CLI verbatim as the positional arg (no normalization, no trimming). Canonical board URLs with `/views/<n>` segments or `?filterQuery=...` query strings are accepted; the CLI parses `users` vs `orgs` and the project number itself.
   - If neither matches: scan the user's opening message / recent context for an issue ref (`#\d+`) or a Project URL of the form above, and use the first match.
   - Still nothing: actively list candidates from the current repo/worktree instead of asking for a pasted number/URL:
     1. Run `gh issue list --state open --json number,title --limit 100`.
     2. Get the repo owner login with `gh repo view --json owner -q .owner.login`.
     3. Run Project listing commands with `--limit 100`: `gh project list --format json --limit 100` for the current user's Projects, and `gh project list --owner <repo-owner> --format json --limit 100` for the repo owner's Projects. Run the repo-owner command even when the owner is a user, not only for orgs. Dedupe Projects by URL if the two lists overlap.
     4. If a Project listing command fails due auth/scope/network, warn that Project candidates could not be fully listed, keep any issue candidates, and continue. If the user needs a Project candidate, tell them to refresh `gh` Project access or paste the Project URL.
     5. Present one combined list: issues as `#<num> <title>`, Projects as `<title> (<url>)`, then ask the user to choose one.
     6. If no issue candidates and no Project candidates are available, tell the user there is no OPEN issue or Project target to fan out and stop; if Project listing failed, mention that Project candidates were unavailable rather than claiming none exist.
     7. Resolve the selection to the CLI positional arg: issues become bare digits with any leading `#` removed; Projects become the Project URL from `gh project list`.
   - This fallback is slash-command/skill-side target resolution for non-TTY agent entrypoints. Do not change the Go `fanout` CLI for it; the CLI already accepts the resolved positional arg via `internal/cliflags.Parse()`.
2. **Detect `--go`** in the remaining arguments. Strip it out — it is this command's own bypass flag, not a `fanout` flag and not a selector for the Go implementation. The rest of the arguments are forwarded verbatim to `fanout`. If the forwarded flags include lifecycle/read-only modes (`--status`, `--close`, `--merge`, `--cleanup`), do not add an agent or run the dry-run/name-generation path; execute the requested lifecycle command directly after target resolution. (The `dashboard` subcommand is already handled by Step 0 before this point.)
3. **Detect `--wait`** in the remaining arguments. Strip it out — it is template-side only and is never forwarded to the `fanout` CLI. `--wait` is meaningful only for issue-mode pane creation, not Project URLs, `--dry-run`, `--status`, `--close`, `--merge`, or `--cleanup`. If neither the user nor the environment supplies an agent for a pane-creation run, add `--agent codex` when `--codex-plan-mode` is present; otherwise add `--agent claude`, because the direct tmux runtime requires an explicit agent name.
4. **Verify the target repository cwd** before dry-run or execution. Run from the target repository worktree so `git rev-parse --show-toplevel` resolves the repo that owns the parent issue or Project items. If the current shell is not already in the intended repo, `cd` only when a reliable repo path is already known from context; otherwise stop and ask the user to run `/fanout` from the target repo worktree.
5. **Scan the parent body for implicit children** before the dry-run. **Issue mode only — skip this step entirely in project mode.** Project items are the source-of-truth for project mode; the Project has no parent body to scan, and Project descriptions often reference epic / context issues that are *not* intended as children. In issue mode, `fanout` only auto-detects children from the Sub-issues API and `- [ ] #N` task-list rows, so children that are only mentioned in prose (close keywords like `Closes #N`, `Depends on #N`, plain bullets `- #N`, Japanese idioms like `#N に関連` / `#N を対応`) won't appear unless you forward them via `--include`. See the skill doc (`~/.claude/skills/fanout/SKILL.md`, "Body scan for implicit children") for the full detection rules and the exclusion list (cross-repo refs, bare `#N`, code blocks, blockquotes, the parent itself). If you find candidates, list them to the user with one-line justifications and ask which to include; pass the accepted numbers as `--include A,B,C` to both the dry-run and the real run. Auto-accept when `--go` is set (still print the list for transparency).
6. **Project mode only: discover final targets before naming.** Run `fanout <project-url> --dry-run <forwarded>` from the target repository worktree with all selection flags and any user-supplied `--name` flags, but without newly generated `--name` flags. Use that output to learn which Project items survived Status / repo / blocker / limit filtering. This discovery dry-run still runs when `--go` was passed; it is not the confirmation step.
7. **Generate pane names for each target** — fanout has a deterministic default slug, but issue context usually allows clearer names. In issue mode you already have every target's title and body from the parent issue context; in project mode use the discovery dry-run output from step 6 and, if you need body content for naming, fetch each per-issue body with `gh issue view <num> --json body -q .body`. Produce a `<slug-hint>` (2–4 kebab-case words) and a `<display-name>` (≤40 readable chars, JP/EN OK) per target, and optionally a `<branch-name>` when the team has a branch-naming convention worth enforcing (e.g. `feat/issue-<N>-foo`). fanout appends `-<NUM>` to slug hints that do not already have that suffix; rerun idempotency comes from `.fanout/state.json`. Forward them as `--name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]` (one flag per target, repeatable; any segment may be empty as long as one is non-empty). See `~/.claude/skills/fanout/SKILL.md` "Generate pane names" for the naming policy. Do not ask the user to confirm the names individually — they'll see them in the dry-run summary and can course-correct then.
8. **Dry-run first** (unless `--go` was passed):
   - Run `fanout <target> --dry-run <forwarded>` (where `<target>` is the issue number or Project URL from step 1; include any `--include` from step 5 and `--name` from step 7) via Bash from the target repository worktree.
   - Summarize the output: the mode banner (issue / project) the CLI prints, number of targets, child issue numbers and titles, briefing file paths under `/tmp/fanout-<repo>-<N>.md`, and the generated slug-hint / display-name per issue. In project mode also relay any `cross-repo item skipped` warnings — those items are intentionally excluded.
   - Do not dump the raw command plan unless the user asks for debug detail.
   - Ask the user to confirm.
9. **Execute**:
   - Run `fanout <target> <forwarded>` via Bash.
   - Relay the `created / skipped / deferred / failed` summary.
   - If `--wait` was passed, the run exited 0, the target is an issue number, and this was not `--dry-run` or a lifecycle/read-only command, enter the wait-and-continue loop documented in `~/.claude/skills/fanout/SKILL.md`: use `ScheduleWakeup` with `prompt: "<<autonomous-loop-dynamic>>"` and poll `fanout --status <N>` until `summary.all_merged == true`, then refresh and merge the same base branch used for the fanout run before resuming parent-scope integration. Use the forwarded `--base-branch` when present; otherwise resolve fanout's default branch (`gh repo view defaultBranchRef`, then `origin/HEAD`, then `main`). Fetch the normalized remote branch and run `git merge --ff-only origin/<branch>` (or the equivalent `refs/remotes/origin/<branch>`). If the user intervenes, drop the loop.
10. **On failure**: consult `/Users/butaosuinu/fanout/README.md` Troubleshooting and surface the most likely fix. Common cases:
   - `fanout must be run inside tmux` → start or attach a tmux session first.
   - `agent is required` → rerun with `--agent claude`, `--agent codex`, or set `FANOUT_AGENT`.
   - `unknown agent` / `agent ... is not installed` → choose or install a supported agent CLI.
   - `prepare worktree` → inspect the git error; use `--no-refresh` only when skipping base refresh is intentional.
   - Missing `gh-sub-issue` extension → `gh extension install yahsan2/gh-sub-issue`.

## Notes

- `fanout` targets the invoking pane by default and uses detached splits, so it does not move focus away from the caller's pane; the agent keeps working on the parent issue in the current session. `--session` intentionally targets a named session instead.
- The command always invokes the stable `fanout` command name (the Go binary installed at `$(BINDIR)/fanout`).
- Rerun is safe for recorded panes; action mode skips children already present in `.fanout/state.json` for the same `(parent, issueNum)`, and also skips unrecorded existing `.fanout/worktrees/<slug>` directories as a migration fallback. If the same issue is recorded for another parent, only an existing worktree matching the slug this current run would create is treated as fallback. `fanout --status` reads the same state store.
- If the same child issue is already recorded for another parent or Project, fanout parent-qualifies the default slug/branch so the new run gets a separate worktree.
- Default flags the CLI already applies: `--sleep 4`, `--popup-timeout 20` (deprecated compatibility). Pass `--sleep 8` or higher if you want a longer pause between pane creations.
- Briefing settings flags are forwarded like other fanout flags: `--auto-pr` / `--no-auto-pr`, `--pr-review-gate` / `--no-pr-review-gate`, `--briefing-code-review` / `--no-briefing-code-review`, `--agent-teams-hint` / `--no-agent-teams-hint`, and `--pr-visualization` / `--no-pr-visualization`. `--pr-visualization` controls structured PR-body plus gated Mermaid briefing guidance; defaults are all on.
- `--codex-plan-mode` is forwarded like other fanout flags and is valid only with `--agent codex`. fanout starts a child Codex app-server, creates a Plan Mode thread, starts the initial Plan turn with the fanout prompt through app-server, and attaches the normal interactive Codex TUI to that remote session. If the Plan turn setup or TUI attach fails, fanout fails that launch before recording state and cleans up the pane/worktree so the child can be retried.
- To target a non-contiguous subset of children, pass `--only N1,N2,...` (keep-list) or `--skip N1,N2,...` (deny-list). The two are mutually exclusive. `--only` entries that aren't in the parent's OPEN child set are warned and ignored — surface that warning rather than rerunning or hunting for the number elsewhere. Both flags are applied before `--limit`.
- To force-add children that the Sub-issues API and `- [ ] #N` task-list scan miss (e.g. surfaced by the body scan in step 4), pass `--include N1,N2,...`. These are appended to the children set before `--only`/`--skip` filter it, so combinations like `--include 100 --only 4,7,100` behave as you'd expect. CLOSED or non-existent numbers are warned and skipped.
- `--unblocked-only` defers children whose blockers are still OPEN (blockers come from the child body's `## Blocked by` section, the parent task-list row's `(blocked by #X, #Y)` trailer, or the `blocked` label). Safe to rerun periodically as blocker PRs merge — the next run picks up newly-unblocked children automatically. Prefer this over hand-building `--only` wave lists when the parent has explicit blocker annotations. In project mode the parent-row trailer is unavailable (no parent body), so blockers come only from the child body section and the `blocked` label.
- `--project-status <name>` (**project mode only**): filter Project items by their Status field value. Default is `Todo` (i.e. `/fanout <url>` fans out only the Todo column). Pass `--project-status all` to disable the filter and fan out every OPEN item in the Project. Pass any single Status value (e.g. `--project-status "In Progress"`) to target that column. The Status field is matched case-sensitively. If the Project has no Status field at all, `fanout` warns and falls back to all OPEN items. Ignored in issue mode (the flag is accepted but unused). An empty value is rejected.
- Project mode needs the `read:project` `gh` scope on top of `repo`. If `fanout` exits with an authorization error against `projectV2` (`HTTP 401` / `Resource not accessible`), tell the user to run `gh auth refresh -s read:project` and retry.
- In project mode, items whose `content.repository.nameWithOwner` differs from the current git repository are warned and skipped. Relay the warning rather than retrying.
- To override per-pane naming, pass `--name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]` once per target. The slug hint becomes the worktree slug stem; fanout appends `-<NUM>` when missing. display-name becomes the tmux pane title, and branch-name sets the git branch directly. This is normally filled in automatically by step 5 above. If the user supplies their own `--name` for a given issue, respect it and do not override.
- Always pass an agent explicitly unless the user has already supplied `FANOUT_AGENT`; use `--agent codex` when `--codex-plan-mode` is present, otherwise choose the requested/default agent such as `--agent claude`.
- `--wait` polls `fanout --status <parent>` and is only for issue-mode pane creation runs where the user explicitly asked to wait for child PRs.

## Examples

- `/fanout 123` — dry-run preview for parent issue #123, then real run after confirmation.
- `/fanout 123 --go` — skip confirmation, run immediately.
- `/fanout 123 --limit 3 --agent codex` — only the first 3 children, launch child panes with Codex.
- `/fanout 123 --agent codex --codex-plan-mode` — launch Codex child panes by starting an app-server Plan turn and attaching interactive TUI sessions to those remote sessions.
- `/fanout 123 --no-auto-pr --no-agent-teams-hint` — omit the PR-opening requirement and Agent Teams hint from child briefings.
- `/fanout 123 --only 4,7,8,10` — fan out only these four children. `--skip 6,9` is the opposite form (deny-list).
- `/fanout 123 --unblocked-only` — only children whose blockers are all CLOSED. Great for periodic reruns that walk Wave 1 → 2 → ... automatically.
- `/fanout https://github.com/users/butaosuinu/projects/3` — project mode (default). Fans out only the Status=Todo column of the user Project.
- `/fanout https://github.com/users/butaosuinu/projects/3/views/1` — canonical board-URL form (works the same as the bare project URL; `/views/N` is preserved verbatim and the CLI ignores it).
- `/fanout https://github.com/users/butaosuinu/projects/3 --project-status all` — project mode, disable the Status filter, fan out every OPEN item.
- `/fanout https://github.com/orgs/acme/projects/12 --project-status "In Progress" --limit 5` — organization Project, In Progress column, first 5 items only.
- `/fanout` (no args in a session that started with "work on #456" or `https://github.com/.../projects/3`) — extract the issue ref or Project URL from context and proceed.
