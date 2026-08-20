package paneruntime

import (
	"io"

	"github.com/butaosuinu/fanout/internal/core/backend"
	"github.com/butaosuinu/fanout/internal/infra/herdrrun"
)

// Entries publishes the hidden subcommands a runtime re-executes this binary
// for, in first-match-wins order. The composition root iterates them ahead of
// its own dispatch table: the entries are recognized by an inherited
// environment variable or by a reserved token no user-facing verb uses, so no
// ordinary invocation can reach them.
func Entries() []backend.SelfExecEntry {
	return []backend.SelfExecEntry{
		{
			Name:  "herdr-pane-launcher",
			Match: func([]string) bool { return herdrrun.IsPaneLauncherRequest() },
			Run: func(in io.Reader, out, errw io.Writer, _ []string) int {
				return herdrrun.RunPaneLauncher(in, out, errw)
			},
		},
		dashboardOpenEntry(),
		{
			Name:  "herdr-supervisor",
			Match: herdrrun.IsSupervisorRequest,
			Run: func(_ io.Reader, _ io.Writer, errw io.Writer, args []string) int {
				return herdrrun.RunSupervisor(args, errw)
			},
		},
	}
}

func dashboardOpenEntry() backend.SelfExecEntry {
	return backend.SelfExecEntry{
		Name:  "herdr-dashboard-open",
		Match: herdrrun.IsDashboardOpenRequest,
		Run: func(_ io.Reader, _ io.Writer, errw io.Writer, args []string) int {
			return herdrrun.RunDashboardOpen(args, errw)
		},
	}
}
