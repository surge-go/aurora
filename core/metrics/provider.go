package metrics

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
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
)

var (
	ErrGlobalProviderInstalled = errors.New("metrics global provider is already installed")
	ErrRuntimeClosed           = errors.New("metrics runtime is closed")
	initialGlobalProvider      = otel.GetMeterProvider()
)

// ReaderFactory creates a reader for a runtime. The runtime takes ownership
// of the returned reader and closes it through MeterProvider.Shutdown.
type ReaderFactory func(context.Context, Config) (sdkmetric.Reader, error)

type options struct {
	readerFactory ReaderFactory
}

// Option customizes runtime construction.
type Option func(*options)

// WithReaderFactory replaces the default OTLP exporter and periodic reader.
// The returned reader must own and close any exporter it uses.
func WithReaderFactory(factory ReaderFactory) Option {
	return func(o *options) { o.readerFactory = factory }
}

// Runtime owns the MeterProvider and reader created by New.
type Runtime struct {
	provider metric.MeterProvider
	shutdown func(context.Context) error

	closed       atomic.Bool
	shutdownOnce sync.Once
	shutdownErr  error
}

var globalInstall struct {
	sync.Mutex
	runtime *Runtime
}

// New creates a metrics runtime from configuration.
func New(ctx context.Context, cfg *Config, opts ...Option) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("metrics context is nil")
	}
	if cfg == nil {
		return nil, errors.New("metrics config is nil")
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
	if !config.Enabled {
		return &Runtime{
			provider: metricnoop.NewMeterProvider(),
			shutdown: func(context.Context) error { return nil },
		}, nil
	}

	runtimeResource, err := buildResource(config)
	if err != nil {
		return nil, fmt.Errorf("create metrics resource: %w", err)
	}

	var reader sdkmetric.Reader
	if settings.readerFactory != nil {
		reader, err = settings.readerFactory(ctx, config)
		if err != nil {
			if reader != nil {
				err = errors.Join(err, reader.Shutdown(ctx))
			}
			return nil, fmt.Errorf("create metrics reader: %w", err)
		}
		if reader == nil {
			return nil, errors.New("create metrics reader: reader factory returned nil")
		}
	} else {
		reader, err = newOTLPReader(ctx, config)
		if err != nil {
			return nil, fmt.Errorf("create OTLP metrics reader: %w", err)
		}
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(runtimeResource),
		sdkmetric.WithReader(reader),
	)
	return &Runtime{provider: provider, shutdown: provider.Shutdown}, nil
}

// MeterProvider returns the runtime provider for explicit instrumentation injection.
func (r *Runtime) MeterProvider() metric.MeterProvider {
	if r == nil || r.provider == nil {
		return metricnoop.NewMeterProvider()
	}
	return r.provider
}

// InstallGlobal installs this runtime's provider exactly once for the process.
func (r *Runtime) InstallGlobal() error {
	if r == nil || r.provider == nil {
		return errors.New("metrics runtime is nil")
	}
	if r.closed.Load() {
		return ErrRuntimeClosed
	}

	globalInstall.Lock()
	defer globalInstall.Unlock()
	if r.closed.Load() {
		return ErrRuntimeClosed
	}
	current := otel.GetMeterProvider()
	if globalInstall.runtime != nil && globalInstall.runtime != r {
		return ErrGlobalProviderInstalled
	}
	if globalInstall.runtime == r {
		if !reflect.DeepEqual(current, r.provider) {
			return ErrGlobalProviderInstalled
		}
		return nil
	}
	if !reflect.DeepEqual(current, initialGlobalProvider) {
		return ErrGlobalProviderInstalled
	}
	otel.SetMeterProvider(r.provider)
	globalInstall.runtime = r
	return nil
}

// Shutdown flushes and closes resources owned by this runtime. It is safe to
// call concurrently and repeatedly; all callers observe the first result.
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("metrics shutdown context is nil")
	}
	r.shutdownOnce.Do(func() {
		globalInstall.Lock()
		r.closed.Store(true)
		globalInstall.Unlock()
		if r.shutdown != nil {
			r.shutdownErr = r.shutdown(ctx)
		}
	})
	return r.shutdownErr
}

func buildResource(cfg Config) (*resource.Resource, error) {
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

func newOTLPReader(ctx context.Context, cfg Config) (sdkmetric.Reader, error) {
	var exporter sdkmetric.Exporter
	var err error
	if cfg.effectiveProtocol() == ProtocolHTTPProto {
		exporter, err = newHTTPExporter(ctx, cfg)
	} else {
		exporter, err = newGRPCExporter(ctx, cfg)
	}
	if err != nil {
		return nil, err
	}
	readerOptions := make([]sdkmetric.PeriodicReaderOption, 0, 2)
	if cfg.ExportInterval > 0 {
		readerOptions = append(readerOptions, sdkmetric.WithInterval(cfg.ExportInterval))
	}
	if cfg.ExportTimeout > 0 {
		readerOptions = append(readerOptions, sdkmetric.WithTimeout(cfg.ExportTimeout))
	}
	return sdkmetric.NewPeriodicReader(exporter, readerOptions...), nil
}

func newHTTPExporter(ctx context.Context, cfg Config) (sdkmetric.Exporter, error) {
	opts := []otlpmetrichttp.Option{otlpmetrichttp.WithHeaders(cloneStringMap(cfg.Headers))}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if strings.Contains(endpoint, "://") {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("parse http endpoint: %w", err)
		}
		if parsed.Path == "" {
			opts = append(opts, otlpmetrichttp.WithEndpoint(parsed.Host))
		} else {
			opts = append(opts, otlpmetrichttp.WithEndpointURL(endpoint))
		}
	} else {
		opts = append(opts, otlpmetrichttp.WithEndpoint(endpoint))
	}
	if cfg.Insecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}
	if cfg.ExportTimeout > 0 {
		opts = append(opts, otlpmetrichttp.WithTimeout(cfg.ExportTimeout))
	}
	return otlpmetrichttp.New(ctx, opts...)
}

func newGRPCExporter(ctx context.Context, cfg Config) (sdkmetric.Exporter, error) {
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithHeaders(cloneStringMap(cfg.Headers))}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if strings.Contains(endpoint, "://") {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return nil, fmt.Errorf("parse grpc endpoint: %w", err)
		}
		opts = append(opts, otlpmetricgrpc.WithEndpointURL(endpoint))
		if parsed.Scheme == "http" {
			opts = append(opts, otlpmetricgrpc.WithInsecure())
		}
	} else {
		opts = append(opts, otlpmetricgrpc.WithEndpoint(endpoint))
		if cfg.Insecure {
			opts = append(opts, otlpmetricgrpc.WithInsecure())
		}
	}
	if cfg.ExportTimeout > 0 {
		opts = append(opts, otlpmetricgrpc.WithTimeout(cfg.ExportTimeout))
	}
	return otlpmetricgrpc.New(ctx, opts...)
}
