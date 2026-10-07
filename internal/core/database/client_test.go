package database

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/surge-go/aurora/internal/core/logger"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func TestNewClientSQLite(t *testing.T) {
	db, err := NewClient(&Config{
		Driver: DriverSQLite,
		DSN:    sqliteMemoryDSN(t, "primary"),
		GORM: &GORMConfig{
			DisableAutomaticPing: true,
		},
		Pool: &PoolConfig{
			MaxOpenConns: 1,
			MaxIdleConns: 1,
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer func() {
		if err := Close(db); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	var _ *gorm.DB = db
	if err := db.Exec("CREATE TABLE users (id integer primary key, name text unique)").Error; err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if err := db.Exec("INSERT INTO users (name) VALUES (?)", "alice").Error; err != nil {
		t.Fatalf("first insert error = %v", err)
	}
	if err := db.Exec("INSERT INTO users (name) VALUES (?)", "alice").Error; !errors.Is(err, gorm.ErrDuplicatedKey) {
		t.Fatalf("duplicate insert error = %v, want %v", err, gorm.ErrDuplicatedKey)
	}
}

func TestNewClientWithLoggerUsesProvidedSlogLogger(t *testing.T) {
	var output bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	db, err := NewClientWithSlog(&Config{
		Driver: DriverSQLite,
		DSN:    sqliteMemoryDSN(t, "slog_logger"),
		GORM: &GORMConfig{
			DisableAutomaticPing: true,
		},
		Logger: &LoggerConfig{
			Level:  LogLevelInfo,
			LogSQL: true,
		},
	}, log)
	if err != nil {
		t.Fatalf("NewClientWithSlog() error = %v", err)
	}
	defer closeTestDB(t, db)

	if err := db.Exec("CREATE TABLE users (id integer primary key, name text)").Error; err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	if !strings.Contains(output.String(), `"msg":"gorm sql"`) {
		t.Fatal("provided slog logger did not receive gorm sql log")
	}
	if !strings.Contains(output.String(), `"sql":"CREATE TABLE users`) {
		t.Fatalf("gorm sql log = %s, want create table statement", output.String())
	}
}

func TestNewClientWithLoggerUsesAuroraLogger(t *testing.T) {
	path := t.TempDir() + "/database.log"
	appLog, err := logger.New(&logger.Config{
		Output:          logger.OutputFile,
		File:            path,
		StacktraceLevel: logger.StacktraceLevelNone,
	})
	if err != nil {
		t.Fatal(err)
	}

	db, err := NewClientWithLogger(&Config{
		Driver: DriverSQLite,
		DSN:    sqliteMemoryDSN(t, "aurora_logger"),
		GORM:   &GORMConfig{DisableAutomaticPing: true},
		Logger: &LoggerConfig{Level: LogLevelInfo, LogSQL: true},
	}, appLog)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)

	if err := db.Exec("CREATE TABLE users (id integer primary key)").Error; err != nil {
		t.Fatal(err)
	}
	if err := appLog.Sync(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "gorm sql") || !strings.Contains(content, "CREATE TABLE users") {
		t.Fatalf("database log = %s, want SQL through Aurora logger", content)
	}
}

func TestDefaultDatabaseLoggerHidesSQL(t *testing.T) {
	var output bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
	db, err := NewClientWithSlog(&Config{
		Driver: DriverSQLite,
		DSN:    sqliteMemoryDSN(t, "default_logger"),
		GORM: &GORMConfig{
			DisableAutomaticPing: true,
		},
	}, log)
	if err != nil {
		t.Fatalf("NewClientWithSlog() error = %v", err)
	}
	defer func() {
		if err := Close(db); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	if err := db.Exec("CREATE TABLE users (id integer primary key)").Error; err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if strings.Contains(output.String(), "gorm sql") || strings.Contains(output.String(), "CREATE TABLE users") {
		t.Fatalf("default database logger emitted SQL: %s", output.String())
	}
}

func TestNewClientAppliesConfig(t *testing.T) {
	db, err := NewClient(&Config{
		Driver: DriverSQLite,
		DSN:    sqliteMemoryDSN(t, "config"),
		GORM: &GORMConfig{
			SkipDefaultTransaction:   true,
			PrepareStmt:              true,
			DisableNestedTransaction: true,
			AllowGlobalUpdate:        true,
			DisableAutomaticPing:     true,
		},
		Naming: &NamingConfig{
			TablePrefix:         "t_",
			SingularTable:       true,
			NoLowerCase:         true,
			IdentifierMaxLength: 32,
		},
		Logger: &LoggerConfig{
			Level:                LogLevelInfo,
			LogSQL:               false,
			ParameterizedQueries: true,
		},
		Migration: &MigrationConfig{
			DisableForeignKeyConstraintWhenMigrating: true,
		},
		Pool: &PoolConfig{
			MaxOpenConns:    3,
			MaxIdleConns:    2,
			ConnMaxLifetime: time.Minute,
			ConnMaxIdleTime: time.Second,
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer closeTestDB(t, db)
	if !db.Config.SkipDefaultTransaction {
		t.Fatal("SkipDefaultTransaction = false, want true")
	}
	if !db.Config.PrepareStmt {
		t.Fatal("PrepareStmt = false, want true")
	}
	if !db.Config.DisableNestedTransaction {
		t.Fatal("DisableNestedTransaction = false, want true")
	}
	if !db.Config.AllowGlobalUpdate {
		t.Fatal("AllowGlobalUpdate = false, want true")
	}
	if !db.Config.DisableAutomaticPing {
		t.Fatal("DisableAutomaticPing = false, want true")
	}
	if !db.Config.TranslateError {
		t.Fatal("TranslateError = false, want true")
	}
	if !db.Config.DisableForeignKeyConstraintWhenMigrating {
		t.Fatal("DisableForeignKeyConstraintWhenMigrating = false, want true")
	}

	naming, ok := db.Config.NamingStrategy.(schema.NamingStrategy)
	if !ok {
		t.Fatalf("NamingStrategy type = %T, want schema.NamingStrategy", db.Config.NamingStrategy)
	}
	if naming.TablePrefix != "t_" || !naming.SingularTable || !naming.NoLowerCase || naming.IdentifierMaxLength != 32 {
		t.Fatalf("NamingStrategy = %+v, want configured values", naming)
	}

	if _, ok := db.Config.Logger.(slogGORMLogger); !ok {
		t.Fatalf("Logger type = %T, want slogGORMLogger", db.Config.Logger)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("DB() error = %v", err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 3 {
		t.Fatalf("MaxOpenConnections = %d, want 3", got)
	}
}

func TestSQLHiddenLoggerKeepsSlowAndErrorSignals(t *testing.T) {
	var output bytes.Buffer
	log := slogGORMLogger{
		logger: slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})),
		config: gormlogger.Config{
			SlowThreshold: time.Millisecond,
			LogLevel:      gormlogger.Info,
		},
		logSQL: false,
	}

	log.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "select * from users where phone = '13800000000'", 1
	}, nil)
	if output.Len() != 0 {
		t.Fatalf("normal trace log output = %q, want empty", output.String())
	}

	log.Trace(context.Background(), time.Now().Add(-2*time.Millisecond), func() (string, int64) {
		return "select * from users where phone = '13800000000'", 1
	}, nil)
	slowOutput := output.String()
	if !strings.Contains(slowOutput, `"msg":"gorm slow sql"`) {
		t.Fatalf("slow trace output = %q, want gorm slow sql", slowOutput)
	}
	if !strings.Contains(slowOutput, `"sql":"[SQL hidden]"`) {
		t.Fatalf("slow trace output = %q, want hidden sql", slowOutput)
	}

	output.Reset()
	log.Trace(context.Background(), time.Now(), func() (string, int64) {
		return "select * from users where token = 'secret'", -1
	}, errors.New("query failed"))
	errorOutput := output.String()
	if !strings.Contains(errorOutput, `"msg":"gorm sql error"`) {
		t.Fatalf("error trace output = %q, want gorm sql error", errorOutput)
	}
	if !strings.Contains(errorOutput, `"sql":"[SQL hidden]"`) {
		t.Fatalf("error trace output = %q, want hidden sql", errorOutput)
	}
	if !strings.Contains(errorOutput, `"error":"query failed"`) {
		t.Fatalf("error trace output = %q, want query failed", errorOutput)
	}
}

func TestSlogGORMLoggerParamsFilter(t *testing.T) {
	log := slogGORMLogger{
		config: gormlogger.Config{
			ParameterizedQueries: true,
		},
	}

	sql, params := log.ParamsFilter(context.Background(), "select * from users where id = ?", 1)
	if sql != "select * from users where id = ?" {
		t.Fatalf("ParamsFilter() sql = %q, want original sql", sql)
	}
	if params != nil {
		t.Fatalf("ParamsFilter() params = %v, want nil", params)
	}

	log.config.ParameterizedQueries = false
	_, params = log.ParamsFilter(context.Background(), "select * from users where id = ?", 1)
	if len(params) != 1 || params[0] != 1 {
		t.Fatalf("ParamsFilter() params = %v, want original params", params)
	}
}

func TestNewClientRegistersResolver(t *testing.T) {
	db, err := NewClient(&Config{
		Driver: DriverSQLite,
		DSN:    sqliteMemoryDSN(t, "resolver_primary"),
		GORM: &GORMConfig{
			DisableAutomaticPing: true,
		},
		Resolver: &ResolverConfig{
			Replicas: []string{sqliteMemoryDSN(t, "resolver_replica")},
			Policy:   ResolverPolicyRandom,
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer func() {
		if err := Close(db); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	if _, ok := db.Plugins["gorm:db_resolver"]; !ok {
		t.Fatal("dbresolver plugin is not registered")
	}
}

func TestResolverRoutesLockingRawSQLToPrimary(t *testing.T) {
	db, err := NewClient(&Config{
		Driver: DriverSQLite,
		DSN:    sqliteMemoryDSN(t, "locking_primary"),
		GORM: &GORMConfig{
			DisableAutomaticPing: true,
		},
		Resolver: &ResolverConfig{
			Replicas: []string{sqliteMemoryDSN(t, "locking_replica")},
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer closeTestDB(t, db)
	var captured gorm.ConnPool
	if err := db.Callback().Row().After("gorm:db_resolver").Register("test:capture_resolver_pool", func(tx *gorm.DB) {
		captured = tx.Statement.ConnPool
	}); err != nil {
		t.Fatalf("register capture callback error = %v", err)
	}
	manual := db.Session(&gorm.Session{NewDB: true})
	manual.Statement.SQL.WriteString("SELECT 1 FOR UPDATE;")
	routeLockingRawSQL(manual)
	if manual.Statement.ConnPool != db.Config.ConnPool {
		t.Fatalf("manual locking raw SQL conn pool = %p, want primary %p", manual.Statement.ConnPool, db.Config.ConnPool)
	}

	_, _ = db.Raw("SELECT 1 FOR UPDATE;").Rows()
	if captured != db.Config.ConnPool {
		t.Fatalf("locking raw SQL conn pool = %p, want primary %p", captured, db.Config.ConnPool)
	}

	for _, query := range []string{
		"SELECT * FROM users FOR UPDATE;",
		"SELECT * FROM users FOR SHARE /* comment */;",
		"SELECT * FROM users FOR NO KEY UPDATE NOWAIT",
		"SELECT * FROM users LOCK IN SHARE MODE",
	} {
		if !isLockingRawSQL(query) {
			t.Errorf("isLockingRawSQL(%q) = false, want true", query)
		}
	}
	for _, query := range []string{
		"SELECT * FROM users",
		"SELECT * FROM users WHERE id = ?",
	} {
		if isLockingRawSQL(query) {
			t.Errorf("isLockingRawSQL(%q) = true, want false", query)
		}
	}
}

func TestResolverCallbackIsRegistered(t *testing.T) {
	db, err := NewClient(&Config{
		Driver: DriverSQLite,
		DSN:    sqliteMemoryDSN(t, "callback_primary"),
		GORM: &GORMConfig{
			DisableAutomaticPing: true,
		},
		Resolver: &ResolverConfig{
			Replicas: []string{sqliteMemoryDSN(t, "callback_replica")},
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer closeTestDB(t, db)

	if db.Callback().Raw().Get(lockingRawSQLCallbackName) == nil {
		t.Fatal("locking raw SQL callback is not registered")
	}
	if db.Callback().Row().Get(lockingRawSQLRowCallbackName) == nil {
		t.Fatal("locking row SQL callback is not registered")
	}
}

func TestCloseClosesResolverPools(t *testing.T) {
	db, err := NewClient(&Config{
		Driver: DriverSQLite,
		DSN:    sqliteMemoryDSN(t, "close_primary"),
		GORM: &GORMConfig{
			DisableAutomaticPing: true,
		},
		Resolver: &ResolverConfig{
			Replicas: []string{sqliteMemoryDSN(t, "close_replica")},
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	value, ok := clients.Load(db)
	if !ok {
		t.Fatal("client resources were not registered")
	}
	resources := value.(*clientResources)
	resources.mu.Lock()
	pools := make([]*sql.DB, 0, len(resources.dbs))
	for _, resource := range resources.dbs {
		pools = append(pools, resource.db)
	}
	resources.mu.Unlock()
	if len(pools) != 2 {
		t.Fatalf("tracked pools = %d, want primary and replica", len(pools))
	}

	if err := Close(db); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, ok := clients.Load(db); ok {
		t.Fatal("client resources remain registered after Close")
	}
	for i, pool := range pools {
		if err := pool.Ping(); err == nil {
			t.Fatalf("pool %d still accepts Ping after Close", i)
		}
	}
}

func TestNewClientRegistersTracing(t *testing.T) {
	db, err := NewClient(&Config{
		Driver: DriverSQLite,
		DSN:    sqliteMemoryDSN(t, "tracing"),
		GORM: &GORMConfig{
			DisableAutomaticPing: true,
		},
		Monitoring: &MonitoringConfig{
			TracingEnabled: true,
			MetricsEnabled: false,
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer closeTestDB(t, db)

	if _, ok := db.Plugins["otelgorm"]; !ok {
		t.Fatal("opentelemetry tracing plugin is not registered")
	}
}

func TestNewClientUsesExplicitMeterProvider(t *testing.T) {
	provider := metric.NewMeterProvider()
	defer func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("MeterProvider.Shutdown() error = %v", err)
		}
	}()

	db, err := NewClient(&Config{
		Driver: DriverSQLite,
		DSN:    sqliteMemoryDSN(t, "explicit_meter_provider"),
		GORM: &GORMConfig{
			DisableAutomaticPing: true,
		},
		Monitoring: &MonitoringConfig{
			MetricsEnabled: true,
			MeterProvider:  provider,
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	defer closeTestDB(t, db)
}

func TestCloseUnregistersDatabaseMetrics(t *testing.T) {
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	defer func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Errorf("MeterProvider.Shutdown() error = %v", err)
		}
	}()

	db, err := NewClient(&Config{
		Driver: DriverSQLite,
		DSN:    sqliteMemoryDSN(t, "metrics_close"),
		GORM: &GORMConfig{
			DisableAutomaticPing: true,
		},
		Monitoring: &MonitoringConfig{
			MetricsEnabled: true,
			MeterProvider:  provider,
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	var before metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &before); err != nil {
		t.Fatalf("Collect() before Close error = %v", err)
	}
	if len(before.ScopeMetrics) == 0 {
		t.Fatal("Collect() before Close returned no database metrics")
	}

	if err := Close(db); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	var after metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &after); err != nil {
		t.Fatalf("Collect() after Close error = %v", err)
	}
	if len(after.ScopeMetrics) != 0 {
		t.Fatalf("Collect() after Close returned %d scopes, want 0", len(after.ScopeMetrics))
	}
}

func TestNewClientRejectsInvalidConfig(t *testing.T) {
	db, err := NewClient(&Config{})
	if err == nil {
		closeTestDB(t, db)
		t.Fatal("NewClient() error = nil, want error")
	}
}

func sqliteMemoryDSN(t *testing.T, name string) string {
	t.Helper()
	testName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	return "file:" + testName + "_" + name + "?mode=memory&cache=shared"
}

func closeTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := Close(db); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
