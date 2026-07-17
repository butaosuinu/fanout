package codexapp

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const (
	interruptShutdownGrace = 250 * time.Millisecond
	processShutdownTimeout = 2 * time.Second
)

type codexSignalError struct {
	signal os.Signal
}

func (e *codexSignalError) Error() string {
	return fmt.Sprintf("interrupted by %s", e.signal)
}

func newCodexSignalError(sig os.Signal) error {
	if sig == nil {
		return nil
	}
	return &codexSignalError{signal: sig}
}

func signalFromError(err error) os.Signal {
	var signalErr *codexSignalError
	if errors.As(err, &signalErr) {
		return signalErr.signal
	}
	return nil
}

// SignalErrorExitCode returns the conventional shell exit status for a
// signal-driven Codex controller shutdown.
func SignalErrorExitCode(err error) (int, bool) {
	sig := signalFromError(err)
	if sig == nil {
		return 0, false
	}
	return signalExitCode(sig), true
}

func installCodexControllerSignals() (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, 1)
	var stopOnce sync.Once
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	return signals, func() {
		stopOnce.Do(func() {
			signal.Stop(signals)
		})
	}
}

func firstSignalChannel(channels []<-chan os.Signal) <-chan os.Signal {
	if len(channels) == 0 {
		return nil
	}
	return channels[0]
}

type codexOperationResult[T any] struct {
	value T
	err   error
}

func waitForCodexOperation[T any](signals <-chan os.Signal, operation func() (T, error)) (T, error) {
	if signals == nil {
		return operation()
	}
	done := make(chan codexOperationResult[T], 1)
	go func() {
		value, err := operation()
		done <- codexOperationResult[T]{value: value, err: err}
	}()
	select {
	case result := <-done:
		return result.value, result.err
	case sig := <-signals:
		var zero T
		return zero, newCodexSignalError(sig)
	}
}

func connectAppServerWithSignals(server *appServer, timeout time.Duration, signals <-chan os.Signal) (*client, error) {
	if signals == nil {
		return connectAppServer(server, timeout)
	}
	done := make(chan codexOperationResult[*client], 1)
	go func() {
		connected, err := connectAppServer(server, timeout)
		done <- codexOperationResult[*client]{value: connected, err: err}
	}()
	select {
	case result := <-done:
		return result.value, result.err
	case sig := <-signals:
		// finish() closes app-server immediately after this returns. Drain the
		// bounded connector so a connection won by the race cannot escape.
		go func() {
			result := <-done
			if result.value != nil {
				result.value.Close()
			}
		}()
		return nil, newCodexSignalError(sig)
	}
}

func waitForObserverExit(done <-chan struct{}, timeout time.Duration) bool {
	if done == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func isInterruptSignal(sig os.Signal) bool {
	return sig == syscall.SIGINT
}

func requiresPaneGroupFallback(sig os.Signal) bool {
	return sig == syscall.SIGHUP || sig == syscall.SIGTERM
}

func forceCurrentPaneProcessGroup() {
	processGroup := syscall.Getpgrp()
	if processGroup <= 0 {
		return
	}
	// This is deliberately the final shutdown operation. The controller,
	// launch wrapper, and remote TUI share the pane's foreground process group.
	_ = syscall.Kill(-processGroup, syscall.SIGKILL)
}
