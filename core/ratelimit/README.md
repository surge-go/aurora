# core/ratelimit

分布式限流器，基于 Redis 实现精确的滑动窗口算法，用于保护 API 免受滥用和攻击。

## 快速开始

### 1. 安装

```bash
go get github.com/surge-go/aurora
```

### 2. 基础用法

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/surge-go/aurora/core/ratelimit"
)

func main() {
    // 1. 创建 Redis 客户端
    rdb := redis.NewClient(&redis.Options{
        Addr: "localhost:6379",
    })

    // 2. 配置限流器
    limiter, err := ratelimit.New(rdb, ratelimit.Config{
        KeyPrefix: "myapp:login:ip:{limiter}",
        HMACKey:   "your-32-byte-secret-key-here!!!", // 生产环境使用环境变量
        Limit:     10,                                  // 10 次/分钟
        Window:    time.Minute,
    })
    if err != nil {
        panic(err)
    }

    // 3. 检查限流
    ctx := context.Background()
    ip := "192.168.1.1"

    allowed, remaining, retryAfter, err := limiter.Allow(ctx, ip)
    if err != nil {
        panic(err)
    }

    if !allowed {
        fmt.Printf("Rate limit exceeded! Retry after %v\n", retryAfter)
        return
    }

    fmt.Printf("Request allowed. Remaining: %d\n", remaining)
}
```

## HTTP 中间件集成

### 登录接口限流

```go
package main

import (
    "os"
    "time"

    "github.com/redis/go-redis/v9"
    "github.com/surge-go/aurora"
    "github.com/surge-go/aurora/core/ratelimit"
    "github.com/surge-go/aurora/middleware"
)

func main() {
    // Redis 客户端
    rdb := redis.NewClient(&redis.Options{
        Addr: os.Getenv("REDIS_ADDR"),
    })

    // 登录限流器：10 次/分钟
    loginLimiter, err := ratelimit.New(rdb, ratelimit.Config{
        KeyPrefix: "myapp:login:ip:{limiter}",
        HMACKey:   os.Getenv("HMAC_KEY"), // 至少 32 字节
        Limit:     10,
        Window:    time.Minute,
    })
    if err != nil {
        panic(err)
    }

    // 路由配置
    engine, _ := aurora.New(aurora.DefaultConfig())

    // 只对登录接口限流
    engine.POST("/auth/login",
        middleware.IPRateLimiter(loginLimiter),
        handleLogin,
    )

    engine.Run(":8080")
}

func handleLogin(c *aurora.Context) {
    // 登录逻辑
}
```

### 全局限流（降级保护）

```go
// 全局限流器：1000 次/分钟
globalLimiter, _ := ratelimit.New(rdb, ratelimit.Config{
    KeyPrefix: "myapp:global:ip:{limiter}",
    HMACKey:   os.Getenv("HMAC_KEY"),
    Limit:     1000,
    Window:    time.Minute,
})

// 全局中间件（Redis 故障时放行请求）
engine.Use(middleware.IPRateLimiter(globalLimiter,
    middleware.WithFailOpen(), // 降级策略
))
```

### 自定义 IP 提取器

```go
// 只信任 Cloudflare 的 CF-Connecting-IP
engine.POST("/api/data",
    middleware.IPRateLimiter(limiter,
        middleware.WithIPExtractor(func(c *aurora.Context) string {
            if cfIP := c.GetHeader("CF-Connecting-IP"); cfIP != "" {
                return cfIP
            }
            // 非 Cloudflare 流量，使用 RemoteAddr
            if ip, _, err := net.SplitHostPort(c.Request().RemoteAddr); err == nil {
                return ip
            }
            return c.Request().RemoteAddr
        }),
    ),
    handleData,
)
```

## 配置说明

### Config 结构

```go
type Config struct {
    KeyPrefix string        // Redis 键前缀，必须包含 {name} hash tag
    HMACKey   string        // HMAC 密钥，至少 32 字节
    Limit     int           // 限流阈值（窗口内最大请求数）
    Window    time.Duration // 时间窗口，至少 1 秒
}
```

### 配置约束

| 字段 | 约束 | 默认值 | 说明 |
|------|------|--------|------|
| `KeyPrefix` | 必须包含 `{name}` | `aurora:ratelimit:{ratelimit}` | Redis Cluster 需要 hash tag |
| `HMACKey` | ≥32 字节 | 无（必须提供） | 用于隐私保护，防止 IP 泄露 |
| `Limit` | >0 | `100` | 限流阈值 |
| `Window` | ≥1 秒 | `60 秒` | 时间窗口 |

### 默认配置

```go
cfg := ratelimit.DefaultConfig()
cfg.HMACKey = os.Getenv("HMAC_KEY") // 必须设置

limiter, err := ratelimit.New(rdb, cfg)
```

## API 参考

### New

创建限流器实例。

```go
func New(rdb redis.UniversalClient, cfg Config) (*Limiter, error)
```

**错误**：
- `ErrRedisRequired`：Redis 客户端为 nil
- `ErrInvalidConfig`：配置验证失败（嵌套具体错误）

### Allow

检查是否允许请求通过。

```go
func (l *Limiter) Allow(ctx context.Context, key string) (allowed bool, remaining int, retryAfter time.Duration, err error)
```

**参数**：
- `ctx`：上下文（支持超时和取消）
- `key`：限流维度标识（IP 地址、用户 ID 等）

**返回值**：
- `allowed`：是否允许请求
- `remaining`：剩余配额（0 到 limit-1）
- `retryAfter`：重试等待时间（allowed=false 时有效）
- `err`：错误

**错误**：
- `ErrKeyRequired`：key 为空
- `ErrStoreUnavailable`：Redis 操作失败
- `context.Canceled`：请求被取消
- `context.DeadlineExceeded`：请求超时

**并发安全**：✅ 多个 goroutine 可并发调用

### Reset

重置指定 key 的限流状态（管理操作）。

```go
func (l *Limiter) Reset(ctx context.Context, key string) error
```

**使用场景**：
- 管理员手动解除用户限流
- 测试环境重置状态

### Limit / Window

获取限流器配置（用于日志和监控）。

```go
func (l *Limiter) Limit() int
func (l *Limiter) Window() time.Duration
```

## 高级用法

### 多级限流

```go
// IP 级限流：10 次/分钟
ipLimiter, _ := ratelimit.New(rdb, ratelimit.Config{
    KeyPrefix: "myapp:login:ip:{limiter}",
    HMACKey:   hmacKey,
    Limit:     10,
    Window:    time.Minute,
})

// 账号级限流：5 次/分钟（更严格）
accountLimiter, _ := ratelimit.New(rdb, ratelimit.Config{
    KeyPrefix: "myapp:login:account:{limiter}",
    HMACKey:   hmacKey,
    Limit:     5,
    Window:    time.Minute,
})

// 组合使用
engine.POST("/auth/login",
    middleware.IPRateLimiter(ipLimiter),
    middleware.IPRateLimiter(accountLimiter, middleware.WithIPExtractor(extractAccountID)),
    handleLogin,
)

func extractAccountID(c *aurora.Context) string {
    // 从请求体中提取账号 ID
    return c.PostForm("username")
}
```

### 非 HTTP 场景

```go
// 消息队列速率控制
func processMessage(msg Message) error {
    ctx := context.Background()
    queueName := msg.Queue

    allowed, _, retryAfter, err := queueLimiter.Allow(ctx, queueName)
    if err != nil {
        return fmt.Errorf("rate limit check failed: %w", err)
    }

    if !allowed {
        // 等待后重试
        time.Sleep(retryAfter)
        return processMessage(msg)
    }

    // 处理消息
    return handleMessage(msg)
}
```

### 动态限流配置

```go
// 使用 Consul/etcd 动态调整限流配置
type DynamicLimiter struct {
    mu      sync.RWMutex
    limiter *ratelimit.Limiter
}

func (d *DynamicLimiter) UpdateConfig(cfg ratelimit.Config) error {
    newLimiter, err := ratelimit.New(rdb, cfg)
    if err != nil {
        return err
    }

    d.mu.Lock()
    d.limiter = newLimiter
    d.mu.Unlock()

    return nil
}

func (d *DynamicLimiter) Allow(ctx context.Context, key string) (bool, int, time.Duration, error) {
    d.mu.RLock()
    limiter := d.limiter
    d.mu.RUnlock()

    return limiter.Allow(ctx, key)
}
```

## HTTP 响应头

中间件会自动设置标准的限流响应头：

| 响应头 | 说明 | 示例 |
|--------|------|------|
| `X-RateLimit-Limit` | 限流阈值 | `10` |
| `X-RateLimit-Remaining` | 剩余配额 | `7` |
| `Retry-After` | 重试等待时间（秒）| `45` |

**示例响应**：

```http
HTTP/1.1 429 Too Many Requests
X-RateLimit-Limit: 10
X-RateLimit-Remaining: 0
Retry-After: 45
Content-Type: application/json

{
  "error": "too many requests",
  "retry_after": 45.123
}
```

## 降级策略

### FailOpen（放行请求）

适用于非关键路由的全局限流。

```go
engine.Use(middleware.IPRateLimiter(globalLimiter,
    middleware.WithFailOpen(), // Redis 故障时放行请求
))
```

**优点**：
- ✅ 避免 Redis 故障导致服务不可用
- ✅ 降低运维压力

**缺点**：
- ⚠️ Redis 故障期间失去限流保护

### FailClosed（拒绝请求）

适用于安全敏感接口（默认行为）。

```go
engine.POST("/auth/login",
    middleware.IPRateLimiter(loginLimiter), // 默认 FailClosed
    handleLogin,
)
```

**优点**：
- ✅ 安全优先，防止攻击突破

**缺点**：
- ⚠️ Redis 故障时服务不可用

## 安全最佳实践

### 1. 密钥管理

```go
// ❌ 错误：硬编码密钥
limiter, _ := ratelimit.New(rdb, ratelimit.Config{
    HMACKey: "my-secret-key",
})

// ✅ 正确：使用环境变量
limiter, _ := ratelimit.New(rdb, ratelimit.Config{
    HMACKey: os.Getenv("RATELIMIT_HMAC_KEY"),
})

// ✅ 更好：使用密钥管理服务
hmacKey, _ := secretsManager.GetSecret("ratelimit-hmac-key")
limiter, _ := ratelimit.New(rdb, ratelimit.Config{
    HMACKey: hmacKey,
})
```

### 2. IP 提取安全

```go
// ❌ 错误：盲目信任 X-Forwarded-For
// 攻击者可伪造该头部绕过限流

// ✅ 正确：只信任已知代理的头部
middleware.IPRateLimiter(limiter, middleware.WithIPExtractor(func(c *aurora.Context) string {
    // 只信任 Cloudflare 的 CF-Connecting-IP
    if cfIP := c.GetHeader("CF-Connecting-IP"); cfIP != "" {
        return cfIP
    }
    // 非代理流量使用 RemoteAddr
    if ip, _, err := net.SplitHostPort(c.Request().RemoteAddr); err == nil {
        return ip
    }
    return c.Request().RemoteAddr
}))
```

### 3. 多维度限流

```go
// 结合 IP 和账号限流，防止攻击者切换 IP
engine.POST("/auth/login",
    middleware.IPRateLimiter(ipLimiter),       // IP 级限流
    middleware.IPRateLimiter(accountLimiter,    // 账号级限流
        middleware.WithIPExtractor(extractAccountID),
    ),
    handleLogin,
)
```

## 性能考虑

### Redis 操作次数

每次 `Allow()` 调用执行 **1 次 Lua 脚本**，包含：
- `ZREMRANGEBYSCORE`：删除过期请求（O(log(N)+M)）
- `ZCARD`：统计请求数（O(1)）
- `ZADD`：记录新请求（O(log(N))）
- `EXPIRE`：设置 TTL（O(1)）

**总时间复杂度**：O(log(N))，N 为窗口内的请求数

### 性能优化建议

1. **使用 Redis Cluster**：水平扩展（hash tag 保证同一限流器的键在同一 slot）
2. **启用 Redis 持久化**：AOF + RDB，防止重启后限流状态丢失
3. **监控 Redis 延迟**：P99 延迟应 <10ms
4. **合理设置 Window**：过小的窗口会增加 Redis 负载

## 监控与调试

### 日志记录

中间件支持传入 logger 实例记录限流事件：

```go
import (
    "github.com/surge-go/aurora/core/logger"
    "github.com/surge-go/aurora/middleware"
)

// 创建 logger 实例
log := logger.New(logger.Config{
    Level:  logger.LevelInfo,
    Format: logger.FormatJSON,
})

// 传入 logger
engine.POST("/auth/login",
    middleware.IPRateLimiter(loginLimiter,
        middleware.WithLogger(log), // 记录限流事件
        middleware.WithFailOpen(),  // Redis 故障时放行
    ),
    handleLogin,
)
```

**记录的事件**：
- **限流拒绝**：记录 IP、剩余配额、重试时间
- **Redis 故障**（FailOpen）：记录错误原因，便于运维排查
- **Redis 故障**（FailClosed）：记录错误原因和拒绝决策

**日志示例**（JSON 格式）：

```json
{
  "level": "warn",
  "msg": "rate limit exceeded",
  "ip": "192.168.1.1",
  "remaining": 0,
  "retry_after_sec": 45
}

{
  "level": "error",
  "msg": "rate limiter unavailable, fail open",
  "error": "redis: connection refused"
}
```

### 手动记录限流事件

如果不使用中间件，可以手动记录：

```go
allowed, remaining, retryAfter, err := limiter.Allow(ctx, ip)
if err != nil {
    log.Error("rate limit check failed",
        "ip", ip,
        "error", err,
    )
}

if !allowed {
    log.Warn("rate limit exceeded",
        "ip", ip,
        "remaining", remaining,
        "retry_after", retryAfter,
    )
}
```

### Prometheus 指标（示例）

```go
var (
    rateLimitAllowed = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "ratelimit_requests_allowed_total",
            Help: "Total number of allowed requests",
        },
        []string{"limiter"},
    )

    rateLimitRejected = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "ratelimit_requests_rejected_total",
            Help: "Total number of rejected requests",
        },
        []string{"limiter"},
    )
)

// 中间件中记录指标
if allowed {
    rateLimitAllowed.WithLabelValues("login").Inc()
} else {
    rateLimitRejected.WithLabelValues("login").Inc()
}
```

## 故障排查

### 1. 限流器无效（所有请求都通过）

**可能原因**：
- HMACKey 配置错误（每次重启生成不同的摘要）
- Redis 连接失败 + FailOpen 策略

**排查步骤**：
```bash
# 检查 Redis 键是否存在
redis-cli KEYS "aurora:ratelimit:*"

# 检查键的类型和内容
redis-cli TYPE "aurora:ratelimit:{ratelimit}:state:xxx"
redis-cli ZRANGE "aurora:ratelimit:{ratelimit}:state:xxx" 0 -1 WITHSCORES
```

### 2. 限流过于严格（正常请求被拒绝）

**可能原因**：
- `Limit` 配置过小
- `Window` 配置过长
- 多个服务共享同一个 `KeyPrefix`

**排查步骤**：
```go
// 查看实际配置
fmt.Printf("Limit: %d, Window: %v\n", limiter.Limit(), limiter.Window())

// 查看剩余配额
allowed, remaining, _, _ := limiter.Allow(ctx, ip)
fmt.Printf("Allowed: %v, Remaining: %d\n", allowed, remaining)
```

### 3. Redis 性能问题

**可能原因**：
- 窗口内请求数过多（N > 10000）
- Redis 单机 QPS 达到瓶颈
- 网络延迟高

**排查步骤**：
```bash
# 查看 Redis 慢日志
redis-cli SLOWLOG GET 10

# 查看 Redis INFO
redis-cli INFO stats

# 查看网络延迟
redis-cli --latency
```

## 常见问题

### Q: 为什么必须使用 HMAC？

**A**: 限流器的 Redis 键包含敏感信息（IP 地址、用户 ID），HMAC 可以：
- 防止敏感信息泄露到 Redis
- 固定键长度（64 字符），提高性能
- 支持多租户隔离（不同的 HMACKey 生成不同的摘要）

### Q: 滑动窗口和固定窗口的区别？

| 算法 | 优点 | 缺点 | 适用场景 |
|------|------|------|---------|
| **固定窗口** | 实现简单，性能高 | 窗口边界有突发流量 | 粗粒度限流 |
| **滑动窗口** | 限流更精确 | Redis 操作稍多 | 精确限流（本包实现） |

### Q: 如何实现 IP 白名单？

**A**: 在中间件中提前检查：

```go
func IPWhitelistRateLimiter(limiter *ratelimit.Limiter, whitelist []string) aurora.HandlerFunc {
    whitelistMap := make(map[string]struct{})
    for _, ip := range whitelist {
        whitelistMap[ip] = struct{}{}
    }

    return func(c *aurora.Context) {
        ip := extractIP(c)

        // 白名单 IP 直接放行
        if _, ok := whitelistMap[ip]; ok {
            c.Next()
            return
        }

        // 其他 IP 执行限流检查
        allowed, remaining, retryAfter, err := limiter.Allow(c.Context(), ip)
        // ...
    }
}
```

### Q: 如何实现按用户 ID 限流？

**A**: 自定义 IP 提取器：

```go
middleware.IPRateLimiter(limiter, middleware.WithIPExtractor(func(c *aurora.Context) string {
    // 从 JWT token 或 session 中提取用户 ID
    userID := c.GetString("user_id")
    return userID
}))
```

## 贡献与反馈

- 报告 Bug：https://github.com/surge-go/aurora/issues
- 功能建议：https://github.com/surge-go/aurora/discussions
- 贡献代码：https://github.com/surge-go/aurora/pulls

## 许可证

MIT License
