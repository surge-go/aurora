package kafka

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

const (
	maxConsumerConcurrency             = 256
	maxRetryAttempts                   = 100
	maxProducerRetries                 = 100
	FailureStop            FailureMode = "stop"
	FailureRetry           FailureMode = "retry"
	FailureDeadLetter      FailureMode = "dead_letter"
	FailureDrop            FailureMode = "drop"

	StartEarliest StartOffset = "earliest"
	StartLatest   StartOffset = "latest"

	SASLPlain    SASLMechanism = "plain"
	SASLSCRAM256 SASLMechanism = "scram_sha_256"
	SASLSCRAM512 SASLMechanism = "scram_sha_512"

	messageIDHeader     = "x-aurora-mq-id-v1"
	maxKafkaTopicLength = 249
)

type FailureMode string
type StartOffset string
type SASLMechanism string

type RouteConfig struct {
	Topic string `json:"topic" yaml:"topic" mapstructure:"topic"`
}

type TLSConfig struct {
	Config             *tls.Config `json:"-" yaml:"-" mapstructure:"-"`
	CAFile             string      `json:"ca_file" yaml:"ca_file" mapstructure:"ca_file"`
	CertFile           string      `json:"cert_file" yaml:"cert_file" mapstructure:"cert_file"`
	KeyFile            string      `json:"key_file" yaml:"key_file" mapstructure:"key_file"`
	ServerName         string      `json:"server_name" yaml:"server_name" mapstructure:"server_name"`
	InsecureSkipVerify bool        `json:"insecure_skip_verify" yaml:"insecure_skip_verify" mapstructure:"insecure_skip_verify"`
}

type SASLConfig struct {
	Mechanism SASLMechanism `json:"mechanism" yaml:"mechanism" mapstructure:"mechanism"`
	Username  string        `json:"username" yaml:"username" mapstructure:"username"`
	Password  string        `json:"password" yaml:"password" mapstructure:"password"`
}

type ProducerConfig struct {
	Timeout    time.Duration `json:"timeout" yaml:"timeout" mapstructure:"timeout"`
	MaxRetries int           `json:"max_retries" yaml:"max_retries" mapstructure:"max_retries"`
}

type RetryConfig struct {
	MaxAttempts int           `json:"max_attempts" yaml:"max_attempts" mapstructure:"max_attempts"`
	Backoff     time.Duration `json:"backoff" yaml:"backoff" mapstructure:"backoff"`
}

type ConsumerConfig struct {
	GroupID          string        `json:"group_id" yaml:"group_id" mapstructure:"group_id"`
	Concurrency      int           `json:"concurrency" yaml:"concurrency" mapstructure:"concurrency"`
	StartOffset      StartOffset   `json:"start_offset" yaml:"start_offset" mapstructure:"start_offset"`
	SessionTimeout   time.Duration `json:"session_timeout" yaml:"session_timeout" mapstructure:"session_timeout"`
	RebalanceTimeout time.Duration `json:"rebalance_timeout" yaml:"rebalance_timeout" mapstructure:"rebalance_timeout"`
	Retry            RetryConfig   `json:"retry" yaml:"retry" mapstructure:"retry"`
	Failure          FailureConfig `json:"failure" yaml:"failure" mapstructure:"failure"`
}

type FailureConfig struct {
	Mode            FailureMode `json:"mode" yaml:"mode" mapstructure:"mode"`
	DeadLetterRoute string      `json:"dead_letter_route" yaml:"dead_letter_route" mapstructure:"dead_letter_route"`
	AllowDrop       bool        `json:"allow_drop" yaml:"allow_drop" mapstructure:"allow_drop"`
}

// Record is the broker-neutral subset used by the injected transport.
type Record struct {
	Topic       string
	Partition   int32
	Offset      int64
	Key         []byte
	Value       []byte
	Headers     []RecordHeader
	Timestamp   time.Time
	LeaderEpoch int32
	native      any
}

type RecordHeader struct {
	Key   string
	Value []byte
}

// Transport is the Kafka operation surface used by Client and test fakes.
type Transport interface {
	Produce(context.Context, *Record) error
	Subscribe(string)
	Poll(context.Context, int) ([]*Record, error)
	Commit(context.Context, *Record) error
	Close()
}

// Dialer constructs a runtime transport. It is excluded from serialized config.
type Dialer func(Config, *tls.Config, func(string, []int32)) (Transport, error)

type Config struct {
	Brokers    []string               `json:"brokers" yaml:"brokers" mapstructure:"brokers"`
	ClientID   string                 `json:"client_id" yaml:"client_id" mapstructure:"client_id"`
	Routes     map[string]RouteConfig `json:"routes" yaml:"routes" mapstructure:"routes"`
	TLS        *TLSConfig             `json:"tls" yaml:"tls" mapstructure:"tls"`
	SASL       *SASLConfig            `json:"sasl" yaml:"sasl" mapstructure:"sasl"`
	Producer   ProducerConfig         `json:"producer" yaml:"producer" mapstructure:"producer"`
	Consumer   ConsumerConfig         `json:"consumer" yaml:"consumer" mapstructure:"consumer"`
	MaxPayload int                    `json:"max_payload" yaml:"max_payload" mapstructure:"max_payload"`
	Dialer     Dialer                 `json:"-" yaml:"-" mapstructure:"-"`
}

var topicPattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func (c *Config) Validate() error {
	if c == nil {
		return errors.New("mq kafka config is nil")
	}
	var errs []error
	if len(c.Brokers) == 0 && c.Dialer == nil {
		errs = append(errs, errors.New("mq kafka brokers are required"))
	}
	for _, broker := range c.Brokers {
		host, port, err := net.SplitHostPort(strings.TrimSpace(broker))
		portNumber, portErr := strconv.Atoi(port)
		if err != nil || strings.TrimSpace(host) == "" || portErr != nil || portNumber < 1 || portNumber > 65535 {
			errs = append(errs, fmt.Errorf("mq kafka broker %q must be host:port", broker))
		}
	}
	if strings.TrimSpace(c.ClientID) == "" {
		errs = append(errs, errors.New("mq kafka client_id is required"))
	}
	if len(c.Routes) == 0 {
		errs = append(errs, errors.New("mq kafka routes are required"))
	}
	for name, route := range c.Routes {
		if strings.TrimSpace(name) == "" {
			errs = append(errs, errors.New("mq kafka route name is required"))
		}
		if !validTopic(route.Topic) {
			errs = append(errs, fmt.Errorf("mq kafka route %q has invalid topic", name))
		}
	}
	if strings.TrimSpace(c.Consumer.GroupID) == "" {
		errs = append(errs, errors.New("mq kafka consumer group_id is required"))
	}
	if c.MaxPayload < 0 || c.Producer.Timeout < 0 || c.Producer.MaxRetries < 0 || c.Producer.MaxRetries > maxProducerRetries {
		errs = append(errs, errors.New("mq kafka payload and producer timeout must be non-negative; max_retries must be between 0 and 100"))
	}
	if c.Consumer.Concurrency < 0 || c.Consumer.Concurrency > maxConsumerConcurrency || c.Consumer.SessionTimeout < 0 || c.Consumer.RebalanceTimeout < 0 || c.Consumer.Retry.MaxAttempts < 0 || c.Consumer.Retry.MaxAttempts > maxRetryAttempts || c.Consumer.Retry.Backoff < 0 {
		errs = append(errs, errors.New("mq kafka consumer limits must be non-negative and within supported bounds"))
	}
	mode := c.Consumer.Failure.Mode
	if mode == "" {
		mode = FailureStop
	}
	switch mode {
	case FailureStop:
	case FailureRetry:
		if c.Consumer.Retry.MaxAttempts < 1 || c.Consumer.Retry.Backoff <= 0 {
			errs = append(errs, errors.New("mq kafka retry mode requires positive max_attempts and backoff"))
		}
	case FailureDeadLetter:
		route, ok := c.Routes[c.Consumer.Failure.DeadLetterRoute]
		if !ok || route.Topic == "" {
			errs = append(errs, errors.New("mq kafka dead_letter mode requires a configured dead_letter_route"))
		}
		hasSourceRoute := false
		for name, source := range c.Routes {
			if name == c.Consumer.Failure.DeadLetterRoute {
				continue
			}
			hasSourceRoute = true
			if ok && name != c.Consumer.Failure.DeadLetterRoute && source.Topic == route.Topic {
				errs = append(errs, errors.New("mq kafka dead_letter topic must differ from every configured source topic"))
				break
			}
		}
		if !hasSourceRoute {
			errs = append(errs, errors.New("mq kafka dead_letter mode requires at least one source route"))
		}
	case FailureDrop:
		if !c.Consumer.Failure.AllowDrop {
			errs = append(errs, errors.New("mq kafka drop mode requires allow_drop"))
		}
	default:
		errs = append(errs, fmt.Errorf("mq kafka failure mode %q is unsupported", mode))
	}
	if mode != FailureDeadLetter && c.Consumer.Failure.DeadLetterRoute != "" {
		errs = append(errs, errors.New("mq kafka dead_letter_route requires dead_letter mode"))
	}
	if c.SASL != nil {
		if c.SASL.Username == "" || c.SASL.Password == "" {
			errs = append(errs, errors.New("mq kafka sasl username and password are required"))
		}
		switch c.SASL.Mechanism {
		case SASLPlain, SASLSCRAM256, SASLSCRAM512:
		default:
			errs = append(errs, fmt.Errorf("mq kafka sasl mechanism %q is unsupported", c.SASL.Mechanism))
		}
	}
	if c.TLS != nil && ((c.TLS.CertFile == "") != (c.TLS.KeyFile == "")) {
		errs = append(errs, errors.New("mq kafka tls cert_file and key_file must be provided together"))
	}
	if c.Consumer.StartOffset != "" && c.Consumer.StartOffset != StartEarliest && c.Consumer.StartOffset != StartLatest {
		errs = append(errs, fmt.Errorf("mq kafka start_offset %q is unsupported", c.Consumer.StartOffset))
	}
	return errors.Join(errs...)
}

func (c Config) normalized() Config {
	if c.Producer.Timeout == 0 {
		c.Producer.Timeout = 30 * time.Second
	}
	if c.Producer.MaxRetries == 0 {
		c.Producer.MaxRetries = 5
	}
	if c.Consumer.Concurrency == 0 {
		c.Consumer.Concurrency = 4
	}
	if c.Consumer.StartOffset == "" {
		c.Consumer.StartOffset = StartEarliest
	}
	if c.Consumer.SessionTimeout == 0 {
		c.Consumer.SessionTimeout = 45 * time.Second
	}
	if c.Consumer.RebalanceTimeout == 0 {
		c.Consumer.RebalanceTimeout = 60 * time.Second
	}
	if c.Consumer.Failure.Mode == "" {
		c.Consumer.Failure.Mode = FailureStop
	}
	return c
}

func validTopic(topic string) bool {
	return len(topic) > 0 && len(topic) <= maxKafkaTopicLength && topic != "." && topic != ".." && topicPattern.MatchString(topic)
}

func buildTLSConfig(input *TLSConfig) (*tls.Config, error) {
	if input == nil {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if input.Config != nil {
		cfg = input.Config.Clone()
	}
	if input.ServerName != "" {
		cfg.ServerName = input.ServerName
	}
	if input.InsecureSkipVerify {
		cfg.InsecureSkipVerify = true
	}
	if input.CAFile != "" {
		data, err := os.ReadFile(input.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read kafka tls ca file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, errors.New("parse kafka tls ca file: no valid certificates")
		}
		cfg.RootCAs = pool
	}
	if input.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(input.CertFile, input.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load kafka tls client certificate: %w", err)
		}
		cfg.Certificates = append(cfg.Certificates, cert)
	}
	return cfg, nil
}

func saslMechanism(cfg *SASLConfig) sasl.Mechanism {
	if cfg == nil {
		return nil
	}
	auth := scram.Auth{User: cfg.Username, Pass: cfg.Password}
	switch cfg.Mechanism {
	case SASLPlain:
		return plain.Plain(func(context.Context) (plain.Auth, error) {
			return plain.Auth{User: cfg.Username, Pass: cfg.Password}, nil
		})
	case SASLSCRAM256:
		return auth.AsSha256Mechanism()
	case SASLSCRAM512:
		return auth.AsSha512Mechanism()
	default:
		return nil
	}
}
