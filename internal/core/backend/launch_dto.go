package backend

// Launch-side data exchanged between orchestration and the herdr adapter: the
// per-session launch route, the observed pane process tree, and the terminal
// outcome of a bounded snapshot wait. The adapter that fills these values lives
// in infra; this file is data only.

type PaneProcess struct {
	PID          int      `json:"pid"`
	ParentPID    int      `json:"-"`
	ProcessGroup int      `json:"-"`
	Executable   string   `json:"-"`
	Name         string   `json:"name"`
	Argv         []string `json:"argv"`
	Argv0        string   `json:"argv0"`
	Cmdline      string   `json:"cmdline"`
	CWD          string   `json:"cwd"`
}

type PaneProcessInfo struct {
	PaneID                 string        `json:"pane_id"`
	ShellPID               int           `json:"shell_pid"`
	ForegroundProcessGroup int           `json:"foreground_process_group_id"`
	ForegroundProcesses    []PaneProcess `json:"foreground_processes"`
}

type OwnedLaunchRoute struct {
	GitCommonDir string
	RuntimeDir   string
	Session      string
	SocketPath   string
	LauncherPath string
	EmitterPath  string
	ControlPath  string
}

// WaitStatus is the terminal outcome of a bounded snapshot wait.
type WaitStatus string

const (
	WaitMatched   WaitStatus = "matched"
	WaitTimedOut  WaitStatus = "timed_out"
	WaitCancelled WaitStatus = "cancelled" //nolint:misspell // The published terminal-result contract uses this spelling.
	WaitFailed    WaitStatus = "failed"
)

// WaitResult reports one of the four terminal wait outcomes. Panes contains
// the last compatible snapshot only for matched and timed-out results.
type WaitResult struct {
	Status WaitStatus
	Panes  []LivePane
	Err    error
}
