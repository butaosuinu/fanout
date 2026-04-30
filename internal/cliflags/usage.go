package cliflags

import (
	"fmt"
	"io"
)

const usageText = `Usage: fanout <parent-issue> [options]

Creates one dmux pane per OPEN sub-issue of <parent-issue>. Each pane gets a
dedicated git worktree (dmux handles that) and starts the configured agent
with a briefing that points at /tmp/fanout-<repo>-<num>.md.

Options:
  --agent <name>      Agent to launch (claude|codex|opencode|...). Required
                      unless the caller's tmux pane is itself a dmux-managed
                      pane — in that case fanout auto-detects the agent from
                      dmux.config.json. dmux v5.6.3 always opens an agent-
                      choice popup after the prompt popup, so fanout must
                      know the agent name to inject into it.
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
  --name <NUM>=<slug-hint>[|<display-name>]
                      Override the default naming for issue <NUM>. Repeatable
                      (once per issue). <slug-hint> is 2-4 kebab-case words
                      (lowercase alnum + hyphens) front-loaded into the one-
                      line prompt so dmux's slug generator is very likely to
                      echo it as the branch / worktree directory name — this
                      is the only reliable way to name branches/worktrees
                      because dmux computes them from the prompt at creation
                      time and they become immutable git state afterward.
                      <display-name> (optional; ≤80 chars after sanitization)
                      is written post-creation into panes[].displayName in
                      dmux.config.json and merged into the worktree's
                      .dmux/worktree-metadata.json, so dmux's enforcePaneTitles
                      loop (every 5-30s) surfaces it as the tmux pane border
                      title and reopenWorktree restores it across restarts.
                      Intended to be filled in by the bundled Claude/Codex
                      agent integrations, which generate both hints from
                      issue title/body using in-conversation reasoning (no
                      extra LLM call).
                      Examples:
                        --name 17=fix-login-timeout|Fix login
                        --name 18=update-docs
                        --name 19=|Bug triage   (display-name only)
  --unblocked-only    Only fan out children whose blockers are all CLOSED.
                      Blockers are read from (1) the child body's
                      "## Blocked by" section, (2) a "(blocked by #X, #Y)"
                      trailer on the parent's task-list row, and (3) the
                      child's "blocked" label (weak signal — logged but
                      not deduced from). Children with any OPEN blocker
                      are reported as deferred in the final summary.
                      Safe to rerun as blocker PRs merge.
  --session <name>    Target a specific tmux session (required when more than
                      one dmux session is alive on this machine).
  --sleep <seconds>   Pause between pane-creation requests. Default 4. Raise
                      this if dmux reports "pane creation failed" under load.
  --popup-timeout <s> Seconds to wait for each dmux popup (new-pane, agent-
                      choice) to appear after the triggering keystroke.
                      Default 20. Raise on slow machines or large worktrees
                      where dmux takes longer than that between closing the
                      prompt popup and opening the agent-choice popup.
  --dry-run           Print the send-keys commands, the popup JSON payloads
                      that would be injected, and the briefings, without
                      driving dmux.
  --debug             Log intercept steps (popup PID, result file path, JSON
                      payload) to stderr as each pane is created.
  -h, --help          Show this message.

Prerequisites:
  * gh, gh-sub-issue extension, tmux, pgrep installed.
  * ` + "`" + `cd <repo> && dmux` + "`" + ` has been run; tmux session is alive.
  * dmux TUI is on the pane-list view (no modal open). fanout sends one Esc
    at startup as a best-effort.
  * --agent given, OR the caller's pane is a dmux-managed pane so fanout
    can auto-detect it. dmux v5.6.3 routes new-pane prompts through
    tmux display-popup (a separate tmux client that send-keys cannot
    reach), so fanout drives the flow by intercepting the popup's result
    file. The agent name is required to satisfy the picker popup that
    dmux always shows next.

Exit codes:
  0 success (including "no children, nothing to do")
  1 prerequisite / environment problem
  2 bad invocation
`

// Usage writes the help text to w.
func Usage(w io.Writer) {
	fmt.Fprint(w, usageText)
}
