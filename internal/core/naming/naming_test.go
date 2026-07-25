package naming

import (
	"regexp"
	"strings"
	"testing"
)

func TestHerdrSessionNameUsesPhysicalGitCommonDirectoryIdentity(t *testing.T) {
	got := HerdrSessionName(42, 81)
	if got != HerdrSessionName(42, 81) {
		t.Fatal("HerdrSessionName is not deterministic")
	}
	if got == HerdrSessionName(42, 82) || got == HerdrSessionName(43, 81) {
		t.Fatalf("independent clones share session name %q", got)
	}
	if len(got) > MaxHerdrSessionNameLength || !regexp.MustCompile(`^[A-Za-z0-9._-]+$`).MatchString(got) {
		t.Fatalf("invalid herdr session name %q", got)
	}
}

func TestHerdrSessionNameIgnoresPathAliasesForSameIdentity(t *testing.T) {
	first := HerdrSessionName(42, 81)
	second := HerdrSessionName(42, 81)
	if first != second || len(first) > MaxHerdrSessionNameLength {
		t.Fatalf("physical identity aliases = %q, %q", first, second)
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
