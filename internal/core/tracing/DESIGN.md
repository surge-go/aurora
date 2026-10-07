# core/tracing 链路追踪初始化设计

## 1. 设计范围

`internal/core/tracing` 是应用启动阶段使用的 OpenTelemetry tracing 初始化包，负责把可序列化配置转换为运行时的 `TracerProvider`、采样器、资源属性、span exporter、传播器和关闭句柄。

本包的目标是提供一个明确的观测运行时边界，让 HTTP、MQ、数据库、Redis 以及业务代码共享同一套 Provider 和上下文传播规则。当前数据库和 Redis 包已经支持注入 `trace.TracerProvider`；它们只注册自身 instrumentation，不创建、替换或关闭应用级 Provider。

本设计定义初始化契约；当前实现覆盖 Provider、OTLP exporter、采样、传播和生命周期，HTTP middleware、MQ middleware 及具体业务 span 仍由调用方负责。

## 2. 目标与非目标

### 目标

- 在进程启动阶段校验 tracing 配置并创建可复用的 `trace.TracerProvider`。
- 使用统一的资源属性标识服务、版本和部署环境。
- 支持 OTLP exporter，传输协议和 endpoint 由配置决定。
- 使用明确的 parent-based 采样策略，默认保留上游采样决定。
- 使用 W3C Trace Context + W3C Baggage 传播，统一跨 HTTP、MQ 和 RPC 边界的上下文格式。
- 为数据库、Redis 和其他 instrumentation 提供显式 Provider 注入入口。
- 让 exporter、span processor 和 Provider 的所有权与关闭顺序清晰、幂等、可测试。

### 非目标

- 不读取配置文件、不解析环境变量、不管理进程信号、不创建全局配置单例。
- 不在 `init` 中修改 OpenTelemetry 全局 Provider 或全局 propagator。
- 不实现 HTTP server/client middleware、MQ consumer middleware 或业务领域 span。
- 不在本包中创建 metrics provider、logger 或 trace backend；metrics 后续由独立包设计。
- 不默认支持 Jaeger、Zipkin 等专有协议。需要这些后端时由 OTLP collector 负责适配。
- 不把密码、token、完整 HTTP body、Redis key/value、SQL 参数或消息载荷写入 span 属性。

## 3. 包边界和依赖方向

```text
internal/core/tracing/
  DESIGN.md       # 本文：初始化契约和生命周期
  config.go       # 可序列化配置、枚举、默认值和校验
  provider.go     # Provider、exporter、processor 和关闭生命周期
  propagator.go   # W3C propagator 和上下文辅助函数
  *_test.go       # 配置、采样、资源、关闭和失败回滚测试
```

依赖方向：

```text
application bootstrap
        |
        v
core/tracing ---> OpenTelemetry SDK/exporter
        |
        +----> core/database (注入 trace.TracerProvider)
        +----> core/redis    (注入 trace.TracerProvider)
        +----> core/mq/http  (后续注入或使用 propagator)
```

`core/tracing` 可以依赖 OpenTelemetry SDK 和 OTLP exporter，但不反向依赖数据库、Redis、MQ、日志或业务包。数据库和 Redis 仍然只接收 `trace.TracerProvider` 接口；它们的 `Close` 不得调用 tracing Provider 的 `Shutdown`。

## 4. 配置模型草案

配置结构面向 YAML/JSON/mapstructure，运行时 Provider、exporter 和函数回调必须排除在序列化之外。

```go
type Config struct {
    Enabled           bool              `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
    ServiceName       string            `json:"service_name" yaml:"service_name" mapstructure:"service_name"`
    ServiceVersion    string            `json:"service_version" yaml:"service_version" mapstructure:"service_version"`
    Environment       string            `json:"environment" yaml:"environment" mapstructure:"environment"`
    Endpoint          string            `json:"endpoint" yaml:"endpoint" mapstructure:"endpoint"`
    Protocol          Protocol          `json:"protocol" yaml:"protocol" mapstructure:"protocol"`
    Insecure          bool              `json:"insecure" yaml:"insecure" mapstructure:"insecure"`
    Headers           map[string]string `json:"headers" yaml:"headers" mapstructure:"headers"`
    SampleRatio       float64           `json:"sample_ratio" yaml:"sample_ratio" mapstructure:"sample_ratio"`
    Batch             BatchConfig       `json:"batch" yaml:"batch" mapstructure:"batch"`
    Resource          map[string]string `json:"resource" yaml:"resource" mapstructure:"resource"`
}

type Protocol string

const (
    ProtocolGRPC      Protocol = "grpc"
    ProtocolHTTPProto Protocol = "http/protobuf"
)

type BatchConfig struct {
    MaxQueueSize       int           `json:"max_queue_size" yaml:"max_queue_size" mapstructure:"max_queue_size"`
    MaxExportBatchSize int           `json:"max_export_batch_size" yaml:"max_export_batch_size" mapstructure:"max_export_batch_size"`
    ScheduleDelay      time.Duration `json:"schedule_delay" yaml:"schedule_delay" mapstructure:"schedule_delay"`
    ExportTimeout      time.Duration `json:"export_timeout" yaml:"export_timeout" mapstructure:"export_timeout"`
}
```

### 配置语义

- `Enabled=false` 时不创建网络 exporter，不产生 SDK 导出 span；返回的运行时仍提供可安全调用的 Provider 和 W3C propagator。这样调用方无需在每个 instrumentation 包中增加 nil 分支。
- `ServiceName` 必填。没有明确服务名时初始化失败，避免所有服务以 `unknown_service` 汇聚到同一组数据。
- `ServiceVersion` 和 `Environment` 可为空；非空时分别映射到 `service.version` 和 `deployment.environment.name`。
- `Endpoint` 在启用 tracing 时必填，并按 OpenTelemetry OTLP exporter 的 endpoint 语义校验。不得把 endpoint 中的 userinfo、token 或 headers 写入错误日志。
- `Protocol` 默认 `grpc`；仅允许 `grpc` 和 `http/protobuf`。HTTP/protobuf 的 endpoint path 由 exporter 约定，不允许在本包中拼接另一套自定义路径。
- `Insecure=true` 只允许明文 OTLP 连接，必须显式配置；生产配置默认使用 TLS。TLS 证书、代理和自定义 dialer 后续按 exporter 需要扩展，不在首期配置中放入不可序列化对象。
- `Headers` 用于 collector 认证或租户路由。键名可以记录在配置诊断中，但 header 值视为敏感信息，错误和日志中必须隐藏。
- `SampleRatio` 范围为 `[0,1]`，`DefaultConfig` 将其设为 `1.0` 以保持开发环境不丢 span；直接构造 `Config` 时，零值 `0` 表示完全不采样，生产环境应显式设置采样比例。采样器使用 `ParentBased(TraceIDRatioBased(SampleRatio))`。
- `Batch` 的零值使用 SDK 的安全默认值；显式的负数、批次大于队列、非正 timeout 等无效值应在 `Validate` 阶段拒绝。
- `Resource` 只允许稳定、低基数的字符串属性。实现应拒绝空键，并限制保留键 `service.name`、`service.version` 和 `deployment.environment.name` 被任意覆盖；这些键由顶层字段决定。

`Config.Validate` 只进行本地确定性校验，不创建 exporter、不建立网络连接、不探测 collector。

## 5. 运行时 API 草案

```go
type Runtime struct {
    // 不暴露内部 exporter 和 processor 的具体类型。
}

func New(ctx context.Context, cfg *Config, opts ...Option) (*Runtime, error)

func (r *Runtime) TracerProvider() trace.TracerProvider
func (r *Runtime) Propagator() propagation.TextMapPropagator

// InstallGlobal 显式设置 OpenTelemetry 全局 Provider 和 propagator。
// 该操作应只在应用 bootstrap 阶段调用一次。
func (r *Runtime) InstallGlobal() error

// Shutdown 刷新并关闭 exporter 和 Provider；可重复、并发安全。
func (r *Runtime) Shutdown(ctx context.Context) error
```

### API 约定

- `New` 不自动安装全局 Provider。应用可以把 `Runtime.TracerProvider()` 注入数据库和 Redis 的 Monitoring 配置，也可以在 bootstrap 阶段显式调用 `InstallGlobal`。
- `ctx` 只用于 exporter 初始化所需的短期操作；构造函数不得启动无法通过 `Shutdown` 停止的后台 goroutine。
- `TracerProvider()` 返回 `trace.TracerProvider`，避免调用方依赖 SDK 具体类型。只有 tracing 包拥有 Provider 的关闭权。
- `Propagator()` 返回 `propagation.TextMapPropagator`，默认组合 `TraceContext` 和 `Baggage`，顺序固定且可测试。
- `InstallGlobal` 若被重复调用，应返回稳定错误或保持幂等；设计首选返回错误并要求应用只在 bootstrap 调用一次。库包、测试和热更新回调不得调用它。
- `Shutdown` 先停止接受新 span，再调用 SDK Provider 的 `Shutdown`。它必须尊重 `ctx` deadline，返回原始错误链；重复调用返回同一关闭结果，不能二次关闭 exporter。
- `Runtime` 不负责关闭由调用方注入的外部 Provider。首期 `New` 自己创建的 Provider 才由 `Runtime` 拥有；未来若增加 `WithTracerProvider`，必须明确为借用模式且 `Shutdown` 不得关闭外部 Provider。

## 6. 初始化顺序与资源所有权

启用 tracing 时，初始化顺序固定为：

1. 复制并校验 `Config`，避免在初始化期间读取调用方并发修改的 map。
2. 构造 `resource.Resource`：合并 SDK 默认资源、服务字段和额外稳定属性；顶层服务字段覆盖默认值，保留键不能由 `Resource` 覆盖。
3. 按 `Protocol` 创建 OTLP exporter，应用 endpoint、TLS/insecure 和 headers；exporter 创建失败立即返回带阶段信息的 `%w` 错误。
4. 创建 `BatchSpanProcessor`，使用 `Batch` 配置；不在请求 goroutine 中同步导出。
5. 创建 `sdktrace.TracerProvider`，设置 resource、processor 和 parent-based sampler。
6. 创建 W3C propagator。
7. 组装 `Runtime` 并返回。此时 Provider 的所有权转移给 `Runtime`。

任一步骤失败都必须按相反顺序释放已创建资源：关闭 processor/exporter，不能泄漏网络连接或后台 goroutine。失败错误包含 `create resource`、`create OTLP exporter`、`create span processor` 等阶段文本并保留底层错误。

禁用 tracing 时不创建 exporter、processor 或网络连接；返回 no-op Provider 和 W3C propagator。`Shutdown` 仍然安全可调用。

## 7. 全局 Provider 与应用装配

推荐的应用 bootstrap 顺序：

```go
runtime, err := tracing.New(ctx, &tracing.Config{
    Enabled:        true,
    ServiceName:    "user-api",
    ServiceVersion: version,
    Environment:    "production",
    Endpoint:       "otel-collector:4317",
    Protocol:       tracing.ProtocolGRPC,
    SampleRatio:    0.1,
})
if err != nil {
    return err
}
defer runtime.Shutdown(shutdownCtx)

if err := runtime.InstallGlobal(); err != nil {
    return err
}

dbCfg.Monitoring = &database.MonitoringConfig{
    TracingEnabled: true,
    TracerProvider: runtime.TracerProvider(),
}
redisCfg.Monitoring = &redis.MonitoringConfig{
    TracingEnabled: true,
    TracerProvider: runtime.TracerProvider(),
}
```

应用应在创建数据库、Redis、MQ 和 HTTP server/client instrumentation 之前完成 Provider 装配。若应用不安装全局 Provider，所有需要 tracing 的基础设施都必须显式注入同一个 Provider；不能一部分使用全局 Provider、另一部分使用独立 Provider。

生产关闭顺序建议为：停止接收新请求/消息 -> 关闭业务 client -> 关闭数据库和 Redis instrumentation 所属 client -> 调用 `Runtime.Shutdown` 刷新剩余 span。Provider 关闭后不得继续创建新 span。

## 8. Span 与敏感数据约定

- `service.name`、`service.version`、`deployment.environment.name` 是资源属性，不重复作为每个 span 的自定义属性。
- instrumentation 应优先使用 OpenTelemetry 语义约定；未知或高基数字段不得直接写入属性。
- SQL、Redis 命令、HTTP header、MQ body、认证信息和用户输入默认不记录；如某个 instrumentation 支持显式采集，必须在其自身设计中再次声明脱敏和开关语义。
- 错误 span 可以记录错误类型和稳定错误码；错误消息可能含敏感内容时，不应直接作为 span attribute。
- 不在 tracing 包中添加日志 handler；初始化错误只返回调用方，由应用决定日志级别和输出。

## 9. 测试与验收矩阵

- `Config.Validate` 覆盖空配置、缺少服务名、非法协议、启用时缺少 endpoint、采样比例越界、batch 边界、保留资源键冲突和敏感 header 不泄漏。
- 禁用 tracing 时验证不创建网络 exporter，返回的 Provider/propagator 可用且 `Shutdown` 幂等。
- 启用 tracing 时使用本地 test exporter 或可替换 exporter 验证 resource、sampler、processor 参数和 Provider 注入。
- 验证 `InstallGlobal` 的重复调用语义，并确保测试不会污染其他测试的全局 Provider；必要时在测试进程中隔离调用。
- exporter 创建失败、processor 创建失败和 Provider 关闭失败必须验证资源回滚及错误链。
- 通过 `Shutdown` 验证 batch span 被 flush、重复/并发关闭不会 panic 或重复关闭 exporter，deadline 到期不会无限阻塞。
- 使用 `go test -race` 覆盖并发 `Shutdown`、span 创建和全局安装保护。

## 10. 依赖和实现决策

- OpenTelemetry API：`go.opentelemetry.io/otel`、`trace`、`propagation`。
- OpenTelemetry SDK：`go.opentelemetry.io/otel/sdk/trace`、`resource`。
- OTLP exporter：按协议引入 `otlptracegrpc` 和/或 `otlptracehttp`，版本必须与仓库 OpenTelemetry 主版本一致。
- 首期只支持 OTLP/gRPC 和 OTLP/HTTP protobuf；不在配置层同时暴露两套 exporter 专属 TLS 字段。
- 默认不自动探测主机、容器、云厂商资源，避免启动延迟、网络访问和不稳定属性；后续如需资源探测，单独增加可选且有超时的配置。

## 11. 后续待定

- 是否把 exporter endpoint、认证 headers 和采样配置接入 `configs/core.yaml`，由应用配置装配设计决定；tracing 包本身只提供可反序列化结构。
- 是否支持 tail sampling、动态采样和多 exporter fan-out，留待实际部署规模和 collector 拓扑明确后再设计。
- 是否提供测试用 in-memory exporter、HTTP propagation middleware 和 MQ carrier adapter，按具体调用方需求分别设计，不提前扩大本包 API。
