package cliflags

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

const usageText = `Usage: fanout
       fanout <parent-issue|project-url> [options]
       fanout plan <spec.json|plan-slug> [options]

With no arguments, starts fanout's persistent tmux console. The console creates
or attaches a fanout-managed tmux session when launched from a plain shell, and
shows recorded panes from .fanout/state.json with live tmux and issue/PR status.

With a parent issue or GitHub Projects v2 URL, creates one tmux pane per OPEN
sub-issue of a parent issue, OR per OPEN item in that Project. Each pane gets a
dedicated git worktree under .fanout/worktrees/ and starts the configured agent
with a briefing that points at /tmp/fanout-<repo>-<num>.md.

Options:
  --agent <name|NUM=name>
                      Agent to launch (claude|codex). Repeatable: a bare
                      name is the default, and NUM=name overrides one child
                      issue. Required unless FANOUT_AGENT is set or every
                      selected child has an override. Unknown agents fail
                      before pane creation; missing agent CLIs fail in live
                      mode.
  --base-branch <branch>
                      Branch to refresh and branch child worktrees from.
                      Default: GitHub default branch, then origin/HEAD, then
                      main.
  --branch-prefix <p> Prefix for generated branch names. Default: fanout/.
  --no-refresh        Skip git fetch + fast-forward of the base branch before
                      creating child worktrees.
  --limit <N>         Cap how many children to enqueue this run. Remainder is
                      printed with a rerun command.
  --only <list>       Comma-separated list of issue numbers to fan out,
                      e.g. --only 4,7,8,10. Numbers not present in the OPEN
                      child set (Sub-issues API + parent body task-list
                      union) are warned and ignored; fanout never widens the
                      search to arbitrary issues. Cannot be combined with
                      --skip. Applied before --limit.
  --skip <list>       Comma-separated list of issue numbers to exclude,
                      e.g. --skip 6,9. Everything else in the OPEN child
                      set is fanned out. Cannot be combined with --only.
                      Applied before --limit.
  --include <list>    Comma-separated list of issue numbers to force-add to
                      the children set even if they aren't returned by the
                      Sub-issues API or picked up from the parent body's
                      task-list scan, e.g. --include 123,456. Intended for
                      the bundled Claude/Codex agent integrations, which
                      read the parent body for implicit child references
                      (close keywords, prose, Japanese idioms) and forward
                      the accepted numbers here. Combines with --only/--skip
                      (included first, then filtered). Numbers that end up
                      CLOSED or don't exist are warned and skipped.
  --name <NUM>=<slug-hint>[|<display-name>[|<branch-name>]]
                      Override the default naming for issue <NUM>. Repeatable
                      (once per issue). <slug-hint> is 2-4 kebab-case words
                      (lowercase alnum + hyphens) used as the worktree slug
                      stem; fanout appends -<NUM> when it is missing.
                      <display-name> (optional) is used as the tmux pane title
                      for the newly-created pane.
                      <branch-name> (optional) overrides the generated
                      branch name for that issue.
                      Examples:
                        --name 17=fix-login-timeout|Fix login
                        --name 18=update-docs
                        --name 19=|Bug triage   (display-name only)
                        --name 20=feat-x|Feature X|feat/issue-20-x
                        --name 21=||release/v2.0   (branch override only)
  --unblocked-only    Only fan out children whose blockers are all CLOSED.
                      Blockers are read from (1) the child body's
                      "## Blocked by" section, (2) a "(blocked by #X, #Y)"
                      trailer on the parent's task-list row, and (3) the
                      child's "blocked" label (weak signal — logged but
                      not deduced from). Children with any OPEN blocker
                      are reported as deferred in the final summary.
                      Safe to rerun as blocker PRs merge.
  --session <name>    Target a named tmux session instead of the invoking pane.
                      fanout itself still must be invoked from inside tmux.
  --project-status <name>
                      [project mode only] Restrict to Project items whose
                      single-select "Status" field equals <name>. Default:
                      "Todo". Pass "all" to disable the filter.
  --auto-pr / --no-auto-pr
                      Include or omit the child briefing requirement to open
                      a PR with "Closes #N" after tests pass. Default: on.
  --pr-review-gate / --no-pr-review-gate
                      Keep the default PR review-gate expectation, or add a
                      Claude briefing note allowing
                      FANOUT_SKIP_PR_REVIEW=1 gh pr create ... if the
                      PreToolUse hook blocks before /post-work-review.
                      Default: on.
  --briefing-code-review / --no-briefing-code-review
                      Include or omit the Claude-only /code-review briefing
                      instruction. Default: on.
  --agent-teams-hint / --no-agent-teams-hint
                      Include or omit the Claude-only Agent Teams hint in
                      child briefings. Default: on.
  --codex-plan-mode / --no-codex-plan-mode
                      For --agent codex, create a Codex app-server plan thread
                      and resume it with the prompt through an interactive
                      Codex TUI instead of positional ` + "`" + `codex "<prompt>"` + "`" + `. Default: off.
  --pr-visualization / --no-pr-visualization
                      Include or omit structured PR-body plus gated Mermaid
                      guidance in auto-PR child briefings. Default: on.
  --dashboard-keybind / --no-dashboard-keybind
                      Register (or skip) tmux 'F12' / 'prefix + D' keybindings
                      after a live fan-out so the read-only web dashboard can be
                      opened from any pane. Default: on.
  --team              Opt in to sibling-pane messaging for this run: add a
                      "Coordinating with your sibling panes" section (roster +
                      shared SQLite DB path) to every child briefing and seed
                      the created panes into the per-parent peers registry.
                      Best-effort; registry failures never fail the fan-out.
                      Default: off.
  --sleep <seconds>   Pause between pane-creation requests. Default 4.
  --popup-timeout <s> Deprecated compatibility flag; accepted but ignored by
                      the direct tmux path.
  --dry-run           Print the git worktree, tmux split-window, and agent
                      launch commands without executing them.
  --debug             Enable extra diagnostic logging.
  --status            Status mode. Print status describing each fanned-out
                      child issue recorded in .fanout/state.json, including
                      issue state plus closed-by PR merge/review/CI status,
                      then exit.
                      Read-only unless --post-dashboard is also set.
                      Exclusive with action-bearing flags.
  --format <json|table>
                      Output format for --status. Default: json. The table
                      format adds PR state, CI, diff bars, changed-file
                      counts, Conventional-Commit type, and PR links.
  --post-dashboard    With --status, upsert one marker-based rollup comment
                      on the parent issue. The comment aggregates child PR
                      links, PR state, CI, diff size, Conventional-Commit
                      type, TL;DR, and Review effort score from
                      machine-readable PR data.
  --close <NUM>       Remove the recorded child worktree for issue <NUM>,
                      kill its tmux pane when still present, update state,
                      and run git worktree prune.
  --merge <NUM>       Run git merge --ff-only against the recorded child
                      branch for issue <NUM>. Non-fast-forward failures are
                      reported without starting conflict resolution.
  --cleanup           Close recorded child worktrees whose issue is CLOSED or
                      has a MERGED closed-by PR.
  plan                Subcommand. Launch issue-less task panes from a JSON
                      plan spec, or rerun a spec copied under
                      .fanout/plans/<slug>.json. See 'fanout plan --help'.
  dashboard           Subcommand. Start a read-only localhost web dashboard
                      that visualizes fanout Sessions (panes grouped by parent)
                      live — pane liveness, issue state, PR merge status.
                      127.0.0.1-bound, GET-only, token-gated. See
                      'fanout dashboard --help'.
  msg                 Subcommand. Peer messaging between fanout panes over a
                      per-parent SQLite DB: send/post/mark-read/register plus
                      peers/inbox/board read views. See 'fanout msg --help'.
  update              Subcommand. Replace this binary and bundled Claude/Codex
                      integrations through install.sh immediately. Supports
                      --version <tag> and --no-skills.
  --check-update      Read-only mode. Fetch the latest fanout release tag,
                      compare it with this binary's version, print whether an
                      update is available, then exit. Also accepted as
                      ` + "`" + `fanout check-update` + "`" + `.
  -V, --version       Print version and commit, then exit.
  -h, --help          Show this message.

Prerequisites:
  * gh, git, tmux installed.
  * fanout pane-creation mode is invoked from inside a tmux session. TUI mode
    can be started from a plain shell; it creates or attaches its tmux session.
  * --agent is given, FANOUT_AGENT is set, or every selected target has a
    per-target --agent override for pane-creation mode.

Exit codes (default flow):
  0 success (including "no children, nothing to do")
  1 prerequisite / environment problem
  2 bad invocation

Exit codes (--status):
  0 success (status emitted; check summary.all_merged in JSON mode for state)
  2 cannot enumerate children or state
  3 gh API call failed

Exit codes (--check-update):
  0 success (including update available, up-to-date, dev build)
  2 cannot compare version strings (MAJOR.MINOR.PATCH, optional v prefix)
  3 gh release lookup failed

Exit codes (update):
  0 success (update completed, or already up to date)
  1 prerequisite / environment problem, or missing option value
  2 unknown option, unexpected argument, or cannot compare version strings
  3 gh release lookup failed
`

// Usage writes the help text to w.
func Usage(w io.Writer) {
	fmt.Fprint(w, usageText)
}

// NormalizeParentRef canonicalizes a parent reference exactly the way Parse
// records it in Config.ParentRef: integer refs lose leading zeros, Projects
// v2 URLs drop any trailing path/query. ok is false when raw is neither.
// Consumers that persist or compare parent refs (fanout msg scopes every
// messages row by this string) must normalize through here so an explicit
// --parent matches the refs recorded at pane-launch time.
func NormalizeParentRef(raw string) (ref string, ok bool) {
	raw = strings.TrimSpace(raw)
	if reAllDigits.MatchString(raw) {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return "", false
		}
		return strconv.Itoa(n), true
	}
	if m := reProjectURL.FindStringSubmatch(raw); len(m) == 5 {
		n, err := strconv.Atoi(m[3])
		if err != nil {
			return "", false
		}
		return fmt.Sprintf("https://github.com/%s/%s/projects/%d", m[1], m[2], n), true
	}
	return "", false
}
