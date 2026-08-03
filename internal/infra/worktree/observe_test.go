package worktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveHerdrRepoIdentityDoesNotRequirePathFormat(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	binDir := t.TempDir()
	fakeGit := filepath.Join(binDir, "git")
	if err := os.WriteFile(fakeGit, []byte(`#!/bin/sh
case "$*" in
  "rev-parse --git-common-dir") printf '.git\n' ;;
  "rev-parse --show-toplevel") pwd -P ;;
  *) exit 64 ;;
esac
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	identity, err := ResolveRepoIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, err := physicalPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	wantKey, err := physicalPath(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if identity != (RepoIdentity{RepoKey: wantKey, RepoRoot: wantRoot}) {
		t.Fatalf("identity = %+v, want repo key %s and root %s", identity, wantKey, wantRoot)
	}
}

func TestHerdrBranchReservationIsAtomicAndCompareDeleted(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	fullRef, refErr := LocalBranchRef(context.Background(), repo, "fanout/child")
	if refErr != nil {
		t.Fatal(refErr)
	}
	if err := ReserveBranch(context.Background(), repo, fullRef, base); err != nil {
		t.Fatal(err)
	}
	if err := ReserveBranch(context.Background(), repo, fullRef, base); err == nil {
		t.Fatal("second atomic branch reservation unexpectedly succeeded")
	}
	got, found, err := ObserveBranch(context.Background(), repo, fullRef)
	if err != nil || !found || got != base {
		t.Fatalf("reserved branch = (%q,%t,%v), want %s", got, found, err, base)
	}

	checkout := filepath.Join(t.TempDir(), "checkout")
	gitTest(t, repo, "worktree", "add", checkout, "fanout/child")
	if err := DeleteReservedBranch(context.Background(), repo, fullRef, base); err == nil ||
		!strings.Contains(err.Error(), "checked-out") {
		t.Fatalf("checked-out delete error = %v", err)
	}
	gitTest(t, repo, "worktree", "remove", checkout)
	if err := DeleteReservedBranch(context.Background(), repo, fullRef, base); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ObserveBranch(context.Background(), repo, fullRef); err != nil || found {
		t.Fatalf("branch after compare-delete = (found:%t, err:%v)", found, err)
	}
}

func TestDeleteReservedHerdrBranchHonorsCanceledContext(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	fullRef, err := LocalBranchRef(context.Background(), repo, "fanout/canceled-delete")
	if err != nil {
		t.Fatal(err)
	}
	if err := ReserveBranch(context.Background(), repo, fullRef, base); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := DeleteReservedBranch(ctx, repo, fullRef, base); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled branch delete error = %v", err)
	}
	if got, found, observeErr := ObserveBranch(
		context.Background(),
		repo,
		fullRef,
	); observeErr != nil || !found || got != base {
		t.Fatalf("branch after canceled delete = (%q,%t,%v), want %s", got, found, observeErr, base)
	}
}

func TestHerdrBranchReservationSupportsSHA256ObjectIDs(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, "", "init", "--object-format=sha256", "-b", "main", repo)
	gitTest(t, repo, "config", "user.name", "Fanout Test")
	gitTest(t, repo, "config", "user.email", "fanout@example.test")
	writeFile(t, filepath.Join(repo, "file.txt"), "base\n")
	gitTest(t, repo, "add", "file.txt")
	gitTest(t, repo, "commit", "-m", "base")

	base := gitOutput(t, repo, "rev-parse", "HEAD")
	if len(base) != 64 {
		t.Fatalf("SHA-256 repository HEAD length = %d, want 64", len(base))
	}
	resolved, err := ResolveLaunchBase(context.Background(), Options{
		ProjectRoot: repo, Slug: "sha256", BranchName: "fanout/sha256",
		AllowMissingOrigin: true,
	})
	if err != nil || resolved.SHA != base {
		t.Fatalf("SHA-256 base = %+v, err=%v", resolved, err)
	}
	fullRef, err := LocalBranchRef(context.Background(), repo, "fanout/sha256")
	if err != nil {
		t.Fatal(err)
	}
	if err := ReserveBranch(context.Background(), repo, fullRef, base); err != nil {
		t.Fatal(err)
	}
	if got, found, err := ObserveBranch(context.Background(), repo, fullRef); err != nil || !found || got != base {
		t.Fatalf("SHA-256 branch = (%q,%t,%v), want %s", got, found, err, base)
	}
	if err := DeleteReservedBranch(context.Background(), repo, fullRef, base); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteReservedHerdrBranchRejectsMovedTip(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	fullRef, err := LocalBranchRef(context.Background(), repo, "fanout/moved")
	if err != nil {
		t.Fatal(err)
	}
	if err := ReserveBranch(context.Background(), repo, fullRef, base); err != nil {
		t.Fatal(err)
	}
	tree := gitOutput(t, repo, "rev-parse", "HEAD^{tree}")
	moved := gitOutput(
		t,
		repo,
		"-c", "user.name=Fanout Test",
		"-c", "user.email=fanout@example.test",
		"commit-tree", tree, "-p", base, "-m", "moved",
	)
	gitTest(t, repo, "update-ref", fullRef, moved, base)
	if err := DeleteReservedBranch(context.Background(), repo, fullRef, base); err == nil ||
		!strings.Contains(err.Error(), "moved from") {
		t.Fatalf("moved delete error = %v", err)
	}
	if got := gitOutput(t, repo, "rev-parse", fullRef); got != moved {
		t.Fatalf("moved branch was changed: %s != %s", got, moved)
	}
}

func TestResolveHerdrBasePinsCommitAndRejectsDirtySource(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	want := gitOutput(t, repo, "rev-parse", "HEAD")
	got, err := ResolveLaunchBase(context.Background(), Options{
		ProjectRoot: repo, Slug: "child", BranchName: "fanout/child",
		AllowMissingOrigin: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseBranch != "main" || got.SHA != want {
		t.Fatalf("base = %+v, want main at %s", got, want)
	}
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveLaunchBase(context.Background(), Options{
		ProjectRoot: repo, Slug: "dirty", BranchName: "fanout/dirty",
		AllowMissingOrigin: true,
	}); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("dirty base error = %v", err)
	}
}

func TestResolveHerdrBaseContextStopsCanceledGitWork(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ResolveLaunchBase(ctx, Options{
		ProjectRoot: repo, Slug: "canceled", BranchName: "fanout/canceled",
		NoRefresh: true, AllowMissingOrigin: true,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveLaunchBase() error = %v, want context.Canceled", err)
	}
}

func TestResolveHerdrBaseContextStopsDefaultBranchLookup(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	gitTest(t, repo, "remote", "add", "origin", "https://example.invalid/fanout.git")
	binDir := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(binDir, "gh"),
		[]byte("#!/bin/sh\nexec /bin/sleep 5\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := ResolveLaunchBase(ctx, Options{
		ProjectRoot: repo, Slug: "default-timeout", BranchName: "fanout/default-timeout",
		NoRefresh: true, AllowMissingOrigin: true,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ResolveLaunchBase() error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("default branch lookup stopped after %v, want under 1s", elapsed)
	}
}

func TestRefreshBaseContextStopsCanceledFetch(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := RefreshBaseContext(ctx, repo, "main")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RefreshBaseContext() error = %v, want context.Canceled", err)
	}
}

func TestEnsureHerdrWorktreeParentRejectsSymlinkComponent(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(repo, ".fanout")); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(repo, ".fanout", "worktrees", "child")
	if err := EnsureWorktreeParentDir(repo, checkout); err == nil ||
		!strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("symlink parent error = %v", err)
	}
}

func TestObserveHerdrCheckoutIgnoresUnrelatedPrunableWorktree(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	stale := filepath.Join(t.TempDir(), "stale")
	gitTest(t, repo, "worktree", "add", "-b", "fanout/stale", stale, "HEAD")
	if err := os.RemoveAll(stale); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(repo, ".fanout", "worktrees", "target")
	got, err := ObserveCheckout(context.Background(), repo, target)
	if err != nil {
		t.Fatal(err)
	}
	if !got.PathAbsent || got.Registered {
		t.Fatalf("target checkout observation = %+v", got)
	}
}

func TestObserveHerdrCheckoutRejectsRecreatedPrunableDirectory(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	checkout := filepath.Join(repo, ".fanout", "worktrees", "stale")
	gitTest(t, repo, "worktree", "add", "-b", "fanout/stale", checkout, "HEAD")
	if err := os.RemoveAll(checkout); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := ObserveCheckout(context.Background(), repo, checkout); err == nil ||
		!strings.Contains(err.Error(), "does not resolve to its registered worktree root") {
		t.Fatalf("recreated prunable checkout error = %v", err)
	}
}

func TestVerifyHerdrCheckoutPinsBranchHeadAndRepository(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	fullRef, refErr := LocalBranchRef(context.Background(), repo, "fanout/child")
	if refErr != nil {
		t.Fatal(refErr)
	}
	if err := ReserveBranch(context.Background(), repo, fullRef, base); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(repo, ".fanout", "worktrees", "child")
	if err := EnsureWorktreeParentDir(repo, checkout); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "worktree", "add", checkout, "fanout/child")
	identity, err := ResolveRepoIdentity(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyCheckout(
		context.Background(),
		repo,
		checkout,
		fullRef,
		base,
		identity.RepoKey,
		identity.RepoRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Registered || got.PathAbsent || got.RepoKey != identity.RepoKey ||
		got.RepoRoot != identity.RepoRoot {
		t.Fatalf("checkout observation = %+v", got)
	}

	if err := os.WriteFile(filepath.Join(checkout, "next.txt"), []byte("next\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, checkout, "add", "next.txt")
	gitTest(t, checkout, "-c", "user.name=Fanout Test", "-c", "user.email=fanout@example.test", "commit", "-m", "next")
	if _, err := VerifyCheckout(
		context.Background(),
		repo,
		checkout,
		fullRef,
		base,
		identity.RepoKey,
		identity.RepoRoot,
	); err == nil || !strings.Contains(err.Error(), "HEAD is") {
		t.Fatalf("moved checkout verification error = %v", err)
	}
}

// --branch mode rejects names (HEAD, leading dash, @{-1} aliases) that pass
// the full-ref check but fail when handed to Herdr's --branch.
func TestLocalBranchRefRejectsBranchModeInvalidNames(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	if ref, err := LocalBranchRef(context.Background(), repo, "fanout/valid-name"); err != nil || ref != "refs/heads/fanout/valid-name" {
		t.Fatalf("valid branch = (%q, %v)", ref, err)
	}
	for _, name := range []string{"HEAD", "-foo", "@{-1}"} {
		if _, err := LocalBranchRef(context.Background(), repo, name); err == nil {
			t.Fatalf("branch name %q was accepted", name)
		}
	}
}

func TestLocalBranchRefStopsBranchModeCheckAtContextDeadline(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	binDir := t.TempDir()
	fakeGit := filepath.Join(binDir, "git")
	if err := os.WriteFile(fakeGit, []byte(`#!/bin/sh
case "$*" in
  "check-ref-format refs/heads/fanout/slow") exit 0 ;;
  "check-ref-format --branch fanout/slow") while :; do :; done ;;
  *) exit 64 ;;
esac
`), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := LocalBranchRef(ctx, repo, "fanout/slow")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("LocalBranchRef() error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("branch mode check stopped after %v, want under 1s", elapsed)
	}
}
