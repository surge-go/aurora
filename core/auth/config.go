package auth

import (
	"fmt"
	"strings"
	"time"
)

const (
	defaultAccessTTL     = 15 * time.Minute
	defaultRefreshTTL    = 168 * time.Hour
	defaultRevokeBatch   = 100
	maxRevokeBatch       = 100
	maxClockSkew         = 5 * time.Minute
	defaultPlatformName  = "default"
	maxPrincipalFieldLen = 256
	maxPlatformFieldLen  = 64
	minTokenTTL          = time.Second
)

// Config controls token validation, session expiry and login concurrency.
// MaxSessionTTL is intentionally required and must be at least one second.
type Config struct {
	KeyPrefix       string        `json:"key_prefix" yaml:"key_prefix" mapstructure:"key_prefix"`
	SigningSecret   string        `json:"signing_secret" yaml:"signing_secret" mapstructure:"signing_secret"`
	Issuer          string        `json:"issuer" yaml:"issuer" mapstructure:"issuer"`
	Audience        []string      `json:"audience" yaml:"audience" mapstructure:"audience"`
	AccessTTL       time.Duration `json:"access_ttl" yaml:"access_ttl" mapstructure:"access_ttl"`
	RefreshTTL      time.Duration `json:"refresh_ttl" yaml:"refresh_ttl" mapstructure:"refresh_ttl"`
	MaxSessionTTL   time.Duration `json:"max_session_ttl" yaml:"max_session_ttl" mapstructure:"max_session_ttl"`
	ClockSkew       time.Duration `json:"clock_skew" yaml:"clock_skew" mapstructure:"clock_skew"`
	RevokeBatchSize int           `json:"revoke_batch_size" yaml:"revoke_batch_size" mapstructure:"revoke_batch_size"`
	Policy          SessionPolicy `json:"policy" yaml:"policy" mapstructure:"policy"`
}

// DefaultConfig returns safe operational defaults; callers must still set issuer,
// audience and a MaxSessionTTL of at least one second.
func DefaultConfig() Config {
	return Config{
		KeyPrefix:       defaultKeyPrefix,
		AccessTTL:       defaultAccessTTL,
		RefreshTTL:      defaultRefreshTTL,
		RevokeBatchSize: defaultRevokeBatch,
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
	if c.Policy.DefaultPlatformPolicy.KickoutStrategy == "" {
		c.Policy.DefaultPlatformPolicy.KickoutStrategy = KickoutOldest
	}
	if !c.Policy.DefaultPlatformPolicy.Enabled &&
		c.Policy.DefaultPlatformPolicy.MaxSessions == 0 &&
		!c.Policy.DefaultPlatformPolicy.RequireDeviceID &&
		len(c.Policy.DefaultPlatformPolicy.ExclusiveWith) == 0 {
		// An omitted default policy means all platforms are allowed. A specific
		// platform can still be disabled through PlatformPolicies.
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
