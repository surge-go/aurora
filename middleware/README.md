# Aurora 中间件

Aurora 框架的官方中间件集合，提供常用的 HTTP 中间件功能。

## ✨ 核心特性

- 🚀 **高性能**: 对象池复用、并发控制、零拷贝设计
- 🔒 **生产就绪**: 完整的测试覆盖、错误处理和降级策略
- 🎯 **配置灵活**: 统一的配置模式，支持默认、严格和自定义配置
- 📦 **最小依赖**: 大多数中间件零外部依赖，仅基于 Go 标准库
- 🔌 **易于集成**: 遵循 Aurora HandlerFunc 签名，即插即用
- 📖 **文档完善**: 每个中间件提供独立文档和使用示例

## 📦 可用中间件

| 中间件 | 说明 | 文档 |
|--------|------|------|
| **Auth** | JWT 认证，自动刷新令牌 | [AUTH.md](./AUTH.md) |
| **RateLimit** | 基于 IP 的限流保护 | [RATELIMIT.md](./RATELIMIT.md) |
| **CORS** | 跨域资源共享配置 | [CORS.md](./CORS.md) |
| **Gzip** | 响应压缩，减少传输数据 70-90% | [GZIP.md](./GZIP.md) |
| **Security** | 安全响应头（HSTS、CSP、XFO 等） | [SECURITY.md](./SECURITY.md) |
| **BodyLimit** | 请求体大小限制，防止资源耗尽 | [BODYLIMIT.md](./BODYLIMIT.md) |

## 🚀 快速开始

### 安装

```bash
go get -u github.com/surge-go/aurora
```

### 依赖说明

大多数中间件**零外部依赖**，仅依赖 Go 标准库和 Aurora 核心：
- ✅ CORS
- ✅ Gzip
- ✅ Security
- ✅ BodyLimit

部分中间件依赖 Aurora 核心组件和 Redis：
- **Auth**: 依赖 `aurora/core/auth` 和 Redis（用于 token 存储和轮换）
- **RateLimit**: 依赖 `aurora/core/ratelimit` 和 Redis（用于分布式限流计数）

```bash
# 如果需要使用 Auth 或 RateLimit，还需要安装 Redis 客户端
go get -u github.com/redis/go-redis/v9
```

### 基础使用

```go
package main

import (
    "github.com/surge-go/aurora"
    "github.com/surge-go/aurora/middleware"
)

func main() {
    engine := aurora.New()
    
    // 使用默认配置的中间件
    engine.Use(middleware.CORS(middleware.DefaultCORSConfig()))
    engine.Use(middleware.Security(middleware.DefaultSecurityConfig()))
    engine.Use(middleware.BodyLimit(middleware.DefaultBodyLimitConfig()))
    engine.Use(middleware.Gzip(middleware.DefaultGzipConfig()))
    
    engine.GET("/", func(c *aurora.Context) {
        c.JSON(200, map[string]string{
            "message": "Hello, Aurora!",
        })
    })
    
    engine.Run(":8080")
}
```

### 推荐的中间件顺序

```go
engine := aurora.New()

// 1. 日志 - 记录所有请求
engine.Use(middleware.Logger())

// 2. 恢复 - 捕获 panic
engine.Use(middleware.Recovery())

// 3. 安全头 - 早期设置安全响应头
engine.Use(middleware.Security(middleware.DefaultSecurityConfig()))

// 4. CORS - 处理跨域请求
engine.Use(middleware.CORS(middleware.DefaultCORSConfig()))

// 5. 认证 - 验证请求身份（如果需要）
// authManager := coreauth.NewManager(...)
// engine.Use(middleware.Auth(authManager))

// 6. 限流 - 防止滥用（如果需要）
// limiter, _ := ratelimit.New(redisClient, ratelimit.Config{...})
// engine.Use(middleware.IPRateLimiter(limiter))

// 7. Body Limit - 限制请求体大小（在解析前）
engine.Use(middleware.BodyLimit(middleware.DefaultBodyLimitConfig()))

// 8. Gzip - 压缩响应（在业务逻辑后）
engine.Use(middleware.Gzip(middleware.DefaultGzipConfig()))

// 9. 业务路由
engine.GET("/api/data", dataHandler)
```

## 📚 详细文档

### Auth JWT 认证

基于 JWT 的请求认证中间件，支持自动令牌刷新和主动刷新策略。

**特性**:
- ✅ Bearer Token 验证
- ✅ 自动令牌刷新（Access Token 过期时）
- ✅ 主动刷新策略（临近过期时提前刷新）
- ✅ 单次刷新并发控制（singleflight）
- ✅ 灵活的配置选项

```go
// 初始化 Auth Manager
authManager, err := coreauth.NewManager(&coreauth.Config{
    Redis:              redisClient,
    AccessTokenSecret:  os.Getenv("JWT_ACCESS_SECRET"),
    RefreshTokenSecret: os.Getenv("JWT_REFRESH_SECRET"),
    AccessTokenTTL:     15 * time.Minute,
    RefreshTokenTTL:    7 * 24 * time.Hour,
})

// 使用默认配置
engine.Use(middleware.Auth(authManager))

// 主动刷新配置（临近过期前 5 分钟自动刷新）
engine.Use(middleware.Auth(authManager,
    middleware.WithPreRefreshWindow(5 * time.Minute),
))

// 在处理器中获取 claims
engine.GET("/profile", func(c *aurora.Context) {
    claims, ok := middleware.Claims(c)
    if !ok {
        c.JSON(401, map[string]string{"error": "unauthorized"})
        return
    }
    
    c.JSON(200, map[string]any{
        "user_id": claims.Subject,
        "role": claims.Get("role"),
    })
})
```

**适用场景**: API 认证、用户身份验证、权限控制

[📖 完整文档](./AUTH.md)

---

### RateLimit IP 限流

基于 Redis 的 IP 限流中间件，防止单个客户端滥用 API。

**特性**:
- ✅ 滑动窗口限流算法
- ✅ 基于 IP 的自动识别（支持代理头）
- ✅ 标准限流响应头（X-RateLimit-*）
- ✅ 故障降级策略（fail-open）
- ✅ 自定义 IP 提取逻辑

```go
// 创建限流器
limiter, err := ratelimit.New(redisClient, ratelimit.Config{
    KeyPrefix: "api:limit:ip:",
    Limit:     100,                // 100 次请求
    Window:    time.Minute,        // 每分钟
})

// 全局限流（故障时放行）
engine.Use(middleware.IPRateLimiter(limiter,
    middleware.WithFailOpen(),
    middleware.WithLogger(logger),
))

// 针对特定路由限流
loginLimiter, _ := ratelimit.New(redisClient, ratelimit.Config{
    KeyPrefix: "login:limit:ip:",
    Limit:     5,                  // 5 次登录尝试
    Window:    time.Minute,        // 每分钟
})

engine.POST("/login",
    middleware.IPRateLimiter(loginLimiter), // 故障时拒绝（安全优先）
    handlers.Login,
)
```

**适用场景**: API 保护、登录限制、防止爬虫、防止暴力破解

[📖 完整文档](./RATELIMIT.md)

---

### CORS 跨域资源共享

配置跨域资源共享（CORS）策略，支持通配符和精确匹配。

**特性**:
- ✅ 灵活的源配置（精确匹配、通配符）
- ✅ 预检请求（OPTIONS）缓存
- ✅ 凭证支持（Credentials）
- ✅ 自定义暴露头
- ✅ 私有网络请求支持

```go
// 使用默认配置（允许所有源）
engine.Use(middleware.CORS(middleware.DefaultCORSConfig()))

// 生产环境配置（限制源）
engine.Use(middleware.CORS(middleware.CORSConfig{
    AllowOrigins:     []string{"https://app.example.com", "https://*.example.com"},
    AllowMethods:     []string{"GET", "POST", "PUT", "DELETE"},
    AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
    ExposeHeaders:    []string{"X-Request-ID"},
    AllowCredentials: true,
    MaxAge:           12 * time.Hour,
}))
```

**适用场景**: 前后端分离应用、多域名应用、第三方 API 集成

[📖 完整文档](./CORS.md)

---

### Gzip 响应压缩

自动压缩 HTTP 响应，减少网络传输数据量。

**特性**:
- ✅ 自动检测客户端支持
- ✅ 可配置压缩级别（1-9）
- ✅ 最小压缩长度阈值
- ✅ 排除已压缩类型（图片、视频等）
- ✅ gzip.Writer 对象池复用

```go
// 使用默认配置
engine.Use(middleware.Gzip(middleware.DefaultGzipConfig()))

// 自定义配置
engine.Use(middleware.Gzip(middleware.GzipConfig{
    Level:     gzip.BestSpeed, // 快速压缩
    MinLength: 1024,            // 1KB 以上才压缩
}))
```

**适用场景**: JSON API、HTML 页面、静态资源

[📖 完整文档](./GZIP.md)

---

### Security 安全响应头

添加 HTTP 安全响应头，防御常见 Web 攻击。

**特性**:
- ✅ X-Frame-Options (防点击劫持)
- ✅ X-Content-Type-Options (防 MIME 嗅探)
- ✅ Strict-Transport-Security (强制 HTTPS)
- ✅ Content-Security-Policy (内容安全策略)
- ✅ Referrer-Policy (引用策略)
- ✅ Permissions-Policy (权限策略)

```go
// 使用默认配置
engine.Use(middleware.Security(middleware.DefaultSecurityConfig()))

// 严格模式（生产环境推荐）
engine.Use(middleware.Security(middleware.StrictSecurityConfig()))

// 自定义配置
engine.Use(middleware.Security(middleware.SecurityConfig{
    XFrameOptions: "DENY",
    HSTSMaxAge:    31536000, // 1 年
    ContentSecurityPolicy: "default-src 'self'",
}))
```

**适用场景**: 所有生产环境的 Web 应用

[📖 完整文档](./SECURITY.md)

---

### BodyLimit 请求体限制

限制 HTTP 请求体的大小，防止恶意或意外的大请求耗尽资源。

**特性**:
- ✅ 快速路径：先检查 Content-Length
- ✅ 流式限制：实际读取时限制
- ✅ 跳过特定路径
- ✅ 自定义超限响应

```go
// 使用默认配置 (4MB)
engine.Use(middleware.BodyLimit(middleware.DefaultBodyLimitConfig()))

// 自定义配置
engine.Use(middleware.BodyLimit(middleware.BodyLimitConfig{
    Limit: 10 * 1024 * 1024, // 10MB
}))

// 针对不同路由设置不同限制
api := engine.Group("/api")
api.Use(middleware.BodyLimit(middleware.BodyLimitConfig{
    Limit: 1 * 1024 * 1024, // API: 1MB
}))

upload := engine.Group("/upload")
upload.Use(middleware.BodyLimit(middleware.BodyLimitConfig{
    Limit: 100 * 1024 * 1024, // 上传: 100MB
}))
```

**适用场景**: 所有接受 POST/PUT 请求的应用

[📖 完整文档](./BODYLIMIT.md)

---

## 🔧 配置模式

所有中间件遵循统一的配置模式：

### 1. 默认配置

```go
// 使用 Default*Config() 获取默认配置
engine.Use(middleware.CORS(middleware.DefaultCORSConfig()))
engine.Use(middleware.Gzip(middleware.DefaultGzipConfig()))
engine.Use(middleware.Security(middleware.DefaultSecurityConfig()))
engine.Use(middleware.BodyLimit(middleware.DefaultBodyLimitConfig()))
```

**适用**: 快速开始、开发环境、标准场景

### 2. 严格配置

```go
// Security 提供了严格模式（生产环境推荐）
engine.Use(middleware.Security(middleware.StrictSecurityConfig()))
```

**适用**: 生产环境、安全性要求高的场景

### 3. 自定义配置

```go
// 创建并修改配置结构体
config := middleware.GzipConfig{
    Level:     gzip.BestCompression,
    MinLength: 512,
    ExcludedPaths: []string{"/api/metrics"},
}
engine.Use(middleware.Gzip(config))

// Auth 中间件使用选项模式
engine.Use(middleware.Auth(authManager,
    middleware.WithPreRefreshWindow(5 * time.Minute),
    middleware.WithClaimsKey("custom.claims.key"),
))

// RateLimit 使用选项模式
engine.Use(middleware.IPRateLimiter(limiter,
    middleware.WithFailOpen(),
    middleware.WithLogger(logger),
))
```

**适用**: 特殊需求、性能优化、复杂场景

### 4. 基于依赖的配置

某些中间件需要外部依赖：

```go
// Auth 需要 Manager（依赖 Redis）
authManager, _ := coreauth.NewManager(&coreauth.Config{
    Redis:              redisClient,
    AccessTokenSecret:  secret,
    AccessTokenTTL:     15 * time.Minute,
})
engine.Use(middleware.Auth(authManager))

// RateLimit 需要 Limiter（依赖 Redis）
limiter, _ := ratelimit.New(redisClient, ratelimit.Config{
    Limit:  100,
    Window: time.Minute,
})
engine.Use(middleware.IPRateLimiter(limiter))
```

**适用**: 需要状态存储的中间件（认证、限流等）

## 🎯 使用场景

### 场景 1: 标准 API 服务（带认证）

```go
engine := aurora.New()

// 初始化认证管理器
authManager, _ := coreauth.NewManager(&coreauth.Config{
    Redis:              redisClient,
    AccessTokenSecret:  os.Getenv("JWT_ACCESS_SECRET"),
    RefreshTokenSecret: os.Getenv("JWT_REFRESH_SECRET"),
    AccessTokenTTL:     15 * time.Minute,
    RefreshTokenTTL:    7 * 24 * time.Hour,
})

// 全局中间件
engine.Use(middleware.Security(middleware.DefaultSecurityConfig()))
engine.Use(middleware.CORS(middleware.DefaultCORSConfig()))
engine.Use(middleware.BodyLimit(middleware.BodyLimitConfig{
    Limit: 1 * 1024 * 1024, // 1MB
}))
engine.Use(middleware.Gzip(middleware.GzipConfig{
    Level:     gzip.BestSpeed,
    MinLength: 1024,
}))

// 公开路由
engine.POST("/auth/login", handlers.Login)
engine.POST("/auth/register", handlers.Register)

// 需要认证的路由
api := engine.Group("/api/v1")
api.Use(middleware.Auth(authManager,
    middleware.WithPreRefreshWindow(5 * time.Minute), // 主动刷新
))
api.GET("/profile", handlers.GetProfile)
api.PUT("/profile", handlers.UpdateProfile)
api.GET("/users/:id", handlers.GetUser)
```

### 场景 2: 带限流保护的登录接口

```go
engine := aurora.New()

// 全局限流：100 req/min
globalLimiter, _ := ratelimit.New(redisClient, ratelimit.Config{
    KeyPrefix: "api:global:ip:",
    Limit:     100,
    Window:    time.Minute,
})
engine.Use(middleware.IPRateLimiter(globalLimiter,
    middleware.WithFailOpen(), // 限流器故障时放行
    middleware.WithLogger(logger),
))

// 登录限流：5 次/分钟（严格模式）
loginLimiter, _ := ratelimit.New(redisClient, ratelimit.Config{
    KeyPrefix: "auth:login:ip:",
    Limit:     5,
    Window:    time.Minute,
})
engine.POST("/auth/login",
    middleware.IPRateLimiter(loginLimiter), // 故障时拒绝
    handlers.Login,
)

// 注册限流：3 次/小时
registerLimiter, _ := ratelimit.New(redisClient, ratelimit.Config{
    KeyPrefix: "auth:register:ip:",
    Limit:     3,
    Window:    time.Hour,
})
engine.POST("/auth/register",
    middleware.IPRateLimiter(registerLimiter),
    handlers.Register,
)
```

### 场景 3: 标准 API 服务（无认证）

```go
engine := aurora.New()

// 核心中间件
engine.Use(middleware.Security(middleware.DefaultSecurityConfig()))
engine.Use(middleware.BodyLimit(middleware.BodyLimitConfig{
    Limit: 1 * 1024 * 1024, // API 限制 1MB
}))
engine.Use(middleware.Gzip(middleware.GzipConfig{
    Level:     gzip.BestSpeed, // 快速压缩
    MinLength: 1024,
}))

// 业务路由
api := engine.Group("/api/v1")
api.POST("/users", createUser)
api.GET("/users/:id", getUser)
```

### 场景 4: 文件上传服务

```go
engine := aurora.New()

// 全局配置
engine.Use(middleware.Security(middleware.DefaultSecurityConfig()))

// API 路由：小请求
api := engine.Group("/api")
api.Use(middleware.BodyLimit(middleware.BodyLimitConfig{
    Limit: 1 * 1024 * 1024, // 1MB
}))

// 上传路由：大文件
upload := engine.Group("/upload")
upload.Use(middleware.BodyLimit(middleware.BodyLimitConfig{
    Limit: 100 * 1024 * 1024, // 100MB
}))
// 上传不压缩响应
upload.POST("/image", uploadImageHandler)
upload.POST("/file", uploadFileHandler)
```

### 场景 5: 静态资源服务

```go
engine := aurora.New()

// 静态资源：高压缩率
static := engine.Group("/static")
static.Use(middleware.Gzip(middleware.GzipConfig{
    Level:     gzip.BestCompression, // 最高压缩
    MinLength: 512,
    ExcludedExtensions: []string{".png", ".jpg", ".mp4"}, // 排除已压缩格式
}))
static.StaticFS("/", http.Dir("./public"))

// API：快速压缩
api := engine.Group("/api")
api.Use(middleware.Gzip(middleware.GzipConfig{
    Level: gzip.BestSpeed,
}))
```

### 场景 6: 生产环境全栈配置（带认证和限流）

```go
engine := aurora.New()

// 1. 日志与恢复
engine.Use(middleware.Logger())
engine.Use(middleware.Recovery())

// 2. 安全头（严格模式）
engine.Use(middleware.Security(middleware.StrictSecurityConfig()))

// 3. CORS（限制源）
engine.Use(middleware.CORS(middleware.CORSConfig{
    AllowOrigins:     []string{"https://yourdomain.com"},
    AllowMethods:     []string{"GET", "POST", "PUT", "DELETE"},
    AllowCredentials: true,
}))

// 4. 全局限流（宽松策略，故障时放行）
globalLimiter, _ := ratelimit.New(redisClient, ratelimit.Config{
    KeyPrefix: "api:global:ip:",
    Limit:     1000,
    Window:    time.Minute,
})
engine.Use(middleware.IPRateLimiter(globalLimiter,
    middleware.WithFailOpen(),
    middleware.WithLogger(logger),
))

// 5. Body Limit（全局保守策略）
engine.Use(middleware.BodyLimit(middleware.BodyLimitConfig{
    Limit: 1 * 1024 * 1024, // 1MB
}))

// 6. Gzip（平衡压缩）
engine.Use(middleware.Gzip(middleware.GzipConfig{
    Level:     gzip.DefaultCompression,
    MinLength: 1024,
}))

// 7. 认证管理器
authManager, _ := coreauth.NewManager(&coreauth.Config{
    Redis:              redisClient,
    AccessTokenSecret:  os.Getenv("JWT_ACCESS_SECRET"),
    RefreshTokenSecret: os.Getenv("JWT_REFRESH_SECRET"),
    AccessTokenTTL:     15 * time.Minute,
    RefreshTokenTTL:    7 * 24 * time.Hour,
})

// 8. 公开路由（带严格限流）
loginLimiter, _ := ratelimit.New(redisClient, ratelimit.Config{
    KeyPrefix: "auth:login:ip:",
    Limit:     5,
    Window:    time.Minute,
})
engine.POST("/auth/login",
    middleware.IPRateLimiter(loginLimiter),
    handlers.Login,
)

// 9. 受保护的 API 路由
api := engine.Group("/api/v1")
api.Use(middleware.Auth(authManager,
    middleware.WithPreRefreshWindow(5 * time.Minute),
))
setupRoutes(api)
```

## 📊 性能建议

### Auth 认证策略

| 场景 | 推荐配置 | 原因 |
|------|---------|------|
| 高流量 API | 不启用主动刷新 | 减少 Redis 负载 |
| 移动端应用 | 启用主动刷新（5 分钟） | 减少客户端刷新逻辑 |
| Web 应用 | 启用主动刷新（2-3 分钟） | 平滑的用户体验 |

**Access Token TTL 建议**:
- 移动端 API: 30-60 分钟
- Web 应用: 15-30 分钟
- 高安全场景: 5-15 分钟

### RateLimit 限流策略

| 业务类型 | 推荐配置 | 降级策略 |
|----------|---------|---------|
| 全局限流 | 100-1000 req/min | fail-open（放行） |
| API 限流 | 60-100 req/min | fail-open（放行） |
| 登录接口 | 5 req/min | fail-close（拒绝） |
| 注册接口 | 3 req/hour | fail-close（拒绝） |
| 短信发送 | 10 req/hour | fail-close（拒绝） |

### Gzip 压缩级别选择

| 场景 | 推荐级别 | 原因 |
|------|---------|------|
| 动态内容（API） | `BestSpeed` (1) | 低延迟，CPU 占用少 |
| 静态资源 | `BestCompression` (9) | 高压缩率，可缓存 |
| 平衡 | `DefaultCompression` (-1) | 折中 |

### Body Limit 大小建议

| 业务类型 | 推荐限制 |
|----------|---------|
| JSON API | 1-2MB |
| 表单提交 | 512KB-1MB |
| 图片上传 | 5-10MB |
| 文件上传 | 50-100MB |
| 视频上传 | 500MB-2GB |

### 安全头配置建议

| 环境 | 推荐配置 | Auth 认证 |
|------|---------|----------|
| 开发环境 | `DefaultSecurityConfig()` | 可选 |
| 测试环境 | `DefaultSecurityConfig()` | 推荐 |
| 生产环境 (HTTP) | `DefaultSecurityConfig()` | 推荐 |
| 生产环境 (HTTPS) | `StrictSecurityConfig()` | 必需 |

## 🛠️ 开发指南

### 创建自定义中间件

所有中间件遵循 `aurora.HandlerFunc` 签名：

```go
package middleware

import "github.com/surge-go/aurora"

type MyMiddlewareConfig struct {
    Option1 string
    Option2 int
}

func DefaultMyMiddlewareConfig() MyMiddlewareConfig {
    return MyMiddlewareConfig{
        Option1: "default",
        Option2: 100,
    }
}

func MyMiddleware(config MyMiddlewareConfig) aurora.HandlerFunc {
    // 初始化逻辑（执行一次）
    
    return func(c *aurora.Context) {
        // 请求前逻辑
        
        // 调用下一个中间件/处理器
        c.Next()
        
        // 响应后逻辑
    }
}
```

### 中间件设计原则

1. **最小依赖**: 优先依赖 Aurora 核心和 Go 标准库
   - Auth 和 RateLimit 依赖 Aurora 核心组件（`core/auth`, `core/ratelimit`）
   - 其他中间件保持零外部依赖
2. **配置驱动**: 提供 `Config` 结构体和 `Default*Config()` 函数
3. **文档完善**: 每个中间件提供独立的 Markdown 文档
4. **性能优先**: 避免不必要的内存分配和计算
   - 对象池复用（如 Gzip Writer Pool）
   - 并发控制（如 Auth Singleflight）
5. **向后兼容**: 配置结构体只增不减，使用指针表示可选字段
6. **故障处理**: 支持降级策略和优雅失败（如 RateLimit fail-open）

## 🧪 测试

每个中间件都提供了完整的测试用例：

```bash
# 测试所有中间件
go test -v ./middleware/...

# 测试特定中间件
go test -v ./middleware -run TestAuth
go test -v ./middleware -run TestIPRateLimiter
go test -v ./middleware -run TestCORS
go test -v ./middleware -run TestGzip
go test -v ./middleware -run TestSecurity
go test -v ./middleware -run TestBodyLimit

# 基准测试
go test -bench=. ./middleware/...

# 测试覆盖率
go test -cover ./middleware/...
```

## 📖 相关资源

- [Aurora 核心文档](../README.md)
- [中间件开发指南](./CONTRIBUTING.md)
- [性能优化指南](./PERFORMANCE.md)

## 🤝 贡献

欢迎贡献新的中间件！请遵循：
1. 零依赖原则
2. 统一的配置模式
3. 完整的文档和测试
4. 性能基准测试

详见 [CONTRIBUTING.md](./CONTRIBUTING.md)

## 📄 许可证

与 Aurora 框架相同的许可证。
