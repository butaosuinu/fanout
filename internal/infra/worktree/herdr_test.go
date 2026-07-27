package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHerdrBranchReservationIsAtomicAndCompareDeleted(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	fullRef, refErr := HerdrBranchRef(repo, "fanout/child")
	if refErr != nil {
		t.Fatal(refErr)
	}
	if err := ReserveHerdrBranch(repo, fullRef, base); err != nil {
		t.Fatal(err)
	}
	if err := ReserveHerdrBranch(repo, fullRef, base); err == nil {
		t.Fatal("second atomic branch reservation unexpectedly succeeded")
	}
	got, found, err := ObserveHerdrBranch(repo, fullRef)
	if err != nil || !found || got != base {
		t.Fatalf("reserved branch = (%q,%t,%v), want %s", got, found, err, base)
	}

	checkout := filepath.Join(t.TempDir(), "checkout")
	gitTest(t, repo, "worktree", "add", checkout, "fanout/child")
	if err := DeleteReservedHerdrBranch(repo, fullRef, base); err == nil ||
		!strings.Contains(err.Error(), "checked-out") {
		t.Fatalf("checked-out delete error = %v", err)
	}
	gitTest(t, repo, "worktree", "remove", checkout)
	if err := DeleteReservedHerdrBranch(repo, fullRef, base); err != nil {
		t.Fatal(err)
	}
	if _, found, err := ObserveHerdrBranch(repo, fullRef); err != nil || found {
		t.Fatalf("branch after compare-delete = (found:%t, err:%v)", found, err)
	}
}

func TestDeleteReservedHerdrBranchRejectsMovedTip(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	fullRef, err := HerdrBranchRef(repo, "fanout/moved")
	if err != nil {
		t.Fatal(err)
	}
	if err := ReserveHerdrBranch(repo, fullRef, base); err != nil {
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
	if err := DeleteReservedHerdrBranch(repo, fullRef, base); err == nil ||
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
	got, err := ResolveHerdrBase(Options{
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
	if _, err := ResolveHerdrBase(Options{
		ProjectRoot: repo, Slug: "dirty", BranchName: "fanout/dirty",
		AllowMissingOrigin: true,
	}); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("dirty base error = %v", err)
	}
}

func TestEnsureHerdrWorktreeParentRejectsSymlinkComponent(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(repo, ".fanout")); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(repo, ".fanout", "worktrees", "child")
	if err := EnsureHerdrWorktreeParent(repo, checkout); err == nil ||
		!strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("symlink parent error = %v", err)
	}
}

func TestVerifyHerdrCheckoutPinsBranchHeadAndRepository(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	fullRef, refErr := HerdrBranchRef(repo, "fanout/child")
	if refErr != nil {
		t.Fatal(refErr)
	}
	if err := ReserveHerdrBranch(repo, fullRef, base); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(repo, ".fanout", "worktrees", "child")
	if err := EnsureHerdrWorktreeParent(repo, checkout); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "worktree", "add", checkout, "fanout/child")
	identity, err := ResolveHerdrRepoIdentity(repo)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyHerdrCheckout(
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
	if _, err := VerifyHerdrCheckout(
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
