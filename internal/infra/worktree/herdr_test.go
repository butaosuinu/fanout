package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHerdrBranchReservationIsAtomic(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	fullRef, err := HerdrBranchRef(repo, "fanout/herdr-527")
	if err != nil {
		t.Fatal(err)
	}
	if reserveErr := ReserveBranch(repo, fullRef, base); reserveErr != nil {
		t.Fatal(reserveErr)
	}
	if err := ReserveBranch(repo, fullRef, base); err == nil {
		t.Fatal("second empty-old-OID reservation unexpectedly succeeded")
	}
	if got, found, err := ObserveBranch(repo, fullRef); err != nil || !found || got != base {
		t.Fatalf("reserved branch = (%q,%t,%v), want %s", got, found, err, base)
	}
}

func TestResolveHerdrRepoIdentityReturnsPhysicalGitTuple(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	got, err := ResolveHerdrRepoIdentity(repo)
	if err != nil {
		t.Fatal(err)
	}
	wantKey, err := physicalHerdrPath(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	wantRoot, err := physicalHerdrPath(repo)
	if err != nil {
		t.Fatal(err)
	}
	wantGitDir, err := physicalHerdrPath(filepath.Join(repo, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if got.RepoKey != wantKey || got.RepoRoot != wantRoot || got.GitDir != wantGitDir ||
		got.GitDirDevice == 0 || got.GitDirInode == 0 {
		t.Fatalf("repo identity = %+v", got)
	}

	sibling := filepath.Join(t.TempDir(), "sibling")
	gitTest(t, repo, "worktree", "add", "-b", "identity-sibling", sibling, "HEAD")
	siblingIdentity, err := ResolveHerdrRepoIdentity(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if siblingIdentity.RepoKey != got.RepoKey ||
		siblingIdentity.RepoRoot == got.RepoRoot ||
		siblingIdentity.GitDir == got.GitDir ||
		(siblingIdentity.GitDirDevice == got.GitDirDevice &&
			siblingIdentity.GitDirInode == got.GitDirInode) {
		t.Fatalf("linked worktree identity = %+v, root identity = %+v", siblingIdentity, got)
	}
}

func TestCheckoutRegisteredHandlesNewlineInWorktreePath(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	checkout := filepath.Join(repo, ".fanout", "worktrees", "line\nbreak")
	if err := os.MkdirAll(filepath.Dir(checkout), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "worktree", "add", "-b", "fanout/herdr-newline", checkout, "HEAD")
	registered, err := checkoutRegistered(repo, checkout)
	if err != nil {
		t.Fatal(err)
	}
	if !registered {
		t.Fatal("registered worktree with newline path was not found")
	}
}

func TestResolveHerdrBasePinsCanonicalTupleAndRejectsDirtySource(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	baseBranch := gitOutput(t, repo, "branch", "--show-current")
	wantSHA := gitOutput(t, repo, "rev-parse", "HEAD")

	got, err := ResolveHerdrBase(repo, baseBranch, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResolvedRef != "refs/heads/"+baseBranch || got.ResolvedName != baseBranch ||
		got.EffectiveBase != baseBranch || got.PRBaseName != baseBranch || got.SHA != wantSHA {
		t.Fatalf("resolution = %+v", got)
	}

	if err := os.WriteFile(filepath.Join(repo, "untracked"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveHerdrBase(repo, baseBranch, true); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("dirty source error = %v", err)
	}
	if _, err := ResolveHerdrBase(repo, baseBranch, false); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("dirty source should fail before refresh: %v", err)
	}
}

func TestHerdrOwnershipMarkerIsExclusiveAndExact(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	fullRef, err := HerdrBranchRef(repo, "fanout/herdr-marker")
	if err != nil {
		t.Fatal(err)
	}
	if reserveErr := ReserveBranch(repo, fullRef, base); reserveErr != nil {
		t.Fatal(reserveErr)
	}
	checkout := filepath.Join(repo, ".fanout", "worktrees", "herdr-marker")
	if mkdirErr := os.MkdirAll(filepath.Dir(checkout), 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	gitTest(t, repo, "worktree", "add", checkout, "fanout/herdr-marker")
	gitDir := gitOutput(t, checkout, "rev-parse", "--path-format=absolute", "--git-dir")
	markerPath := filepath.Join(gitDir, herdrOwnershipMarkerName)

	if _, proofErr := EnsureHerdrOwnershipMarker(
		repo,
		checkout,
		"refs/heads/fanout/wrong-marker-branch",
		base,
		"nonce-527",
	); proofErr == nil {
		t.Fatal("mismatched checkout branch unexpectedly accepted")
	}
	if _, proofErr := EnsureHerdrOwnershipMarker(
		repo,
		checkout,
		fullRef,
		strings.Repeat("0", 40),
		"nonce-527",
	); proofErr == nil {
		t.Fatal("mismatched checkout HEAD unexpectedly accepted")
	}
	if _, markerErr := os.Lstat(markerPath); !errors.Is(markerErr, os.ErrNotExist) {
		t.Fatalf("invalid checkout proof wrote marker: %v", markerErr)
	}

	marker, err := EnsureHerdrOwnershipMarker(repo, checkout, fullRef, base, "nonce-527")
	if err != nil {
		t.Fatal(err)
	}
	if marker != markerPath {
		t.Fatalf("marker = %q, want %q", marker, markerPath)
	}
	err = VerifyHerdrOwnershipMarker(repo, checkout, fullRef, base, marker, "nonce-527")
	if err != nil {
		t.Fatal(err)
	}
	ensured, err := EnsureHerdrOwnershipMarker(repo, checkout, fullRef, base, "nonce-527")
	if err != nil || ensured != marker {
		t.Fatalf("ensure exact marker = %q, %v; want %q", ensured, err, marker)
	}
	err = VerifyHerdrOwnershipMarker(repo, checkout, fullRef, base, marker, "foreign")
	if err == nil {
		t.Fatal("foreign nonce unexpectedly verified")
	}
	_, err = EnsureHerdrOwnershipMarker(repo, checkout, fullRef, base, "foreign")
	if err == nil {
		t.Fatal("foreign existing marker unexpectedly adopted")
	}
}

func TestHerdrOwnershipMarkerRejectsUnregisteredAndSymlinkCheckoutRoots(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	fullRef, err := HerdrBranchRef(repo, "fanout/herdr-marker-race")
	if err != nil {
		t.Fatal(err)
	}
	if err := ReserveBranch(repo, fullRef, base); err != nil {
		t.Fatal(err)
	}
	sourceMarker := filepath.Join(repo, ".git", herdrOwnershipMarkerName)

	plain := filepath.Join(repo, ".fanout", "worktrees", "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureHerdrOwnershipMarker(repo, plain, fullRef, base, "plain-nonce"); err == nil {
		t.Fatal("unregistered nested directory unexpectedly accepted")
	}
	if _, err := os.Lstat(sourceMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unregistered directory wrote source marker: %v", err)
	}

	alias := filepath.Join(repo, ".fanout", "worktrees", "source-link")
	if err := os.Symlink(repo, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureHerdrOwnershipMarker(repo, alias, fullRef, base, "link-nonce"); err == nil {
		t.Fatal("symlink checkout root unexpectedly accepted")
	}
	if _, err := os.Lstat(sourceMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink checkout wrote source marker: %v", err)
	}
}

func TestVerifyReservedBranchRejectsMovingResolvedBase(t *testing.T) {
	repo := newCommittedRepoWithoutOrigin(t)
	baseBranch := gitOutput(t, repo, "branch", "--show-current")
	base := gitOutput(t, repo, "rev-parse", "HEAD")
	fullRef, err := HerdrBranchRef(repo, "fanout/herdr-base-move")
	if err != nil {
		t.Fatal(err)
	}
	if err := ReserveBranch(repo, fullRef, base); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "commit", "--allow-empty", "-m", "move base")
	if err := VerifyReservedBranch(repo, "refs/heads/"+baseBranch, base, fullRef); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatalf("moving base error = %v", err)
	}
}
