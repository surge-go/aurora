# MQ 公共契约设计

## 1. 设计范围

本阶段设计并实现 `internal/core/mq` 公共契约包，包含消息类型、生产/消费接口和稳定错误类别；Redis Streams adapter 已在 `internal/core/mq/redis` 实现。RabbitMQ、Kafka 等其他 adapter 暂不实现，具体 broker 专属配置、能力映射和 SDK 另行评审。

公共包的目标是让业务代码依赖稳定的消息生产、消费接口和消息模型，不直接依赖 broker SDK。接口保持小而明确，不尝试抹平不同 broker 的全部差异。

## 2. 需求

- 生产者可以向一个逻辑目的地发布不透明的消息载荷。
- 消费者通过 handler 处理消息，并能通过 `context.Context` 取消消费和传播调用上下文。
- 消息模型至少承载稳定消息 ID、可选路由/分区键、字节载荷、可重复的 header 和时间戳。
- 发布、消费、错误和资源关闭具有明确约定，业务可以编写 broker 无关的调用代码和 fake 测试替身。
- 包不读取配置文件、不创建全局 client、不管理进程信号、不初始化日志或 OpenTelemetry Provider。

## 3. 非目标

- 不在公共包中声明 broker 选择、地址、认证、TLS、连接池、重试、死信、分区或拓扑配置。
- 不提供消息序列化、schema 管理、请求-响应 RPC、批量发布、事务、outbox/inbox 或 exactly-once 保证。
- 不承诺不同目的地之间或同一目的地内的全局顺序。
- 不把任一 broker SDK 的消息类型、错误类型或配置结构暴露到公共接口。

## 4. 包结构

```text
internal/core/mq/
  DESIGN.md   # 本文：公共契约设计
  message.go  # 消息与 header 类型
  client.go   # Publisher、Consumer、Handler 接口
  errors.go   # 稳定的公共错误类别（仅在确有需要时增加）
  redis/      # Redis Streams adapter；复用注入的 internal/core/redis client
  rabbitmq/   # RabbitMQ adapter 设计；实现前先完成本目录设计评审
  kafka/      # Kafka adapter 设计；实现前先完成本目录设计评审
```

公共 API 落在 `internal/core/mq` 根包；具体实现放在独立子包中，Redis Streams adapter 依赖公共契约和注入的 go-redis client，公共包不反向依赖实现。应用配置和 client 装配由应用配置层及 bootstrap 负责，公共包不负责构造具体实现。

Kafka adapter 的设计见 `internal/core/mq/kafka/DESIGN.md`。该实现通过逻辑 destination 映射 Kafka topic，并在 adapter 内部管理 consumer group 与 offset；这些 broker 专属概念不进入公共契约。

## 5. 公共 API 草案

```go
type Header struct {
    Key   string
    Value []byte
}

type Message struct {
    ID        string
    Key       []byte
    Body      []byte
    Headers   []Header
    Timestamp time.Time
}

type Handler func(context.Context, Message) error

type Publisher interface {
    Publish(ctx context.Context, destination string, message Message) error
    Close(ctx context.Context) error
}

type Consumer interface {
    Consume(ctx context.Context, destination string, handler Handler) error
    Close(ctx context.Context) error
}
```

公共包不强制定义同时包含生产和消费能力的 `Client`。调用方只依赖其需要的 `Publisher` 或 `Consumer`；具体实现可在后续按连接共享与生命周期需求提供组合 client 或独立构造入口。

## 6. 类型与行为约定

### Message

- `ID` 是调用方提供的稳定消息标识，用于日志关联和业务幂等；公共包不生成 ID，也不承诺 broker 接受空 ID 时的行为。具体实现需说明是否保留或补充 ID。
- `Key` 是可选的路由/分区亲和提示。公共契约不将其解释为全局顺序保证；具体实现对 key 的支持由实现文档说明。
- `Body` 是不透明字节，序列化、版本兼容和 schema 校验由业务负责。
- `Headers` 使用切片，允许重复 key，并保留调用方给出的顺序。`Publish` 返回后实现不得继续读取或修改调用方的可变切片；异步发送实现必须在返回前复制。Consumer 传给 handler 的消息及其所有字节切片由 Consumer 独占，handler 返回前不得修改；handler 如需在返回后保留消息，应调用 `Message.Clone()`。
- `Timestamp` 是可选的消息时间，不用于去重、过期或排序承诺。

### Publish

- `destination` 是非空的逻辑目标名称；具体实现负责映射到自身的目标概念。根包不规定其命名语法。
- 成功表示实现按其公开的确认策略接受了消息。返回网络或超时错误时，消息可能已经被接受，调用方重试可能导致重复，因此业务应使用稳定 ID/幂等键。
- 参数非法、client 已关闭、消息超出实现限制和底层发布失败都应返回错误；不得静默丢弃或截断消息。

### Consume 与 Handler

- `Consume` 在调用期间持续拉取并处理指定目的地的消息，直到 `ctx` 取消、client 关闭或发生无法继续的消费错误。
- handler 返回 `nil` 表示业务处理成功；返回非 nil 表示处理失败。具体实现如何重试、暂停、终止或隔离失败消息不由公共包规定，必须在具体实现文档中说明。
- handler 错误默认属于单条消息处理结果，不自动视为消费连接故障；不可恢复的连接或协议错误由 `Consume` 返回。
- `Consume` 允许异步实现，但同一 Consumer 的并发调用规则必须固定。建议首期限制每个 Consumer 同时只有一个活跃 `Consume` 调用，重复启动返回稳定错误；并发处理度由具体实现配置控制。
- 取消 context 后应停止接收新消息，并尽可能等待已开始的 handler 完成。Consumer 必须为活跃消费/handler 派生可取消 context；`Close` 必须触发取消，使 handler 能及时退出。handler 应响应取消。实现不得在调用已返回后继续访问 handler 的消息缓冲区。

## 7. 错误约定

- 公共错误只描述稳定、跨实现有意义的类别，例如无效参数、client 已关闭、重复启动消费；不包装或复制某个 broker 的全部错误分类。
- 具体实现应使用 `%w` 保留底层错误链，使调用方可以检查公共 sentinel/type，也可在需要时检查底层错误。
- 错误消息不得包含密码、连接凭据、完整敏感 header 或消息 body。
- handler 返回的业务错误应尽可能保留原始错误身份，便于日志和指标区分业务失败与基础设施失败。

## 8. 生命周期与并发

- `Publisher`、`Consumer` 各自是调用方拥有的资源，调用方必须调用 `Close` 释放连接和后台资源。组合实现可以让二者共享底层资源，但 `Close` 仍需保证资源只释放一次。
- `Close(ctx)` 幂等且并发安全；停止接收新操作，取消活跃消费，等待在途操作并释放该接口拥有的资源。`ctx` deadline 到期时应返回 `ctx.Err()` 或包含它的关闭错误，不得无限等待；具体实现须说明 deadline 到期后清理是继续后台完成还是由调用方再次 Close 推进。
- 调用方负责安排关闭顺序和 shutdown deadline；公共包不接管系统信号，也不调用 `os.Exit`。
- `Publish`、`Consume` 和 `Close` 的并发安全性必须由具体实现满足并在其文档中写明。公共接口要求 `Close` 与正在运行的 `Consume` 可并发调用。
- context 取消应尽快传递到底层操作；实现必须避免无法退出的 goroutine 和关闭后继续回调 handler。

## 9. 可靠性边界

公共契约不承诺 exactly-once，也不把所有实现都描述成相同的投递保证。业务需要按至少一次的故障模型编写幂等处理：处理成功与确认之间发生进程故障时，同一消息可能再次交付。具体实现必须说明其发布确认、消费确认、失败恢复和重复投递行为。

公共 API 暂不暴露 delivery attempt、ack/nack、offset、partition、routing key、transaction 或 dead-letter 等 broker 专属概念。未来若多个实现确实需要统一某项能力，应先定义跨实现一致的语义，再决定是否纳入根包；单个实现独有能力留在其专属包。

## 10. 验收标准

- 根包 API 仅依赖 Go 标准库，不导入 broker SDK 或应用配置包。
- 业务代码可以仅依赖 `Publisher` 或 `Consumer` 编写调用和 fake 测试。
- 消息字段、发布结果不确定性、handler 返回值含义、消费取消和关闭语义均有文档约定。
- 公共包不暴露具体 broker 配置，不包含网络连接或隐式后台任务。
- RabbitMQ adapter 的设计见 `internal/core/mq/rabbitmq/DESIGN.md`；Kafka adapter 的设计见 `internal/core/mq/kafka/DESIGN.md`。两个 adapter 都需在实现时说明与公共契约的语义差异和验证方式。Redis adapter 的连接、TLS 和连接池配置沿用 `internal/core/redis`，Streams 语义见 `internal/core/mq/redis/README.md`。

## 11. 后续待定

当前实现选择调用方提供 ID、每次操作显式传 destination，并分别定义 Publisher 和 Consumer；不预设组合 client。消息 ID 自动生成、生产者与消费者共享生命周期及组合 `Client` 接口留待实际调用场景出现后再决定。
