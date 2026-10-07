# Redis 客户端设计

## 1. 需求与目标

`internal/core/redis` 为 Aurora 提供统一的 Redis 客户端构造入口，底层使用
`github.com/redis/go-redis/v9`。该包负责把应用配置映射到 go-redis，校验配置，
配置 TLS 与 OpenTelemetry instrumentation，并管理客户端及 instrumentation 的关闭。

首期需求如下：

- 支持 Standalone、Sentinel 主从故障转移和 Redis Cluster 三种拓扑。
- 支持 Redis ACL/密码认证、超时、命令重试和连接池参数。
- 支持 TLS 服务端校验、自定义 CA 和可选双向 TLS。
- 支持通过 `redisotel` 接入 OpenTelemetry tracing 和 metrics。
- 提供确定性的配置校验和完整资源清理，不在构造时隐式探测网络。
- 对敏感命令参数采取安全默认值，避免 key、token 等数据进入 tracing 属性。

## 2. 非目标

- 不封装或重定义 Redis 命令 API；调用方直接使用 go-redis 的命令、Pipeline、Pub/Sub、事务等能力。
- 不负责创建或关闭应用级 `TracerProvider`、`MeterProvider`、exporter、采集端点和采样策略。
- 不提供 Redis 服务端部署、哨兵/集群管理、连接健康检查任务或自动重连之外的业务重试。
- 首期不实现连接池热更新、多租户客户端注册表、自动故障熔断或命令白名单。
- 不默认记录 Redis 命令全文，也不提供独立的 Redis 命令日志系统。

## 3. 公共 API

建议公共接口保持精简：

```go
type Mode string

const (
    ModeStandalone Mode = "standalone"
    ModeSentinel   Mode = "sentinel"
    ModeCluster    Mode = "cluster"
)

type Config struct {
    Mode       Mode
    Addrs      []string
    Network    string
    DB         int
    Username   string
    Password   string
    ClientName string
    Protocol   int
    DisableIdentity bool
    IdentitySuffix  string

    Timeout    *TimeoutConfig
    Retry      *RetryConfig
    Pool       *PoolConfig
    Buffer     *BufferConfig
    TLS        *TLSConfig
    Monitoring *MonitoringConfig
    Sentinel   *SentinelConfig
    Cluster    *ClusterConfig
}

type SentinelConfig struct {
    MasterName string
    Username   string
    Password   string
}

func (c *Config) Validate() error
func NewClient(cfg *Config) (redis.UniversalClient, error)
```

具体字段应使用 JSON/YAML/mapstructure 标签供应用配置系统读取。Provider 等运行时依赖
只用于代码注入，不序列化到配置文件。除非实现阶段确认需要，不在首期公开 go-redis
所有底层选项；缓冲区、RESP 协议和集群读路由等可按使用场景纳入配置。

`NewClient` 返回值应保持 `redis.UniversalClient` 兼容，允许调用方直接调用 go-redis API。
当开启 metrics 时，包内应通过轻量包装器持有指标采集的停止句柄；包装器仍实现完整的
`redis.UniversalClient`，其 `Close()` 先停止 instrumentation，再关闭底层客户端，并返回
可观察的关闭错误。未启用 metrics 时直接返回原生 go-redis 客户端，保留其具体类型兼容性。
由于 redisotel 会按具体的 `*redis.Client`/`*redis.ClusterClient`
类型注册 metrics，必须先对原始 go-redis 客户端完成 instrumentation 注册，再将它包装返回。
不要要求调用方断言具体的 `*redis.Client` 或 `*redis.ClusterClient`。

## 4. 拓扑与配置规则

### Standalone

- 空 `Mode` 视为 `ModeStandalone`。
- `Addrs` 必须有且仅有一个合法地址；支持 TCP 系列网络，Unix Socket 仅在 Standalone 下开放。
- `DB` 必须大于等于 0；用户名和密码用于数据节点 ACL/认证。
- 使用 `redis.NewClient`。

### Sentinel

- `Addrs` 是 Sentinel 地址列表，必须至少有一个地址。
- `Network` 仅允许默认的 `tcp`；Sentinel 连接选项没有独立网络类型配置。
- `Sentinel.MasterName` 必填；Sentinel 的认证凭据与数据节点凭据分开配置。
- `Config.Username/Password` 认证 Redis 主从数据节点；`Sentinel.Username/Password` 认证哨兵。
- go-redis/v9 的 `FailoverOptions` 只有一份 `TLSConfig`，会同时用于 Sentinel 控制面和
  数据节点连接；本包首期不承诺为两者提供不同 TLS 配置。
- `DB` 可用。默认读写均经当前主节点；只读副本路由属于显式可选策略，并须说明复制延迟风险。
- 使用 `redis.NewFailoverClient`。主节点发现和故障切换由 go-redis/Sentinel 完成。

### Cluster

- `Addrs` 是 Cluster 种子节点，允许一个或多个合法地址；运行中拓扑和槽位由 go-redis 加载、刷新。
- `DB` 必须为 0，因为 Redis Cluster 不支持选择其他逻辑库。
- `Network` 仅允许默认的 `tcp`；go-redis 的 ClusterOptions 不提供 Unix Socket 或独立
  Network 配置。
- `Sentinel` 配置不允许与 Cluster 同时出现。
- 副本读路由、随机路由和延迟路由默认关闭；开启时明确提示读取可能落在副本且存在复制延迟。
- 使用 `redis.NewClusterClient`。MOVED/ASK 重定向和拓扑刷新由 go-redis 处理。

### 通用校验

`Config.Validate` 只检查本地配置，不解析 DNS、不建立连接、不发送 PING。校验至少覆盖：

- Mode、Network、地址格式/端口、地址数量和各拓扑专属配置是否匹配；非当前拓扑的
  Sentinel/Cluster 配置组应拒绝，而不是静默忽略。
- DB、Protocol、超时、重试、池容量、TLS 证书配对及集群参数的取值范围。
- 运行时 TLS 初始化时读取 CA/证书并校验 PEM 与密钥；文件缺失或格式错误时构造失败。

时间与重试字段遵循 go-redis 的语义。未配置配置组时保留 go-redis 默认值；显式传入的
零值是否覆盖底层默认值必须在实现中统一处理并通过测试固定。对禁用重试、无超时等特殊
值只接受 go-redis 明确定义的值。文档和示例提醒：网络错误后的自动重试可能使非幂等操作
结果不确定，业务层应按命令语义决定重试策略。

## 5. 连接池与超时

配置组建议包含拨号、读写、池等待超时，以及连接池容量、空闲连接数、最大活动连接数和
连接空闲/生命周期限制。零值默认行为应以所选 go-redis/v9 版本为准，不在本包复制一套
不同的默认值。

`BufferConfig` 只用于大 pipeline、大 value 或高吞吐批处理场景；零值保留 go-redis 默认
缓冲区大小。`DisableIdentity` 和 `IdentitySuffix` 控制 go-redis 建连时的客户端身份上报，
仅在代理兼容性或连接观测需要时配置。

Standalone 和 Sentinel 的池配置作用于相应客户端连接池。Cluster 下连接池通常按节点建立，
`Pool.Size` 不是整个 Cluster 的总连接上限；总连接数会随节点数、连接池配置及实际访问节点
增长。容量规划需要同时考虑应用实例数和 Redis 节点数。

## 6. TLS 与认证

TLS 配置为空或 `Enabled=false` 时不创建 TLS 配置。启用后：

- 默认执行系统根证书链和主机名校验，可配置 `ServerName`、额外 CA 文件。
- 可选加载客户端证书与私钥，二者必须同时提供。
- 证书校验跳过开关仅为特殊测试/兼容用途，默认关闭，生产环境不得作为常规配置。
- 不记录密码、证书内容或连接凭据；错误信息不得拼接敏感值。

通用 TLS 配置应用到 go-redis 所管理的数据节点连接。Sentinel 模式中同一份 TLS 配置也会
应用到 Sentinel 控制面；Sentinel 认证密码与数据节点密码仍须独立配置。若部署确实要求
控制面和数据面使用不同 TLS 参数，必须在后续设计中引入自定义 Dialer/客户端适配，并补充
对应的安全测试，不能仅靠当前 `TLSConfig` 字段实现。Cluster 的 TLS 配置应用到各数据节点连接。

## 7. Tracing 与 Metrics

使用 `github.com/redis/go-redis/extra/redisotel/v9` 为底层 UniversalClient 注册 instrumentation。
建议 `MonitoringConfig` 包含独立的 `TracingEnabled`、`MetricsEnabled` 开关，以及可选的
运行时 `trace.TracerProvider`、`metric.MeterProvider`。Provider 未注入时使用 OpenTelemetry
全局 Provider。Provider 创建、exporter 配置、flush 和 shutdown 全由应用观测模块负责。

Tracing 的敏感数据规则：

- 完整命令（`db.statement`）默认不采集；只有调用方确认命令参数无敏感信息时才允许显式开启。
- 调用方位置和连接拨号 span 默认关闭，以控制数据量和开销；需要诊断时可显式打开。
- 不把 key、value、密码或凭据添加为自定义 span 属性。

Metrics 通过 redisotel 输出到 OpenTelemetry MeterProvider，Prometheus exporter 由应用注册。
客户端关闭时先关闭 redisotel 的停止通道，再关闭 Redis 连接池；redisotel 在后台异步注销
指标回调，因此 `Close` 不承诺注销动作已完成才返回。创建客户端过程中若任一 instrumentation
注册失败，应关闭停止通道、关闭已创建客户端并释放已注册资源，再返回带上下文的错误。

## 8. 生命周期与错误语义

构造顺序：校验配置 -> 构建所需 TLS 配置 -> 按拓扑构造 go-redis 客户端 -> 注册启用的
instrumentation -> 返回可关闭的 UniversalClient。TLS 文件加载错误、参数映射错误或
instrumentation 注册错误均在构造阶段返回；不以连接 Redis 成功作为构造成功的前提。

关闭顺序：发出停止 metrics 采集信号 -> 关闭底层 go-redis 客户端。指标回调注销异步执行，
客户端 API 不提供等待注销完成的句柄。`Close` 应可重复调用且并发安全，并对每次调用返回
一致的底层关闭结果。客户端关闭后由 go-redis 返回命令错误，包不另设隐式重建行为。

配置错误、TLS 文件错误、拓扑客户端构造错误和 instrumentation 错误应包含阶段信息并保留
底层错误链（`%w`），但不得包含密码或证书数据。

## 9. 目录建议

实现阶段按职责拆分，避免单个文件混合配置校验、TLS 与遥测：

- `config.go`：类型、默认语义、字段校验。
- `client.go`：公共构造入口、拓扑客户端构造及关闭包装。
- `tls.go`：TLS 配置构建和证书加载。
- `monitoring.go`：redisotel 注册、错误回滚和指标采集关闭。
- `*_test.go`：配置、拓扑映射、TLS、监控和生命周期测试。

## 10. 验收与测试矩阵

- 配置校验覆盖默认 Standalone、未知模式、空/非法地址、Standalone 多地址、缺失 Sentinel
  MasterName、Cluster 非零 DB、拓扑配置冲突和边界值。
- 每种拓扑可在不依赖在线 Redis 的情况下构造，并验证底层 go-redis 选项映射正确。
- TLS 覆盖关闭、有效自定义 CA、证书/私钥配对错误、无效 PEM 和 ServerName 传递。
- Tracing 验证使用注入 Provider 生成 span，默认不包含 `db.statement`，显式启用才包含。
- Metrics 验证注册、失败回滚和 `Close` 后停止采集，不关闭应用持有的 Provider。
- `Close` 验证关闭底层客户端、重复调用及并发调用行为。
- 单元测试不依赖外部 Redis；需要协议级验证时使用本地临时 Redis/容器集成测试，并与单测分开。

## 11. 依赖

- `github.com/redis/go-redis/v9`：Standalone、Sentinel、Cluster 客户端及连接池。
- `github.com/redis/go-redis/extra/redisotel/v9`：OpenTelemetry tracing 和 metrics instrumentation。
- `go.opentelemetry.io/otel`、`trace`、`metric`：Provider 注入类型和全局 Provider 接入。

两个 Redis 模块应选择与仓库 OpenTelemetry 主版本兼容的版本，并在 `go.mod` 中保持
redisotel 与 go-redis 的版本组合一致。
