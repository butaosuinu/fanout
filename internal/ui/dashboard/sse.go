package dashboard

import (
	"sync"
	"time"
)

// snapshotActivityLease keeps the GitHub tier active long enough for the next
// gh tick after a /api/snapshot request. The SPA uses that endpoint every two
// seconds when SSE is unavailable, so an open fallback client continuously
// renews the lease without increasing the GitHub polling cadence.
const snapshotActivityLease = defaultGHInterval + defaultCheapInterval

// hub is a minimal server-sent-events fan-out: subscribers register a buffered
// channel and receive every broadcast. Sends never block the poller — a slow
// client's queued frame is coalesced to the latest rather than stalling the
// broadcast. Pure stdlib; no websocket dependency.
type hub struct {
	mu                  sync.Mutex
	subs                map[chan []byte]struct{}
	snapshotActiveUntil time.Time
	closed              bool
}

func newHub() *hub {
	return &hub{subs: map[chan []byte]struct{}{}}
}

func (h *hub) subscribe() chan []byte {
	ch := make(chan []byte, 1)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(ch)
		return ch
	}
	h.subs[ch] = struct{}{}
	return ch
}

func (h *hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
}

func (h *hub) broadcast(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- payload:
		default:
			// Subscriber is behind: its queued frame is now stale. Broadcasts only
			// fire on a content change and heartbeats carry no snapshot, so simply
			// dropping the newest frame could leave a slow client showing an
			// outdated state indefinitely. Coalesce instead — discard the stale
			// queued frame and enqueue this one (the buffer holds the latest only).
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- payload:
			default:
			}
		}
	}
}

// closeAll terminates every subscriber (used on shutdown).
func (h *hub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}

func (h *hub) subscriberCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func (h *hub) noteSnapshotRequest(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.snapshotActiveUntil = now.Add(snapshotActivityLease)
}

func (h *hub) snapshotRecentlyRequested(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return now.Before(h.snapshotActiveUntil)
}
