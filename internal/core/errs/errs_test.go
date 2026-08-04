package errs

import (
	"errors"
	"fmt"
	"testing"
)

var errSentinel = errors.New("sentinel")

// TestWrap pins the message shape and the nil short-circuit: Wrap is called on
// every return of a wrapped function, so the nil case is the hot path.
func TestWrap(t *testing.T) {
	tests := []struct {
		name   string
		in     error
		format string
		args   []any
		want   string
	}{
		{
			name: "nil error stays nil",
			in:   nil,
			// A no-op format proves the nil check short-circuits before Sprintf.
			format: "unused %d",
			args:   []any{1},
			want:   "",
		},
		{
			name:   "prefixes the message with a colon",
			in:     errors.New("boom"),
			format: "load config",
			want:   "load config: boom",
		},
		{
			name:   "expands format arguments",
			in:     errors.New("no such file"),
			format: "worktree patch %q at %d",
			args:   []any{"/tmp/x", 42},
			want:   `worktree patch "/tmp/x" at 42: no such file`,
		},
		{
			name:   "literal percent in the message is not a verb",
			in:     errors.New("boom"),
			format: "100%% done",
			want:   "100% done: boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in
			Wrap(&err, tt.format, tt.args...)
			got := ""
			if err != nil {
				got = err.Error()
			}
			if got != tt.want {
				t.Errorf("Wrap(%v, %q) = %q, want %q", tt.in, tt.format, got, tt.want)
			}
		})
	}
}

// TestWrapPreservesChain guarantees callers can keep using errors.Is/As across
// a wrapped boundary — the whole point of %w over %v.
func TestWrapPreservesChain(t *testing.T) {
	err := fmt.Errorf("read state: %w", errSentinel)
	Wrap(&err, "load session %d", 7)

	if !errors.Is(err, errSentinel) {
		t.Errorf("errors.Is(%v, errSentinel) = false, want true", err)
	}
	if want := "load session 7: read state: sentinel"; err.Error() != want {
		t.Errorf("Wrap(...).Error() = %q, want %q", err.Error(), want)
	}
}

// TestWrapDeferredOverShadowedErr pins the mechanic the helper depends on: the
// deferred Wrap runs after the return values are assigned, so an err created by
// := inside an if block still gets wrapped.
func TestWrapDeferredOverShadowedErr(t *testing.T) {
	shadowing := func() (_ int, err error) {
		defer Wrap(&err, "outer")
		if v, err := 0, errSentinel; err != nil {
			return v, err
		}
		return 1, nil
	}

	_, err := shadowing()
	if want := "outer: sentinel"; err == nil || err.Error() != want {
		t.Errorf("shadowing() err = %v, want %q", err, want)
	}
}

// TestWrapRegisteredFirstWrapsCleanupErr pins the documented registration
// order: Wrap goes first so LIFO runs it last, catching an error a later
// cleanup defer assigned.
func TestWrapRegisteredFirstWrapsCleanupErr(t *testing.T) {
	withCleanup := func() (err error) {
		defer Wrap(&err, "outer")
		defer func() { err = errSentinel }()
		return nil
	}

	err := withCleanup()
	if want := "outer: sentinel"; err == nil || err.Error() != want {
		t.Errorf("withCleanup() err = %v, want %q", err, want)
	}
}
