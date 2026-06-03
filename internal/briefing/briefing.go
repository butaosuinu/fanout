// Package briefing builds the per-issue task brief that fanout drops at
// /tmp/fanout-<repo>-<N>.md and points the agent at via the one-line prompt.
//
// The body is locked in by Tier 2 goldens (briefing size: NNN bytes) — both
// the heredoc text and the trailing newline must match fanout:799-814 byte
// for byte.
package briefing

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/butaosuinu/fanout/internal/settings"
)

// Path returns /tmp/fanout-<repo_slug>-<num>.md.
func Path(projectRoot string, num int) string {
	repo := filepath.Base(projectRoot)
	return fmt.Sprintf("/tmp/fanout-%s-%d.md", repo, num)
}

// Render produces the brief body. Live mode writes it to Path(); dry-run uses
// len(Render()) to compute the goldened "briefing size" without touching disk.
func Render(num int, title, body, agent string, s settings.Settings) string {
	lines := []string{
		fmt.Sprintf("You are assigned GitHub issue #%d in this repository.", num),
		"",
		fmt.Sprintf("Title: %s", title),
		"",
		"Body:",
		body,
		"",
		"Requirements:",
		"- You are working inside a git worktree that was prepared for this task. Do not create additional worktrees.",
		"- Make focused, minimal changes scoped to this single issue.",
		"- Run the project's lint/test commands if they exist (inspect package.json / Makefile / pyproject.toml first).",
		"- When implementation passes tests, commit and push the branch.",
	}
	if s.AutoPullRequest {
		lines = append(lines, fmt.Sprintf("- Open a pull request with \"Closes #%d\" in the body.", num))
	}
	lines = append(lines, "- If the scope is ambiguous, stop and leave a comment on the issue instead of guessing.")

	base := strings.Join(lines, "\n") + "\n"
	if agent != "claude" {
		return base
	}

	if !s.PRReviewGate {
		base += reviewGateBypassSection
	}
	if s.BriefingCodeReview {
		base += codeReviewSection
	}
	if s.AgentTeamsHint {
		base += agentTeamsSection
	}
	return base
}

const reviewGateBypassSection = `
The PR review gate is disabled for this fanout run. If ` + "`gh pr create`" + ` is denied
before ` + "`/post-work-review`" + `, you may run it as ` + "`FANOUT_SKIP_PR_REVIEW=1 gh pr create ...`" + `.
`

const codeReviewSection = `
Before committing your final changes, run the ` + "`/code-review`" + ` slash command on the
files you've changed. /code-review is a Claude Code skill that reviews changed code
for reuse, quality, and efficiency and fixes issues it finds. Apply its fixes,
re-run lint/test, then commit and push as described above.
`

const agentTeamsSection = `
Optional: Agent Teams (Claude Code v2.1.32+, requires CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1)

Before starting, decide whether this issue benefits from spawning an Agent Team.
This is a hint, not a rule — if Agent Teams aren't enabled in your environment,
or the issue doesn't fit the criteria below, just proceed as a single session.

Consider Agent Teams when the issue involves:
- Open-ended research or investigation that benefits from multiple angles
  (RFC drafting, library evaluation, root-cause hunts with competing hypotheses).
- New feature work that splits cleanly across independent layers
  (e.g. backend handler + frontend integration + tests, each ownable separately).
- Refactors where files partition naturally so teammates won't collide.
- Reviewing a large diff where security / performance / coverage are distinct lenses.

Skip Agent Teams (single session is better) when:
- The change is sequential or mostly in one file.
- Subtasks share state and would race on the same files.
- The fix is small and focused (typo, single bug, config bump).

If you decide to use Agent Teams:
1. Sketch 3-5 self-contained subtasks with clear deliverables before spawning.
2. Spawn teammates with task-specific prompts that include the issue scope they own.
3. Coordinate via the shared task list; let teammates self-claim where possible.
4. Synthesize findings yourself before opening the PR.
5. Ask the lead to "Clean up the team" before closing the issue.
   Token cost scales linearly with teammate count — favor 3 focused teammates over 5 scattered ones.
`
