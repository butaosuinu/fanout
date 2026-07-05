package peermsg

import (
	"errors"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/team"
)

func TestResolveMsgIdentity(t *testing.T) {
	detected := team.Identity{
		Issue:  70,
		Parent: "68",
		Pane: state.Pane{
			IssueNum: 70, Parent: "68", PaneID: "%1", Slug: "msg-cli-surface-70",
			Agent: "claude", DisplayName: "msg cli", WorktreePath: "/tmp/wt",
		},
	}
	for _, tc := range []struct {
		name       string
		req        Request
		detect     func() (team.Identity, error)
		code       exitcode.Code
		wantSelf   int
		wantParent string
		wantPane   string // expected pane_id, "" when identity is explicit
	}{
		{
			name: "both explicit skips detection",
			req:  Request{Verb: "send", Self: 70, Parent: "68"},
			detect: func() (team.Identity, error) {
				t.Error("detect called despite explicit --self/--parent")
				return team.Identity{}, nil
			},
			code: exitcode.OK, wantSelf: 70, wantParent: "68",
		},
		{
			name:   "detection fills both",
			req:    Request{Verb: "inbox"},
			detect: func() (team.Identity, error) { return detected, nil },
			code:   exitcode.OK, wantSelf: 70, wantParent: "68", wantPane: "%1",
		},
		{
			name:   "explicit self keeps detected parent",
			req:    Request{Verb: "send", Self: 99},
			detect: func() (team.Identity, error) { return detected, nil },
			code:   exitcode.OK, wantSelf: 99, wantParent: "68",
		},
		{
			name:   "explicit parent keeps detected self",
			req:    Request{Verb: "send", Parent: "77"},
			detect: func() (team.Identity, error) { return detected, nil },
			code:   exitcode.OK, wantSelf: 70, wantParent: "77", wantPane: "%1",
		},
		{
			name: "manual pane negative synthetic issue is accepted",
			req:  Request{Verb: "inbox"},
			detect: func() (team.Identity, error) {
				return team.Identity{
					Issue:  -1,
					Parent: "@manual",
					Pane:   state.Pane{IssueNum: -1, Parent: "@manual", PaneID: "%9"},
				}, nil
			},
			code: exitcode.OK, wantSelf: -1, wantParent: "@manual", wantPane: "%9",
		},
		{
			name:   "detection failure",
			req:    Request{Verb: "send"},
			detect: func() (team.Identity, error) { return team.Identity{}, team.ErrPaneNotFound },
			code:   exitcode.Invocation,
		},
		{
			name:   "peers needs only parent",
			req:    Request{Verb: "peers", Parent: "68"},
			detect: func() (team.Identity, error) { return team.Identity{}, errors.New("must not be called") },
			code:   exitcode.OK, wantSelf: 0, wantParent: "68",
		},
		{
			name:   "nudge needs only parent",
			req:    Request{Verb: "nudge", To: 71, Parent: "68"},
			detect: func() (team.Identity, error) { return team.Identity{}, errors.New("must not be called") },
			code:   exitcode.OK, wantSelf: 0, wantParent: "68",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := Deps{DetectIdentity: tc.detect}

			self, parent, pane, code := resolveMsgIdentity(&tc.req, deps, msgTestLogger())
			if code != tc.code {
				t.Fatalf("code = %d, want %d", code, tc.code)
			}
			if code != exitcode.OK {
				return
			}
			if self != tc.wantSelf || parent != tc.wantParent {
				t.Errorf("self, parent = %d, %q, want %d, %q", self, parent, tc.wantSelf, tc.wantParent)
			}
			if pane.PaneID != tc.wantPane {
				t.Errorf("pane.PaneID = %q, want %q", pane.PaneID, tc.wantPane)
			}
		})
	}
}

// TestResolveMemberNum covers the seam that decides whether a token is an issue
// number or a plan task id once the parent is known — in particular that an
// all-digit token routes to a task under a plan parent (not the issue number).
func TestResolveMemberNum(t *testing.T) {
	var buf strings.Builder
	lg := log.NewWith(&buf, &buf, false)
	const planParent = "plan:demo"

	n, code := resolveMemberNum("send", "--to", "123", 123, planParent, lg)
	if code != exitcode.OK {
		t.Fatalf("plan all-digit code = %d, want OK", code)
	}
	if n == 123 || n >= 0 {
		t.Errorf("plan all-digit resolved to %d, want a negative task number (not the issue number 123)", n)
	}
	if again, _ := resolveMemberNum("send", "--to", "123", 123, planParent, lg); again != n {
		t.Errorf("resolveMemberNum not deterministic: %d != %d", again, n)
	}

	if _, code := resolveMemberNum("send", "--to", "api-client", 0, "68", lg); code != exitcode.Invocation {
		t.Error("a task id under an issue parent must be rejected")
	}
	if got, code := resolveMemberNum("send", "--to", "71", 71, "68", lg); code != exitcode.OK || got != 71 {
		t.Errorf("numeric under issue = (%d, %d), want (71, OK)", got, code)
	}
	if got, code := resolveMemberNum("send", "--to", "-1", -1, "68", lg); code != exitcode.OK || got != -1 {
		t.Errorf("manual negative under issue = (%d, %d), want (-1, OK)", got, code)
	}
	if _, code := resolveMemberNum("send", "--to", "-1", -1, planParent, lg); code != exitcode.Invocation {
		t.Error("a non-task token under a plan parent must be rejected")
	}
}
