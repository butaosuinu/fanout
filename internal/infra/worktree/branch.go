package worktree

// Branch reservation and observation shared by launch flows that must create
// or release refs atomically (empty old OID create, compare-and-delete).

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrBranchRollbackBlocked marks a confirmed rollback blocker (checked-out or
// moved branch) or an ambiguous delete outcome, as opposed to an observation
// failure that classified nothing.
var ErrBranchRollbackBlocked = errors.New("branch rollback is blocked")

func LocalBranchRef(ctx context.Context, root, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" || strings.HasPrefix(branch, "refs/") {
		return "", fmt.Errorf("herdr branch must be an unqualified local branch name")
	}
	fullRef := "refs/heads/" + branch
	if _, err := gitContext(ctx, root, "check-ref-format", fullRef); err != nil {
		return "", fmt.Errorf("invalid Herdr branch %q: %w", branch, err)
	}
	// The name is also handed to Herdr's --branch, whose branch mode rejects
	// HEAD and leading dashes that pass the full-ref check. Requiring the
	// expansion to echo the input also blocks @{-1}-style aliases.
	expanded, err := gitStdout(ctx, root, "check-ref-format", "--branch", branch)
	if err != nil {
		return "", fmt.Errorf("validate Herdr branch name %q for --branch mode: %w", branch, err)
	}
	if strings.TrimSuffix(string(expanded), "\n") != branch {
		return "", fmt.Errorf("invalid Herdr branch name %q for --branch mode", branch)
	}
	return fullRef, nil
}

func ObserveBranch(ctx context.Context, root, fullRef string) (string, bool, error) {
	if !strings.HasPrefix(fullRef, "refs/heads/") {
		return "", false, fmt.Errorf("invalid Herdr local branch ref %q", fullRef)
	}
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", fullRef)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("observe Herdr branch %s: %w", fullRef, err)
	}
	sha := strings.ToLower(strings.TrimSpace(string(out)))
	if !commitSHAPattern.MatchString(sha) {
		return "", false, fmt.Errorf("observe Herdr branch %s: invalid commit %q", fullRef, sha)
	}
	return sha, true, nil
}

// ReserveBranch atomically creates fullRef at baseSHA with an empty old
// OID. Existing refs fail without being modified.
func ReserveBranch(ctx context.Context, root, fullRef, baseSHA string) error {
	if !strings.HasPrefix(fullRef, "refs/heads/") || !commitSHAPattern.MatchString(baseSHA) {
		return fmt.Errorf("invalid Herdr branch reservation %s -> %s", fullRef, baseSHA)
	}
	emptyOID := strings.Repeat("0", len(baseSHA))
	if _, err := gitContext(ctx, root, "update-ref", "--create-reflog", fullRef, baseSHA, emptyOID); err != nil {
		return fmt.Errorf("reserve Herdr branch %s at %s: %w", fullRef, baseSHA, err)
	}
	return nil
}

// DeleteReservedBranch compare-and-deletes a fanout-created branch only
// when its expected tip is unchanged and no linked worktree checks it out.
func DeleteReservedBranch(ctx context.Context, root, fullRef, expectedSHA string) error {
	if !strings.HasPrefix(fullRef, "refs/heads/") || !commitSHAPattern.MatchString(expectedSHA) {
		return fmt.Errorf("%w: invalid Herdr branch deletion %s at %s", ErrBranchRollbackBlocked, fullRef, expectedSHA)
	}
	// The caller supplies an independent finite rollback deadline when its
	// launch context is already canceled. Observation failures return untyped
	// so the caller can retry; only a confirmed blocker or an ambiguous delete
	// outcome is terminal.
	checkedOut, err := branchCheckedOut(ctx, root, fullRef)
	if err != nil {
		return err
	}
	if checkedOut {
		return fmt.Errorf("%w: refusing to delete checked-out Herdr branch %s", ErrBranchRollbackBlocked, fullRef)
	}
	current, found, err := ObserveBranch(ctx, root, fullRef)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if current != expectedSHA {
		return fmt.Errorf("%w: herdr branch %s moved from %s to %s", ErrBranchRollbackBlocked, fullRef, expectedSHA, current)
	}
	if _, err := gitContext(ctx, root, "update-ref", "-d", fullRef, expectedSHA); err != nil {
		return fmt.Errorf("%w: delete reserved Herdr branch %s: %w", ErrBranchRollbackBlocked, fullRef, err)
	}
	return nil
}

func BranchAvailable(ctx context.Context, root, fullRef string) error {
	checkedOut, err := branchCheckedOut(ctx, root, fullRef)
	if err != nil {
		return err
	}
	if checkedOut {
		return fmt.Errorf("herdr branch %s is already checked out", fullRef)
	}
	return nil
}

func branchCheckedOut(ctx context.Context, root, fullRef string) (bool, error) {
	entries, err := worktreeEntries(ctx, root)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.branch == fullRef {
			return true, nil
		}
	}
	return false, nil
}
