package tracing

import (
	"context"

	"go.opentelemetry.io/otel/propagation"
)

// Inject writes the runtime propagator values into a carrier.
func (r *Runtime) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	r.Propagator().Inject(ctx, carrier)
}

// Extract reads propagation values from a carrier into a context.
func (r *Runtime) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return r.Propagator().Extract(ctx, carrier)
}
