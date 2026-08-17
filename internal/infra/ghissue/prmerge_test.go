package ghissue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestParseMergeMethod(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want MergeMethod
		ok   bool
	}{
		{name: "squash", in: "squash", want: MergeSquash, ok: true},
		{name: "merge commit", in: "merge", want: MergeCommit, ok: true},
		{name: "rebase", in: "rebase", want: MergeRebase, ok: true},
		{name: "rejects the empty string rather than defaulting", in: "", ok: false},
		{name: "rejects an unknown strategy", in: "fast-forward", ok: false},
		{name: "is case sensitive", in: "Squash", ok: false},
		{name: "rejects a flag spelling", in: "--squash", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseMergeMethod(tt.in)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("ParseMergeMethod(%q) = %q, %v, want %q, %v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestMergePRArgs(t *testing.T) {
	tests := []struct {
		name string
		req  MergePRRequest
		want []string
	}{
		{
			name: "squash carries the head commit guard",
			req:  MergePRRequest{Owner: "o", Repo: "r", Number: 673, Method: MergeSquash, HeadSha: "abc123"},
			want: []string{"pr", "merge", "673", "-R", "o/r", "--squash", "--match-head-commit", "abc123"},
		},
		{
			name: "merge commit",
			req:  MergePRRequest{Owner: "o", Repo: "r", Number: 7, Method: MergeCommit, HeadSha: "abc123"},
			want: []string{"pr", "merge", "7", "-R", "o/r", "--merge", "--match-head-commit", "abc123"},
		},
		{
			name: "rebase",
			req:  MergePRRequest{Owner: "o", Repo: "r", Number: 7, Method: MergeRebase, HeadSha: "abc123"},
			want: []string{"pr", "merge", "7", "-R", "o/r", "--rebase", "--match-head-commit", "abc123"},
		},
		{
			// The `gh pr list` path leaves HeadSha empty; sending an empty
			// --match-head-commit would fail every merge on those rows.
			name: "omits the guard when no head sha is known",
			req:  MergePRRequest{Owner: "o", Repo: "r", Number: 7, Method: MergeSquash},
			want: []string{"pr", "merge", "7", "-R", "o/r", "--squash"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argsPath := installFakeGH(t, "")
			if err := (Runner{}).MergePR(context.Background(), tt.req); err != nil {
				t.Fatal(err)
			}
			assertFakeGHArgs(t, argsPath, tt.want)
		})
	}
}

// TestMergePRNeverWidensItsBlastRadius is the load-bearing guard on the
// dashboard's only mutation. Each of these flags would let one click do
// something the invariant catalog promises it cannot: --admin bypasses branch
// protection, --auto arms a merge that fires later with no human present, and
// -d/--delete-branch hands gh a local branch that fanout has checked out in a
// linked worktree.
func TestMergePRNeverWidensItsBlastRadius(t *testing.T) {
	for _, method := range []MergeMethod{MergeSquash, MergeCommit, MergeRebase} {
		t.Run(string(method), func(t *testing.T) {
			argsPath := installFakeGH(t, "")
			req := MergePRRequest{Owner: "o", Repo: "r", Number: 7, Method: method, HeadSha: "abc123"}
			if err := (Runner{}).MergePR(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(argsPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, banned := range []string{"--admin", "--auto", "--delete-branch", "-d"} {
				for arg := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
					if arg == banned {
						t.Fatalf("MergePR passed %q to gh; argv = %q", banned, data)
					}
				}
			}
		})
	}
}

func TestMergePRRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name string
		req  MergePRRequest
	}{
		{name: "missing owner", req: MergePRRequest{Repo: "r", Number: 7, Method: MergeSquash}},
		{name: "missing repo", req: MergePRRequest{Owner: "o", Number: 7, Method: MergeSquash}},
		{name: "zero pr number", req: MergePRRequest{Owner: "o", Repo: "r", Method: MergeSquash}},
		{name: "negative pr number", req: MergePRRequest{Owner: "o", Repo: "r", Number: -1, Method: MergeSquash}},
		{name: "unset method", req: MergePRRequest{Owner: "o", Repo: "r", Number: 7}},
		{name: "unknown method", req: MergePRRequest{Owner: "o", Repo: "r", Number: 7, Method: "ff"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argsPath := installFakeGH(t, "")
			if err := (Runner{}).MergePR(context.Background(), tt.req); err == nil {
				t.Fatal("MergePR() error = nil, want a validation error")
			}
			if _, err := os.ReadFile(argsPath); err == nil {
				t.Fatal("MergePR ran gh despite failing validation")
			}
		})
	}
}

func TestMergePRSurfacesGHFailure(t *testing.T) {
	installFakeGHWithResult(t, "", "Pull request is not mergeable", 1)
	req := MergePRRequest{Owner: "o", Repo: "r", Number: 7, Method: MergeSquash, HeadSha: "abc123"}
	err := (Runner{}).MergePR(context.Background(), req)
	if err == nil {
		t.Fatal("MergePR() error = nil, want the gh failure")
	}
	if !strings.Contains(err.Error(), "merge pull request #7") ||
		!strings.Contains(err.Error(), "not mergeable") {
		t.Fatalf("MergePR() error = %v, want it to name the PR and carry gh's stderr", err)
	}
}

// deleteScript answers the OID read with oid, then records the DELETE.
func deleteScript(t *testing.T, oid string) string {
	t.Helper()
	return installFakeGHScript(t, `
printf '%s\n' "$*" >> "$GH_FAKE_ARGS"
case "$*" in
  *--method\ DELETE*) exit 0 ;;
  *) printf '%s' "`+oid+`" ;;
esac
`)
}

func TestDeleteRemoteBranchArgs(t *testing.T) {
	argsPath := deleteScript(t, "abc123")
	if err := (Runner{}).DeleteRemoteBranch(context.Background(), "o", "r", "fanout/foo", "abc123"); err != nil {
		t.Fatal(err)
	}
	assertFakeGHCommandLines(t, argsPath, []string{
		"api repos/o/r/git/ref/heads/fanout/foo -q .object.sha",
		"api --method DELETE repos/o/r/git/refs/heads/fanout/foo",
	})
}

// TestDeleteRemoteBranchEscapesTheRefPath pins that a legal-but-URL-hostile ref
// reaches the API intact. `#` would otherwise truncate the path at a fragment,
// and the resulting 404 is the shape isMissingRefError reports as success — so
// the branch would survive while the response claimed it was deleted.
func TestDeleteRemoteBranchEscapesTheRefPath(t *testing.T) {
	argsPath := deleteScript(t, "abc123")
	err := (Runner{}).DeleteRemoteBranch(context.Background(), "o", "r", "feature/#123", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	assertFakeGHCommandLines(t, argsPath, []string{
		"api repos/o/r/git/ref/heads/feature/%23123 -q .object.sha",
		"api --method DELETE repos/o/r/git/refs/heads/feature/%23123",
	})
}

// TestDeleteRemoteBranchRefusesAMovedBranch is the fence on the destructive
// step: the delete runs after the merge, so a pane that kept pushing would
// otherwise have its unmerged commits discarded.
func TestDeleteRemoteBranchRefusesAMovedBranch(t *testing.T) {
	argsPath := deleteScript(t, "def456")
	err := (Runner{}).DeleteRemoteBranch(context.Background(), "o", "r", "fanout/foo", "abc123")
	if err == nil {
		t.Fatal("DeleteRemoteBranch() error = nil, want a refusal for the moved branch")
	}
	assertFakeGHCommandLines(t, argsPath, []string{
		"api repos/o/r/git/ref/heads/fanout/foo -q .object.sha",
	})
}

func TestDeleteRemoteBranchTreatsMissingRefAsDone(t *testing.T) {
	tests := []struct {
		name    string
		stderr  string
		wantErr bool
	}{
		{name: "422 reference does not exist", stderr: "gh: Reference does not exist (HTTP 422)"},
		{name: "404 on the ref path", stderr: "gh: Not Found (HTTP 404)"},
		{name: "any other failure still fails", stderr: "gh: Bad credentials (HTTP 401)", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installFakeGHWithResult(t, "", tt.stderr, 1)
			err := (Runner{}).DeleteRemoteBranch(context.Background(), "o", "r", "fanout/foo", "abc123")
			if (err != nil) != tt.wantErr {
				t.Fatalf("DeleteRemoteBranch() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDeleteRemoteBranchRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name        string
		branch      string
		expectedOID string
	}{
		{name: "blank branch", branch: "  ", expectedOID: "abc123"},
		// Without an expected commit there is nothing to fence the delete on.
		{name: "no expected commit", branch: "fanout/foo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argsPath := installFakeGH(t, "")
			err := (Runner{}).DeleteRemoteBranch(context.Background(), "o", "r", tt.branch, tt.expectedOID)
			if err == nil {
				t.Fatal("DeleteRemoteBranch() error = nil, want a validation error")
			}
			if _, err := os.ReadFile(argsPath); err == nil {
				t.Fatal("DeleteRemoteBranch ran gh despite failing validation")
			}
		})
	}
}

// TestPRStateAsksGitHubRatherThanTrustingTheExitCode pins the merge-queue guard:
// `gh pr merge` exits 0 after enqueueing, so the exit code alone would report an
// unmerged pull request as merged. The same read also carries the base branch,
// which --match-head-commit does not pin.
func TestPRStateAsksGitHubRatherThanTrustingTheExitCode(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want PRTarget
	}{
		{
			name: "merged by state",
			out:  `{"state":"MERGED","mergedAt":"2026-08-15T00:00:00Z","baseRefName":"main","headRefOid":"abc"}`,
			want: PRTarget{Merged: true, BaseRef: "main", HeadSha: "abc"},
		},
		{
			name: "merged by mergedAt alone",
			out:  `{"state":"OPEN","mergedAt":"2026-08-15T00:00:00Z","baseRefName":"main","headRefOid":"abc"}`,
			want: PRTarget{Merged: true, BaseRef: "main", HeadSha: "abc"},
		},
		{
			name: "queued is not merged",
			out:  `{"state":"OPEN","mergedAt":null,"baseRefName":"release","headRefOid":"def"}`,
			want: PRTarget{Merged: false, BaseRef: "release", HeadSha: "def"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			argsPath := installFakeGH(t, tt.out)
			got, err := (Runner{}).PRState(context.Background(), "o", "r", 7)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("PRState() = %#v, want %#v", got, tt.want)
			}
			assertFakeGHArgs(t, argsPath, []string{
				"pr", "view", "7", "-R", "o/r", "--json", "state,mergedAt,baseRefName,headRefName,headRefOid,autoMergeRequest",
			})
		})
	}
}

// TestIsTransportFailure separates "gh could not reach GitHub" from "GitHub said
// no". Only the first leaves an executed mutation unaccounted for.
func TestIsTransportFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "deadline", err: context.DeadlineExceeded, want: true},
		{name: "cancel", err: context.Canceled, want: true},
		{name: "connection refused", err: errors.New("dial tcp: connection refused"), want: true},
		{name: "a clean rejection is not transport", err: errors.New("Pull request is not mergeable")},
		{
			// gh never ran, so the outcome is known: nothing was sent.
			name: "gh missing is not transport", err: errors.New(`exec: "gh": executable file not found`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTransportFailure(tt.err); got != tt.want {
				t.Fatalf("IsTransportFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestIsPreSendFailure pins what is provably retryable. These leave nothing for
// GitHub to have accepted, so probing after one of them would run through the
// same broken gh and hold the pull request on an outcome nobody can settle.
func TestIsPreSendFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "gh missing", err: errors.New(`exec: "gh": executable file not found`), want: true},
		{
			name: "credentials refused",
			err:  errors.New("gh: authentication failed; run gh auth login"), want: true,
		},
		{name: "a dropped connection is not pre-send", err: errors.New("dial tcp: connection reset")},
		{name: "a clean rejection is not pre-send", err: errors.New("Pull request is not mergeable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPreSendFailure(tt.err); got != tt.want {
				t.Fatalf("IsPreSendFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestMergePRPropagatesContextCancellation pins that a handler deadline reaches
// the gh process with its identity intact, so the caller can map it to 504
// without parsing an os/exec signal error.
func TestMergePRPropagatesContextCancellation(t *testing.T) {
	installFakeGH(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := MergePRRequest{Owner: "o", Repo: "r", Number: 7, Method: MergeSquash, HeadSha: "abc123"}
	err := (Runner{}).MergePR(ctx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("MergePR() error = %v, want context.Canceled", err)
	}
}

// TestOpenHeadNumbers pins which rows count as "this branch is still in use".
// `gh pr list --head` takes a bare branch name — the `owner:branch` form is not
// supported — so the answer carries fork pull requests that merely share the
// name, and it is capped by `--limit`.
func TestOpenHeadNumbers(t *testing.T) {
	row := func(num int, owner, name, ref string) string {
		return fmt.Sprintf(
			`{"number":%d,"headRefName":%q,"headRepository":{"name":%q},"headRepositoryOwner":{"login":%q}}`,
			num, ref, name, owner)
	}
	tests := []struct {
		name    string
		rows    []string
		want    []int
		wantErr bool
	}{
		{
			name: "counts this repository's own head",
			rows: []string{row(9, "o", "r", "fanout/foo")},
			want: []int{9},
		},
		{
			// A fork's branch of the same name is a different branch; letting it
			// veto would strand the delete on any popular branch name.
			name: "drops a fork with the same branch name",
			rows: []string{row(9, "stranger", "r", "fanout/foo")},
			want: []int{},
		},
		{
			name: "drops a same-named repository under another owner",
			rows: []string{row(9, "stranger", "r", "fanout/foo"), row(10, "o", "r", "fanout/foo")},
			want: []int{10},
		},
		{
			name: "drops a row gh matched on a different branch",
			rows: []string{row(9, "o", "r", "fanout/other")},
			want: []int{},
		},
		{name: "no open pull request uses the branch", rows: nil, want: []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := []byte("[" + strings.Join(tt.rows, ",") + "]")
			got, err := openHeadNumbers(out, "o", "r", "fanout/foo")
			if (err != nil) != tt.wantErr {
				t.Fatalf("openHeadNumbers() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("openHeadNumbers() = %v, want %v", got, tt.want)
			}
		})
	}

	// A truncated list reads as "nobody else uses this branch", which is the one
	// answer that lets a live branch be deleted. Refuse instead.
	t.Run("refuses a list that may be truncated", func(t *testing.T) {
		rows := make([]string, openHeadListLimit)
		for i := range rows {
			rows[i] = row(i+1, "o", "r", "fanout/foo")
		}
		out := []byte("[" + strings.Join(rows, ",") + "]")
		if _, err := openHeadNumbers(out, "o", "r", "fanout/foo"); err == nil {
			t.Fatal("openHeadNumbers() error = nil, want the truncation refusal")
		}
	})
}
