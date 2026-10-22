package aurora

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/go-viper/mapstructure/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/surge-go/aurora/core/auth"
	"github.com/surge-go/aurora/core/auth/attempt"
	coreconfig "github.com/surge-go/aurora/core/config"
	"github.com/surge-go/aurora/core/database"
	"github.com/surge-go/aurora/core/logger"
	"github.com/surge-go/aurora/core/metrics"
	"github.com/surge-go/aurora/core/mq"
	"github.com/surge-go/aurora/core/ratelimit"
	mqkafka "github.com/surge-go/aurora/core/mq/kafka"
	mqrabbitmq "github.com/surge-go/aurora/core/mq/rabbitmq"
	mqredis "github.com/surge-go/aurora/core/mq/redis"
	coreRedis "github.com/surge-go/aurora/core/redis"
	"github.com/surge-go/aurora/core/tracing"
	"gorm.io/gorm"
)

const defaultLifecycleConfigPath = "config/aurora.yaml"

// Lifecycle 是 Aurora 的应用组合根。
//
// Lifecycle 嵌入 Engine，因此用户可以直接注册路由并调用 Run：
//
//	engine, err := aurora.NewLifecycle()
//	if err != nil { log.Fatal(err) }
//	engine.GET("/health", health)
//	if err := engine.Run(ctx); err != nil { log.Fatal(err) }
//
// Lifecycle 只关闭由 NewLifecycle 创建的资源。通过普通 New 创建的 Engine
// 仍然遵循原有约定，由调用方管理外部 logger、tracing、metrics 和客户端。
type Lifecycle struct {
	*Engine

	// Config 是实际加载配置的管理器。调用方不需要使用它来注册路由，
	// 但可以读取配置或在停机时观察配置监控状态。
	Config *coreconfig.Config

	Logger   *logger.Logger
	Tracing  *tracing.Runtime
	Metrics  *metrics.Runtime
	Database *gorm.DB
	Redis    redis.UniversalClient

	// authManagers is populated during construction and is read-only afterwards.
	authManagers map[string]*auth.Manager
	// attemptLimiters is populated during construction and is read-only afterwards.
	attemptLimiters map[string]*attempt.Limiter
	// rateLimiters is populated during construction and is read-only afterwards.
	rateLimiters map[string]*ratelimit.Limiter

	MQPublisher mq.Publisher
	MQConsumer  mq.Consumer

	stateMu   sync.Mutex
	running   bool
	closed    bool
	runCancel context.CancelFunc
	runDone   chan struct{}

	closeOnce sync.Once
	closeErr  error
}

// lifecycleConfig 是配置文件的稳定外层结构。
// 外部资源使用指针或 map 表示可选配置段：配置段不存在时保持 nil，生命周期
// 不会创建对应资源，也不需要在 YAML 中增加 enabled 开关。
type lifecycleConfig struct {
	Aurora   Config            `mapstructure:"aurora"`
	Tracing  *tracing.Config   `mapstructure:"tracing"`
	Metrics  *metrics.Config   `mapstructure:"metrics"`
	Logger   logger.Config     `mapstructure:"logger"`
	Database *database.Config  `mapstructure:"database"`
	Redis    *coreRedis.Config `mapstructure:"redis"`
	MQ       *lifecycleMQ      `mapstructure:"mq"`

	// Auth is decoded separately to reject unknown fields without leaking secrets
	// through configuration decoder errors.
	Auth      map[string]auth.Config      `mapstructure:"-"`
	Attempt   map[string]attempt.Config   `mapstructure:"-"`
	RateLimit map[string]ratelimit.Config `mapstructure:"-"`
}

type lifecycleMQ struct {
	Driver   string            `mapstructure:"driver"`
	Redis    mqredis.Config    `mapstructure:"redis"`
	RabbitMQ mqrabbitmq.Config `mapstructure:"rabbitmq"`
	Kafka    mqkafka.Config    `mapstructure:"kafka"`
}

// NewLifecycle loads the Aurora configuration and creates a complete
// application lifecycle. With no path it creates config/aurora.yaml in the
// current working directory when that file does not exist. An explicit path
// must already exist and is never overwritten.
//
// NewLifecycle does not register routes and does not start listening. Register
// routes on the returned Lifecycle, then call Run, Start, or Serve yourself.
func NewLifecycle(path ...string) (*Lifecycle, error) {
	configPath, generated, err := lifecycleConfigPath(path)
	if err != nil {
		return nil, err
	}
	if generated {
		if err := ensureDefaultLifecycleConfig(configPath); err != nil {
			return nil, err
		}
	}

	managedConfig := coreconfig.New(coreconfig.WithConfigFile(configPath))
	if err := managedConfig.Load(); err != nil {
		managedConfig.Close()
		return nil, fmt.Errorf("load Aurora config %q: %w", configPath, err)
	}

	settings := defaultLifecycleConfig()
	if err := managedConfig.Unmarshal(&settings); err != nil {
		managedConfig.Close()
		return nil, fmt.Errorf("decode Aurora config %q: %w", configPath, err)
	}
	settings.Auth, err = decodeLifecycleAuth(managedConfig)
	if err != nil {
		managedConfig.Close()
		return nil, err
	}
	settings.Attempt, err = decodeLifecycleAttempt(managedConfig)
	if err != nil {
		managedConfig.Close()
		return nil, err
	}
	settings.RateLimit, err = decodeLifecycleRateLimit(managedConfig)
	if err != nil {
		managedConfig.Close()
		return nil, err
	}

	ctx := context.Background()
	log, err := logger.New(&settings.Logger)
	if err != nil {
		managedConfig.Close()
		return nil, fmt.Errorf("initialize logger: %w", err)
	}

	var traceRuntime *tracing.Runtime
	if settings.Tracing != nil {
		settings.Tracing.Enabled = true
		traceRuntime, err = tracing.New(ctx, settings.Tracing)
		if err != nil {
			_ = log.Sync()
			managedConfig.Close()
			return nil, fmt.Errorf("initialize tracing: %w", err)
		}
	}

	var metricRuntime *metrics.Runtime
	if settings.Metrics != nil {
		settings.Metrics.Enabled = true
		metricRuntime, err = metrics.New(ctx, settings.Metrics)
		if err != nil {
			if traceRuntime != nil {
				_ = traceRuntime.Shutdown(ctx)
			}
			_ = log.Sync()
			managedConfig.Close()
			return nil, fmt.Errorf("initialize metrics: %w", err)
		}
	}

	var db *gorm.DB
	if settings.Database != nil {
		if settings.Database.Monitoring == nil {
			settings.Database.Monitoring = &database.MonitoringConfig{}
		}
		if traceRuntime != nil {
			settings.Database.Monitoring.TracerProvider = traceRuntime.TracerProvider()
		}
		if metricRuntime != nil {
			settings.Database.Monitoring.MeterProvider = metricRuntime.MeterProvider()
		}
		db, err = database.NewClientWithLogger(settings.Database, log)
		if err != nil {
			if metricRuntime != nil {
				_ = metricRuntime.Shutdown(ctx)
			}
			if traceRuntime != nil {
				_ = traceRuntime.Shutdown(ctx)
			}
			_ = log.Sync()
			managedConfig.Close()
			return nil, fmt.Errorf("initialize database: %w", err)
		}
	}

	var redisClient redis.UniversalClient
	if settings.Redis != nil {
		if settings.Redis.Monitoring == nil {
			settings.Redis.Monitoring = &coreRedis.MonitoringConfig{}
		}
		if traceRuntime != nil {
			settings.Redis.Monitoring.TracerProvider = traceRuntime.TracerProvider()
		}
		if metricRuntime != nil {
			settings.Redis.Monitoring.MeterProvider = metricRuntime.MeterProvider()
		}
		redisClient, err = coreRedis.NewClient(settings.Redis)
		if err != nil {
			_ = database.Close(db)
			if metricRuntime != nil {
				_ = metricRuntime.Shutdown(ctx)
			}
			if traceRuntime != nil {
				_ = traceRuntime.Shutdown(ctx)
			}
			_ = log.Sync()
			managedConfig.Close()
			return nil, fmt.Errorf("initialize redis: %w", err)
		}
	}

	authManagers, err := newLifecycleAuth(settings.Auth, redisClient)
	if err != nil {
		if redisClient != nil {
			_ = redisClient.Close()
		}
		_ = database.Close(db)
		if metricRuntime != nil {
			_ = metricRuntime.Shutdown(ctx)
		}
		if traceRuntime != nil {
			_ = traceRuntime.Shutdown(ctx)
		}
		_ = log.Sync()
		managedConfig.Close()
		return nil, err
	}

	attemptLimiters, err := newLifecycleAttempt(settings.Attempt, redisClient)
	if err != nil {
		if redisClient != nil {
			_ = redisClient.Close()
		}
		_ = database.Close(db)
		if metricRuntime != nil {
			_ = metricRuntime.Shutdown(ctx)
		}
		if traceRuntime != nil {
			_ = traceRuntime.Shutdown(ctx)
		}
		_ = log.Sync()
		managedConfig.Close()
		return nil, err
	}

	rateLimiters, err := newLifecycleRateLimit(settings.RateLimit, redisClient)
	if err != nil {
		if redisClient != nil {
			_ = redisClient.Close()
		}
		_ = database.Close(db)
		if metricRuntime != nil {
			_ = metricRuntime.Shutdown(ctx)
		}
		if traceRuntime != nil {
			_ = traceRuntime.Shutdown(ctx)
		}
		_ = log.Sync()
		managedConfig.Close()
		return nil, err
	}

	publisher, consumer, err := newLifecycleMQ(settings.MQ, redisClient)
	if err != nil {
		if redisClient != nil {
			_ = redisClient.Close()
		}
		_ = database.Close(db)
		if metricRuntime != nil {
			_ = metricRuntime.Shutdown(ctx)
		}
		if traceRuntime != nil {
			_ = traceRuntime.Shutdown(ctx)
		}
		_ = log.Sync()
		managedConfig.Close()
		return nil, fmt.Errorf("initialize mq: %w", err)
	}

	engineConfig := settings.Aurora
	engineOptions := []Option{WithLogger(log)}
	if traceRuntime != nil {
		engineOptions = append(engineOptions, WithTracingRuntime(traceRuntime))
	}
	if metricRuntime != nil {
		engineOptions = append(engineOptions, WithMetricsProvider(metricRuntime.MeterProvider()))
	}
	engine, err := New(engineConfig, engineOptions...)
	if err != nil {
		_ = closeMQ(ctx, publisher, consumer)
		if redisClient != nil {
			_ = redisClient.Close()
		}
		_ = database.Close(db)
		if metricRuntime != nil {
			_ = metricRuntime.Shutdown(ctx)
		}
		if traceRuntime != nil {
			_ = traceRuntime.Shutdown(ctx)
		}
		_ = log.Sync()
		managedConfig.Close()
		return nil, fmt.Errorf("initialize Aurora engine: %w", err)
	}

	return &Lifecycle{
		Engine:          engine,
		Config:          managedConfig,
		Logger:          log,
		Tracing:         traceRuntime,
		Metrics:         metricRuntime,
		Database:        db,
		Redis:           redisClient,
		authManagers:    authManagers,
		attemptLimiters: attemptLimiters,
		rateLimiters:    rateLimiters,
		MQPublisher:     publisher,
		MQConsumer:      consumer,
	}, nil
}

// Auth returns the named authentication manager. Names are matched exactly;
// configuration keys are lowercased by the configuration loader. The registry
// is immutable after construction and supports concurrent lookups.
func (l *Lifecycle) Auth(name string) (*auth.Manager, error) {
	if l == nil {
		return nil, errors.New("lifecycle is nil")
	}
	manager, ok := l.authManagers[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrAuthInstanceNotFound, name)
	}
	return manager, nil
}

// AuthNames returns a sorted copy of the registered authentication manager names.
// A nil lifecycle or an empty registry returns nil. Concurrent calls are safe.
func (l *Lifecycle) AuthNames() []string {
	if l == nil || len(l.authManagers) == 0 {
		return nil
	}
	names := make([]string, 0, len(l.authManagers))
	for name := range l.authManagers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Attempt returns the named attempt limiter. Names are matched exactly;
// configuration keys are lowercased by the configuration loader. The registry
// is immutable after construction and supports concurrent lookups.
func (l *Lifecycle) Attempt(name string) (*attempt.Limiter, error) {
	if l == nil {
		return nil, errors.New("lifecycle is nil")
	}
	limiter, ok := l.attemptLimiters[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrAttemptInstanceNotFound, name)
	}
	return limiter, nil
}

// AttemptNames returns a sorted copy of the registered attempt limiter names.
// A nil lifecycle or an empty registry returns nil. Concurrent calls are safe.
func (l *Lifecycle) AttemptNames() []string {
	if l == nil || len(l.attemptLimiters) == 0 {
		return nil
	}
	names := make([]string, 0, len(l.attemptLimiters))
	for name := range l.attemptLimiters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RateLimit returns the named rate limiter. Names are matched exactly;
// configuration keys are lowercased by the configuration loader. The registry
// is immutable after construction and supports concurrent lookups.
func (l *Lifecycle) RateLimit(name string) (*ratelimit.Limiter, error) {
	if l == nil {
		return nil, errors.New("lifecycle is nil")
	}
	limiter, ok := l.rateLimiters[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrRateLimitInstanceNotFound, name)
	}
	return limiter, nil
}

// RateLimitNames returns a sorted copy of the registered rate limiter names.
// A nil lifecycle or an empty registry returns nil. Concurrent calls are safe.
func (l *Lifecycle) RateLimitNames() []string {
	if l == nil || len(l.rateLimiters) == 0 {
		return nil
	}
	names := make([]string, 0, len(l.rateLimiters))
	for name := range l.rateLimiters {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Decode each instance independently so malformed fields cannot expose signing
// secrets in mapstructure's detailed conversion errors. Other configuration
// sections retain their existing decoding behavior.
func decodeLifecycleAuth(config *coreconfig.Config) (map[string]auth.Config, error) {
	raw := coreconfig.Get[any](config, "auth")
	if raw == nil {
		return nil, nil
	}
	instances, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("initialize auth: %w: expected a map of named instances", auth.ErrInvalidConfig)
	}
	names := make([]string, 0, len(instances))
	for name := range instances {
		names = append(names, name)
	}
	sort.Strings(names)
	configs := make(map[string]auth.Config, len(instances))
	for _, name := range names {
		if !validAuthInstanceName(name) {
			return nil, fmt.Errorf("initialize auth %q: %w: name must match [a-z][a-z0-9_-]{0,63}", name, auth.ErrInvalidConfig)
		}
		fields, ok := instances[name].(map[string]any)
		if !ok || len(fields) == 0 {
			return nil, fmt.Errorf("initialize auth %q: %w: expected a non-empty configuration map", name, auth.ErrInvalidConfig)
		}
		var cfg auth.Config
		decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
			Result:      &cfg,
			TagName:     "mapstructure",
			ErrorUnused: true,
			DecodeHook:  mapstructure.StringToTimeDurationHookFunc(),
		})
		if err != nil {
			return nil, fmt.Errorf("initialize auth %q: %w", name, err)
		}
		if err := decoder.Decode(fields); err != nil {
			return nil, fmt.Errorf("initialize auth %q: %w: invalid configuration fields or types", name, auth.ErrInvalidConfig)
		}
		configs[name] = cfg
	}
	return configs, nil
}

func validAuthInstanceName(name string) bool {
	if len(name) == 0 || len(name) > 64 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func newLifecycleAuth(configs map[string]auth.Config, client redis.UniversalClient) (map[string]*auth.Manager, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	if client == nil {
		return nil, errors.New("initialize auth: redis must be configured")
	}
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	managers := make(map[string]*auth.Manager, len(configs))
	prefixOwners := make(map[string]string, len(configs))
	for _, name := range names {
		cfg := configs[name]
		cfg.KeyPrefix = strings.TrimSpace(cfg.KeyPrefix)
		if cfg.KeyPrefix == "" {
			cfg.KeyPrefix = "aurora:auth:{" + name + "}"
		}
		if owner, exists := prefixOwners[cfg.KeyPrefix]; exists {
			return nil, fmt.Errorf("initialize auth %q: %w: key_prefix conflicts with instance %q", name, auth.ErrInvalidConfig, owner)
		}
		manager, err := auth.New(client, cfg)
		if err != nil {
			return nil, fmt.Errorf("initialize auth %q: %w", name, err)
		}
		prefixOwners[cfg.KeyPrefix] = name
		managers[name] = manager
	}
	return managers, nil
}

// Decode each attempt instance independently so malformed fields cannot expose
// HMAC keys in mapstructure's detailed conversion errors.
func decodeLifecycleAttempt(config *coreconfig.Config) (map[string]attempt.Config, error) {
	raw := coreconfig.Get[any](config, "attempt")
	if raw == nil {
		return nil, nil
	}
	instances, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("initialize attempt: %w: expected a map of named instances", attempt.ErrInvalidConfig)
	}
	names := make([]string, 0, len(instances))
	for name := range instances {
		names = append(names, name)
	}
	sort.Strings(names)
	configs := make(map[string]attempt.Config, len(instances))
	for _, name := range names {
		if !validAttemptInstanceName(name) {
			return nil, fmt.Errorf("initialize attempt %q: %w: name must match [a-z][a-z0-9_-]{0,63}", name, attempt.ErrInvalidConfig)
		}
		fields, ok := instances[name].(map[string]any)
		if !ok || len(fields) == 0 {
			return nil, fmt.Errorf("initialize attempt %q: %w: expected a non-empty configuration map", name, attempt.ErrInvalidConfig)
		}
		var cfg attempt.Config
		decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
			Result:      &cfg,
			TagName:     "mapstructure",
			ErrorUnused: true,
			DecodeHook:  mapstructure.StringToTimeDurationHookFunc(),
		})
		if err != nil {
			return nil, fmt.Errorf("initialize attempt %q: %w", name, err)
		}
		if err := decoder.Decode(fields); err != nil {
			return nil, fmt.Errorf("initialize attempt %q: %w: invalid configuration fields or types", name, attempt.ErrInvalidConfig)
		}
		configs[name] = cfg
	}
	return configs, nil
}

func validAttemptInstanceName(name string) bool {
	if len(name) == 0 || len(name) > 64 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func newLifecycleAttempt(configs map[string]attempt.Config, client redis.UniversalClient) (map[string]*attempt.Limiter, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	if client == nil {
		return nil, errors.New("initialize attempt: redis must be configured")
	}
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	limiters := make(map[string]*attempt.Limiter, len(configs))
	prefixOwners := make(map[string]string, len(configs))
	for _, name := range names {
		cfg := configs[name]
		cfg.KeyPrefix = strings.TrimSpace(cfg.KeyPrefix)
		if cfg.KeyPrefix == "" {
			cfg.KeyPrefix = "aurora:auth:attempt:{" + name + "}"
		}
		if owner, exists := prefixOwners[cfg.KeyPrefix]; exists {
			return nil, fmt.Errorf("initialize attempt %q: %w: key_prefix conflicts with instance %q", name, attempt.ErrInvalidConfig, owner)
		}
		limiter, err := attempt.New(client, cfg)
		if err != nil {
			return nil, fmt.Errorf("initialize attempt %q: %w", name, err)
		}
		prefixOwners[cfg.KeyPrefix] = name
		limiters[name] = limiter
	}
	return limiters, nil
}

// Decode each ratelimit instance independently so malformed fields cannot expose
// HMAC keys in mapstructure's detailed conversion errors.
func decodeLifecycleRateLimit(config *coreconfig.Config) (map[string]ratelimit.Config, error) {
	raw := coreconfig.Get[any](config, "ratelimit")
	if raw == nil {
		return nil, nil
	}
	instances, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("initialize ratelimit: %w: expected a map of named instances", ratelimit.ErrInvalidConfig)
	}
	names := make([]string, 0, len(instances))
	for name := range instances {
		names = append(names, name)
	}
	sort.Strings(names)
	configs := make(map[string]ratelimit.Config, len(instances))
	for _, name := range names {
		if !validRateLimitInstanceName(name) {
			return nil, fmt.Errorf("initialize ratelimit %q: %w: name must match [a-z][a-z0-9_-]{0,63}", name, ratelimit.ErrInvalidConfig)
		}
		fields, ok := instances[name].(map[string]any)
		if !ok || len(fields) == 0 {
			return nil, fmt.Errorf("initialize ratelimit %q: %w: expected a non-empty configuration map", name, ratelimit.ErrInvalidConfig)
		}
		var cfg ratelimit.Config
		decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
			Result:      &cfg,
			TagName:     "mapstructure",
			ErrorUnused: true,
			DecodeHook:  mapstructure.StringToTimeDurationHookFunc(),
		})
		if err != nil {
			return nil, fmt.Errorf("initialize ratelimit %q: %w", name, err)
		}
		if err := decoder.Decode(fields); err != nil {
			return nil, fmt.Errorf("initialize ratelimit %q: %w: invalid configuration fields or types", name, ratelimit.ErrInvalidConfig)
		}
		configs[name] = cfg
	}
	return configs, nil
}

func validRateLimitInstanceName(name string) bool {
	if len(name) == 0 || len(name) > 64 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func newLifecycleRateLimit(configs map[string]ratelimit.Config, client redis.UniversalClient) (map[string]*ratelimit.Limiter, error) {
	if len(configs) == 0 {
		return nil, nil
	}
	if client == nil {
		return nil, errors.New("initialize ratelimit: redis must be configured")
	}
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	limiters := make(map[string]*ratelimit.Limiter, len(configs))
	prefixOwners := make(map[string]string, len(configs))
	for _, name := range names {
		cfg := configs[name]
		cfg.KeyPrefix = strings.TrimSpace(cfg.KeyPrefix)
		if cfg.KeyPrefix == "" {
			cfg.KeyPrefix = "aurora:ratelimit:{" + name + "}"
		}
		if owner, exists := prefixOwners[cfg.KeyPrefix]; exists {
			return nil, fmt.Errorf("initialize ratelimit %q: %w: key_prefix conflicts with instance %q", name, ratelimit.ErrInvalidConfig, owner)
		}
		limiter, err := ratelimit.New(client, cfg)
		if err != nil {
			return nil, fmt.Errorf("initialize ratelimit %q: %w", name, err)
		}
		prefixOwners[cfg.KeyPrefix] = name
		limiters[name] = limiter
	}
	return limiters, nil
}

// Run starts the HTTP server and releases all lifecycle-owned resources when
// the server exits. Routes must be registered before calling Run.
func (l *Lifecycle) Run(ctx context.Context) error {
	if l == nil || l.Engine == nil {
		return errors.New("lifecycle is nil")
	}
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	runCtx, cancel, err := l.beginRun(ctx)
	if err != nil {
		return err
	}
	err = l.Engine.Run(runCtx)
	done := l.endRun(cancel)
	closeErr := l.closeAfterRun()
	close(done)
	return errors.Join(err, closeErr)
}

// Start starts the HTTP server and releases lifecycle-owned resources when it
// exits. It is the blocking equivalent of Engine.Start.
func (l *Lifecycle) Start() error {
	if l == nil || l.Engine == nil {
		return errors.New("lifecycle is nil")
	}
	runCtx, cancel, err := l.beginRun(context.Background())
	if err != nil {
		return err
	}
	err = l.Engine.start(runCtx, nil)
	done := l.endRun(cancel)
	closeErr := l.closeAfterRun()
	close(done)
	return errors.Join(err, closeErr)
}

// Serve serves HTTP traffic on listener and releases lifecycle-owned resources
// when the server exits.
func (l *Lifecycle) Serve(listener net.Listener) error {
	if l == nil || l.Engine == nil {
		return errors.New("lifecycle is nil")
	}
	runCtx, cancel, err := l.beginRun(context.Background())
	if err != nil {
		return err
	}
	err = l.Engine.serve(runCtx, listener)
	done := l.endRun(cancel)
	closeErr := l.closeAfterRun()
	close(done)
	return errors.Join(err, closeErr)
}

// Shutdown gracefully stops HTTP and then releases all resources created by
// NewLifecycle. It is safe to call repeatedly and concurrently.
func (l *Lifecycle) Shutdown(ctx context.Context) error {
	if l == nil || l.Engine == nil {
		return errors.New("lifecycle is nil")
	}
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	l.stateMu.Lock()
	if !l.closed {
		l.closed = true
	}
	running := l.running
	cancel := l.runCancel
	done := l.runDone
	l.stateMu.Unlock()
	if cancel != nil {
		cancel()
	}
	err := l.Engine.Shutdown(ctx)
	if running {
		// The active Run/Start/Serve call owns cleanup. Waiting for it to return
		// avoids closing dependencies while requests are still draining.
		select {
		case <-done:
			return errors.Join(err, l.closeResources(ctx))
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		}
	}
	return errors.Join(err, l.closeResources(ctx))
}

// Close is an alias for Shutdown for callers that manage lifecycle objects as
// ordinary closers.
func (l *Lifecycle) Close(ctx context.Context) error {
	return l.Shutdown(ctx)
}

func (l *Lifecycle) beginRun(ctx context.Context) (context.Context, context.CancelFunc, error) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.closed {
		return nil, nil, errors.New("lifecycle is closed")
	}
	if l.running {
		return nil, nil, errors.New("lifecycle already running")
	}
	runCtx, cancel := context.WithCancel(ctx)
	l.running = true
	l.runCancel = cancel
	l.runDone = make(chan struct{})
	return runCtx, cancel, nil
}

func (l *Lifecycle) endRun(cancel context.CancelFunc) chan struct{} {
	l.stateMu.Lock()
	done := l.runDone
	l.runCancel = nil
	l.runDone = nil
	l.running = false
	l.closed = true
	l.stateMu.Unlock()
	cancel()
	return done
}

func (l *Lifecycle) closeAfterRun() error {
	ctx := context.Background()
	if l != nil && l.Engine != nil && l.config.WriteTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, l.config.WriteTimeout)
		defer cancel()
	}
	return l.closeResources(ctx)
}

func (l *Lifecycle) closeResources(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	l.closeOnce.Do(func() {
		var errs []error
		// The reverse order mirrors dependency creation: MQ may borrow Redis,
		// database/Redis instrumentation may borrow tracing and metrics.
		if err := closeMQ(ctx, l.MQPublisher, l.MQConsumer); err != nil {
			errs = append(errs, fmt.Errorf("close mq: %w", err))
		}
		if l.Database != nil {
			if err := database.Close(l.Database); err != nil {
				errs = append(errs, fmt.Errorf("close database: %w", err))
			}
		}
		if l.Redis != nil {
			if err := l.Redis.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close redis: %w", err))
			}
		}
		if l.Metrics != nil {
			if err := l.Metrics.Shutdown(ctx); err != nil {
				errs = append(errs, fmt.Errorf("shutdown metrics: %w", err))
			}
		}
		if l.Tracing != nil {
			if err := l.Tracing.Shutdown(ctx); err != nil {
				errs = append(errs, fmt.Errorf("shutdown tracing: %w", err))
			}
		}
		if l.Logger != nil {
			if err := l.Logger.Sync(); err != nil {
				errs = append(errs, fmt.Errorf("sync logger: %w", err))
			}
		}
		if l.Config != nil {
			l.Config.Close()
		}
		l.closeErr = errors.Join(errs...)
	})
	return l.closeErr
}

func closeMQ(ctx context.Context, publisher mq.Publisher, consumer mq.Consumer) error {
	if publisher == nil && consumer == nil {
		return nil
	}
	// All bundled adapters implement both interfaces. Close once even though
	// both fields are exposed separately for callers that only need one side.
	if publisher != nil {
		return publisher.Close(ctx)
	}
	return consumer.Close(ctx)
}

func newLifecycleMQ(cfg *lifecycleMQ, redisClient redis.UniversalClient) (mq.Publisher, mq.Consumer, error) {
	if cfg == nil {
		return nil, nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Driver)) {
	case "redis":
		if redisClient == nil {
			return nil, nil, errors.New("redis MQ requires Redis to be configured")
		}
		cfg.Redis.Client = redisClient
		// Redis is owned by the lifecycle root, so the adapter must borrow it.
		cfg.Redis.OwnClient = false
		client, err := mqredis.NewClient(&cfg.Redis)
		if err != nil {
			return nil, nil, err
		}
		return client, client, nil
	case "rabbitmq", "rabbit":
		client, err := mqrabbitmq.NewClient(&cfg.RabbitMQ)
		if err != nil {
			return nil, nil, err
		}
		return client, client, nil
	case "kafka":
		client, err := mqkafka.NewClient(&cfg.Kafka)
		if err != nil {
			return nil, nil, err
		}
		return client, client, nil
	default:
		return nil, nil, fmt.Errorf("unsupported MQ driver %q", cfg.Driver)
	}
}

func lifecycleConfigPath(paths []string) (string, bool, error) {
	switch len(paths) {
	case 0:
		cwd, err := os.Getwd()
		if err != nil {
			return "", false, fmt.Errorf("resolve working directory: %w", err)
		}
		return filepath.Join(cwd, defaultLifecycleConfigPath), true, nil
	case 1:
		path := strings.TrimSpace(paths[0])
		if path == "" {
			return "", false, errors.New("config path cannot be empty")
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", false, fmt.Errorf("resolve config path: %w", err)
		}
		return absolute, false, nil
	default:
		return "", false, errors.New("NewLifecycle accepts at most one config path")
	}
}

func ensureDefaultLifecycleConfig(path string) error {
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return fmt.Errorf("default config path %q is a directory", path)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect default config %q: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil
		}
		return fmt.Errorf("create default config %q: %w", path, err)
	}
	if _, err := file.WriteString(defaultLifecycleYAML); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("write default config %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close default config %q: %w", path, err)
	}
	return nil
}

func defaultLifecycleConfig() lifecycleConfig {
	return lifecycleConfig{
		Aurora: DefaultConfig(),
		Logger: logger.Config{Level: logger.LevelInfo, Format: logger.FormatConsole, Outputs: []logger.Output{logger.OutputStdout}},
	}
}

const defaultLifecycleYAML = `# Aurora 应用启动配置文件
#
# 此文件仅在 config/aurora.yaml 不存在时自动生成。模板中的值均为安全的本地默认值：
# 外部资源（数据库、Redis、MQ、链路追踪、监控）默认禁用，需手动配置连接信息后启用。

aurora:
  addr: ":8080"                    # HTTP 服务监听地址
  mode: release                    # 运行模式: debug, release, test
  read_timeout: 15s                # 读取请求超时
  read_header_timeout: 5s          # 读取请求头超时
  write_timeout: 30s               # 写入响应超时
  idle_timeout: 60s                # 保持连接空闲超时
  max_header_bytes: 1048576        # 最大请求头大小 (1MB)
  max_body_bytes: 10485760         # 最大请求体大小 (10MB)
  trusted_proxies: []              # 可信代理列表，为空时信任所有代理
  middleware:
    request_id: true               # 请求 ID 中间件
    recovery: true                 # Panic 恢复中间件
    access_log: true               # 访问日志中间件
    tracing: false                 # 链路追踪中间件（需启用 tracing 配置）
    metrics: false                 # 指标采集中间件（需启用 metrics 配置）
    auth:                          # 认证中间件（需启用 auth 配置）
      enabled: false               # 是否启用认证中间件
      instance: ""                 # 使用的 auth 实例名称（如 "admin"）
      skip_paths: []               # 跳过认证的路径列表
    ratelimit:                     # 限流中间件（需启用 ratelimit 配置）
      enabled: false               # 是否启用限流中间件
      instance: ""                 # 使用的 ratelimit 实例名称（如 "api"）
      skip_paths: []               # 跳过限流的路径列表

# 可选的链路追踪配置。取消注释整个块以启用。
# tracing:
#   service_name: aurora
#   service_version: "0.0.4"
#   environment: development
#   endpoint: "localhost:4317"
#   protocol: grpc
#   insecure: true
#   sample_ratio: 1
#   headers: {}
#   batch:
#     max_queue_size: 2048
#     max_export_batch_size: 512
#     schedule_delay: 5s
#     export_timeout: 30s
#   resource:
#     service_namespace: aurora
#     deployment_environment: development

# Optional metrics configuration. Uncomment the whole block to enable it.
# metrics:
#   service_name: aurora
#   service_version: "0.0.4"
#   environment: development
#   endpoint: "localhost:4317"
#   protocol: grpc
#   insecure: true
#   headers: {}
#   export_interval: 15s
#   export_timeout: 10s
#   resource:
#     service_namespace: aurora
#     deployment_environment: development

logger:
  level: info
  format: console
  outputs: [stdout]
  error_output: stderr
  development: false
  add_caller: true
  caller_skip: 0
  stacktrace_level: error
  initial_fields:
    service: aurora
    environment: development
    version: "0.0.4"
  encoder:
    message_key: msg
    level_key: level
    time_key: ts
    name_key: logger
    caller_key: caller
    function_key: ""
    stacktrace_key: stacktrace
    time_encoding: iso8601
    duration_encoding: string
    level_encoding: lowercase
  # Rotation is only needed when outputs contains file.
  # rotation:
  #   max_size: 100
  #   max_age: 7
  #   max_backups: 10
  #   local_time: false
  #   compress: true
  # sampling:
  #   enabled: true
  #   initial: 100
  #   thereafter: 100

# Optional database configuration. Uncomment the whole block to enable it.
# database:
#   driver: sqlite
#   dsn: "./data/aurora.db"
#   pool:
#     max_open_conns: 20
#     max_idle_conns: 5
#     conn_max_lifetime: 1h
#     conn_max_idle_time: 10m
#   gorm:
#     skip_default_transaction: false
#     dry_run: false
#     prepare_stmt: true
#     disable_nested_transaction: false
#     allow_global_update: false
#     disable_automatic_ping: false
#   naming:
#     table_prefix: ""
#     singular_table: false
#     no_lower_case: false
#     identifier_max_length: 0
#   logger:
#     level: warn
#     log_sql: false
#     slow_threshold: 200ms
#     ignore_record_not_found_error: true
#     parameterized_queries: true
#     colorful: false
#   migration:
#     disable_foreign_key_constraint_when_migrating: false
#   monitoring:
#     tracing_enabled: false
#     metrics_enabled: false
#   resolver:
#     sources: []
#     replicas: []
#     policy: random
#     trace_resolver_mode: false

# Optional Redis configuration. Uncomment the whole block to enable it.
# redis:
#   mode: standalone
#   addrs: ["127.0.0.1:6379"]
#   network: tcp
#   db: 0
#   username: ""
#   password: ""
#   client_name: aurora
#   protocol: 3
#   disable_identity: false
#   identity_suffix: ""
#   timeout:
#     dial_timeout: 5s
#     dialer_retries: 5
#     dialer_retry_timeout: 5s
#     read_timeout: 3s
#     write_timeout: 3s
#     pool_timeout: 4s
#     context_timeout_enabled: true
#   retry:
#     max_retries: 3
#     min_retry_backoff: 8ms
#     max_retry_backoff: 512ms
#   pool:
#     fifo: false
#     size: 20
#     max_concurrent_dials: 0
#     min_idle_conns: 2
#     max_idle_conns: 10
#     max_active_conns: 0
#     conn_max_idle_time: 30m
#     conn_max_lifetime: 0s
#     conn_max_lifetime_jitter: 0s
#   buffer:
#     read_buffer_size: 0
#     write_buffer_size: 0
#   tls:
#     enabled: false
#     server_name: ""
#     insecure_skip_verify: false
#     ca_file: ""
#     cert_file: ""
#     key_file: ""
#   monitoring:
#     tracing_enabled: false
#     db_statement_enabled: false
#     caller_enabled: false
#     dial_enabled: false
#     metrics_enabled: false
#   # Sentinel alternative: change mode and replace addrs with sentinel nodes.
#   # sentinel:
#   #   master_name: primary
#   #   username: ""
#   #   password: ""
#   # Cluster alternative: change mode and use multiple data node addresses.
#   # cluster:
#   #   max_redirects: 3
#   #   read_only: false
#   #   route_by_latency: false
#   #   route_randomly: false
#   #   failing_timeout_seconds: 15
#   #   cluster_state_reload_interval: 0s

# Optional auth configuration. Uncomment the whole block to enable JWT/Redis
# session management. Keep the signing secret protected with the config file.
# auth:
#   admin:
#     key_prefix: aurora:auth:{admin}
#     signing_secret: "replace-with-at-least-32-random-bytes"
#     issuer: aurora-admin
#     audience: [admin-client]
#     access_ttl: 15m
#     refresh_ttl: 168h
#     max_session_ttl: 720h
#     clock_skew: 0s
#     revoke_batch_size: 100
#     refresh_compensation_timeout: 5s
#     policy:
#       max_sessions: 0
#       default_platform_policy:
#         enabled: true
#         max_sessions: 0
#         require_device_id: false
#         kickout_strategy: oldest
#         exclusive_with: []
#       platform_policies: {}
#   user:
#     key_prefix: aurora:auth:{user}
#     signing_secret: "replace-with-a-different-32-byte-random-secret"
#     issuer: aurora-user
#     audience: [user-client]
#     access_ttl: 30m
#     refresh_ttl: 168h
#     max_session_ttl: 720h

# Optional attempt limiter configuration. Uncomment the whole block to enable
# login failure tracking and account lockout. Requires Redis to be configured.
# attempt:
#   admin:
#     key_prefix: aurora:auth:attempt:{admin}
#     hmac_key: "replace-with-at-least-32-random-bytes"
#     failure_limit: 5
#     window: 10m
#     lock_duration: 15m
#     attempt_ttl: 10m
#   user:
#     key_prefix: aurora:auth:attempt:{user}
#     hmac_key: "replace-with-a-different-32-byte-random-secret"
#     failure_limit: 3
#     window: 5m
#     lock_duration: 10m
#     attempt_ttl: 5m

# Optional rate limiter configuration. Uncomment the whole block to enable
# distributed rate limiting. Requires Redis to be configured.
# ratelimit:
#   api:
#     key_prefix: aurora:ratelimit:{api}
#     hmac_key: "replace-with-at-least-32-random-bytes"
#     limit: 100
#     window: 1m
#   strict:
#     key_prefix: aurora:ratelimit:{strict}
#     hmac_key: "replace-with-a-different-32-byte-random-secret"
#     limit: 10
#     window: 1m

# Optional MQ configuration. Uncomment the whole block to enable one adapter.
# mq:
#   driver: redis
#   redis:
#     own_client: false
#     group: aurora
#     consumer: aurora
#     start_id: "0-0"
#     read_count: 10
#     block: 5s
#     claim_idle: 30s
#     disable_claim: false
#     max_payload: 10485760
#   rabbitmq:
#     url: "amqp://user:password@rabbitmq:5672/aurora"
#     declare_topology: true
#     publisher:
#       confirm: true
#       mandatory: true
#       timeout: 30s
#     consumer:
#       prefetch: 10
#       concurrency: 1
#       consumer_tag: aurora
#       shutdown_timeout: 30s
#       reconnect_attempts: 3
#       reconnect_backoff: 250ms
#     failure:
#       mode: reject
#       allow_drop: false
#     dead_letter:
#       exchange: aurora.dlx
#       routing_key: ""
#     max_payload: 10485760
#     tls:
#       ca_file: /etc/aurora/rabbitmq/ca.pem
#       cert_file: /etc/aurora/rabbitmq/client.pem
#       key_file: /etc/aurora/rabbitmq/client-key.pem
#       server_name: rabbitmq
#       insecure_skip_verify: false
#     routes:
#       orders:
#         exchange: aurora.orders
#         exchange_type: topic
#         routing_key: order.created
#         queue: aurora.orders.worker
#         durable: true
#         auto_delete: false
#         exclusive: false
#         exchange_arguments: {}
#         queue_arguments: {}
#         bindings:
#           - queue: aurora.orders.worker
#             routing_key: order.created
#             arguments: {}
#   kafka:
#     brokers: ["kafka:9092"]
#     client_id: aurora
#     max_payload: 10485760
#     routes:
#       orders:
#         topic: aurora.orders.v1
#     producer:
#       timeout: 30s
#       max_retries: 5
#     tls:
#       ca_file: /etc/aurora/kafka/ca.pem
#       cert_file: /etc/aurora/kafka/client.pem
#       key_file: /etc/aurora/kafka/client-key.pem
#       server_name: kafka
#       insecure_skip_verify: false
#     sasl:
#       mechanism: scram_sha_512
#       username: kafka-user
#       password: replace-me
#     consumer:
#       group_id: aurora
#       concurrency: 4
#       start_offset: earliest
#       session_timeout: 45s
#       rebalance_timeout: 60s
#       retry:
#         max_attempts: 3
#         backoff: 500ms
#       failure:
#         mode: stop
#         dead_letter_route: ""
#         allow_drop: false
`
