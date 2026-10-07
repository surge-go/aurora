package tracing

import (
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Protocol identifies the OTLP transport used by the trace exporter.
type Protocol string

const (
	ProtocolGRPC      Protocol = "grpc"
	ProtocolHTTPProto Protocol = "http/protobuf"
)

// Config contains serializable tracing initialization settings.
type Config struct {
	Enabled        bool              `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
	ServiceName    string            `json:"service_name" yaml:"service_name" mapstructure:"service_name"`
	ServiceVersion string            `json:"service_version" yaml:"service_version" mapstructure:"service_version"`
	Environment    string            `json:"environment" yaml:"environment" mapstructure:"environment"`
	Endpoint       string            `json:"endpoint" yaml:"endpoint" mapstructure:"endpoint"`
	Protocol       Protocol          `json:"protocol" yaml:"protocol" mapstructure:"protocol"`
	Insecure       bool              `json:"insecure" yaml:"insecure" mapstructure:"insecure"`
	Headers        map[string]string `json:"headers" yaml:"headers" mapstructure:"headers"`
	SampleRatio    float64           `json:"sample_ratio" yaml:"sample_ratio" mapstructure:"sample_ratio"`
	Batch          BatchConfig       `json:"batch" yaml:"batch" mapstructure:"batch"`
	Resource       map[string]string `json:"resource" yaml:"resource" mapstructure:"resource"`
}

// BatchConfig controls the SDK batch span processor. Zero values use SDK defaults.
type BatchConfig struct {
	MaxQueueSize       int           `json:"max_queue_size" yaml:"max_queue_size" mapstructure:"max_queue_size"`
	MaxExportBatchSize int           `json:"max_export_batch_size" yaml:"max_export_batch_size" mapstructure:"max_export_batch_size"`
	ScheduleDelay      time.Duration `json:"schedule_delay" yaml:"schedule_delay" mapstructure:"schedule_delay"`
	ExportTimeout      time.Duration `json:"export_timeout" yaml:"export_timeout" mapstructure:"export_timeout"`
}

// DefaultConfig returns development-safe defaults. Production deployments
// should set Endpoint, Protocol, SampleRatio, and TLS settings explicitly.
func DefaultConfig() Config {
	return Config{
		Protocol:    ProtocolGRPC,
		SampleRatio: 1,
	}
}

// Validate performs deterministic local validation only.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("tracing config is nil")
	}

	if strings.TrimSpace(c.ServiceName) == "" {
		return errors.New("tracing service_name must not be empty")
	}
	if c.Protocol != "" && c.Protocol != ProtocolGRPC && c.Protocol != ProtocolHTTPProto {
		return fmt.Errorf("tracing protocol must be %q or %q", ProtocolGRPC, ProtocolHTTPProto)
	}
	if c.SampleRatio < 0 || c.SampleRatio > 1 || math.IsNaN(c.SampleRatio) || math.IsInf(c.SampleRatio, 0) {
		return errors.New("tracing sample_ratio must be between 0 and 1")
	}
	if c.Batch.MaxQueueSize < 0 || c.Batch.MaxExportBatchSize < 0 || c.Batch.ScheduleDelay < 0 || c.Batch.ExportTimeout < 0 {
		return errors.New("tracing batch values must not be negative")
	}
	if c.Batch.MaxQueueSize > 0 && c.Batch.MaxExportBatchSize > c.Batch.MaxQueueSize {
		return errors.New("tracing batch max_export_batch_size must not exceed max_queue_size")
	}
	for key := range c.Resource {
		if strings.TrimSpace(key) == "" {
			return errors.New("tracing resource keys must not be empty")
		}
		if isReservedResourceKey(key) {
			return fmt.Errorf("tracing resource key %q is reserved", key)
		}
	}
	for key := range c.Headers {
		if strings.TrimSpace(key) == "" {
			return errors.New("tracing header keys must not be empty")
		}
	}
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.New("tracing endpoint must not be empty when enabled")
	}
	return validateEndpoint(c.effectiveProtocol(), c.Endpoint, c.Insecure)
}

func (c *Config) effectiveProtocol() Protocol {
	if c.Protocol == "" {
		return ProtocolGRPC
	}
	return c.Protocol
}

func validateEndpoint(protocol Protocol, endpoint string, insecure bool) error {
	endpoint = strings.TrimSpace(endpoint)
	if protocol == ProtocolGRPC && !strings.Contains(endpoint, "://") {
		return validateHostPort(endpoint, "tracing grpc endpoint")
	}

	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("tracing endpoint is invalid: %w", err)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("tracing endpoint must not contain a query or fragment")
	}
	if parsed.User != nil {
		return errors.New("tracing endpoint must not contain userinfo")
	}

	if protocol == ProtocolGRPC {
		if parsed.Scheme == "" {
			return validateHostPort(parsed.Path, "tracing grpc endpoint")
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return errors.New("tracing grpc endpoint scheme must be http or https")
		}
		if parsed.Host == "" || parsed.Path != "" {
			return errors.New("tracing grpc endpoint URL must contain only scheme and host")
		}
		if err := validateHostPort(parsed.Host, "tracing grpc endpoint"); err != nil {
			return err
		}
		if parsed.Scheme == "http" && !insecure {
			return errors.New("tracing grpc http endpoint requires insecure=true")
		}
		return nil
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("tracing http endpoint must use http or https")
	}
	if parsed.Host == "" {
		return errors.New("tracing http endpoint must include a host")
	}
	if parsed.Port() != "" {
		if err := validatePort(parsed.Port()); err != nil {
			return fmt.Errorf("tracing http endpoint: %w", err)
		}
	}
	if parsed.Scheme == "http" && !insecure {
		return errors.New("tracing http endpoint requires insecure=true")
	}
	return nil
}

func validateHostPort(endpoint, label string) error {
	if strings.ContainsAny(endpoint, "?#") {
		return fmt.Errorf("%s must be host:port", label)
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("%s must be host:port", label)
	}
	if strings.ContainsAny(host, " /@") {
		return fmt.Errorf("%s host is invalid", label)
	}
	if err := validatePort(port); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func validatePort(port string) error {
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func isReservedResourceKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "service.name", "service.version", "deployment.environment.name":
		return true
	default:
		return false
	}
}

func cloneConfig(c *Config) Config {
	copy := *c
	copy.Headers = cloneStringMap(c.Headers)
	copy.Resource = cloneStringMap(c.Resource)
	return copy
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}
