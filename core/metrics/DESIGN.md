# core/metrics 指标采集初始化设计

## 1. 设计范围

`core/metrics` 是应用启动阶段使用的 OpenTelemetry metrics 初始化包，负责把可序列化配置转换为运行时的 `metric.MeterProvider`、资源属性、metric reader、OTLP exporter 和关闭句柄。

本包提供应用级指标运行时边界。数据库、Redis、HTTP、MQ 和业务 instrumentation 只接收注入的 `metric.MeterProvider`，负责注册自己的 instruments 或 callbacks，不创建、替换或关闭应用级 Provider。

本阶段先设计 OTLP metrics 的初始化契约，同时保留 Prometheus scrape 集成的边界。Prometheus HTTP server、路由和应用监听器由应用层拥有，不由本包隐式启动。

## 2. 目标与非目标

### 目标

- 在应用 bootstrap 阶段校验 metrics 配置并创建可复用的 `metric.MeterProvider`。
- 使用统一的 `service.name`、`service.version`、`deployment.environment.name` 和额外稳定资源属性。
- 支持 OTLP/gRPC 与 OTLP/HTTP protobuf exporter，协议、endpoint、TLS 和 headers 由配置决定。
- 使用 `PeriodicReader` 异步导出，避免在请求或数据库操作 goroutine 中同步访问 collector。
- 为数据库、Redis 和其他 instrumentation 提供显式 MeterProvider 注入入口。
- 明确 Reader、Exporter、Provider 的所有权、失败回滚、超时和关闭顺序。
- 允许通过可替换的 reader factory 进行无外部 collector 的单元测试。

### 非目标

- 不读取配置文件、不解析环境变量、不管理进程信号、不创建全局配置单例。
- 不在 `init` 中修改 OpenTelemetry 全局 MeterProvider。
- 不在本包中注册数据库、Redis、HTTP、MQ 或业务指标；这些指标由各自 instrumentation 包负责。
- 不自动启动 HTTP server、Prometheus scrape endpoint 或独立的 metrics 管理端口。
- 不在首期实现 tail aggregation、动态过滤规则、远程配置、指标转发队列或多租户 exporter fan-out。
- 不把密码、token、完整 SQL、Redis key/value、HTTP body、消息载荷或用户输入作为 metric attribute。

## 3. 包边界和依赖方向

```text
core/metrics/
  DESIGN.md       # 本文：初始化契约、指标边界和生命周期
  config.go       # 可序列化配置、协议、默认值和校验
  provider.go     # MeterProvider、reader、exporter 和关闭生命周期
  *_test.go       # 配置、资源、reader、全局安装和关闭测试
```

依赖方向：

```text
application bootstrap
        |
        v
core/metrics ---> OpenTelemetry SDK/exporter
        |
        +----> core/database (注入 metric.MeterProvider)
        +----> core/redis    (注入 metric.MeterProvider)
        +----> core/mq/http  (后续注入 metric.MeterProvider)
```

`core/metrics` 可以依赖 OpenTelemetry API、SDK 和 OTLP metric exporter，但不反向依赖 database、Redis、MQ、logger、tracing 或业务包。metrics 与 tracing 共用资源属性约定，但首期不为了复用而引入跨包运行时依赖；应用可以从同一份配置分别构造两个 runtime。

## 4. 配置模型

配置结构面向 YAML/JSON/mapstructure，运行时 Provider、reader、exporter 和函数回调必须排除在序列化之外。

```go
type Config struct {
    Enabled        bool              `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
    ServiceName    string            `json:"service_name" yaml:"service_name" mapstructure:"service_name"`
    ServiceVersion string            `json:"service_version" yaml:"service_version" mapstructure:"service_version"`
    Environment    string            `json:"environment" yaml:"environment" mapstructure:"environment"`
    Endpoint       string            `json:"endpoint" yaml:"endpoint" mapstructure:"endpoint"`
    Protocol       Protocol          `json:"protocol" yaml:"protocol" mapstructure:"protocol"`
    Insecure       bool              `json:"insecure" yaml:"insecure" mapstructure:"insecure"`
    Headers        map[string]string `json:"headers" yaml:"headers" mapstructure:"headers"`
    ExportInterval time.Duration     `json:"export_interval" yaml:"export_interval" mapstructure:"export_interval"`
    ExportTimeout  time.Duration     `json:"export_timeout" yaml:"export_timeout" mapstructure:"export_timeout"`
    Resource       map[string]string `json:"resource" yaml:"resource" mapstructure:"resource"`
}

type Protocol string

const (
    ProtocolGRPC      Protocol = "grpc"
    ProtocolHTTPProto Protocol = "http/protobuf"
)
```

`Reader`、`MeterProvider`、`Exporter` 和 `ReaderFactory` 不放入 `Config`，避免把不可序列化对象写入配置文件。
自定义 `ReaderFactory` 返回的 reader 必须自行持有其 exporter；Runtime 只持有并关闭 reader，不能再单独关闭同一个 exporter。

### 配置语义

- `Enabled=false` 时不创建 exporter、reader、网络连接或后台导出 goroutine；返回 no-op MeterProvider，`Shutdown` 仍可安全调用。
- `ServiceName` 必填。没有明确服务名时初始化失败，避免所有服务的指标聚合到同一个隐式服务名。
- `ServiceVersion` 和 `Environment` 可为空；非空时分别映射到 `service.version` 和 `deployment.environment.name`。
- `Endpoint` 在启用 OTLP 时必填。gRPC 使用 `host:port` 或 `http(s)://host:port`；HTTP/protobuf 使用 `http(s)://host:port`，可带 exporter 需要的 path。不得包含 userinfo、query、fragment 或认证 token。
- `Protocol` 默认 `grpc`；仅允许 `grpc` 和 `http/protobuf`。HTTP endpoint path 交由 exporter 解释，本包不自行拼接路径。
- `Insecure=true` 只允许明文连接，必须显式配置；生产环境默认使用 TLS。`http://` endpoint 必须同时设置 `Insecure=true`。
- `Headers` 用于 collector 认证或租户路由。header 值视为敏感信息，不得进入错误、日志或诊断输出。
- `ExportInterval=0` 和 `ExportTimeout=0` 使用 OpenTelemetry SDK/PeriodicReader 默认值；负数必须在 `Validate` 阶段拒绝。显式 timeout 必须大于 0。
- `Resource` 只允许稳定、低基数的字符串属性。实现应拒绝空键，并禁止覆盖 `service.name`、`service.version` 和 `deployment.environment.name`。

`Config.Validate` 只进行本地确定性校验，不创建 exporter、不建立网络连接、不探测 collector。

## 5. 运行时 API 草案

```go
type Runtime struct {
    // 不暴露内部 reader、exporter 和 Provider 的具体实现类型。
}

func New(ctx context.Context, cfg *Config, opts ...Option) (*Runtime, error)

func (r *Runtime) MeterProvider() metric.MeterProvider

// InstallGlobal 显式设置 OpenTelemetry 全局 MeterProvider。
func (r *Runtime) InstallGlobal() error

// Shutdown 刷新并关闭本 Runtime 创建的 Provider 及其 reader/exporter。
func (r *Runtime) Shutdown(ctx context.Context) error
```

### 测试和扩展选项

首期保留一个可替换的 reader factory，便于使用 `sdkmetric.NewManualReader` 做本地测试，也允许后续接入自定义 reader：

```go
type ReaderFactory func(context.Context, Config) (sdkmetric.Reader, error)
type Option func(*options)
```

factory 返回的 reader 所有权转移给 `Runtime`；`Runtime.Shutdown` 通过 `MeterProvider.Shutdown` 负责关闭它及其所拥有的 exporter。factory 不得把同一个 reader 或 exporter 的关闭责任保留给调用方。Option 只用于运行时装配和测试，不参与配置文件反序列化。

### API 约定

- `New` 不自动安装全局 MeterProvider。应用可以把 `Runtime.MeterProvider()` 注入 database、Redis 和其他 instrumentation，也可以在 bootstrap 阶段显式调用 `InstallGlobal`。
- `ctx` 只用于 exporter/reader 初始化的短期操作；构造函数不得启动无法通过 `Shutdown` 停止的后台 goroutine。
- `MeterProvider()` 返回 `metric.MeterProvider` 接口，调用方不依赖 SDK 具体类型。只有 metrics 包拥有本 Runtime 创建的 Provider、reader 和 exporter 的关闭权。
- 同一个 Runtime 重复 `InstallGlobal` 应幂等；不同 Runtime 或已有外部全局 Provider 时返回稳定的 `ErrGlobalProviderInstalled`，不得静默覆盖外部安装。全局安装只允许应用 bootstrap 调用，库包和热更新回调不得调用。
- `Shutdown` 不恢复或替换进程原先的全局 MeterProvider；应用若需要切换 Provider，必须在进程级 bootstrap/重启流程中显式协调，不能依赖 Runtime 的关闭副作用。
- `Shutdown` 必须停止 reader 的周期导出，按 SDK 语义 flush 剩余指标，并尊重 context deadline。Runtime 只调用 Provider 的关闭入口一次，由 SDK 负责 reader/exporter 的级联关闭；不得再次直接关闭 exporter。重复或并发调用只执行一次，所有调用返回同一个关闭结果。
- `Runtime` 不关闭调用方外部创建并注入的 Provider。未来若支持外部 Provider 借用模式，必须在 API 中显式区分 ownership。

## 6. Provider、Reader 与 Exporter

### OTLP reader

启用 metrics 且没有自定义 reader factory 时，初始化流程按协议创建 OTLP metric exporter，再使用 `sdkmetric.NewPeriodicReader` 创建 reader：

```text
OTLP exporter -> PeriodicReader -> MeterProvider
```

`ExportInterval` 控制周期导出间隔，`ExportTimeout` 同时限制 exporter 和 reader 的单次导出操作。具体 option 名称和 exporter 版本必须与仓库使用的 OpenTelemetry 主版本保持一致。

首期不在配置层暴露 exporter 专属 TLS client、proxy、dialer 或 compression 对象；需要这些能力时扩展为不可序列化的显式 Option，并明确所有权。

### Prometheus 边界

Prometheus pull 模型与 OTLP push 模型的生命周期不同。首期不在 `New` 中自动启动 HTTP 服务，也不把监听地址写入 metrics runtime。后续如果需要 Prometheus，应单独设计：

- `PrometheusReader` 是否与 OTLP reader 并存；
- handler 的创建和关闭责任；
- metric name、namespace、scope info 和 temporality 映射；
- 应用 HTTP server 重启或多路由注册时的 ownership。

应用若自行创建 Prometheus reader，应通过 reader factory 注入，并由约定的 owner 负责 handler 和 reader 生命周期。

### 资源属性

初始化时构造 `resource.Resource`，合并 SDK 默认资源、服务字段和额外稳定属性。顶层服务字段覆盖 SDK 默认值；`Resource` 不得覆盖保留键。资源属性必须低基数，不能放 request ID、用户 ID、订单 ID、URL、SQL、Redis key 或错误原文。

## 7. 指标命名、属性与聚合

- 基础设施 instrumentation 优先遵循 OpenTelemetry semantic conventions；已有 database/Redis 指标的兼容名称在迁移前保持稳定。
- Counter 只记录单调累计事件；Gauge/ObservableGauge 用于连接数、队列深度等当前状态；Histogram 用于延迟、大小等分布。
- 属性键必须有限、稳定、可枚举。禁止把任意 header、路径、异常消息、用户输入或消息内容直接作为 label。
- 资源属性与 metric attributes 分开管理；服务标识放在 Resource，不在每个 metric 上重复添加。
- 不在 metrics 包内自动创建业务指标；业务指标的名称、单位、描述和属性由业务模块负责，并需单独评审基数和数据敏感性。
- 采集端不负责去重、降采样或修正错误的 instrumentation。View、过滤和聚合策略若要暴露配置，应先定义稳定的语义和回滚行为。

## 8. 初始化顺序与所有权

启用 OTLP metrics 时，初始化顺序固定为：

1. 复制并校验 `Config`，避免初始化期间读取调用方并发修改的 map。
2. 应用默认值，构造 resource；保留键不能被额外 Resource 覆盖。
3. 使用自定义 reader factory，或按 Protocol 创建 OTLP exporter 和 PeriodicReader。
4. 创建 `sdkmetric.MeterProvider`，设置 resource 和 reader。
5. 组装 `Runtime`，把 Provider 及其 reader/exporter 的所有权转移给 Runtime。

任一步骤失败都必须释放已经创建的 reader；reader 负责关闭其已绑定的 exporter。若 exporter 尚未绑定到 reader，则由创建阶段立即关闭。返回错误必须带阶段信息并保留底层错误链；失败路径不得遗留 exporter 连接、周期 ticker、goroutine 或已注册的 callback。

关闭顺序建议为：停止接受新 instrumentation 注册 -> 调用 `MeterProvider.Shutdown(ctx)` flush 并关闭 reader（由 reader 关闭其 exporter）。Provider 关闭后不得继续创建需要导出的新指标；调用方负责先关闭 database/Redis client，注销它们的 callbacks，再关闭 metrics Runtime。

禁用 metrics 时不创建 exporter、reader 或网络连接；返回 no-op Provider，且 `Shutdown` 幂等。

## 9. 全局 MeterProvider 与应用装配

推荐的 bootstrap 顺序：

```go
runtime, err := metrics.New(ctx, &metrics.Config{
    Enabled:        true,
    ServiceName:    "user-api",
    ServiceVersion: version,
    Environment:    "production",
    Endpoint:       "otel-collector:4317",
    Protocol:       metrics.ProtocolGRPC,
    ExportInterval: 15 * time.Second,
})
if err != nil {
    return err
}
defer runtime.Shutdown(shutdownCtx)

if err := runtime.InstallGlobal(); err != nil {
    return err
}

dbCfg.Monitoring = &database.MonitoringConfig{
    MetricsEnabled: true,
    MeterProvider:  runtime.MeterProvider(),
}
redisCfg.Monitoring = &redis.MonitoringConfig{
    MetricsEnabled: true,
    MeterProvider:  runtime.MeterProvider(),
}
```

应用应在创建 database、Redis、MQ 和 HTTP instrumentation 之前完成 Provider 装配。应用可以不安装全局 Provider，但所有 instrumentation 必须显式注入同一个 Provider，不能一部分使用全局 Provider、另一部分使用独立 Provider。

metrics runtime 不负责关闭 database、Redis 或其他 client；它们的 callback/registration 必须由各自 owner 在 Provider 关闭前注销。

## 10. 安全与运维约定

- endpoint、headers 和 TLS 配置属于敏感运维配置；错误信息不得包含 header 值、密码、token 或证书内容。
- 生产环境默认 TLS；`Insecure` 只能用于明确的本地或受控网络场景。
- OTLP exporter 必须有有限的导出 timeout；不得因为 collector 不可用而无限阻塞应用关闭。
- 周期导出失败应通过 OpenTelemetry error handler 或应用提供的错误通道暴露，但不得在每个周期无限重复打印相同敏感信息。
- Reader 队列、批量和重试策略使用 SDK 默认值时必须记录其版本语义；后续覆盖这些参数需评估内存上限、collector backpressure 和关闭耗时。
- 指标 labels 的基数增长是主要运行风险。新增 attribute 前必须说明取值集合、最大基数、保留时间和降级行为。

## 11. 测试与验收矩阵

- `Config.Validate` 覆盖空配置、缺少 service name、非法协议、endpoint、userinfo/query/fragment、端口、TLS/insecure、负 interval/timeout、保留 Resource key 和 header 空键。
- 禁用 metrics 时验证不创建 exporter、reader 或网络连接，返回 no-op Provider 且 `Shutdown` 幂等。
- 使用 `sdkmetric.NewManualReader` 或 reader factory 验证 MeterProvider 注入、resource 属性和 instrument 数据可被采集。
- 使用本地 fake exporter 验证周期导出、export timeout、导出失败错误链和 Shutdown flush；测试不得依赖外部 collector。
- 验证 OTLP/gRPC 与 OTLP/HTTP endpoint option 映射，HTTP path 不被错误拼接或丢失。
- 验证 `InstallGlobal` 的重复调用、不同 Runtime 冲突和并发调用；测试进程不得污染其他包的全局 Provider。
- 验证 Provider 关闭后 reader 不再导出，database/Redis callback 注销后不再产生对应指标，且 Runtime 不关闭外部注入的 Provider。
- 涉及周期 reader、全局状态和关闭并发时运行 `go test -race`。

## 12. 依赖和实施阶段

首期依赖：

- `go.opentelemetry.io/otel/metric`：公共 MeterProvider 接口；
- `go.opentelemetry.io/otel/sdk/metric`：MeterProvider、PeriodicReader、resource reader 生命周期；
- `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc`；
- `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp`；
- `go.opentelemetry.io/otel/sdk/resource`：资源属性构造。

实现阶段建议分为：

1. 配置、endpoint 校验、资源构造和 no-op runtime；
2. OTLP exporters、PeriodicReader、Shutdown 和错误回滚；
3. database/Redis Provider 注入示例与集成测试；
4. 评估 Prometheus reader、View/aggregation 配置和多 reader 支持。

所有 OpenTelemetry 依赖必须与仓库现有 v1 主版本保持一致。metrics 包不得因为支持 Prometheus 而隐式引入 HTTP server 生命周期或修改 application bootstrap 的监听责任。

## 13. 后续待定

- 是否把 OTLP endpoint、headers 和 export interval 接入 `configs/core.yaml`，由应用配置装配决定；metrics 包只提供可反序列化结构。
- 是否支持 Prometheus reader、HTTP handler 和 OTLP reader 并存，需要先确定 handler/reader ownership。
- 是否暴露 View、aggregation、temporality 和 exemplar 配置，需结合实际指标规模和 backend 语义评估。
- 是否需要与 tracing 抽取共享 Resource 构造 helper，需避免两个 core 包形成不必要的运行时依赖。
