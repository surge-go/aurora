# RabbitMQ MQ adapter

The package implements `internal/core/mq` with RabbitMQ AMQP 0.9.1 through
`github.com/rabbitmq/amqp091-go`. A logical destination is resolved through
`Config.Routes`; it is never treated as an arbitrary queue name.

```go
client, err := rabbitmq.NewClient(&rabbitmq.Config{
	URL: "amqp://user:password@rabbitmq:5672/app",
	DeclareTopology: true,
	Failure: rabbitmq.FailureConfig{Mode: rabbitmq.FailureReject},
	DeadLetter: rabbitmq.DeadLetterConfig{Exchange: "orders.dlx"},
	Routes: map[string]rabbitmq.RouteConfig{
		"orders": {
			Exchange: "orders",
			ExchangeType: "topic",
			Queue: "orders.worker",
			RoutingKey: "order.created",
			Bindings: []rabbitmq.BindingConfig{{RoutingKey: "order.created"}},
		},
	},
})
if err != nil {
	return err
}
defer client.Close(context.Background())
```

Publishing uses a dedicated confirm channel and mandatory routing. A publish
returns an error when the broker nacks it, the publish is returned, the channel
fails, or the confirmation deadline expires. A timeout or connection failure
means acceptance can be uncertain; callers should use a stable message ID and
idempotent handling when retrying.

Consumption uses manual acknowledgements, bounded prefetch and configurable
handler concurrency. A successful handler is acknowledged. The default failure
mode for consumption is `FailureReject`, which requires a configured
dead-letter exchange.
`FailureRequeue` must be selected explicitly and should only be used when the
caller accepts RabbitMQ redelivery semantics. Rejected messages use
`requeue=false`, allowing a broker-configured DLX to receive them. The adapter
does not silently discard rejected messages unless `AllowDrop` is explicitly
enabled.

`Message.Headers` and `Message.Key` are encoded in versioned reserved headers,
which preserves duplicate header keys, ordering and binary values. These
reserved names must not be used by application code.

`NewClient` performs local validation only. The client owns connections and
channels created by its dialer. A custom `Config.Dialer` is useful for tests or
for a transport wrapper; the returned connection is still closed by `Client`.
