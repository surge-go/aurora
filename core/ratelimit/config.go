package ratelimit

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Config 定义限流器配置。
type Config struct {
	// KeyPrefix 是 Redis key 前缀，必须包含 hash tag（如 {limiter}）以确保集群环境下的 key 分布。
	KeyPrefix string `json:"key_prefix" yaml:"key_prefix" mapstructure:"key_prefix"`

	// HMACKey 用于对限流 key（如 IP 地址）进行 HMAC-SHA256 摘要，防止 Redis key 泄露原始标识。
	// 必须至少 32 字节。
	HMACKey string `json:"hmac_key" yaml:"hmac_key" mapstructure:"hmac_key"`

	// Limit 是时间窗口内允许的最大请求数。
	Limit int `json:"limit" yaml:"limit" mapstructure:"limit"`

	// Window 是滑动时间窗口大小。
	Window time.Duration `json:"window" yaml:"window" mapstructure:"window"`
}

const (
	defaultKeyPrefix = "aurora:ratelimit:{limiter}"
	defaultLimit     = 100
	defaultWindow    = time.Minute

	minLimit = 1
	maxLimit = 10000
)

// DefaultConfig 返回默认配置。
// HMACKey 必须由应用提供。
func DefaultConfig() Config {
	return Config{
		KeyPrefix: defaultKeyPrefix,
		HMACKey:   "",
		Limit:     defaultLimit,
		Window:    defaultWindow,
	}
}

// withDefaults 应用默认值到零值字段。
func (c Config) withDefaults() Config {
	if c.KeyPrefix == "" {
		c.KeyPrefix = defaultKeyPrefix
	}
	if c.Limit == 0 {
		c.Limit = defaultLimit
	}
	if c.Window == 0 {
		c.Window = defaultWindow
	}
	return c
}

var hashTagPattern = regexp.MustCompile(`\{[a-zA-Z0-9_-]+\}`)

// Validate 验证配置有效性（不应用默认值）。
func (c Config) Validate() error {
	// 验证 KeyPrefix
	if c.KeyPrefix != "" {
		if err := validateKeyPrefix(c.KeyPrefix); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
		}
	}

	// 验证 HMACKey（必填项）
	if len(c.HMACKey) < 32 {
		return fmt.Errorf("%w: hmac_key must be at least 32 bytes", ErrInvalidConfig)
	}

	// 验证 Limit（0 表示使用默认值）
	if c.Limit != 0 && (c.Limit < minLimit || c.Limit > maxLimit) {
		return fmt.Errorf("%w: limit must be between %d and %d", ErrInvalidConfig, minLimit, maxLimit)
	}

	// 验证 Window（0 表示使用默认值，非 0 必须 ≥1 秒）
	if c.Window != 0 && c.Window < time.Second {
		return fmt.Errorf("%w: window must be at least 1 second", ErrInvalidConfig)
	}

	return nil
}

// validateKeyPrefix 验证 key 前缀必须包含 hash tag。
func validateKeyPrefix(prefix string) error {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return fmt.Errorf("key_prefix cannot be empty")
	}
	if !hashTagPattern.MatchString(prefix) {
		return fmt.Errorf("key_prefix must contain a hash tag like {limiter} for Redis cluster compatibility")
	}
	return nil
}
