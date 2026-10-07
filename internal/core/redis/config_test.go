package redis

import (
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		want string
	}{
		{name: "nil config", want: "config is nil"},
		{name: "default standalone", cfg: &Config{Addrs: []string{"127.0.0.1:6379"}}},
		{name: "unknown mode", cfg: &Config{Mode: "ring", Addrs: []string{"127.0.0.1:6379"}}, want: "redis mode"},
		{name: "multiple standalone addresses", cfg: &Config{Addrs: []string{"127.0.0.1:6379", "127.0.0.1:6380"}}, want: "exactly one"},
		{name: "bad port", cfg: &Config{Addrs: []string{"127.0.0.1:70000"}}, want: "port"},
		{name: "missing sentinel config", cfg: &Config{Mode: ModeSentinel, Addrs: []string{"127.0.0.1:26379"}}, want: "requires sentinel"},
		{name: "missing master name", cfg: &Config{Mode: ModeSentinel, Addrs: []string{"127.0.0.1:26379"}, Sentinel: &SentinelConfig{}}, want: "master_name"},
		{name: "cluster db", cfg: &Config{Mode: ModeCluster, Addrs: []string{"127.0.0.1:6379"}, DB: 1}, want: "db to be 0"},
		{name: "cluster unix network", cfg: &Config{Mode: ModeCluster, Network: "unix", Addrs: []string{"/tmp/redis.sock"}}, want: "only supported in standalone"},
		{name: "topology conflict", cfg: &Config{Addrs: []string{"127.0.0.1:6379"}, Cluster: &ClusterConfig{}}, want: "does not accept"},
		{name: "invalid retry special value", cfg: &Config{Addrs: []string{"127.0.0.1:6379"}, Retry: &RetryConfig{MaxRetries: -2}}, want: "retry values"},
		{name: "negative duration", cfg: &Config{Addrs: []string{"127.0.0.1:6379"}, Timeout: &TimeoutConfig{DialTimeout: -time.Second}}, want: "timeout values"},
		{name: "read timeout special value", cfg: &Config{Addrs: []string{"127.0.0.1:6379"}, Timeout: &TimeoutConfig{ReadTimeout: -3}}, want: "read and write timeout"},
		{name: "tls fields disabled", cfg: &Config{Addrs: []string{"127.0.0.1:6379"}, TLS: &TLSConfig{CAFile: "ca.pem"}}, want: "require tls.enabled"},
		{name: "tls key without cert", cfg: &Config{Addrs: []string{"127.0.0.1:6379"}, TLS: &TLSConfig{Enabled: true, KeyFile: "client.key"}}, want: "must be set together"},
		{name: "cluster conflicting routes", cfg: &Config{Mode: ModeCluster, Addrs: []string{"127.0.0.1:6379"}, Cluster: &ClusterConfig{RouteByLatency: true, RouteRandomly: true}}, want: "mutually exclusive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
