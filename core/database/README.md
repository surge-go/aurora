# core/database

基于 GORM 的数据库客户端，支持 MySQL/PostgreSQL/SQLite/SQL Server，提供连接池管理、读写分离、链路追踪和指标采集。

## 快速开始

```go
package main

import (
    "log"
    "github.com/surge-go/aurora/core/database"
)

func main() {
    // 创建数据库客户端
    db, err := database.NewClient(&database.Config{
        Driver: database.DriverMySQL,
        DSN:    "user:pass@tcp(localhost:3306)/dbname?charset=utf8mb4&parseTime=True",
        Pool: &database.PoolConfig{
            MaxOpenConns: 100,
            MaxIdleConns: 10,
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    defer database.Close(db)

    // 使用 GORM
    var users []User
    db.Where("age > ?", 18).Find(&users)
}
```

## 核心功能

### 1. 多数据库支持
```go
// MySQL
db, _ := database.NewClient(&database.Config{
    Driver: database.DriverMySQL,
    DSN:    "user:pass@tcp(localhost:3306)/db",
})

// PostgreSQL
db, _ := database.NewClient(&database.Config{
    Driver: database.DriverPostgres,
    DSN:    "host=localhost user=user password=pass dbname=db port=5432",
})

// SQLite
db, _ := database.NewClient(&database.Config{
    Driver: database.DriverSQLite,
    DSN:    "test.db",
})
```

### 2. 连接池管理
```go
db, _ := database.NewClient(&database.Config{
    Driver: database.DriverMySQL,
    DSN:    "...",
    Pool: &database.PoolConfig{
        MaxOpenConns:    100,              // 最大连接数
        MaxIdleConns:    10,               // 最大空闲连接
        ConnMaxLifetime: 1 * time.Hour,    // 连接最大复用时间
        ConnMaxIdleTime: 10 * time.Minute, // 连接最大空闲时间
    },
})
```

### 3. SQL 日志

数据库日志通过 Aurora `core/logger` 输出，也保留 `NewClientWithSlog` 适配入口。未配置 `Logger` 时默认只记录错误和慢查询，
不会输出普通 SQL；只有开发调试时才建议同时设置 `Level=LogLevelInfo` 和 `LogSQL=true`。

```go
db, _ := database.NewClient(&database.Config{
    Driver: database.DriverMySQL,
    DSN:    "...",
    Logger: &database.LoggerConfig{
        Level:                     database.LogLevelInfo,
        LogSQL:                    true,  // 开发调试时记录 SQL
        SlowThreshold:             200 * time.Millisecond,
        IgnoreRecordNotFoundError: true,
        ParameterizedQueries:      true,  // 隐藏参数值（生产环境推荐）
    },
})
```

### 4. 读写分离
```go
db, _ := database.NewClient(&database.Config{
    Driver: database.DriverMySQL,
    DSN:    "master-dsn",  // 主库
    Resolver: &database.ResolverConfig{
        Replicas: []string{
            "replica1-dsn",  // 从库 1
            "replica2-dsn",  // 从库 2
        },
        Policy: database.ResolverPolicyRandom,
    },
})

// 写操作自动路由到主库
db.Create(&user)

// 读操作自动路由到从库
db.Find(&users)

// 强制使用主库
db.Clauses(dbresolver.Write).Find(&users)

// 带 FOR UPDATE/FOR SHARE/LOCK IN SHARE MODE 的 Raw SQL 会自动路由到主库；
// 其他无法从 SQL 文本可靠判断读写意图的 Raw SQL 仍建议显式指定 dbresolver.Read/Write。

// 关闭客户端时使用 database.Close，以便同时关闭主库、副库连接池并注销指标回调。
```

### 5. 链路追踪 + 指标采集
```go
db, _ := database.NewClient(&database.Config{
    Driver: database.DriverMySQL,
    DSN:    "...",
    Monitoring: &database.MonitoringConfig{
        TracingEnabled: true,  // OpenTelemetry 链路追踪
        MetricsEnabled: true,  // Prometheus 指标采集
    },
})
```

Provider 由应用的 `core/observability` 统一创建。也可以显式注入，避免数据库包
自行管理 OpenTelemetry 生命周期：

```go
db, _ := database.NewClient(&database.Config{
    Driver: database.DriverMySQL,
    DSN:    "...",
    Monitoring: &database.MonitoringConfig{
        TracingEnabled: true,
        MetricsEnabled: true,
        TracerProvider: runtime.TracerProvider,
        MeterProvider:  runtime.MeterProvider,
    },
})
```

### 6. 自动迁移
```go
db, _ := database.NewClient(&database.Config{
    Driver: database.DriverMySQL,
    DSN:    "...",
    Migration: &database.MigrationConfig{
        DisableForeignKeyConstraintWhenMigrating: true,
    },
})

// 迁移表结构
db.AutoMigrate(&User{}, &Order{})
```

## 配置参考

### 完整配置示例

```go
cfg := &database.Config{
    // 基础配置
    Driver: database.DriverMySQL,
    DSN:    "user:pass@tcp(localhost:3306)/db?charset=utf8mb4&parseTime=True",

    // 连接池配置
    Pool: &database.PoolConfig{
        MaxOpenConns:    100,
        MaxIdleConns:    10,
        ConnMaxLifetime: 1 * time.Hour,
        ConnMaxIdleTime: 10 * time.Minute,
    },

    // GORM 配置
    GORM: &database.GORMConfig{
        SkipDefaultTransaction: true,  // 禁用默认事务，提升性能
        PrepareStmt:            true,  // 启用 prepared statement 缓存
    },

    // 命名策略
    Naming: &database.NamingConfig{
        TablePrefix:   "t_",   // 表名前缀
        SingularTable: false,  // 使用复数表名
    },

    // 日志配置
    Logger: &database.LoggerConfig{
        Level:                     database.LogLevelInfo,
        LogSQL:                    true,
        SlowThreshold:             200 * time.Millisecond,
        ParameterizedQueries:      true,
        IgnoreRecordNotFoundError: true,
    },

    // 迁移配置
    Migration: &database.MigrationConfig{
        DisableForeignKeyConstraintWhenMigrating: true,
    },

    // 监控配置
    Monitoring: &database.MonitoringConfig{
        TracingEnabled: true,
        MetricsEnabled: true,
    },

    // 读写分离
    Resolver: &database.ResolverConfig{
        Replicas: []string{
            "replica1-dsn",
            "replica2-dsn",
        },
        Policy: database.ResolverPolicyRandom,
    },
}

db, err := database.NewClient(cfg)
```

### 配置字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `Driver` | `Driver` | 数据库驱动：mysql/postgres/sqlite/sqlserver |
| `DSN` | `string` | 数据库连接字符串 |
| `Pool` | `*PoolConfig` | 连接池配置 |
| `GORM` | `*GORMConfig` | GORM 初始化配置 |
| `Naming` | `*NamingConfig` | 表名/列名命名策略 |
| `Logger` | `*LoggerConfig` | SQL 日志配置 |
| `Migration` | `*MigrationConfig` | 自动迁移配置 |
| `Monitoring` | `*MonitoringConfig` | 链路追踪和指标采集 |
| `Resolver` | `*ResolverConfig` | 读写分离配置 |

数据库客户端固定启用 GORM 标准错误转换，使 MySQL、PostgreSQL、SQLite 和
SQL Server 的重复键等驱动错误可以通过 `errors.Is` 统一判断。

## YAML 配置示例

下面示例按生产环境安全默认配置，普通 SQL 默认不写入日志。开发调试时可临时将
`level` 改为 `info` 并将 `log_sql` 改为 `true`，排查完成后应恢复为安全默认值。

```yaml
database:
  driver: mysql
  dsn: "user:pass@tcp(localhost:3306)/db?charset=utf8mb4&parseTime=True"

  pool:
    max_open_conns: 100
    max_idle_conns: 10
    conn_max_lifetime: 1h
    conn_max_idle_time: 10m

  gorm:
    skip_default_transaction: true
    prepare_stmt: true

  naming:
    table_prefix: "t_"
    singular_table: false

  logger:
    level: warn
    log_sql: false
    slow_threshold: 200ms
    parameterized_queries: true
    ignore_record_not_found_error: true

  monitoring:
    tracing_enabled: true
    metrics_enabled: true

  resolver:
    replicas:
      - "replica1-dsn"
      - "replica2-dsn"
    policy: random
```

## 最佳实践

### 1. 生产环境配置
```go
db, _ := database.NewClient(&database.Config{
    Driver: database.DriverMySQL,
    DSN:    os.Getenv("DATABASE_DSN"),
    Pool: &database.PoolConfig{
        MaxOpenConns:    100,
        MaxIdleConns:    10,
        ConnMaxLifetime: 1 * time.Hour,
        ConnMaxIdleTime: 10 * time.Minute,
    },
    GORM: &database.GORMConfig{
        SkipDefaultTransaction: true,  // 提升性能
        PrepareStmt:            true,  // 缓存 prepared statement
    },
    Logger: &database.LoggerConfig{
        Level:                database.LogLevelWarn,  // 只记录慢查询和错误
        LogSQL:               false, // 生产环境默认不输出普通 SQL
        SlowThreshold:        200 * time.Millisecond,
        ParameterizedQueries: true,  // 隐藏敏感参数
    },
    Monitoring: &database.MonitoringConfig{
        TracingEnabled: true,
        MetricsEnabled: true,
    },
})
```

### 2. 开发环境配置
```go
db, _ := database.NewClient(&database.Config{
    Driver: database.DriverSQLite,
    DSN:    "test.db",
    Logger: &database.LoggerConfig{
        Level:  database.LogLevelInfo,  // 输出所有 SQL
        LogSQL: true, // 仅开发调试时开启
    },
})
```

### 3. 事务处理
```go
// 自动事务
err := db.Transaction(func(tx *gorm.DB) error {
    if err := tx.Create(&user).Error; err != nil {
        return err
    }
    if err := tx.Create(&order).Error; err != nil {
        return err
    }
    return nil
})

// 手动事务
tx := db.Begin()
defer func() {
    if r := recover(); r != nil {
        tx.Rollback()
    }
}()

if err := tx.Create(&user).Error; err != nil {
    tx.Rollback()
    return err
}
tx.Commit()
```

### 4. 预加载关联
```go
// 预加载单个关联
db.Preload("Orders").Find(&users)

// 预加载多个关联
db.Preload("Orders").Preload("Profile").Find(&users)

// 条件预加载
db.Preload("Orders", "status = ?", "paid").Find(&users)
```

### 5. 批量操作
```go
// 批量插入
users := []User{{Name: "alice"}, {Name: "bob"}}
db.CreateInBatches(users, 100)  // 每批 100 条

// 批量更新
db.Model(&User{}).Where("age > ?", 18).Update("status", "active")
```

### 6. 软删除
```go
type User struct {
    ID        uint
    Name      string
    DeletedAt gorm.DeletedAt `gorm:"index"`
}

// 软删除
db.Delete(&user)

// 查询时自动过滤已删除记录
db.Find(&users)

// 包含已删除记录
db.Unscoped().Find(&users)

// 永久删除
db.Unscoped().Delete(&user)
```

### 7. 性能优化
```go
// 使用索引
type User struct {
    Email string `gorm:"index"`
    Name  string `gorm:"index:idx_name_age"`
    Age   int    `gorm:"index:idx_name_age"`
}

// 只查询需要的字段
db.Select("id", "name").Find(&users)

// 使用原生 SQL
db.Raw("SELECT * FROM users WHERE age > ?", 18).Scan(&users)

// 禁用默认事务
db, _ := database.NewClient(&database.Config{
    GORM: &database.GORMConfig{
        SkipDefaultTransaction: true,
    },
})
```

## DSN 格式

### MySQL
```
user:pass@tcp(host:port)/dbname?charset=utf8mb4&parseTime=True&loc=Local
```

### PostgreSQL
```
host=localhost user=user password=pass dbname=db port=5432 sslmode=disable TimeZone=Asia/Shanghai
```

### SQLite
```
test.db
```

### SQL Server
```
sqlserver://user:pass@localhost:1433?database=db
```

## 性能指标

### 连接池监控
```go
sqlDB, _ := db.DB()

// 获取连接池状态
stats := sqlDB.Stats()
fmt.Printf("打开连接数: %d\n", stats.OpenConnections)
fmt.Printf("使用中连接: %d\n", stats.InUse)
fmt.Printf("空闲连接: %d\n", stats.Idle)
```

### Prometheus 指标
启用 `MetricsEnabled` 后自动采集：
- `gorm_dbstats_max_open_connections` - 最大连接数
- `gorm_dbstats_open_connections` - 当前打开连接数
- `gorm_dbstats_in_use` - 使用中连接数
- `gorm_dbstats_idle` - 空闲连接数
- `gorm_dbstats_wait_count` - 等待连接次数
- `gorm_dbstats_wait_duration` - 等待连接总时长

## 注意事项

1. **连接池大小**：根据 QPS 和查询耗时合理设置，避免过大或过小
2. **连接复用**：配置 `ConnMaxLifetime` 避免长连接老化
3. **慢查询**：设置 `SlowThreshold` 监控慢查询
4. **参数化查询**：生产环境启用 `ParameterizedQueries` 避免敏感信息泄露
5. **读写分离**：确保从库延迟可接受，必要时强制读主库

## 依赖

- `gorm.io/gorm` - GORM ORM 框架
- `gorm.io/driver/mysql` - MySQL 驱动
- `gorm.io/driver/postgres` - PostgreSQL 驱动
- `gorm.io/driver/sqlite` - SQLite 驱动
- `gorm.io/driver/sqlserver` - SQL Server 驱动
- `gorm.io/plugin/dbresolver` - 读写分离插件
- `gorm.io/plugin/opentelemetry` - OpenTelemetry 插件

## 相关文档

- [GORM 官方文档](https://gorm.io/docs/)
- [数据库连接池最佳实践](https://www.alexedwards.net/blog/configuring-sqldb)

### 与 logger 包集成

应用初始化 Aurora logger 后，通过 `NewClientWithLogger` 复用同一套输出、固定字段和链路上下文：

```go
import (
    "os"
    "github.com/surge-go/aurora/core/database"
    "github.com/surge-go/aurora/core/logger"
)

appLog, err := logger.New(&logger.Config{Output: logger.OutputStdout})
if err != nil {
    return err
}
db, err := database.NewClientWithLogger(&database.Config{
    Driver: database.DriverMySQL,
    DSN:    os.Getenv("DATABASE_DSN"),
}, appLog)
```
