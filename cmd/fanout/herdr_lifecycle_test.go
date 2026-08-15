package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

func TestIsHerdrLifecycleRequest(t *testing.T) {
	for _, test := range []struct {
		args []string
		want bool
	}{
		{args: []string{"herdr", "restart"}, want: true},
		{args: []string{"herdr", "shutdown"}, want: true},
		{args: []string{"restart"}},
		{args: nil},
	} {
		if got := isHerdrLifecycleRequest(test.args); got != test.want {
			t.Errorf("isHerdrLifecycleRequest(%q) = %t, want %t", test.args, got, test.want)
		}
	}
}

func TestHerdrLifecycleTimeoutLeavesFinalizationBudget(t *testing.T) {
	if herdrLifecycleTimeout <= backend.DefaultWaitTimeout {
		t.Fatalf(
			"herdr lifecycle timeout = %s, want greater than restart wait %s",
			herdrLifecycleTimeout,
			backend.DefaultWaitTimeout,
		)
	}
}

func TestRunHerdrLifecycleRequiresExplicitAction(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"bogus"}, {"restart", "shutdown"}} {
		var out, errOut bytes.Buffer
		called := false
		deps := herdrLifecycleDeps{projectRoot: func() (string, error) { called = true; return "/repo", nil }}
		code := runHerdrLifecycle(args, log.NewWith(&out, &errOut, false), deps)
		if code != exitcode.Invocation || called || !strings.Contains(errOut.String(), herdrLifecycleUsage) {
			t.Fatalf("runHerdrLifecycle(%q) = %d called=%t stderr=%q", args, code, called, errOut.String())
		}
	}
}

func TestRunHerdrLifecycleDispatchesOnlySelectedAction(t *testing.T) {
	for _, action := range []string{"restart", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			var out, errOut bytes.Buffer
			restarts, shutdowns := 0, 0
			deps := herdrLifecycleDeps{
				projectRoot: func() (string, error) { return "/repo", nil },
				repoIdentity: func(_ context.Context, root string) (worktree.RepoIdentity, error) {
					if root != "/repo" {
						return worktree.RepoIdentity{}, errors.New("wrong root")
					}
					return worktree.RepoIdentity{RepoKey: "/repo/.git", RepoRoot: root}, nil
				},
				restart: func(_ context.Context, root, repoKey string) (string, error) {
					restarts++
					assertHerdrLifecycleInputs(t, root, repoKey)
					return "fanout-owned", nil
				},
				shutdown: func(_ context.Context, root, repoKey string) error {
					shutdowns++
					assertHerdrLifecycleInputs(t, root, repoKey)
					return nil
				},
			}
			code := runHerdrLifecycle([]string{action}, log.NewWith(&out, &errOut, false), deps)
			if code != exitcode.OK || errOut.Len() != 0 {
				t.Fatalf("runHerdrLifecycle(%q) = %d stderr=%q", action, code, errOut.String())
			}
			wantRestart, wantShutdown := 0, 1
			if action == "restart" {
				wantRestart, wantShutdown = 1, 0
			}
			if restarts != wantRestart || shutdowns != wantShutdown {
				t.Fatalf("calls restart=%d shutdown=%d", restarts, shutdowns)
			}
		})
	}
}

func assertHerdrLifecycleInputs(t *testing.T, root, repoKey string) {
	t.Helper()
	if root != "/repo" || repoKey != "/repo/.git" {
		t.Fatalf("lifecycle inputs root=%q repoKey=%q", root, repoKey)
	}
}
