package herdrrun

import (
	"os"
	"strings"
	"testing"
)

func TestOwnedOperationRejectsPinnedBinaryTampering(t *testing.T) {
	h := newOwnedHarness(t)
	target := h.target()
	marker, found, err := readOwnerMarker(h.layout.markerPath)
	if err != nil || !found {
		t.Fatalf("readOwnerMarker() = %+v, %v, %v", marker, found, err)
	}
	if marker.BinaryPath == h.binary || !strings.HasPrefix(marker.BinaryPath, h.layout.binaryDir+string(os.PathSeparator)) {
		t.Fatalf("owned binary path = %q, want content-addressed bundle", marker.BinaryPath)
	}
	if err := os.Chmod(marker.BinaryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker.BinaryPath, []byte("tampered"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.session.Backend().BindOwnedTarget(target); err == nil || !strings.Contains(err.Error(), "binary identity changed") {
		t.Fatalf("BindOwnedTarget() pinned binary error = %v", err)
	}
}
