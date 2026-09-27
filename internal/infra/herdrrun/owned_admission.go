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
