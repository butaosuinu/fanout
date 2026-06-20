package main

import (
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/lifecycle"
	"github.com/butaosuinu/fanout/internal/log"
	"github.com/butaosuinu/fanout/internal/settings"
)

func cmdClose(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	opts, code := lifecycleOptions("--close", cfg, lg)
	if code != exitcode.OK {
		return code
	}
	return lifecycle.Close(opts, cfg.ParentRef, cfg.CloseNum, lg)
}

func cmdMerge(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	opts, code := lifecycleOptions("--merge", cfg, lg)
	if code != exitcode.OK {
		return code
	}
	return lifecycle.Merge(opts, cfg.ParentRef, cfg.MergeNum, lg)
}

func cmdCleanup(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	opts, code := lifecycleOptions("--cleanup", cfg, lg)
	if code != exitcode.OK {
		return code
	}
	return lifecycle.Cleanup(opts, cfg.ParentRef, lg)
}

func lifecycleOptions(mode string, cfg *cliflags.Config, lg *log.Logger) (lifecycle.Options, exitcode.Code) {
	rt, code := resolveStateRuntimeForMode(mode, lg)
	if code != exitcode.OK {
		return lifecycle.Options{}, code
	}
	resolved := settings.Resolve(rt.projectRoot, settings.CLIOverrides{HooksEnabled: cfg.HooksEnabled}, lg.Warn)
	return lifecycle.Options{
		ProjectRoot:  rt.projectRoot,
		StatePath:    rt.statePath,
		HooksEnabled: resolved.HooksEnabled,
	}, exitcode.OK
}
