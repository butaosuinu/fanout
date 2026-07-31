package dashboard

import (
	"testing"
	"time"
)

func TestBroadcastCoalescesStaleFrameForSlowSubscriber(t *testing.T) {
	h := newHub()
	ch := h.subscribe()

	// The subscriber never reads. The first frame fills the cap-1 buffer.
	h.broadcast([]byte("frame-1"))
	// The second broadcast finds the buffer full. It must coalesce — discard the
	// stale queued frame and enqueue the latest — rather than drop the newest one.
	// Broadcasts only fire on a content change, so dropping the newest frame would
	// leave a slow client stuck on the stale state indefinitely.
	h.broadcast([]byte("frame-2"))

	if got := string(<-ch); got != "frame-2" {
		t.Fatalf("queued frame = %q, want the latest %q", got, "frame-2")
	}
}

func TestBroadcastDeliversToReadySubscriber(t *testing.T) {
	h := newHub()
	ch := h.subscribe()
	h.broadcast([]byte("hello"))
	if got := string(<-ch); got != "hello" {
		t.Fatalf("frame = %q, want %q", got, "hello")
	}
}

func TestSnapshotActivityLeaseExpires(t *testing.T) {
	h := newHub()
	now := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC)
	h.noteSnapshotRequest(now)

	if !h.snapshotRecentlyRequested(now.Add(snapshotActivityLease - time.Nanosecond)) {
		t.Fatal("snapshot activity lease expired too early")
	}
	if h.snapshotRecentlyRequested(now.Add(snapshotActivityLease)) {
		t.Fatal("snapshot activity lease did not expire")
	}
}
