package backend

// Observation policy shared by the runtime adapters and the orchestration that
// drives them: the wait budget a caller inherits when it names none of its own,
// and the contract that marks a read-only failure retryable inside that budget.
// The adapters that produce such failures live in infra; this file is the
// policy value, the contract, and the predicate over it.

import (
	"errors"
	"time"
)

// DefaultWaitTimeout is the bounded wait used when a caller omits an
// explicit total timeout.
const DefaultWaitTimeout = 300 * time.Second

// RetryableObservation is the contract a runtime adapter's transient read-only
// failure implements. Orchestration never names the concrete error type.
type RetryableObservation interface {
	error
	RetryableObservation() bool
}

// IsRetryableObservationError reports whether a read-only runtime command may
// be retried within the caller's fixed observation budget.
func IsRetryableObservationError(err error) bool {
	retryable, ok := errors.AsType[RetryableObservation](err)
	return ok && retryable.RetryableObservation()
}
