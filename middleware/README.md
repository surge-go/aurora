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

| 中间件 | 说明 |
|--------|------|
| **IPFilter** | IP 白名单/黑名单，支持 CIDR 段过滤，支持动态更新 |
| **CORS** | 跨域资源共享配置 |
| **Gzip** | 响应压缩，减少传输数据 70-90% |
| **Security** | 安全响应头（HSTS、CSP、XFO 等） |
| **BodyLimit** | 请求体大小限制，防止资源耗尽 |

## 🚀 快速开始

### 安装

```bash
go get -u github.com/surge-go/aurora
```

### 依赖说明

所有中间件**零外部依赖**，仅依赖 Go 标准库和 Aurora 核心：
- ✅ CORS
- ✅ Gzip
- ✅ Security
- ✅ BodyLimit
- ✅ IPFilter

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

// 5. IP 过滤 - IP 白名单/黑名单（如果需要）
// engine.Use(middleware.IPFilter(middleware.IPFilterConfig{...}))

// 6. Body Limit - 限制请求体大小（在解析前）
engine.Use(middleware.BodyLimit(middleware.DefaultBodyLimitConfig()))

// 7. Gzip - 压缩响应（在业务逻辑后）
engine.Use(middleware.Gzip(middleware.DefaultGzipConfig()))

// 8. 业务路由
engine.GET("/api/data", dataHandler)
```

## 📚 详细文档

### IPFilter IP 白名单/黑名单

基于 IP 地址的访问控制中间件，支持白名单、黑名单和 CIDR 段过滤。

**特性**:
- ✅ IP 白名单（只允许特定 IP 访问）
- ✅ IP 黑名单（阻止特定 IP 访问）
- ✅ CIDR 段支持（如 `192.168.0.0/16`）
- ✅ 路径跳过（特定路径可跳过 IP 检查）
- ✅ 代理支持（X-Forwarded-For / X-Real-IP）
- ✅ IPv6 完整支持
- ✅ 自定义拒绝响应

```go
// 白名单模式（只允许内网访问）
engine.Use(middleware.IPFilter(middleware.IPFilterConfig{
    AllowList: []string{
        "192.168.0.0/16",       // 内网 A
        "10.0.0.0/8",           // 内网 B
        "172.16.0.0/12",        // 内网 C
    },
    SkipPaths: []string{"/health"}, // 健康检查例外
}))

// 黑名单模式（封禁恶意 IP）
engine.Use(middleware.IPFilter(middleware.IPFilterConfig{
    BlockList: []string{
        "203.0.113.0/24",       // 封禁整个段
        "198.51.100.50",        // 封禁单个 IP
    },
}))

// 混合模式（允许内网，但排除特定 IP）
engine.Use(middleware.IPFilter(middleware.IPFilterConfig{
    AllowList: []string{"192.168.0.0/16"},
    BlockList: []string{"192.168.1.100"}, // 黑名单优先
}))

// 配合负载均衡器使用
engine.Use(middleware.IPFilter(middleware.IPFilterConfig{
    BlockList: []string{"203.0.113.0/24"},
    TrustedProxies: []string{"10.0.0.1", "10.0.0.2"}, // 负载均衡器
}))

// 自定义拒绝响应
engine.Use(middleware.IPFilter(middleware.IPFilterConfig{
    AllowList: []string{"192.168.0.0/16"},
    OnRejected: func(c *aurora.Context, ip string) {
        c.JSON(403, map[string]string{
            "error": "forbidden",
            "message": fmt.Sprintf("IP %s is not allowed", ip),
        })
    },
}))
```

**CIDR 支持示例**:
```go
"10.0.0.0/8"          // 10.0.0.0 - 10.255.255.255 (1600万+ IP)
"172.16.0.0/12"       // 172.16.0.0 - 172.31.255.255 (100万+ IP)
"192.168.0.0/16"      // 192.168.0.0 - 192.168.255.255 (65536 IP)
"192.168.1.0/24"      // 192.168.1.0 - 192.168.1.255 (256 IP)
"2001:db8::/32"       // IPv6 段
```

**适用场景**: 内网服务保护、防御攻击、地域限制、访问控制

#### 动态更新 IP 规则

IPFilter 支持运行时动态修改白名单/黑名单，适用于实时封禁攻击 IP 的场景。

```go
// 创建 Controller 用于运行时控制
ctrl := &middleware.IPFilterController{}

engine.Use(middleware.IPFilter(
    middleware.IPFilterConfig{
        AllowList: []string{"10.0.0.0/8"},
    },
    middleware.WithController(ctrl), // 注入 controller
))

// 运行时动态封禁攻击 IP
func blockAttacker(ip string) {
    if err := ctrl.AddToBlockList(ip); err != nil {
        log.Error("failed to block IP:", err)
        return
    }
    log.Info("blocked IP:", ip)
}

// 从 Redis 同步黑名单（定时任务）
go func() {
    ticker := time.NewTicker(10 * time.Second)
    for range ticker.C {
        blockedIPs := loadFromRedis("security:blocked_ips")
        ctrl.SetBlockList(blockedIPs)
    }
}()

// 提供管理接口
adminAPI.POST("/security/block-ip", func(c *aurora.Context) {
    var req struct {
        IP string `json:"ip"`
    }
    if err := c.BindJSON(&req); err != nil {
        c.Fail(aurora.ErrBadRequest)
        return
    }
    
    if err := ctrl.AddToBlockList(req.IP); err != nil {
        c.Fail(aurora.ErrInternal)
        return
    }
    
    c.OK(map[string]any{"blocked": req.IP})
})
```

**Controller API**:
```go
// 黑名单操作
ctrl.AddToBlockList("1.2.3.4", "5.6.7.8")       // 添加到黑名单
ctrl.RemoveFromBlockList("1.2.3.4")             // 从黑名单移除
ctrl.SetBlockList([]string{"1.2.3.4"})          // 替换整个黑名单
ctrl.GetBlockList()                              // 获取当前黑名单

// 白名单操作
ctrl.AddToAllowList("10.0.0.0/8")               // 添加到白名单
ctrl.RemoveFromAllowList("10.0.0.0/8")          // 从白名单移除
ctrl.SetAllowList([]string{"10.0.0.0/8"})       // 替换整个白名单
ctrl.GetAllowList()                              // 获取当前白名单
```

**线程安全保证**: 所有 Controller 方法都是并发安全的，可以在多个 goroutine 中同时调用。

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

// IPFilter 使用配置结构体
engine.Use(middleware.IPFilter(middleware.IPFilterConfig{
    AllowList: []string{"192.168.0.0/16"},
    BlockList: []string{"192.168.1.100"},
    SkipPaths: []string{"/health"},
}))

// Auth 中间件使用选项模式
engine.Use(middleware.Auth(authManager,
    middleware.WithPreRefreshWindow(5 * time.Minute),
    middleware.WithSkipPaths("/health", "/login"),
    middleware.WithClaimsKey("custom.claims.key"),
))

// RateLimit 使用选项模式
engine.Use(middleware.IPRateLimiter(limiter,
    middleware.WithFailOpen(),
    middleware.WithLogger(logger),
))
```

**适用**: 特殊需求、性能优化、复杂场景

## 🎯 使用场景

### 场景 1: 标准 API 服务

```go
engine := aurora.New()

// 核心中间件
engine.Use(middleware.Security(middleware.DefaultSecurityConfig()))
engine.Use(middleware.CORS(middleware.DefaultCORSConfig()))
engine.Use(middleware.BodyLimit(middleware.BodyLimitConfig{
    Limit: 1 * 1024 * 1024, // 1MB
}))
engine.Use(middleware.Gzip(middleware.GzipConfig{
    Level:     gzip.BestSpeed,
    MinLength: 1024,
}))

// 业务路由
api := engine.Group("/api/v1")
api.POST("/users", createUser)
api.GET("/users/:id", getUser)
```

### 场景 2: 内网 API（IP 白名单保护）

```go
engine := aurora.New()

// IP 白名单：只允许内网访问
engine.Use(middleware.IPFilter(middleware.IPFilterConfig{
    AllowList: []string{
        "10.0.0.0/8",           // 内网 A
        "172.16.0.0/12",        // 内网 B
        "192.168.0.0/16",       // 内网 C
    },
    SkipPaths: []string{"/health"}, // 健康检查例外
}))

// 核心中间件
engine.Use(middleware.Security(middleware.DefaultSecurityConfig()))
engine.Use(middleware.BodyLimit(middleware.BodyLimitConfig{
    Limit: 1 * 1024 * 1024, // API 限制 1MB
}))
engine.Use(middleware.Gzip(middleware.GzipConfig{
    Level:     gzip.BestSpeed,
    MinLength: 1024,
}))

// 业务路由
api := engine.Group("/api/v1")
api.POST("/users", createUser)
api.GET("/users/:id", getUser)
```

### 场景 3: 防御攻击（IP 黑名单）

```go
engine := aurora.New()

// IP 黑名单：封禁恶意 IP 段
engine.Use(middleware.IPFilter(middleware.IPFilterConfig{
    BlockList: []string{
        "203.0.113.0/24",       // 恶意 IP 段
        "198.51.100.50",        // 特定攻击者
    },
    TrustedProxies: []string{"10.0.0.1"}, // 负载均衡器
}))

// 核心中间件
engine.Use(middleware.Security(middleware.DefaultSecurityConfig()))
engine.Use(middleware.BodyLimit(middleware.DefaultBodyLimitConfig()))

// 业务路由
api := engine.Group("/api")
api.GET("/data", getData)
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
upload.POST("/image", uploadImageHandler)
upload.POST("/file", uploadFileHandler)
```

### 场景 5: 静态资源服务

```go
engine := aurora.New()

// 静态资源：高压缩率
static := engine.Group("/static")
static.Use(middleware.Gzip(middleware.GzipConfig{
    Level:     gzip.BestCompression,
    MinLength: 512,
    ExcludedExtensions: []string{".png", ".jpg", ".mp4"},
}))
static.StaticFS("/", http.Dir("./public"))

// API：快速压缩
api := engine.Group("/api")
api.Use(middleware.Gzip(middleware.GzipConfig{
    Level: gzip.BestSpeed,
}))
```

### 场景 6: 生产环境全栈配置

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

// 4. Body Limit
engine.Use(middleware.BodyLimit(middleware.BodyLimitConfig{
    Limit: 1 * 1024 * 1024, // 1MB
}))

// 5. Gzip
engine.Use(middleware.Gzip(middleware.GzipConfig{
    Level:     gzip.DefaultCompression,
    MinLength: 1024,
}))

// 6. 业务路由
api := engine.Group("/api/v1")
setupRoutes(api)
```

## 📊 性能建议

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

| 环境 | 推荐配置 |
|------|---------|
| 开发环境 | `DefaultSecurityConfig()` |
| 测试环境 | `DefaultSecurityConfig()` |
| 生产环境 (HTTP) | `DefaultSecurityConfig()` |
| 生产环境 (HTTPS) | `StrictSecurityConfig()` |

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
   - 所有中间件保持零外部依赖
2. **配置驱动**: 提供 `Config` 结构体和 `Default*Config()` 函数
3. **文档完善**: 每个中间件提供独立的 Markdown 文档
4. **性能优先**: 避免不必要的内存分配和计算
   - 对象池复用（如 Gzip Writer Pool）
5. **向后兼容**: 配置结构体只增不减，使用指针表示可选字段
6. **故障处理**: 支持降级策略和优雅失败

## 🧪 测试

每个中间件都提供了完整的测试用例：

```bash
# 测试所有中间件
go test -v ./middleware/...

# 测试特定中间件
go test -v ./middleware -run TestIPFilter
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
