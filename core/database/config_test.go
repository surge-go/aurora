package database

import (
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	cfg := &Config{
		Driver: DriverMySQL,
		DSN:    "user:pass@tcp(127.0.0.1:3306)/app?parseTime=true",
		Pool: &PoolConfig{
			MaxOpenConns:    20,
			MaxIdleConns:    10,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		Logger: &LoggerConfig{
			Level:                LogLevelWarn,
			LogSQL:               true,
			SlowThreshold:        200 * time.Millisecond,
			ParameterizedQueries: true,
		},
		Monitoring: &MonitoringConfig{
			TracingEnabled: true,
			MetricsEnabled: true,
		},
		Resolver: &ResolverConfig{
			Replicas: []string{"user:pass@tcp(127.0.0.1:3307)/app?parseTime=true"},
			Policy:   ResolverPolicyRandom,
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfigValidateRequiresDriverAndDSN(t *testing.T) {
	cfg := &Config{}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want error")
	}

	wantContains := []string{
		"database driver must be one of",
		"database dsn must not be empty",
	}
	for _, want := range wantContains {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate() error = %v, want to contain %q", err, want)
		}
	}
}

func TestConfigValidateSubConfigs(t *testing.T) {
	cfg := &Config{
		Driver: DriverPostgres,
		DSN:    "postgres://user:pass@127.0.0.1:5432/app?sslmode=disable",
		Pool: &PoolConfig{
			MaxOpenConns:    1,
			MaxIdleConns:    2,
			ConnMaxLifetime: -time.Second,
			ConnMaxIdleTime: -time.Second,
		},
		Naming: &NamingConfig{
			IdentifierMaxLength: -1,
		},
		Logger: &LoggerConfig{
			Level:         LogLevel("debug"),
			SlowThreshold: -time.Second,
		},
		Resolver: &ResolverConfig{
			Sources:  []string{""},
			Replicas: []string{" "},
			Policy:   ResolverPolicy("least_conn"),
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want error")
	}

	wantContains := []string{
		"database pool.max_idle_conns must be less than or equal to max_open_conns",
		"database pool.conn_max_lifetime must be greater than or equal to 0",
		"database pool.conn_max_idle_time must be greater than or equal to 0",
		"database naming.identifier_max_length must be greater than or equal to 0",
		"database logger.level must be one of",
		"database logger.slow_threshold must be greater than or equal to 0",
		"database resolver.sources[0] must not be empty",
		"database resolver.replicas[0] must not be empty",
		"database resolver.policy must be \"random\"",
	}
	for _, want := range wantContains {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Validate() error = %v, want to contain %q", err, want)
		}
	}
}
