package aurora

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

func writeLifecycleRateLimitConfig(t *testing.T, addr, rateLimitYAML string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aurora.yaml")
	config := "aurora:\n  addr: ':0'\n  mode: test\n"
	if addr != "" {
		config += fmt.Sprintf("redis:\n  mode: standalone\n  addrs: [%q]\n", addr)
	}
	require.NoError(t, os.WriteFile(path, []byte(config+rateLimitYAML), 0o600))
	return path
}

func namedRateLimitConfig(name, keyPrefix, hmacKey string, limit int, window string) string {
	return fmt.Sprintf("  %s:\n    key_prefix: %q\n    hmac_key: %q\n    limit: %d\n    window: %s\n", name, keyPrefix, hmacKey, limit, window)
}

// TestLifecycleRateLimitLookup verifies that rate limiters can be looked up by name
// and that non-existent names return errors.
func TestLifecycleRateLimitLookup(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	config := "ratelimit:\n" +
		namedRateLimitConfig("api", "aurora:ratelimit:{api}", "12345678901234567890123456789012", 100, "1m") +
		namedRateLimitConfig("strict", "aurora:ratelimit:{strict}", "abcdefghijklmnopqrstuvwxyz123456", 10, "30s")

	lifecycle, err := NewLifecycle(writeLifecycleRateLimitConfig(t, s.Addr(), config))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lifecycle.Close(context.Background())) })

	api, err := lifecycle.RateLimit("api")
	require.NoError(t, err)
	require.NotNil(t, api)
	require.Equal(t, 100, api.Limit())

	strict, err := lifecycle.RateLimit("strict")
	require.NoError(t, err)
	require.NotNil(t, strict)
	require.Equal(t, 10, strict.Limit())

	names := lifecycle.RateLimitNames()
	require.Equal(t, []string{"api", "strict"}, names)

	// Mutating returned slice should not affect internal state
	names[0] = "changed"
	require.Equal(t, []string{"api", "strict"}, lifecycle.RateLimitNames())

	// Non-existent instance should error
	_, err = lifecycle.RateLimit("nonexistent")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrRateLimitInstanceNotFound)
}

// TestLifecycleRateLimitEmptyConfig verifies that missing or empty ratelimit
// configuration sections do not cause errors.
func TestLifecycleRateLimitEmptyConfig(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "no ratelimit section",
			yaml: "",
		},
		{
			name: "empty ratelimit map",
			yaml: "ratelimit: {}\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle, err := NewLifecycle(writeLifecycleRateLimitConfig(t, "", tc.yaml))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lifecycle.Close(context.Background())) })

			names := lifecycle.RateLimitNames()
			require.Nil(t, names)

			_, err = lifecycle.RateLimit("any")
			require.Error(t, err)
		})
	}
}

// TestLifecycleRateLimitConfigurationNames verifies that instance names follow
// the documented naming rules: [a-z][a-z0-9_-]{0,63}.
// Configuration keys are automatically lowercased, so "Admin" becomes "admin".
func TestLifecycleRateLimitConfigurationNames(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	// Valid names that should succeed (even with uppercase in config)
	validNames := []string{"api", "a", "api-v2_strict", "Admin", strings.Repeat("a", 64)}
	for _, name := range validNames {
		t.Run(name, func(t *testing.T) {
			config := "ratelimit:\n" +
				namedRateLimitConfig(name, "aurora:ratelimit:{"+name+"}", "12345678901234567890123456789012", 100, "1m")

			lifecycle, err := NewLifecycle(writeLifecycleRateLimitConfig(t, s.Addr(), config))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lifecycle.Close(context.Background())) })

			// Configuration keys are lowercased, so lookup with lowercase name
			limiter, err := lifecycle.RateLimit(strings.ToLower(name))
			require.NoError(t, err)
			require.NotNil(t, limiter)
		})
	}
}

// TestLifecycleRateLimitStateIsolation verifies that rate limiters with different
// names maintain independent state, and custom key_prefix values are respected.
func TestLifecycleRateLimitStateIsolation(t *testing.T) {
	s := miniredis.RunT(t)
	defer s.Close()

	tests := []struct {
		name         string
		customPrefix bool
	}{
		{"custom_prefix=false", false},
		{"custom_prefix=true", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s.FlushAll()

			apiPrefix := "aurora:ratelimit:{api}"
			strictPrefix := "aurora:ratelimit:{strict}"
			if tc.customPrefix {
				apiPrefix = "custom:{api}"
				strictPrefix = "custom:{strict}"
			}

			config := "ratelimit:\n" +
				namedRateLimitConfig("api", apiPrefix, "12345678901234567890123456789012", 100, "1m") +
				namedRateLimitConfig("strict", strictPrefix, "abcdefghijklmnopqrstuvwxyz123456", 10, "1m")

			lifecycle, err := NewLifecycle(writeLifecycleRateLimitConfig(t, s.Addr(), config))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lifecycle.Close(context.Background())) })

			apiLimiter, err := lifecycle.RateLimit("api")
			require.NoError(t, err)
			strictLimiter, err := lifecycle.RateLimit("strict")
			require.NoError(t, err)

			ctx := context.Background()
			testKey := "user:123"

			// Allow on api limiter should not affect strict limiter
			allowed, _, _, err := apiLimiter.Allow(ctx, testKey)
			require.NoError(t, err)
			require.True(t, allowed)

			// Strict limiter should have full quota
			allowed, remaining, _, err := strictLimiter.Allow(ctx, testKey)
			require.NoError(t, err)
			require.True(t, allowed)
			require.Equal(t, 9, remaining) // 10 - 1 = 9

			// Different keys should have independent state
			allowed, remaining, _, err = apiLimiter.Allow(ctx, "user:456")
			require.NoError(t, err)
			require.True(t, allowed)
			require.Equal(t, 99, remaining) // First request for this key
		})
	}
}
