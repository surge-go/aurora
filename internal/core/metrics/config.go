package metrics

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Protocol identifies the OTLP transport used by the metrics exporter.
type Protocol string

const (
	ProtocolGRPC      Protocol = "grpc"
	ProtocolHTTPProto Protocol = "http/protobuf"
)

// Config contains serializable metrics initialization settings.
type Config struct {
	Enabled        bool              `json:"enabled" yaml:"enabled" mapstructure:"enabled"`
	ServiceName    string            `json:"service_name" yaml:"service_name" mapstructure:"service_name"`
	ServiceVersion string            `json:"service_version" yaml:"service_version" mapstructure:"service_version"`
	Environment    string            `json:"environment" yaml:"environment" mapstructure:"environment"`
	Endpoint       string            `json:"endpoint" yaml:"endpoint" mapstructure:"endpoint"`
	Protocol       Protocol          `json:"protocol" yaml:"protocol" mapstructure:"protocol"`
	Insecure       bool              `json:"insecure" yaml:"insecure" mapstructure:"insecure"`
	Headers        map[string]string `json:"headers" yaml:"headers" mapstructure:"headers"`
	ExportInterval time.Duration     `json:"export_interval" yaml:"export_interval" mapstructure:"export_interval"`
	ExportTimeout  time.Duration     `json:"export_timeout" yaml:"export_timeout" mapstructure:"export_timeout"`
	Resource       map[string]string `json:"resource" yaml:"resource" mapstructure:"resource"`
}

// DefaultConfig returns the standard OTLP defaults with metrics disabled.
func DefaultConfig() Config {
	return Config{Protocol: ProtocolGRPC}
}

// Validate performs deterministic local validation without opening a connection.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("metrics config is nil")
	}
	if strings.TrimSpace(c.ServiceName) == "" {
		return errors.New("metrics service_name must not be empty")
	}
	if c.Protocol != "" && c.Protocol != ProtocolGRPC && c.Protocol != ProtocolHTTPProto {
		return fmt.Errorf("metrics protocol must be %q or %q", ProtocolGRPC, ProtocolHTTPProto)
	}
	if c.ExportInterval < 0 || c.ExportTimeout < 0 {
		return errors.New("metrics export_interval and export_timeout must not be negative")
	}
	for key := range c.Resource {
		if strings.TrimSpace(key) == "" {
			return errors.New("metrics resource keys must not be empty")
		}
		if isReservedResourceKey(key) {
			return fmt.Errorf("metrics resource key %q is reserved", key)
		}
	}
	for key, value := range c.Headers {
		if !isHeaderToken(key) {
			return errors.New("metrics header keys must be valid and not empty")
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("metrics header %q contains an invalid line break", key)
		}
	}
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Endpoint) == "" {
		return errors.New("metrics endpoint must not be empty when enabled")
	}
	return validateEndpoint(c.effectiveProtocol(), c.Endpoint, c.Insecure)
}

func isHeaderToken(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char > unicode.MaxASCII || !(unicode.IsLetter(char) || unicode.IsDigit(char) || strings.ContainsRune("!#$%&'*+-.^_`|~", char)) {
			return false
		}
	}
	return true
}

func (c Config) effectiveProtocol() Protocol {
	if c.Protocol == "" {
		return ProtocolGRPC
	}
	return c.Protocol
}

func validateEndpoint(protocol Protocol, endpoint string, insecure bool) error {
	endpoint = strings.TrimSpace(endpoint)
	if protocol == ProtocolGRPC && !strings.Contains(endpoint, "://") {
		return validateHostPort(endpoint, "metrics grpc endpoint")
	}

	parsed, err := url.Parse(endpoint)
	if err != nil {
		return errors.New("metrics endpoint is invalid")
	}
	if parsed.User != nil {
		return errors.New("metrics endpoint must not contain userinfo")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return errors.New("metrics endpoint must not contain a query or fragment")
	}
	if protocol == ProtocolGRPC {
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return errors.New("metrics grpc endpoint scheme must be http or https")
		}
		if parsed.Host == "" || parsed.Path != "" || parsed.RawPath != "" {
			return errors.New("metrics grpc endpoint URL must contain only scheme and host")
		}
		if err := validateURLHostPort(parsed, "metrics grpc endpoint", true); err != nil {
			return err
		}
		return validateTransportSecurity(parsed.Scheme, insecure, "metrics grpc endpoint")
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("metrics http endpoint must use http or https")
	}
	if err := validateURLHostPort(parsed, "metrics http endpoint", false); err != nil {
		return err
	}
	return validateTransportSecurity(parsed.Scheme, insecure, "metrics http endpoint")
}

func validateURLHostPort(parsed *url.URL, label string, requirePort bool) error {
	if parsed.Hostname() == "" {
		return fmt.Errorf("%s must include a host", label)
	}
	if port := parsed.Port(); port != "" {
		if err := validatePort(port); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
	}
	if requirePort && parsed.Port() == "" {
		return fmt.Errorf("%s must include a port", label)
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return fmt.Errorf("%s has an empty port", label)
	}
	return nil
}

func validateTransportSecurity(scheme string, insecure bool, label string) error {
	if scheme == "http" && !insecure {
		return fmt.Errorf("%s using http requires insecure=true", label)
	}
	if scheme == "https" && insecure {
		return fmt.Errorf("%s using https cannot set insecure=true", label)
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
