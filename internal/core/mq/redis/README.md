# Redis Streams MQ

`internal/core/mq/redis` adapts the public `internal/core/mq` contract to Redis Streams (Redis 6.2+ when the default pending-message reclaim is enabled). It reuses an injected `github.com/redis/go-redis/v9` client; connection topology, TLS, pooling and OpenTelemetry belong to `internal/core/redis`.

```go
client, err := mqredis.NewClient(&mqredis.Config{
    Client:   redisClient,
    Group:    "orders",
    Consumer: "worker-1",
    ClaimIdle: 30 * time.Second,
})
if err != nil {
    return err
}
defer client.Close(context.Background())

err = client.Publish(ctx, "orders.created", mq.Message{
    ID:   "order-event-123",
    Body: payload,
})
```

The destination is the Redis stream key. The adapter stores one JSON envelope in a reserved `__mq_payload` stream field so arbitrary bytes and ordered duplicate headers survive the Redis round trip. The caller-provided message ID is preserved; if it is empty, the Redis stream entry ID is used when consuming.

Consumers use a Redis consumer group. The group is created with `XGROUP CREATE ... MKSTREAM` on the first `Consume` call; `StartID` defaults to `0-0`, so a new group receives existing entries as well as new entries. Set `StartID` to `$` when the group should receive only messages added after group creation. Successful handlers are acknowledged with `XACK`. Handler errors leave entries pending; reclaim defaults to 30 seconds and uses paginated `XAUTOCLAIM` scans to recover pending entries idle for at least that duration. Reclaim requires Redis 6.2 or newer. Set `DisableClaim: true` only when pending recovery is handled operationally.

`Close` cancels an active consumer and waits for active publish/consume operations. When it is called from the handler using the handler context, it triggers cancellation without self-waiting; an owned Redis client is closed after the consumer exits. The injected Redis client is borrowed by default and is not closed. Set `OwnClient` only when ownership of the injected client is explicitly transferred to this adapter.

This adapter currently does not implement a separate dead-letter stream, explicit retry count, batch publish, or broker transactions. Pending entries remain available for reclaim or operational inspection.
