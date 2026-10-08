package metrics

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type recordingExporter struct {
	mu          sync.Mutex
	collected   []metricdata.ResourceMetrics
	shutdown    atomic.Int32
	exportCalls atomic.Int32
	exportErr   error
}

func (e *recordingExporter) Temporality(sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.CumulativeTemporality
}

func (e *recordingExporter) Aggregation(kind sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(kind)
}

func (e *recordingExporter) Export(_ context.Context, data *metricdata.ResourceMetrics) error {
	e.exportCalls.Add(1)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.collected = append(e.collected, *data)
	return e.exportErr
}

func (e *recordingExporter) ForceFlush(context.Context) error { return nil }

func (e *recordingExporter) Shutdown(context.Context) error {
	e.shutdown.Add(1)
	return nil
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
		{name: "invalid protocol", cfg: Config{ServiceName: "svc", Protocol: "prometheus"}, want: "protocol"},
		{name: "negative interval", cfg: Config{ServiceName: "svc", ExportInterval: -time.Second}, want: "must not be negative"},
		{name: "reserved resource", cfg: Config{ServiceName: "svc", Resource: map[string]string{"service.name": "other"}}, want: "reserved"},
		{name: "empty resource key", cfg: Config{ServiceName: "svc", Resource: map[string]string{" ": "v"}}, want: "resource keys"},
		{name: "empty header key", cfg: Config{ServiceName: "svc", Headers: map[string]string{" ": "v"}}, want: "header keys"},
		{name: "invalid header key", cfg: Config{ServiceName: "svc", Headers: map[string]string{"x:y": "v"}}, want: "header keys"},
		{name: "header newline", cfg: Config{ServiceName: "svc", Headers: map[string]string{"Authorization": "secret\nvalue"}}, want: "line break"},
		{name: "grpc endpoint", cfg: Config{Enabled: true, ServiceName: "svc", Endpoint: "collector:4317", Insecure: true}},
		{name: "grpc url requires port", cfg: Config{Enabled: true, ServiceName: "svc", Endpoint: "https://collector"}, want: "port"},
		{name: "grpc port", cfg: Config{Enabled: true, ServiceName: "svc", Endpoint: "collector:65536", Insecure: true}, want: "port"},
		{name: "grpc http requires insecure", cfg: Config{Enabled: true, ServiceName: "svc", Endpoint: "http://collector:4317"}, want: "insecure"},
		{name: "grpc https rejects insecure", cfg: Config{Enabled: true, ServiceName: "svc", Endpoint: "https://collector:4317", Insecure: true}, want: "cannot set insecure"},
		{name: "http endpoint path", cfg: Config{Enabled: true, ServiceName: "svc", Protocol: ProtocolHTTPProto, Endpoint: "https://collector:4318/custom/v1/metrics"}},
		{name: "http endpoint insecure", cfg: Config{Enabled: true, ServiceName: "svc", Protocol: ProtocolHTTPProto, Endpoint: "http://collector:4318", Insecure: true}},
		{name: "endpoint userinfo", cfg: Config{Enabled: true, ServiceName: "svc", Protocol: ProtocolHTTPProto, Endpoint: "https://user:secret@collector:4318"}, want: "userinfo"},
		{name: "endpoint query", cfg: Config{Enabled: true, ServiceName: "svc", Protocol: ProtocolHTTPProto, Endpoint: "https://collector:4318?token=secret"}, want: "query"},
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
	var factoryCalls atomic.Int32
	runtime, err := New(context.Background(), &Config{ServiceName: "svc"}, WithReaderFactory(func(context.Context, Config) (sdkmetric.Reader, error) {
		factoryCalls.Add(1)
		return nil, errors.New("must not be called")
	}))
	require.NoError(t, err)
	require.NotNil(t, runtime)
	assert.Zero(t, factoryCalls.Load())
	assert.NoError(t, runtime.Shutdown(context.Background()))
	assert.NoError(t, runtime.Shutdown(context.Background()))
}

func TestNewCollectsMetricsAndFlushesOnShutdown(t *testing.T) {
	exporter := &recordingExporter{}
	reader := sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(time.Hour))
	runtime, err := New(context.Background(), &Config{
		Enabled:        true,
		ServiceName:    "svc",
		ServiceVersion: "1.2.3",
		Environment:    "test",
		Endpoint:       "collector:4317",
		Insecure:       true,
		ExportTimeout:  time.Second,
		Resource:       map[string]string{"service.instance.id": "instance-1"},
	}, WithReaderFactory(func(context.Context, Config) (sdkmetric.Reader, error) { return reader, nil }))
	require.NoError(t, err)

	counter, err := runtime.MeterProvider().Meter("test").Int64Counter("requests")
	require.NoError(t, err)
	counter.Add(context.Background(), 3)
	require.NoError(t, runtime.Shutdown(context.Background()))
	require.Equal(t, int32(1), exporter.shutdown.Load())
	require.Len(t, exporter.collected, 1)

	data := exporter.collected[0]
	require.GreaterOrEqual(t, len(data.Resource.Attributes()), 4)
	assertResourceAttribute(t, data.Resource.Attributes(), "service.name", "svc")
	assertResourceAttribute(t, data.Resource.Attributes(), "service.version", "1.2.3")
	assertResourceAttribute(t, data.Resource.Attributes(), "deployment.environment.name", "test")
	assertResourceAttribute(t, data.Resource.Attributes(), "service.instance.id", "instance-1")
	require.Len(t, data.ScopeMetrics, 1)
	require.Len(t, data.ScopeMetrics[0].Metrics, 1)
	sum, ok := data.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[int64])
	require.True(t, ok)
	require.Len(t, sum.DataPoints, 1)
	assert.Equal(t, int64(3), sum.DataPoints[0].Value)
}

func TestOTLPHTTPExporterPreservesEndpointPath(t *testing.T) {
	requests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	runtime, err := New(context.Background(), &Config{
		Enabled:        true,
		ServiceName:    "svc",
		Protocol:       ProtocolHTTPProto,
		Endpoint:       server.URL + "/custom/v1/metrics",
		Insecure:       true,
		ExportInterval: time.Hour,
		ExportTimeout:  time.Second,
	})
	require.NoError(t, err)
	counter, err := runtime.MeterProvider().Meter("test").Int64Counter("requests")
	require.NoError(t, err)
	counter.Add(context.Background(), 1)
	require.NoError(t, runtime.Shutdown(context.Background()))
	select {
	case path := <-requests:
		assert.Equal(t, "/custom/v1/metrics", path)
	case <-time.After(time.Second):
		t.Fatal("OTLP HTTP exporter did not send a request")
	}
}

func TestOTLPHTTPExporterUsesDefaultMetricsPath(t *testing.T) {
	requests := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	runtime, err := New(context.Background(), &Config{
		Enabled:        true,
		ServiceName:    "svc",
		Protocol:       ProtocolHTTPProto,
		Endpoint:       server.URL,
		Insecure:       true,
		ExportInterval: time.Hour,
		ExportTimeout:  time.Second,
	})
	require.NoError(t, err)
	counter, err := runtime.MeterProvider().Meter("test").Int64Counter("requests")
	require.NoError(t, err)
	counter.Add(context.Background(), 1)
	require.NoError(t, runtime.Shutdown(context.Background()))
	select {
	case path := <-requests:
		assert.Equal(t, "/v1/metrics", path)
	case <-time.After(time.Second):
		t.Fatal("OTLP HTTP exporter did not send a request")
	}
}

func TestReaderFactoryFailureClosesReturnedReader(t *testing.T) {
	exporter := &recordingExporter{}
	reader := sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(time.Hour))
	sentinel := errors.New("reader setup failed")
	_, err := New(context.Background(), &Config{Enabled: true, ServiceName: "svc", Endpoint: "collector:4317", Insecure: true}, WithReaderFactory(func(context.Context, Config) (sdkmetric.Reader, error) {
		return reader, sentinel
	}))
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, int32(1), exporter.shutdown.Load())
}

func TestShutdownConcurrentAndIdempotent(t *testing.T) {
	exporter := &recordingExporter{}
	reader := sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(time.Hour))
	runtime, err := New(context.Background(), &Config{Enabled: true, ServiceName: "svc", Endpoint: "collector:4317", Insecure: true}, WithReaderFactory(func(context.Context, Config) (sdkmetric.Reader, error) { return reader, nil }))
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
	assert.Equal(t, first.MeterProvider(), otel.GetMeterProvider())
}

func TestInstallGlobalRejectsExternalProvider(t *testing.T) {
	external := metricnoop.NewMeterProvider()
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(external)
	t.Cleanup(func() {
		if previous == initialGlobalProvider {
			return
		}
		otel.SetMeterProvider(previous)
	})

	runtime, err := New(context.Background(), &Config{ServiceName: "svc"})
	require.NoError(t, err)
	assert.ErrorIs(t, runtime.InstallGlobal(), ErrGlobalProviderInstalled)
}

func TestNewCopiesConfigMaps(t *testing.T) {
	config := &Config{
		Enabled:     true,
		ServiceName: "svc",
		Endpoint:    "collector:4317",
		Insecure:    true,
		Headers:     map[string]string{"authorization": "secret"},
		Resource:    map[string]string{"region": "west"},
	}
	var got Config
	runtime, err := New(context.Background(), config, WithReaderFactory(func(_ context.Context, cfg Config) (sdkmetric.Reader, error) {
		got = cfg
		return sdkmetric.NewManualReader(), nil
	}))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, runtime.Shutdown(context.Background())) })
	config.Headers["authorization"] = "changed"
	config.Resource["region"] = "east"
	assert.Equal(t, "secret", got.Headers["authorization"])
	assert.Equal(t, "west", got.Resource["region"])
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

var _ sdkmetric.Exporter = (*recordingExporter)(nil)
