package ghissue

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunnerRateLimitCooldownSkipsProcessAndResumes(t *testing.T) {
	now := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)
	gate := newRateLimitGate(func() time.Time { return now })
	first := Runner{rateLimitGate: gate}
	second := Runner{rateLimitGate: gate}
	argsPath := installFakeGHScript(t, `
printf '%s\n' "$*" >> "$GH_FAKE_ARGS"
calls="$(wc -l < "$GH_FAKE_ARGS" | tr -d ' ')"
if [[ "$calls" == "1" ]]; then
  printf 'HTTP 429: API rate limit exceeded' >&2
  exit 1
fi
printf 'owner/repo'
`)

	if _, err := first.RepoNameWithOwner(); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("RepoNameWithOwner() error = %v, want ErrRateLimited", err)
	}

	err := second.PostIssueComment(587, "body")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("PostIssueComment() error = %v, want ErrRateLimited", err)
	}
	if !strings.Contains(err.Error(), "cooldown 30s remaining") {
		t.Fatalf("PostIssueComment() error = %v, want cooldown remaining", err)
	}
	assertFakeGHCallCount(t, argsPath, 1)

	now = now.Add(rateLimitInitialBackoff)
	got, err := second.RepoNameWithOwner()
	if err != nil {
		t.Fatal(err)
	}
	if got != "owner/repo" {
		t.Fatalf("RepoNameWithOwner() = %q, want owner/repo", got)
	}
	assertFakeGHCallCount(t, argsPath, 2)
}

func TestRunnerPreservesNonRateLimitError(t *testing.T) {
	gate := newRateLimitGate(func() time.Time {
		return time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)
	})
	installFakeGHWithResult(t, "", "authentication failed", 1)

	_, err := (Runner{rateLimitGate: gate}).RepoNameWithOwner()
	const want = "gh repo view --json nameWithOwner -q .nameWithOwner: authentication failed"
	if err == nil || err.Error() != want {
		t.Fatalf("RepoNameWithOwner() error = %v, want %q", err, want)
	}
	if errors.Is(err, ErrRateLimited) {
		t.Fatalf("RepoNameWithOwner() error = %v, do not want ErrRateLimited", err)
	}
}

func TestIssueDetailsPreservesRateLimitSentinelAcrossJoinedErrors(t *testing.T) {
	now := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)
	runner := Runner{
		rateLimitGate: newRateLimitGate(func() time.Time { return now }),
	}
	argsPath := installFakeGHScript(t, `
printf 'call\n' >> "$GH_FAKE_ARGS"
printf 'GraphQL: API rate limit already exceeded' >&2
exit 1
`)
	nums := make([]int, issueDetailsBatchSize+1)
	for i := range nums {
		nums[i] = i + 1
	}

	_, err := runner.IssueDetails(nums)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("IssueDetails() error = %v, want ErrRateLimited", err)
	}
	assertFakeGHCallCount(t, argsPath, 1)
}

func TestIsRateLimitError(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		want    bool
	}{
		{name: "HTTP 403", message: "gh api: HTTP 403: forbidden", want: true},
		{name: "HTTP 429", message: "gh api: HTTP 429: too many requests", want: true},
		{name: "API limit", message: "GraphQL: API rate limit exceeded", want: true},
		{name: "API limit already", message: "GraphQL: API rate limit already exceeded", want: true},
		{name: "secondary limit", message: "You have exceeded a secondary rate limit", want: true},
		{name: "unrelated", message: "gh api: HTTP 500: server error", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRateLimitError(errors.New(tc.message)); got != tc.want {
				t.Fatalf("isRateLimitError(%q) = %v, want %v", tc.message, got, tc.want)
			}
		})
	}
}

func TestRateLimitResetAt(t *testing.T) {
	now := time.Date(2026, 7, 31, 9, 0, 0, 0, time.UTC)
	resetAt := now.Add(5 * time.Minute)
	for _, tc := range []struct {
		name    string
		message string
		want    time.Time
	}{
		{
			name:    "unix header",
			message: "HTTP 429\nX-RateLimit-Reset: " + strconv.FormatInt(resetAt.Unix(), 10),
			want:    resetAt,
		},
		{
			name:    "RFC3339 field",
			message: `API rate limit exceeded; "resetAt":"` + resetAt.Format(time.RFC3339) + `"`,
			want:    resetAt,
		},
		{
			name:    "retry after",
			message: "secondary rate limit; Retry-After: 90",
			want:    now.Add(90 * time.Second),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := rateLimitResetAt(tc.message, now)
			if !ok || !got.Equal(tc.want) {
				t.Fatalf("rateLimitResetAt(%q) = %s, %v, want %s, true", tc.message, got, ok, tc.want)
			}
		})
	}
}

func TestRateLimitBackoffIsCapped(t *testing.T) {
	for _, tc := range []struct {
		failures int
		want     time.Duration
	}{
		{failures: 1, want: 30 * time.Second},
		{failures: 2, want: time.Minute},
		{failures: 5, want: 8 * time.Minute},
		{failures: 6, want: rateLimitMaxBackoff},
		{failures: 100, want: rateLimitMaxBackoff},
	} {
		if got := rateLimitBackoff(tc.failures); got != tc.want {
			t.Fatalf("rateLimitBackoff(%d) = %s, want %s", tc.failures, got, tc.want)
		}
	}
}

func assertFakeGHCallCount(t *testing.T, argsPath string, want int) {
	t.Helper()
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "\n"); got != want {
		t.Fatalf("gh call count = %d, want %d\n%s", got, want, data)
	}
}
