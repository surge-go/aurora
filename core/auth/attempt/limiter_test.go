package attempt

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newTestLimiter(t *testing.T, configure func(*Config)) (*Limiter, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.HMACKey = strings.Repeat("h", 32)
	cfg.FailureLimit = 3
	cfg.Window = 10 * time.Second
	cfg.LockDuration = 20 * time.Second
	cfg.AttemptTTL = 5 * time.Second
	if configure != nil {
		configure(&cfg)
	}
	limiter, err := New(rdb, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rdb.Close() })
	return limiter, mr, rdb
}

func TestNewValidatesConfiguration(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Config)
	}{
		{name: "missing hmac key", set: func(cfg *Config) { cfg.HMACKey = "short" }},
		{name: "missing hash tag", set: func(cfg *Config) { cfg.KeyPrefix = "aurora:attempt" }},
		{name: "zero failure limit", set: func(cfg *Config) { cfg.FailureLimit = -1 }},
		{name: "zero window", set: func(cfg *Config) { cfg.Window = -time.Second }},
		{name: "excessive failure limit", set: func(cfg *Config) { cfg.FailureLimit = 200 }},
		{name: "attempt ttl exceeds window", set: func(cfg *Config) { cfg.AttemptTTL = 20 * time.Second; cfg.Window = 10 * time.Second }},
		{name: "lock duration less than window", set: func(cfg *Config) { cfg.LockDuration = 5 * time.Second; cfg.Window = 10 * time.Second }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.HMACKey = strings.Repeat("h", 32)
			tc.set(&cfg)
			_, err := New(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}), cfg)
			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}

	var nilClient *redis.Client
	_, err := New(nilClient, DefaultConfig())
	require.ErrorIs(t, err, ErrRedisRequired)
}

func TestFailureWindowLocksAndThenExpires(t *testing.T) {
	limiter, mr, _ := newTestLimiter(t, func(cfg *Config) {
		cfg.FailureLimit = 2
		cfg.Window = 10 * time.Second
		cfg.LockDuration = 10 * time.Second
		cfg.AttemptTTL = 5 * time.Second
	})
	ctx := context.Background()

	first, status, err := limiter.Begin(ctx, "user@example.com")
	require.NoError(t, err)
	require.False(t, status.Locked)
	require.Equal(t, 2, status.RemainingAttempt)
	status, err = limiter.RecordFailure(ctx, first)
	require.NoError(t, err)
	require.Equal(t, 1, status.FailureCount)
	require.Equal(t, 1, status.RemainingAttempt)

	second, status, err := limiter.Begin(ctx, "user@example.com")
	require.NoError(t, err)
	require.Equal(t, 1, status.RemainingAttempt)
	status, err = limiter.RecordFailure(ctx, second)
	require.NoError(t, err)
	require.True(t, status.Locked)
	require.Equal(t, 0, status.RemainingAttempt)
	require.GreaterOrEqual(t, status.RetryAfter, time.Second)

	_, status, err = limiter.Begin(ctx, "user@example.com")
	require.NoError(t, err)
	require.True(t, status.Locked)
	require.GreaterOrEqual(t, status.RetryAfter, time.Second)

	mr.SetTime(time.Now().Add(11 * time.Second))
	third, status, err := limiter.Begin(ctx, "user@example.com")
	require.NoError(t, err)
	require.False(t, status.Locked)
	require.Equal(t, 2, status.RemainingAttempt)
	require.NoError(t, limiter.RecordSuccess(ctx, third))
}

func TestSuccessClearsFailuresAndInvalidatesOtherAttempts(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, nil)
	ctx := context.Background()

	failed, _, err := limiter.Begin(ctx, "user-1")
	require.NoError(t, err)
	_, err = limiter.RecordFailure(ctx, failed)
	require.NoError(t, err)

	success, _, err := limiter.Begin(ctx, "user-1")
	require.NoError(t, err)
	require.NoError(t, limiter.RecordSuccess(ctx, success))
	_, status, err := limiter.Begin(ctx, "user-1")
	require.NoError(t, err)
	require.Equal(t, 0, status.FailureCount)
	require.Equal(t, 3, status.RemainingAttempt)

	_, err = limiter.RecordFailure(ctx, success)
	require.ErrorIs(t, err, ErrAttemptInvalidated)
}

func TestUnlockClearsStateAndInvalidatesInFlightAttempt(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, nil)
	ctx := context.Background()

	inFlight, _, err := limiter.Begin(ctx, "user-2")
	require.NoError(t, err)
	failed, _, err := limiter.Begin(ctx, "user-2")
	require.NoError(t, err)
	_, err = limiter.RecordFailure(ctx, failed)
	require.NoError(t, err)

	result, err := limiter.Unlock(ctx, "user-2")
	require.NoError(t, err)
	require.True(t, result.Cleared)
	_, err = limiter.RecordFailure(ctx, inFlight)
	require.ErrorIs(t, err, ErrAttemptInvalidated)

	_, status, err := limiter.Begin(ctx, "user-2")
	require.NoError(t, err)
	require.False(t, status.Locked)
	require.Equal(t, 3, status.RemainingAttempt)

	result, err = limiter.Unlock(ctx, "user-2")
	require.NoError(t, err)
	require.False(t, result.Cleared)
}

func TestUnlockCanReleaseAutomaticLock(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, func(cfg *Config) { cfg.FailureLimit = 1 })
	ctx := context.Background()

	first, _, err := limiter.Begin(ctx, "locked-user")
	require.NoError(t, err)
	status, err := limiter.RecordFailure(ctx, first)
	require.NoError(t, err)
	require.True(t, status.Locked)

	result, err := limiter.Unlock(ctx, "locked-user")
	require.NoError(t, err)
	require.True(t, result.Cleared)
	_, status, err = limiter.Begin(ctx, "locked-user")
	require.NoError(t, err)
	require.False(t, status.Locked)
}

func TestAccountKeysAreHashedAndInstancesAreIsolated(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()
	base := DefaultConfig()
	base.HMACKey = strings.Repeat("h", 32)
	base.FailureLimit = 1
	adminCfg := base
	adminCfg.KeyPrefix = "aurora:auth:attempt:{admin}"
	userCfg := base
	userCfg.KeyPrefix = "aurora:auth:attempt:{user}"
	admin, err := New(rdb, adminCfg)
	require.NoError(t, err)
	user, err := New(rdb, userCfg)
	require.NoError(t, err)

	ctx := context.Background()
	adminAttempt, _, err := admin.Begin(ctx, "sensitive@example.com")
	require.NoError(t, err)
	status, err := admin.RecordFailure(ctx, adminAttempt)
	require.NoError(t, err)
	require.True(t, status.Locked)
	_, userStatus, err := user.Begin(ctx, "sensitive@example.com")
	require.NoError(t, err)
	require.False(t, userStatus.Locked)

	for _, key := range mr.Keys() {
		require.NotContains(t, key, "sensitive@example.com")
	}
}

func TestRedisFailureIsStable(t *testing.T) {
	limiter, _, rdb := newTestLimiter(t, nil)
	require.NoError(t, rdb.Close())
	_, _, err := limiter.Begin(context.Background(), "user-1")
	require.ErrorIs(t, err, ErrStoreUnavailable)
}

func TestAttemptRequired(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, nil)
	ctx := context.Background()
	_, err := limiter.RecordFailure(ctx, Attempt{})
	require.ErrorIs(t, err, ErrAttemptRequired)
	require.ErrorIs(t, limiter.RecordSuccess(ctx, Attempt{}), ErrAttemptRequired)
	_, _, err = limiter.Begin(ctx, " ")
	require.ErrorIs(t, err, ErrAccountKeyRequired)
	_, _, err = limiter.Begin(ctx, "user\nwith\nnewline")
	require.ErrorIs(t, err, ErrAccountKeyRequired)
	_, _, err = limiter.Begin(ctx, "user\twith\ttab")
	require.ErrorIs(t, err, ErrAccountKeyRequired)
}
