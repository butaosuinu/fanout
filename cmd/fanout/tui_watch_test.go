package main

import (
	"testing"

	"github.com/butaosuinu/fanout/internal/infra/settings"
)

func TestNewWatchLaunchConfigDisablesResolvedCodexPlanMode(t *testing.T) {
	resolved := settings.Defaults()
	resolved.CodexPlanMode = true

	cfg := newWatchLaunchConfig(resolved, 123, 2)

	if cfg.CodexPlanMode == nil || *cfg.CodexPlanMode {
		t.Fatalf("CodexPlanMode = %v, want explicit false watcher override", cfg.CodexPlanMode)
	}
}
