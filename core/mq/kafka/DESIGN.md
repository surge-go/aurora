# Kafka MQ Adapter 设计

## 1. 设计范围

`core/mq/kafka` 将公共 `core/mq` 契约映射到 Apache Kafka。本文档定义 adapter 的配置、路由、发布确认、consumer group、offset 提交、失败恢复、安全和验收标准；首期代码实现按本设计落地，并在发现实现约束时通过本文件记录取舍。

首期固定使用 `github.com/twmb/franz-go` v1.22.1（Go 1.26+），支持 TLS、SASL PLAIN/SCRAM-SHA-256/SCRAM-SHA-512、同步 `acks=all` produce、禁用自动提交和显式 offset commit。最低 Kafka broker/兼容产品版本由部署环境确定；实现不创建或修改 topic。

## 2. 目标与非目标

### 目标

- 以 `mq.Publisher`、`mq.Consumer` 接口发布和消费 Kafka record，业务层不依赖 Kafka 客户端类型。
- 通过显式 route 表将逻辑 destination 映射到 topic，避免业务代码任意选择 topic。
- 发布等待 broker acknowledgement；消费成功后手动提交 offset，提供至少一次处理语义。
- 支持 consumer group、多 broker、TLS、常用 SASL 认证、有限并发、取消和受控关闭。
- 在分区重平衡期间避免旧 generation 的 handler 提交 offset；保留每个分区的顺序和连续提交边界。
- 将 `Message` 字段映射到 Kafka key、value、headers 和 record timestamp，并避免敏感数据进入错误或 telemetry。

### 非目标

- 不承诺 exactly-once，不协调 Kafka offset 与数据库事务，也不提供 Kafka transaction API。
- 不把 topic、partition、offset、consumer group、rebalance callback 或 Kafka record 暴露到公共 `mq` 包。
- 不负责创建/删除 topic、修改 replication factor、分区数、ACL 或 broker 集群配置；topic 运维由 Kafka 管理面负责。
- 首期不提供 schema registry、RPC、批量事务生产、自动 topic 管理、任意 partition 手动指定或动态配置热更新。
- 不把 Kafka consumer group 的横向扩缩容和 MQ 公共接口中的全局顺序混为一谈。

## 3. 公共契约映射

Kafka 的自然目的地是 topic，但公共契约使用逻辑 destination。配置提供静态映射：

```text
destination -> RouteConfig.Topic
  Publish: topic + record key + value + headers + timestamp
  Consume: topic + Consumer.GroupID + committed offset
```

- destination 必须精确命中配置 route；未知值在任何网络操作前返回 `mq.ErrInvalidArgument`。
- 同一 route 可用于发布和消费。消费还要求配置非空 `Consumer.GroupID`。
- Kafka topic 名称必须符合 broker 规则；adapter 不从 destination 拼接或推导 topic。
- route 配置不得含动态 topic 模板、通配符订阅或运行时 topic override。
- `NewClient` 只做本地校验和客户端对象构造，不连接 broker、不创建 topic、不执行 metadata 健康检查。

## 4. 建议配置模型

字段名可在实现时微调，但 broker 专属结构留在本包；文件配置字段使用 JSON/YAML/mapstructure 标签。运行时对象使用 `json:"-" yaml:"-" mapstructure:"-"`。

```go
type Config struct {
    Brokers  []string
    ClientID string
    Routes   map[string]RouteConfig
    TLS      *TLSConfig
    SASL     *SASLConfig
    Producer ProducerConfig
    Consumer ConsumerConfig
    MaxPayload int
    Dialer   Dialer // runtime-only test/transport injection
}

type RouteConfig struct {
    Topic string
}

type ProducerConfig struct {
    RequiredAcks RequiredAcks
    Timeout      time.Duration
    MaxRetries   int
    Idempotent   bool
}

type ConsumerConfig struct {
    GroupID          string
    Concurrency       int
    StartOffset       StartOffset
    SessionTimeout    time.Duration
    RebalanceTimeout  time.Duration
    Retry             RetryConfig
    Failure            FailureConfig
}

type RetryConfig struct {
    MaxAttempts int
    Backoff     time.Duration
}

type FailureConfig struct {
    Mode            FailureMode
    DeadLetterRoute string
}
```

TLS 配置包含 CA、client certificate/key、server name 和显式的 `InsecureSkipVerify`。SASL 至少定义 PLAIN、SCRAM-SHA-256、SCRAM-SHA-512；token callback 等运行时 provider 通过 Option/Dialer 注入且不序列化。不得自行实现认证加密或把认证 secret 写入错误。

`RequiredAcks` 首期只允许 `all`（等待当前 in-sync replica 集合确认）；producer idempotence 默认开启。若需要允许较弱的确认策略，必须作为明确的可靠性降级配置记录，并在调用文档中说明数据丢失风险。重试次数和总 delivery timeout 必须有限。

`StartOffset` 只作用于新建 consumer group 没有已提交 offset 时，允许 `earliest` 或 `latest`。一旦 group 已有 offset，Kafka 已提交进度优先。默认选择必须结合部署语义固定并写进 README；建议默认 `earliest`，避免新 group 默默跳过已有数据。

`Concurrency` 是单个 Consumer 实例处理不同 partition 的最大并发度，不允许同一 topic-partition 上多个 handler 并行，以维持分区内处理顺序和安全提交。不同实例加入相同 group 后由 Kafka 分配 partition；实例数超过 partition 数时，多出的实例处于空闲状态。

`Config.Validate` 只做本地确定性校验，不 DNS 解析、不拨号、不请求 metadata。至少验证：broker 地址列表与 host/port 格式、route 和 topic 名称、consumer group/client ID、启用的认证组合、TLS 证书配对、时间/容量/重试范围、DLQ route 完整性和 route 自循环、保留 header 冲突以及 `MaxPayload` 合法性。

## 5. Record 与消息字段映射

Kafka record 结构天然含 key、value、headers 和 timestamp，可直接表达公共消息的主要字段：

| `mq.Message` 字段 | Kafka record 映射 | 约束 |
| --- | --- | --- |
| `ID` | 保留 header `x-aurora-mq-id-v1` | 字符串原样编码；冲突在发布前拒绝，消费时仅解出该键并从业务 headers 移除 |
| `Key` | record key | 保留二进制值；nil 与空切片按客户端语义记录并测试 |
| `Body` | record value | 不透明字节，发布前检查最大 record 大小 |
| `Headers` | record headers 切片 | 顺序及重复 key 必须保留；值是二进制 |
| `Timestamp` | record timestamp | 零值交由客户端/broker 时间戳策略处理；非零值原样设置 |

Kafka headers 是有序切片，可以保留公共 header 的重复键及顺序。由于 Kafka 没有 record ID 字段，adapter 需为 `Message.ID` 使用固定保留键；公共 header 不得覆盖该键。解码外部生产者发来的无该版本字段时返回空 ID，不从 offset 伪造稳定 ID；delivery 的临时定位信息只允许用于诊断，不保证重投/重启后稳定。

Kafka header key 是字符串，公共 key/value 字段必须在发出网络请求前校验。二进制 `Value` 不做 UTF-8 转换或文本归一化。Kafka 内部 header、压缩和 broker metadata 不得伪装成业务 `mq.Header`。

若 `Timestamp` 为零，需固定选择由 producer 写入零值还是不设置字段并由 Kafka broker 生成时间戳；两种行为不可混用。Kafka broker 可使用 CreateTime 或 LogAppendTime，后者会覆盖应用时间，部署文档应指出时间戳来源取决于 topic/broker 配置。

## 6. 发布语义

- 每次 `Publish` 对应一个 Kafka record，使用同步 produce API 等待 record 的最终 delivery result；仅本地 enqueue 成功不算发布成功。
- `acks=all` 表示由当前 ISR 集合确认，不代表所有副本，也不替代合理的 `min.insync.replicas` 和 replication factor 运维配置。
- producer idempotence 可以去重同一 producer session 的协议重试，但不等于应用级 exactly-once；进程重启或调用方重试可能仍产生重复 record。
- ctx 取消、delivery timeout、broker 错误或连接断开可能使结果不确定。adapter 不在调用返回后继续访问调用方的 `Message` 切片；需要异步发送时必须在返回前深拷贝。
- 发布错误使用 `%w` 保留底层错误链，并标记发生在校验、produce 还是 delivery-confirm 阶段。错误不得包含 body、headers 全文、SASL secret 或带凭据的完整 broker URL。
- `MaxPayload` 在网络调用前校验 value、key、header envelope 及其协议开销的保守上界；超限返回 `mq.ErrInvalidArgument`。broker 自身的 `message.max.bytes` 仍可能拒绝 record。
- 同一 producer client 可以被多个 goroutine 并发调用；若所选库对象本身并发安全，adapter 不额外串行化所有目的地。相同 key 的分区路由和分区内写入顺序由 partitioner 和 broker 决定，不构成公共接口承诺。

## 7. Consumer group、offset 与重平衡

Kafka 消费确认是提交“下一条待读 offset”，不是逐条 ack。实现必须关闭自动提交，处理每个 record 后再手动提交已完成进度：

1. 独立 poll loop 持续 fetch records 并处理 group/rebalance 事件；它不得同步等待 handler 或 commit 完成，否则慢 handler 可能拖延 poll 并触发不必要的 group 迁移。
2. poll loop 确认 record 属于当前 generation/assignment 后，将 record 放入有界的 partition 内队列；队列满时暂停继续接收/分发该分区数据，不能无界缓存。
3. 按分区顺序调用 handler，并确认 record 仍属于当前 group generation 和 partition assignment。
4. 为 handler 创建派生 context；`Close`、父 context 取消或 partition revoke 会取消相应 handler context。
5. handler 成功后，将该 record 标记为完成。
6. 只提交同一 topic-partition 上从上次已提交位置起连续成功的已交付 records；提交值是该连续前缀最后一条 record 的 `offset + 1`。offset 数值可能因 compaction/control records 有空洞，连续性按实际交付顺序判断，不要求整数逐个相邻。
7. commit 成功后推进本地已提交水位。commit error 视为结果可能不确定；不能假设 offset 一定未提交。

为避免 offset 越过失败/未完成 record，首期实现按 partition 顺序调用 handler；不同 partition 可并行，且受 `Concurrency` 限制。若未来同 partition 并发处理，必须实现连续完成水位，严禁提交高于仍未完成 offset 的位置。

### Rebalance 与取消

- Consumer group 的 partition assignment/revocation 全由 Kafka 客户端管理；adapter 必须在撤销分区时停止向该分区分发新 handler，取消该 generation 的在途 handler，并在 rebalance deadline 内等待可完成任务。
- 只允许在当前 assignment/generation 有效时提交 offset。旧 generation handler 即使晚返回也不得提交；未提交记录交由新 owner 重读。
- rebalance 中已成功完成但尚未确认提交的消息可能再次投递；业务必须按至少一次模型幂等处理。
- `Consume` 返回错误前不得启动仍能调用 handler 或提交 offset 的后台 goroutine。
- 非恢复性认证、配置、协议错误直接从 `Consume` 返回；短暂网络故障和可恢复 coordinator 错误遵循有限重试/客户端恢复策略。不得无上限自建重连循环与 Kafka 客户端内部恢复叠加。

### 投递保证

本 adapter 提供至少一次处理语义，不提供 exactly-once。成功 handler 后、offset commit 前进程崩溃会再次处理该 record；offset 提交结果不确定时也可能发生重复。禁止在 handler 开始前提交 offset。

## 8. Handler 错误与死信策略

Kafka 没有 RabbitMQ 式原生 reject/DLX。消息处理失败必须使用明确策略，不能把未提交 offset 与失败消息继续前进混为一谈：

- `stop`：首期默认。保留失败 record 未提交，取消当前消费并返回带 handler 错误链的错误；调用方修复后重新启动 consumer 可从失败位置继续。不得在后台无限重试或提交该 partition 的更高 offset。
- `retry`：仅当显式配置有限 `MaxAttempts` 和正 backoff 时启用。同一 record 在同一 assignment 下按原位重试，期间不得处理/提交该 partition 更高 offset。次数耗尽后转入配置的最终失败策略。
- `dead_letter`：要求显式配置目标 logical route。先将原 body/key/headers/ID/timestamp 与源 topic、partition、offset、错误类型等受控诊断 metadata 发布到 DLQ，并等待 `acks=all` delivery success，再提交源 offset。若 DLQ publish 成功而源 offset commit 失败，DLQ 可能重复；保留源 ID 以供幂等。
- `drop` 不作为默认可选项。只有显式允许丢弃时，handler 失败才可提交源 offset；配置名称必须体现数据丢失风险。

DLQ route 不得等于消费 source topic；启动校验需阻止直接自循环。跨 topic 多级循环无法仅靠本地配置完整发现，运维需保证拓扑无环。DLQ publish 失败时源 offset 必须保持未提交，并让消费停止或按有限退避重试。

### Retry topic 边界

首期不依赖延迟消息插件，也不默认采用 retry topic，因为 Kafka 原生 topic 没有逐条 TTL。后续若设计分层 retry topics，必须定义 attempt header、每层延迟、最大次数、DLQ 转移、原 topic offset commit 顺序、重复语义和防循环校验；不能用 sleep 阻塞整个 consumer group 的 poll/rebalance 事件循环。

## 9. 并发、生命周期与资源所有权

- 同一 Consumer client 同时只允许一个活跃 `Consume`；第二次调用返回 `mq.ErrConsumerAlreadyRunning`。不同 client 可加入相同 group 参与负载均衡。
- Publisher 与 Consumer 若由一个 `Client` 组合实现，共享 Kafka client/transport 时只有 adapter 负责一次关闭；应用不得关闭 adapter 内部对象。
- `Close(ctx)` 幂等、并发安全：拒绝新 publish/consume，取消消费和在途 handler，等待正在提交的 offset 与 delivery result 到 deadline，再关闭由 adapter 创建的 Kafka client。
- Close deadline 到期必须说明清理策略：停止等待并安排后台最终关闭，或由后续 Close 推进；不得遗留无限 goroutine、poll loop 或后台重试。
- handler 使用从 Consume ctx 或当前 assignment ctx 派生的 context。handler 内调用 `Close` 必须取消运行并避免等待自身导致死锁。
- publish 与 Close 并发时，Close 拒绝新操作并等待已接收的发布直到 deadline；超时发布结果仍可能不确定。Client 对外不暴露底层 Kafka 客户端。
- `NewClient` 接收运行时 `Dialer`、SASL token provider、TracerProvider、MeterProvider 等对象时必须排除序列化；只关闭明确由 adapter 创建并拥有的 client/provider，不关闭应用级 OpenTelemetry Provider。

## 10. 安全与可观测性

- TLS 默认执行 broker 证书校验；禁用校验需要显式配置。SASL 机制、用户名、密码、token 与证书私钥不得写入错误、日志、span 或 metric 属性。
- Broker address 允许作为受控配置，但错误脱敏时必须去除 URL userinfo 和任何认证参数。
- Trace 传播 headers 只在应用通过已注入的 OpenTelemetry propagator 明确启用时映射；首期不擅自从任意业务 header 提取 span context，也不覆盖调用方 headers。
- 指标关注 produce 成功/失败/确认延迟、consume record/handler 结果、offset commit 成功/失败、rebalance 次数、在途 handler 和重试/DLQ 次数。metric labels 只允许逻辑 destination、操作、有限结果类别；禁止用 topic partition/offset、message ID、key、任意错误文本或 header 值作为高基数标签。
- Span 记录 broker 类型、逻辑 destination、topic、操作结果和耗时；不记录 body、key、ID、完整 headers、凭据或用户敏感 routing 信息。
- 应用 bootstrap 创建并拥有 OpenTelemetry Provider；Kafka adapter 只注册 instrumentation，Close 不得关闭应用级 Provider。

## 11. 测试与验收

### 单元测试

- 配置覆盖空 broker、非法地址、route/topic 无效、重复/保留 header、错误 SASL/TLS 组合、负超时/重试、缺 consumer group 和无效 DLQ route。
- Producer fake 覆盖 record 字段映射、headers 顺序/重复、message ID 保留、深拷贝、acks/timeout 错误链、ctx 取消和最大大小检查。
- Consumer fake 或可替换 poll/commit 层覆盖 handler 成功后提交 `offset+1`、失败不提交、commit 失败、仅提交连续成功 offset、partition 间并发、同 partition 顺序、重复 Consume 和 Close 取消。
- 覆盖 revoke 时 handler context 取消、旧 generation 不提交、成功 DLQ 后 commit 失败导致可重复 DLQ 的语义、失败 DLQ 不推进源 offset。
- 覆盖 Close deadline 后资源最终释放、Publish 与 Close 并发、所有 goroutine 退出和 race。

### 集成测试

- 用专用临时 Kafka broker/container 验证 producer acknowledgement、key 分区亲和、header round trip、consumer group 重新分配、手动 commit、未提交重投、DLQ 和 TLS/SASL 可选路径。
- 外部 broker 集成测试通过 `MQ_KAFKA_BROKERS` 等显式环境变量选择性运行；使用随机 topic/group 前缀或隔离测试集群，不消费或删除应用已有 topic，不执行全局清理。
- 故障注入覆盖 broker 断开、coordinator 迁移、rebalance 时 handler 未完成、commit 超时和 DLQ publish/commit 的部分成功。
- 验收运行 `go test ./core/mq/...`、`go test -race ./core/mq/...`、`go vet ./core/mq/...`、`go build ./core/mq/...`；修改共享连接/生命周期时扩展至全仓 race。

## 12. 首期实现取舍与后续工作

### 首期实现取舍

- 通过 `Dialer` 注入本包的 `Transport` 边界，使消息映射、发布、offset 提交和生命周期可在不启动 broker 的情况下测试。
- 消费 poll loop 使用有界的分区队列；单分区单 worker 保序，不同分区通过全局并发上限并行。每条成功消息同步提交其 `offset + 1`，不批量越过未完成记录。
- 首期实现 `stop`、有限 `retry`、显式 DLQ 和显式允许的 `drop`；DLQ 发布成功后才提交源 offset。诊断 header 不写入任意 handler 错误文本。
- franz-go revoke/lost callback 先取消并移除对应 partition worker，再等待该 partition 的在途 commit 结束。worker 在 commit 前重新核对 assignment，并持有独立 commit 锁；已在途 handler 和 transport commit 必须响应 context 取消。
- poll 返回记录及 fetch error 时，先分发有效记录；可恢复 group session、data-loss 通知及 broker retriable error 经过短退避后继续 poll。终止性 poll error 则等待本轮已入队记录完成或 assignment 被撤销后再返回。
- `NewClient` 使用 franz-go 的 lazy client 构造，不主动拨号或请求 metadata。Kafka record 零 timestamp 由 producer 写入当前时间。
- 由于公共 route 结构没有 publish/consume 角色字段，配置 routes 均视为可消费 route，因而要求配置 consumer group ID。
- 首期未接入指标、trace 注入、OAuth token refresh 和外部 Kafka broker 集成测试；这些能力需要结合应用现有 observability 装配和部署目标另行验收。

后续实施时需要明确部署环境的最低 Kafka/兼容 broker 版本、OAuth 是否必要，以及 trace context 传播由应用 propagator 还是 adapter 承担。应用 bootstrap 还需决定是否自动构造和关闭本 adapter；首期目前由调用方显式提供完整 `Config` 并管理 `Client.Close`。
