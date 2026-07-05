package dashboard

import "testing"

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
