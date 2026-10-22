package attempt

import "time"

// Attempt is a one-time handle returned by Begin. Its fields are intentionally
// private so callers cannot forge a handle for another account or epoch.
//
// Each Attempt must be settled exactly once via RecordFailure or RecordSuccess.
// After settlement, the handle is invalidated. Concurrent settlement of the same
// handle will cause one call to return ErrAttemptInvalidated.
type Attempt struct {
	id          string
	accountHash string
	epoch       int64
}

// Status describes the current login-attempt state.
// A locked account is represented by Status.Locked rather than an error.
//
// Status is a point-in-time snapshot and should not be cached. If the account
// is unlocked or successfully authenticated after Status is returned, the
// snapshot becomes stale.
type Status struct {
	Locked           bool
	FailureCount     int
	RemainingAttempt int
	RetryAfter       time.Duration
}

// UnlockResult reports whether Unlock cleared an existing failure state.
type UnlockResult struct {
	Cleared bool
}
