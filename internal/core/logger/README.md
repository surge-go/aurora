# core/logger

基于 Zap 的高性能结构化日志库，支持 JSON/Console 格式、文件轮转和日志采样。

## 快速开始

```go
package main

import (
    "github.com/surge-go/aurora/internal/core/logger"
)

func main() {
    // 创建 logger
    log, err := logger.New(&logger.Config{
        Level:  logger.LevelInfo,
        Format: logger.FormatJSON,
        Output: logger.OutputStdout,
    })
    if err != nil {
        panic(err)
    }
    defer log.Sync()

    // 使用 logger
    log.Info("服务启动",
        logger.String("version", "1.0.0"),
        logger.Int("port", 8080),
    )
}
```

## 核心功能

### 1. 多级别日志
```go
log.Debug("调试信息", logger.Any("data", data))
log.Info("普通日志", logger.String("user", "alice"))
log.Warn("警告信息", logger.Error(err))
log.Error("错误日志", logger.String("stacktrace", "..."))
log.Fatal("致命错误")  // 记录后退出进程
```

### 2. 结构化日志
```go
// JSON 格式（生产环境推荐）
log, _ := logger.New(&logger.Config{
    Format: logger.FormatJSON,
})
// 输出: {"level":"info","ts":"2026-05-26T13:00:00.000Z","msg":"用户登录","user":"alice","ip":"192.168.1.1"}

// Console 格式（开发环境）
log, _ := logger.New(&logger.Config{
    Format: logger.FormatConsole,
})
// 输出: 2026-05-26T13:00:00.000Z  INFO  用户登录  {"user": "alice", "ip": "192.168.1.1"}
```

### 3. 文件输出 + 轮转
```go
log, _ := logger.New(&logger.Config{
    Output: logger.OutputFile,
    File:   "/var/log/app.log",
    Rotation: &logger.RotationConfig{
        MaxSize:    100,  // 100MB
        MaxAge:     7,    // 保留 7 天
        MaxBackups: 10,   // 最多 10 个备份
        Compress:   true, // 压缩旧日志
    },
})
```

`Rotation` 为空时写入单个文件且不自动轮转；生产环境请配置 `Rotation`。

### 4. 固定字段
```go
log, _ := logger.New(&logger.Config{
    InitialFields: map[string]string{
        "service": "user-api",
        "env":     "production",
        "region":  "us-west-2",
    },
})
// 每条日志都会携带这些字段
```

### 5. 调用方信息
```go
log, _ := logger.New(&logger.Config{
    AddCaller: true,  // 记录文件名和行号
})
// 输出: {"caller":"main.go:42","msg":"用户登录"}
```

### 6. 日志采样
高频重复日志场景，降低 IO 压力。

```go
log, _ := logger.New(&logger.Config{
    Sampling: &logger.SamplingConfig{
        Enabled:    true,
        Initial:    100,  // 每秒前 100 条完整输出
        Thereafter: 100,  // 之后每 100 条输出 1 条
    },
})
```

## 配置参考

### 完整配置示例

```go
cfg := &logger.Config{
    // 基础配置
    Level:  logger.LevelInfo,
    Format: logger.FormatJSON,
    Output: logger.OutputFile,
    File:   "/var/log/app.log",

    // 开发模式
    Development: false,

    // 调用方信息
    AddCaller:  true,
    CallerSkip: 0,

    // Stacktrace 级别
    StacktraceLevel: logger.StacktraceLevelError,

    // 固定字段
    InitialFields: map[string]string{
        "service": "user-api",
        "version": "1.0.0",
    },

    // 编码器配置
    Encoder: &logger.EncoderConfig{
        TimeEncoding:     "iso8601",
        DurationEncoding: "seconds",
        LevelEncoding:    "lowercase",
    },

    // 文件轮转
    Rotation: &logger.RotationConfig{
        MaxSize:    100,
        MaxAge:     7,
        MaxBackups: 10,
        Compress:   true,
    },

    // 日志采样
    Sampling: &logger.SamplingConfig{
        Enabled:    true,
        Initial:    100,
        Thereafter: 100,
    },
}

log, err := logger.New(cfg)
```

### 配置字段说明

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `Level` | `Level` | `info` | 日志级别：debug/info/warn/error/dpanic/panic/fatal |
| `Format` | `Format` | `json` | 输出格式：json/console |
| `Output` | `Output` | `stdout` | 输出目标：stdout/stderr/file |
| `File` | `string` | - | 日志文件路径（Output=file 时必填） |
| `Development` | `bool` | `false` | 开发模式（更易读的输出） |
| `AddCaller` | `bool` | `false` | 是否记录调用方文件和行号 |
| `StacktraceLevel` | `StacktraceLevel` | `error` | 从哪个级别开始记录 stacktrace |
| `InitialFields` | `map[string]string` | - | 每条日志都携带的固定字段 |

## YAML 配置示例

```yaml
logger:
  level: info
  format: json
  output: file
  file: /var/log/app.log
  add_caller: true
  stacktrace_level: error

  initial_fields:
    service: user-api
    env: production
    region: us-west-2

  rotation:
    max_size: 100
    max_age: 7
    max_backups: 10
    compress: true

  sampling:
    enabled: true
    initial: 100
    thereafter: 100
```

## 最佳实践

### 1. 生产环境配置
```go
log, _ := logger.New(&logger.Config{
    Level:           logger.LevelInfo,
    Format:          logger.FormatJSON,
    Output:          logger.OutputStdout,  // 容器环境输出到 stdout
    AddCaller:       true,
    StacktraceLevel: logger.StacktraceLevelError,
    InitialFields: map[string]string{
        "service": os.Getenv("SERVICE_NAME"),
        "env":     os.Getenv("ENV"),
        "version": os.Getenv("VERSION"),
    },
})
```

### 2. 开发环境配置
```go
log, _ := logger.New(&logger.Config{
    Level:       logger.LevelDebug,
    Format:      logger.FormatConsole,  // 更易读
    Output:      logger.OutputStdout,
    Development: true,
    AddCaller:   true,
})
```

### 3. Context 日志
```go
log, _ := logger.New(cfg)
log.InfoContext(ctx, "请求完成",
    logger.String("request_id", requestID),
)
```

### 4. 结构化日志
```go
// ✅ 推荐：使用结构化字段
log.Info("用户登录",
    logger.String("user", "alice"),
    logger.String("ip", "192.168.1.1"),
    logger.Duration("latency", 100*time.Millisecond),
)

// ❌ 不推荐：字符串拼接
log.Info(fmt.Sprintf("用户 %s 从 %s 登录", user, ip))
```

### 5. 错误日志
```go
if err != nil {
    log.Error("数据库查询失败",
        logger.Error(err),
        logger.String("query", sql),
    )
}
```

### 6. 日志采样
```go
// 高频日志场景（如每个请求都记录）
log, _ := logger.New(&logger.Config{
    Sampling: &logger.SamplingConfig{
        Enabled:    true,
        Initial:    100,  // 每秒前 100 条完整输出
        Thereafter: 100,  // 之后每 100 条输出 1 条
    },
})
```

## 性能对比

| 场景 | Zap | Logrus | 标准库 log |
|------|-----|--------|-----------|
| 结构化日志 | **0.3 µs** | 4.2 µs | - |
| 字符串日志 | **0.2 µs** | 3.8 µs | 1.5 µs |
| 内存分配 | **0 allocs** | 5 allocs | 2 allocs |

## 注意事项

1. **Sync 调用**：程序退出前调用 `log.Sync()` 刷新缓冲区
2. **Fatal/Panic**：会导致进程退出或 panic，谨慎使用
3. **文件权限**：确保日志文件路径有写权限
4. **磁盘空间**：配置 Rotation 避免日志文件无限增长
5. **敏感信息**：避免记录密码、Token 等敏感数据

## 依赖

- `go.uber.org/zap` - 高性能日志库
- `gopkg.in/natefinch/lumberjack.v2` - 日志文件轮转

## 相关文档

- [Zap 官方文档](https://github.com/uber-go/zap)
- [日志最佳实践](https://12factor.net/logs)


### 纯文本消息

`InfoText` 用于访问日志等已经格式化的文本，仅输出消息和一个换行，不添加 JSON 外层、
时间、级别、caller、名称或固定字段。普通 `Info` 等接口继续输出结构化日志。

```go
log.InfoText(`[QI] 2026/10/02 - 23:21:30 | 200 | 179.292µs |       127.0.0.1 | GET     "/health/live"`)
```

纯文本和结构化日志共用输出 writer，遵循相同的日志级别、输出目标、文件轮转和采样配置。
`With`、`Named` 派生的 logger 保留纯文本能力；这些附加字段不会加入纯文本消息。
`Sync` 同时刷新两种输出；`Nop` 不输出纯文本。


## 同时输出到控制台与文件

统一配置可使用 `outputs` 指定多个目标，旧版单目标 `output` 仍然兼容。
二者不能同时设置，重复或无效的目标会在启动校验时被拒绝。

```yaml
logger:
  level: info
  format: json
  outputs: [stdout, file]
  file: runtime/logs/qi.log
```

从项目根目录启动时，文件位于项目的 `runtime/logs/qi.log`；父目录在首次写入时自动创建。
结构化日志、`InfoText` 路由和访问日志都会同时输出，内容保持一致。
日志同步仅忽略控制台设备不支持 fsync 的错误，文件写入与同步错误仍会保留。
Console 格式同时输出文件时默认不使用颜色编码，避免文件中出现 ANSI 转义符。
Core 运行时配置可配合 `Rotation` 使用轮转；当前统一文件配置未启用轮转。
