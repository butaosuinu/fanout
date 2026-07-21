package naming

import (
	"regexp"
	"strings"
	"testing"
)

func TestHerdrSessionNameUsesCanonicalGitCommonDir(t *testing.T) {
	const commonDir = "/Users/alice/src/fanout/.git"
	worktreeACommonDir := commonDir
	worktreeBCommonDir := commonDir

	gotA := HerdrSessionName(worktreeACommonDir)
	gotB := HerdrSessionName(worktreeBCommonDir)
	if gotA != gotB {
		t.Fatalf("linked worktree session names differ: %q != %q", gotA, gotB)
	}
	if want := "fanout-fanout-e4709e1b043a0622"; gotA != want {
		t.Fatalf("HerdrSessionName() = %q, want %q", gotA, want)
	}
}

func TestHerdrSessionNameDistinguishesIndependentClones(t *testing.T) {
	cloneA := HerdrSessionName("/Users/alice/src/fanout/.git")
	cloneB := HerdrSessionName("/Users/alice/tmp/fanout/.git")
	if cloneA == cloneB {
		t.Fatalf("independent clone session names collide: %q", cloneA)
	}
}

func TestHerdrSessionNameIsValidBoundedAndNonDefault(t *testing.T) {
	commonDir := "/private/tmp/" + strings.Repeat("A very_long.repo name_", 10) + "/.git"
	got := HerdrSessionName(commonDir)

	if got == "default" {
		t.Fatal("HerdrSessionName() returned the herdr default session name")
	}
	if len(got) > MaxHerdrSessionNameLength {
		t.Fatalf("session name length = %d, want <= %d: %q", len(got), MaxHerdrSessionNameLength, got)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(got) {
		t.Fatalf("session name contains invalid characters: %q", got)
	}
	if !strings.HasPrefix(got, "fanout-a-very-long-repo-name-") {
		t.Fatalf("session name lacks readable repository token: %q", got)
	}
}

func TestHerdrSessionNameFallsBackForNonASCIIRepoName(t *testing.T) {
	got := HerdrSessionName("/Users/alice/src/ファンアウト/.git")
	if !strings.HasPrefix(got, "fanout-repo-") {
		t.Fatalf("HerdrSessionName() = %q, want fallback repository token", got)
	}
}

func TestSlugIsDeterministicKebabWithIssueNumber(t *testing.T) {
	if got, want := Slug("Fix auth timeout", 123), "fix-auth-timeout-123"; got != want {
		t.Fatalf("Slug() = %q, want %q", got, want)
	}
}

func TestSlugFallsBackForNonASCIIOnlyTitle(t *testing.T) {
	if got, want := Slug("コア作成", 82), "issue-82"; got != want {
		t.Fatalf("Slug() = %q, want %q", got, want)
	}
}

func TestSlugCapsLengthAndPreservesIssueNumber(t *testing.T) {
	got := Slug(strings.Repeat("a", 300), 12345)
	if len(got) > MaxSlugLength {
		t.Fatalf("Slug length = %d, want <= %d: %q", len(got), MaxSlugLength, got)
	}
	if !strings.HasSuffix(got, "-12345") {
		t.Fatalf("Slug() = %q, want issue number suffix", got)
	}
}

func TestBranchNameHonorsOverride(t *testing.T) {
	if got, want := BranchName("feat/custom", DefaultBranchPrefix, "child-1"), "feat/custom"; got != want {
		t.Fatalf("BranchName override = %q, want %q", got, want)
	}
	if got, want := BranchName("", "", "child-1"), "fanout/child-1"; got != want {
		t.Fatalf("BranchName default = %q, want %q", got, want)
	}
}

func TestEnsureIssueSuffix(t *testing.T) {
	if got, want := EnsureIssueSuffix("fix-login-timeout", 17), "fix-login-timeout-17"; got != want {
		t.Fatalf("EnsureIssueSuffix() = %q, want %q", got, want)
	}
	if got, want := EnsureIssueSuffix("fix-login-timeout-17", 17), "fix-login-timeout-17"; got != want {
		t.Fatalf("EnsureIssueSuffix() existing suffix = %q, want %q", got, want)
	}
}

func TestQualifySlugForParentKeepsIssueSuffix(t *testing.T) {
	got := QualifySlugForParent("shared-child-501", "200", 501)
	if want := "shared-child-parent-200-501"; got != want {
		t.Fatalf("QualifySlugForParent() = %q, want %q", got, want)
	}
}

func TestQualifySlugForProjectParentStaysBounded(t *testing.T) {
	parent := "https://github.com/users/butaosuinu/projects/12345"
	got := QualifySlugForParent(strings.Repeat("a", 100)+"-77", parent, 77)
	if len(got) > MaxSlugLength {
		t.Fatalf("qualified slug length = %d, want <= %d: %q", len(got), MaxSlugLength, got)
	}
	if !strings.HasSuffix(got, "-77") {
		t.Fatalf("qualified slug = %q, want issue suffix", got)
	}
}
