# Kafka MQ adapter

`kafka.Client` implements the broker-independent `mq.Publisher` and `mq.Consumer` contracts with `franz-go`. A logical destination must be present in `Config.Routes`; its route selects the Kafka topic.

Publishing waits for the broker result with `acks=all` and idempotent produce enabled. A timeout or connection error can leave acceptance uncertain, so applications should use stable message IDs and make retried operations idempotent. The producer writes a zero timestamp as the current producer time; broker `LogAppendTime` topic policy may replace it.

Consumption requires a group ID. New groups start at `earliest` unless `Consumer.StartOffset` is set to `latest`; existing committed offsets take precedence. Each partition is processed in order, different partitions may run concurrently up to `Consumer.Concurrency`, and a successful handler is followed by a synchronous commit of that record's next offset. Delivery is at least once. The default `stop` failure mode stops consumption without committing the failed record. Bounded `retry`, `dead_letter`, and explicitly enabled data-loss `drop` modes are also available.

TLS validates broker certificates by default. SASL supports PLAIN, SCRAM-SHA-256, and SCRAM-SHA-512. `NewClient` validates and creates the client without connecting to brokers. The adapter owns its Kafka transport and closes it through `Close`.

```go
client, err := kafka.NewClient(&kafka.Config{
	Brokers: []string{"kafka-1:9092", "kafka-2:9092"},
	ClientID: "orders-service",
	Routes: map[string]kafka.RouteConfig{
		"orders.created": {Topic: "orders.created.v1"},
	},
	Consumer: kafka.ConsumerConfig{GroupID: "order-indexer"},
})
```

Kafka topics, replication, `min.insync.replicas`, ACLs, and broker authentication policies remain deployment responsibilities. This adapter does not provide exactly-once processing or coordinate Kafka offsets with application transactions.
