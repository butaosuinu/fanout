// Package herdrrun implements fanout's herdr runtime backend.
package herdrrun

import (
	"time"
)

const (
	commandName         = "herdr"
	commandTimeout      = 5 * time.Second
	commandCleanupDelay = 100 * time.Millisecond
	readRetryCount      = 1
	readRetryDelay      = 100 * time.Millisecond
	minimumWaitTimeout  = 3 * time.Second
	waitInterval        = 2 * time.Second

	sessionEnv = "HERDR_SESSION"
	socketEnv  = "HERDR_SOCKET_PATH"
)
