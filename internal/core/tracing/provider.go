package tracing

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

var (
	ErrGlobalProviderInstalled = errors.New("tracing global provider is already installed")
	ErrRuntimeClosed           = errors.New("tracing runtime is closed")
)

type exporterFactory func(context.Context, Config) (sdktrace.SpanExporter, error)

type options struct {
	exporterFactory exporterFactory
}

// Option customizes runtime construction. Options are intentionally small;
// exporter construction remains owned by this package.
type Option func(*options)

// Runtime owns the Provider and exporter created by New.
type Runtime struct {
	provider   trace.TracerProvider
	propagator propagation.TextMapPropagator
	shutdown   func(context.Context) error

	closed          atomic.Bool
	globalInstalled atomic.Bool
	shutdownOnce    sync.Once
	shutdownErr     error
}

var globalInstall struct {
	sync.Mutex
	runtime *Runtime
}

// New creates a tracing runtime from configuration.
func New(ctx context.Context, cfg *Config, opts ...Option) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("tracing context is nil")
	}
	if cfg == nil {
		return nil, errors.New("tracing config is nil")
	}

	config := cloneConfig(cfg)
	if err := config.Validate(); err != nil {
		return nil, err
	}
	settings := options{}
	for _, opt := range opts {
		if opt != nil {
			opt(&settings)
		}
	}

	propagator := newPropagator()
	if !config.Enabled {
		return &Runtime{
			provider:   trace.NewNoopTracerProvider(),
			propagator: propagator,
			shutdown:   func(context.Context) error { return nil },
		}, nil
	}

	runtimeResource, err := buildResource(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create tracing resource: %w", err)
	}

	factory := settings.exporterFactory
	if factory == nil {
		factory = newOTLPExporter
	}
	exporter, err := factory(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create tracing OTLP exporter: %w", err)
	}

	batchOptions := batchProcessorOptions(config.Batch)
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(runtimeResource),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(config.SampleRatio))),
		sdktrace.WithBatcher(exporter, batchOptions...),
	)

	return &Runtime{
		provider:   provider,
		propagator: propagator,
		shutdown:   provider.Shutdown,
	}, nil
}

// TracerProvider returns the runtime's provider for explicit instrumentation injection.
func (r *Runtime) TracerProvider() trace.TracerProvider {
	if r == nil || r.provider == nil {
		return trace.NewNoopTracerProvider()
	}
	return r.provider
}

// Propagator returns the W3C Trace Context and Baggage propagator.
func (r *Runtime) Propagator() propagation.TextMapPropagator {
	if r == nil || r.propagator == nil {
		return newPropagator()
	}
	return r.propagator
}

// InstallGlobal installs this runtime's provider and propagator exactly once
// for the process. It does not replace a provider installed by this package.
func (r *Runtime) InstallGlobal() error {
	if r == nil {
		return errors.New("tracing runtime is nil")
	}
	if r.closed.Load() {
		return ErrRuntimeClosed
	}

	globalInstall.Lock()
	defer globalInstall.Unlock()
	if globalInstall.runtime != nil && globalInstall.runtime != r {
		return ErrGlobalProviderInstalled
	}
	if globalInstall.runtime == r {
		if otel.GetTracerProvider() != r.provider || !reflect.DeepEqual(otel.GetTextMapPropagator(), r.propagator) {
			return ErrGlobalProviderInstalled
		}
		r.globalInstalled.Store(true)
		return nil
	}
	if globalInstall.runtime == nil {
		otel.SetTracerProvider(r.provider)
		otel.SetTextMapPropagator(r.propagator)
		globalInstall.runtime = r
	}
	r.globalInstalled.Store(true)
	return nil
}

// Shutdown flushes and closes resources owned by this runtime. It is safe to
// call concurrently and repeatedly; all callers observe the first result.
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("tracing shutdown context is nil")
	}
	r.shutdownOnce.Do(func() {
		r.closed.Store(true)
		r.shutdownErr = r.shutdown(ctx)
	})
	return r.shutdownErr
}

func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func buildResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	attrs := make([]attribute.KeyValue, 0, 3+len(cfg.Resource))
	attrs = append(attrs, attribute.String("service.name", strings.TrimSpace(cfg.ServiceName)))
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, attribute.String("service.version", cfg.ServiceVersion))
	}
	if cfg.Environment != "" {
		attrs = append(attrs, attribute.String("deployment.environment.name", cfg.Environment))
	}
	keys := make([]string, 0, len(cfg.Resource))
	for key := range cfg.Resource {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		attrs = append(attrs, attribute.String(key, cfg.Resource[key]))
	}

	custom := resource.NewSchemaless(attrs...)
	merged, err := resource.Merge(resource.Default(), custom)
	if err != nil {
		return nil, err
	}
	return merged, nil
}

func newOTLPExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	protocol := cfg.effectiveProtocol()
	if protocol == ProtocolHTTPProto {
		opts := []otlptracehttp.Option{otlptracehttp.WithHeaders(cloneStringMap(cfg.Headers))}
		endpoint := strings.TrimSpace(cfg.Endpoint)
		if strings.Contains(endpoint, "://") {
			opts = append(opts, otlptracehttp.WithEndpointURL(endpoint))
		} else {
			opts = append(opts, otlptracehttp.WithEndpoint(endpoint))
		}
		if cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		if cfg.Batch.ExportTimeout > 0 {
			opts = append(opts, otlptracehttp.WithTimeout(cfg.Batch.ExportTimeout))
		}
		return otlptracehttp.New(ctx, opts...)
	}

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithHeaders(cloneStringMap(cfg.Headers)),
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if strings.Contains(endpoint, "://") {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("invalid grpc endpoint: %w", err)
		}
		if parsed.Scheme == "" {
			return nil, errors.New("invalid grpc endpoint: missing scheme")
		}
		opts = append(opts, otlptracegrpc.WithEndpointURL(endpoint))
	} else {
		opts = append(opts, otlptracegrpc.WithEndpoint(endpoint))
	}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	if cfg.Batch.ExportTimeout > 0 {
		opts = append(opts, otlptracegrpc.WithTimeout(cfg.Batch.ExportTimeout))
	}
	return otlptracegrpc.New(ctx, opts...)
}

func batchProcessorOptions(cfg BatchConfig) []sdktrace.BatchSpanProcessorOption {
	options := make([]sdktrace.BatchSpanProcessorOption, 0, 4)
	if cfg.MaxQueueSize > 0 {
		options = append(options, sdktrace.WithMaxQueueSize(cfg.MaxQueueSize))
	}
	if cfg.MaxExportBatchSize > 0 {
		options = append(options, sdktrace.WithMaxExportBatchSize(cfg.MaxExportBatchSize))
	}
	if cfg.ScheduleDelay > 0 {
		options = append(options, sdktrace.WithBatchTimeout(cfg.ScheduleDelay))
	}
	if cfg.ExportTimeout > 0 {
		options = append(options, sdktrace.WithExportTimeout(cfg.ExportTimeout))
	}
	return options
}
