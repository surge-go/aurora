package mq

import "context"

// Publisher publishes messages to logical destinations.
//
// Implementations must document their confirmation behavior. A publish error
// caused by a timeout or connection failure may leave acceptance uncertain,
// so callers should make retried messages idempotent.
// Implementations must not retain or mutate message byte slices after Publish
// returns. An implementation that publishes asynchronously must copy them
// before returning.
type Publisher interface {
	Publish(ctx context.Context, destination string, message Message) error
	Close(ctx context.Context) error
}

// Consumer consumes messages from a logical destination until ctx is canceled,
// the consumer is closed, or an unrecoverable consumption error occurs.
//
// A Consumer must document its handler-error and concurrency behavior. Close
// must be safe to call concurrently with Consume. Consume must derive a
// context for each active handler from ctx; Close must cancel that context so
// handlers can stop promptly. Handlers are expected to honor cancellation.
type Consumer interface {
	Consume(ctx context.Context, destination string, handler Handler) error
	Close(ctx context.Context) error
}
