//go:build !darwin && !linux

package selfupdate

import "os"

func isTerminalFile(*os.File) bool {
	return false
}
