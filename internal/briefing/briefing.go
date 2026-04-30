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
)

// Path returns /tmp/fanout-<repo_slug>-<num>.md.
func Path(projectRoot string, num int) string {
	repo := filepath.Base(projectRoot)
	return fmt.Sprintf("/tmp/fanout-%s-%d.md", repo, num)
}

// Render produces the brief body. Live mode writes it to Path(); dry-run uses
// len(Render()) to compute the goldened "briefing size" without touching disk.
func Render(num int, title, body string) string {
	return fmt.Sprintf(`You are assigned GitHub issue #%d in this repository.

Title: %s

Body:
%s

Requirements:
- You are working inside a git worktree that was prepared for this task. Do not create additional worktrees.
- Make focused, minimal changes scoped to this single issue.
- Run the project's lint/test commands if they exist (inspect package.json / Makefile / pyproject.toml first).
- When implementation passes tests, commit and push the branch.
- Open a pull request with "Closes #%d" in the body.
- If the scope is ambiguous, stop and leave a comment on the issue instead of guessing.
`, num, title, body, num)
}
