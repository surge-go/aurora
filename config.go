package aurora

import (
	"errors"
	"time"
)

// Config 是 Aurora Web 服务器的配置。
//
// 超时和大小字段直接对应 net/http 的服务器配置；MaxBodyBytes 属于
// Aurora 请求处理中间件，因此不会传给 http.Server。
type Config struct {
	// Addr 是服务器监听地址，例如 ":8080" 或 "127.0.0.1:8080"
	Addr string `json:"addr" yaml:"addr" mapstructure:"addr"`

	// Mode 是 Gin 的运行模式：debug、release 或 test
	Mode string `json:"mode" yaml:"mode" mapstructure:"mode"`

	// ReadTimeout 是读取整个请求的超时时间（包括 body）
	ReadTimeout time.Duration `json:"read_timeout" yaml:"read_timeout" mapstructure:"read_timeout"`

	// ReadHeaderTimeout 是读取请求头的超时时间
	ReadHeaderTimeout time.Duration `json:"read_header_timeout" yaml:"read_header_timeout" mapstructure:"read_header_timeout"`

	// WriteTimeout 是写入响应的超时时间
	WriteTimeout time.Duration `json:"write_timeout" yaml:"write_timeout" mapstructure:"write_timeout"`

	// IdleTimeout 是保持连接的空闲超时时间
	IdleTimeout time.Duration `json:"idle_timeout" yaml:"idle_timeout" mapstructure:"idle_timeout"`

	// MaxHeaderBytes 是请求头的最大字节数
	MaxHeaderBytes int `json:"max_header_bytes" yaml:"max_header_bytes" mapstructure:"max_header_bytes"`

	// MaxBodyBytes 限制请求体的最大大小（字节）。
	// 超过此限制的请求会被拒绝并返回 413 Payload Too Large。
	// 默认值：10 MiB
	MaxBodyBytes int64 `json:"max_body_bytes" yaml:"max_body_bytes" mapstructure:"max_body_bytes"`

	// TrustedProxies 是信任的代理 IP 列表
	TrustedProxies []string `json:"trusted_proxies" yaml:"trusted_proxies" mapstructure:"trusted_proxies"`

	// Middleware 是中间件配置
	Middleware MiddlewareConfig `json:"middleware" yaml:"middleware" mapstructure:"middleware"`
}

// MiddlewareConfig 是默认中间件的开关配置。
// 中间件按 middleware.go 中的固定顺序安装，开关只决定是否加入链路，
// 不改变各中间件之间的相对顺序。
type MiddlewareConfig struct {
	// RequestID 是否启用请求 ID 中间件
	RequestID bool `json:"request_id" yaml:"request_id" mapstructure:"request_id"`

	// Recovery 是否启用恢复中间件（捕获 panic）
	Recovery bool `json:"recovery" yaml:"recovery" mapstructure:"recovery"`

	// AccessLog 是否启用访问日志中间件
	AccessLog bool `json:"access_log" yaml:"access_log" mapstructure:"access_log"`

	// Tracing 是否启用链路追踪中间件
	Tracing bool `json:"tracing" yaml:"tracing" mapstructure:"tracing"`

	// Metrics 是否启用 HTTP 指标中间件
	Metrics bool `json:"metrics" yaml:"metrics" mapstructure:"metrics"`
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	// 默认值偏向生产安全：启用请求标识、恢复和访问日志，Tracing/Metrics
	// 因为需要外部运行时 Provider 而默认关闭。
	return Config{
		Addr:              ":8080",
		Mode:              "release",
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,  // 1 MiB
		MaxBodyBytes:      10 << 20, // 10 MiB
		Middleware: MiddlewareConfig{
			RequestID: true,
			Recovery:  true,
			AccessLog: true,
			Tracing:   false,
		},
	}
}

// Validate 验证配置的有效性。
// 只做确定性检查，不监听端口或创建网络连接。
func (c *Config) Validate() error {
	// Validate 只检查本地配置的一致性，不创建 listener、连接外部服务或
	// 修改 Gin 全局状态，因此可以在 New 之前安全调用。
	// 检查地址
	if c.Addr == "" {
		return errors.New("addr cannot be empty")
	}

	// 检查 Gin 模式
	if c.Mode != "debug" && c.Mode != "release" && c.Mode != "test" {
		return errors.New("mode must be debug, release or test")
	}

	// 检查超时时间
	if c.ReadTimeout < 0 {
		return errors.New("read_timeout cannot be negative")
	}
	if c.ReadHeaderTimeout < 0 {
		return errors.New("read_header_timeout cannot be negative")
	}
	if c.WriteTimeout < 0 {
		return errors.New("write_timeout cannot be negative")
	}
	if c.IdleTimeout < 0 {
		return errors.New("idle_timeout cannot be negative")
	}

	// 检查最大请求头大小
	if c.MaxHeaderBytes < 0 {
		return errors.New("max_header_bytes cannot be negative")
	}

	// 检查最大请求体大小
	if c.MaxBodyBytes < 0 {
		return errors.New("max_body_bytes cannot be negative")
	}

	// 检查信任的代理列表
	for _, proxy := range c.TrustedProxies {
		if proxy == "" {
			return errors.New("trusted_proxies cannot contain empty items")
		}
	}

	return nil
}
