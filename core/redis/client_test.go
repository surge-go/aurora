package redis

import (
	"context"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	metricdata "go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestNewClientMapsTopologies(t *testing.T) {
	tests := []struct {
		name  string
		cfg   *Config
		check func(*testing.T, goredis.UniversalClient)
	}{
		{
			name: "standalone",
			cfg:  &Config{Addrs: []string{" 127.0.0.1:6379 "}, DB: 2, Username: "app", DisableIdentity: true, IdentitySuffix: "test", Buffer: &BufferConfig{ReadBufferSize: 4096, WriteBufferSize: 8192}, Pool: &PoolConfig{Size: 23}},
			check: func(t *testing.T, client goredis.UniversalClient) {
				underlying, ok := unwrapClient(client).(*goredis.Client)
				if !ok {
					t.Fatalf("underlying client type = %T", client)
				}
				opt := underlying.Options()
				if opt.Addr != "127.0.0.1:6379" || opt.DB != 2 || opt.Username != "app" || opt.PoolSize != 23 || !opt.DisableIdentity || opt.IdentitySuffix != "test" || opt.ReadBufferSize != 4096 || opt.WriteBufferSize != 8192 {
					t.Fatalf("unexpected standalone options: %+v", opt)
				}
			},
		},
		{
			name: "sentinel",
			cfg:  &Config{Mode: ModeSentinel, Addrs: []string{"127.0.0.1:26379"}, Username: "data-user", Password: "data-pass", Sentinel: &SentinelConfig{MasterName: "primary", Username: "sentinel-user", Password: "sentinel-pass"}},
			check: func(t *testing.T, client goredis.UniversalClient) {
				underlying, ok := unwrapClient(client).(*goredis.Client)
				if !ok {
					t.Fatalf("underlying client type = %T", client)
				}
				opt := underlying.Options()
				if opt.DB != 0 || opt.Username != "data-user" || opt.Password != "data-pass" {
					t.Fatalf("unexpected failover data-node options: %+v", opt)
				}
			},
		},
		{
			name: "cluster",
			cfg:  &Config{Mode: ModeCluster, Addrs: []string{" 127.0.0.1:6379 "}, Username: "cluster-user", Cluster: &ClusterConfig{MaxRedirects: 5, RouteByLatency: true}},
			check: func(t *testing.T, client goredis.UniversalClient) {
				underlying, ok := unwrapClient(client).(*goredis.ClusterClient)
				if !ok {
					t.Fatalf("underlying client type = %T", client)
				}
				opt := underlying.Options()
				if len(opt.Addrs) != 1 || opt.Addrs[0] != "127.0.0.1:6379" || opt.Username != "cluster-user" || opt.MaxRedirects != 5 || !opt.ReadOnly || !opt.RouteByLatency {
					t.Fatalf("unexpected cluster options: %+v", opt)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(tt.cfg)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			tt.check(t, client)
			if err := client.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
		})
	}
}

func TestNewClientWithoutMetricsReturnsNativeClient(t *testing.T) {
	client, err := NewClient(&Config{Addrs: []string{"127.0.0.1:6379"}})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()
	if _, ok := client.(*goredis.Client); !ok {
		t.Fatalf("client type = %T, want *redis.Client when metrics are disabled", client)
	}
}

func unwrapClient(client goredis.UniversalClient) goredis.UniversalClient {
	if managed, ok := client.(*managedClient); ok {
		return managed.UniversalClient
	}
	return client
}

func TestNewClientTracingHidesCommandByDefault(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := trace.NewTracerProvider(trace.WithSpanProcessor(recordingProcessor{recorder}))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	client, err := NewClient(&Config{
		Addrs:      []string{"127.0.0.1:1"},
		Timeout:    &TimeoutConfig{DialTimeout: 20 * time.Millisecond},
		Retry:      &RetryConfig{MaxRetries: -1},
		Monitoring: &MonitoringConfig{TracingEnabled: true, TracerProvider: provider},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = client.Get(ctx, "secret-key").Err()

	spans := recorder.Ended()
	if len(spans) == 0 {
		t.Fatal("expected a Redis client span")
	}
	for _, attr := range spans[0].Attributes() {
		if attr.Key == attribute.Key("db.statement") {
			t.Fatalf("sensitive command attribute was recorded: %v", attr)
		}
	}
}

func TestManagedClientCloseIsIdempotent(t *testing.T) {
	client, err := NewClient(&Config{
		Addrs:      []string{"127.0.0.1:6379"},
		Monitoring: &MonitoringConfig{MetricsEnabled: true},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	const callers = 12
	results := make(chan error, callers)
	for range callers {
		go func() { results <- client.Close() }()
	}
	var first error
	firstSet := false
	for range callers {
		got := <-results
		if !firstSet {
			first = got
			firstSet = true
			continue
		}
		if (got == nil) != (first == nil) || (got != nil && got.Error() != first.Error()) {
			t.Fatalf("Close() results differ: first=%v current=%v", first, got)
		}
	}
}

func TestMetricsInstrumentationCloseDoesNotShutdownProvider(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	client, err := NewClient(&Config{
		Addrs:      []string{"127.0.0.1:6379"},
		Monitoring: &MonitoringConfig{MetricsEnabled: true, MeterProvider: provider},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	var redisMetrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &redisMetrics); err != nil {
		t.Fatalf("Collect() Redis metrics error = %v", err)
	}
	if !hasMetric(redisMetrics, "db.client.connections.usage") {
		t.Fatal("redisotel connection pool metrics were not registered")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	counter, err := provider.Meter("redis-test").Int64Counter("provider.still_open")
	if err != nil {
		t.Fatalf("create counter after client Close() error = %v", err)
	}
	counter.Add(context.Background(), 1)
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatalf("Collect() after client Close() error = %v", err)
	}
	if !hasMetric(data, "provider.still_open") {
		t.Fatal("application MeterProvider was shut down by Redis client Close()")
	}
}

func hasMetric(data metricdata.ResourceMetrics, name string) bool {
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == name {
				return true
			}
		}
	}
	return false
}

type recordingProcessor struct{ recorder *tracetest.SpanRecorder }

func (p recordingProcessor) OnStart(context.Context, trace.ReadWriteSpan) {}
func (p recordingProcessor) OnEnd(span trace.ReadOnlySpan)                { p.recorder.OnEnd(span) }
func (recordingProcessor) Shutdown(context.Context) error                 { return nil }
func (recordingProcessor) ForceFlush(context.Context) error               { return nil }
