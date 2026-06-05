---
description: Fan out a parent GitHub issue's OPEN sub-issues — or a GitHub Projects v2 board's OPEN items — into parallel dmux panes with git worktrees via the fanout CLI.
argument-hint: "[parent-issue | project-url] [--go] [--wait] [extra fanout flags]"
---

Invoke the `fanout` CLI to spawn one dmux pane per OPEN sub-issue of a parent GitHub issue, or per OPEN item of a GitHub Projects v2 board. See the `fanout` skill (`~/.claude/skills/fanout/SKILL.md`) for context on when and why to use this.

Arguments: `$ARGUMENTS`

If the user is only asking to check or update the `fanout` binary itself, stop
this pane-creation workflow and use the skill's self-update path:
`fanout self-update --check`, then `fanout self-update --yes` after
confirmation.

## Steps

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
2. **Detect `--go`** in the remaining arguments. Strip it out — it is this command's own bypass flag, not a `fanout` flag and not a selector for the Go implementation. The rest of the arguments are forwarded verbatim to `fanout`.
3. **Detect `--wait`** in the remaining arguments. Strip it out — it is template-side only and is **never** forwarded to the `fanout` CLI. Remember it is set so step 7 can launch the wait-and-continue loop after a successful real run. `--wait` is a no-op when combined with `--dry-run` (no real run = nothing to wait for) and only meaningful in issue mode (project URLs are rejected by `fanout --status`).
4. **Scan the parent body for implicit children** before the dry-run. **Issue mode only — skip this step entirely in project mode.** Project items are the source-of-truth for project mode; the Project has no parent body to scan, and Project descriptions often reference epic / context issues that are *not* intended as children. In issue mode, `fanout` only auto-detects children from the Sub-issues API and `- [ ] #N` task-list rows, so children that are only mentioned in prose (close keywords like `Closes #N`, `Depends on #N`, plain bullets `- #N`, Japanese idioms like `#N に関連` / `#N を対応`) won't appear unless you forward them via `--include`. See the skill doc (`~/.claude/skills/fanout/SKILL.md`, "Body scan for implicit children") for the full detection rules and the exclusion list (cross-repo refs, bare `#N`, code blocks, blockquotes, the parent itself). If you find candidates, list them to the user with one-line justifications and ask which to include; pass the accepted numbers as `--include A,B,C` to both the dry-run and the real run. Auto-accept when `--go` is set (still print the list for transparency).
5. **Generate pane names for each target** — dmux's default slug generator would otherwise call OpenRouter / the local `claude --no-interactive` fallback just to name each pane. In issue mode you already have every target's title and body from the parent issue context; in project mode pull the target numbers and titles from the first dry-run output (step 6) and, if you need body content for naming, fetch each per-issue body with `gh issue view <num> --json body -q .body`. Produce a `<slug-hint>` (2–4 kebab-case words) and a `<display-name>` (≤40 readable chars, JP/EN OK) per target, and optionally a `<branch-name>` (dmux v5.8.1+) when the team has a branch-naming convention worth enforcing (e.g. `feat/issue-<N>-foo`). Forward them as `--name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]` (one flag per target, repeatable; any segment may be empty as long as one is non-empty). See `~/.claude/skills/fanout/SKILL.md` "Generate pane names" for the naming policy. Do not ask the user to confirm the names individually — they'll see them in the dry-run summary and can course-correct then.
6. **Dry-run first** (unless `--go` was passed):
   - Run `fanout <target> --dry-run <forwarded>` (where `<target>` is the issue number or Project URL from step 1; include any `--include` from step 4 and `--name` from step 5) via Bash. cwd is irrelevant; do NOT `cd` first.
   - Summarize the output: the mode banner (issue / project) the CLI prints, number of targets, child issue numbers and titles, briefing file paths under `/tmp/fanout-<repo>-<N>.md`, and the generated slug-hint / display-name per issue. In project mode also relay any `cross-repo item skipped` warnings — those items are intentionally excluded.
   - Do not dump the raw `tmux send-keys` lines — they are long and noisy.
   - Ask the user to confirm.
7. **Execute**:
   - Run `fanout <target> <forwarded>` via Bash.
   - Relay the `created / skipped / deferred / failed` summary.
   - **If `--wait` was passed AND the run exited 0 AND at least one pane was created/skipped/deferred AND `--dry-run` was NOT passed AND the target is an issue number (not a Project URL)**: enter the wait-and-continue loop documented in `~/.claude/skills/fanout/SKILL.md` ("Optional: wait-and-continue"). Use `ScheduleWakeup` with `prompt: "<<autonomous-loop-dynamic>>"` and poll `! fanout --status <N>` until `summary.all_merged == true`, then `git fetch origin main && git merge --ff-only origin/main` in the parent worktree and proceed with integration. If the user intervenes, drop the loop.
8. **On failure**: consult `/Users/butaosuinu/fanout/README.md` Troubleshooting and surface the most likely fix. Common cases:
   - dmux not running → tell the user to `cd <target-repo> && dmux` in another shell.
   - Multiple dmux sessions alive → rerun with `--session <name>`.
   - 60s wait-for-new-pane timeout or `popup did not appear within <N>s` → a popup-intercept stage failed; ask the user to rerun with `--debug`, press `Esc` in the dmux pane, and retry. If specifically `agentChoicePopup did not appear within 20s` on a large worktree, suggest bumping `--popup-timeout 45` (or higher).
   - `no agent resolved` → the caller isn't in a dmux-managed pane; rerun with `--agent <name>`.
   - Missing `gh-sub-issue` extension → `gh extension install yahsan2/gh-sub-issue`.

## Notes

- `fanout` never touches the caller's pane; the agent keeps working on the parent issue in the current session.
- The command always invokes the stable `fanout` command name (the Go binary installed at `$(BINDIR)/fanout`).
- Rerun is safe; idempotency is handled by the `[fanout #<N> of #<parent>]` prompt prefix. The parent annotation also enables `fanout --status <parent>` to filter to one parent's children in sessions that fanned multiple parents.
- Default flags the CLI already applies: `--sleep 4`, `--popup-timeout 20`. Pass `--sleep 8` or higher on slow machines. Pass `--popup-timeout 45` (or higher) when dmux is slow to open the agent-choice popup on large worktrees.
- Briefing settings flags are forwarded like other fanout flags: `--auto-pr` / `--no-auto-pr`, `--pr-review-gate` / `--no-pr-review-gate`, `--briefing-code-review` / `--no-briefing-code-review`, and `--agent-teams-hint` / `--no-agent-teams-hint`. Defaults are all on.
- To target a non-contiguous subset of children, pass `--only N1,N2,...` (keep-list) or `--skip N1,N2,...` (deny-list). The two are mutually exclusive. `--only` entries that aren't in the parent's OPEN child set are warned and ignored — surface that warning rather than rerunning or hunting for the number elsewhere. Both flags are applied before `--limit`.
- To force-add children that the Sub-issues API and `- [ ] #N` task-list scan miss (e.g. surfaced by the body scan in step 4), pass `--include N1,N2,...`. These are appended to the children set before `--only`/`--skip` filter it, so combinations like `--include 100 --only 4,7,100` behave as you'd expect. CLOSED or non-existent numbers are warned and skipped.
- `--unblocked-only` defers children whose blockers are still OPEN (blockers come from the child body's `## Blocked by` section, the parent task-list row's `(blocked by #X, #Y)` trailer, or the `blocked` label). Safe to rerun periodically as blocker PRs merge — the next run picks up newly-unblocked children automatically. Prefer this over hand-building `--only` wave lists when the parent has explicit blocker annotations. In project mode the parent-row trailer is unavailable (no parent body), so blockers come only from the child body section and the `blocked` label.
- `--project-status <name>` (**project mode only**): filter Project items by their Status field value. Default is `Todo` (i.e. `/fanout <url>` fans out only the Todo column). Pass `--project-status all` to disable the filter and fan out every OPEN item in the Project. Pass any single Status value (e.g. `--project-status "In Progress"`) to target that column. The Status field is matched case-sensitively. If the Project has no Status field at all, `fanout` warns and falls back to all OPEN items. Ignored in issue mode (the flag is accepted but unused). An empty value is rejected.
- Project mode needs the `read:project` `gh` scope on top of `repo`. If `fanout` exits with an authorization error against `projectV2` (`HTTP 401` / `Resource not accessible`), tell the user to run `gh auth refresh -s read:project` and retry.
- In project mode, items whose `content.repository.nameWithOwner` differs from the dmux project_root repo are warned and skipped (single-repo invariant — briefings live under `/tmp/fanout-<repo>-<N>.md` and worktrees branch from the project_root's `main`). Relay the warning rather than retrying.
- To override per-pane naming (the slug-hint determines the worktree directory; the display-name is the tmux pane border title; the optional 3rd `<branch-name>` segment, dmux v5.8.1+, sets the git branch directly via dmux's `branchNameOverride`), pass `--name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]` once per target. This is normally filled in automatically by step 5 above. If the user supplies their own `--name` for a given issue, respect it and do not override.
- `fanout` auto-detects the calling pane's agent from `dmux.config.json` and injects it into dmux's agent-choice popup via popup-result-file interception. Do not pass `--agent` yourself; only pass it when the user explicitly wants to override (e.g. spawn children under a different agent than the parent pane) or when the caller isn't in a dmux-managed pane.
- If the user asks why this is complicated: dmux v5.8.1 still renders both the prompt input and the agent picker via `tmux display-popup` (separate tmux clients that `send-keys` cannot reach), so fanout intercepts each popup's `<tmpdir>/dmux-popup-*.json` result file. v5.8.1 ships an `apiActionHandler` skeleton in `dist/adapters/` but with no HTTP transport, so the intercept is still the only inbound path. See `/Users/butaosuinu/fanout/README.md` ("Why this looks weird") for details. `--debug` exposes the intercept steps.
- `--wait` is template-side only and never reaches the `fanout` binary. It tells this command to start a `ScheduleWakeup`-driven `fanout --status <PARENT>` polling loop after the real run succeeds, exit when `summary.all_merged == true`, and then `git merge --ff-only origin/main` in the parent worktree before resuming parent-scope work. See the skill's "Optional: wait-and-continue" section.

## Examples

- `/fanout 123` — dry-run preview for parent issue #123, then real run after confirmation.
- `/fanout 123 --go` — skip confirmation, run immediately.
- `/fanout 123 --limit 3 --agent codex` — only the first 3 children, override the auto-detected agent and force the picker to `codex`.
- `/fanout 123 --no-auto-pr --no-agent-teams-hint` — omit the PR-opening requirement and Agent Teams hint from child briefings.
- `/fanout 123 --only 4,7,8,10` — fan out only these four children. `--skip 6,9` is the opposite form (deny-list).
- `/fanout 123 --unblocked-only` — only children whose blockers are all CLOSED. Great for periodic reruns that walk Wave 1 → 2 → ... automatically.
- `/fanout 123 --wait` — fanout, then poll `fanout --status 123` until every child PR is MERGED, then `git merge --ff-only origin/main` and resume parent-scope work. `--wait` is parsed by this slash command, not by the CLI; issue mode only (Project URLs are rejected by `fanout --status`).
- `/fanout https://github.com/users/butaosuinu/projects/3` — project mode (default). Fans out only the Status=Todo column of the user Project.
- `/fanout https://github.com/users/butaosuinu/projects/3/views/1` — canonical board-URL form (works the same as the bare project URL; `/views/N` is preserved verbatim and the CLI ignores it).
- `/fanout https://github.com/users/butaosuinu/projects/3 --project-status all` — project mode, disable the Status filter, fan out every OPEN item.
- `/fanout https://github.com/orgs/acme/projects/12 --project-status "In Progress" --limit 5` — organization Project, In Progress column, first 5 items only.
- `/fanout` (no args in a session that started with "work on #456" or `https://github.com/.../projects/3`) — extract the issue ref or Project URL from context and proceed.
