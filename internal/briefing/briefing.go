// Package briefing writes the per-issue task brief that fanout drops at
// /tmp/fanout-<repo>-<N>.md and points the agent at via the one-line prompt.
//
// The body is locked in by Tier 2 goldens (briefing size: NNN bytes) — both
// the heredoc text and the trailing newline must match fanout:799-814 byte
// for byte.
package briefing

import (
	"fmt"
	"os"
	"path/filepath"
)

// Path returns /tmp/fanout-<repo_slug>-<num>.md.
func Path(projectRoot string, num int) string {
	repo := filepath.Base(projectRoot)
	return fmt.Sprintf("/tmp/fanout-%s-%d.md", repo, num)
}

// Write builds the briefing file and returns its path. Caller is responsible
// for cleanup; the bash version doesn't either, so parity is preserved.
func Write(projectRoot string, num int, title, body string) (string, error) {
	p := Path(projectRoot, num)
	content := Render(num, title, body)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// Render is exposed separately so dry-run can compute the byte size without
// hitting the filesystem twice.
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
