package redis

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// Mode 表示 Redis 部署拓扑。
type Mode string

const (
	ModeStandalone Mode = "standalone"
	ModeSentinel   Mode = "sentinel"
	ModeCluster    Mode = "cluster"
)

// Config 是可序列化的 Redis 客户端配置。运行时 Provider 不参与配置文件序列化。
type Config struct {
	Mode            Mode              `json:"mode" yaml:"mode" mapstructure:"mode"`
	Addrs           []string          `json:"addrs" yaml:"addrs" mapstructure:"addrs"`
	Network         string            `json:"network" yaml:"network" mapstructure:"network"`
	DB              int               `json:"db" yaml:"db" mapstructure:"db"`
	Username        string            `json:"username" yaml:"username" mapstructure:"username"`
	Password        string            `json:"password" yaml:"password" mapstructure:"password"`
	ClientName      string            `json:"client_name" yaml:"client_name" mapstructure:"client_name"`
	Protocol        int               `json:"protocol" yaml:"protocol" mapstructure:"protocol"`
	DisableIdentity bool              `json:"disable_identity" yaml:"disable_identity" mapstructure:"disable_identity"`
	IdentitySuffix  string            `json:"identity_suffix" yaml:"identity_suffix" mapstructure:"identity_suffix"`
	Timeout         *TimeoutConfig    `json:"timeout" yaml:"timeout" mapstructure:"timeout"`
	Retry           *RetryConfig      `json:"retry" yaml:"retry" mapstructure:"retry"`
	Pool            *PoolConfig       `json:"pool" yaml:"pool" mapstructure:"pool"`
	Buffer          *BufferConfig     `json:"buffer" yaml:"buffer" mapstructure:"buffer"`
	TLS             *TLSConfig        `json:"tls" yaml:"tls" mapstructure:"tls"`
	Monitoring      *MonitoringConfig `json:"monitoring" yaml:"monitoring" mapstructure:"monitoring"`
	Sentinel        *SentinelConfig   `json:"sentinel" yaml:"sentinel" mapstructure:"sentinel"`
	Cluster         *ClusterConfig    `json:"cluster" yaml:"cluster" mapstructure:"cluster"`
}

// Validate 只执行确定性的本地校验，不解析 DNS 或探测 Redis 服务。
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("redis config is nil")
	}
	var errs []error
	mode := c.modeOrDefault()
	network := c.networkOrDefault()
	if !validMode(mode) {
		errs = append(errs, fmt.Errorf("redis mode must be one of %q, %q, %q", ModeStandalone, ModeSentinel, ModeCluster))
	}
	if !validNetwork(network) {
		errs = append(errs, errors.New("redis network must be tcp, tcp4, tcp6, or unix"))
	}
	if len(c.Addrs) == 0 {
		errs = append(errs, errors.New("redis addrs must not be empty"))
	}
	for i, addr := range c.Addrs {
		if err := validateAddr(network, addr); err != nil {
			errs = append(errs, fmt.Errorf("redis addrs[%d]: %w", i, err))
		}
	}
	if mode == ModeStandalone && len(c.Addrs) != 1 {
		errs = append(errs, errors.New("redis standalone mode requires exactly one addr"))
	}
	if network == "unix" && mode != ModeStandalone {
		errs = append(errs, errors.New("redis unix network is only supported in standalone mode"))
	}
	if mode != ModeStandalone && network != "" && network != "tcp" {
		errs = append(errs, errors.New("redis sentinel and cluster modes only support tcp network"))
	}
	if c.DB < 0 {
		errs = append(errs, errors.New("redis db must be greater than or equal to 0"))
	}
	if mode == ModeCluster && c.DB != 0 {
		errs = append(errs, errors.New("redis cluster mode requires db to be 0"))
	}
	if c.Protocol != 0 && c.Protocol != 2 && c.Protocol != 3 {
		errs = append(errs, errors.New("redis protocol must be 0, 2, or 3"))
	}
	if c.Timeout != nil {
		errs = append(errs, c.Timeout.validate()...)
	}
	if c.Retry != nil {
		errs = append(errs, c.Retry.validate()...)
	}
	if c.Pool != nil {
		errs = append(errs, c.Pool.validate()...)
	}
	if c.Buffer != nil {
		errs = append(errs, c.Buffer.validate()...)
	}
	if c.TLS != nil {
		errs = append(errs, c.TLS.validate()...)
	}
	if c.Monitoring != nil {
		errs = append(errs, c.Monitoring.validate()...)
	}
	switch mode {
	case ModeStandalone:
		if c.Sentinel != nil || c.Cluster != nil {
			errs = append(errs, errors.New("redis standalone mode does not accept sentinel or cluster config"))
		}
	case ModeSentinel:
		if c.Cluster != nil {
			errs = append(errs, errors.New("redis sentinel mode does not accept cluster config"))
		}
		if c.Sentinel == nil {
			errs = append(errs, errors.New("redis sentinel mode requires sentinel config"))
		} else {
			errs = append(errs, c.Sentinel.validate()...)
		}
	case ModeCluster:
		if c.Sentinel != nil {
			errs = append(errs, errors.New("redis cluster mode does not accept sentinel config"))
		}
		if c.Cluster != nil {
			errs = append(errs, c.Cluster.validate()...)
		}
	}
	return errors.Join(errs...)
}

func (c *Config) modeOrDefault() Mode {
	if c.Mode == "" {
		return ModeStandalone
	}
	return c.Mode
}

func (c *Config) networkOrDefault() string {
	if c.Network == "" {
		return "tcp"
	}
	return c.Network
}

func validMode(mode Mode) bool {
	return mode == ModeStandalone || mode == ModeSentinel || mode == ModeCluster
}

func validNetwork(network string) bool {
	switch network {
	case "tcp", "tcp4", "tcp6", "unix":
		return true
	default:
		return false
	}
}

func validateAddr(network, addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return errors.New("address must not be empty")
	}
	if network == "unix" {
		return nil
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("address must be host:port: %w", err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("port must be a number between 1 and 65535")
	}
	return nil
}

type TimeoutConfig struct {
	DialTimeout           time.Duration `json:"dial_timeout" yaml:"dial_timeout" mapstructure:"dial_timeout"`
	DialerRetries         int           `json:"dialer_retries" yaml:"dialer_retries" mapstructure:"dialer_retries"`
	DialerRetryTimeout    time.Duration `json:"dialer_retry_timeout" yaml:"dialer_retry_timeout" mapstructure:"dialer_retry_timeout"`
	ReadTimeout           time.Duration `json:"read_timeout" yaml:"read_timeout" mapstructure:"read_timeout"`
	WriteTimeout          time.Duration `json:"write_timeout" yaml:"write_timeout" mapstructure:"write_timeout"`
	PoolTimeout           time.Duration `json:"pool_timeout" yaml:"pool_timeout" mapstructure:"pool_timeout"`
	ContextTimeoutEnabled bool          `json:"context_timeout_enabled" yaml:"context_timeout_enabled" mapstructure:"context_timeout_enabled"`
}

func (c *TimeoutConfig) validate() []error {
	var errs []error
	if c.DialTimeout < 0 || c.DialerRetries < 0 || c.DialerRetryTimeout < 0 || c.PoolTimeout < 0 {
		errs = append(errs, errors.New("redis timeout values must be non-negative"))
	}
	if !validSocketTimeout(c.ReadTimeout) || !validSocketTimeout(c.WriteTimeout) {
		errs = append(errs, errors.New("redis read and write timeout must be -2, -1, or non-negative"))
	}
	return errs
}

func validSocketTimeout(v time.Duration) bool { return v >= 0 || v == -1 || v == -2 }

type RetryConfig struct {
	MaxRetries      int           `json:"max_retries" yaml:"max_retries" mapstructure:"max_retries"`
	MinRetryBackoff time.Duration `json:"min_retry_backoff" yaml:"min_retry_backoff" mapstructure:"min_retry_backoff"`
	MaxRetryBackoff time.Duration `json:"max_retry_backoff" yaml:"max_retry_backoff" mapstructure:"max_retry_backoff"`
}

func (c *RetryConfig) validate() []error {
	var errs []error
	if c.MaxRetries < -1 || c.MinRetryBackoff < -1 || c.MaxRetryBackoff < -1 {
		errs = append(errs, errors.New("redis retry values must be -1 or greater"))
	}
	if c.MinRetryBackoff >= 0 && c.MaxRetryBackoff >= 0 && c.MinRetryBackoff > c.MaxRetryBackoff {
		errs = append(errs, errors.New("redis retry.min_retry_backoff must not exceed max_retry_backoff"))
	}
	return errs
}

type PoolConfig struct {
	FIFO                  bool          `json:"fifo" yaml:"fifo" mapstructure:"fifo"`
	Size                  int           `json:"size" yaml:"size" mapstructure:"size"`
	MaxConcurrentDials    int           `json:"max_concurrent_dials" yaml:"max_concurrent_dials" mapstructure:"max_concurrent_dials"`
	MinIdleConns          int           `json:"min_idle_conns" yaml:"min_idle_conns" mapstructure:"min_idle_conns"`
	MaxIdleConns          int           `json:"max_idle_conns" yaml:"max_idle_conns" mapstructure:"max_idle_conns"`
	MaxActiveConns        int           `json:"max_active_conns" yaml:"max_active_conns" mapstructure:"max_active_conns"`
	ConnMaxIdleTime       time.Duration `json:"conn_max_idle_time" yaml:"conn_max_idle_time" mapstructure:"conn_max_idle_time"`
	ConnMaxLifetime       time.Duration `json:"conn_max_lifetime" yaml:"conn_max_lifetime" mapstructure:"conn_max_lifetime"`
	ConnMaxLifetimeJitter time.Duration `json:"conn_max_lifetime_jitter" yaml:"conn_max_lifetime_jitter" mapstructure:"conn_max_lifetime_jitter"`
}

// BufferConfig controls the per-connection read and write buffers.
// Zero values keep go-redis defaults.
type BufferConfig struct {
	ReadBufferSize  int `json:"read_buffer_size" yaml:"read_buffer_size" mapstructure:"read_buffer_size"`
	WriteBufferSize int `json:"write_buffer_size" yaml:"write_buffer_size" mapstructure:"write_buffer_size"`
}

func (c *BufferConfig) validate() []error {
	var errs []error
	if c.ReadBufferSize < 0 {
		errs = append(errs, errors.New("redis buffer.read_buffer_size must be non-negative"))
	}
	if c.WriteBufferSize < 0 {
		errs = append(errs, errors.New("redis buffer.write_buffer_size must be non-negative"))
	}
	return errs
}

func (c *PoolConfig) validate() []error {
	var errs []error
	if c.Size < 0 || c.MaxConcurrentDials < 0 || c.MinIdleConns < 0 || c.MaxIdleConns < 0 || c.MaxActiveConns < 0 {
		errs = append(errs, errors.New("redis pool sizes must be non-negative"))
	}
	if c.MaxIdleConns > 0 && c.MinIdleConns > c.MaxIdleConns {
		errs = append(errs, errors.New("redis pool.min_idle_conns must not exceed max_idle_conns"))
	}
	if c.MaxActiveConns > 0 && c.MinIdleConns > c.MaxActiveConns {
		errs = append(errs, errors.New("redis pool.min_idle_conns must not exceed max_active_conns"))
	}
	if c.ConnMaxIdleTime < 0 || c.ConnMaxLifetime < 0 || c.ConnMaxLifetimeJitter < 0 {
		errs = append(errs, errors.New("redis pool connection durations must be non-negative"))
	}
	if c.ConnMaxLifetime == 0 && c.ConnMaxLifetimeJitter > 0 {
		errs = append(errs, errors.New("redis pool.conn_max_lifetime_jitter requires conn_max_lifetime"))
	}
	return errs
}

type TLSConfig struct {
	Enabled            bool   `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
	ServerName         string `json:"server_name" yaml:"server_name" mapstructure:"server_name"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify" yaml:"insecure_skip_verify" mapstructure:"insecure_skip_verify"`
	CAFile             string `json:"ca_file" yaml:"ca_file" mapstructure:"ca_file"`
	CertFile           string `json:"cert_file" yaml:"cert_file" mapstructure:"cert_file"`
	KeyFile            string `json:"key_file" yaml:"key_file" mapstructure:"key_file"`
}

func (c *TLSConfig) validate() []error {
	if !c.Enabled && (c.CAFile != "" || c.CertFile != "" || c.KeyFile != "" || c.ServerName != "" || c.InsecureSkipVerify) {
		return []error{errors.New("redis tls options require tls.enabled")}
	}
	if (c.CertFile == "") != (c.KeyFile == "") {
		return []error{errors.New("redis tls.cert_file and tls.key_file must be set together")}
	}
	return nil
}

type MonitoringConfig struct {
	TracerProvider     trace.TracerProvider `json:"-" yaml:"-" mapstructure:"-"`
	MeterProvider      metric.MeterProvider `json:"-" yaml:"-" mapstructure:"-"`
	TracingEnabled     bool                 `json:"tracing_enabled" yaml:"tracing_enabled" mapstructure:"tracing_enabled"`
	DBStatementEnabled bool                 `json:"db_statement_enabled" yaml:"db_statement_enabled" mapstructure:"db_statement_enabled"`
	CallerEnabled      bool                 `json:"caller_enabled" yaml:"caller_enabled" mapstructure:"caller_enabled"`
	DialEnabled        bool                 `json:"dial_enabled" yaml:"dial_enabled" mapstructure:"dial_enabled"`
	MetricsEnabled     bool                 `json:"metrics_enabled" yaml:"metrics_enabled" mapstructure:"metrics_enabled"`
}

func (*MonitoringConfig) validate() []error { return nil }

type SentinelConfig struct {
	MasterName              string `json:"master_name" yaml:"master_name" mapstructure:"master_name"`
	Username                string `json:"username" yaml:"username" mapstructure:"username"`
	Password                string `json:"password" yaml:"password" mapstructure:"password"`
	ReplicaOnly             bool   `json:"replica_only" yaml:"replica_only" mapstructure:"replica_only"`
	UseDisconnectedReplicas bool   `json:"use_disconnected_replicas" yaml:"use_disconnected_replicas" mapstructure:"use_disconnected_replicas"`
}

func (c *SentinelConfig) validate() []error {
	if strings.TrimSpace(c.MasterName) == "" {
		return []error{errors.New("redis sentinel.master_name must not be empty")}
	}
	return nil
}

type ClusterConfig struct {
	MaxRedirects               int           `json:"max_redirects" yaml:"max_redirects" mapstructure:"max_redirects"`
	ReadOnly                   bool          `json:"read_only" yaml:"read_only" mapstructure:"read_only"`
	RouteByLatency             bool          `json:"route_by_latency" yaml:"route_by_latency" mapstructure:"route_by_latency"`
	RouteRandomly              bool          `json:"route_randomly" yaml:"route_randomly" mapstructure:"route_randomly"`
	FailingTimeoutSeconds      int           `json:"failing_timeout_seconds" yaml:"failing_timeout_seconds" mapstructure:"failing_timeout_seconds"`
	ClusterStateReloadInterval time.Duration `json:"cluster_state_reload_interval" yaml:"cluster_state_reload_interval" mapstructure:"cluster_state_reload_interval"`
}

func (c *ClusterConfig) validate() []error {
	var errs []error
	if c.MaxRedirects < -1 || c.FailingTimeoutSeconds < 0 || c.ClusterStateReloadInterval < 0 {
		errs = append(errs, errors.New("redis cluster options are outside supported ranges"))
	}
	if c.RouteByLatency && c.RouteRandomly {
		errs = append(errs, errors.New("redis cluster route_by_latency and route_randomly are mutually exclusive"))
	}
	return errs
}
