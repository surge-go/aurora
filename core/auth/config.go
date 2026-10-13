package auth

import (
	"fmt"
	"strings"
	"time"
)

const (
	defaultAccessTTL           = 15 * time.Minute
	defaultRefreshTTL          = 168 * time.Hour
	defaultRevokeBatch         = 100
	defaultIdempotencyTTL      = 10 * time.Second
	maxRevokeBatch             = 100
	maxClockSkew               = 5 * time.Minute
	defaultCompensationTimeout = 5 * time.Second
	maxCompensationTimeout     = 1 * time.Minute
	defaultPlatformName        = "default"
	maxPrincipalFieldLen       = 256
	maxPlatformFieldLen        = 64
	minTokenTTL                = time.Second
)

// Config 控制 token 校验、会话过期与登录并发限制。
// MaxSessionTTL 是必填项，且必须至少为一秒。
type Config struct {
	KeyPrefix     string        `json:"key_prefix" yaml:"key_prefix" mapstructure:"key_prefix"`
	SigningSecret string        `json:"signing_secret" yaml:"signing_secret" mapstructure:"signing_secret"`
	Issuer        string        `json:"issuer" yaml:"issuer" mapstructure:"issuer"`
	Audience      []string      `json:"audience" yaml:"audience" mapstructure:"audience"`
	AccessTTL     time.Duration `json:"access_ttl" yaml:"access_ttl" mapstructure:"access_ttl"`
	RefreshTTL    time.Duration `json:"refresh_ttl" yaml:"refresh_ttl" mapstructure:"refresh_ttl"`
	MaxSessionTTL time.Duration `json:"max_session_ttl" yaml:"max_session_ttl" mapstructure:"max_session_ttl"`
	ClockSkew     time.Duration `json:"clock_skew" yaml:"clock_skew" mapstructure:"clock_skew"`
	// RefreshCompensationTimeout 限定在轮换后 validator 失败时
	// 触发"尽力而为"的会话撤销的最长时间。
	RefreshCompensationTimeout time.Duration `json:"refresh_compensation_timeout" yaml:"refresh_compensation_timeout" mapstructure:"refresh_compensation_timeout"`
	RevokeBatchSize            int           `json:"revoke_batch_size" yaml:"revoke_batch_size" mapstructure:"revoke_batch_size"`
	IdempotencyTTL             time.Duration `json:"idempotency_ttl" yaml:"idempotency_ttl" mapstructure:"idempotency_ttl"`
	Policy                     SessionPolicy `json:"policy" yaml:"policy" mapstructure:"policy"`
}

// DefaultConfig 返回安全的运维默认值；调用方仍需设置 Issuer、Audience
// 以及至少一秒的 MaxSessionTTL。
func DefaultConfig() Config {
	return Config{
		KeyPrefix:                  defaultKeyPrefix,
		AccessTTL:                  defaultAccessTTL,
		RefreshTTL:                 defaultRefreshTTL,
		RevokeBatchSize:            defaultRevokeBatch,
		IdempotencyTTL:             defaultIdempotencyTTL,
		RefreshCompensationTimeout: defaultCompensationTimeout,
		Policy: SessionPolicy{
			DefaultPlatformPolicy: PlatformPolicy{
				Enabled:         true,
				KickoutStrategy: KickoutOldest,
			},
		},
	}
}

func (c Config) withDefaults() Config {
	c.KeyPrefix = strings.TrimSpace(c.KeyPrefix)
	if c.KeyPrefix == "" {
		c.KeyPrefix = defaultKeyPrefix
	}
	if c.AccessTTL == 0 {
		c.AccessTTL = defaultAccessTTL
	}
	if c.RefreshTTL == 0 {
		c.RefreshTTL = defaultRefreshTTL
	}
	if c.RevokeBatchSize == 0 {
		c.RevokeBatchSize = defaultRevokeBatch
	}
	if c.IdempotencyTTL == 0 {
		c.IdempotencyTTL = defaultIdempotencyTTL
	}
	if c.RefreshCompensationTimeout == 0 {
		c.RefreshCompensationTimeout = defaultCompensationTimeout
	}
	if c.Policy.DefaultPlatformPolicy.KickoutStrategy == "" {
		c.Policy.DefaultPlatformPolicy.KickoutStrategy = KickoutOldest
	}
	if !c.Policy.DefaultPlatformPolicy.Enabled &&
		c.Policy.DefaultPlatformPolicy.MaxSessions == 0 &&
		!c.Policy.DefaultPlatformPolicy.RequireDeviceID &&
		len(c.Policy.DefaultPlatformPolicy.ExclusiveWith) == 0 {
		// 缺省的 default policy 表示允许所有平台。特定平台仍可以
		// 通过 PlatformPolicies 单独禁用。
		c.Policy.DefaultPlatformPolicy.Enabled = true
	}
	return c
}

func (c Config) Validate() error {
	c = c.withDefaults()
	if err := validateKeyPrefix(c.KeyPrefix); err != nil {
		return err
	}
	if strings.TrimSpace(c.Issuer) == "" {
		return fmt.Errorf("%w: issuer is required", ErrInvalidConfig)
	}
	if len(c.Audience) == 0 {
		return fmt.Errorf("%w: audience is required", ErrInvalidConfig)
	}
	for i, audience := range c.Audience {
		if strings.TrimSpace(audience) == "" {
			return fmt.Errorf("%w: audience[%d] is empty", ErrInvalidConfig, i)
		}
	}
	if c.AccessTTL < minTokenTTL || c.RefreshTTL < minTokenTTL || c.RefreshTTL < c.AccessTTL {
		return fmt.Errorf("%w: access and refresh ttl must be at least %s, with refresh ttl not shorter than access ttl", ErrInvalidConfig, minTokenTTL)
	}
	if c.MaxSessionTTL < minTokenTTL {
		return fmt.Errorf("%w: max session ttl must be at least %s", ErrInvalidConfig, minTokenTTL)
	}
	if c.ClockSkew < 0 || c.ClockSkew > maxClockSkew {
		return fmt.Errorf("%w: clock skew must be between 0 and %s", ErrInvalidConfig, maxClockSkew)
	}
	if c.RevokeBatchSize <= 0 || c.RevokeBatchSize > maxRevokeBatch {
		return fmt.Errorf("%w: revoke batch size must be between 1 and %d", ErrInvalidConfig, maxRevokeBatch)
	}
	if c.IdempotencyTTL < minTokenTTL {
		return fmt.Errorf("%w: idempotency ttl must be at least %s", ErrInvalidConfig, minTokenTTL)
	}
	if c.RefreshCompensationTimeout < minTokenTTL || c.RefreshCompensationTimeout > maxCompensationTimeout {
		return fmt.Errorf("%w: refresh compensation timeout must be between %s and %s", ErrInvalidConfig, minTokenTTL, maxCompensationTimeout)
	}
	if err := validateSessionPolicy(c.Policy); err != nil {
		return err
	}
	return nil
}

func validateSessionPolicy(policy SessionPolicy) error {
	if policy.MaxSessions < 0 {
		return fmt.Errorf("%w: max sessions cannot be negative", ErrInvalidConfig)
	}
	if err := validatePlatformPolicy(policy.DefaultPlatformPolicy, "default"); err != nil {
		return err
	}
	for platform, item := range policy.PlatformPolicies {
		if err := validateKeySegment(platform, maxPlatformFieldLen, "platform"); err != nil {
			return err
		}
		if err := validatePlatformPolicy(item, platform); err != nil {
			return err
		}
	}
	return nil
}

func validatePlatformPolicy(policy PlatformPolicy, name string) error {
	if policy.MaxSessions < 0 {
		return fmt.Errorf("%w: platform %q max sessions cannot be negative", ErrInvalidConfig, name)
	}
	strategy := policy.KickoutStrategy
	if strategy != "" && strategy != KickoutOldest && strategy != KickoutLatest && strategy != KickoutAll {
		return fmt.Errorf("%w: platform %q has invalid kickout strategy", ErrInvalidConfig, name)
	}
	for _, platform := range policy.ExclusiveWith {
		if err := validateKeySegment(platform, maxPlatformFieldLen, "exclusive platform"); err != nil {
			return err
		}
	}
	return nil
}

func (p SessionPolicy) forPlatform(platform string) PlatformPolicy {
	if item, ok := p.PlatformPolicies[platform]; ok {
		if item.KickoutStrategy == "" {
			item.KickoutStrategy = KickoutOldest
		}
		return item
	}
	item := p.DefaultPlatformPolicy
	if item.KickoutStrategy == "" {
		item.KickoutStrategy = KickoutOldest
	}
	return item
}

func cloneSessionPolicy(policy SessionPolicy) SessionPolicy {
	policy.DefaultPlatformPolicy.ExclusiveWith = append([]string(nil), policy.DefaultPlatformPolicy.ExclusiveWith...)
	if policy.PlatformPolicies == nil {
		return policy
	}
	cloned := make(map[string]PlatformPolicy, len(policy.PlatformPolicies))
	for platform, item := range policy.PlatformPolicies {
		item.ExclusiveWith = append([]string(nil), item.ExclusiveWith...)
		cloned[platform] = item
	}
	policy.PlatformPolicies = cloned
	return policy
}
