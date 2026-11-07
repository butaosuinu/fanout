package backend

import "fmt"

// VerifyLauncherProcess checks the OS executable as well as the runtime's argv
// and foreground group before admitting the pinned, argument-free launcher.
func VerifyLauncherProcess(info PaneProcessInfo, cwd, launcherPath string) error {
	if info.ShellPID <= 1 || info.ForegroundProcessGroup <= 1 {
		return fmt.Errorf("launcher process group is incomplete")
	}
	for _, process := range info.ForegroundProcesses {
		if process.PID == info.ShellPID && process.CWD == cwd &&
			process.ProcessGroup == info.ForegroundProcessGroup && process.Executable == launcherPath &&
			process.Argv0 == launcherPath && len(process.Argv) == 0 {
			return nil
		}
	}
	return fmt.Errorf("launcher process identity does not match the bundled fanout executable")
}
