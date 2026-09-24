package herdrrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/butaosuinu/fanout/internal/infra/state"
)

type ownedAdmission struct {
	marker     ownerMarker
	markerPath string
	lockPath   string
}

//nolint:funlen // Revalidation deliberately keeps the ownership lock through every identity check.
func (b *Backend) acquireOwnedOperation(ctx context.Context) (ownedAdmission, *os.File, error) {
	if b == nil || b.owner == nil {
		return ownedAdmission{}, nil, fmt.Errorf("herdr mutation requires a fanout-owned session")
	}
	lock, err := lockExistingPrivateFileContext(ctx, b.owner.lockPath)
	if err != nil {
		return ownedAdmission{}, nil, err
	}
	admission := *b.owner
	marker, found, err := readOwnerMarker(admission.markerPath)
	if err != nil || !found || marker != admission.marker {
		unlockPrivateFile(lock)
		if err != nil {
			return ownedAdmission{}, nil, err
		}
		return ownedAdmission{}, nil, fmt.Errorf("herdr ownership marker changed")
	}
	commonDir, commonIdentity, err := openCanonicalGitCommonDir(marker.GitCommonDir)
	if err == nil && commonDir != marker.GitCommonDir {
		err = fmt.Errorf("herdr ownership marker git common directory is not canonical")
	}
	layout := ownedLayout{}
	if err == nil {
		layout, err = prepareOwnedLayout(filepath.Dir(marker.RuntimeDir), marker.Session)
	}
	if err == nil {
		admitted := binaryAdmission{
			path: marker.BinaryPath, sha256: marker.BinarySHA256, version: marker.BinaryVersion,
		}
		launcher := binaryAdmission{path: marker.LauncherPath, sha256: marker.LauncherSHA256}
		err = validateOwnedMarker(marker, layout, commonDir, commonIdentity, admitted, launcher)
	}
	if err == nil {
		err = verifyLiveSupervisor(layout.supervisorLock, marker)
	}
	if err == nil {
		err = validatePrivateSocket(marker.SocketPath)
	}
	if err == nil {
		err = validatePrivateSocket(marker.ClientSocketPath)
	}
	if err != nil {
		unlockPrivateFile(lock)
		return ownedAdmission{}, nil, err
	}
	return admission, lock, nil
}

func (b *Backend) acquireOwnedMutation(ctx context.Context) (ownedAdmission, *os.File, error) {
	if b != nil && b.owner != nil {
		if err := rejectOwnedServerLifecycle(b.owner.marker.GitCommonDir); err != nil {
			return ownedAdmission{}, nil, err
		}
	}
	admission, lock, err := b.acquireOwnedOperation(ctx)
	if err != nil {
		return ownedAdmission{}, nil, err
	}
	if err := rejectOwnedServerLifecycle(admission.marker.GitCommonDir); err != nil {
		unlockPrivateFile(lock)
		return ownedAdmission{}, nil, err
	}
	return admission, lock, nil
}

func rejectOwnedServerLifecycle(gitCommonDir string) error {
	path := filepath.Join(gitCommonDir, "fanout", "herdr-intents.json")
	journal, err := state.LoadLaunchJournalPath(path)
	if err != nil {
		return fmt.Errorf("load Herdr server lifecycle fence: %w", err)
	}
	intent, found, err := journal.ServerLifecycleIntent()
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	action := "restart"
	if intent.Kind == state.IntentShutdown {
		action = "shutdown"
	}
	return fmt.Errorf("herdr owned server %s is pending; only %s and read-only operations are allowed", action, action)
}

func (b *Backend) probeOwned(ctx context.Context, admission ownedAdmission) (probeResult, error) {
	if b.session != admission.marker.Session || b.socketPath != admission.marker.SocketPath {
		return probeResult{}, fmt.Errorf("herdr backend route does not match owned admission")
	}
	probed, err := b.probeContext(ctx)
	if err != nil {
		return probeResult{}, err
	}
	if probed.binary != admission.marker.BinaryPath || probed.sha256 != admission.marker.BinarySHA256 ||
		probed.version != admission.marker.BinaryVersion {
		return probeResult{}, fmt.Errorf("herdr binary identity changed after owned admission")
	}
	return probed, nil
}

// ownedLane selects which admission a withOwned call takes: operation for
// observations and launch reads, mutation when the server lifecycle fence
// must also be clear.
type ownedLane int

const (
	ownedOperationLane ownedLane = iota
	ownedMutationLane
)

// ownedCall is one admitted and probed owned route, valid only inside the
// withOwned callback that holds the ownership lock.
type ownedCall struct {
	b         *Backend
	admission ownedAdmission
	probed    probeResult
}

// ownedErrors carries the caller's wrapping for admission failures. The
// helper never classifies errors itself; a nil field returns the error as-is.
type ownedErrors struct {
	acquire func(error) error
	probe   func(error) error
}

func sameOwnedErrors(wrap func(error) error) ownedErrors {
	return ownedErrors{acquire: wrap, probe: wrap}
}

func wrapOwnedError(wrap func(error) error, err error) error {
	if wrap == nil {
		return err
	}
	return wrap(err)
}

// withOwned acquires the lane's admission, holds the ownership lock, probes
// the exact route, and runs fn. fn's error is returned unchanged.
func (b *Backend) withOwned(ctx context.Context, lane ownedLane, wrap ownedErrors, fn func(ownedCall) error) error {
	acquire := b.acquireOwnedOperation
	if lane == ownedMutationLane {
		acquire = b.acquireOwnedMutation
	}
	admission, lock, err := acquire(ctx)
	if err != nil {
		return wrapOwnedError(wrap.acquire, err)
	}
	defer unlockPrivateFile(lock)
	probed, err := b.probeOwned(ctx, admission)
	if err != nil {
		return wrapOwnedError(wrap.probe, err)
	}
	return fn(ownedCall{b: b, admission: admission, probed: probed})
}
