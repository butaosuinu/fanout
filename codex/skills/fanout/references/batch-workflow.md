# Batch workflow reference

Use this reference for `fanout <parent-issue|project-url>` runs.

## Contents

- [Target resolution](#target-resolution)
- [Flags and selection](#flags-and-selection)
- [Implicit child scan](#implicit-child-scan)
- [Project target discovery](#project-target-discovery)
- [Pane names](#pane-names)
- [Agent selection](#agent-selection)
- [Preview and execution](#preview-and-execution)
- [Wait and continue](#wait-and-continue)
- [Failure mapping](#failure-mapping)

## Target resolution

Accept these positional forms:

- Issue mode: bare digits. Strip a user-facing leading `#` before invoking the
  CLI.
- Project mode: a URL matching
  `https://github.com/(users|orgs)/<owner>/projects/<num>` with an optional
  path or query suffix. Pass the full URL unchanged.

If recent context does not identify one target:

1. Run `gh issue list --state open --json number,title --limit 100`.
2. Resolve the repository owner with
   `gh repo view --json owner -q .owner.login`.
3. Run `gh project list --format json --limit 100` and
   `gh project list --owner <repo-owner> --format json --limit 100`. Run the
   owner command for both user and organization owners. Dedupe by Project URL.
4. Keep issue candidates when Project listing fails. Warn about the failed
   scope or network lookup instead of claiming that no Projects exist.
5. Present one compact candidate list and ask the user to choose.
6. Stop when no issue candidate exists and no Project candidate is available.

Do not change the Go CLI to perform conversational target discovery.

## Flags and selection

Forward user-supplied flags supported by the issue/Project lane:

- `--agent <name|NUM=name>`
- `--limit <N>`, `--only <list>`, `--skip <list>`, `--include <list>`
- `--unblocked-only`
- `--project-status <name>` in Project mode
- `--name <NUM>=<slug>[|<display>[|<branch>]]`
- `--base-branch <branch>`, `--branch-prefix <prefix>`, `--no-refresh`
- `--session <name>`, `--sleep <seconds>`, `--popup-timeout <seconds>`
- `--debug`
- `--auto-pr` / `--no-auto-pr`
- `--pr-review-gate` / `--no-pr-review-gate`
- `--briefing-code-review` / `--no-briefing-code-review`
- `--agent-teams-hint` / `--no-agent-teams-hint`
- `--pr-visualization` / `--no-pr-visualization`
- `--dashboard-keybind` / `--no-dashboard-keybind`
- `--team`

Forward `--format` and `--post-dashboard` only with `--status`. Do not mix
status or lifecycle operations into pane creation.

Apply `--include` before `--only` or `--skip`. Apply those filters before
`--limit`. Let the CLI warn about selected numbers outside the OPEN child set.

Treat user-facing `--go` as a wrapper instruction, not a CLI flag. Strip it
before execution.

## Implicit child scan

Run this scan only for issue mode. The CLI already discovers GitHub Sub-issues
and parent rows shaped like `- [ ] #N`.

1. Fetch the parent body with
   `gh issue view <parent> --json body -q .body`.
2. Run an initial `fanout <parent> --dry-run <flags>` and record its targets.
3. Find same-repository issue references with strong child signals:
   - close/fix/resolve keywords;
   - dependency or relation wording such as `Depends on`, `Blocked by`,
     `Related to`, `See`, or `Refs`;
   - plain list rows beginning with an issue reference;
   - Japanese child, dependency, fix, or resolution wording.
4. Exclude cross-repository references, code fences, blockquotes, the parent
   itself, weak historical mentions, and targets already found.
5. List each candidate with one reason. Ask which to add unless the user
   explicitly authorized immediate execution.
6. Pass accepted numbers through `--include A,B,C` to both preview and live
   commands.

Never scan a Project as though it had a parent issue body.

## Project target discovery

Run `fanout <project-url> --dry-run <flags>` before generating missing names.
Include all selection flags and user-supplied `--name` values, but no generated
name values. Use the resulting targets after Status, repository, blocker,
idempotency, and limit filtering.

This discovery run is required even when the user skips confirmation because
the Project API, not prose, defines the targets. Fetch an item's issue body
only when its title lacks enough context for naming.

Surface Project warnings, especially cross-repository items and authorization
failures. Do not retry cross-repository items in the current checkout.

## Pane names

Generate names only for final targets without complete user overrides:

- `slug-hint`: 2–4 lowercase kebab-case words. Start with an alphanumeric and
  use only `[a-z0-9-]`. fanout adds `-<issue-number>` when absent.
- `display-name`: a readable pane title, preferably at most 40 characters.
- `branch-name`: an optional exact branch. Set it only for an established
  repository convention; otherwise retain fanout's generated
  `branchPrefix + slug`.

Forward `--name <NUM>=<slug>[|<display>[|<branch>]]` once per target. Any
segment may be empty when another is present. Respect all user-provided
segments and fill only missing ones.

## Agent selection

Preserve the user's global and per-target selections. If neither `--agent` nor
`FANOUT_AGENT` supplies a default, use `--agent codex`.

Do not choose Claude or Codex from task size, breadth, number of files, or
work type. Add `--agent NUM=name` only for an explicit user choice or a real
provider-specific constraint. Do not emit unsupported agent names.

Plan Mode is resolved independently from the user-level `childPlanMode`
setting or `FANOUT_CHILD_PLAN_MODE`. Claude-specific briefing toggles affect
briefing text; they do not justify silently changing the selected agent.

## Preview and execution

For the normal approval-gated flow:

1. Run `fanout <target> --dry-run <flags>`.
2. Summarize the issue/Project banner, final targets, generated names,
   worktree/branch intent, briefing preview paths, skipped/deferred rows, and
   warnings.
3. Stop if the user requested only `--dry-run`.
4. Ask for confirmation.
5. Reuse the exact target and flags without `--dry-run`.
6. Report the created/skipped/deferred/failed summary.

When the user says `--go`, “go ahead,” or “run it now,” skip the confirmation
preview but retain discovery dry-runs needed for child scanning or Project
naming. Then execute live with `--go` removed.

Let fanout hold its state lock across planning and pane launch. Do not create
worktrees or panes independently, and do not continue past the first failed
child launch.

## Wait and continue

Use this flow only when the user explicitly asks to wait for children to merge
and then continue parent work.

1. Run `fanout <parent> --status` from the parent worktree. Use default JSON
   for automation and inspect `summary.all_merged` and `summary.blocked`.
2. Continue parent work that does not depend on child merges.
3. Poll `fanout <parent> --status` periodically.
4. Treat `prs: []` as pending, never merged.
5. When `summary.all_merged` becomes true, refresh the same base branch used
   for fan-out and fast-forward it from the normalized remote branch before
   integration tests.

Stop on status exit `2` (target/state enumeration failure) or `3` (GitHub API
failure). On exit `0` with `summary.total == 0`, report that nothing remains
recorded and stop polling.

## Failure mapping

- `fanout must be run inside tmux`: start or attach tmux for batch creation.
- `agent is required`: pass a supported default agent, set `FANOUT_AGENT`, or
  cover every selected target with an override.
- `unknown agent` or `agent "<name>" is not installed`: choose or install
  `claude`, `codex`, or `opencode`.
- `prepare worktree`: report the git failure. Use `--no-refresh` only when a
  stale base is intentional.
- `sub-issues fetch failed`: check `gh auth status`. Treat HTTP 404 as a
  missing parent issue.
- `no sub-issues on #<N>`: report nothing to do; exit `0` is success.
- Project `HTTP 401` or `Resource not accessible by integration`: request
  `gh auth refresh -s read:project` and rerun.
- Project “no items … after status/repo filter”: report nothing to do; exit
  `0` is success.
- A documented flag rejected by the installed binary: compare versions and
  inspect the relevant `--help` output, then report the version mismatch.
