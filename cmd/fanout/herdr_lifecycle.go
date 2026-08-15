package main

import (
	"context"
	"fmt"
	"time"

	"github.com/butaosuinu/fanout/internal/app/panelaunch"
	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/gitroot"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/worktree"
)

const herdrLifecycleTimeout = backend.DefaultWaitTimeout + time.Minute

type herdrLifecycleDeps struct {
	projectRoot  func() (string, error)
	repoIdentity func(context.Context, string) (worktree.RepoIdentity, error)
	restart      func(context.Context, string, herdrrun.OwnedOptions) (*herdrrun.OwnedSession, error)
	shutdown     func(context.Context, string, herdrrun.OwnedOptions) error
}

func isHerdrLifecycleRequest(args []string) bool {
	return len(args) > 0 && args[0] == "herdr"
}

func cmdHerdrLifecycle(args []string, lg *log.Logger) exitcode.Code {
	deps := herdrLifecycleDeps{
		projectRoot:  func() (string, error) { return gitroot.Toplevel("") },
		repoIdentity: worktree.ResolveRepoIdentity,
		restart:      panelaunch.RestartHerdrServer,
		shutdown:     panelaunch.ShutdownHerdrServer,
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
	opts := herdrrun.OwnedOptions{GitCommonDir: identity.RepoKey}
	return executeHerdrLifecycle(ctx, root, action, opts, lg, deps)
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
	opts herdrrun.OwnedOptions,
	lg *log.Logger,
	deps herdrLifecycleDeps,
) exitcode.Code {
	if action == "restart" {
		session, err := deps.restart(ctx, root, opts)
		if err != nil {
			lg.Err("herdr restart: %v", err)
			return exitcode.Env
		}
		lg.Ok("Herdr owned server restarted: %s", session.Session)
		return exitcode.OK
	}
	if err := deps.shutdown(ctx, root, opts); err != nil {
		lg.Err("herdr shutdown: %v", err)
		return exitcode.Env
	}
	lg.Ok("Herdr owned server shut down")
	return exitcode.OK
}

const herdrLifecycleUsage = `Usage: fanout herdr <restart|shutdown>

  restart   Restart the repository-owned Herdr server after its saved
            supervisor, server process, and sockets are proven absent.
  shutdown  Stop an empty repository-owned Herdr server.
`
