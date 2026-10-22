package attempt

import (
	"fmt"
	"strings"
	"time"
)

const (
	defaultKeyPrefix    = "aurora:auth:attempt:{auth}"
	defaultFailureLimit = 5
	defaultWindow       = 10 * time.Minute
	defaultLockDuration = 15 * time.Minute
	defaultAttemptTTL   = 5 * time.Minute
	minSecretLength     = 32
	maxAccountKeyLen    = 512
	maxFailureLimit     = 100
)

// Config controls account-level login failure tracking.
//
// HMACKey is used to derive Redis key segments and must be kept secret. It is
// separate from the JWT signing secret so rotating token keys does not reveal
// or merge login-attempt state unexpectedly.
type Config struct {
	KeyPrefix    string        `json:"key_prefix" yaml:"key_prefix" mapstructure:"key_prefix"`
	HMACKey      string        `json:"hmac_key" yaml:"hmac_key" mapstructure:"hmac_key"`
	FailureLimit int           `json:"failure_limit" yaml:"failure_limit" mapstructure:"failure_limit"`
	Window       time.Duration `json:"window" yaml:"window" mapstructure:"window"`
	LockDuration time.Duration `json:"lock_duration" yaml:"lock_duration" mapstructure:"lock_duration"`
	AttemptTTL   time.Duration `json:"attempt_ttl" yaml:"attempt_ttl" mapstructure:"attempt_ttl"`
}

// DefaultConfig returns conservative defaults. HMACKey must still be set by
// the application before constructing a Limiter.
func DefaultConfig() Config {
	return Config{
		KeyPrefix:    defaultKeyPrefix,
		FailureLimit: defaultFailureLimit,
		Window:       defaultWindow,
		LockDuration: defaultLockDuration,
		AttemptTTL:   defaultAttemptTTL,
	}
}

func (c Config) withDefaults() Config {
	c.KeyPrefix = strings.TrimSpace(c.KeyPrefix)
	if c.KeyPrefix == "" {
		c.KeyPrefix = defaultKeyPrefix
	}
	if c.FailureLimit == 0 {
		c.FailureLimit = defaultFailureLimit
	}
	if c.Window == 0 {
		c.Window = defaultWindow
	}
	if c.LockDuration == 0 {
		c.LockDuration = defaultLockDuration
	}
	if c.AttemptTTL == 0 {
		c.AttemptTTL = defaultAttemptTTL
	}
	return c
}

// Validate checks configuration without connecting to Redis.
func (c Config) Validate() error {
	c = c.withDefaults()
	if err := validateKeyPrefix(c.KeyPrefix); err != nil {
		return err
	}
	if len([]byte(c.HMACKey)) < minSecretLength {
		return fmt.Errorf("%w: hmac key must be at least %d bytes", ErrInvalidConfig, minSecretLength)
	}
	if c.FailureLimit < 1 {
		return fmt.Errorf("%w: failure limit must be positive", ErrInvalidConfig)
	}
	if c.FailureLimit > maxFailureLimit {
		return fmt.Errorf("%w: failure limit must not exceed %d", ErrInvalidConfig, maxFailureLimit)
	}
	if c.Window < time.Second || c.LockDuration < time.Second || c.AttemptTTL < time.Second {
		return fmt.Errorf("%w: window, lock duration, and attempt ttl must be at least 1s", ErrInvalidConfig)
	}
	if c.AttemptTTL > c.Window {
		return fmt.Errorf("%w: attempt ttl must not exceed window duration", ErrInvalidConfig)
	}
	if c.LockDuration < c.Window {
		return fmt.Errorf("%w: lock duration should be at least window duration", ErrInvalidConfig)
	}
	if len(c.KeyPrefix) > 512 {
		return fmt.Errorf("%w: key prefix is too long", ErrInvalidConfig)
	}
	return nil
}

func validateKeyPrefix(prefix string) error {
	if prefix == "" || strings.ContainsAny(prefix, " \t\r\n") {
		return fmt.Errorf("%w: invalid key prefix", ErrInvalidConfig)
	}
	open := strings.IndexByte(prefix, '{')
	if open < 0 {
		return fmt.Errorf("%w: key prefix must contain a redis hash tag", ErrInvalidConfig)
	}
	closeOffset := strings.IndexByte(prefix[open+1:], '}')
	if closeOffset <= 0 || strings.IndexByte(prefix[open+1:open+1+closeOffset], '{') >= 0 || strings.IndexByte(prefix[open+1+closeOffset+1:], '}') >= 0 {
		return fmt.Errorf("%w: invalid redis hash tag in key prefix", ErrInvalidConfig)
	}
	return nil
}
