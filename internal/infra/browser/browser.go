// Package browser opens URLs in the OS default browser.
package browser

import (
	"os/exec"
	"runtime"
	"time"
)

// Open launches the platform opener (open / rundll32 / xdg-open) for url and
// waits up to wait for it to exit. Openers that outlive the wait (some
// xdg-open implementations block for the browser's lifetime) are treated as
// success so the caller never hangs.
func Open(url string, wait time.Duration) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(wait):
		return nil
	}
}
