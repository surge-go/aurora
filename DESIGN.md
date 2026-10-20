# Aurora Web 框架设计

## 1. 设计范围

Aurora 根包 `github.com/surge-go/aurora` 是基于 Gin 的 Go Web 应用框架。
第一阶段提供 HTTP 服务、路由、静态文件服务 API、请求上下文、统一响应、业务错误、泛型绑定和通用中间件。
根包同时提供可选的 `NewLifecycle` bootstrap，用于从 `config/aurora.yaml` 组合 core
基础设施并统一管理它们的生命周期。

Web 框架代码直接放在仓库根目录，根包直接持有 Gin Engine 和 `net/http.Server`；不新增
`core/server` 子包。现有 `core` 下的配置、日志、Tracing、Metrics、数据库、
Redis 和 MQ 仍作为独立基础设施包存在，由应用组合根创建并注入。

本阶段明确不包含 OpenAPI、路由元信息、Swagger UI、自动文档生成和业务模块代码。根包不
直接实现认证授权；通用 JWT/Redis 生命周期由独立的 `core/auth` 包设计和提供，认证中间件
仍需由应用或后续适配层装配。普通 `New` 仍不自动装配数据库、Redis、MQ 或可观测性资源；
需要统一 bootstrap 时使用 `NewLifecycle`。

## 2. 目标与非目标

### 目标

- 保留 Gin 的路由和中间件能力，同时提供稳定的 Aurora 业务开发 API。
- 让业务 Handler 使用 Aurora `*Context`，并通过 `c.Context()` 将标准库 `context.Context` 传递给数据库、Redis、MQ 和业务服务。
- 提供显式、可测试、可组合的 HTTP Server 生命周期。
- 提供安全的统一响应和业务错误映射，避免内部错误泄露到 HTTP 响应。
- 复用现有 Aurora logger、tracing 和 metrics 的资源所有权约定。
- 通过 Aurora 自有路由、上下文和中间件 API 提供稳定的业务开发接口。
- 提供 `Static`、`StaticFS` 和 `StaticFile` 静态资源路由注册入口。

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
lifecycle.go    # NewLifecycle、core bootstrap 和资源关闭
config.go       # Config、MiddlewareConfig 和默认值
context.go      # HTTP Context 封装
handler.go      # HandlerFunc、Middleware 和泛型绑定
router.go       # RouterGroup、路由注册和内部路由适配
response.go     # Response 和响应写入
errors.go       # Error、错误构造和预定义错误
middleware.go   # 框架默认中间件装配
middleware/     # 可选的可复用业务中间件适配器（如 Auth）
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
core/auth
```

框架默认中间件继续放在根包；可复用的业务适配器单独放在根目录的 `middleware/` 包，
避免根包引入认证、权限等业务依赖。HTTP Server 不拆成 `core/server`，除非未来出现独立
复用需求并重新评审包边界。

`core/auth` 使用 JWT v5 和注入的 Redis 客户端管理 access/refresh token 生命周期。它不创建或
关闭 Redis，也不属于根包默认中间件；使用方式和 `MaxSessionTTL`、`RevokeSubject` 分页契约见
`core/auth/DESIGN.md`。

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

普通 Aurora Engine 所有并关闭的资源只有：

- Gin Engine；
- `http.Server`；
- `net.Listener`（由 `Start` 创建或由 `Serve` 接管）。

Logger、TracerProvider、Propagator、MeterProvider、数据库连接、Redis 客户端和 MQ 客户端由
应用组合根拥有。普通 `New` 创建的 Engine 只借用它们，不在 `Shutdown` 中关闭它们。

`NewLifecycle` 是显式的应用组合根：它加载或生成默认配置，创建启用的 logger、tracing、
metrics、database、Redis、Auth 和 MQ 资源，并由返回的 `Lifecycle` 持有和关闭这些资源。Auth
配置段启用时必须同时启用 Redis，并在 `auth.<name>.signing_secret` 中分别提供至少 32 字节的签名密钥；
配置文件必须由部署系统保护。Auth 不单独拥有或关闭 Redis。关闭顺序
为 HTTP Server、MQ、database、Redis、metrics、tracing、logger、config；默认 database、
Redis、Auth、MQ、tracing 和 metrics 均关闭，首次启动不会连接外部服务。配置文件中的这些外部
资源使用可选配置段：配置段缺失（例如整段保持注释）时解析为 `nil`，不会初始化；不使用
`enabled: false` 作为额外开关。

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
	MaxBodyBytes      int64            `json:"max_body_bytes" yaml:"max_body_bytes" mapstructure:"max_body_bytes"`
	TrustedProxies    []string         `json:"trusted_proxies" yaml:"trusted_proxies" mapstructure:"trusted_proxies"`
	Middleware        MiddlewareConfig `json:"middleware" yaml:"middleware" mapstructure:"middleware"`
}

type MiddlewareConfig struct {
	RequestID bool `json:"request_id" yaml:"request_id" mapstructure:"request_id"`
	Recovery  bool `json:"recovery" yaml:"recovery" mapstructure:"recovery"`
	AccessLog bool `json:"access_log" yaml:"access_log" mapstructure:"access_log"`
	Tracing   bool `json:"tracing" yaml:"tracing" mapstructure:"tracing"`
	Metrics   bool `json:"metrics" yaml:"metrics" mapstructure:"metrics"`
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
MaxBodyBytes      10 MiB
RequestID         true
Recovery          true
AccessLog         true
Tracing           false
Metrics           false
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

// 从 config/aurora.yaml 创建并管理完整应用生命周期；不启动 HTTP 服务。
func NewLifecycle(path ...string) (*Lifecycle, error)

type Lifecycle struct {
	*Engine
	Config       *config.Config
	Logger       *logger.Logger
	Tracing      *tracing.Runtime
	Metrics      *metrics.Runtime
	Database     *gorm.DB
	Redis        redis.UniversalClient
	authManagers map[string]*auth.Manager // 初始化完成后只读
	MQPublisher  mq.Publisher
	MQConsumer   mq.Consumer
}

func (l *Lifecycle) Auth(name string) (*auth.Manager, error)
func (l *Lifecycle) AuthNames() []string
func (l *Lifecycle) Run(context.Context) error
func (l *Lifecycle) Start() error
func (l *Lifecycle) Serve(net.Listener) error
func (l *Lifecycle) Shutdown(context.Context) error
func (l *Lifecycle) Close(context.Context) error

func (e *Engine) GET(path string, handlers ...HandlerFunc)
func (e *Engine) POST(path string, handlers ...HandlerFunc)
func (e *Engine) PUT(path string, handlers ...HandlerFunc)
func (e *Engine) PATCH(path string, handlers ...HandlerFunc)
func (e *Engine) DELETE(path string, handlers ...HandlerFunc)
func (e *Engine) HEAD(path string, handlers ...HandlerFunc)
func (e *Engine) OPTIONS(path string, handlers ...HandlerFunc)
func (e *Engine) Any(path string, handlers ...HandlerFunc)

func (e *Engine) Static(relativePath, root string)
func (e *Engine) StaticFS(relativePath string, fs http.FileSystem)
func (e *Engine) StaticFile(relativePath, filePath string)

func (e *Engine) Use(handlers ...Middleware)
func (e *Engine) Group(prefix string, handlers ...Middleware) *RouterGroup

func (e *Engine) Start() error
func (e *Engine) Serve(net.Listener) error
func (e *Engine) Shutdown(context.Context) error
func (e *Engine) Run(context.Context) error

func (e *Engine) ServeHTTP(http.ResponseWriter, *http.Request)
func (e *Engine) Addr() string
func (e *Engine) Listening() bool
```

`New` 和 `NewLifecycle` 都只创建对象，不监听端口。路由和全局应用中间件必须在 `Start`、
`Serve` 或 `Run` 之前注册；服务开始监听后不支持修改路由树。`NewLifecycle` 在没有路径参数
时使用当前工作目录下的 `config/aurora.yaml`，文件不存在时创建默认模板；显式传入的路径
不会被覆盖。

## 7. Handler、Context 和路由分组

```go
type HandlerFunc func(*Context)
type Middleware = HandlerFunc

type Context struct {
	// 内部持有请求上下文实现。
}

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
只接收这个标准 context；不得把请求上下文封装对象传入业务层并长期保存。

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
func (g *RouterGroup) Static(relativePath, root string)
func (g *RouterGroup) StaticFS(relativePath string, fs http.FileSystem)
func (g *RouterGroup) StaticFile(relativePath, filePath string)
```

路由分组只负责前缀和 middleware 继承，不携带认证、权限或业务依赖。

### 7.1 静态文件服务 API

静态文件服务 API 只负责声明资源 URL 与文件来源的映射关系：

- `Static(relativePath, root)` 映射本地目录；
- `StaticFS(relativePath, fs)` 映射 `http.FileSystem`；
- `StaticFile(relativePath, filePath)` 映射单个文件。

这三组方法同时存在于 `Engine` 和 `RouterGroup`。分组版本会继承分组前缀和中间件，
因此可以将静态资源挂载到指定 API 或 Web 前缀下。当前实现复用底层 Gin 的静态文件
注册能力；目录索引、缓存、隐藏文件、SPA 回退、路径穿越防护和错误页等行为以 Gin
实现为准，Aurora 暂不额外增加一套静态资源策略。

## 8. 泛型绑定

第一阶段提供四种适配器：

```go
func Bind[Req any, Resp any](
	fn func(c *Context, req *Req) (*Resp, error),
) HandlerFunc

func BindR[Resp any](
	fn func(c *Context) (*Resp, error),
) HandlerFunc

func BindE[Req any](
	fn func(c *Context, req *Req) error,
) HandlerFunc

func BindRE(
	fn func(c *Context) error,
) HandlerFunc
```

**重要：** 业务函数接收 `*Context` 而不是 `context.Context`，这样业务函数可以直接访问请求上下文的所有方法（如 `c.Param()`、`c.Query()` 等）。如需传递给数据库、Redis、MQ 等基础设施，通过 `c.Context()` 获取标准 `context.Context`。

绑定规则：

- `POST`、`PUT`、`PATCH` 优先绑定 body，并额外绑定 URI 参数；
- `GET`、`DELETE`、`HEAD`、`OPTIONS` 绑定 query 和 URI 参数；
- `uri` tag 触发路径参数绑定；
- `form` tag 触发 query/form 绑定；
- JSON、XML、form 等 body 格式由 Gin binding 根据 Content-Type 选择；
- 绑定或校验失败自动调用 `Context.Fail` 返回 `ErrBadRequest`，底层校验错误不直接返回；
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
	Code    int
	Message string
	err     error  // 原始错误（内部字段）
	status  int    // HTTP 状态码（内部字段）
}

func NewError(code int, message string) *Error
func NewWithStatus(code int, status int, message string) *Error
func Wrap(err error, code int, message string) *Error
func WrapWithStatus(err error, code int, status int, message string) *Error

// 链式方法（返回新实例）
func (e *Error) WithErr(err error) *Error
func (e *Error) WithStatus(status int) *Error
func (e *Error) WithMessage(message string) *Error
func (e *Error) WithMessagef(format string, args ...any) *Error

// 查询方法
func (e *Error) Error() string
func (e *Error) Unwrap() error
func (e *Error) Status() int
```

预定义错误：

```go
var (
	ErrBadRequest          = NewWithStatus(1001, http.StatusBadRequest, "请求无效")
	ErrUnauthorized        = NewWithStatus(1002, http.StatusUnauthorized, "未授权")
	ErrForbidden           = NewWithStatus(1003, http.StatusForbidden, "禁止访问")
	ErrNotFound            = NewWithStatus(1004, http.StatusNotFound, "资源未找到")
	ErrMethodNotAllowed    = NewWithStatus(1005, http.StatusMethodNotAllowed, "方法不允许")
	ErrConflict            = NewWithStatus(1006, http.StatusConflict, "资源冲突")
	ErrTooManyRequests     = NewWithStatus(1007, http.StatusTooManyRequests, "请求过多")
	ErrInternal            = NewWithStatus(1000, http.StatusInternalServerError, "内部服务器错误")
	ErrServiceUnavailable  = NewWithStatus(1008, http.StatusServiceUnavailable, "服务不可用")
	ErrGatewayTimeout      = NewWithStatus(1009, http.StatusGatewayTimeout, "网关超时")
)
```

**实现要点：**

- `Error` 实现 `error` 接口和 `Unwrap()`，业务层可以通过 `errors.Is`/`errors.As` 检查原因
- `Context.Fail` 自动调用 `c.Abort()` 中止后续 Handler，对外只输出安全的 `Message`
- 不能把 `err.Error()`、SQL、Redis key、连接地址或堆栈写入响应，原始错误只交给日志或 tracing
- 非 Aurora Error 的未知错误统一映射为 `ErrInternal`，响应状态为 500
- 链式方法返回新实例，避免污染预定义错误

## 10. 通用中间件

默认顺序：

```text
RequestID -> Metrics -> Recovery -> Tracing -> AccessLog -> application middleware -> Handler
```

所有中间件通过配置开关控制，运行时依赖（Logger、TracingRuntime、MeterProvider）通过 Option 注入。

### RequestID

```go
func RequestIDMiddleware() Middleware
```

- 读取 `X-Request-ID` 请求头；
- 为空时使用 `uuid.New()` 生成新的 UUID；
- 写入 Context (`request_id` key) 和响应头 `X-Request-ID`；
- 可通过 `c.TraceID()` 获取。

### Metrics

HTTP 指标通过注入的 OpenTelemetry `metric.MeterProvider` 采集。启用 Metrics 时必须注入 Provider；
Aurora 只创建和使用 Meter，不负责关闭 Provider。

```go
func WithMetricsProvider(provider metric.MeterProvider) Option
```

采集以下指标：

- `http.server.active_requests`：当前处理中的请求数；
- `http.server.request.duration`：请求耗时，单位秒；
- `http.server.request.body.size`：请求体大小，单位字节；
- `http.server.response.body.size`：响应体大小，单位字节。

指标属性只包含 `http.request.method`、`http.route` 和 `http.response.status_code`。
`http.route` 使用路由模板；未匹配路由不记录原始 URL，避免动态路径造成高基数。
不记录 query、Cookie、Authorization、RequestID、TraceID、客户端 IP 或用户输入。
请求体大小在响应产生前记录，因此只包含 method 和 route；耗时与响应体大小还包含最终状态码。

### Recovery

```go
func RecoveryMiddleware(logger *logger.Logger) Middleware
```

- 捕获业务 Handler 和后续中间件的 panic；
- 使用注入的 logger 记录 panic 信息（error、request_id、method、path、client_ip）；
- 自动调用 `c.Fail(ErrInternal)` 返回统一错误响应；
- 如果没有注入 logger，只返回错误响应不记录日志。

### Tracing

```go
func TracingMiddleware(rt TracingRuntime) Middleware
```

- 使用注入的 OpenTelemetry Tracer 创建 Span；
- 从请求的 context 中提取 trace context；
- 设置 Span 属性：http.method、http.url、http.route、http.client_ip、http.status_code；
- 将新的 context 设置回 gin.Context；
- 如果没有注入 tracer，跳过处理。

### AccessLog

```go
func AccessLogMiddleware(logger *logger.Logger) Middleware
```

记录请求信息：

- 使用 `[Aurora]` 纯文本格式输出访问日志，不添加 JSON 外层或结构化字段
- 格式为：`[Aurora] 时间 | 状态码 | 耗时 | 客户端 IP | 方法 路径 trace_id=...`
- 不记录 query、Authorization、Cookie、请求体和 User-Agent

**配置示例：**

```go
cfg := aurora.Config{
    Middleware: aurora.MiddlewareConfig{
        RequestID: true,  // 默认开启
        Recovery:  true,  // 默认开启
        AccessLog: true,  // 默认开启
        Tracing:   false, // 默认关闭
        Metrics:   false, // 默认关闭
    },
}

engine, err := aurora.New(cfg,
    aurora.WithLogger(logger),
    aurora.WithTracingRuntime(tracingRuntime),
)
```

访问日志使用请求 URL path，并在 trace ID 存在时追加 `trace_id=...`。日志前缀固定为 `[Aurora]`，
不记录 query、Authorization、Cookie、请求体、User-Agent、完整 header 和错误原文。

### Tracing

通过 Option 注入：

```go
func WithTracingRuntime(rt TracingRuntime) Option
```

`TracingRuntime` 由应用负责实现，封装 TracerProvider、Propagator 以及上下文提取和注入。Aurora
只借用该运行时，不负责关闭应用拥有的 tracing provider。

Tracing middleware：

1. 使用 propagator 从请求头提取 W3C Trace Context；
2. 使用注入的 provider 创建 HTTP server span；
3. 将带 span 的 context 写回 `*http.Request`；
4. 使用 `METHOD + route template` 作为 span name，避免动态路径高基数；
5. 将 trace ID 保存在请求 Context，供 `c.TraceID()` 和访问日志使用；
6. 请求结束后记录路由、状态码和响应大小，5xx 标记为错误；
7. 未注入 tracing runtime 时跳过 tracing，不创建或关闭应用拥有的 Provider。

## 11. Option 与外部依赖

```go
type Option func(*Engine)

func WithLogger(logger *logger.Logger) Option
func WithTracingRuntime(rt TracingRuntime) Option
func WithMetricsProvider(provider metric.MeterProvider) Option
```

**实际实现：**

- 使用 `core/logger.Logger` 作为日志接口，访问日志通过 `InfoText` 保持纯文本格式
- 使用 `TracingRuntime` 注入 OpenTelemetry Provider、Propagator 和上下文传播能力
- 使用 `metric.MeterProvider` 注入 HTTP 指标采集能力
- Option 直接修改 `*Engine` 结构体字段
- 中间件通过 Engine 的字段获取注入的依赖

**使用示例：**

```go
logger, err := loggerpkg.New(&loggerpkg.Config{Output: loggerpkg.OutputStdout})
if err != nil {
    return err
}
tracingRuntime := newTracingRuntime(provider, propagator)

engine, err := aurora.New(cfg,
    aurora.WithLogger(logger),
    aurora.WithTracingRuntime(tracingRuntime),
)
```

## 12. 生命周期

```go
func New(cfg Config, opts ...Option) (*Engine, error)
func (e *Engine) Start() error
func (e *Engine) Serve(net.Listener) error
func (e *Engine) Shutdown(context.Context) error
func (e *Engine) Run(context.Context) error
```

当应用希望由 Aurora 统一组合 core 资源时使用：

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

**生命周期流程：**

```text
New
  -> 注册全局 middleware、路由
  -> Start / Serve / Run
  -> Listening
  -> Shutdown(ctx)
```

`NewLifecycle` 在此流程前增加配置加载和资源创建；如果配置文件包含 `auth` 段，生命周期会
按 `auth.<name>` 配置使用同一 Redis 客户端创建多个 `auth.Manager`，业务代码通过
`lifecycle.Auth("admin")` 或 `lifecycle.Auth("user")` 获取指定实例。未找到实例返回包装了
`ErrAuthInstanceNotFound` 的错误，不设置默认实例或回退。实例表只读，不支持运行时增删或配置热重载。
`AuthNames()` 返回按名称排序的独立标识切片，其长度即实例数量；无实例或 nil Lifecycle 返回 nil。
获取标识列表可并发调用，修改返回切片不会影响注册表。

实例名遵循 `[a-z][a-z0-9_-]{0,63}`。配置键经 Viper 归一化为小写，运行时按小写名称精确查找。
不配置 `auth` 或配置空 map 不创建实例；空实例、未知字段和错误类型拒绝启动。
实例配置独立严格解码，解码错误不包含配置值，避免暴露签名密钥。
所有实例按名称排序创建，任一实例失败时整体初始化失败并回滚资源。
默认 Redis 前缀按名称生成为 `aurora:auth:{<name>}`，拒绝实例间重复的有效前缀。
各实例独立配置签名密钥、issuer、audience、TTL 和会话策略；实例名不自动写入 JWT 或 Principal.Type。
不同认证域应使用独立密钥及不同的 issuer、audience。原单实例配置和字段不再支持。
`Lifecycle` 的 Run、Start、Serve 返回后
会自动执行一次资源关闭。显式调用 Shutdown 或 Close 也会执行同一套幂等关闭流程。

**状态要求：**

- `New` 只创建 Engine，不监听端口，不启动服务
- `Start` 内部调用 `net.Listen(cfg.Addr)`，成功后阻塞运行 `http.Server.Serve`
- `Serve` 接管调用方传入的 listener，Server 关闭时负责关闭它
- `Run` 是便利 API：启动服务并在 context 取消时自动调用 Shutdown
- 同一个 Engine 只能成功启动一次；重复启动返回错误
- `Shutdown` 调用 `http.Server.Shutdown`，停止接收新连接、关闭空闲连接并等待活动请求
- Shutdown 超时由传入的 context deadline 控制
- 重复或并发 Shutdown 必须安全

**使用示例：**

```go
// 方式 1: 使用 Run（推荐）
ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
defer cancel()

if err := engine.Run(ctx); err != nil {
    log.Fatal(err)
}

// 方式 2: 手动控制
go func() {
    if err := engine.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
        log.Fatal(err)
    }
}()

// 等待信号
<-sigChan

// 优雅关闭
shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()
engine.Shutdown(shutdownCtx)
```

**资源管理：**

- Engine 只拥有并关闭：Gin Engine、http.Server、net.Listener
- Logger、TracerProvider、数据库、Redis、MQ 由应用组合根拥有
- Aurora 只借用它们，不在 Shutdown 中关闭它们

## 13. 完整示例

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "time"

    "github.com/surge-go/aurora"
    loggerpkg "github.com/surge-go/aurora/core/logger"
    "go.opentelemetry.io/otel"
)

// 定义请求和响应类型
type CreateUserReq struct {
    Name  string `json:"name" binding:"required"`
    Email string `json:"email" binding:"required,email"`
}

type User struct {
    ID        int64     `json:"id"`
    Name      string    `json:"name"`
    Email     string    `json:"email"`
    CreatedAt time.Time `json:"created_at"`
}

func main() {
    // 创建日志器
    logger, err := loggerpkg.New(&loggerpkg.Config{
        Level:  loggerpkg.LevelInfo,
        Output: loggerpkg.OutputStdout,
    })
    if err != nil {
        log.Fatal(err)
    }

    // 配置 Aurora
    cfg := aurora.DefaultConfig()
    cfg.Addr = ":8080"
    cfg.Mode = "release"
    cfg.Middleware.Tracing = true

    // 创建 Engine（注入 logger 和 tracer）
    engine, err := aurora.New(cfg,
        aurora.WithLogger(logger),
        aurora.WithTracingRuntime(tracingRuntime),
    )
    if err != nil {
        log.Fatal(err)
    }

    // 注册路由
    engine.GET("/health", aurora.BindRE(healthCheck))

    api := engine.Group("/api/v1")
    {
        users := api.Group("/users")
        {
            users.POST("", aurora.Bind(createUser))
            users.GET("/:id", aurora.BindR(getUser))
        }
    }

    // 启动服务（监听中断信号）
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
    defer cancel()

    logger.Info("Starting Aurora server", "addr", cfg.Addr)
    if err := engine.Run(ctx); err != nil {
        logger.Error("Server stopped", "error", err)
    }
}

// 业务 Handler 实现
func healthCheck(c *aurora.Context) error {
    return nil
}

func createUser(c *aurora.Context, req *CreateUserReq) (*User, error) {
    // 业务逻辑：创建用户
    user := &User{
        ID:        time.Now().Unix(),
        Name:      req.Name,
        Email:     req.Email,
        CreatedAt: time.Now(),
    }

    // 可以通过 c.Context() 传递给数据库
    // db.CreateUser(c.Context(), user)

    return user, nil
}

func getUser(c *aurora.Context) (*User, error) {
    id := c.Param("id")

    // 业务逻辑：查询用户
    // user, err := db.GetUser(c.Context(), id)

    return &User{
        ID:        1,
        Name:      "Alice",
        Email:     "alice@example.com",
        CreatedAt: time.Now(),
    }, nil
}
```

## 14. 实现状态

### ✅ 已完成

- **aurora.go** - Engine 核心和生命周期管理
- **config.go** - 配置模型、验证和默认值
- **context.go** - Context 封装（67个方法）
- **handler.go** - 4种泛型绑定适配器
- **router.go** - RouterGroup 和路由注册
- **response.go** - 统一响应格式
- **errors.go** - Error 类型和预定义错误
- **middleware.go** - 默认中间件装配
- **middleware.go** - 默认中间件装配和 HTTP 指标中间件
- **option.go** - WithLogger、WithTracingRuntime 和 WithMetricsProvider
- **lifecycle.go** - 配置文件 bootstrap、core 资源初始化和统一生命周期关闭

### 🔄 待优化

- 参数绑定：继续补充不同 Content-Type 和 DELETE 请求的边界测试

### 📋 未来扩展

- CORS 中间件
- 限流中间件
- 认证授权中间件
- OpenAPI/Swagger 支持
- 静态文件服务策略和缓存控制
- WebSocket 支持
## 15. 应用示例

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
func createUser(c *aurora.Context, req *CreateUserRequest) (*User, error) {
	return userService.Create(c.Context(), req)
}
```

## 16. 实施阶段

### 阶段一：HTTP 和路由核心

- 引入 Gin；
- 实现 Config、默认值和校验；
- 实现 Engine、RouterGroup 和内部路由适配；
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
- 注入 MeterProvider 并实现 HTTP metrics middleware，不在根包创建或关闭 MeterProvider；
- 覆盖 trace parent、指标路由模板、5xx、低基数属性和关闭顺序。

OpenAPI、路由元信息、Swagger UI 和自动文档生成不属于以上阶段，后续单独设计。

## 17. 测试与验收

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
- HTTP Metrics 的 Provider 注入、成功/错误状态、panic、未匹配路由和低基数属性。

验证命令：

```bash
gofmt -w <modified-go-files>
go test ./...
go build ./...
go test -race ./...
```

涉及新增依赖时同时检查 `go.mod`、`go.sum` 和最小 Go 版本兼容性。
