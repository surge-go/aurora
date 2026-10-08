# core/redis

基于 `github.com/redis/go-redis/v9` 的 Redis 客户端，支持 Standalone、Sentinel 和 Cluster，
并可选接入 TLS、OpenTelemetry tracing 和 metrics。具体配置边界见 [设计文档](DESIGN.md)。

## Standalone

```go
client, err := redis.NewClient(&redis.Config{
	Addrs: []string{"127.0.0.1:6379"},
	Pool: &redis.PoolConfig{
		Size:         100,
		MinIdleConns: 10,
	},
})
if err != nil {
	return err
}
defer client.Close()

value, err := client.Get(ctx, "cache-key").Result()
```

## Sentinel

```go
client, err := redis.NewClient(&redis.Config{
	Mode:  redis.ModeSentinel,
	Addrs: []string{"sentinel-1:26379", "sentinel-2:26379"},
	Username: "app-user",
	Password: "data-node-password",
	Sentinel: &redis.SentinelConfig{
		MasterName: "primary",
		Username:   "sentinel-user",
		Password:   "sentinel-password",
	},
})
```

`Config.Username/Password` 用于 Redis 数据节点，`Sentinel.Username/Password` 用于哨兵。

需要控制客户端身份上报或缓冲区时，可配置 `DisableIdentity`、`IdentitySuffix` 和
`BufferConfig`；未配置时使用 go-redis 默认值。

## Cluster

```go
client, err := redis.NewClient(&redis.Config{
	Mode:  redis.ModeCluster,
	Addrs: []string{"redis-1:6379", "redis-2:6379", "redis-3:6379"},
	Cluster: &redis.ClusterConfig{
		ReadOnly: true,
	},
})
```

Cluster 的连接池大小按节点生效，整体连接数会随节点数增加。Redis Cluster 只支持 DB 0。

## TLS 与 OpenTelemetry

```go
client, err := redis.NewClient(&redis.Config{
	Addrs: []string{"redis.example.com:6379"},
	TLS: &redis.TLSConfig{
		Enabled:    true,
		ServerName: "redis.example.com",
		CAFile:     "/etc/redis/ca.pem",
	},
	Monitoring: &redis.MonitoringConfig{
		TracingEnabled: true,
		MetricsEnabled: true,
	},
})
```

TLS 默认校验证书。完整 Redis 命令默认不写入 span，避免 key 和参数泄露。TracerProvider、
MeterProvider 与 exporter 由应用统一创建和管理；Provider 可通过 `MonitoringConfig` 注入。
关闭客户端时请调用 `client.Close()`，它会关闭连接池并停止 Redis metrics 采集。
