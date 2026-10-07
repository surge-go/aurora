package database

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Driver 表示数据库驱动类型。
type Driver string

const (
	// DriverMySQL 表示 MySQL 或兼容 MySQL 协议的数据库。
	DriverMySQL Driver = "mysql"

	// DriverPostgres 表示 PostgreSQL。
	DriverPostgres Driver = "postgres"

	// DriverSQLite 表示 SQLite，常用于本地开发、测试或轻量单机场景。
	DriverSQLite Driver = "sqlite"

	// DriverSQLServer 表示 Microsoft SQL Server。
	DriverSQLServer Driver = "sqlserver"
)

// Config 表示 GORM 数据库配置。
//
// 该结构体面向“应用配置文件”设计，只保留字符串、数字、布尔值、Duration 等
// 可序列化字段，不包含 logger.Interface、schema.Namer、NowFunc 等运行时代码钩子。
//
// 常规项目通常只需要配置 Driver、DSN、Pool 和 Logger。GORM、Naming、Migration、
// Monitoring、Resolver 等字段属于按需配置：需要调优 GORM 行为、统一表名规则、
// 控制迁移约束、接入监控或启用读写分离时再使用。
//
// 配置组字段使用指针是为了区分“整组未配置”和“整组参与覆盖”。例如 Pool 为 nil
// 表示使用 database/sql 默认连接池行为；Pool 非 nil 时，组内字段会按 database/sql
// 或 GORM 对应字段的语义传递，字段零值通常表示使用库默认值。
type Config struct {
	// Driver 指定数据库驱动。常用字段。
	// 当前包建议支持 mysql、postgres、sqlite、sqlserver。
	Driver Driver `json:"driver" yaml:"driver" mapstructure:"driver"`

	// DSN 是数据库连接字符串。常用字段。
	// 格式由具体驱动决定，例如 MySQL DSN、PostgreSQL URL/DSN、SQLite 文件路径等。
	DSN string `json:"dsn" yaml:"dsn" mapstructure:"dsn"`

	// Pool 是 database/sql 连接池配置。常用配置组。
	// nil 表示使用 database/sql 默认连接池行为。
	Pool *PoolConfig `json:"pool" yaml:"pool" mapstructure:"pool"`

	// GORM 是 GORM 初始化行为配置。按需配置组。
	// nil 表示除数据库包固定启用的标准错误转换外，使用 GORM 默认初始化行为。
	GORM *GORMConfig `json:"gorm" yaml:"gorm" mapstructure:"gorm"`

	// Naming 是 GORM 命名策略配置。按需配置组。
	// nil 表示使用 GORM 默认命名策略。
	Naming *NamingConfig `json:"naming" yaml:"naming" mapstructure:"naming"`

	// Logger 是 GORM 日志配置。常用配置组。
	// nil 表示使用本包基于 slog 的安全默认 logger：记录错误和慢查询，
	// 不输出普通 SQL，慢查询和错误中的 SQL 文本也会被隐藏。
	Logger *LoggerConfig `json:"logger" yaml:"logger" mapstructure:"logger"`

	// Migration 是迁移相关配置。按需配置组。
	// nil 表示不改变 GORM 默认迁移行为。
	Migration *MigrationConfig `json:"migration" yaml:"migration" mapstructure:"migration"`

	// Monitoring 是数据库监控配置。按需配置组。
	// nil 表示不启用数据库链路追踪和指标采集。
	Monitoring *MonitoringConfig `json:"monitoring" yaml:"monitoring" mapstructure:"monitoring"`

	// Resolver 是读写分离、主从和多数据源路由配置。进阶配置组。
	// nil 表示不启用 GORM dbresolver 插件。
	Resolver *ResolverConfig `json:"resolver" yaml:"resolver" mapstructure:"resolver"`
}

// Validate 校验数据库配置是否满足创建 GORM DB 的基本要求。
//
// 该方法只做确定性的静态配置校验，不会打开网络连接，也不会检查数据库是否真实可达。
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("database config is nil")
	}

	var errs []error
	if !isValidDriver(c.Driver) {
		errs = append(errs, fmt.Errorf("database driver must be one of %q, %q, %q, %q", DriverMySQL, DriverPostgres, DriverSQLite, DriverSQLServer))
	}
	if strings.TrimSpace(c.DSN) == "" {
		errs = append(errs, errors.New("database dsn must not be empty"))
	}
	if c.Pool != nil {
		errs = append(errs, c.Pool.validate()...)
	}
	if c.GORM != nil {
		errs = append(errs, c.GORM.validate()...)
	}
	if c.Naming != nil {
		errs = append(errs, c.Naming.validate()...)
	}
	if c.Logger != nil {
		errs = append(errs, c.Logger.validate()...)
	}
	if c.Migration != nil {
		errs = append(errs, c.Migration.validate()...)
	}
	if c.Monitoring != nil {
		errs = append(errs, c.Monitoring.validate()...)
	}
	if c.Resolver != nil {
		errs = append(errs, c.Resolver.validate()...)
	}
	return errors.Join(errs...)
}

// isValidDriver 校验当前包计划支持的 GORM driver。
func isValidDriver(driver Driver) bool {
	switch driver {
	case DriverMySQL, DriverPostgres, DriverSQLite, DriverSQLServer:
		return true
	default:
		return false
	}
}

// PoolConfig 表示 database/sql 连接池配置。
//
// 这组字段属于生产服务常用配置。多数项目重点关注 MaxOpenConns、MaxIdleConns、
// ConnMaxLifetime 和 ConnMaxIdleTime，避免连接无限增长或长期复用老连接。
type PoolConfig struct {
	// MaxOpenConns 设置打开到数据库的最大连接数。
	// 0 表示不限制，生产环境通常建议显式设置。
	MaxOpenConns int `json:"max_open_conns" yaml:"max_open_conns" mapstructure:"max_open_conns"`

	// MaxIdleConns 设置连接池中最大空闲连接数。
	// 0 表示不保留空闲连接。
	MaxIdleConns int `json:"max_idle_conns" yaml:"max_idle_conns" mapstructure:"max_idle_conns"`

	// ConnMaxLifetime 设置连接可复用的最长时间。
	// 常用于配合 MySQL wait_timeout、负载均衡或数据库代理连接回收策略。
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime" yaml:"conn_max_lifetime" mapstructure:"conn_max_lifetime"`

	// ConnMaxIdleTime 设置连接保持空闲的最长时间。
	// 用于回收低峰期长时间不用的连接。
	ConnMaxIdleTime time.Duration `json:"conn_max_idle_time" yaml:"conn_max_idle_time" mapstructure:"conn_max_idle_time"`
}

// validate 校验连接池配置。连接数量和生命周期不能为负数。
func (c *PoolConfig) validate() []error {
	var errs []error
	if c.MaxOpenConns < 0 {
		errs = append(errs, errors.New("database pool.max_open_conns must be greater than or equal to 0"))
	}
	if c.MaxIdleConns < 0 {
		errs = append(errs, errors.New("database pool.max_idle_conns must be greater than or equal to 0"))
	}
	if c.MaxOpenConns > 0 && c.MaxIdleConns > c.MaxOpenConns {
		errs = append(errs, errors.New("database pool.max_idle_conns must be less than or equal to max_open_conns"))
	}
	if c.ConnMaxLifetime < 0 {
		errs = append(errs, errors.New("database pool.conn_max_lifetime must be greater than or equal to 0"))
	}
	if c.ConnMaxIdleTime < 0 {
		errs = append(errs, errors.New("database pool.conn_max_idle_time must be greater than or equal to 0"))
	}
	return errs
}

// GORMConfig 表示 gorm.Config 中适合配置文件表达的常用字段。
//
// 不包含 NamingStrategy、Logger、NowFunc 等运行时代码对象；这些能力分别由
// NamingConfig、LoggerConfig 或数据库客户端初始化逻辑负责。
type GORMConfig struct {
	// SkipDefaultTransaction 禁用 GORM 默认写事务。
	// 开启后写入性能更高，但 create/update/delete 不再自动包裹事务。
	SkipDefaultTransaction bool `json:"skip_default_transaction" yaml:"skip_default_transaction" mapstructure:"skip_default_transaction"`

	// DryRun 只生成 SQL 不执行，通常用于调试或测试 SQL 生成结果。
	DryRun bool `json:"dry_run" yaml:"dry_run" mapstructure:"dry_run"`

	// PrepareStmt 启用 prepared statement 缓存，可提升重复 SQL 性能，但会增加资源占用。
	PrepareStmt bool `json:"prepare_stmt" yaml:"prepare_stmt" mapstructure:"prepare_stmt"`

	// DisableNestedTransaction 禁用嵌套事务中的 SavePoint/RollbackTo。
	DisableNestedTransaction bool `json:"disable_nested_transaction" yaml:"disable_nested_transaction" mapstructure:"disable_nested_transaction"`

	// AllowGlobalUpdate 允许无 where 条件的全表 update/delete。
	// 生产环境通常不建议开启。
	AllowGlobalUpdate bool `json:"allow_global_update" yaml:"allow_global_update" mapstructure:"allow_global_update"`

	// DisableAutomaticPing 禁用 GORM 初始化后的自动 Ping。
	// 如果应用希望延迟连接或把可用性检查交给健康检查，可以开启。
	DisableAutomaticPing bool `json:"disable_automatic_ping" yaml:"disable_automatic_ping" mapstructure:"disable_automatic_ping"`
}

// validate 预留 GORM 初始化行为配置校验入口。
func (c *GORMConfig) validate() []error {
	return nil
}

// NamingConfig 表示 GORM schema.NamingStrategy 中常用的可配置字段。
type NamingConfig struct {
	// TablePrefix 为表名增加统一前缀，例如 t_。
	TablePrefix string `json:"table_prefix" yaml:"table_prefix" mapstructure:"table_prefix"`

	// SingularTable 使用单数表名，User 对应 user，而不是 users。
	SingularTable bool `json:"singular_table" yaml:"singular_table" mapstructure:"singular_table"`

	// NoLowerCase 禁用默认的小写转换。
	NoLowerCase bool `json:"no_lower_case" yaml:"no_lower_case" mapstructure:"no_lower_case"`

	// IdentifierMaxLength 限制索引、约束等标识符最大长度。
	// 0 表示使用 GORM 默认值；不同数据库对标识符长度限制不同。
	IdentifierMaxLength int `json:"identifier_max_length" yaml:"identifier_max_length" mapstructure:"identifier_max_length"`
}

// validate 校验命名策略配置。
func (c *NamingConfig) validate() []error {
	var errs []error
	if c.IdentifierMaxLength < 0 {
		errs = append(errs, errors.New("database naming.identifier_max_length must be greater than or equal to 0"))
	}
	return errs
}

// LogLevel 表示 GORM logger 日志级别。
type LogLevel string

const (
	// LogLevelSilent 禁用 GORM 日志输出。
	LogLevelSilent LogLevel = "silent"

	// LogLevelError 只输出错误日志。
	LogLevelError LogLevel = "error"

	// LogLevelWarn 输出慢 SQL 和错误日志。
	LogLevelWarn LogLevel = "warn"

	// LogLevelInfo 输出详细 SQL 日志。
	LogLevelInfo LogLevel = "info"
)

// LoggerConfig 表示 GORM logger.Config 中适合配置文件表达的字段。
type LoggerConfig struct {
	// Level 指定 GORM 日志级别。为空时使用 warn，只记录错误和慢查询。
	// 只有 Level 为 info 且 LogSQL 为 true 时才输出普通 SQL，通常仅用于开发调试。
	Level LogLevel `json:"level" yaml:"level" mapstructure:"level"`

	// LogSQL 表示是否在日志中记录 SQL 语句。
	// 开启后便于本地调试、慢查询排查和线上问题定位；生产环境建议同时开启
	// ParameterizedQueries，避免参数值、手机号、令牌等敏感数据进入日志。
	// 关闭后，即使 Level 配置为 info，也不应输出完整 SQL 明细。
	LogSQL bool `json:"log_sql" yaml:"log_sql" mapstructure:"log_sql"`

	// SlowThreshold 是慢 SQL 阈值。
	// 0 表示使用 GORM logger 默认值。
	SlowThreshold time.Duration `json:"slow_threshold" yaml:"slow_threshold" mapstructure:"slow_threshold"`

	// IgnoreRecordNotFoundError 表示是否忽略 gorm.ErrRecordNotFound 日志。
	IgnoreRecordNotFoundError bool `json:"ignore_record_not_found_error" yaml:"ignore_record_not_found_error" mapstructure:"ignore_record_not_found_error"`

	// ParameterizedQueries 表示 SQL 日志中是否隐藏参数值。
	// 生产环境建议开启，避免敏感参数进入日志。
	ParameterizedQueries bool `json:"parameterized_queries" yaml:"parameterized_queries" mapstructure:"parameterized_queries"`

	// Colorful 表示是否启用彩色日志输出。
	// slog 本身不负责该字段的渲染；是否支持颜色由实际配置的 slog Handler 决定。
	Colorful bool `json:"colorful" yaml:"colorful" mapstructure:"colorful"`
}

// validate 校验日志配置。
func (c *LoggerConfig) validate() []error {
	var errs []error
	if c.Level != "" && !isValidLogLevel(c.Level) {
		errs = append(errs, fmt.Errorf("database logger.level must be one of %q, %q, %q, %q", LogLevelSilent, LogLevelError, LogLevelWarn, LogLevelInfo))
	}
	if c.SlowThreshold < 0 {
		errs = append(errs, errors.New("database logger.slow_threshold must be greater than or equal to 0"))
	}
	return errs
}

func isValidLogLevel(level LogLevel) bool {
	switch level {
	case LogLevelSilent, LogLevelError, LogLevelWarn, LogLevelInfo:
		return true
	default:
		return false
	}
}

// MigrationConfig 表示 GORM 自动迁移相关配置。
type MigrationConfig struct {
	// DisableForeignKeyConstraintWhenMigrating 禁用 AutoMigrate/CreateTable 时自动创建外键约束。
	DisableForeignKeyConstraintWhenMigrating bool `json:"disable_foreign_key_constraint_when_migrating" yaml:"disable_foreign_key_constraint_when_migrating" mapstructure:"disable_foreign_key_constraint_when_migrating"`
}

// validate 预留迁移配置校验入口。
func (c *MigrationConfig) validate() []error {
	return nil
}

// MonitoringConfig 表示数据库监控配置。
//
// 该结构与 Redis 监控配置保持一致：TracingEnabled 控制链路追踪，MetricsEnabled
// 控制指标采集。具体 tracer/provider、Prometheus exporter 或 HTTP 路由注册应由
// 应用观测初始化代码统一负责。
type MonitoringConfig struct {
	// TracerProvider 是数据库 tracing 使用的 Provider；为空时使用全局 Provider。
	TracerProvider trace.TracerProvider `json:"-" yaml:"-" mapstructure:"-"`

	// MeterProvider 是数据库连接池指标使用的 Provider；为空时使用全局 Provider。
	MeterProvider metric.MeterProvider `json:"-" yaml:"-" mapstructure:"-"`

	// TracingEnabled 表示是否启用数据库链路追踪。
	// NewClient 会据此接入 gorm.io/plugin/opentelemetry/tracing。
	TracingEnabled bool `json:"tracing_enabled" yaml:"tracing_enabled" mapstructure:"tracing_enabled"`

	// MetricsEnabled 表示是否启用数据库指标采集。
	// 当前主要采集 database/sql 连接池指标；Prometheus exporter 由应用层统一注册。
	MetricsEnabled bool `json:"metrics_enabled" yaml:"metrics_enabled" mapstructure:"metrics_enabled"`
}

// validate 校验监控配置。
func (c *MonitoringConfig) validate() []error {
	return nil
}

// ResolverPolicy 表示读写分离负载策略。
type ResolverPolicy string

const (
	// ResolverPolicyRandom 表示随机选择一个可用连接。
	ResolverPolicyRandom ResolverPolicy = "random"
)

// ResolverConfig 表示 GORM dbresolver 插件常用配置。
//
// 这里只描述配置文件可表达的信息；具体注册 dbresolver.Config、sources、replicas
// 和 policy 的逻辑应在 NewClient 或独立 resolver 构造函数中完成。
type ResolverConfig struct {
	// Sources 是写库 DSN 列表。为空时通常使用 Config.DSN 作为默认写库。
	Sources []string `json:"sources" yaml:"sources" mapstructure:"sources"`

	// Replicas 是只读库 DSN 列表。启用读写分离时通常需要至少一个 replica。
	Replicas []string `json:"replicas" yaml:"replicas" mapstructure:"replicas"`

	// Policy 指定 replica 负载策略。为空时建议使用 dbresolver 默认策略。
	Policy ResolverPolicy `json:"policy" yaml:"policy" mapstructure:"policy"`

	// TraceResolverMode 表示是否在 SQL 日志中标识 resolver 模式。
	TraceResolverMode bool `json:"trace_resolver_mode" yaml:"trace_resolver_mode" mapstructure:"trace_resolver_mode"`
}

// validate 校验读写分离配置。
func (c *ResolverConfig) validate() []error {
	var errs []error
	for i, dsn := range c.Sources {
		if strings.TrimSpace(dsn) == "" {
			errs = append(errs, fmt.Errorf("database resolver.sources[%d] must not be empty", i))
		}
	}
	for i, dsn := range c.Replicas {
		if strings.TrimSpace(dsn) == "" {
			errs = append(errs, fmt.Errorf("database resolver.replicas[%d] must not be empty", i))
		}
	}
	if c.Policy != "" && !isValidResolverPolicy(c.Policy) {
		errs = append(errs, fmt.Errorf("database resolver.policy must be %q", ResolverPolicyRandom))
	}
	return errs
}

func isValidResolverPolicy(policy ResolverPolicy) bool {
	switch policy {
	case ResolverPolicyRandom:
		return true
	default:
		return false
	}
}
