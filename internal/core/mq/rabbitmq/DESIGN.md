# RabbitMQ MQ Adapter 设计

## 1. 设计范围

`internal/core/mq/rabbitmq` 将公共 `internal/core/mq` 契约映射到 RabbitMQ。本文只定义需求、配置、拓扑、投递语义、生命周期和验收标准，不添加 RabbitMQ 依赖，也不实现代码。

实现阶段优先选择维护中的 `github.com/rabbitmq/amqp091-go`。具体版本、RabbitMQ 服务端最低版本和 TLS/SASL 能力在实现提交前固定，并与依赖变更一起评审。

## 2. 目标与非目标

### 目标

- 通过 `mq.Publisher` 发布消息，通过 `mq.Consumer` 使用消息；业务代码不依赖 AMQP 类型。
- 默认使用 publisher confirm、手动 ack、有限并发和可取消消费，提供至少一次投递语义。
- 支持显式声明或运维预建 exchange、queue、binding 和 dead-letter 拓扑。
- 支持连接断开后的消费者恢复、拓扑重新声明和优雅关闭。
- 将公共 `Message` 的 ID、Body、Key、Headers、Timestamp 映射到 AMQP 属性，并保持敏感数据不进入错误和 telemetry。

### 非目标

- 不提供 exactly-once、跨 RabbitMQ 与数据库事务、AMQP 事务消息或 outbox/inbox。
- 不把 exchange、queue、routing key、delivery tag、ack/nack 和 channel 暴露到公共 `mq` 包。
- 不在 adapter 内管理 RabbitMQ 集群、用户、权限、镜像队列策略、插件或 broker 运维。
- 首期不提供 RPC、批量发布、延迟插件依赖、优先级策略和动态拓扑热更新。

## 3. 与公共 MQ 契约的映射

公共接口只有一个字符串 `destination`，RabbitMQ 需要同时表达发布路由和消费队列，因此 adapter 不把该字符串直接当作 queue 或 routing key，而是通过逻辑 route 表解析：

```text
destination -> RouteConfig
  Publish: exchange + routing_key
  Consume: queue + binding(s)
```

未知 destination 在操作前返回 `mq.ErrInvalidArgument`。同一 route 可只启用发布或只启用消费；未配置对应部分时，调用会返回明确错误。

建议的运行时配置形状如下，字段名在实现时可调整，但不得把这些类型放入公共 `mq` 包：

```go
type Config struct {
    URL              string
    TLS              *TLSConfig
    Routes           map[string]RouteConfig
    DeclareTopology  bool
    Publisher        PublisherConfig
    Consumer         ConsumerConfig
    Failure          FailureConfig
    DeadLetter       DeadLetterConfig
    MaxPayload       int
    Dialer            Dialer // runtime-only injection, excluded from YAML
}

type RouteConfig struct {
    Exchange         string
    ExchangeType     string // direct, topic, fanout, headers
    RoutingKey       string
    Queue            string
    Durable          bool
    AutoDelete       bool
    Exclusive        bool
    Bindings         []BindingConfig
}
```

`Config` 的 URL、route 和拓扑字段可序列化；Dialer、TLS runtime object、TracerProvider、MeterProvider 等运行时依赖使用 `json:"-" yaml:"-" mapstructure:"-"`。应用配置层负责把文件配置转换为该运行时配置，adapter 不读取 YAML。

## 4. 拓扑与所有权

### Exchange

- `Exchange` 非空时，发布使用 `exchange + routing_key`；`ExchangeType` 必须是 RabbitMQ 支持的类型，首期支持 `direct`、`topic`、`fanout`、`headers`。
- 空 exchange 表示 RabbitMQ 默认 exchange，只允许发布到与 queue 同名的 routing key；此时 route 必须有 Queue，且 adapter 不声明默认 exchange。
- `DeclareTopology=true` 时，adapter 在首次使用 route 前声明 exchange、queue 和 binding，并校验 durable、auto-delete、exclusive 等属性。RabbitMQ 对已存在对象的属性冲突必须作为错误返回。
- `DeclareTopology=false` 时只建立 channel 和消费/发布，不创建对象；broker 预建缺失或属性错误由 RabbitMQ 错误返回。

### Queue 与 Binding

- 消费 route 必须配置 Queue。Queue 的 durable、auto-delete、exclusive 和 arguments 必须明确；生产环境默认 durable=true、auto-delete=false、exclusive=false。
- 一个 route 可以有多个 binding；`direct`/`topic` 使用 routing key，`fanout` 忽略 key，`headers` 使用显式 header 匹配参数。
- 消费者只消费 route 的 queue，不允许通过 `destination` 动态拼接任意 queue 名称，避免越权访问或意外消费其他业务队列。
- 拓扑声明是启动/首次使用行为，不支持运行中自动替换配置。配置热更新必须重启并由 bootstrap 逆序关闭旧 client。

### Dead Letter 与 Retry 拓扑

- Dead-letter exchange、dead-letter routing key 和 dead-letter queue 是显式配置；adapter 不默认猜测名称。
- RabbitMQ 原生 DLX 只在消息 reject/nack 且 `requeue=false`、TTL 到期或队列长度超限时生效。DLX 未配置时，`reject` 会丢弃消息，因此生产配置应在校验阶段拒绝“reject 且无 DLX”的组合，除非显式允许丢弃。
- 有限重试建议使用独立 retry queue：adapter 将原 delivery 复制到 retry exchange/route，递增受 adapter 管理的 attempt 元数据，并等待 mandatory publish return 检查和 publisher confirm 成功后，才 ack 原 delivery。retry queue 经 TTL 和 DLX 回到主 queue；达到最大次数后按相同顺序发布到最终 DLX 并确认原 delivery。confirm 未到、nack 或 mandatory return 时不得 ack 原 delivery；应返回消费基础设施错误，让原消息保持未确认并由 RabbitMQ 重新投递。confirm 成功但原 ack 失败可能产生重复消息，仍按至少一次语义处理。retry queue、TTL、目标 queue 和 binding 必须在拓扑中显式声明。
- 不使用无限 `requeue=true` 作为默认重试机制；它可能在 poison message 上形成 CPU/网络热循环并阻塞队列。

## 5. 发布语义

### Channel 与 Confirm

- Client 至少维护独立的 publisher channel 和 consumer channel；AMQP channel 不作为可并发共享对象直接暴露。
- Publisher channel 开启 confirm mode。`Publish` 将消息发送到 exchange/routing key 后等待 broker confirm，并监听 `NotifyReturn`；mandatory publish 未路由到任何 queue 时返回错误。
- 未收到 confirm、连接断开或 context 超时表示结果可能不确定，不能自动重发同一消息；调用方可用稳定 Message.ID 做幂等处理。
- confirm nack、mandatory return、exchange 不存在、权限失败和 payload 超限均返回带 `%w` 的阶段化错误。
- `Message` 映射建议：`ID -> MessageId`，`Timestamp -> Timestamp`，`Body -> Body`。由于 AMQP `Table` 是 map，不能保留公共 `Headers` 切片允许的重复 key 和顺序；adapter 应将有序 header 列表编码为版本化的保留 AMQP header（例如 `x-aurora-mq-headers-v1` 的 JSON/base64 envelope），消费时解码回原列表。保留键冲突必须拒绝或采用明确的命名空间策略，不能覆盖调用方 header。
- `Key` 默认不映射到 routing key；可通过 route 显式选择映射到 `CorrelationId` 或保留 header。二进制字段必须采用无损编码，无法编码时在发布前报错，不能静默丢失。
- Publisher 必须在 `Publish` 返回前复制调用方的 Body、Key 和 Headers，不能在异步 confirm 等待期间依赖调用方可变缓冲区。

### 发布并发

- 单一 publisher channel 的 publish/confirm 序列必须由 adapter 串行化或使用明确的序列号分发器；不得让多个 goroutine 无锁读写同一 AMQP channel。
- `Publisher.Close` 先拒绝新发布，等待在途 confirm 到达或关闭 deadline，随后关闭 channel 和 connection。deadline 到期返回 `ctx.Err()`，未确认消息结果保持不确定。

## 6. 消费语义

- Consumer 使用独立 channel、`autoAck=false` 和显式 consumer tag。Queue 的 `basic.qos` prefetch 由配置控制，默认有限值，避免无界预取。
- 每条 delivery 先解码为独立拥有的 `mq.Message`，再调用 handler；handler 返回 nil 后执行 `Ack(deliveryTag, false)`。
- handler 返回错误时按 `FailureConfig` 执行：有限 retry、reject-to-DLX 或显式 requeue。失败策略必须记录/计数，但不将业务错误直接作为连接故障返回。
- ack/nack/reject 失败、channel 关闭、协议错误和权限错误属于消费基础设施错误；`Consume` 应停止当前循环并返回带底层错误链的错误。未确认消息由 RabbitMQ 重新投递，保持至少一次语义。
- `Message.ID` 优先取 AMQP `MessageId`；为空时使用 adapter 生成的临时 delivery 标识（例如 exchange、routing key、delivery tag 的组合仅用于本次消费，不保证跨重投或重启稳定）。业务幂等不应依赖 delivery tag。
- `Message.Timestamp` 从 AMQP 时间戳读取；缺失时保留零值。AMQP headers 未能映射的 broker 专属元数据不得伪装成公共 header。

### 并发与顺序

- 同一 Consumer 只允许一个活跃 `Consume` 调用；重复调用返回 `mq.ErrConsumerAlreadyRunning`。
- handler 并发度由 `ConsumerConfig.Concurrency` 控制，并受 prefetch 限制。并发度大于 1 时不承诺队列全局顺序；需要顺序的业务必须设为 1 并使用单一 queue/consumer。
- 每个 handler 使用从 Consume context 派生的 context。`Close` 或消费 context 取消应停止接收新 delivery，并在 deadline 内等待在途 handler；handler 需要响应取消。
- 若 handler 内使用传入的 handler context 调用 `Close`，adapter 必须只触发取消并避免等待自身，防止自等待死锁；拥有 connection 的清理可在消费循环退出后完成。

## 7. 连接、重连与生命周期

- `NewClient` 只做本地配置校验，不隐式拨号；首次 Publish/Consume 建立 connection。也可提供显式 runtime `Connect`/startup check，但不改变公共 `mq` 接口。
- Client 拥有连接、publisher/consumer channel、confirm listener、consumer goroutine、重连 timer 和 topology 声明资源。除非配置明确转移所有权，不关闭应用注入的外部资源。
- Consumer 连接异常时按有限指数退避重连，重新声明启用的 topology、重建 channel/QoS/consumer tag，然后继续消费；超过最大重连时间或收到不可恢复的认证/权限/协议错误时返回 `Consume` 错误。
- Publisher 连接异常不得自动重发已发送但未确认的消息；可以在下一次 Publish 时建立新 channel。连接重建期间的当前 Publish 返回不确定性错误。
- `Close(ctx)` 幂等且并发安全：停止重连、取消 consumer、等待 handler 和在途 confirm、关闭 channel，再关闭由 client 拥有的 connection。关闭超时不得无限等待；后台清理必须有明确所有权和日志/指标。
- URL、用户名、密码、TLS 私钥和 SASL secret 不得出现在错误文本、日志、route label 或 telemetry 属性中。

## 8. 配置校验

`Validate` 只做本地校验，不 DNS 解析、不拨号、不声明拓扑。至少校验：

- URL scheme、地址、虚拟主机和认证字段组合；TLS 证书/私钥配对、CA 文件和安全默认值。
- Routes 非空；每个 route 的发布/消费字段至少配置一侧；queue、exchange、exchange type、routing key 和 binding 参数互相匹配。
- topology declaration、durability、exclusive、auto-delete 和 dead-letter 参数冲突。
- Publisher confirm、mandatory、publish timeout、prefetch、consumer concurrency 和 shutdown timeout 为合法范围。
- retry 最大次数、退避/TTL、retry queue、DLX 目标完整且不会形成自指循环；reject 且无 DLX 只有在显式 `AllowDrop=true` 时允许。
- `MaxPayload` 只限制发布 envelope/body 大小，超过限制在网络调用前返回 `mq.ErrInvalidArgument`。

## 9. 可观测性与安全

- 可选接入 OpenTelemetry，Provider 由应用观测模块创建并注入；RabbitMQ adapter 不关闭应用级 Provider。
- 指标至少包括 publish confirm 成功/失败/nack/return、publish 延迟、消费 delivery、handler 成功/失败、ack/nack 错误、重试/DLX 次数、重连次数和在途数量。
- span 属性只记录 broker 类型、逻辑 destination、exchange、queue、结果和耗时；不要记录 Body、密码、完整 header、routing key 中的用户敏感数据或 delivery tag 高基数值。
- TLS 服务端校验默认开启；允许跳过校验时必须显式配置并在启动日志中给出不含凭据的警告。

## 10. 测试与验收

### 单元测试

- 配置校验覆盖空 route、exchange/queue 冲突、TLS 配对、retry/DLX 缺失、非法 QoS/并发和敏感错误脱敏。
- 使用 fake Dialer/AMQP channel 覆盖 route 解析、发布属性映射、confirm ack/nack、mandatory return、handler ack、失败策略和关闭顺序。
- 覆盖重复 Consume、Publish 与 Close 并发、handler context 取消、关闭 deadline、连接断开和重连上限。

### 集成测试

- 使用临时 RabbitMQ 6.2+ 实例验证 exchange/queue/binding 声明、publisher confirm、mandatory return、手动 ack、重复投递、retry queue TTL 和 DLX。
- 集成测试使用随机 vhost 或随机命名空间，不执行全局删除/清空命令，不依赖开发机上固定的共享 queue。
- 故障测试覆盖 broker 重启、channel 关闭、消费中断、confirm 未决和重连后 topology 恢复。
- 运行 `go test -race ./internal/core/mq/...`，并在有 goroutine、channel 和 callback 变更时扩大到 `go test -race ./...`。

## 11. 实施顺序与待确认项

1. 固定 `amqp091-go` 版本和最低 RabbitMQ 版本，完成 Config/Route 的本地校验。
2. 实现连接所有权、publisher confirm 和 route topology 声明。
3. 实现单消费者、手动 ack、prefetch 和 handler context 取消。
4. 实现显式失败策略、retry queue/DLX 和重连恢复。
5. 接入应用配置/bootstrap、OpenTelemetry 和集成测试。

编码前仍需确认：

- route 是由应用声明还是由运维预建；生产环境是否允许 adapter 自动声明 topology。
- 首期是否必须提供有限重试和 DLX，还是只提供 ack/reject 基础能力。
- 连接是由 RabbitMQ adapter 创建并拥有，还是由应用注入并共享。
- 是否要求 publish 的 mandatory return、confirm nack 和重连指标进入统一告警。
