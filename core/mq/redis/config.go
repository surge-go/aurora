package redis

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/surge-go/aurora/core/mq"
)

// Config configures a Redis Streams MQ client.
//
// Client is a runtime dependency and is not serialized. It is normally
// created by core/redis and injected by the application bootstrap.
type Config struct {
	Client       redis.UniversalClient `json:"-" yaml:"-" mapstructure:"-"`
	OwnClient    bool                  `json:"own_client" yaml:"own_client" mapstructure:"own_client"`
	Group        string                `json:"group" yaml:"group" mapstructure:"group"`
	Consumer     string                `json:"consumer" yaml:"consumer" mapstructure:"consumer"`
	StartID      string                `json:"start_id" yaml:"start_id" mapstructure:"start_id"` // Defaults to 0-0.
	ReadCount    int64                 `json:"read_count" yaml:"read_count" mapstructure:"read_count"`
	Block        time.Duration         `json:"block" yaml:"block" mapstructure:"block"`
	ClaimIdle    time.Duration         `json:"claim_idle" yaml:"claim_idle" mapstructure:"claim_idle"` // Defaults to 30s; requires Redis 6.2+.
	DisableClaim bool                  `json:"disable_claim" yaml:"disable_claim" mapstructure:"disable_claim"`
	MaxPayload   int                   `json:"max_payload" yaml:"max_payload" mapstructure:"max_payload"`
}

func (c *Config) Validate() error {
	if c == nil {
		return errors.New("mq redis config is nil")
	}
	var errs []error
	if isNilClient(c.Client) {
		errs = append(errs, errors.New("mq redis client is required"))
	}
	if strings.TrimSpace(c.Group) == "" {
		errs = append(errs, errors.New("mq redis group is required"))
	}
	if strings.TrimSpace(c.Consumer) == "" {
		errs = append(errs, errors.New("mq redis consumer is required"))
	}
	if c.StartID != "" && strings.TrimSpace(c.StartID) == "" {
		errs = append(errs, errors.New("mq redis start_id must not be whitespace"))
	}
	if c.ReadCount < 0 {
		errs = append(errs, errors.New("mq redis read_count must be non-negative"))
	}
	if c.Block < 0 {
		errs = append(errs, errors.New("mq redis block must be non-negative"))
	}
	if c.ClaimIdle < 0 {
		errs = append(errs, errors.New("mq redis claim_idle must be non-negative"))
	}
	if c.MaxPayload < 0 {
		errs = append(errs, errors.New("mq redis max_payload must be non-negative"))
	}
	return errors.Join(errs...)
}

func (c Config) normalized() Config {
	if c.StartID == "" {
		c.StartID = "0-0"
	}
	if c.ReadCount == 0 {
		c.ReadCount = 10
	}
	if c.Block == 0 {
		c.Block = 5 * time.Second
	}
	if c.ClaimIdle == 0 && !c.DisableClaim {
		c.ClaimIdle = 30 * time.Second
	}
	return c
}

func isNilClient(client redis.UniversalClient) bool {
	if client == nil {
		return true
	}
	value := reflect.ValueOf(client)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func invalidDestination(destination string) error {
	if strings.TrimSpace(destination) == "" {
		return fmt.Errorf("%w: destination is required", mq.ErrInvalidArgument)
	}
	return nil
}
