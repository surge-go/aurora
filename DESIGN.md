# Aurora Web 框架设计

## 1. 设计范围

Aurora 根包 `github.com/surge-go/aurora` 是基于 Gin 的 Go Web 应用框架。
第一阶段提供 HTTP 服务、路由、请求上下文、统一响应、业务错误、泛型绑定和通用中间件。

Web 框架代码直接放在仓库根目录，根包直接持有 Gin Engine 和 `net/http.Server`；不新增
`core/server` 子包。现有 `core` 下的配置、日志、Tracing、Metrics、数据库、
Redis 和 MQ 仍作为独立基础设施包存在，由应用组合根创建并注入。

本阶段明确不包含 OpenAPI、路由元信息、Swagger UI、自动文档生成、认证授权、数据库/Redis/MQ
自动装配和业务模块代码。

## 2. 目标与非目标

### 目标

- 保留 Gin 的路由和中间件能力，同时提供稳定的 Aurora 业务开发 API。
- 让业务 Handler 使用标准库 `context.Context`，可以直接传递给数据库、Redis、MQ 和业务服务。
- 提供显式、可测试、可组合的 HTTP Server 生命周期。
- 提供安全的统一响应和业务错误映射，避免内部错误泄露到 HTTP 响应。
- 复用现有 Aurora logger、tracing 和 metrics 的资源所有权约定。
- 支持原生 Gin escape hatch，避免框架 API 覆盖 Gin 的全部能力。

### 非目标

- 不在 `New` 中创建或关闭数据库、Redis、MQ、Logger、TracerProvider 或 MeterProvider。
- 不在框架内部安装或替换 OpenTelemetry 全局 Provider。
- 不监听操作系统信号，不调用 `os.Exit`，不决定进程退出码。
- 不定义认证、授权、CORS、限流、幂等、上传、WebSocket 或业务审计策略。
- 不把 Gin Context 传入 service、repository 或其他可能长期持有上下文的组件。

## 3. 包结构

第一阶段新增根包文件：

```text
aurora.go       # Engine、New、生命周期和根级选项
config.go       # Config、MiddlewareConfig 和默认值
context.go      # HTTP Context 封装
handler.go      # HandlerFunc、Middleware 和泛型绑定
router.go       # RouterGroup、路由注册和原生 Gin 适配
response.go     # Response 和响应写入
errors.go       # Error、错误构造和预定义错误
middleware.go   # 框架默认中间件装配
*_test.go       # 根包测试
```

现有基础设施目录保持不变：

```text
core/config
core/logger
core/tracing
core/metrics
core/database
core/redis
core/mq
```

如果后续某个中间件需要较多内部实现，可以新增根目录下的 `internal/` 子目录，但不把
HTTP Server 再拆成 `core/server`，除非未来出现独立复用需求并重新评审包边界。

## 4. 依赖方向与所有权

```text
application bootstrap
        |
        v
aurora root package
        |
        +----> gin
        +----> net/http
        +----> core/logger adapter
        +----> OpenTelemetry API
        |
        +----> application routes and middleware

application bootstrap ----> core/config, tracing, metrics, database, redis, mq
application bootstrap ----> aurora options (inject runtime dependencies)
```

Aurora Engine 所有并关闭的资源只有：

- Gin Engine；
- `http.Server`；
- `net.Listener`（由 `Start` 创建或由 `Serve` 接管）。

Logger、TracerProvider、Propagator、MeterProvider、数据库连接、Redis 客户端和 MQ 客户端由
应用组合根拥有。Aurora 只借用它们，不在 `Shutdown` 中关闭它们。

## 5. 配置模型

配置面向 YAML/JSON/mapstructure，运行时对象通过 Option 注入，不放进配置文件。

```go
type Config struct {
	Addr              string           `json:"addr" yaml:"addr" mapstructure:"addr"`
	Mode              string           `json:"mode" yaml:"mode" mapstructure:"mode"`
	ReadTimeout       time.Duration    `json:"read_timeout" yaml:"read_timeout" mapstructure:"read_timeout"`
	ReadHeaderTimeout time.Duration    `json:"read_header_timeout" yaml:"read_header_timeout" mapstructure:"read_header_timeout"`
	WriteTimeout      time.Duration    `json:"write_timeout" yaml:"write_timeout" mapstructure:"write_timeout"`
	IdleTimeout       time.Duration    `json:"idle_timeout" yaml:"idle_timeout" mapstructure:"idle_timeout"`
	MaxHeaderBytes    int              `json:"max_header_bytes" yaml:"max_header_bytes" mapstructure:"max_header_bytes"`
	TrustedProxies    []string         `json:"trusted_proxies" yaml:"trusted_proxies" mapstructure:"trusted_proxies"`
	Middleware        MiddlewareConfig `json:"middleware" yaml:"middleware" mapstructure:"middleware"`
}

type MiddlewareConfig struct {
	RequestID bool `json:"request_id" yaml:"request_id" mapstructure:"request_id"`
	Recovery  bool `json:"recovery" yaml:"recovery" mapstructure:"recovery"`
	AccessLog bool `json:"access_log" yaml:"access_log" mapstructure:"access_log"`
	Tracing   bool `json:"tracing" yaml:"tracing" mapstructure:"tracing"`
}
```

默认值：

```text
Addr              :8080
Mode              release
ReadTimeout       15s
ReadHeaderTimeout 5s
WriteTimeout      30s
IdleTimeout       60s
MaxHeaderBytes    1 MiB
RequestID         true
Recovery          true
AccessLog         true
Tracing           false
```

`Addr` 使用标准监听地址语义，例如 `:8080`、`127.0.0.1:8080` 或
`[::1]:8080`。使用 `:0` 时由系统分配端口，`Addr()` 在启动后返回实际地址。

`Validate` 只做确定性检查：地址非空、超时时间和请求头大小不能为负、Gin mode 必须是
`debug`、`release` 或 `test`、Trusted Proxies 不能包含空项。校验不监听端口、不创建网络连接。

停机超时不放进 `Config`，统一由 `Shutdown(ctx)` 的 deadline 表达，避免配置和调用方 context
出现两个冲突的超时来源。

## 6. Engine API

```go
type Engine struct {
	// 持有 gin.Engine、http.Server 和生命周期状态。
}

func New(cfg Config, opts ...Option) (*Engine, error)

func (e *Engine) GET(path string, handlers ...HandlerFunc)
func (e *Engine) POST(path string, handlers ...HandlerFunc)
func (e *Engine) PUT(path string, handlers ...HandlerFunc)
func (e *Engine) PATCH(path string, handlers ...HandlerFunc)
func (e *Engine) DELETE(path string, handlers ...HandlerFunc)
func (e *Engine) HEAD(path string, handlers ...HandlerFunc)
func (e *Engine) OPTIONS(path string, handlers ...HandlerFunc)
func (e *Engine) Any(path string, handlers ...HandlerFunc)

func (e *Engine) Use(handlers ...Middleware)
func (e *Engine) Group(prefix string, handlers ...Middleware) *RouterGroup

func (e *Engine) Start() error
func (e *Engine) Serve(net.Listener) error
func (e *Engine) Shutdown(context.Context) error
func (e *Engine) Run(context.Context) error

func (e *Engine) ServeHTTP(http.ResponseWriter, *http.Request)
func (e *Engine) Gin() *gin.Engine
func (e *Engine) Addr() string
func (e *Engine) Listening() bool
```

`New` 只创建 Engine，不监听端口。路由和全局应用中间件必须在 `Start`、`Serve` 或 `Run`
之前注册；服务开始监听后不支持修改路由树。

`Gin()` 是 escape hatch，返回底层 `*gin.Engine`。通过该入口注册的原生 Gin handler 必须遵守
同样的上下文传递和错误响应约定；框架不尝试拦截任意自定义响应。

## 7. Handler、Context 和路由分组

```go
type HandlerFunc func(*Context)
type Middleware = HandlerFunc

type Context struct {
	// 内部持有 *gin.Context。
}

func (c *Context) Gin() *gin.Context
func (c *Context) Request() *http.Request
func (c *Context) Context() context.Context
func (c *Context) Param(string) string
func (c *Context) Query(string) string
func (c *Context) Header(string) string
func (c *Context) ClientIP() string
func (c *Context) Set(string, any)
func (c *Context) Get(string) (any, bool)
func (c *Context) Next()
func (c *Context) Abort()
```

`Context()` 返回 `c.Request().Context()`。业务代码、数据库、Redis、MQ 和下游 HTTP 客户端
只接收这个标准 context；不得把 `*Context` 或 `*gin.Context` 传入业务层并长期保存。

```go
type RouterGroup struct {
	// Engine、路径前缀和该分组的 middleware chain。
}

func (g *RouterGroup) Use(handlers ...Middleware)
func (g *RouterGroup) Group(prefix string, handlers ...Middleware) *RouterGroup
func (g *RouterGroup) GET(path string, handlers ...HandlerFunc)
func (g *RouterGroup) POST(path string, handlers ...HandlerFunc)
func (g *RouterGroup) PUT(path string, handlers ...HandlerFunc)
func (g *RouterGroup) PATCH(path string, handlers ...HandlerFunc)
func (g *RouterGroup) DELETE(path string, handlers ...HandlerFunc)
func (g *RouterGroup) HEAD(path string, handlers ...HandlerFunc)
func (g *RouterGroup) OPTIONS(path string, handlers ...HandlerFunc)
func (g *RouterGroup) Any(path string, handlers ...HandlerFunc)
```

路由分组只负责前缀和 middleware 继承，不携带认证、权限或业务依赖。

## 8. 泛型绑定

第一阶段提供四种适配器：

```go
func Bind[Req any, Resp any](
	fn func(context.Context, *Req) (*Resp, error),
) HandlerFunc

func BindR[Resp any](
	fn func(context.Context) (*Resp, error),
) HandlerFunc

func BindE[Req any](
	fn func(context.Context, *Req) error,
) HandlerFunc

func BindRE(
	fn func(context.Context) error,
) HandlerFunc
```

绑定规则：

- `POST`、`PUT`、`PATCH` 优先绑定 body，并额外绑定 URI 参数；
- `GET`、`DELETE`、`HEAD`、`OPTIONS` 绑定 query 和 URI 参数；
- `uri` tag 触发路径参数绑定；
- `form` tag 触发 query/form 绑定；
- JSON、XML、form 等 body 格式由 Gin binding 根据 Content-Type 选择；
- 绑定或校验失败返回 `ErrBadRequest`，底层校验错误只进入日志，不直接返回；
- Handler 返回 error 时调用 `Context.Fail`，成功结果调用 `Context.OK`。

绑定器不负责业务校验、权限判断、事务和数据库操作。

## 9. 响应和错误

```go
type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
	TraceID string `json:"trace_id,omitempty"`
}

func (c *Context) JSON(status int, value any)
func (c *Context) OK(data any, message ...string)
func (c *Context) Fail(err error)
```

```go
type Error struct {
	Code       int
	HTTPStatus int
	Message    string
	Cause      error
}

func NewError(code, status int, message string) *Error
func WrapError(cause error, code, status int, message string) *Error
```

预定义错误至少包括：

```go
var (
	ErrBadRequest   = NewError(1001, http.StatusBadRequest, "bad request")
	ErrUnauthorized = NewError(1002, http.StatusUnauthorized, "unauthorized")
	ErrForbidden    = NewError(1003, http.StatusForbidden, "forbidden")
	ErrNotFound     = NewError(1004, http.StatusNotFound, "not found")
	ErrInternal     = NewError(1000, http.StatusInternalServerError, "internal server error")
)
```

`Error` 实现 `error` 和 `Unwrap`，业务层可以通过 `errors.Is`/`errors.As` 检查 Cause。
`Context.Fail` 对外只输出安全的 `Message`，不能把 `Cause.Error()`、SQL、Redis key、连接地址
或堆栈写入响应。原始 Cause 只能交给日志或 tracing 记录。

非 Aurora Error 的未知错误统一映射为 `ErrInternal`，响应状态为 500。

## 10. 通用中间件

默认顺序：

```text
RequestID -> Tracing -> Recovery -> AccessLog -> application middleware -> Handler
```

### RequestID

- 读取 `X-Request-ID`；
- 只接受长度受限的 ASCII 字母、数字、`-`、`_`、`.`、`:`；
- 非法或为空时使用 `crypto/rand` 生成 ID；
- 写入 Aurora Context、request context 和响应头；
- 提供 `RequestIDFromContext(context.Context) string` 辅助函数。

### Recovery

- 捕获业务 Handler panic；
- 记录 panic 值、stacktrace、request ID 和 trace ID；
- 响应尚未写出时返回 `ErrInternal`；
- 响应已经写出时只 `Abort`，不追加第二个响应；
- `http.ErrAbortHandler` 按 net/http 语义重新 panic，不伪装成业务错误；
- 不依赖不存在的业务包，允许通过 Option 注入自定义恢复响应器。

### AccessLog

只记录低敏感、低基数信息：

```text
method, route template, status, latency, client ip, request id, trace id
```

优先使用 `c.FullPath()`，没有路由模板时才使用 URL path。默认不记录 query、Authorization、
Cookie、请求体、User-Agent、完整 header 和错误原文。日志使用 Aurora logger 的结构化接口，
不硬编码特定项目名或 ANSI 颜色。

### Tracing

通过 Option 注入：

```go
func WithTracerProvider(
	provider trace.TracerProvider,
	propagator propagation.TextMapPropagator,
) Option
```

Tracing middleware：

1. 使用 propagator 从请求头提取 W3C Trace Context；
2. 使用注入的 provider 创建 HTTP server span；
3. 将带 span 的 context 写回 `*http.Request`；
4. 使用 `METHOD + route template` 作为 span name，避免动态路径高基数；
5. 写入 trace ID 到 Context，按配置写入 `X-Trace-ID` 响应头；
6. 请求结束后记录路由、状态码和响应大小，5xx 标记为错误；
7. 未注入 provider 时使用 OpenTelemetry no-op/global API，不创建或关闭 Provider。

## 11. Option 与外部依赖

```go
type Option func(*options) error

func WithLogger(log Logger) Option
func WithTracerProvider(provider trace.TracerProvider, propagator propagation.TextMapPropagator) Option
func WithRecoveryHandler(fn RecoveryHandler) Option
```

根包不能在公开 API 中要求调用方导入 `core/logger`。因此第一版定义最小公开日志接口：

```go
type Logger interface {
	InfoContext(context.Context, string, ...Field)
	ErrorContext(context.Context, string, ...Field)
}
```

`Field` 需要是根包可导出的轻量字段类型，或由 logger adapter 提供；根包不得把 zap 类型暴露
给业务代码。Aurora 当前 `core/logger.Logger` 通过 adapter 接入该接口。

如果公开 Logger 接口在实现阶段造成过多适配复杂度，可以先将日志 Option 设计为内部适配层，
但必须保证根包公开 API 不直接暴露 `internal` 路径。

## 12. 生命周期

```text
New
  -> 注册全局 middleware、路由和应用依赖
  -> Start 或 Serve
  -> Listening
  -> Shutdown(ctx)
```

状态要求：

- `New` 不监听端口；
- `Start` 内部调用 `net.Listen`，成功后调用 `http.Server.Serve`；
- `Serve` 接管调用方传入的 listener，Server 关闭时负责关闭它；
- 同一个 Engine 只能成功启动一次；重复启动返回稳定的 `ErrServerStarted`；
- Shutdown 后再次启动返回 `ErrServerClosed`；
- `Shutdown(nil)` 返回 `ErrNilContext`；
- `Shutdown` 调用 `http.Server.Shutdown`，停止接收新连接、关闭空闲连接并等待活动请求；
- Shutdown 超时返回 context/HTTP Server 错误，不强制中断 Handler；
- WebSocket/hijack 连接由独立连接管理器负责；
- 重复或并发 Shutdown 必须安全，不能二次关闭 listener。

`Run(ctx)` 是便利 API：启动服务并在 context 取消时调用 Shutdown。它不监听系统信号。
应用入口负责把 `signal.NotifyContext` 传入 `Run`，并在 HTTP Server 关闭后按应用顺序关闭其他资源。

推荐应用关闭顺序：

```text
停止接收应用信号
  -> Aurora.Shutdown
  -> database.Close / redis.Close / mq.Close
  -> tracing.Shutdown
  -> metrics.Shutdown
  -> logger.Sync/Close
```

## 13. 应用示例

```go
app, err := aurora.New(aurora.Config{
	Addr: ":8080",
})
if err != nil {
	return err
}

app.GET("/health", func(c *aurora.Context) {
	c.OK(map[string]string{"status": "ok"})
})

api := app.Group("/api/v1")
api.Use(authMiddleware())
api.POST("/users", aurora.Bind(createUser))

ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
return app.Run(ctx)
```

业务函数只接收标准 context：

```go
func createUser(ctx context.Context, req *CreateUserRequest) (*User, error) {
	return userService.Create(ctx, req)
}
```

需要 Gin 特有能力时使用：

```go
app.Gin().GET("/raw", func(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"ok": true})
})
```

## 14. 实施阶段

### 阶段一：HTTP 和路由核心

- 引入 Gin；
- 实现 Config、默认值和校验；
- 实现 Engine、RouterGroup、原生 Gin 适配；
- 实现 Start、Serve、Shutdown、Run；
- 覆盖监听失败、临时端口、重复启动、优雅关闭和并发关闭。

### 阶段二：Context、响应和错误

- 实现 Context 常用方法；
- 实现 Response、Error 和预定义错误；
- 实现未知错误的安全 500 映射；
- 覆盖 `errors.Is`/`errors.As` 和响应脱敏。

### 阶段三：绑定和默认中间件

- 实现 Bind、BindR、BindE、BindRE；
- 实现 RequestID、Recovery、AccessLog；
- 覆盖 body/query/URI 混合绑定、panic 和敏感日志字段。

### 阶段四：可观测性接入

- 注入现有 tracing Provider 和 propagator；
- 实现 HTTP tracing middleware；
- 增加 HTTP metrics 的注入边界，但不在根包创建 MeterProvider；
- 覆盖 trace parent、路由模板、5xx 和关闭顺序。

OpenAPI、路由元信息、Swagger UI 和自动文档生成不属于以上阶段，后续单独设计。

## 15. 测试与验收

至少覆盖：

- Config 默认值、非法值和 `:0` 临时端口；
- Engine 原生路由、分组、middleware 顺序和 `ServeHTTP`；
- Start/Serve/Shutdown 成功、失败、取消、重复和并发路径；
- Request ID 合法/非法输入和 context 传递；
- Recovery 未写响应和已写响应两条路径；
- AccessLog 不输出 query、Authorization、Cookie、body 和动态路径高基数；
- 泛型绑定的 body、query、URI、混合参数和校验失败；
- Error 的安全响应、Cause 保留和未知错误映射；
- Tracing 的上游提取、span context 传递、路由模板和 5xx 状态。

验证命令：

```bash
gofmt -w <modified-go-files>
go test ./...
go build ./...
go test -race ./...
```

涉及新增依赖时同时检查 `go.mod`、`go.sum` 和最小 Go 版本兼容性。
