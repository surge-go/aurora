package aurora

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

func writeLifecycleAttemptConfig(t *testing.T, addr, attemptYAML string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "aurora.yaml")
	config := "aurora:\n  addr: ':0'\n  mode: test\n"
	if addr != "" {
		config += fmt.Sprintf("redis:\n  mode: standalone\n  addrs: [%q]\n", addr)
	}
	require.NoError(t, os.WriteFile(path, []byte(config+attemptYAML), 0o600))
	return path
}

func namedAttemptConfig(name, hmacKey, extra string) string {
	return fmt.Sprintf("  %s:\n    hmac_key: %q\n    failure_limit: 5\n    window: 10m\n    lock_duration: 15m\n    attempt_ttl: 5m\n%s", name, hmacKey, extra)
}

func TestLifecycleAttemptLookup(t *testing.T) {
	config := "attempt:\n" + namedAttemptConfig("admin", strings.Repeat("a", 32), "") +
		namedAttemptConfig("user", strings.Repeat("u", 32), "")
	lifecycle, err := NewLifecycle(writeLifecycleAttemptConfig(t, "127.0.0.1:6379", config))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lifecycle.Close(context.Background())) })
	admin, err := lifecycle.Attempt("admin")
	require.NoError(t, err)
	user, err := lifecycle.Attempt("user")
	require.NoError(t, err)
	require.NotSame(t, admin, user)
	names := lifecycle.AttemptNames()
	require.Equal(t, []string{"admin", "user"}, names)
	names[0] = "changed"
	require.Equal(t, []string{"admin", "user"}, lifecycle.AttemptNames())
	_, err = lifecycle.Attempt("admin")
	require.NoError(t, err)
	for _, name := range []string{"", "missing", "Admin", " admin "} {
		limiter, err := lifecycle.Attempt(name)
		require.Nil(t, limiter)
		require.ErrorIs(t, err, ErrAttemptInstanceNotFound)
		require.Contains(t, err.Error(), fmt.Sprintf("%q", name))
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			limiter, err := lifecycle.Attempt("admin")
			if err != nil || limiter != admin {
				t.Errorf("concurrent lookup = %p, %v; want %p", limiter, err, admin)
			}
			names := lifecycle.AttemptNames()
			if !slices.Equal(names, []string{"admin", "user"}) {
				t.Errorf("concurrent AttemptNames() = %v", names)
			}
			names[0] = "changed"
		})
	}
	wg.Wait()
	var empty Lifecycle
	require.Nil(t, empty.AttemptNames())
	_, err = empty.Attempt("admin")
	require.ErrorIs(t, err, ErrAttemptInstanceNotFound)
	var missing *Lifecycle
	require.Nil(t, missing.AttemptNames())
	_, err = missing.Attempt("admin")
	require.EqualError(t, err, "lifecycle is nil")
}

func TestLifecycleAttemptEmptyConfig(t *testing.T) {
	for _, config := range []string{"", "attempt: {}\n"} {
		t.Run(config, func(t *testing.T) {
			lifecycle, err := NewLifecycle(writeLifecycleAttemptConfig(t, "", config))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lifecycle.Close(context.Background())) })
			_, err = lifecycle.Attempt("admin")
			require.ErrorIs(t, err, ErrAttemptInstanceNotFound)
			require.Nil(t, lifecycle.Redis)
			require.Nil(t, lifecycle.AttemptNames())
		})
	}
}

func TestLifecycleAttemptConfigurationNames(t *testing.T) {
	for _, name := range []string{"admin", "a", "admin_2-web", strings.Repeat("a", 64)} {
		t.Run(name, func(t *testing.T) {
			config := "attempt:\n" + namedAttemptConfig(name, strings.Repeat("k", 32), "")
			lifecycle, err := NewLifecycle(writeLifecycleAttemptConfig(t, "127.0.0.1:6379", config))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lifecycle.Close(context.Background())) })
			limiter, err := lifecycle.Attempt(strings.ToLower(name))
			require.NoError(t, err)
			require.NotNil(t, limiter)
		})
	}
}

func TestNewLifecycleRejectsInvalidAttempt(t *testing.T) {
	hmacKey := strings.Repeat("secret", 6)
	valid := namedAttemptConfig("admin", hmacKey, "")
	cases := []struct {
		name, config, want string
	}{
		{"flat config", "attempt:\n  hmac_key: " + hmacKey + "\n", "non-empty configuration map"},
		{"scalar", "attempt: " + hmacKey + "\n", "map of named instances"},
		{"list", "attempt: [admin, user]\n", "map of named instances"},
		{"null instance", "attempt:\n  admin:\n", `initialize attempt "admin"`},
		{"empty instance", "attempt:\n  admin: {}\n", `initialize attempt "admin"`},
		{"scalar instance", "attempt:\n  admin: " + hmacKey + "\n", `initialize attempt "admin"`},
		{"invalid name", "attempt:\n" + namedAttemptConfig("bad!name", hmacKey, ""), "name must match"},
		{"digit first", "attempt:\n" + namedAttemptConfig("1admin", hmacKey, ""), "name must match"},
		{"long name", "attempt:\n" + namedAttemptConfig(strings.Repeat("a", 65), hmacKey, ""), "name must match"},
		{"unknown field", "attempt:\n" + valid + "    failu re_limit: 10\n", "invalid configuration fields or types"},
		{"wrong hmac_key type", "attempt:\n  admin:\n    hmac_key: [" + hmacKey + "]\n", "invalid configuration fields or types"},
		{"missing hmac_key", "attempt:\n  admin:\n    failure_limit: 5\n    window: 10m\n    lock_duration: 15m\n", "hmac key"},
		{"short hmac_key", "attempt:\n" + namedAttemptConfig("admin", "short", ""), "hmac key"},
		{"invalid prefix", "attempt:\n" + valid + "    key_prefix: invalid\n", "redis hash tag"},
		{"duplicate prefix", "attempt:\n" + valid + "    key_prefix: ' shared:{attempt} '\n" + namedAttemptConfig("user", hmacKey, "    key_prefix: 'shared:{attempt}'\n"), `key_prefix conflicts with instance "admin"`},
		{"generated prefix conflict", "attempt:\n" + valid + namedAttemptConfig("user", hmacKey, "    key_prefix: 'aurora:auth:attempt:{admin}'\n"), `key_prefix conflicts with instance "admin"`},
		{"invalid second instance", "attempt:\n" + valid + namedAttemptConfig("user", "short", ""), `initialize attempt "user"`},
		{"attempt_ttl > window", "attempt:\n  admin:\n    hmac_key: " + hmacKey + "\n    failure_limit: 5\n    window: 10m\n    lock_duration: 15m\n    attempt_ttl: 15m\n", "attempt ttl must not exceed window"},
		{"lock_duration < window", "attempt:\n  admin:\n    hmac_key: " + hmacKey + "\n    failure_limit: 5\n    window: 10m\n    lock_duration: 5m\n    attempt_ttl: 5m\n", "lock duration should be at least window"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle, err := NewLifecycle(writeLifecycleAttemptConfig(t, "127.0.0.1:6379", tc.config))
			require.Nil(t, lifecycle)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
			require.NotContains(t, err.Error(), hmacKey)
		})
	}
	t.Run("redis required", func(t *testing.T) {
		lifecycle, err := NewLifecycle(writeLifecycleAttemptConfig(t, "", "attempt:\n"+valid))
		require.Nil(t, lifecycle)
		require.EqualError(t, err, "initialize attempt: redis must be configured")
	})
}

func TestLifecycleAttemptStateIsolation(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(fmt.Sprintf("custom_prefix=%t", custom), func(t *testing.T) {
			mr := miniredis.RunT(t)
			ctx := context.Background()
			config := "attempt:\n"
			prefixes := map[string]string{}
			for _, name := range []string{"admin", "user"} {
				prefixes[name] = "aurora:auth:attempt:{" + name + "}"
				extra := ""
				if custom {
					prefixes[name] = "service:" + name + ":{attempt}"
					extra = fmt.Sprintf("    key_prefix: %q\n", prefixes[name])
				}
				// Use identical HMAC keys deliberately to exercise storage isolation alone.
				config += namedAttemptConfig(name, strings.Repeat("k", 32), extra)
			}
			lifecycle, err := NewLifecycle(writeLifecycleAttemptConfig(t, mr.Addr(), config))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lifecycle.Close(ctx)) })
			admin, err := lifecycle.Attempt("admin")
			require.NoError(t, err)
			user, err := lifecycle.Attempt("user")
			require.NoError(t, err)
			accountID := "same-account"
			// Fail admin 3 times
			for i := 0; i < 3; i++ {
				adminTry, adminStatus, err := admin.Begin(ctx, accountID)
				require.NoError(t, err)
				require.False(t, adminStatus.Locked)
				_, err = admin.RecordFailure(ctx, adminTry)
				require.NoError(t, err)
			}
			// Fail user 5 times to lock it
			for i := 0; i < 5; i++ {
				userTry, userStatus, err := user.Begin(ctx, accountID)
				require.NoError(t, err)
				require.False(t, userStatus.Locked)
				_, err = user.RecordFailure(ctx, userTry)
				require.NoError(t, err)
			}
			// Admin should still be unlocked (only 3 failures)
			adminTry, adminStatus, err := admin.Begin(ctx, accountID)
			require.NoError(t, err)
			require.False(t, adminStatus.Locked)
			require.Equal(t, 3, adminStatus.FailureCount)
			// User should be locked (5 failures)
			_, userStatus, err := user.Begin(ctx, accountID)
			require.NoError(t, err)
			require.True(t, userStatus.Locked)
			require.Equal(t, 5, userStatus.FailureCount)
			// Admin success should not affect user lock
			require.NoError(t, admin.RecordSuccess(ctx, adminTry))
			_, userStatus, err = user.Begin(ctx, accountID)
			require.NoError(t, err)
			require.True(t, userStatus.Locked)
			// User unlock should not affect admin state
			_, err = user.Unlock(ctx, accountID)
			require.NoError(t, err)
			_, userStatus, err = user.Begin(ctx, accountID)
			require.NoError(t, err)
			require.False(t, userStatus.Locked)
			require.Equal(t, 0, userStatus.FailureCount)
			// Admin state should remain unchanged after reset
			adminTry, adminStatus, err = admin.Begin(ctx, accountID)
			require.NoError(t, err)
			require.False(t, adminStatus.Locked)
			require.Equal(t, 0, adminStatus.FailureCount)
		})
	}
}
