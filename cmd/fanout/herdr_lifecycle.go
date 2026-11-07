package main

import (
	"context"
	"fmt"
	"time"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/app/sessionbinding"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/gitroot"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/paneruntime"
	"github.com/butaosuinu/fanout/internal/infra/state"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const herdrLifecycleTimeout = backend.DefaultWaitTimeout + time.Minute

type herdrLifecycleDeps struct {
	projectRoot     func() (string, error)
	repoIdentity    func(context.Context, string) (worktree.RepoIdentity, error)
	refreshSessions func(string) error
	restart         func(ctx context.Context, projectRoot, repoKey string) (string, error)
	shutdown        func(ctx context.Context, projectRoot, repoKey string) error
}

// newManagedServerIO adapts the runtime's owned-server lifecycle calls to the
// app port. Construction stays behind paneruntime; the app only drives the
// journal-fenced transaction around these calls.
func newManagedServerIO(repoKey string) panelaunch.ManagedServerIO {
	ops := paneruntime.NewServerOps(repoKey)
	return panelaunch.ManagedServerIO{
		InspectServer:      ops.Inspect,
		ObserveWorkspaces:  ops.Observe,
		DiscardEnvironment: ops.DiscardEnvironment,
		RestartServer: func(
			ctx context.Context,
			identity state.RuntimeServerIdentity,
		) (panelaunch.ManagedRestartedServer, error) {
			restarted, err := ops.Restart(ctx, identity)
			if err != nil {
				return panelaunch.ManagedRestartedServer{}, err
			}
			return panelaunch.ManagedRestartedServer{Runtime: restarted, Session: restarted.Session}, nil
		},
		ShutdownServer: ops.Shutdown,
	}
}

func isHerdrLifecycleRequest(args []string) bool {
	return len(args) > 0 && args[0] == "herdr"
}

func cmdHerdrLifecycle(args []string, lg *log.Logger) exitcode.Code {
	deps := herdrLifecycleDeps{
		projectRoot:  func() (string, error) { return gitroot.Toplevel("") },
		repoIdentity: worktree.ResolveRepoIdentity,
		refreshSessions: func(root string) error {
			_, err := sessionbinding.StateLoader(root, runtimeListLiveForProject(root, false))()
			return err
		},
		restart: func(ctx context.Context, root, repoKey string) (string, error) {
			return panelaunch.RestartManagedServer(ctx, root, newManagedServerIO(repoKey))
		},
		shutdown: func(ctx context.Context, root, repoKey string) error {
			return panelaunch.ShutdownManagedServer(ctx, root, newManagedServerIO(repoKey))
		},
	}
	return runHerdrLifecycle(args, lg, deps)
}

func runHerdrLifecycle(args []string, lg *log.Logger, deps herdrLifecycleDeps) exitcode.Code {
	action, code := parseHerdrLifecycle(args, lg)
	if code != exitcode.OK || action == "" {
		return code
	}
	root, err := deps.projectRoot()
	if err != nil {
		lg.Err("herdr %s: %v", action, err)
		return exitcode.Env
	}
	ctx, cancel := context.WithTimeout(context.Background(), herdrLifecycleTimeout)
	defer cancel()
	identity, err := deps.repoIdentity(ctx, root)
	if err != nil {
		lg.Err("herdr %s: %v", action, err)
		return exitcode.Env
	}
	return executeHerdrLifecycle(ctx, root, action, identity.RepoKey, lg, deps)
}

func parseHerdrLifecycle(args []string, lg *log.Logger) (string, exitcode.Code) {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		fmt.Fprint(lg.Stdout(), herdrLifecycleUsage)
		return "", exitcode.OK
	}
	if len(args) != 1 || args[0] != "restart" && args[0] != "shutdown" {
		lg.Err("herdr: expected restart or shutdown")
		fmt.Fprint(lg.Stderr(), herdrLifecycleUsage)
		return "", exitcode.Invocation
	}
	return args[0], exitcode.OK
}

func executeHerdrLifecycle(
	ctx context.Context,
	root string,
	action string,
	repoKey string,
	lg *log.Logger,
	deps herdrLifecycleDeps,
) exitcode.Code {
	if action == "restart" {
		return executeServerRestart(ctx, root, repoKey, lg, deps.refreshSessions, deps.restart)
	}
	if err := deps.shutdown(ctx, root, repoKey); err != nil {
		lg.Err("herdr shutdown: %v", err)
		return exitcode.Env
	}
	lg.Ok("Herdr owned server shut down")
	return exitcode.OK
}

func executeServerRestart(
	ctx context.Context,
	root string,
	repoKey string,
	lg *log.Logger,
	refreshSessions func(string) error,
	restart func(context.Context, string, string) (string, error),
) exitcode.Code {
	if err := refreshSessions(root); err != nil {
		lg.Err("herdr restart: refresh agent sessions: %v", err)
		return exitcode.Env
	}
	session, err := restart(ctx, root, repoKey)
	if err != nil {
		lg.Err("herdr restart: %v", err)
		return exitcode.Env
	}
	lg.Ok("Herdr owned server restarted: %s", session)
	return exitcode.OK
}

const herdrLifecycleUsage = `Usage: fanout herdr <restart|shutdown>

  restart   Restart the repository-owned Herdr server after its saved
            supervisor, server process, and sockets are proven absent.
  shutdown  Stop an empty repository-owned Herdr server.
`
