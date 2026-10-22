// Package attempt provides Redis-backed login failure tracking for account
// authentication flows.
//
// The package does not validate credentials, know account status, issue tokens,
// or authorize administrative unlock requests. The application owns those
// responsibilities and the injected Redis client's lifecycle.
//
// # Concurrency
//
// A Limiter instance is safe for concurrent use by multiple goroutines. Each
// Attempt handle returned by Begin must be settled exactly once (via
// RecordFailure or RecordSuccess) and should not be shared across goroutines.
// After settlement, the handle is invalidated and cannot be reused.
package attempt
