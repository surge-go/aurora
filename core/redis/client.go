package redis

import (
	"crypto/tls"
	"fmt"
	"strings"
	"sync"

	goredis "github.com/redis/go-redis/v9"
)

// NewClient 创建并配置 Redis 客户端。构造过程不会连接或探测 Redis 服务。
func NewClient(cfg *Config) (goredis.UniversalClient, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	normalized := *cfg
	normalized.Addrs = make([]string, len(cfg.Addrs))
	for i, addr := range cfg.Addrs {
		normalized.Addrs[i] = strings.TrimSpace(addr)
	}
	tlsConfig, err := buildTLSConfig(cfg.TLS)
	if err != nil {
		return nil, fmt.Errorf("redis build tls config: %w", err)
	}

	var client goredis.UniversalClient
	switch cfg.modeOrDefault() {
	case ModeStandalone:
		client = newStandaloneClient(&normalized, tlsConfig)
	case ModeSentinel:
		client = newSentinelClient(&normalized, tlsConfig)
	case ModeCluster:
		client = newClusterClient(&normalized, tlsConfig)
	default:
		return nil, fmt.Errorf("redis unsupported mode %q", cfg.Mode)
	}

	releaseMetrics, err := instrumentClient(client, cfg.Monitoring)
	if err != nil {
		if releaseMetrics != nil {
			releaseMetrics()
		}
		_ = client.Close()
		return nil, err
	}
	if releaseMetrics == nil {
		return client, nil
	}
	return &managedClient{UniversalClient: client, releaseMetrics: releaseMetrics}, nil
}

type managedClient struct {
	goredis.UniversalClient
	releaseMetrics func()
	closeOnce      sync.Once
	closeErr       error
}

func (c *managedClient) Close() error {
	c.closeOnce.Do(func() {
		if c.releaseMetrics != nil {
			c.releaseMetrics()
		}
		c.closeErr = c.UniversalClient.Close()
	})
	return c.closeErr
}

func newStandaloneClient(cfg *Config, tlsConfigOption *tls.Config) goredis.UniversalClient {
	opt := &goredis.Options{
		Network:         cfg.networkOrDefault(),
		Addr:            cfg.Addrs[0],
		DB:              cfg.DB,
		Username:        cfg.Username,
		Password:        cfg.Password,
		ClientName:      cfg.ClientName,
		Protocol:        cfg.Protocol,
		DisableIdentity: cfg.DisableIdentity,
		IdentitySuffix:  cfg.IdentitySuffix,
		TLSConfig:       tlsConfigOption,
	}
	applyTimeout(opt, cfg.Timeout)
	applyRetry(opt, cfg.Retry)
	applyPool(opt, cfg.Pool)
	applyBuffer(&opt.ReadBufferSize, &opt.WriteBufferSize, cfg.Buffer)
	return goredis.NewClient(opt)
}

func newSentinelClient(cfg *Config, tlsConfigOption *tls.Config) goredis.UniversalClient {
	opt := &goredis.FailoverOptions{
		MasterName:              cfg.Sentinel.MasterName,
		SentinelAddrs:           cfg.Addrs,
		SentinelUsername:        cfg.Sentinel.Username,
		SentinelPassword:        cfg.Sentinel.Password,
		DB:                      cfg.DB,
		Username:                cfg.Username,
		Password:                cfg.Password,
		ClientName:              cfg.ClientName,
		Protocol:                cfg.Protocol,
		DisableIdentity:         cfg.DisableIdentity,
		IdentitySuffix:          cfg.IdentitySuffix,
		ReplicaOnly:             cfg.Sentinel.ReplicaOnly,
		UseDisconnectedReplicas: cfg.Sentinel.UseDisconnectedReplicas,
		TLSConfig:               tlsConfigOption,
	}
	applyFailoverTimeout(opt, cfg.Timeout)
	applyFailoverRetry(opt, cfg.Retry)
	applyFailoverPool(opt, cfg.Pool)
	applyBuffer(&opt.ReadBufferSize, &opt.WriteBufferSize, cfg.Buffer)
	return goredis.NewFailoverClient(opt)
}

func newClusterClient(cfg *Config, tlsConfigOption *tls.Config) goredis.UniversalClient {
	cluster := cfg.Cluster
	if cluster == nil {
		cluster = &ClusterConfig{}
	}
	opt := &goredis.ClusterOptions{
		Addrs:                      cfg.Addrs,
		Username:                   cfg.Username,
		Password:                   cfg.Password,
		ClientName:                 cfg.ClientName,
		Protocol:                   cfg.Protocol,
		DisableIdentity:            cfg.DisableIdentity,
		IdentitySuffix:             cfg.IdentitySuffix,
		MaxRedirects:               cluster.MaxRedirects,
		ReadOnly:                   cluster.ReadOnly,
		RouteByLatency:             cluster.RouteByLatency,
		RouteRandomly:              cluster.RouteRandomly,
		FailingTimeoutSeconds:      cluster.FailingTimeoutSeconds,
		ClusterStateReloadInterval: cluster.ClusterStateReloadInterval,
		TLSConfig:                  tlsConfigOption,
	}
	applyClusterTimeout(opt, cfg.Timeout)
	applyClusterRetry(opt, cfg.Retry)
	applyClusterPool(opt, cfg.Pool)
	applyBuffer(&opt.ReadBufferSize, &opt.WriteBufferSize, cfg.Buffer)
	return goredis.NewClusterClient(opt)
}

func applyTimeout(opt *goredis.Options, cfg *TimeoutConfig) {
	if cfg == nil {
		return
	}
	opt.DialTimeout = cfg.DialTimeout
	opt.DialerRetries = cfg.DialerRetries
	opt.DialerRetryTimeout = cfg.DialerRetryTimeout
	opt.ReadTimeout = cfg.ReadTimeout
	opt.WriteTimeout = cfg.WriteTimeout
	opt.PoolTimeout = cfg.PoolTimeout
	opt.ContextTimeoutEnabled = cfg.ContextTimeoutEnabled
}

func applyRetry(opt *goredis.Options, cfg *RetryConfig) {
	if cfg == nil {
		return
	}
	opt.MaxRetries = cfg.MaxRetries
	opt.MinRetryBackoff = cfg.MinRetryBackoff
	opt.MaxRetryBackoff = cfg.MaxRetryBackoff
}

func applyPool(opt *goredis.Options, cfg *PoolConfig) {
	if cfg == nil {
		return
	}
	opt.PoolFIFO = cfg.FIFO
	opt.PoolSize = cfg.Size
	opt.MaxConcurrentDials = cfg.MaxConcurrentDials
	opt.MinIdleConns = cfg.MinIdleConns
	opt.MaxIdleConns = cfg.MaxIdleConns
	opt.MaxActiveConns = cfg.MaxActiveConns
	opt.ConnMaxIdleTime = cfg.ConnMaxIdleTime
	opt.ConnMaxLifetime = cfg.ConnMaxLifetime
	opt.ConnMaxLifetimeJitter = cfg.ConnMaxLifetimeJitter
}

func applyBuffer(readBuffer, writeBuffer *int, cfg *BufferConfig) {
	if cfg == nil {
		return
	}
	*readBuffer = cfg.ReadBufferSize
	*writeBuffer = cfg.WriteBufferSize
}

func applyFailoverTimeout(opt *goredis.FailoverOptions, cfg *TimeoutConfig) {
	if cfg == nil {
		return
	}
	opt.DialTimeout = cfg.DialTimeout
	opt.DialerRetries = cfg.DialerRetries
	opt.DialerRetryTimeout = cfg.DialerRetryTimeout
	opt.ReadTimeout = cfg.ReadTimeout
	opt.WriteTimeout = cfg.WriteTimeout
	opt.PoolTimeout = cfg.PoolTimeout
	opt.ContextTimeoutEnabled = cfg.ContextTimeoutEnabled
}

func applyFailoverRetry(opt *goredis.FailoverOptions, cfg *RetryConfig) {
	if cfg == nil {
		return
	}
	opt.MaxRetries = cfg.MaxRetries
	opt.MinRetryBackoff = cfg.MinRetryBackoff
	opt.MaxRetryBackoff = cfg.MaxRetryBackoff
}

func applyFailoverPool(opt *goredis.FailoverOptions, cfg *PoolConfig) {
	if cfg == nil {
		return
	}
	opt.PoolFIFO = cfg.FIFO
	opt.PoolSize = cfg.Size
	opt.MaxConcurrentDials = cfg.MaxConcurrentDials
	opt.MinIdleConns = cfg.MinIdleConns
	opt.MaxIdleConns = cfg.MaxIdleConns
	opt.MaxActiveConns = cfg.MaxActiveConns
	opt.ConnMaxIdleTime = cfg.ConnMaxIdleTime
	opt.ConnMaxLifetime = cfg.ConnMaxLifetime
	opt.ConnMaxLifetimeJitter = cfg.ConnMaxLifetimeJitter
}

func applyClusterTimeout(opt *goredis.ClusterOptions, cfg *TimeoutConfig) {
	if cfg == nil {
		return
	}
	opt.DialTimeout = cfg.DialTimeout
	opt.DialerRetries = cfg.DialerRetries
	opt.DialerRetryTimeout = cfg.DialerRetryTimeout
	opt.ReadTimeout = cfg.ReadTimeout
	opt.WriteTimeout = cfg.WriteTimeout
	opt.PoolTimeout = cfg.PoolTimeout
	opt.ContextTimeoutEnabled = cfg.ContextTimeoutEnabled
}

func applyClusterRetry(opt *goredis.ClusterOptions, cfg *RetryConfig) {
	if cfg == nil {
		return
	}
	opt.MaxRetries = cfg.MaxRetries
	opt.MinRetryBackoff = cfg.MinRetryBackoff
	opt.MaxRetryBackoff = cfg.MaxRetryBackoff
}

func applyClusterPool(opt *goredis.ClusterOptions, cfg *PoolConfig) {
	if cfg == nil {
		return
	}
	opt.PoolFIFO = cfg.FIFO
	opt.PoolSize = cfg.Size
	opt.MaxConcurrentDials = cfg.MaxConcurrentDials
	opt.MinIdleConns = cfg.MinIdleConns
	opt.MaxIdleConns = cfg.MaxIdleConns
	opt.MaxActiveConns = cfg.MaxActiveConns
	opt.ConnMaxIdleTime = cfg.ConnMaxIdleTime
	opt.ConnMaxLifetime = cfg.ConnMaxLifetime
	opt.ConnMaxLifetimeJitter = cfg.ConnMaxLifetimeJitter
}
