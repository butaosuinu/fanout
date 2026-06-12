package team

import (
	"os"
	"time"
)

// FakeNowEnv freezes Now for golden/test determinism (FANOUT_FAKE_NOW).
const FakeNowEnv = "FANOUT_FAKE_NOW"

// Now returns the current time as RFC3339 UTC, the repo-wide timestamp
// convention (state createdAt). When FANOUT_FAKE_NOW is set its value is
// returned verbatim — not validated — so goldens control the exact bytes
// that land in the DB and Now stays infallible.
func Now() string {
	if fake := os.Getenv(FakeNowEnv); fake != "" {
		return fake
	}
	return time.Now().UTC().Format(time.RFC3339)
}
