package rabbitmq

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/surge-go/aurora/core/mq"
)

const (
	FailureRequeue      FailureMode = "requeue"
	FailureReject       FailureMode = "reject"
	FailureDeadLetter   FailureMode = "dead_letter"
	defaultExchangeType             = amqp.ExchangeDirect
)

// TLSConfig contains the TLS material used while opening an amqp or amqps URL.
// Config is runtime-only; file fields are convenient for application config.
type TLSConfig struct {
	Config             *tls.Config `json:"-" yaml:"-" mapstructure:"-"`
	CAFile             string      `json:"ca_file" yaml:"ca_file" mapstructure:"ca_file"`
	CertFile           string      `json:"cert_file" yaml:"cert_file" mapstructure:"cert_file"`
	KeyFile            string      `json:"key_file" yaml:"key_file" mapstructure:"key_file"`
	ServerName         string      `json:"server_name" yaml:"server_name" mapstructure:"server_name"`
	InsecureSkipVerify bool        `json:"insecure_skip_verify" yaml:"insecure_skip_verify" mapstructure:"insecure_skip_verify"`
}

// BindingConfig describes one queue-to-exchange binding.
type BindingConfig struct {
	Queue      string     `json:"queue" yaml:"queue" mapstructure:"queue"`
	RoutingKey string     `json:"routing_key" yaml:"routing_key" mapstructure:"routing_key"`
	Arguments  amqp.Table `json:"arguments" yaml:"arguments" mapstructure:"arguments"`
}

// RouteConfig maps a logical mq destination to RabbitMQ resources.
type RouteConfig struct {
	Exchange          string          `json:"exchange" yaml:"exchange" mapstructure:"exchange"`
	ExchangeType      string          `json:"exchange_type" yaml:"exchange_type" mapstructure:"exchange_type"`
	RoutingKey        string          `json:"routing_key" yaml:"routing_key" mapstructure:"routing_key"`
	Queue             string          `json:"queue" yaml:"queue" mapstructure:"queue"`
	Durable           bool            `json:"durable" yaml:"durable" mapstructure:"durable"`
	AutoDelete        bool            `json:"auto_delete" yaml:"auto_delete" mapstructure:"auto_delete"`
	Exclusive         bool            `json:"exclusive" yaml:"exclusive" mapstructure:"exclusive"`
	ExchangeArguments amqp.Table      `json:"exchange_arguments" yaml:"exchange_arguments" mapstructure:"exchange_arguments"`
	QueueArguments    amqp.Table      `json:"queue_arguments" yaml:"queue_arguments" mapstructure:"queue_arguments"`
	Bindings          []BindingConfig `json:"bindings" yaml:"bindings" mapstructure:"bindings"`
}

type PublisherConfig struct {
	Confirm   bool          `json:"confirm" yaml:"confirm" mapstructure:"confirm"`
	Mandatory bool          `json:"mandatory" yaml:"mandatory" mapstructure:"mandatory"`
	Timeout   time.Duration `json:"timeout" yaml:"timeout" mapstructure:"timeout"`
}

type ConsumerConfig struct {
	Prefetch          int           `json:"prefetch" yaml:"prefetch" mapstructure:"prefetch"`
	Concurrency       int           `json:"concurrency" yaml:"concurrency" mapstructure:"concurrency"`
	ConsumerTag       string        `json:"consumer_tag" yaml:"consumer_tag" mapstructure:"consumer_tag"`
	ShutdownTimeout   time.Duration `json:"shutdown_timeout" yaml:"shutdown_timeout" mapstructure:"shutdown_timeout"`
	ReconnectAttempts int           `json:"reconnect_attempts" yaml:"reconnect_attempts" mapstructure:"reconnect_attempts"`
	ReconnectBackoff  time.Duration `json:"reconnect_backoff" yaml:"reconnect_backoff" mapstructure:"reconnect_backoff"`
}

type FailureMode string

type FailureConfig struct {
	Mode      FailureMode `json:"mode" yaml:"mode" mapstructure:"mode"`
	AllowDrop bool        `json:"allow_drop" yaml:"allow_drop" mapstructure:"allow_drop"`
}

type DeadLetterConfig struct {
	Exchange   string `json:"exchange" yaml:"exchange" mapstructure:"exchange"`
	RoutingKey string `json:"routing_key" yaml:"routing_key" mapstructure:"routing_key"`
}

// Connection and Channel are deliberately small runtime interfaces. They make
// protocol and lifecycle behavior testable without requiring a RabbitMQ broker.
type Connection interface {
	Channel() (Channel, error)
	NotifyClose(chan *amqp.Error) chan *amqp.Error
	Close() error
}

type PublishConfirmation interface {
	Done() <-chan struct{}
	Acked() bool
}

type Channel interface {
	Close() error
	Confirm(bool) error
	PublishWithDeferredConfirm(string, string, bool, bool, amqp.Publishing) (PublishConfirmation, error)
	NotifyReturn(chan amqp.Return) chan amqp.Return
	ExchangeDeclare(string, string, bool, bool, bool, bool, amqp.Table) error
	QueueDeclare(string, bool, bool, bool, bool, amqp.Table) (amqp.Queue, error)
	QueueBind(string, string, string, bool, amqp.Table) error
	Qos(int, int, bool) error
	ConsumeWithContext(context.Context, string, string, bool, bool, bool, bool, amqp.Table) (<-chan amqp.Delivery, error)
}

// Dialer is injected in tests or by applications that own a custom transport.
// The default dialer uses amqp091-go and owns the returned connection.
type Dialer func(string, *tls.Config) (Connection, error)

type Config struct {
	URL             string                 `json:"url" yaml:"url" mapstructure:"url"`
	TLS             *TLSConfig             `json:"tls" yaml:"tls" mapstructure:"tls"`
	Routes          map[string]RouteConfig `json:"routes" yaml:"routes" mapstructure:"routes"`
	DeclareTopology bool                   `json:"declare_topology" yaml:"declare_topology" mapstructure:"declare_topology"`
	Publisher       PublisherConfig        `json:"publisher" yaml:"publisher" mapstructure:"publisher"`
	Consumer        ConsumerConfig         `json:"consumer" yaml:"consumer" mapstructure:"consumer"`
	Failure         FailureConfig          `json:"failure" yaml:"failure" mapstructure:"failure"`
	DeadLetter      DeadLetterConfig       `json:"dead_letter" yaml:"dead_letter" mapstructure:"dead_letter"`
	MaxPayload      int                    `json:"max_payload" yaml:"max_payload" mapstructure:"max_payload"`
	Dialer          Dialer                 `json:"-" yaml:"-" mapstructure:"-"`
}

func (c *Config) Validate() error {
	if c == nil {
		return errors.New("mq rabbitmq config is nil")
	}
	var errs []error
	if c.Dialer == nil && strings.TrimSpace(c.URL) == "" {
		errs = append(errs, errors.New("mq rabbitmq url is required"))
	}
	if c.URL != "" {
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "amqp" && u.Scheme != "amqps") || u.Host == "" {
			errs = append(errs, errors.New("mq rabbitmq url must use amqp:// or amqps:// with a host"))
		}
	}
	if len(c.Routes) == 0 {
		errs = append(errs, errors.New("mq rabbitmq routes are required"))
	}
	for name, route := range c.Routes {
		if err := validateRoute(name, route); err != nil {
			errs = append(errs, err)
		}
	}
	if c.MaxPayload < 0 {
		errs = append(errs, errors.New("mq rabbitmq max_payload must be non-negative"))
	}
	if c.Publisher.Timeout < 0 {
		errs = append(errs, errors.New("mq rabbitmq publisher timeout must be non-negative"))
	}
	if c.Consumer.Prefetch < 0 {
		errs = append(errs, errors.New("mq rabbitmq consumer prefetch must be non-negative"))
	}
	if c.Consumer.Concurrency < 0 {
		errs = append(errs, errors.New("mq rabbitmq consumer concurrency must be non-negative"))
	}
	if c.Consumer.ShutdownTimeout < 0 || c.Consumer.ReconnectBackoff < 0 {
		errs = append(errs, errors.New("mq rabbitmq consumer timeouts must be non-negative"))
	}
	if c.Consumer.ReconnectAttempts < 0 {
		errs = append(errs, errors.New("mq rabbitmq consumer reconnect_attempts must be non-negative"))
	}
	mode := c.Failure.Mode
	hasConsumerRoute := false
	for _, route := range c.Routes {
		if route.Queue != "" {
			hasConsumerRoute = true
			break
		}
	}
	if mode == "" {
		mode = FailureReject
	}
	if mode != "" && mode != FailureRequeue && mode != FailureReject && mode != FailureDeadLetter {
		errs = append(errs, fmt.Errorf("mq rabbitmq failure mode %q is unsupported", mode))
	}
	if hasConsumerRoute && (mode == FailureReject || mode == FailureDeadLetter) && c.DeadLetter.Exchange == "" && !c.Failure.AllowDrop {
		errs = append(errs, errors.New("mq rabbitmq dead letter exchange is required unless failure allow_drop is enabled"))
	}
	if c.TLS != nil {
		if (c.TLS.CertFile == "") != (c.TLS.KeyFile == "") {
			errs = append(errs, errors.New("mq rabbitmq tls cert_file and key_file must be provided together"))
		}
	}
	if c.DeadLetter.Exchange == "" && c.DeadLetter.RoutingKey != "" {
		errs = append(errs, errors.New("mq rabbitmq dead letter routing_key requires an exchange"))
	}
	return errors.Join(errs...)
}

func validateRoute(name string, route RouteConfig) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("mq rabbitmq route name is required")
	}
	if strings.TrimSpace(route.Exchange) == "" && strings.TrimSpace(route.Queue) == "" {
		return fmt.Errorf("mq rabbitmq route %q must configure exchange or queue", name)
	}
	if route.Exchange != "" {
		exchangeType := route.ExchangeType
		if exchangeType == "" {
			exchangeType = defaultExchangeType
		}
		switch exchangeType {
		case amqp.ExchangeDirect, amqp.ExchangeTopic, amqp.ExchangeFanout, amqp.ExchangeHeaders:
		default:
			return fmt.Errorf("mq rabbitmq route %q has unsupported exchange type %q", name, exchangeType)
		}
	}
	for i, binding := range route.Bindings {
		if route.Exchange == "" {
			return fmt.Errorf("mq rabbitmq route %q bindings require an exchange", name)
		}
		if binding.Queue == "" && route.Queue == "" {
			return fmt.Errorf("mq rabbitmq route %q binding %d requires a queue", name, i)
		}
	}
	return nil
}

func (c Config) normalized() Config {
	if c.Publisher.Timeout == 0 {
		c.Publisher.Timeout = 30 * time.Second
	}
	if !c.Publisher.Confirm {
		// Confirm is a safety property of this adapter, not an opt-in behavior.
		c.Publisher.Confirm = true
	}
	if !c.Publisher.Mandatory {
		c.Publisher.Mandatory = true
	}
	if c.Consumer.Prefetch == 0 {
		c.Consumer.Prefetch = 10
	}
	if c.Consumer.Concurrency == 0 {
		c.Consumer.Concurrency = 1
	}
	if c.Consumer.ShutdownTimeout == 0 {
		c.Consumer.ShutdownTimeout = 30 * time.Second
	}
	if c.Consumer.ReconnectAttempts == 0 {
		c.Consumer.ReconnectAttempts = 3
	}
	if c.Consumer.ReconnectBackoff == 0 {
		c.Consumer.ReconnectBackoff = 250 * time.Millisecond
	}
	if c.Failure.Mode == "" {
		c.Failure.Mode = FailureReject
	}
	for name, route := range c.Routes {
		route.Exchange = strings.TrimSpace(route.Exchange)
		route.Queue = strings.TrimSpace(route.Queue)
		route.RoutingKey = strings.TrimSpace(route.RoutingKey)
		if route.ExchangeType == "" {
			route.ExchangeType = defaultExchangeType
		}
		c.Routes[name] = route
	}
	return c
}

func buildTLSConfig(input *TLSConfig) (*tls.Config, error) {
	if input == nil {
		return nil, nil
	}
	var cfg tls.Config
	if input.Config != nil {
		cfg = *input.Config.Clone()
	}
	if input.ServerName != "" {
		cfg.ServerName = input.ServerName
	}
	cfg.InsecureSkipVerify = input.InsecureSkipVerify
	if input.CAFile != "" {
		data, err := os.ReadFile(input.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read rabbitmq tls ca file: %w", err)
		}
		if cfg.RootCAs == nil {
			cfg.RootCAs = x509.NewCertPool()
		}
		for len(data) > 0 {
			block, rest := pem.Decode(data)
			if block == nil {
				return nil, errors.New("parse rabbitmq tls ca file: invalid PEM")
			}
			data = rest
			if block.Type == "CERTIFICATE" && !cfg.RootCAs.AppendCertsFromPEM(pem.EncodeToMemory(block)) {
				return nil, errors.New("parse rabbitmq tls ca file: certificate rejected")
			}
		}
	}
	if input.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(input.CertFile, input.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load rabbitmq tls client certificate: %w", err)
		}
		cfg.Certificates = append(cfg.Certificates, cert)
	}
	return &cfg, nil
}

func invalidDestination(destination string) error {
	if strings.TrimSpace(destination) == "" {
		return fmt.Errorf("%w: destination is required", mq.ErrInvalidArgument)
	}
	return nil
}
