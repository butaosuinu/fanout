package worktree

import (
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
	if got.RepoKey != wantKey || got.RepoRoot != wantRoot {
		t.Fatalf("repo identity = %+v", got)
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

	marker, err := WriteHerdrOwnershipMarker(checkout, "nonce-527")
	if err != nil {
		t.Fatal(err)
	}
	err = VerifyHerdrOwnershipMarker(checkout, marker, "nonce-527")
	if err != nil {
		t.Fatal(err)
	}
	_, err = WriteHerdrOwnershipMarker(checkout, "nonce-527")
	if err == nil {
		t.Fatal("exclusive marker create unexpectedly succeeded twice")
	}
	err = VerifyHerdrOwnershipMarker(checkout, marker, "foreign")
	if err == nil {
		t.Fatal("foreign nonce unexpectedly verified")
	}
	ensured, err := EnsureHerdrOwnershipMarker(checkout, "nonce-527")
	if err != nil {
		t.Fatalf("ensure exact existing marker: %v", err)
	}
	if ensured != marker {
		t.Fatalf("ensured marker = %q, want %q", ensured, marker)
	}
	_, err = EnsureHerdrOwnershipMarker(checkout, "foreign")
	if err == nil {
		t.Fatal("foreign existing marker unexpectedly adopted")
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
