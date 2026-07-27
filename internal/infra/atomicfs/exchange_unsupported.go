//go:build !darwin && !linux

package atomicfs

import "fmt"

func exchangeFiles(_, _ string) error {
	return fmt.Errorf("atomic file exchange is unsupported on this platform")
}
