package tracing

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type recordingExporter struct {
	mu       sync.Mutex
	spans    []sdktrace.ReadOnlySpan
	shutdown atomic.Int32
}

func (e *recordingExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, spans...)
	return nil
}

func (e *recordingExporter) Shutdown(context.Context) error {
	e.shutdown.Add(1)
	return nil
}

func withTestExporter(exporter sdktrace.SpanExporter) Option {
	return func(o *options) {
		o.exporterFactory = func(context.Context, Config) (sdktrace.SpanExporter, error) {
			return exporter, nil
		}
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "disabled can omit endpoint", cfg: Config{ServiceName: "svc"}},
		{name: "missing service", cfg: Config{Enabled: true, Endpoint: "collector:4317"}, want: "service_name"},
		{name: "missing endpoint", cfg: Config{Enabled: true, ServiceName: "svc"}, want: "endpoint"},
		{name: "invalid ratio", cfg: Config{ServiceName: "svc", SampleRatio: 2}, want: "sample_ratio"},
		{name: "invalid batch", cfg: Config{ServiceName: "svc", Batch: BatchConfig{MaxQueueSize: 2, MaxExportBatchSize: 3}}, want: "max_export_batch_size"},
		{name: "reserved resource", cfg: Config{ServiceName: "svc", Resource: map[string]string{"service.name": "other"}}, want: "reserved"},
		{name: "http requires tls or insecure", cfg: Config{Enabled: true, ServiceName: "svc", Protocol: ProtocolHTTPProto, Endpoint: "http://collector:4318"}, want: "insecure"},
		{name: "grpc host port", cfg: Config{Enabled: true, ServiceName: "svc", Endpoint: "collector:4317", Insecure: true}},
		{name: "grpc invalid port", cfg: Config{Enabled: true, ServiceName: "svc", Endpoint: "collector:65536", Insecure: true}, want: "port"},
		{name: "endpoint userinfo", cfg: Config{Enabled: true, ServiceName: "svc", Protocol: ProtocolHTTPProto, Endpoint: "https://user:secret@collector:4318"}, want: "userinfo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.want == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestNewDisabledRuntime(t *testing.T) {
	runtime, err := New(context.Background(), &Config{ServiceName: "svc"})
	require.NoError(t, err)
	require.NotNil(t, runtime)

	assert.NoError(t, runtime.Shutdown(context.Background()))
	assert.NoError(t, runtime.Shutdown(context.Background()))
	_, span := runtime.TracerProvider().Tracer("test").Start(context.Background(), "ignored")
	assert.False(t, span.SpanContext().IsValid())
}

func TestNewRecordsResourceAndFlushesOnShutdown(t *testing.T) {
	exporter := &recordingExporter{}
	runtime, err := New(context.Background(), &Config{
		Enabled:        true,
		ServiceName:    "svc",
		ServiceVersion: "1.2.3",
		Environment:    "test",
		Endpoint:       "collector:4317",
		Insecure:       true,
		SampleRatio:    1,
		Resource:       map[string]string{"service.instance.id": "instance-1"},
		Batch:          BatchConfig{MaxQueueSize: 16, MaxExportBatchSize: 4},
	}, withTestExporter(exporter))
	require.NoError(t, err)

	_, span := runtime.TracerProvider().Tracer("test").Start(context.Background(), "operation")
	span.End()
	require.NoError(t, runtime.Shutdown(context.Background()))
	require.Equal(t, int32(1), exporter.shutdown.Load())

	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	require.Len(t, exporter.spans, 1)
	attrs := exporter.spans[0].Resource().Attributes()
	assertResourceAttribute(t, attrs, "service.name", "svc")
	assertResourceAttribute(t, attrs, "service.version", "1.2.3")
	assertResourceAttribute(t, attrs, "deployment.environment.name", "test")
	assertResourceAttribute(t, attrs, "service.instance.id", "instance-1")
}

func TestSampleRatioZeroDoesNotSample(t *testing.T) {
	exporter := &recordingExporter{}
	runtime, err := New(context.Background(), &Config{
		Enabled:     true,
		ServiceName: "svc",
		Endpoint:    "collector:4317",
		Insecure:    true,
		SampleRatio: 0,
	}, withTestExporter(exporter))
	require.NoError(t, err)

	_, span := runtime.TracerProvider().Tracer("test").Start(context.Background(), "unsampled")
	span.End()
	require.NoError(t, runtime.Shutdown(context.Background()))

	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	assert.Empty(t, exporter.spans)
}

func TestShutdownConcurrentAndIdempotent(t *testing.T) {
	exporter := &recordingExporter{}
	runtime, err := New(context.Background(), &Config{
		Enabled:     true,
		ServiceName: "svc",
		Endpoint:    "collector:4317",
		Insecure:    true,
	}, withTestExporter(exporter))
	require.NoError(t, err)

	const callers = 20
	results := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() { results <- runtime.Shutdown(context.Background()) }()
	}
	for i := 0; i < callers; i++ {
		assert.NoError(t, <-results)
	}
	assert.Equal(t, int32(1), exporter.shutdown.Load())
}

func TestInstallGlobal(t *testing.T) {
	first, err := New(context.Background(), &Config{ServiceName: "first"})
	require.NoError(t, err)
	second, err := New(context.Background(), &Config{ServiceName: "second"})
	require.NoError(t, err)

	require.NoError(t, first.InstallGlobal())
	assert.NoError(t, first.InstallGlobal())
	assert.ErrorIs(t, second.InstallGlobal(), ErrGlobalProviderInstalled)
	assert.NotNil(t, otel.GetTracerProvider())
}

func TestPropagatorInjectExtract(t *testing.T) {
	runtime, err := New(context.Background(), &Config{ServiceName: "svc"})
	require.NoError(t, err)

	traceID, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	require.NoError(t, err)
	spanID, err := trace.SpanIDFromHex("0102030405060708")
	require.NoError(t, err)
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, TraceFlags: trace.FlagsSampled, Remote: true})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	carrier := propagation.MapCarrier{}
	runtime.Inject(ctx, carrier)
	got := runtime.Extract(context.Background(), carrier)
	assert.Equal(t, traceID, trace.SpanContextFromContext(got).TraceID())
	assert.Equal(t, spanID, trace.SpanContextFromContext(got).SpanID())
}

func TestNewExporterFailurePreservesError(t *testing.T) {
	sentinel := errors.New("factory failed")
	_, err := New(context.Background(), &Config{
		Enabled:     true,
		ServiceName: "svc",
		Endpoint:    "collector:4317",
		Insecure:    true,
	}, Option(func(o *options) {
		o.exporterFactory = func(context.Context, Config) (sdktrace.SpanExporter, error) {
			return nil, sentinel
		}
	}))
	assert.ErrorIs(t, err, sentinel)
}

func assertResourceAttribute(t *testing.T, attrs []attribute.KeyValue, key, want string) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			assert.Equal(t, want, attr.Value.AsString())
			return
		}
	}
	t.Errorf("resource attribute %q not found", key)
}

var _ sdktrace.SpanExporter = (*recordingExporter)(nil)
