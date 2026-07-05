// Package gitroot resolves git work-tree roots. The error strings are shared
// user-visible output (previously copy-pasted across runtime, team, and
// cmd/fanout); do not change them.
package gitroot

import (
	"fmt"
	"os/exec"
	"strings"
)

// Toplevel returns `git rev-parse --show-toplevel` resolved from dir; an empty
// or whitespace-only dir resolves from the current working directory.
func Toplevel(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("current directory is not inside a git work tree")
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("git rev-parse --show-toplevel returned an empty path")
	}
	return root, nil
}

// IsWorkTree reports whether dir is inside a git work tree
// (`git rev-parse --is-inside-work-tree` succeeds there).
func IsWorkTree(dir string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = dir
	return cmd.Run() == nil
}
