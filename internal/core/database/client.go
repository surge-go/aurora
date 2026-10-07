package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/surge-go/aurora/internal/core/logger"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/driver/sqlserver"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
	"gorm.io/plugin/dbresolver"
	"gorm.io/plugin/opentelemetry/tracing"
)

// NewClient 根据 Config 创建 GORM 数据库客户端。
//
// 未注入日志实例时使用 logger.Nop，避免基础设施包隐式创建全局输出；应用需要输出
// 数据库错误和慢查询时，应使用 NewClientWithLogger 传入已初始化的 logger.Logger。
func NewClient(cfg *Config) (*gorm.DB, error) {
	return newClient(cfg, logger.Nop().Slog())
}

// NewClientWithLogger 使用 Aurora logger 包创建 GORM 数据库客户端。
//
// logger.Logger.Slog 会复用 logger 包的输出、字段和 trace context。log 为 nil 时
// 使用丢弃 logger，不会输出日志，也不会 panic。
func NewClientWithLogger(cfg *Config, log *logger.Logger) (*gorm.DB, error) {
	if log == nil {
		return newClient(cfg, logger.Nop().Slog())
	}
	return newClient(cfg, log.Slog())
}

// NewClientWithSlog 保留标准库 slog 适配入口，适合已经持有 *slog.Logger 的基础设施。
func NewClientWithSlog(cfg *Config, log *slog.Logger) (*gorm.DB, error) {
	return newClient(cfg, log)
}

// Close 关闭客户端创建的全部底层数据库连接和指标回调，包括 dbresolver 注册的
// sources 和 replicas。启用 Resolver 或数据库指标时必须使用该函数，否则直接
// 关闭底层 sql.DB 会留下资源跟踪状态。
func Close(db *gorm.DB) error {
	if db == nil {
		return nil
	}

	if value, ok := clients.LoadAndDelete(db); ok {
		return value.(*clientResources).close()
	}
	return closeDB(db)
}

var clients sync.Map // map[*gorm.DB]*clientResources

func newClient(cfg *Config, log *slog.Logger) (*gorm.DB, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	resources := &clientResources{}
	db, err := gorm.Open(newDialector(cfg.Driver, cfg.DSN), buildGORMConfig(cfg, log))
	if err != nil {
		return nil, err
	}
	resources.addDB(db, "primary")

	if err := applyPoolConfig(db, cfg.Pool); err != nil {
		_ = resources.close()
		return nil, err
	}
	if err := applyResolverConfig(db, cfg, resources); err != nil {
		_ = resources.close()
		return nil, err
	}
	if err := instrumentClient(db, cfg, resources); err != nil {
		_ = resources.close()
		return nil, err
	}

	// Keep one resource owner for every client so Close can release both
	// connection pools and metric callbacks, including non-resolver clients.
	clients.Store(db, resources)
	return db, nil
}

// newDialector 根据驱动类型构造 GORM dialector。
func newDialector(driver Driver, dsn string) gorm.Dialector {
	switch driver {
	case DriverMySQL:
		return mysql.Open(dsn)
	case DriverPostgres:
		return postgres.Open(dsn)
	case DriverSQLite:
		return sqlite.Open(dsn)
	case DriverSQLServer:
		return sqlserver.Open(dsn)
	default:
		return nil
	}
}

// buildGORMConfig 将配置文件字段映射为 gorm.Config。
func buildGORMConfig(cfg *Config, log *slog.Logger) *gorm.Config {
	// 统一转换各数据库驱动错误，便于业务层通过 errors.Is 判断重复键等标准错误。
	opt := &gorm.Config{TranslateError: true}

	if cfg.GORM != nil {
		opt.SkipDefaultTransaction = cfg.GORM.SkipDefaultTransaction
		opt.DryRun = cfg.GORM.DryRun
		opt.PrepareStmt = cfg.GORM.PrepareStmt
		opt.DisableNestedTransaction = cfg.GORM.DisableNestedTransaction
		opt.AllowGlobalUpdate = cfg.GORM.AllowGlobalUpdate
		opt.DisableAutomaticPing = cfg.GORM.DisableAutomaticPing
	}
	if cfg.Naming != nil {
		opt.NamingStrategy = schema.NamingStrategy{
			TablePrefix:         cfg.Naming.TablePrefix,
			SingularTable:       cfg.Naming.SingularTable,
			NoLowerCase:         cfg.Naming.NoLowerCase,
			IdentifierMaxLength: cfg.Naming.IdentifierMaxLength,
		}
	}
	loggerConfig := cfg.Logger
	if loggerConfig == nil {
		loggerConfig = &LoggerConfig{
			Level:                LogLevelWarn,
			ParameterizedQueries: true,
		}
	}
	opt.Logger = buildLogger(loggerConfig, log)
	if cfg.Migration != nil {
		opt.DisableForeignKeyConstraintWhenMigrating = cfg.Migration.DisableForeignKeyConstraintWhenMigrating
	}

	return opt
}

// applyPoolConfig 设置底层 database/sql 连接池。
func applyPoolConfig(db *gorm.DB, cfg *PoolConfig) error {
	if cfg == nil {
		return nil
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("database get sql db failed: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	return nil
}

// applyResolverConfig 注册 GORM dbresolver 插件。
func applyResolverConfig(db *gorm.DB, cfg *Config, resources *clientResources) error {
	if cfg.Resolver == nil {
		return nil
	}

	resolver := dbresolver.Register(dbresolver.Config{
		Sources:           buildDialectors(cfg.Driver, cfg.Resolver.Sources, resources, "source"),
		Replicas:          buildDialectors(cfg.Driver, cfg.Resolver.Replicas, resources, "replica"),
		Policy:            buildResolverPolicy(cfg.Resolver.Policy),
		TraceResolverMode: cfg.Resolver.TraceResolverMode,
	})

	if cfg.Pool != nil {
		resolver.
			SetMaxOpenConns(cfg.Pool.MaxOpenConns).
			SetMaxIdleConns(cfg.Pool.MaxIdleConns).
			SetConnMaxLifetime(cfg.Pool.ConnMaxLifetime).
			SetConnMaxIdleTime(cfg.Pool.ConnMaxIdleTime)
	}

	if err := db.Use(resolver); err != nil {
		return fmt.Errorf("database register dbresolver failed: %w", err)
	}
	if err := registerResolverCallbacks(db); err != nil {
		return fmt.Errorf("database register resolver callbacks failed: %w", err)
	}
	return nil
}

func buildDialectors(driver Driver, dsns []string, resources *clientResources, role string) []gorm.Dialector {
	if len(dsns) == 0 {
		return nil
	}

	dialectors := make([]gorm.Dialector, 0, len(dsns))
	for i, dsn := range dsns {
		dialectors = append(dialectors, trackedDialector{
			Dialector: newDialector(driver, dsn),
			resources: resources,
			label:     fmt.Sprintf("%s-%d", role, i),
		})
	}
	return dialectors
}

func buildResolverPolicy(policy ResolverPolicy) dbresolver.Policy {
	switch policy {
	case ResolverPolicyRandom, "":
		return dbresolver.RandomPolicy{}
	default:
		return dbresolver.RandomPolicy{}
	}
}

// instrumentClient 注册数据库链路追踪和指标采集插件。
func instrumentClient(db *gorm.DB, cfg *Config, resources *clientResources) error {
	if cfg.Monitoring == nil {
		return nil
	}

	if cfg.Monitoring.TracingEnabled {
		opts := []tracing.Option{
			tracing.WithDBSystem(string(cfg.Driver)),
			tracing.WithoutQueryVariables(),
			tracing.WithoutMetrics(),
		}
		if cfg.Monitoring.TracerProvider != nil {
			opts = append(opts, tracing.WithTracerProvider(cfg.Monitoring.TracerProvider))
		}
		if err := registerTracingPlugin(db, opts...); err != nil {
			return err
		}
	}

	if cfg.Monitoring.MetricsEnabled {
		if err := resources.reportMetrics(cfg.Monitoring.MeterProvider); err != nil {
			return err
		}
	}
	return nil
}

func registerTracingPlugin(db *gorm.DB, opts ...tracing.Option) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("database register tracing plugin failed: %v", recovered)
		}
	}()
	if err := db.Use(tracing.NewPlugin(opts...)); err != nil {
		return fmt.Errorf("database register tracing plugin failed: %w", err)
	}
	return nil
}

func reportDBStatsMetrics(db *sql.DB, provider metric.MeterProvider, label string) (registration metric.Registration, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("database register metrics failed: %v", recovered)
			registration = nil
		}
	}()
	if provider == nil {
		provider = otel.GetMeterProvider()
	}
	meter := provider.Meter("github.com/surge-go/aurora/internal/core/database")
	maxOpenConns, err := meter.Int64ObservableGauge("go.sql.connections_max_open")
	if err != nil {
		return nil, err
	}
	openConns, err := meter.Int64ObservableGauge("go.sql.connections_open")
	if err != nil {
		return nil, err
	}
	inUseConns, err := meter.Int64ObservableGauge("go.sql.connections_in_use")
	if err != nil {
		return nil, err
	}
	idleConns, err := meter.Int64ObservableGauge("go.sql.connections_idle")
	if err != nil {
		return nil, err
	}
	connsWaitCount, err := meter.Int64ObservableCounter("go.sql.connections_wait_count")
	if err != nil {
		return nil, err
	}
	connsWaitDuration, err := meter.Int64ObservableCounter("go.sql.connections_wait_duration")
	if err != nil {
		return nil, err
	}
	connsClosedMaxIdle, err := meter.Int64ObservableCounter("go.sql.connections_closed_max_idle")
	if err != nil {
		return nil, err
	}
	connsClosedMaxIdleTime, err := meter.Int64ObservableCounter("go.sql.connections_closed_max_idle_time")
	if err != nil {
		return nil, err
	}
	connsClosedMaxLifetime, err := meter.Int64ObservableCounter("go.sql.connections_closed_max_lifetime")
	if err != nil {
		return nil, err
	}
	registration, err = meter.RegisterCallback(func(_ context.Context, observer metric.Observer) error {
		stats := db.Stats()
		option := metric.WithAttributes(attribute.String("db.pool", label))
		observer.ObserveInt64(maxOpenConns, int64(stats.MaxOpenConnections), option)
		observer.ObserveInt64(openConns, int64(stats.OpenConnections), option)
		observer.ObserveInt64(inUseConns, int64(stats.InUse), option)
		observer.ObserveInt64(idleConns, int64(stats.Idle), option)
		observer.ObserveInt64(connsWaitCount, stats.WaitCount, option)
		observer.ObserveInt64(connsWaitDuration, int64(stats.WaitDuration), option)
		observer.ObserveInt64(connsClosedMaxIdle, stats.MaxIdleClosed, option)
		observer.ObserveInt64(connsClosedMaxIdleTime, stats.MaxIdleTimeClosed, option)
		observer.ObserveInt64(connsClosedMaxLifetime, stats.MaxLifetimeClosed, option)
		return nil
	}, maxOpenConns, openConns, inUseConns, idleConns, connsWaitCount, connsWaitDuration, connsClosedMaxIdle, connsClosedMaxIdleTime, connsClosedMaxLifetime)
	return registration, err
}

func closeDB(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

type clientResources struct {
	mu  sync.Mutex
	dbs []dbResource
}

type dbResource struct {
	db           *sql.DB
	label        string
	registration metric.Registration
}

func (r *clientResources) addDB(db *gorm.DB, label string) {
	if db == nil {
		return
	}
	if sqlDB, err := db.DB(); err == nil {
		r.addSQLDB(sqlDB, label)
	}
}

func (r *clientResources) addSQLDB(db *sql.DB, label string) {
	if db == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.dbs {
		if existing.db == db {
			return
		}
	}
	r.dbs = append(r.dbs, dbResource{db: db, label: label})
}

func (r *clientResources) reportMetrics(provider metric.MeterProvider) error {
	r.mu.Lock()
	dbs := append([]dbResource(nil), r.dbs...)
	r.mu.Unlock()
	for _, db := range dbs {
		registration, err := reportDBStatsMetrics(db.db, provider, db.label)
		if err != nil {
			return err
		}
		r.mu.Lock()
		for i := range r.dbs {
			if r.dbs[i].db == db.db {
				if r.dbs[i].registration != nil {
					_ = r.dbs[i].registration.Unregister()
				}
				r.dbs[i].registration = registration
				break
			}
		}
		r.mu.Unlock()
	}
	return nil
}

func (r *clientResources) close() error {
	r.mu.Lock()
	dbs := append([]dbResource(nil), r.dbs...)
	r.dbs = nil
	r.mu.Unlock()

	var errs []error
	for _, db := range dbs {
		if db.registration != nil {
			if err := db.registration.Unregister(); err != nil {
				errs = append(errs, err)
			}
		}
		if err := db.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

type trackedDialector struct {
	gorm.Dialector
	resources *clientResources
	label     string
}

func (d trackedDialector) Apply(config *gorm.Config) error {
	if apply, ok := d.Dialector.(interface{ Apply(*gorm.Config) error }); ok {
		return apply.Apply(config)
	}
	return nil
}

func (d trackedDialector) Initialize(db *gorm.DB) (err error) {
	defer func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			d.resources.addSQLDB(sqlDB, d.label)
		}
	}()
	return d.Dialector.Initialize(db)
}
