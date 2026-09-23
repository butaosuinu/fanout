package herdrrun

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/butaosuinu/fanout/internal/core/naming"
)

func TestNormalizeStatDeviceSupportsDarwinAndLinuxWidths(t *testing.T) {
	if got := normalizeStatDevice(int32(42)); got != 42 {
		t.Fatalf("normalizeStatDevice(int32) = %d, want 42", got)
	}
	if got := normalizeStatDevice(uint64(81)); got != 81 {
		t.Fatalf("normalizeStatDevice(uint64) = %d, want 81", got)
	}
}

func TestPrepareOwnedLayoutUsesShortDefaultWithLongTMPDIR(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join("/private/var/folders", strings.Repeat("long-segment", 20)))
	session := strings.Repeat("s", naming.MaxManagedSessionNameLength)
	layout, err := prepareOwnedLayout("", session)
	if err != nil {
		t.Fatal(err)
	}
	runtimeParent, err := filepath.EvalSymlinks(defaultRuntimeParent)
	if err != nil {
		t.Fatal(err)
	}
	wantBase := filepath.Join(runtimeParent, "fhr-"+strconv.Itoa(os.Getuid()))
	if layout.runtimeBase != wantBase {
		t.Fatalf("runtime base = %q, want %q", layout.runtimeBase, wantBase)
	}
	for _, path := range []string{layout.socketPath, layout.clientSocketPath} {
		if len(path) > maxUnixSocketPathBytes {
			t.Fatalf("default socket path is %d bytes, want at most %d: %s", len(path), maxUnixSocketPathBytes, path)
		}
	}
}

func TestPhysicalRepositoryAliasesShareOwnedSessionName(t *testing.T) {
	root := t.TempDir()
	commonDir := filepath.Join(root, "repo.git")
	if err := os.Mkdir(commonDir, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "repository-alias")
	if err := os.Symlink(commonDir, alias); err != nil {
		t.Fatal(err)
	}
	_, directIdentity, err := openCanonicalGitCommonDir(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	_, aliasIdentity, err := openCanonicalGitCommonDir(alias)
	if err != nil {
		t.Fatal(err)
	}
	direct := naming.ManagedSessionName(directIdentity.device, directIdentity.inode)
	aliased := naming.ManagedSessionName(aliasIdentity.device, aliasIdentity.inode)
	if direct != aliased {
		t.Fatalf("same repository aliases selected %q and %q", direct, aliased)
	}
}

func TestStageExecutablePinsOpenedBytesBeforeCommands(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(root, "stage")
	if err := os.Mkdir(stageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "herdr")
	original := []byte("#!/bin/sh\nprintf 'original\\n'\n")
	if err := os.WriteFile(source, original, 0o700); err != nil {
		t.Fatal(err)
	}
	pinned, digest, err := stageExecutable(source, stageDir)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(original)
	if digest != hex.EncodeToString(wantHash[:]) || pinned == source {
		t.Fatalf("stageExecutable() = %q, %q", pinned, digest)
	}
	err = os.WriteFile(source, []byte("#!/bin/sh\nprintf 'replacement\\n'\n"), 0o700)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(pinned).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "original\n" {
		t.Fatalf("pinned executable output = %q", out)
	}
	info, err := os.Lstat(pinned)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode().Perm() != 0o500 || stat.Nlink != 1 {
		t.Fatalf("pinned executable identity = mode %v, stat %#v", info.Mode(), info.Sys())
	}
}

func TestValidatePublishedPinnedBinaryWaitsForConcurrentLinkToSettle(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("#!/bin/sh\nexit 0\n")
	hash := sha256.Sum256(content)
	digest := hex.EncodeToString(hash[:])
	target := filepath.Join(root, "herdr-"+digest)
	if err := os.WriteFile(target, content, 0o500); err != nil {
		t.Fatal(err)
	}
	temporary := filepath.Join(root, ".herdr-stage-concurrent.tmp")
	if err := os.Link(target, temporary); err != nil {
		t.Fatal(err)
	}

	waits := 0
	err := validatePublishedPinnedBinaryWithWait(target, digest, root, func(delay time.Duration) {
		waits++
		if delay != concurrentStageValidationRetryDelay {
			t.Errorf("retry delay = %v, want %v", delay, concurrentStageValidationRetryDelay)
		}
		if err := os.Remove(temporary); err != nil {
			t.Errorf("remove concurrent stage link: %v", err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if waits != 1 {
		t.Fatalf("retry waits = %d, want 1", waits)
	}
}

func TestValidatePublishedPinnedBinaryRejectsPersistentExtraLink(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("#!/bin/sh\nexit 0\n")
	hash := sha256.Sum256(content)
	digest := hex.EncodeToString(hash[:])
	target := filepath.Join(root, "herdr-"+digest)
	if err := os.WriteFile(target, content, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(target, filepath.Join(root, ".herdr-stage-stale.tmp")); err != nil {
		t.Fatal(err)
	}

	waits := 0
	err := validatePublishedPinnedBinaryWithWait(target, digest, root, func(time.Duration) {
		waits++
	})
	if !errors.Is(err, errPinnedBinaryPhysicalIdentity) {
		t.Fatalf("persistent extra link error = %v", err)
	}
	if waits != concurrentStageValidationAttempts-1 {
		t.Fatalf("retry waits = %d, want %d", waits, concurrentStageValidationAttempts-1)
	}
}

func TestAdmissionSourceOwnerPolicy(t *testing.T) {
	tests := []struct {
		name       string
		ownerUID   int
		currentUID int
		mode       os.FileMode
		want       bool
	}{
		{name: "current user", ownerUID: 501, currentUID: 501, mode: 0o700, want: true},
		{name: "current user group writable", ownerUID: 501, currentUID: 501, mode: 0o770, want: false},
		{name: "current user world writable", ownerUID: 501, currentUID: 501, mode: 0o707, want: false},
		{name: "root installed", ownerUID: 0, currentUID: 501, mode: 0o755, want: true},
		{name: "root group writable", ownerUID: 0, currentUID: 501, mode: 0o775, want: false},
		{name: "other user", ownerUID: 502, currentUID: 501, mode: 0o755, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTrustedAdmissionSourceOwner(tt.ownerUID, tt.currentUID, tt.mode)
			if got != tt.want {
				t.Fatalf("admission source policy = %t, want %t", got, tt.want)
			}
		})
	}
}
