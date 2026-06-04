package main

import (
	"fmt"

	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/ghissue"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/selfupdate"
)

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
