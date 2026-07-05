package main

import (
	"github.com/butaosuinu/fanout/internal/app/cliflags"
	"github.com/butaosuinu/fanout/internal/app/lifecycle"
	"github.com/butaosuinu/fanout/internal/core/exitcode"
	"github.com/butaosuinu/fanout/internal/infra/ghissue"
	"github.com/butaosuinu/fanout/internal/infra/hooks"
	"github.com/butaosuinu/fanout/internal/infra/log"
	"github.com/butaosuinu/fanout/internal/infra/settings"
)

func cmdClose(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	opts, code := lifecycleOptions("--close", true, lg)
	if code != exitcode.OK {
		return code
	}
	return lifecycle.Close(opts, cfg.ParentRef, cfg.CloseNum, lg)
}

func cmdMerge(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	opts, code := lifecycleOptions("--merge", true, lg)
	if code != exitcode.OK {
		return code
	}
	return lifecycle.Merge(opts, cfg.ParentRef, cfg.MergeNum, lg)
}

func cmdCleanup(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	opts, code := lifecycleOptions("--cleanup", true, lg)
	if code != exitcode.OK {
		return code
	}
	return lifecycle.Cleanup(opts, cfg.ParentRef, lg)
}

func lifecycleOptions(mode string, removeWatcherRunningLabel bool, lg *log.Logger) (lifecycle.Options, exitcode.Code) {
	rt, code := resolveStateRuntimeForMode(mode, lg)
	if code != exitcode.OK {
		return lifecycle.Options{}, code
	}
	opts := lifecycle.Options{
		ProjectRoot: rt.projectRoot,
		StatePath:   rt.statePath,
		Hooks:       hooks.LoadUserConfig(lg),
	}
	if removeWatcherRunningLabel {
		resolvedSettings := settings.Resolve(rt.projectRoot, settings.CLIOverrides{}, lg.Warn)
		gh := ghissue.Runner{Cwd: rt.projectRoot}
		opts.WatcherRunningLabel = resolvedSettings.WatcherRunningLabel
		opts.RemoveIssueLabel = gh.RemoveIssueLabel
	}
	return opts, exitcode.OK
}
