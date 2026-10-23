# CORS 中间件

CORS（跨域资源共享）中间件用于处理浏览器的跨域请求，支持预检请求、凭证携带、自定义响应头暴露等功能。

## 功能特性

- ✅ 支持精确源匹配和通配符源匹配
- ✅ 支持子域名通配符（`https://*.example.com`）
- ✅ 自动处理预检请求（OPTIONS）
- ✅ 支持凭证携带（Credentials）
- ✅ 自定义允许的 HTTP 方法和请求头
- ✅ 自定义暴露的响应头
- ✅ 可配置预检请求缓存时间
- ✅ 支持私有网络请求（Private Network Access）
- ✅ 安全检查：凭证模式下禁止通配符源

## 快速开始

### 使用默认配置（允许所有源）

```go
package main

import (
    "github.com/surge-go/aurora"
    "github.com/surge-go/aurora/middleware"
)

func main() {
    lifecycle, _ := aurora.NewLifecycle("config.yaml")
    engine := lifecycle.Engine()

    // 使用默认 CORS 配置（允许所有源，不携带凭证）
    engine.Use(middleware.CORS(middleware.DefaultCORSConfig()))

    engine.GET("/api/users", func(c *aurora.Context) {
        c.JSON(200, map[string]string{"message": "ok"})
    })

    lifecycle.Run()
}
```

### 限制特定源访问

```go
// 只允许指定的源访问
config := middleware.CORSConfig{
    AllowOrigins: []string{
        "https://example.com",
        "https://app.example.com",
    },
    AllowMethods: []string{"GET", "POST", "PUT", "DELETE"},
    AllowHeaders: []string{"Content-Type", "Authorization"},
}

engine.Use(middleware.CORS(config))
```

### 支持子域名通配符

```go
// 允许所有 example.com 的子域名
config := middleware.CORSConfig{
    AllowOrigins: []string{
        "https://*.example.com",
    },
}

engine.Use(middleware.CORS(config))

// 匹配示例：
// ✅ https://app.example.com
// ✅ https://api.example.com
// ✅ https://admin.app.example.com
// ❌ https://example.com（主域名不匹配）
// ❌ https://evil.com
```

### 携带凭证（Cookie、Authorization）

```go
// 允许携带凭证的跨域请求
config := middleware.CORSConfig{
    AllowOrigins: []string{
        "https://example.com",
        "https://app.example.com",
    },
    AllowMethods:     []string{"GET", "POST", "PUT", "DELETE"},
    AllowHeaders:     []string{"Content-Type", "Authorization"},
    AllowCredentials: true, // 允许携带凭证
}

engine.Use(middleware.CORS(config))

// 注意：AllowCredentials=true 时，AllowOrigins 不能包含通配符 "*"
// 否则会触发 panic
```

### 暴露自定义响应头

```go
// 允许客户端访问自定义响应头
config := middleware.CORSConfig{
    AllowOrigins: []string{"*"},
    ExposeHeaders: []string{
        "X-Total-Count",    // 分页总数
        "X-Page-Number",    // 当前页码
        "X-Rate-Limit",     // 限流剩余次数
    },
}

engine.Use(middleware.CORS(config))
```

### 自定义预检请求缓存时间

```go
import "time"

config := middleware.CORSConfig{
    AllowOrigins: []string{"*"},
    MaxAge:       24 * time.Hour, // 预检请求缓存 24 小时
}

engine.Use(middleware.CORS(config))
```

### 支持私有网络访问

```go
// 允许从公网访问私有网络（如 localhost、内网 IP）
config := middleware.CORSConfig{
    AllowOrigins:        []string{"https://example.com"},
    AllowPrivateNetwork: true,
}

engine.Use(middleware.CORS(config))
```

## 配置参数

### CORSConfig 结构体

```go
type CORSConfig struct {
    // AllowOrigins 允许的源列表。支持精确匹配和通配符 "*"。
    // 示例: []string{"https://example.com", "https://*.example.com"}
    // 默认: []string{"*"}
    AllowOrigins []string

    // AllowMethods 允许的 HTTP 方法列表。
    // 默认: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
    AllowMethods []string

    // AllowHeaders 允许的请求头列表。
    // 默认: []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Request-ID"}
    AllowHeaders []string

    // ExposeHeaders 暴露给客户端的响应头列表。
    // 默认: []string{}
    ExposeHeaders []string

    // AllowCredentials 是否允许携带凭证（Cookie、Authorization 等）。
    // 注意：当设置为 true 时，AllowOrigins 不能包含通配符 "*"。
    // 默认: false
    AllowCredentials bool

    // MaxAge 预检请求（OPTIONS）的缓存时间。
    // 默认: 12 小时
    MaxAge time.Duration

    // AllowPrivateNetwork 是否允许私有网络请求。
    // 对应 Access-Control-Allow-Private-Network 响应头。
    // 默认: false
    AllowPrivateNetwork bool
}
```

## 默认配置

`DefaultCORSConfig()` 返回以下默认值：

```go
CORSConfig{
    AllowOrigins: []string{"*"},
    AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"},
    AllowHeaders: []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Request-ID"},
    ExposeHeaders: []string{},
    AllowCredentials: false,
    MaxAge: 12 * time.Hour,
    AllowPrivateNetwork: false,
}
```

## 应用到特定路由组

```go
// 全局路由不使用 CORS
public := engine.Group("/public")
public.GET("/health", healthHandler)

// API 路由启用 CORS
apiCORS := middleware.CORSConfig{
    AllowOrigins: []string{"https://app.example.com"},
    AllowMethods: []string{"GET", "POST", "PUT", "DELETE"},
    AllowCredentials: true,
}

api := engine.Group("/api", middleware.CORS(apiCORS))
api.GET("/users", listUsersHandler)
api.POST("/users", createUserHandler)
```

## 工作原理

### 1. 非跨域请求
如果请求没有 `Origin` 头，中间件直接放行，不设置任何 CORS 响应头。

### 2. 跨域请求
如果请求包含 `Origin` 头：
- 检查源是否在允许列表中
- 如果允许，设置 `Access-Control-Allow-Origin` 响应头
- 如果启用凭证，设置 `Access-Control-Allow-Credentials: true`
- 如果有暴露头，设置 `Access-Control-Expose-Headers`

### 3. 预检请求（OPTIONS）
浏览器在发送实际请求前，会先发送一个 OPTIONS 预检请求：
- 中间件检查源是否允许
- 返回允许的方法（`Access-Control-Allow-Methods`）
- 返回允许的请求头（`Access-Control-Allow-Headers`）
- 返回缓存时间（`Access-Control-Max-Age`）
- 直接返回 `204 No Content`，不执行后续处理

## 安全建议

### 1. 避免在生产环境使用通配符
```go
// ❌ 不推荐（允许所有源）
config := middleware.CORSConfig{
    AllowOrigins: []string{"*"},
}

// ✅ 推荐（明确指定允许的源）
config := middleware.CORSConfig{
    AllowOrigins: []string{
        "https://app.example.com",
        "https://admin.example.com",
    },
}
```

### 2. 携带凭证时必须指定源
```go
// ❌ 错误（会触发 panic）
config := middleware.CORSConfig{
    AllowOrigins:     []string{"*"},
    AllowCredentials: true,
}

// ✅ 正确
config := middleware.CORSConfig{
    AllowOrigins:     []string{"https://example.com"},
    AllowCredentials: true,
}
```

### 3. 限制允许的方法和头
```go
// 只允许必要的 HTTP 方法和请求头
config := middleware.CORSConfig{
    AllowOrigins: []string{"https://example.com"},
    AllowMethods: []string{"GET", "POST"}, // 不允许 DELETE、PUT
    AllowHeaders: []string{"Content-Type", "Authorization"}, // 限制请求头
}
```

### 4. 谨慎暴露响应头
```go
// 只暴露必要的自定义响应头
config := middleware.CORSConfig{
    ExposeHeaders: []string{
        "X-Total-Count", // 分页信息
        // 不要暴露敏感信息如 X-Internal-User-ID
    },
}
```

## 故障排查

### 1. 请求被拒绝，响应没有 CORS 头
- 检查源是否在 `AllowOrigins` 列表中
- 确认请求包含 `Origin` 头
- 检查通配符格式是否正确（如 `https://*.example.com`）

### 2. 预检请求失败
- 确认 `AllowMethods` 包含实际请求的方法
- 确认 `AllowHeaders` 包含实际请求的自定义头
- 检查浏览器控制台的详细错误信息

### 3. Cookie 没有发送
- 确认 `AllowCredentials: true`
- 确认 `AllowOrigins` 不包含通配符 `*`
- 确认客户端请求设置了 `credentials: 'include'`（Fetch API）或 `withCredentials: true`（XMLHttpRequest）

### 4. 自定义响应头访问不到
- 确认响应头名称在 `ExposeHeaders` 列表中
- 默认只能访问 `Cache-Control`、`Content-Language`、`Content-Type` 等简单响应头

## 参考资料

- [MDN: CORS](https://developer.mozilla.org/en-US/docs/Web/HTTP/CORS)
- [W3C: Fetch Standard](https://fetch.spec.whatwg.org/)
- [Private Network Access](https://wicg.github.io/private-network-access/)
