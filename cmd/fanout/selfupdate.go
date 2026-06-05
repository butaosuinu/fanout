package main

import (
	"fmt"
	"os"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/selfupdate"
)

func cmdSelfUpdate(args []string, current string, gh ghissue.Runner, lg *log.Logger) exitcode.Code {
	req, err := selfupdate.ParseArgs(args)
	if err != nil {
		lg.Err("self-update: %s", err.Error())
		if kind, ok := selfupdate.Kind(err); ok && kind == selfupdate.FailureInvocation {
			fmt.Fprint(lg.Stderr(), selfupdate.UsageText)
			return exitcode.Invocation
		}
		return exitcode.Env
	}
	if req.Help {
		fmt.Fprint(lg.Stdout(), selfupdate.UsageText)
		return exitcode.OK
	}

	_, err = selfupdate.Run(selfupdate.Options{
		CurrentVersion: current,
		Request:        req,
		LatestTag:      gh.LatestReleaseTag,
		Runner: selfupdate.ExecRunner{
			Stdin:  os.Stdin,
			Stdout: lg.Stdout(),
			Stderr: lg.Stderr(),
		},
		Stdout: lg.Stdout(),
		Stderr: lg.Stderr(),
		Stdin:  os.Stdin,
	})
	if err == nil {
		return exitcode.OK
	}

	lg.Err("self-update: %s", err.Error())
	if kind, ok := selfupdate.Kind(err); ok {
		switch kind {
		case selfupdate.FailureInvocation:
			return exitcode.Invocation
		case selfupdate.FailureGitHub:
			return exitcode.GitHub
		default:
			return exitcode.Env
		}
	}
	return exitcode.Env
}

func cmdCheckUpdate(current string, gh ghissue.Runner, lg *log.Logger) exitcode.Code {
	if selfupdate.Compare(current, "") == selfupdate.DevBuild {
		fmt.Fprintln(lg.Stdout(), "fanout dev build: --check-update only works for released versions")
		return exitcode.OK
	}

	latest, err := gh.LatestReleaseTag()
	if err != nil {
		lg.Err("--check-update: failed to fetch latest release tag: %v", err)
		return exitcode.GitHub
	}

	switch selfupdate.Compare(current, latest) {
	case selfupdate.UpdateAvailable:
		fmt.Fprintf(lg.Stdout(), "fanout update available: %s -> %s\n", current, latest)
		return exitcode.OK
	case selfupdate.UpToDate:
		fmt.Fprintf(lg.Stdout(), "fanout is up to date: %s\n", current)
		return exitcode.OK
	case selfupdate.CurrentAhead:
		fmt.Fprintf(lg.Stdout(), "fanout %s is newer than latest release %s\n", current, latest)
		return exitcode.OK
	default:
		lg.Err("--check-update: cannot compare current version %q with latest release %q", current, latest)
		return exitcode.Invocation
	}
}
