package attempt

import "errors"

// Stable errors returned by the login-attempt limiter. Callers should use
// errors.Is instead of matching error text.
var (
	ErrInvalidConfig      = errors.New("auth attempt: invalid config")
	ErrRedisRequired      = errors.New("auth attempt: redis client required")
	ErrAccountKeyRequired = errors.New("auth attempt: account key required")
	ErrAttemptRequired    = errors.New("auth attempt: attempt required")
	ErrStoreUnavailable   = errors.New("auth attempt: store unavailable")
	ErrAttemptInvalidated = errors.New("auth attempt: attempt invalidated")
)
