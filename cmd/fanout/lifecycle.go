package main

import (
	"github.com/butaosuinu/fanout/internal/cliflags"
	"github.com/butaosuinu/fanout/internal/exitcode"
	"github.com/butaosuinu/fanout/internal/hooks"
	"github.com/butaosuinu/fanout/internal/lifecycle"
	"github.com/butaosuinu/fanout/internal/log"
)

func cmdClose(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	opts, code := lifecycleOptions("--close", lg)
	if code != exitcode.OK {
		return code
	}
	return lifecycle.Close(opts, cfg.ParentRef, cfg.CloseNum, lg)
}

func cmdMerge(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	opts, code := lifecycleOptions("--merge", lg)
	if code != exitcode.OK {
		return code
	}
	return lifecycle.Merge(opts, cfg.ParentRef, cfg.MergeNum, lg)
}

func cmdCleanup(cfg *cliflags.Config, lg *log.Logger) exitcode.Code {
	opts, code := lifecycleOptions("--cleanup", lg)
	if code != exitcode.OK {
		return code
	}
	return lifecycle.Cleanup(opts, cfg.ParentRef, lg)
}

func lifecycleOptions(mode string, lg *log.Logger) (lifecycle.Options, exitcode.Code) {
	rt, code := resolveStateRuntimeForMode(mode, lg)
	if code != exitcode.OK {
		return lifecycle.Options{}, code
	}
	return lifecycle.Options{
		ProjectRoot: rt.projectRoot,
		StatePath:   rt.statePath,
		Hooks:       hooks.LoadUserConfig(lg),
	}, exitcode.OK
}
