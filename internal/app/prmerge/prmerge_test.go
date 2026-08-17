package prmerge

import (
	"context"
	"errors"
	"testing"

	"github.com/butaosuinu/fanout/internal/app/sessionview"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
)

func TestSelectRef(t *testing.T) {
	row := sessionview.PaneView{PRs: []ghissue.PRRef{
		{Number: 10, State: "MERGED"},
		{Number: 11, State: "OPEN"},
	}}
	tests := []struct {
		name    string
		pv      sessionview.PaneView
		number  int
		want    int
		wantErr bool
	}{
		{
			// PrimaryPR would answer #10 here; a merge action must not.
			name: "picks the named ref, not the merged one", pv: row, number: 11, want: 11,
		},
		{name: "picks a merged ref when named", pv: row, number: 10, want: 10},
		{name: "rejects a number the row does not carry", pv: row, number: 12, wantErr: true},
		{name: "rejects a row with no PRs", pv: sessionview.PaneView{}, number: 11, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SelectRef(tt.pv, tt.number)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SelectRef(pv, %d) error = %v, wantErr %v", tt.number, err, tt.wantErr)
			}
			if tt.wantErr {
				if !errors.Is(err, ErrPRNotOnRow) {
					t.Fatalf("SelectRef(pv, %d) error = %v, want ErrPRNotOnRow", tt.number, err)
				}
				return
			}
			if got.Number != tt.want {
				t.Fatalf("SelectRef(pv, %d) = #%d, want #%d", tt.number, got.Number, tt.want)
			}
		})
	}
}

func TestPreflight(t *testing.T) {
	const sha = "abc123"
	tests := []struct {
		name    string
		ref     ghissue.PRRef
		headSha string
		baseRef string
		want    error
	}{
		{name: "open mergeable PR passes", ref: ghissue.PRRef{State: "OPEN", Mergeable: "MERGEABLE", HeadSha: sha}, headSha: sha},
		{name: "merged by state", ref: ghissue.PRRef{State: "MERGED", HeadSha: sha}, headSha: sha, want: ErrAlreadyMerged},
		{name: "merged by mergedAt alone", ref: ghissue.PRRef{State: "OPEN", MergedAt: new("2026-08-15T00:00:00Z"), HeadSha: sha}, headSha: sha, want: ErrAlreadyMerged},
		{name: "closed", ref: ghissue.PRRef{State: "CLOSED", HeadSha: sha}, headSha: sha, want: ErrPRClosed},
		{name: "draft", ref: ghissue.PRRef{State: "OPEN", IsDraft: true, HeadSha: sha}, headSha: sha, want: ErrPRDraft},
		{name: "conflicting", ref: ghissue.PRRef{State: "OPEN", Mergeable: "CONFLICTING", HeadSha: sha}, headSha: sha, want: ErrPRConflicting},
		{
			// "" is "not known", not "clean" — refusing it would block ordinary
			// merges during GitHub's recompute window.
			name: "unknown mergeability passes", ref: ghissue.PRRef{State: "OPEN", HeadSha: sha}, headSha: sha,
		},
		{name: "head moved", ref: ghissue.PRRef{State: "OPEN", HeadSha: "def456"}, headSha: sha, want: ErrStaleHead},
		{
			// The `gh pr list` path leaves HeadSha empty; there is nothing to
			// compare, so the guard cannot be required.
			name: "no recorded head sha skips the guard", ref: ghissue.PRRef{State: "OPEN"}, headSha: "",
		},
		{name: "merged wins over draft", ref: ghissue.PRRef{State: "MERGED", IsDraft: true}, want: ErrAlreadyMerged},
		{name: "closed wins over conflicting", ref: ghissue.PRRef{State: "CLOSED", Mergeable: "CONFLICTING"}, want: ErrPRClosed},
		{name: "review required is not a gate", ref: ghissue.PRRef{State: "OPEN", ReviewDecision: "REVIEW_REQUIRED"}},
		{name: "changes requested is not a gate", ref: ghissue.PRRef{State: "OPEN", ReviewDecision: "CHANGES_REQUESTED"}},
		{name: "failing CI is not a gate", ref: ghissue.PRRef{State: "OPEN", CIStatus: "fail"}},
		{
			// GitHub lets a PR be retargeted without touching its head, so the SHA
			// alone does not pin where the merge lands.
			name:    "retargeted base",
			ref:     ghissue.PRRef{State: "OPEN", HeadSha: sha, BaseRef: "release"},
			headSha: sha, baseRef: "main", want: ErrStaleBase,
		},
		{
			name:    "same base passes",
			ref:     ghissue.PRRef{State: "OPEN", HeadSha: sha, BaseRef: "main"},
			headSha: sha, baseRef: "main",
		},
		{
			// A client that never rendered a base cannot pin one.
			name:    "no rendered base skips the guard",
			ref:     ghissue.PRRef{State: "OPEN", HeadSha: sha, BaseRef: "main"},
			headSha: sha,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Preflight(tt.ref, RenderedRef{HeadSha: tt.headSha, BaseRef: tt.baseRef})
			if tt.want == nil {
				if err != nil {
					t.Fatalf("Preflight(%#v, %q) = %v, want nil", tt.ref, tt.headSha, err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("Preflight(%#v, %q) = %v, want %v", tt.ref, tt.headSha, err, tt.want)
			}
		})
	}
}

// TestVerifyRowOwns guards the branch-backed lookup. `pullRequests(headRefName:)`
// returns every PR in the repository with that head branch name, forks included,
// so a stranger's PR can land on a plan or @manual row.
func TestVerifyRowOwns(t *testing.T) {
	branchRow := sessionview.PaneView{IssueNum: -1, BranchName: "fanout/foo"}
	tests := []struct {
		name    string
		pv      sessionview.PaneView
		ref     ghissue.PRRef
		wantErr bool
	}{
		{
			name: "branch row accepts its own head",
			pv:   branchRow,
			ref:  ghissue.PRRef{Number: 7, HeadRef: "fanout/foo", HeadRepo: "o/r", BaseRepo: "o/r"},
		},
		{
			name:    "branch row refuses a fork with the same branch name",
			pv:      branchRow,
			ref:     ghissue.PRRef{Number: 7, HeadRef: "fanout/foo", HeadRepo: "stranger/r", BaseRepo: "o/r"},
			wantErr: true,
		},
		{
			name:    "branch row refuses a different head branch",
			pv:      branchRow,
			ref:     ghissue.PRRef{Number: 7, HeadRef: "other", HeadRepo: "o/r", BaseRepo: "o/r"},
			wantErr: true,
		},
		{
			// Ownership cannot be established, so it is not established.
			name:    "branch row refuses an unknown head repository",
			pv:      branchRow,
			ref:     ghissue.PRRef{Number: 7, HeadRef: "fanout/foo", BaseRepo: "o/r"},
			wantErr: true,
		},
		{
			// `Fixes owner/repo#N` closes an issue across repositories, so a row's
			// PR list can carry a PR based somewhere else entirely. Merging it by
			// number against this repo would hit the wrong pull request.
			name:    "refuses a PR based on another repository",
			pv:      sessionview.PaneView{IssueNum: 578},
			ref:     ghissue.PRRef{Number: 7, HeadRepo: "o/r", BaseRepo: "other/repo"},
			wantErr: true,
		},
		{
			name:    "refuses a PR whose base repository is unknown",
			pv:      sessionview.PaneView{IssueNum: 578},
			ref:     ghissue.PRRef{Number: 7, HeadRepo: "o/r"},
			wantErr: true,
		},
		{
			// Issue rows attribute PRs through the closing-PR link, which is an
			// identity — a fork's PR legitimately closes your issue.
			name: "issue row accepts a fork PR",
			pv:   sessionview.PaneView{IssueNum: 578},
			ref:  ghissue.PRRef{Number: 7, HeadRef: "whatever", HeadRepo: "stranger/r", BaseRepo: "o/r"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyRowOwns(tt.pv, tt.ref, "o/r")
			if (err != nil) != tt.wantErr {
				t.Fatalf("VerifyRowOwns() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr && !errors.Is(err, ErrForeignPR) {
				t.Fatalf("VerifyRowOwns() error = %v, want ErrForeignPR", err)
			}
		})
	}
}

func TestPlanDelete(t *testing.T) {
	tests := []struct {
		name    string
		pv      sessionview.PaneView
		ref     ghissue.PRRef
		want    string
		wantErr bool
	}{
		{
			name: "PR head ref matching the row",
			pv:   sessionview.PaneView{BranchName: "fanout/foo"},
			ref:  ghissue.PRRef{HeadRef: "fanout/foo", HeadRepo: "o/r"},
			want: "fanout/foo",
		},
		{
			// The `gh pr list` path leaves HeadRef empty. Falling back to the
			// recorded branch would delete a ref with no evidence it is the one
			// the merge consumed, and would make the mismatch check below compare
			// the value against itself.
			name:    "refuses when the PR head ref is unknown",
			pv:      sessionview.PaneView{BranchName: "fanout/foo"},
			wantErr: true,
		},
		{
			name: "PR head ref with no recorded branch",
			ref:  ghissue.PRRef{HeadRef: "fanout/foo", HeadRepo: "o/r"},
			want: "fanout/foo",
		},
		{
			name:    "refuses when the two disagree",
			pv:      sessionview.PaneView{BranchName: "fanout/foo"},
			ref:     ghissue.PRRef{HeadRef: "someone-elses-branch", HeadRepo: "o/r"},
			wantErr: true,
		},
		{name: "refuses when neither is known", wantErr: true},
		{
			// Issue rows accept fork PRs, so "head repository unknown" must not
			// fall through to "same repository by default".
			name:    "refuses an unknown head repository",
			pv:      sessionview.PaneView{BranchName: "fanout/foo"},
			ref:     ghissue.PRRef{HeadRef: "fanout/foo"},
			wantErr: true,
		},
		{
			// A fork's head lives elsewhere; deleting by name in the base repo
			// would remove a same-named branch this PR never owned.
			name:    "refuses a fork head",
			pv:      sessionview.PaneView{BranchName: "fanout/foo"},
			ref:     ghissue.PRRef{HeadRef: "fanout/foo", HeadRepo: "someone/fork"},
			wantErr: true,
		},
		{
			name: "accepts a head ref in the same repository",
			pv:   sessionview.PaneView{BranchName: "fanout/foo"},
			ref:  ghissue.PRRef{HeadRef: "fanout/foo", HeadRepo: "o/r"},
			want: "fanout/foo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PlanDelete(tt.pv, tt.ref, "o/r")
			if (err != nil) != tt.wantErr {
				t.Fatalf("PlanDelete() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !errors.Is(err, ErrNoBranch) && !errors.Is(err, ErrForkHead) {
					t.Fatalf("PlanDelete() error = %v, want ErrNoBranch or ErrForkHead", err)
				}
				return
			}
			if got != tt.want {
				t.Fatalf("PlanDelete() = %q, want %q", got, tt.want)
			}
		})
	}
}

type deleteCall struct {
	branch      string
	expectedOID string
}

type fakePort struct {
	mergeCalls  []ghissue.MergePRRequest
	deleteCalls []deleteCall
	mergeErr    error
	deleteErr   error
	// notMerged makes PRState answer "not merged", the way a merge-queue base does.
	notMerged bool
	// fenceErr fails the pre-merge read; confirmErr fails the post-merge one.
	// They are separate because only the second leaves an executed mutation
	// unaccounted for.
	fenceErr   error
	confirmErr error
	// liveBase / liveHead override what the pre-merge fence reads back.
	liveBase string
	liveHead string
	// alwaysMerged reports merged on the very first read (the delete path has no
	// pre-merge fence to skip past).
	alwaysMerged bool
	// openHeads is what OpenPRNumbersForHead answers: the OPEN pull requests
	// sharing the head branch.
	liveHeadRef  string
	openHeads    []int
	openHeadsErr error
	mergedRead   int
}

func (f *fakePort) MergePR(_ context.Context, req ghissue.MergePRRequest) error {
	f.mergeCalls = append(f.mergeCalls, req)
	return f.mergeErr
}

func (f *fakePort) PRState(_ context.Context, _, _ string, _ int) (ghissue.PRTarget, error) {
	f.mergedRead++
	if f.mergedRead == 1 && f.fenceErr != nil {
		return ghissue.PRTarget{}, f.fenceErr
	}
	if f.mergedRead > 1 && f.confirmErr != nil {
		return ghissue.PRTarget{}, f.confirmErr
	}
	// The pre-merge fence reads first; only later reads report the merge.
	merged := f.alwaysMerged || (!f.notMerged && f.mergedRead > 1)
	return ghissue.PRTarget{
		Merged:  merged,
		BaseRef: cmpOr(f.liveBase, "main"),
		HeadRef: cmpOr(f.liveHeadRef, "fanout/foo"),
		HeadSha: cmpOr(f.liveHead, "abc123"),
	}, nil
}

func cmpOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func (f *fakePort) OpenPRNumbersForHead(_ context.Context, _, _, _ string) ([]int, error) {
	return f.openHeads, f.openHeadsErr
}

func (f *fakePort) DeleteRemoteBranch(_ context.Context, _, _, branch, expectedOID string) error {
	f.deleteCalls = append(f.deleteCalls, deleteCall{branch: branch, expectedOID: expectedOID})
	return f.deleteErr
}

func baseRequest() Request {
	return Request{
		Owner: "o", Repo: "r", Number: 7,
		Method: ghissue.MergeSquash, HeadSha: "abc123", BaseRef: "main",
	}
}

func TestServiceMerge(t *testing.T) {
	t.Run("merges and confirms with GitHub", func(t *testing.T) {
		port := &fakePort{}
		res, err := Service{GH: port}.Merge(context.Background(), baseRequest())
		if err != nil {
			t.Fatal(err)
		}
		if !res.Merged || res.Queued || res.Unknown {
			t.Fatalf("Merge() = %#v, want a plain merge", res)
		}
		if len(port.mergeCalls) != 1 {
			t.Fatalf("merge calls = %d, want 1", len(port.mergeCalls))
		}
	})

	/* `gh pr merge` が 0 で終わっても merge queue では未マージ。 */
	t.Run("a queued merge is not reported as merged", func(t *testing.T) {
		port := &fakePort{notMerged: true}
		res, err := Service{GH: port}.Merge(context.Background(), baseRequest())
		if err != nil {
			t.Fatal(err)
		}
		if res.Merged || !res.Queued {
			t.Fatalf("Merge() = %#v, want queued and not merged", res)
		}
	})

	/* snapshot は最大 1 poll 古く、--match-head-commit は head しか固定しない。
	 * 直前の retarget をここで捕まえないと、レビューしていない branch へ入る。 */
	t.Run("refuses a base that was retargeted since the snapshot", func(t *testing.T) {
		port := &fakePort{liveBase: "release"}
		_, err := Service{GH: port}.Merge(context.Background(), baseRequest())
		if !errors.Is(err, ErrStaleBase) {
			t.Fatalf("Merge() error = %v, want ErrStaleBase", err)
		}
		if len(port.mergeCalls) != 0 {
			t.Fatalf("merge calls = %d, want 0", len(port.mergeCalls))
		}
	})

	t.Run("refuses a head that moved since the snapshot", func(t *testing.T) {
		port := &fakePort{liveHead: "def456"}
		_, err := Service{GH: port}.Merge(context.Background(), baseRequest())
		if !errors.Is(err, ErrStaleHead) {
			t.Fatalf("Merge() error = %v, want ErrStaleHead", err)
		}
		if len(port.mergeCalls) != 0 {
			t.Fatalf("merge calls = %d, want 0", len(port.mergeCalls))
		}
	})

	/* rate-limit ゲートは gh を走らせる前に弾くので、送信自体が起きていない。 */
	t.Run("a rate-limited send stays a plain retryable failure", func(t *testing.T) {
		port := &fakePort{mergeErr: ghissue.ErrRateLimited}
		res, err := Service{GH: port}.Merge(context.Background(), baseRequest())
		if !errors.Is(err, ghissue.ErrRateLimited) {
			t.Fatalf("Merge() error = %v, want ErrRateLimited", err)
		}
		if res.Unknown {
			t.Fatalf("Merge() = %#v, want no unknown hold on a request that never sent", res)
		}
		// Only the pre-merge fence read; probing again would hit the same cooldown.
		if port.mergedRead != 1 {
			t.Fatalf("PRState reads = %d, want 1", port.mergedRead)
		}
	})

	/* 送信後に deadline や切断が起きると、GitHub には届いているかもしれない。 */
	t.Run("a send failure that actually merged is reported as merged", func(t *testing.T) {
		port := &fakePort{mergeErr: context.DeadlineExceeded}
		res, err := Service{GH: port}.Merge(context.Background(), baseRequest())
		if err != nil {
			t.Fatalf("Merge() error = %v, want nil once GitHub confirms the merge", err)
		}
		if !res.Merged {
			t.Fatalf("Merge() = %#v, want merged", res)
		}
	})

	t.Run("a send failure with an unreadable state is unknown", func(t *testing.T) {
		port := &fakePort{mergeErr: context.DeadlineExceeded, confirmErr: errors.New("gh unavailable")}
		res, err := Service{GH: port}.Merge(context.Background(), baseRequest())
		if err != nil {
			t.Fatalf("Merge() error = %v, want nil so the caller does not invite a retry", err)
		}
		if !res.Unknown {
			t.Fatalf("Merge() = %#v, want an unknown outcome", res)
		}
	})

	/* queue が受理した直後に接続だけ落ちると、PR は OPEN のまま queue にいる。 */
	t.Run("a transport failure with an unmerged PR is unknown, not retryable", func(t *testing.T) {
		port := &fakePort{mergeErr: context.DeadlineExceeded, notMerged: true}
		res, err := Service{GH: port}.Merge(context.Background(), baseRequest())
		if err != nil {
			t.Fatalf("Merge() error = %v, want nil so the caller does not invite a retry", err)
		}
		if !res.Unknown {
			t.Fatalf("Merge() = %#v, want an unknown outcome", res)
		}
	})

	/* GitHub がはっきり断った場合は再試行してよい。 */
	t.Run("a clean GitHub rejection stays retryable", func(t *testing.T) {
		port := &fakePort{mergeErr: errors.New("Pull request is not mergeable"), notMerged: true}
		res, err := Service{GH: port}.Merge(context.Background(), baseRequest())
		if err == nil {
			t.Fatal("Merge() error = nil, want the rejection")
		}
		if res.Unknown {
			t.Fatalf("Merge() = %#v, want no unknown hold on a clean refusal", res)
		}
	})

	/* 結果不明の確認は削除に進ませない — というより、削除はもう別操作なので
	 * merge のどの経路からも remote ref に触らない。 */
	t.Run("no merge path ever touches a remote ref", func(t *testing.T) {
		for _, port := range []*fakePort{
			{},
			{notMerged: true},
			{confirmErr: errors.New("gh unavailable")},
			{mergeErr: context.DeadlineExceeded},
		} {
			_, _ = Service{GH: port}.Merge(context.Background(), baseRequest())
			if len(port.deleteCalls) != 0 {
				t.Fatalf("delete calls = %#v, want none from a merge", port.deleteCalls)
			}
		}
	})

	t.Run("refuses to run without a port", func(t *testing.T) {
		if _, err := (Service{}).Merge(context.Background(), baseRequest()); err == nil {
			t.Fatal("Merge() error = nil, want a configuration error")
		}
	})
}

// TestMergeKeepsPreSendFailuresRetryable pins the one thing a hold must never
// swallow: a failure that never reached GitHub. Probing after one of these runs
// through the same broken gh, and the resulting Unknown would hold a pull
// request that stays OPEN — which no poll ever settles.
func TestMergeKeepsPreSendFailuresRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "credentials refused", err: errors.New("gh: authentication failed; run gh auth login")},
		{name: "no gh binary", err: errors.New(`exec: "gh": executable file not found`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := &fakePort{mergeErr: tt.err}
			res, err := Service{GH: port}.Merge(context.Background(), baseRequest())
			if err == nil {
				t.Fatalf("Merge() error = nil, want the send failure back")
			}
			if res.Unknown {
				t.Fatal("Merge() reported Unknown for a failure that never reached GitHub")
			}
			// One read for the live fence, none for a probe.
			if port.mergedRead != 1 {
				t.Fatalf("PRState reads = %d, want 1 (no probe)", port.mergedRead)
			}
		})
	}
}

// TestServiceDeleteBranch pins the separated cleanup. It is idempotent and
// stateless, which is the whole reason it is its own action — but it still has
// to prove the pull request actually merged before discarding its head ref.
func TestServiceDeleteBranch(t *testing.T) {
	req := DeleteRequest{Owner: "o", Repo: "r", Number: 7, Branch: "fanout/foo", HeadSha: "abc123"}

	t.Run("deletes the head ref of a merged pull request", func(t *testing.T) {
		port := &fakePort{alwaysMerged: true}
		if err := (Service{GH: port}).DeleteBranch(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		want := deleteCall{branch: "fanout/foo", expectedOID: "abc123"}
		if len(port.deleteCalls) != 1 || port.deleteCalls[0] != want {
			t.Fatalf("delete calls = %#v, want [%#v]", port.deleteCalls, want)
		}
	})

	/* snapshot は古くなりうる。未マージの head ref を消すと、どこにも無い commit
	 * を捨てることになる。 */
	/* 同じ head branch から base 違いで 2 本 PR が立つことがある。片方をマージ
	 * しても branch は終わっていないので、消すともう片方がマージ不能になる。 */
	t.Run("refuses while another open pull request still uses the branch", func(t *testing.T) {
		port := &fakePort{alwaysMerged: true, openHeads: []int{9}}
		err := Service{GH: port}.DeleteBranch(context.Background(), req)
		if !errors.Is(err, ErrBranchInUse) {
			t.Fatalf("DeleteBranch() error = %v, want ErrBranchInUse", err)
		}
		if len(port.deleteCalls) != 0 {
			t.Fatalf("delete calls = %#v, want none", port.deleteCalls)
		}
	})

	t.Run("its own still-listed number does not block the delete", func(t *testing.T) {
		port := &fakePort{alwaysMerged: true, openHeads: []int{7}}
		if err := (Service{GH: port}).DeleteBranch(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if len(port.deleteCalls) != 1 {
			t.Fatalf("delete calls = %#v, want 1", port.deleteCalls)
		}
	})

	/* rename は commit を動かさないので SHA の照合を通る。古い名前を消しにいくと
	 * 404 が「既に無い」と読まれ、消えていない branch を消したと報告してしまう。 */
	t.Run("refuses a head branch that was renamed", func(t *testing.T) {
		port := &fakePort{alwaysMerged: true, liveHeadRef: "fanout/renamed"}
		err := Service{GH: port}.DeleteBranch(context.Background(), req)
		if !errors.Is(err, ErrStaleHead) {
			t.Fatalf("DeleteBranch() error = %v, want ErrStaleHead", err)
		}
		if len(port.deleteCalls) != 0 {
			t.Fatalf("delete calls = %#v, want none", port.deleteCalls)
		}
	})

	/* fence の期待 OID を body から取ると、「クライアントが現在の tip を名乗れるか」
	 * を確かめるだけになり、マージ後に push された commit ごと branch を消せる。 */
	t.Run("fences on GitHub's head, not the one in the request", func(t *testing.T) {
		port := &fakePort{alwaysMerged: true, liveHead: "def456"}
		err := Service{GH: port}.DeleteBranch(context.Background(), req)
		if !errors.Is(err, ErrStaleHead) {
			t.Fatalf("DeleteBranch() error = %v, want ErrStaleHead", err)
		}
		if len(port.deleteCalls) != 0 {
			t.Fatalf("delete calls = %#v, want none", port.deleteCalls)
		}
	})

	t.Run("refuses when GitHub says the pull request is not merged", func(t *testing.T) {
		port := &fakePort{notMerged: true}
		err := Service{GH: port}.DeleteBranch(context.Background(), req)
		if !errors.Is(err, ErrNotMerged) {
			t.Fatalf("DeleteBranch() error = %v, want ErrNotMerged", err)
		}
		if len(port.deleteCalls) != 0 {
			t.Fatalf("delete calls = %#v, want none", port.deleteCalls)
		}
	})

	t.Run("an unreadable state never deletes", func(t *testing.T) {
		port := &fakePort{fenceErr: errors.New("gh unavailable")}
		if err := (Service{GH: port}).DeleteBranch(context.Background(), req); err == nil {
			t.Fatal("DeleteBranch() error = nil, want the read failure")
		}
		if len(port.deleteCalls) != 0 {
			t.Fatalf("delete calls = %#v, want none", port.deleteCalls)
		}
	})

	t.Run("refuses to run without a port", func(t *testing.T) {
		if err := (Service{}).DeleteBranch(context.Background(), req); err == nil {
			t.Fatal("DeleteBranch() error = nil, want a configuration error")
		}
	})
}
