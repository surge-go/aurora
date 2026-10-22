# core/ratelimit 设计文档

## 1. 设计范围

`core/ratelimit` 提供基于 Redis 的分布式限流器，用于保护系统资源免受滥用和攻击。

### 1.1 核心功能

- **精确滑动窗口限流**：使用 Redis Sorted Set 实现毫秒级精确的滑动窗口算法
- **隐私保护**：使用 HMAC-SHA256 对限流键进行摘要，防止敏感信息泄露到 Redis
- **原子操作**：所有限流检查和计数更新在单个 Lua 脚本中完成，保证并发安全
- **自动过期**：利用 Redis TTL 自动清理过期数据，无需额外的定时任务
- **标准化响应**：返回 `(allowed, remaining, retryAfter, error)` 支持标准 HTTP 限流响应头

### 1.2 适用场景

| 场景 | 限流维度 | 典型配置 |
|------|---------|---------|
| **登录接口防暴力破解** | IP 地址 | 10 次/分钟 |
| **API 配额管理** | 用户 ID / API Key | 1000 次/小时 |
| **资源访问频率限制** | 资源 ID | 100 次/分钟 |
| **消息队列速率控制** | 队列名称 | 1000 次/秒 |
| **全局流量保护** | 固定键 | 10000 次/分钟 |

### 1.3 非目标

- ❌ **本地限流**：不支持基于内存的单机限流（使用 `golang.org/x/time/rate` 代替）
- ❌ **令牌桶算法**：当前版本只实现滑动窗口（未来可扩展）
- ❌ **配额分配**：不支持用户级配额的分配和管理（属于业务层职责）
- ❌ **动态限流**：不支持根据系统负载动态调整限流阈值
- ❌ **HTTP 集成**：核心包不依赖 HTTP 框架（适配器在 `middleware/ratelimit.go`）

---

## 2. 包结构与依赖

### 2.1 文件组织

```
core/ratelimit/
  ├── DESIGN.md          # 本文档（设计决策与实现细节）
  ├── README.md          # 用户使用指南（快速上手）
  ├── doc.go             # 包文档（并发安全性保证）
  ├── config.go          # 配置结构与验证
  ├── limiter.go         # 核心限流逻辑 + Lua 脚本
  ├── errors.go          # Sentinel 错误定义
  └── limiter_test.go    # 单元测试（使用 miniredis）
```

### 2.2 依赖关系

```
core/ratelimit (本包)
  │
  ├─> github.com/redis/go-redis/v9  (Redis 客户端)
  └─> crypto/hmac, crypto/sha256    (隐私保护)

middleware/ratelimit.go (HTTP 适配器)
  │
  ├─> core/ratelimit                (核心限流逻辑)
  └─> github.com/surge-go/aurora    (HTTP 框架)
```

**设计原则**：
- `core/ratelimit` 不依赖任何 HTTP 框架，可在任何场景复用
- HTTP 适配器只负责 IP 提取、响应头设置、错误映射

---

## 3. 核心模型

### 3.1 限流器配置

```go
type Config struct {
    KeyPrefix string        // Redis 键前缀（必须包含 {name} hash tag）
    HMACKey   string        // HMAC 密钥（≥32 字节）
    Limit     int           // 限流阈值（>0）
    Window    time.Duration // 时间窗口（≥1 秒）
}
```

**配置约束**：

| 字段 | 约束 | 理由 |
|------|------|------|
| `KeyPrefix` | 必须包含 `{name}` | Redis Cluster 需要 hash tag 保证同一限流器的所有键在同一 slot |
| `HMACKey` | ≥32 字节 | HMAC-SHA256 最小安全密钥长度 |
| `Limit` | >0 | 0 或负数无意义 |
| `Window` | ≥1 秒 | Redis EXPIRE 命令只接受秒级 TTL；JWT NumericDate 也是秒级 |

**默认值**：

```go
const (
    defaultKeyPrefix = "aurora:ratelimit:{ratelimit}"
    defaultLimit     = 100
    defaultWindowSec = 60
)
```

### 3.2 限流器实例

```go
type Limiter struct {
    rdb       redis.UniversalClient // Redis 客户端（支持单机/哨兵/集群）
    prefix    string                // Redis 键前缀
    hmacKey   []byte                // HMAC 密钥
    limit     int                   // 限流阈值
    windowSec int64                 // 时间窗口（秒）
}
```

**不可变性**：
- 所有字段在构造后不可修改（无 Setter 方法）
- 多个 goroutine 可安全并发调用 `Allow()`
- Redis 客户端本身也是并发安全的

---

## 4. Redis 状态与原子操作

### 4.1 键命名规范

```
{prefix}:state:{hash}
```

**示例**：

| 限流维度 | 原始键 | HMAC-SHA256 摘要 (hex) | 最终 Redis 键 |
|---------|--------|----------------------|--------------|
| IP 地址 | `192.168.1.1` | `a1b2c3...` | `aurora:ratelimit:{ratelimit}:state:a1b2c3...` |
| 用户 ID | `user:12345` | `d4e5f6...` | `aurora:ratelimit:{ratelimit}:state:d4e5f6...` |

**为什么需要 HMAC？**
- **隐私保护**：防止 IP 地址、用户 ID 等敏感信息以明文存储在 Redis
- **键长度固定**：SHA256 摘要固定 64 字符，避免超长键影响性能
- **碰撞概率**：2^256 碰撞概率极低（比 UUID v4 更安全）

### 4.2 数据结构：Sorted Set

每个限流键对应一个 Redis Sorted Set，存储时间窗口内的所有请求：

```
ZSET {prefix}:state:{hash}
  ├─ member: {request_id}  (随机 UUID，防止重复)
  └─ score:  {timestamp}   (Unix 时间戳，毫秒)
```

**示例**：

```redis
# IP 192.168.1.1 在最近 60 秒内的请求
ZRANGEBYSCORE aurora:ratelimit:{ratelimit}:state:a1b2c3... -inf +inf WITHSCORES

1) "550e8400-e29b-41d4-a716-446655440000"  # 请求 1
2) "1697894567123"                         # 时间戳（毫秒）
3) "550e8400-e29b-41d4-a716-446655440001"  # 请求 2
4) "1697894568456"
5) "550e8400-e29b-41d4-a716-446655440002"  # 请求 3
6) "1697894569789"
```

**为什么用 Sorted Set？**
- **范围删除**：`ZREMRANGEBYSCORE` 原子删除窗口外的过期请求
- **精确计数**：`ZCARD` 获取窗口内的请求数量
- **时间有序**：自动按时间戳排序，便于调试和监控

### 4.3 原子操作：Lua 脚本

所有限流逻辑在单个 Lua 脚本中完成，避免并发竞争：

```lua
-- allowScript: 检查限流并记录请求
local state_key = KEYS[1]          -- {prefix}:state:{hash}
local now = tonumber(ARGV[1])      -- 当前时间戳（毫秒）
local limit = tonumber(ARGV[2])    -- 限流阈值
local window_ms = tonumber(ARGV[3])-- 时间窗口（毫秒）
local request_id = ARGV[4]         -- 请求 UUID

-- 1. 删除窗口外的过期请求
local cutoff = now - window_ms
redis.call("ZREMRANGEBYSCORE", state_key, "-inf", cutoff)

-- 2. 统计窗口内的请求数
local current = redis.call("ZCARD", state_key)

-- 3. 检查是否超限
if current >= limit then
    -- 计算最早请求的时间戳
    local oldest = redis.call("ZRANGE", state_key, 0, 0, "WITHSCORES")
    local oldest_time = tonumber(oldest[2])
    local retry_after_ms = oldest_time + window_ms - now
    
    return {
        0,                    -- allowed = false
        0,                    -- remaining = 0
        retry_after_ms        -- 重试时间（毫秒）
    }
end

-- 4. 记录当前请求
redis.call("ZADD", state_key, now, request_id)

-- 5. 设置 TTL（窗口大小 + 1 秒缓冲）
redis.call("EXPIRE", state_key, math.ceil(window_ms / 1000) + 1)

-- 6. 返回结果
return {
    1,                    -- allowed = true
    limit - current - 1,  -- remaining = limit - current - 1
    0                     -- retry_after = 0
}
```

**脚本特性**：
1. **原子性**：Lua 脚本在 Redis 中是原子执行的（不会被其他命令打断）
2. **精确性**：使用**毫秒时间戳**，支持高频限流场景（可精确到 1000 次/秒）
3. **自动清理**：`ZREMRANGEBYSCORE` 在每次调用时清理过期数据
4. **TTL 管理**：`EXPIRE` 自动删除空闲键，节省内存

### 4.4 重置操作

```lua
-- resetScript: 删除限流状态
local state_key = KEYS[1]

redis.call("DEL", state_key)
return "OK"
```

**使用场景**：
- 管理员手动解除用户的限流
- 测试环境重置限流状态
- 异常情况下的人工干预

---

## 5. Limiter API

### 5.1 构造函数

```go
func New(rdb redis.UniversalClient, cfg Config) (*Limiter, error)
```

**参数验证**：
1. `rdb` 不能为 `nil`（使用反射检查 `redis.UniversalClient` 接口）
2. 调用 `cfg.Validate()` 验证配置（见 3.1 配置约束）
3. 自动应用默认值（`withDefaults()`）

**返回错误**：
- `ErrRedisRequired`：Redis 客户端为 nil
- `ErrInvalidConfig`：配置验证失败（嵌套具体错误原因）

### 5.2 核心方法

#### 5.2.1 Allow

```go
func (l *Limiter) Allow(ctx context.Context, key string) (allowed bool, remaining int, retryAfter time.Duration, err error)
```

**参数**：
- `ctx`：上下文（支持超时和取消）
- `key`：限流维度标识（IP 地址、用户 ID、资源 ID 等）

**返回值**：
- `allowed`：是否允许请求通过
  - `true`：允许通过，计数已增加
  - `false`：拒绝请求，计数未变化
- `remaining`：剩余配额（0 到 `limit-1`）
- `retryAfter`：重试等待时间
  - `allowed=true` 时为 0
  - `allowed=false` 时表示需要等待多久才能再次尝试
- `err`：错误（见 5.3 错误契约）

**行为**：
1. 验证 `key` 非空白
2. 使用 HMAC-SHA256 计算 `key` 的摘要
3. 调用 `allowScript` 执行限流检查
4. 解析 Lua 脚本返回值

**并发安全**：
- ✅ 多个 goroutine 可并发调用（Redis 客户端和 HMAC 都是并发安全的）
- ✅ Lua 脚本保证原子性（同一个 key 的并发请求不会竞争）

#### 5.2.2 Reset

```go
func (l *Limiter) Reset(ctx context.Context, key string) error
```

**用途**：删除指定 key 的限流状态（管理操作）

**返回错误**：
- `ErrKeyRequired`：key 为空
- `ErrStoreUnavailable`：Redis 操作失败

### 5.3 辅助方法

```go
// Limit 返回限流阈值（用于 HTTP 响应头）
func (l *Limiter) Limit() int

// Window 返回时间窗口（用于日志和监控）
func (l *Limiter) Window() time.Duration
```

---

## 6. 错误契约

### 6.1 Sentinel 错误

```go
var (
    ErrInvalidConfig    = errors.New("ratelimit: invalid config")
    ErrRedisRequired    = errors.New("ratelimit: redis client required")
    ErrKeyRequired      = errors.New("ratelimit: key is required")
    ErrStoreUnavailable = errors.New("ratelimit: store unavailable")
)
```

### 6.2 错误处理模式

| 场景 | 错误类型 | 包装方式 | 调用者行为 |
|------|---------|---------|----------|
| **配置无效** | `ErrInvalidConfig` | `fmt.Errorf("%w: 具体原因", ErrInvalidConfig)` | 启动时失败，修复配置 |
| **Redis 客户端为 nil** | `ErrRedisRequired` | 直接返回 | 启动时失败，检查初始化逻辑 |
| **Key 为空** | `ErrKeyRequired` | 直接返回 | 修复调用代码，传入有效 key |
| **Redis 连接失败** | `ErrStoreUnavailable` | `fmt.Errorf("%w: %w", ErrStoreUnavailable, redisErr)` | 降级处理（FailOpen/FailClosed） |
| **Context 取消** | `context.Canceled` | 直接返回（不包装） | 传播取消信号 |
| **Context 超时** | `context.DeadlineExceeded` | 直接返回（不包装） | 传播超时信号 |

**错误检查示例**：

```go
allowed, _, _, err := limiter.Allow(ctx, ip)
if err != nil {
    if errors.Is(err, context.Canceled) {
        // 请求被取消，停止处理
        return
    }
    if errors.Is(err, ratelimit.ErrStoreUnavailable) {
        // Redis 故障，降级处理
        if failOpen {
            // 放行请求
        } else {
            // 拒绝请求
        }
    }
}
```

---

## 7. 安全约定

### 7.1 隐私保护

**问题**：限流器的 Redis 键包含敏感信息（IP 地址、用户 ID）

**解决方案**：使用 HMAC-SHA256 摘要

```go
func (l *Limiter) keyHash(key string) (string, error) {
    h := hmac.New(sha256.New, l.hmacKey)
    _, _ = h.Write([]byte(key))
    return hex.EncodeToString(h.Sum(nil)), nil
}
```

**安全属性**：
- **不可逆**：无法从摘要反推原始 key
- **唯一性**：不同的 HMACKey 生成不同的摘要（隔离多租户）
- **确定性**：相同的 key 和 HMACKey 总是生成相同的摘要

**密钥管理**：
- HMACKey 必须 ≥32 字节（256 位）
- 生产环境使用环境变量或密钥管理服务（如 AWS Secrets Manager）
- 定期轮换密钥（旧数据会自动过期）

### 7.2 IP 提取安全（中间件层）

**信任代理的风险**：

| 场景 | X-Forwarded-For | 正确处理 | 错误处理 |
|------|----------------|---------|---------|
| **直连客户端** | 无 | 使用 `RemoteAddr` | ❌ 信任空值 |
| **可信代理** | `1.2.3.4, proxy1` | 使用 `1.2.3.4` | ✅ |
| **恶意伪造** | `8.8.8.8, 1.2.3.4` | 使用 `RemoteAddr` | ❌ 使用 `8.8.8.8` |

**中间件默认策略**（`middleware/ratelimit.go`）：

```go
func defaultIPExtractor(c *aurora.Context) string {
    // 1. 优先使用 X-Forwarded-For（假设已配置可信代理）
    if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
        if clientIP, _, found := strings.Cut(xff, ","); found {
            return strings.TrimSpace(clientIP)
        }
        return strings.TrimSpace(xff)
    }
    
    // 2. 回退到 X-Real-IP
    if xri := c.GetHeader("X-Real-IP"); xri != "" {
        return strings.TrimSpace(xri)
    }
    
    // 3. 最后使用 RemoteAddr
    if ip, _, err := net.SplitHostPort(c.Request().RemoteAddr); err == nil {
        return ip
    }
    return c.Request().RemoteAddr
}
```

**自定义提取器**（生产环境推荐）：

```go
// 只信任来自 Cloudflare 的 CF-Connecting-IP
middleware.IPRateLimiter(limiter, middleware.WithIPExtractor(func(c *aurora.Context) string {
    if cfIP := c.GetHeader("CF-Connecting-IP"); cfIP != "" {
        return cfIP
    }
    // 非 Cloudflare 流量，使用 RemoteAddr
    if ip, _, err := net.SplitHostPort(c.Request().RemoteAddr); err == nil {
        return ip
    }
    return c.Request().RemoteAddr
}))
```

---

## 8. HTTP/Aurora 集成边界

### 8.1 职责分离

| 层级 | 包 | 职责 | 不应包含 |
|------|-----|------|---------|
| **核心层** | `core/ratelimit` | 限流逻辑、Redis 操作、隐私保护 | HTTP 请求、响应、路由 |
| **适配层** | `middleware/ratelimit.go` | IP 提取、响应头设置、错误映射 | 限流算法、Redis 操作 |

### 8.2 中间件 API

```go
// 基础用法
router.POST("/login", 
    middleware.IPRateLimiter(limiter),
    handlers.Login,
)

// 自定义 IP 提取器
router.POST("/api/data",
    middleware.IPRateLimiter(limiter, 
        middleware.WithIPExtractor(customIPExtractor),
    ),
    handlers.GetData,
)

// 故障降级策略
router.Use(middleware.IPRateLimiter(globalLimiter,
    middleware.WithFailOpen(), // Redis 故障时放行请求
))

// 自定义响应头显示值
router.POST("/upload",
    middleware.IPRateLimiter(limiter,
        middleware.WithRateLimitValue(100), // 显示为 100，实际由 limiter 控制
    ),
    handlers.Upload,
)
```

### 8.3 标准 HTTP 响应头

| 响应头 | 值 | 说明 |
|--------|-----|------|
| `X-RateLimit-Limit` | `{limit}` | 限流阈值（窗口内最大请求数） |
| `X-RateLimit-Remaining` | `{remaining}` | 剩余配额（0 到 limit-1） |
| `Retry-After` | `{seconds}` | 被拒绝时需要等待的秒数 |

**示例**：

```http
# 允许通过（第 1 次请求）
HTTP/1.1 200 OK
X-RateLimit-Limit: 10
X-RateLimit-Remaining: 9

# 允许通过（第 10 次请求）
HTTP/1.1 200 OK
X-RateLimit-Limit: 10
X-RateLimit-Remaining: 0

# 拒绝请求（第 11 次请求）
HTTP/1.1 429 Too Many Requests
X-RateLimit-Limit: 10
X-RateLimit-Remaining: 0
Retry-After: 45

{
  "error": "too many requests",
  "retry_after": 45.123
}
```

---

## 9. 测试与验收

### 9.1 测试策略

| 测试类型 | 工具 | 覆盖场景 |
|---------|------|---------|
| **单元测试** | `miniredis` | 配置验证、限流逻辑、错误处理、并发安全 |
| **集成测试** | 真实 Redis | 集群模式、Lua 脚本兼容性、性能基准 |
| **HTTP 测试** | `httptest` | 中间件集成、IP 提取、响应头 |

### 9.2 测试用例

#### 9.2.1 配置验证测试

```go
func TestNew_ValidatesConfiguration(t *testing.T) {
    cases := []struct {
        name    string
        prepare func(*Config)
        wantErr error
    }{
        {
            name:    "missing redis client",
            prepare: func(cfg *Config) { /* rdb = nil */ },
            wantErr: ErrRedisRequired,
        },
        {
            name:    "invalid key prefix (no hash tag)",
            prepare: func(cfg *Config) { cfg.KeyPrefix = "no-hash-tag" },
            wantErr: ErrInvalidConfig,
        },
        {
            name:    "hmac key too short",
            prepare: func(cfg *Config) { cfg.HMACKey = strings.Repeat("k", 31) },
            wantErr: ErrInvalidConfig,
        },
        {
            name:    "zero limit",
            prepare: func(cfg *Config) { cfg.Limit = 0 },
            wantErr: nil, // 应用默认值
        },
        {
            name:    "window too small",
            prepare: func(cfg *Config) { cfg.Window = 500 * time.Millisecond },
            wantErr: ErrInvalidConfig,
        },
    }
    
    for _, tc := range cases {
        t.Run(tc.name, func(t *testing.T) {
            mr := miniredis.RunT(t)
            rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
            
            cfg := DefaultConfig()
            cfg.HMACKey = strings.Repeat("h", 32)
            tc.prepare(&cfg)
            
            _, err := New(rdb, cfg)
            if tc.wantErr != nil {
                assert.ErrorIs(t, err, tc.wantErr)
            } else {
                assert.NoError(t, err)
            }
        })
    }
}
```

#### 9.2.2 限流逻辑测试

```go
func TestLimiter_Allow_SlidingWindow(t *testing.T) {
    limiter, mr, _ := newTestLimiter(t, func(cfg *Config) {
        cfg.Limit = 3
        cfg.Window = 10 * time.Second
    })
    
    ctx := context.Background()
    
    // T0: 第 1 次请求（允许）
    mr.SetTime(time.Unix(100, 0))
    allowed, remaining, _, err := limiter.Allow(ctx, "192.168.1.1")
    assert.NoError(t, err)
    assert.True(t, allowed)
    assert.Equal(t, 2, remaining)
    
    // T0: 第 2 次请求（允许）
    allowed, remaining, _, err = limiter.Allow(ctx, "192.168.1.1")
    assert.NoError(t, err)
    assert.True(t, allowed)
    assert.Equal(t, 1, remaining)
    
    // T0: 第 3 次请求（允许）
    allowed, remaining, _, err = limiter.Allow(ctx, "192.168.1.1")
    assert.NoError(t, err)
    assert.True(t, allowed)
    assert.Equal(t, 0, remaining)
    
    // T0: 第 4 次请求（拒绝）
    allowed, remaining, retryAfter, err := limiter.Allow(ctx, "192.168.1.1")
    assert.NoError(t, err)
    assert.False(t, allowed)
    assert.Equal(t, 0, remaining)
    assert.Greater(t, retryAfter.Seconds(), 0.0)
    
    // T5: 时间前进 5 秒（仍拒绝）
    mr.SetTime(time.Unix(105, 0))
    allowed, _, _, err = limiter.Allow(ctx, "192.168.1.1")
    assert.NoError(t, err)
    assert.False(t, allowed)
    
    // T11: 时间前进 11 秒（第 1 次请求过期，允许）
    mr.SetTime(time.Unix(111, 0))
    allowed, remaining, _, err = limiter.Allow(ctx, "192.168.1.1")
    assert.NoError(t, err)
    assert.True(t, allowed)
    assert.Equal(t, 2, remaining) // 第 2、3 次请求仍在窗口内
}
```

#### 9.2.3 并发安全测试

```go
func TestLimiter_Allow_Concurrent(t *testing.T) {
    limiter, _, _ := newTestLimiter(t, func(cfg *Config) {
        cfg.Limit = 100
        cfg.Window = 10 * time.Second
    })
    
    ctx := context.Background()
    const goroutines = 50
    const requestsPerGoroutine = 10
    
    var wg sync.WaitGroup
    var mu sync.Mutex
    allowedCount := 0
    
    for i := 0; i < goroutines; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            for j := 0; j < requestsPerGoroutine; j++ {
                allowed, _, _, err := limiter.Allow(ctx, "192.168.1.1")
                assert.NoError(t, err)
                if allowed {
                    mu.Lock()
                    allowedCount++
                    mu.Unlock()
                }
            }
        }()
    }
    
    wg.Wait()
    
    // 总请求 500 次，限流 100 次，应该允许 100 次
    assert.Equal(t, 100, allowedCount)
}
```

#### 9.2.4 HTTP 中间件测试

```go
func TestIPRateLimiter_Integration(t *testing.T) {
    limiter, _ := newTestRateLimiter(t, 2, 10*time.Second)
    
    engine, err := aurora.New(aurora.DefaultConfig())
    require.NoError(t, err)
    engine.Use(middleware.IPRateLimiter(limiter))
    engine.GET("/test", func(c *aurora.Context) {
        c.JSON(http.StatusOK, map[string]any{"status": "ok"})
    })
    
    // 第 1 次请求（允许）
    req := httptest.NewRequest("GET", "/test", nil)
    req.RemoteAddr = "192.168.1.1:12345"
    w := httptest.NewRecorder()
    engine.ServeHTTP(w, req)
    
    assert.Equal(t, http.StatusOK, w.Code)
    assert.Equal(t, "2", w.Header().Get("X-RateLimit-Limit"))
    assert.Equal(t, "1", w.Header().Get("X-RateLimit-Remaining"))
    
    // 第 3 次请求（拒绝）
    req = httptest.NewRequest("GET", "/test", nil)
    req.RemoteAddr = "192.168.1.1:12345"
    w = httptest.NewRecorder()
    engine.ServeHTTP(w, req)
    
    assert.Equal(t, http.StatusTooManyRequests, w.Code)
    assert.NotEmpty(t, w.Header().Get("Retry-After"))
}
```

### 9.3 验收标准

- ✅ 所有单元测试通过（覆盖率 ≥90%）
- ✅ Lua 脚本在 Redis 6.0+ 和 7.0+ 上兼容
- ✅ 并发测试无竞争条件（`go test -race`）
- ✅ HTTP 中间件正确设置响应头
- ✅ Redis 连接失败时降级策略生效
- ✅ 配置验证捕获所有无效输入

---

## 10. 实施阶段

### Phase 1: 核心限流器 ✅

- [x] `config.go`：配置结构与验证
- [x] `limiter.go`：滑动窗口算法 + Lua 脚本
- [x] `errors.go`：Sentinel 错误定义
- [x] `limiter_test.go`：单元测试（miniredis）

### Phase 2: HTTP 中间件 ✅

- [x] `middleware/ratelimit.go`：IP 限流中间件
- [x] `middleware/ratelimit_test.go`：HTTP 集成测试

### Phase 3: 文档与示例 🔄

- [x] `DESIGN.md`：本文档
- [ ] `README.md`：使用指南
- [ ] `doc.go`：包文档

### Phase 4: 生产验证（后续）

- [ ] 集成测试（真实 Redis Cluster）
- [ ] 性能基准测试（QPS、延迟）
- [ ] 监控指标（Prometheus exporter）
- [ ] 生产环境部署与观察

---

## 11. 待确认决策

### 11.1 是否支持令牌桶算法？

**当前状态**：只实现滑动窗口算法

**令牌桶的优势**：
- 支持突发流量（burst capacity）
- 更符合某些业务场景（如 API 配额）

**滑动窗口的优势**：
- 实现简单，Redis 操作更少
- 限流效果更精确（不允许突发）

**决策**：
- **短期**：只提供滑动窗口（满足 90% 场景）
- **长期**：可扩展支持多算法（通过 `Algorithm` 配置项）

### 11.2 是否需要分布式锁？

**问题**：Lua 脚本已保证原子性，是否还需要额外的分布式锁？

**分析**：
- ✅ Lua 脚本在 Redis 中是原子执行的（单个 key 的并发请求不会竞争）
- ✅ Sorted Set 的 ZADD/ZCARD 操作是原子的
- ❌ 不需要额外的分布式锁（会降低性能）

**决策**：不使用分布式锁

### 11.3 是否支持动态限流？

**场景**：根据系统负载自动调整限流阈值

**实现难度**：
- 需要全局状态同步（CPU、内存、队列长度）
- 需要自适应算法（可能引入复杂性）

**决策**：
- **短期**：不支持（使用静态配置 + 多级限流）
- **长期**：可通过外部系统调整限流配置（如 Consul、etcd）

---

## 12. 参考资料

### 12.1 算法与理论

- [Rate Limiting Strategies and Techniques](https://cloud.google.com/architecture/rate-limiting-strategies-techniques)
- [IETF RFC 6585: HTTP Status Code 429](https://datatracker.ietf.org/doc/html/rfc6585)
- [Redis Lua Scripting](https://redis.io/docs/manual/programmability/eval-intro/)

### 12.2 类似实现

- [go-redis/redis_rate](https://github.com/go-redis/redis_rate)：GCRA 算法
- [throttled](https://github.com/throttled/throttled)：GCR 和 Leaky Bucket
- [Cloudflare Rate Limiting](https://developers.cloudflare.com/waf/rate-limiting-rules/)

### 12.3 Aurora 内部参考

- `core/auth/attempt`：登录失败计数（相似的 Lua 脚本模式）
- `middleware/auth.go`：HTTP 适配器模式
- `core/redis`：Redis 客户端配置

---

**文档版本**: v1.0  
**最后更新**: 2026-10-21  
**作者**: Aurora Team
