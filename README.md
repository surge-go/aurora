<div align="center">

# 🌌 Aurora

**基于 Gin 的生产级 Go Web 应用框架**

[![Go Version](https://img.shields.io/badge/Go-1.27+-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![Gin](https://img.shields.io/badge/Gin-1.12-blue?style=flat-square)](https://github.com/gin-gonic/gin)
[![Version](https://img.shields.io/badge/version-v0.0.4-orange?style=flat-square)](https://github.com/surge-go/aurora/releases)
[![License](https://img.shields.io/badge/license-MIT-green?style=flat-square)](./LICENSE)
[![Go Report Card](https://img.shields.io/badge/Go%20Report%20Card-retired-9f9f9f?style=flat-square)](https://goreportcard.com/report/github.com/surge-go/aurora)
[![Go Reference](https://pkg.go.dev/badge/github.com/surge-go/aurora.svg)](https://pkg.go.dev/github.com/surge-go/aurora)

[快速开始](#-快速开始) · [核心概念](#-核心概念) · [设计文档](./DESIGN.md) · [示例](./example/)

</div>

---

Aurora 在 Gin 路由能力之上,提供稳定的业务开发 API、统一的响应与错误模型、泛型请求绑定,以及可插拔的日志、Tracing、Metrics 中间件,让团队用同一套约定快速落地生产级 HTTP 服务。

## ✨ 特性

- 🎯 **泛型绑定** —— `Bind[Req, Resp]` 一行接入业务函数,自动完成参数校验和统一响应
- 🛡️ **安全默认** —— 不信任代理、不暴露内部错误、固定中间件顺序,行为可预测
- 📦 **依赖注入** —— `Config` 描述静态配置,`Option` 注入运行时依赖,无全局魔法
- 🔭 **可观测性** —— 内置 RequestID / Tracing / Metrics / AccessLog,响应自动带 `trace_id`
- 🧩 **生命周期清晰** —— `Start` / `Serve` / `Run` / `Shutdown` 统一封装,防启动竞态
- 📁 **静态文件 API** —— 提供 `Static` / `StaticFS` / `StaticFile` 路由注册入口
- 🚫 **资源所有权明确** —— Engine 只管 Gin/HTTP Server,DB / Redis / MQ 由应用组合根管理

## 📑 目录

- [为什么选择 Aurora](#-为什么选择-aurora)
- [安装](#-安装)
- [快速开始](#-快速开始)
- [核心概念](#-核心概念)
  - [Engine 与生命周期](#engine-与生命周期)
  - [Handler 与泛型绑定](#handler-与泛型绑定)
  - [Context 请求上下文](#context-请求上下文)
  - [路由与分组](#路由与分组)
  - [静态文件服务](#静态文件服务)
- [统一响应与错误处理](#-统一响应与错误处理)
- [中间件](#-中间件)
- [配置](#-配置)
- [依赖注入(Option)](#-依赖注入option)
- [可观测性](#-可观测性)
- [基础设施包(core/*)](#-基础设施包core)
- [示例](#-示例)
- [API 速查](#-api-速查)
  - [Engine](#engine)
  - [RouterGroup](#routergroup)
  - [Handler 与绑定适配器](#handler-与绑定适配器)
  - [Context — 流程控制与请求访问](#context--流程控制与请求访问)
  - [Context — 绑定与响应](#context--绑定与响应)
  - [Context — 请求级状态](#context--请求级状态)
  - [Context — 链路与底层访问](#context--链路与底层访问)
  - [Response](#response)
  - [Error 与错误工具](#error-与错误工具)
  - [Option](#option)
  - [中间件构造函数](#中间件构造函数)
  - [Config 与 MiddlewareConfig](#config-与-middlewareconfig)
  - [TracingRuntime](#tracingruntime)
- [版本与兼容性](#-版本与兼容性)
- [设计文档](#-设计文档)

## 🤔 为什么选择 Aurora

| 关注点 | 裸 Gin | Aurora |
| --- | --- | --- |
| 业务函数签名 | 每个 Handler 自己写 `c.ShouldBind...`、自己拼响应 | 泛型 `Bind[Req, Resp]` 一行接入,统一响应/错误 |
| 错误暴露面 | 容易把 SQL/堆栈写入响应 | 业务码 + Message,内部错误自动映射为 500 |
| Trace/Request ID | 自行接入 OTel | 中间件内置,响应自动带 `trace_id` |
| 配置/依赖注入 | 全局变量散落 | `Config` + `Option` 显式注入,无魔法 |
| 生命周期 | `http.Server` 自己拼 | `Start/Serve/Run/Shutdown` 统一封装,防启动竞态 |
| 中间件顺序 | 每个项目一套写法 | 固定顺序 + 开关,行为可预测 |

Aurora **不**试图替代 Gin。路由匹配、参数解析、Validate 仍由 Gin 承担;Aurora 提供的是团队级约定和安全默认值。

## 📦 安装

```bash
go get github.com/surge-go/aurora@latest
```

## 🚀 快速开始

```go
package main

import (
    "context"

    "github.com/surge-go/aurora"
)

type CreateUserReq struct {
    Name  string `json:"name"  binding:"required"`
    Email string `json:"email" binding:"required,email"`
    Age   int    `json:"age"   binding:"gte=0,lte=150"`
}

type User struct {
    ID    int    `json:"id"`
    Name  string `json:"name"`
    Email string `json:"email"`
}

func CreateUser(c *aurora.Context, req *CreateUserReq) (*User, error) {
    // 业务逻辑;req 已自动完成 body 绑定和 validate
    return &User{ID: 1, Name: req.Name, Email: req.Email}, nil
}

func main() {
    engine, err := aurora.New(aurora.DefaultConfig())
    if err != nil {
        panic(err)
    }

    engine.POST("/users", aurora.Bind(CreateUser))

    if err := engine.Run(context.Background()); err != nil {
        panic(err)
    }
}
```

```bash
curl -X POST http://localhost:8080/users \
  -H 'Content-Type: application/json' \
  -d '{"name":"Alice","email":"alice@example.com","age":25}'
```

响应:

```json
{
  "code": 0,
  "message": "success",
  "data": {"id": 1, "name": "Alice", "email": "alice@example.com"},
  "trace_id": "4b1d3f2c..."
}
```

参数错误时,自动得到:

```json
{"code": 1001, "message": "请求无效", "trace_id": "4b1d3f2c..."}
```

## 💡 核心概念

### Engine 与生命周期

`Engine` 同时拥有 Gin Engine、`http.Server` 和 `net.Listener`。需要统一初始化和关闭 core
资源时，可以使用嵌入 Engine 的 `Lifecycle`。

```go
engine, err := aurora.New(cfg, opts...)   // 仅创建,不监听端口
engine.Start()                            // 阻塞,监听 cfg.Addr
engine.Serve(listener)                    // 使用调用方提供的 listener
engine.Run(ctx)                           // 阻塞 + 监听 ctx 取消,自动 Shutdown
engine.Shutdown(ctx)                      // 优雅停机,由 ctx 的 deadline 控制超时
engine.ServeHTTP(w, r)                    // 实现 http.Handler,便于测试
engine.Addr()                             // 启动后的实际地址(支持 ":0")
engine.Listening()                        // 是否处于监听状态
```

`NewLifecycle` 从配置文件创建 Aurora 和已启用的 core 资源。无参数时使用
`config/aurora.yaml`；文件不存在会写入默认模板。它不会注册路由，也不会启动 HTTP 服务：

```go
engine, err := aurora.NewLifecycle()
if err != nil {
    log.Fatal(err)
}

engine.GET("/health", healthHandler)
if err := engine.Run(ctx); err != nil {
    log.Fatal(err)
}
```

默认配置不会启用 database、Redis、Auth、MQ、tracing 或 metrics。`Lifecycle` 会在 `Run`、`Start`
或 `Serve` 返回后，按依赖逆序关闭它创建的资源；也可以显式调用
`engine.Shutdown(ctx)` 或 `engine.Close(ctx)`。

首次启动生成的 `config/aurora.yaml` 是完整配置模板，已经列出 logger、tracing、metrics、
database、Redis、Auth 和三种 MQ adapter 的可序列化字段。外部资源默认保持关闭，确认连接地址、
凭据和 TLS 文件后再逐项开启。常用配置结构如下，生成文件中还包含连接池、重试、监控、
日志轮转和消息消费策略等进阶字段：

```yaml
aurora:
  addr: ":8080"
  mode: release
  middleware:
    request_id: true
    recovery: true
    access_log: true
    tracing: false
    metrics: false

# Uncomment an optional tracing block to enable it.
# tracing:
#   service_name: aurora
#   service_version: "0.0.4"
#   endpoint: "localhost:4317"
#   protocol: grpc
#   insecure: true

# Uncomment an optional metrics block to enable it.
# metrics:
#   service_name: aurora
#   service_version: "0.0.4"
#   endpoint: "localhost:4317"
#   protocol: grpc
#   insecure: true

logger:
  level: info
  format: console
  outputs: [stdout]
  add_caller: true
  stacktrace_level: error

# Uncomment an optional database block to enable it.
# database:
#   driver: sqlite
#   dsn: "./data/aurora.db"
#   pool:
#     max_open_conns: 20
#     max_idle_conns: 5
#     conn_max_lifetime: 1h

# Uncomment an optional Redis block to enable it.
# redis:
#   mode: standalone
#   addrs: ["127.0.0.1:6379"]
#   timeout:
#     dial_timeout: 5s
#     read_timeout: 3s
#     write_timeout: 3s

# Uncomment an optional Auth block to enable JWT/Redis sessions. Protect the config file.
# auth:
#   admin:
#     signing_secret: "replace-with-at-least-32-random-bytes"
#     issuer: aurora-admin
#     audience: [admin-client]
#     access_ttl: 15m
#     refresh_ttl: 24h
#     max_session_ttl: 168h
#     refresh_compensation_timeout: 5s
#   user:
#     signing_secret: "replace-with-a-different-32-byte-random-secret"
#     issuer: aurora-user
#     audience: [user-client]
#     access_ttl: 30m
#     refresh_ttl: 168h
#     max_session_ttl: 720h

# Uncomment an optional MQ block to enable it.
# mq:
#   driver: redis
#   redis:
#     group: aurora
#     consumer: aurora
# rabbitmq and kafka sections are included in the generated template.
```

Auth 按名称配置多个实例，每个实例完整使用 `auth.Config`，通过 `lifecycle.Auth("admin")` 获取
`*auth.Manager`。不支持原来的单实例扁平配置或 `lifecycle.Auth` 字段。

- 不配置 `auth` 或配置 `auth: {}` 时不创建实例；存在实例时必须同时配置 Redis。
- 实例名使用 `[a-z][a-z0-9_-]{0,63}`；配置键遵循 Viper 的大小写不敏感规则，加载后转为小写。
  `Auth(name)` 按小写名称精确匹配，不自动去除空格、不提供默认实例或回退。
- 每个实例必须填写至少 32 字节的 `signing_secret`、`issuer`、`audience` 和 `max_session_ttl`。
  建议不同认证域使用独立密钥及不同的 issuer、audience；实例名不会自动写入 JWT 或 `Principal.Type`。
- `key_prefix` 未填写时生成 `aurora:auth:{实例名}`；自定义前缀仍须包含非空 Redis hash tag。
  不允许实例间使用相同前缀，包括自定义前缀与自动生成前缀冲突。
- 空实例、未知字段、错误类型或无效配置会使初始化失败，错误包含实例名，解码错误不输出配置值。
- 实例按名称排序创建，任一实例失败时整体初始化失败并清理已创建资源。
- 实例表在启动时构建，之后保持只读，可并发获取；不提供动态注册、删除、替换或热重载。
- 所有实例复用统一创建的 Redis 客户端，Auth 不单独关闭 Redis；`Lifecycle.Close` 统一释放资源。

建议在启动阶段获取实例并注入业务服务和路由中间件。未找到实例时返回
`ErrAuthInstanceNotFound`，可用 `errors.Is` 判断。

通过 `lifecycle.AuthNames()` 获取按名称排序的实例标识列表，通过 `len(lifecycle.AuthNames())`
获取实例数量。每次调用返回独立切片，修改结果不会影响注册表；没有实例时返回 `nil`（长度为 0）。

```go
names := lifecycle.AuthNames() // []string{"admin", "user"}
count := len(names)            // 2
```

`refresh_compensation_timeout` 用于限制 refresh 轮换已提交、但后置状态校验失败时的补偿吊销操作，默认 `5s`，允许范围为 `1s-1m`。
补偿吊销使用独立的有界 context，Redis 暂时不可用时不会让请求无限等待。

未写入配置段的组件不会初始化；启用 tracing 或 metrics 后必须替换为实际 collector 地址。
Redis Streams 依赖同一份 `redis` 客户端，启用 MQ Redis adapter 时也必须同时配置 Redis。
配置中的运行时 client、dialer、provider 和 logger 不会写入 YAML，而是在 `NewLifecycle` 内部装配。

要点:

- 路由和全局中间件**必须**在 `Start`/`Serve`/`Run` 之前注册,启动后路由树冻结。
- `Run(ctx)` 内部使用启动屏障,避免取消信号与启动过程竞态;Shutdown 超时由 `WriteTimeout` 兜底。
- `New` 只创建,**不会**监听端口、创建网络连接或修改全局状态,可以安全地在测试里反复调用。

### Handler 与泛型绑定

Aurora 提供四种泛型适配器,把业务函数包装成 `HandlerFunc`:

```go
// 有请求 + 有响应
func Bind[Req, Resp any](fn func(*Context, *Req) (*Resp, error)) *HandlerWithMeta

// 无请求 + 有响应
func BindR[Resp any](fn func(*Context) (*Resp, error)) *HandlerWithMeta

// 有请求 + 无响应(删除等)
func BindE[Req any](fn func(*Context, *Req) error) *HandlerWithMeta

// 无请求 + 无响应(健康检查)
func BindRE(fn func(*Context) error) *HandlerWithMeta
```

**绑定规则**(无需手动调用 `ShouldBind`):

| HTTP 方法 | 绑定来源 |
| --- | --- |
| `POST` `PUT` `PATCH` | body(JSON/XML/YAML/TOML/MsgPack/BSON/Protobuf/Form) + URI 参数 |
| `GET` `DELETE` `HEAD` `OPTIONS` | query 参数 + URI 参数 |

- 结构体 tag:`uri` 标记路径参数,`form` 标记 query/form,`json`/`xml`/... 由 Content-Type 决定。
- 所有字段映射完成后**统一执行** Gin 的 `binding` 校验,允许同一结构体混用 URI 与 body 字段。
- 绑定失败自动响应 `ErrBadRequest`(400);请求体超过 `MaxBodyBytes` 自动响应 `ErrPayloadTooLarge`(413)。
- 业务函数返回 `error` 时自动调用 `c.Fail`,成功时自动调用 `c.OK`。

`HandlerWithMeta` 在运行时携带业务函数名,只用于启动时的路由日志,不影响请求分发。

### Context 请求上下文

`Context` 是对 Gin Context 的封装,**仅在当前请求处理期间有效**,不应保存到全局变量或异步 goroutine。

```go
// 参数获取
c.Param("id")              // 路径参数
c.Query("page")            // query
c.DefaultQuery("size", "20")
c.PostForm("name")         // 表单
c.FormFile("file")         // 上传文件
c.Header("Authorization")  // 请求头
c.ClientIP()               // 客户端 IP(默认不信任代理)
c.FullPath()               // 路由模板,如 /users/:id

// 请求级状态(中间件与 Handler 之间传值)
c.Set("user_id", 42)
c.Get("user_id")
c.GetInt("user_id")

// Trace/Request ID(由中间件自动注入)
c.TraceID()
c.RequestID()

// 流程控制
c.Next()
c.Abort()

// 响应
c.OK(data)                  // 统一成功响应,200
c.Fail(err)                 // 统一错误响应 + Abort
c.JSON(200, obj)            // 裸 JSON(绕过统一响应,谨慎使用)
c.String(200, "ok")
c.File("./report.pdf")
c.Redirect(302, "/login")
```

**关键边界**:跨层传递(交给 service / repository / DB / MQ)时,使用标准库 `context.Context`:

```go
func CreateUser(c *aurora.Context, req *CreateUserReq) (*User, error) {
    return svc.Create(c.Context(), req)   // ✓ 标准 ctx,可传递取消/超时/trace
    // 不要把 c 传给 svc                —— ✗ Context 不该被业务层持有
}
```

### 路由与分组

```go
engine.GET("/health", HealthCheck)
engine.POST("/users", CreateUser)
engine.Any("/ping", Ping)   // 注册 9 种 HTTP 方法

api := engine.Group("/api/v1", AuthMiddleware)
api.GET("/users/:id", GetUser)

admin := api.Group("/admin", AdminOnly)   // 嵌套分组,继承父中间件
admin.DELETE("/users/:id", DeleteUser)
```

`RouterGroup` 只负责**前缀和中间件继承**,不携带认证、权限或业务依赖 —— 这些请通过闭包注入 Handler。

### 静态文件服务

Engine 和 RouterGroup 都提供静态文件服务的路由注册入口：

```go
engine.Static("/assets", "./public")
engine.StaticFS("/static", http.Dir("./static"))
engine.StaticFile("/favicon.ico", "./public/favicon.ico")

assets := engine.Group("/web")
assets.Static("/assets", "./web/assets")
```

`Static` 映射本地目录，`StaticFS` 接收 `http.FileSystem`，`StaticFile` 映射单个文件。
静态资源路由会参与 Aurora 的路由注册和中间件链；目录索引、缓存策略、隐藏文件和 SPA
回退等具体行为遵循底层 Gin 的文件服务实现。业务 Handler 返回单个文件时继续使用
`Context.File` 或 `Context.FileAttachment`。

## 📨 统一响应与错误处理

### 响应结构

所有通过 `c.OK` / `c.Fail` 返回的响应都遵循统一格式:

```go
type Response[T any] struct {
    Code    int    `json:"code"`              // 业务状态码,0 表示成功
    Data    T      `json:"data"`              // 业务数据
    Message string `json:"message"`           // 用户可见消息
    TraceID string `json:"trace_id,omitempty"` // 链路 ID,便于排障
}
```

分页场景可用内置的 `PageResp[T]`:

```go
return aurora.NewPageResp(users, total), nil
// {"list": [...], "total": 123}  —— 自动包装进 Response.Data
```

### Error 模型

```go
// 构造
aurora.NewError(20001, "用户名已存在")
aurora.NewWithStatus(20001, http.StatusConflict, "用户名已存在")
aurora.Wrap(sqlErr, 20002, "查询用户失败")
aurora.WrapWithStatus(sqlErr, 20002, http.StatusInternalServerError, "查询用户失败")

// 链式修改(返回新实例,不污染预定义错误)
aurora.ErrNotFound.WithMessage("user not found")
aurora.ErrInternal.WithErr(sqlErr).WithMessagef("create user %s failed", name)

// 链上查询
aurora.GetCode(err)      // 提取业务码,非 Aurora Error 返回 -1
aurora.GetStatus(err)    // 提取 HTTP 状态码,默认 500
aurora.IsCode(err, 1001)
errors.Is(err, aurora.ErrNotFound)   // 业务码相同即匹配
```

**预定义错误**(完整列表见 `errors.go`):

| 变量 | Code | HTTP | 说明 |
| --- | --- | --- | --- |
| `ErrInternal` | 1000 | 500 | 服务发生异常 |
| `ErrBadRequest` | 1001 | 400 | 请求无效 |
| `ErrUnauthorized` | 1003 | 401 | 请求未认证 |
| `ErrForbidden` | 1004 | 403 | 无权访问 |
| `ErrNotFound` | 1005 | 404 | 资源不存在 |
| `ErrConflict` | 1008 | 409 | 请求冲突 |
| `ErrAlreadyExists` | 1015 | 409 | 资源已存在 |
| `ErrPayloadTooLarge` | 1009 | 413 | 请求体过大 |
| `ErrTooManyRequests` | 1011 | 429 | 请求过于频繁 |
| `ErrServiceUnavailable` | 1013 | 503 | 服务暂不可用 |

**安全约定**:`Message` 会进入响应体;`err`(原始错误)只用于日志和 `errors.Is/As` 链路。绑定失败、未知 `error`、panic 一律映射为 `ErrInternal` 或 `ErrBadRequest`,**不会**把 SQL、堆栈、内部字段名泄露到 HTTP 响应。

## 🔗 中间件

Aurora 按固定顺序安装默认中间件:

```
MaxBody → RequestID → Metrics → Recovery → Tracing → AccessLog → 应用中间件 → Handler
```

| 中间件 | 默认 | 作用 |
| --- | --- | --- |
| MaxBody | 总是启用(可设为 0 关闭) | 限制请求体大小,超限返回 413 |
| RequestID | 开 | 读取/生成 `X-Request-ID`,写入 ctx 和响应头 |
| Metrics | 关 | OTel 指标(活跃请求数、耗时、body 大小),需 `WithMetricsProvider` |
| Recovery | 开 | 捕获 panic,记录日志,返回 `ErrInternal` |
| Tracing | 关 | OTel Span,提取/注入 W3C Trace Context,需 `WithTracingRuntime` |
| AccessLog | 开 | `[Aurora] 时间 \| 状态 \| 耗时 \| IP \| METHOD PATH trace_id=...` |

自定义中间件就是 `HandlerFunc`:

```go
func Auth(c *aurora.Context) {
    token := c.Header("Authorization")
    if token == "" {
        c.Fail(aurora.ErrUnauthorized)
        return
    }
    c.Set("user_id", parseUserID(token))
    c.Next()
}

engine.Use(Auth)              // 全局
api := engine.Group("/api", Auth)  // 仅分组
```

可复用的业务中间件放在独立的 `middleware` 包。例如，`middleware.AuthMiddleware` 会
校验 `Authorization: Bearer <access-token>`，把 `*auth.Claims` 写入请求上下文，并将
认证失败映射为 401；Redis 不可用时返回 503：

```go
import auroramiddleware "github.com/surge-go/aurora/middleware"

userAuth, err := lifecycle.Auth("user")
if err != nil {
    return err
}
api := engine.Group("/api", auroramiddleware.AuthMiddleware(userAuth))
api.GET("/me", func(c *aurora.Context) {
    claims, _ := auroramiddleware.Claims(c)
    c.OK(claims.Subject)
})
```

认证中间件默认只在 access token 过期时使用 `X-Refresh-Token` 刷新。可以通过
`WithPreRefreshWindow(2*time.Minute)` 开启预刷新：access token 进入窗口后会尝试轮换，
刷新成功会在响应中写入 `X-Access-Token` 和 `X-Refresh-Token`，并继续执行当前请求。
预刷新失败不会阻断仍然有效的 access token；access token 已过期时刷新失败仍返回 401。
同一进程内并发刷新同一个 refresh token 会通过 `singleflight` 合并为一次 Redis 刷新，
跨进程的最终一致性仍由 `core/auth` 的 Redis Lua rotation 保证。
测试或受控运行时可以通过 `WithClock` 注入中间件时钟；生产环境默认使用系统时钟。

登录入口可以在 `auth.IssueRequest` 中传入 `IdempotencyKey`，用来处理重复点击或网络重试。
相同主体、平台、设备和幂等键会在短 TTL 内回放第一次 token pair；请求参数不同会返回
`ErrIdempotencyConflict`。客户端应为每次新的主动登录生成新的幂等键。

## ⚙️ 配置

```go
type Config struct {
    Addr              string           // ":8080"、"127.0.0.1:8080"、"[::1]:8080"
    Mode              string           // debug | release | test
    ReadTimeout       time.Duration    // 默认 15s
    ReadHeaderTimeout time.Duration    // 默认 5s
    WriteTimeout      time.Duration    // 默认 30s
    IdleTimeout       time.Duration    // 默认 60s
    MaxHeaderBytes    int              // 默认 1 MiB
    MaxBodyBytes      int64            // 默认 10 MiB,0 表示不限制
    TrustedProxies    []string         // 默认空 —— 不信任任何代理
    Middleware        MiddlewareConfig // 中间件开关
}
```

- 所有字段支持 `json` / `yaml` / `mapstructure` tag,可直接由 `core/config` 加载。
- `Validate()` 只做确定性检查,不监听端口、不创建网络连接。
- 停机超时**不在** `Config` 中,统一由 `Shutdown(ctx)` 的 deadline 表达,避免双超时源。
- **代理安全默认值**:`TrustedProxies` 为空时,`ClientIP()` 直接取 `RemoteAddr`,忽略 `X-Forwarded-For`,防止 IP 伪造。部署在反代后时请显式配置。

## 💉 依赖注入(Option)

运行时对象(Logger / TracerProvider / MeterProvider)不放进配置文件,由应用组合根创建并通过 `Option` 注入:

```go
log, _ := logger.New(&logger.Config{Level: logger.LevelInfo, Format: logger.FormatJSON})
tracingRt, _ := tracing.New(ctx, tracing.Config{ServiceName: "user-svc", Endpoint: "otel-collector:4317"})
meterProvider, _ := metrics.NewProvider(ctx, metrics.Config{ServiceName: "user-svc"})

engine, err := aurora.New(cfg,
    aurora.WithLogger(log),
    aurora.WithTracingRuntime(tracingRt),
    aurora.WithMetricsProvider(meterProvider),
)
```

**所有权约定**:Engine 只拥有并关闭 Gin Engine、`http.Server` 和 `net.Listener`。Logger / TracerProvider / MeterProvider / DB / Redis / MQ 由应用组合根创建和关闭,Aurora 仅借用。

启用 `Middleware.Metrics = true` 时必须同时注入 `WithMetricsProvider`,否则 `New` 返回错误。

## 🔭 可观测性

| 维度 | 接入方式 | 关键点 |
| --- | --- | --- |
| 请求 ID | `Middleware.RequestID = true` | 读 `X-Request-ID`,空则 UUID,回写响应头 |
| Trace ID | `Middleware.Tracing = true` + `WithTracingRuntime` | 自动入 Span;响应体自动带 `trace_id` |
| 访问日志 | `Middleware.AccessLog = true` + `WithLogger` | 不记录 query/Cookie/Authorization/请求体 |
| HTTP 指标 | `Middleware.Metrics = true` + `WithMetricsProvider` | `http.server.request.duration` 等;`http.route` 使用模板,避免高基数 |
| Panic 堆栈 | `Middleware.Recovery = true` + `WithLogger` | 仅记录到日志,不写入响应 |

指标和 Span 的属性只包含 `http.request.method` / `http.route` / `http.response.status_code`,不记录请求 ID、客户端 IP、User-Agent 或任何用户输入。

## 🧱 基础设施包(core/*)

`core/` 下的包是独立的基础设施,由应用组合根创建后注入给 Engine 或业务层使用:

| 包 | 作用 |
| --- | --- |
| `core/config` | 配置加载(YAML/JSON/TOML/env),支持热更新 |
| `core/logger` | 结构化日志(`slog` 风格) |
| `core/tracing` | OTel TracerProvider / Propagator 初始化 |
| `core/metrics` | OTel MeterProvider 初始化 |
| `core/database` | GORM 客户端,支持多库/resolver |
| `core/redis` | go-redis v9 客户端,含 TLS、监控、Streams 适配 |
| `core/mq` | 消息队列公共契约,含 Kafka(franz-go)/ RabbitMQ / Redis Streams 适配器 |

每个子包都有自己的 `DESIGN.md` / `README.md` 详述用法。Aurora Web 框架与这些包**无强制耦合** —— 可以只用根包,也可以按需引入。

## 📂 示例

[`example/`](./example/) 提供完整的用户 CRUD + 健康检查示例,演示:

- 结构化请求/响应定义和 `binding` 校验
- 使用 `Bind` / `BindR` 包装业务函数
- 注入 logger,记录业务日志
- 通过 `aurora.ErrNotFound.WithMessage(...)` 返回业务错误

运行:

```bash
cd example
go run main.go

# 另一个终端
curl http://localhost:8080/health
curl -X POST http://localhost:8080/users \
  -H 'Content-Type: application/json' \
  -d '{"name":"Alice","email":"alice@example.com","age":25}'
curl http://localhost:8080/users/1
curl http://localhost:8080/users
```

## 📚 API 速查

Aurora 根包的全部公开 API,按职责分组。完整签名与文档请使用 `go doc` 或访问 [pkg.go.dev](https://pkg.go.dev/github.com/surge-go/aurora)。

### Engine

HTTP 服务的生命周期对象,持有 Gin Engine、`http.Server` 与 `net.Listener`。

```go
func New(cfg Config, opts ...Option) (*Engine, error)
func NewLifecycle(path ...string) (*Lifecycle, error)

// 路由注册(启动前)
func (e *Engine) GET(path string, handlers ...any)
func (e *Engine) POST(path string, handlers ...any)
func (e *Engine) PUT(path string, handlers ...any)
func (e *Engine) PATCH(path string, handlers ...any)
func (e *Engine) DELETE(path string, handlers ...any)
func (e *Engine) HEAD(path string, handlers ...any)
func (e *Engine) OPTIONS(path string, handlers ...any)
func (e *Engine) Any(path string, handlers ...any)

// 静态文件服务
func (e *Engine) Static(relativePath, root string)
func (e *Engine) StaticFS(relativePath string, fs http.FileSystem)
func (e *Engine) StaticFile(relativePath, filePath string)

// 中间件与分组
func (e *Engine) Use(handlers ...any)
func (e *Engine) Group(prefix string, handlers ...any) *RouterGroup

// 生命周期
func (e *Engine) Start() error                     // 阻塞,监听 cfg.Addr
func (e *Engine) Serve(listener net.Listener) error
func (e *Engine) Run(ctx context.Context) error    // 阻塞 + 监听 ctx 取消
func (e *Engine) Shutdown(ctx context.Context) error

// 状态查询
func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request)
func (e *Engine) Addr() string
func (e *Engine) Listening() bool
```

`Lifecycle` 嵌入 `*Engine`，并额外持有由 `NewLifecycle` 创建的 core 资源：

```go
func NewLifecycle(path ...string) (*Lifecycle, error)
func (l *Lifecycle) Auth(name string) (*auth.Manager, error)
func (l *Lifecycle) AuthNames() []string
func (l *Lifecycle) Run(context.Context) error
func (l *Lifecycle) Start() error
func (l *Lifecycle) Serve(net.Listener) error
func (l *Lifecycle) Shutdown(context.Context) error
func (l *Lifecycle) Close(context.Context) error
```

### RouterGroup

由 `Engine.Group` 或 `RouterGroup.Group` 创建,只负责前缀和中间件继承。

```go
func (g *RouterGroup) Use(handlers ...any) *RouterGroup
func (g *RouterGroup) Group(prefix string, handlers ...any) *RouterGroup

func (g *RouterGroup) GET(path string, handlers ...any)
func (g *RouterGroup) POST(path string, handlers ...any)
func (g *RouterGroup) PUT(path string, handlers ...any)
func (g *RouterGroup) PATCH(path string, handlers ...any)
func (g *RouterGroup) DELETE(path string, handlers ...any)
func (g *RouterGroup) HEAD(path string, handlers ...any)
func (g *RouterGroup) OPTIONS(path string, handlers ...any)
func (g *RouterGroup) Any(path string, handlers ...any)

// 静态文件服务
func (g *RouterGroup) Static(relativePath, root string)
func (g *RouterGroup) StaticFS(relativePath string, fs http.FileSystem)
func (g *RouterGroup) StaticFile(relativePath, filePath string)
```

### Handler 与绑定适配器

```go
type HandlerFunc func(*Context)
type Middleware = HandlerFunc

type HandlerWithMeta struct {
    Handler          HandlerFunc
    BusinessFuncName string
}

func Bind[Req, Resp any](fn func(*Context, *Req) (*Resp, error)) *HandlerWithMeta
func BindR[Resp any](fn func(*Context) (*Resp, error)) *HandlerWithMeta
func BindE[Req any](fn func(*Context, *Req) error) *HandlerWithMeta
func BindRE(fn func(*Context) error) *HandlerWithMeta
```

### Context — 流程控制与请求访问

```go
// 流程控制
func (c *Context) Next()
func (c *Context) Abort()
func (c *Context) IsAborted() bool

// 路径 / query / form
func (c *Context) Param(key string) string
func (c *Context) Query(key string) string
func (c *Context) DefaultQuery(key, defaultValue string) string
func (c *Context) GetQuery(key string) (string, bool)
func (c *Context) QueryArray(key string) []string
func (c *Context) GetQueryArray(key string) ([]string, bool)
func (c *Context) QueryMap(key string) map[string]string
func (c *Context) GetQueryMap(key string) (map[string]string, bool)
func (c *Context) PostForm(key string) string
func (c *Context) DefaultPostForm(key, defaultValue string) string
func (c *Context) GetPostForm(key string) (string, bool)
func (c *Context) PostFormArray(key string) []string
func (c *Context) GetPostFormArray(key string) ([]string, bool)
func (c *Context) PostFormMap(key string) map[string]string
func (c *Context) GetPostFormMap(key string) (map[string]string, bool)

// 文件 / 头 / Cookie / IP
func (c *Context) FormFile(name string) (*multipart.FileHeader, error)
func (c *Context) MultipartForm() (*multipart.Form, error)
func (c *Context) SaveUploadedFile(file *multipart.FileHeader, dst string) error
func (c *Context) Header(key string) string
func (c *Context) GetHeader(key string) string
func (c *Context) ContentType() string
func (c *Context) Cookie(name string) (string, error)
func (c *Context) SetCookie(name, value string, maxAge int, path, domain string, secure, httpOnly bool)
func (c *Context) ClientIP() string
func (c *Context) GetRawData() ([]byte, error)
func (c *Context) FullPath() string
func (c *Context) HandlerName() string
func (c *Context) IsWebsocket() bool
```

### Context — 绑定与响应

```go
// 手动绑定(失败自动写入统一错误响应并 Abort)
func (c *Context) ShouldBind(obj any) error
func (c *Context) ShouldBindJSON(obj any) error
func (c *Context) ShouldBindXML(obj any) error
func (c *Context) ShouldBindYAML(obj any) error
func (c *Context) ShouldBindTOML(obj any) error
func (c *Context) ShouldBindHeader(obj any) error
func (c *Context) ShouldBindQuery(obj any) error
func (c *Context) ShouldBindUri(obj any) error

// Aurora 统一响应(推荐)
func (c *Context) OK(data any, message ...string)
func (c *Context) Fail(err error)

// 原始渲染(绕过统一响应,谨慎使用)
func (c *Context) JSON(code int, obj any)
func (c *Context) XML(code int, obj any)
func (c *Context) YAML(code int, obj any)
func (c *Context) String(code int, format string, values ...any)
func (c *Context) HTML(code int, name string, obj any)
func (c *Context) Data(code int, contentType string, data []byte)
func (c *Context) File(filepath string)
func (c *Context) FileAttachment(filepath, filename string)
func (c *Context) Redirect(code int, location string)
func (c *Context) Stream(step func(w io.Writer) bool) bool
func (c *Context) Status(code int)
func (c *Context) SetHeader(key, value string)
```

### Context — 请求级状态

```go
func (c *Context) Set(key string, value any)
func (c *Context) Get(key string) (any, bool)
func (c *Context) MustGet(key string) any
func (c *Context) GetString(key string) string
func (c *Context) GetBool(key string) bool
func (c *Context) GetInt(key string) int
func (c *Context) GetInt64(key string) int64
func (c *Context) GetFloat64(key string) float64
func (c *Context) GetTime(key string) time.Time
func (c *Context) GetDuration(key string) time.Duration
func (c *Context) GetStringSlice(key string) []string
func (c *Context) GetStringMap(key string) map[string]any
func (c *Context) GetStringMapString(key string) map[string]string
```

### Context — 链路与底层访问

```go
func (c *Context) TraceID() string
func (c *Context) SetTraceID(traceID string)
func (c *Context) RequestID() string

// 跨层传递使用标准 context,不要把 *Context 传给业务层
func (c *Context) Context() context.Context
func (c *Context) Request() *http.Request
func (c *Context) Writer() gin.ResponseWriter
func (c *Context) Error(err error) *gin.Error
```

### Response

```go
type Response[T any] struct {
    Code    int    `json:"code"`
    Data    T      `json:"data"`
    Message string `json:"message"`
    TraceID string `json:"trace_id,omitempty"`
}
func NewResponse[T any](code int, data T, message string, traceID string) *Response[T]

type PageResp[T any] struct {
    List  []T   `json:"list"`
    Total int64 `json:"total"`
}
func NewPageResp[T any](list []T, total int64) *PageResp[T]
```

### Error 与错误工具

```go
type Error struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
    // err, status 为内部字段
}

// 构造
func NewError(code int, message string) *Error
func NewWithStatus(code int, status int, message string) *Error
func Wrap(err error, code int, message string) *Error
func WrapWithStatus(err error, code int, status int, message string) *Error

// 链式修改(返回新实例)
func (e *Error) WithErr(err error) *Error
func (e *Error) WithStatus(status int) *Error
func (e *Error) WithMessage(message string) *Error
func (e *Error) WithMessagef(format string, args ...any) *Error

// 访问器与接口
func (e *Error) Error() string
func (e *Error) Unwrap() error
func (e *Error) Status() int
func (e *Error) Is(target error) bool

// 错误链查询
func GetCode(err error) int
func GetStatus(err error) int
func IsCode(err error, code int) bool
func As(err error, target any) bool
func Is(err, target error) bool
func Join(errs ...error) error
```

预定义错误(完整列表见 [`errors.go`](./errors.go)):

```go
var (
    ErrInternal            *Error  // 1000 / 500
    ErrBadRequest          *Error  // 1001 / 400
    ErrUnauthorized        *Error  // 1003 / 401
    ErrForbidden           *Error  // 1004 / 403
    ErrNotFound            *Error  // 1005 / 404
    ErrMethodNotAllowed    *Error  // 1006 / 405
    ErrRequestTimeout      *Error  // 1007 / 408
    ErrConflict            *Error  // 1008 / 409
    ErrPayloadTooLarge     *Error  // 1009 / 413
    ErrUnprocessableEntity *Error  // 1010 / 422
    ErrTooManyRequests     *Error  // 1011 / 429
    ErrBadGateway          *Error  // 1012 / 502
    ErrServiceUnavailable  *Error  // 1013 / 503
    ErrGatewayTimeout      *Error  // 1014 / 504
    ErrAlreadyExists       *Error  // 1015 / 409
)
```

### Option

```go
type Option func(*Engine)

func WithLogger(log *logger.Logger) Option
func WithTracingRuntime(rt TracingRuntime) Option
func WithMetricsProvider(provider metric.MeterProvider) Option
```

### 中间件构造函数

框架默认按固定顺序装配,也可以单独取用(顺序由调用方保证):

```go
func MaxBodyBytesMiddleware(maxBytes int64) Middleware
func RequestIDMiddleware() Middleware
func RecoveryMiddleware(log *logger.Logger) Middleware
func AccessLogMiddleware(log *logger.Logger) Middleware
func TracingMiddleware(rt TracingRuntime) Middleware
```

### Config 与 MiddlewareConfig

```go
type Config struct {
    Addr              string
    Mode              string           // debug | release | test
    ReadTimeout       time.Duration
    ReadHeaderTimeout time.Duration
    WriteTimeout      time.Duration
    IdleTimeout       time.Duration
    MaxHeaderBytes    int
    MaxBodyBytes      int64
    TrustedProxies    []string
    Middleware        MiddlewareConfig
}
func DefaultConfig() Config
func (c *Config) Validate() error

type MiddlewareConfig struct {
    RequestID bool
    Recovery  bool
    AccessLog bool
    Tracing   bool
    Metrics   bool
}
```

### TracingRuntime

由应用实现并注入,Aurora 只借用不创建:

```go
type TracingRuntime interface {
    TracerProvider() trace.TracerProvider
    Propagator() propagation.TextMapPropagator
    Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context
    Inject(ctx context.Context, carrier propagation.TextMapCarrier)
    Shutdown(ctx context.Context) error
}
```

参考实现见 [`core/tracing`](./core/tracing/)。

## 📌 版本与兼容性

当前版本:**`v0.0.4`**(第一阶段,API 仍在演进)

| 版本 | 状态 | 说明 |
| --- | --- | --- |
| `v0.0.x` | 🚧 开发中 | 第一阶段 Web 框架核心:Engine / Context / Bind / 中间件 / 错误模型 |
| `v0.1.0`(规划) | 📋 待定 | 完成第一阶段的 API 稳定化,补全文档与示例 |

版本规则:**`v0.x` 阶段不保证向后兼容**,破坏性变更会在 release note 中显式标注;进入 `v1.0` 后遵守 SemVer。

## 📖 设计文档

完整的设计目标、非目标、边界约定和设计决策请参阅 [DESIGN.md](./DESIGN.md)。README 聚焦"怎么用",DESIGN 回答"为什么这样设计"。
