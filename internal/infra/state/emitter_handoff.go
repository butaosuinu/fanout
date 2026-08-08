package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/butaosuinu/fanout/internal/core/telemetry"
)

const (
	emitterHandoffPollInterval     = 10 * time.Millisecond
	emitterHandoffReacquireTimeout = 5 * time.Second
)

// EmitterHandoffPath returns one invocation-scoped marker used to hand the
// combined launch lock to emitters that arrived before final-row save.
func EmitterHandoffPath(statePath, emitterNonce, eventNonce string) (string, error) {
	if !filepath.IsAbs(statePath) || filepath.Clean(statePath) != statePath ||
		filepath.Base(statePath) != "state.json" || filepath.Base(filepath.Dir(statePath)) != ".fanout" {
		return "", fmt.Errorf("emitter handoff requires an owning state path")
	}
	if !telemetry.ValidNonce(emitterNonce) || !telemetry.ValidNonce(eventNonce) {
		return "", fmt.Errorf("emitter handoff requires a valid nonce")
	}
	name := ".emitter-" + emitterNonce + "-" + eventNonce + ".wait"
	return filepath.Join(filepath.Dir(statePath), name), nil
}

// MarkEmitterHandoff publishes an owner-only waiter before the emitter tries
// the combined launch lock.
func MarkEmitterHandoff(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("mark emitter lock handoff: %w", err)
	}
	err = errors.Join(file.Sync(), file.Close())
	if err != nil {
		// A failed publication must not leave a waiter that can trigger a later lock handoff.
		_ = os.Remove(path)
	}
	return err
}

// ClearEmitterHandoff removes one launch-scoped waiter.
func ClearEmitterHandoff(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear emitter lock handoff: %w", err)
	}
	return nil
}

// EmitterHandoffWaiting validates and reports one published waiter.
func EmitterHandoffWaiting(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || strings.ContainsRune(path, '\x00') {
		return false, fmt.Errorf("emitter lock handoff is not an owner-only regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return false, fmt.Errorf("emitter lock handoff belongs to another user")
	}
	return true, nil
}

// EmitterHandoffs returns every validated waiter for one launch generation.
func EmitterHandoffs(statePath, emitterNonce string) ([]string, error) {
	probe, err := EmitterHandoffPath(statePath, emitterNonce, strings.Repeat("0", 32))
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(probe)
	prefix := ".emitter-" + emitterNonce + "-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".wait") {
			continue
		}
		eventNonce := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".wait")
		if !telemetry.ValidNonce(eventNonce) {
			return nil, fmt.Errorf("emitter lock handoff has an invalid event nonce")
		}
		path := filepath.Join(dir, name)
		waiting, waitErr := EmitterHandoffWaiting(path)
		if waitErr != nil {
			return nil, waitErr
		}
		if waiting {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// YieldForEmitter releases the combined lock only after a matching realized
// launch has published its waiter, then reacquires and reloads both stores.
// The persisted intent remains the idempotency fence during this handoff. If
// bounded reacquisition fails, l remains unlocked and the caller must stop.
func (l *LockedStore) YieldForEmitter(
	ctx context.Context,
	projectRoot string,
	statePath string,
	emitterNonce string,
) (bool, error) {
	if ctx == nil {
		return false, fmt.Errorf("emitter handoff requires a context")
	}
	handoffs, err := EmitterHandoffs(statePath, emitterNonce)
	if err != nil || len(handoffs) == 0 {
		return len(handoffs) == 0, err
	}
	if l == nil || l.file == nil || l.herdrIntentsFile == nil {
		return false, fmt.Errorf("emitter handoff requires the combined launch lock")
	}
	if err := l.Unlock(); err != nil {
		return false, err
	}
	completed, waitErr := waitForEmitterHandoff(ctx, statePath, emitterNonce)
	// The marker wait may consume ctx. Reacquisition restores the caller's lock
	// invariant under its own deadline instead of falling back to blocking flock.
	reacquireCtx, cancel := context.WithTimeout(context.Background(), emitterHandoffReacquireTimeout)
	defer cancel()
	reloaded, err := LockProjectForLaunchContext(reacquireCtx, projectRoot)
	if err != nil {
		return false, fmt.Errorf("reacquire launch lock after emitter handoff: %w", err)
	}
	*l = *reloaded
	return completed, waitErr
}

func waitForEmitterHandoff(ctx context.Context, statePath, emitterNonce string) (bool, error) {
	ticker := time.NewTicker(emitterHandoffPollInterval)
	defer ticker.Stop()
	for {
		handoffs, err := EmitterHandoffs(statePath, emitterNonce)
		if err != nil || len(handoffs) == 0 {
			return err == nil, err
		}
		select {
		case <-ctx.Done():
			return false, nil
		case <-ticker.C:
		}
	}
}
