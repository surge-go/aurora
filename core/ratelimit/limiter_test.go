package ratelimit

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestLimiter 创建用于测试的限流器实例。
func newTestLimiter(t *testing.T, configure func(*Config)) (*Limiter, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	cfg := DefaultConfig()
	cfg.HMACKey = strings.Repeat("h", 32)
	if configure != nil {
		configure(&cfg)
	}

	limiter, err := New(rdb, cfg)
	require.NoError(t, err)

	t.Cleanup(func() { _ = rdb.Close() })
	return limiter, mr, rdb
}

func TestNewValidatesConfiguration(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	cases := []struct {
		name string
		set  func(*Config)
		want error
	}{
		{
			name: "missing redis client",
			set: func(cfg *Config) {
				// 故意传 nil redis
			},
			want: ErrRedisRequired,
		},
		{
			name: "missing hash tag in key prefix",
			set: func(cfg *Config) {
				cfg.KeyPrefix = "aurora:ratelimit"
				cfg.HMACKey = strings.Repeat("h", 32)
			},
			want: ErrInvalidConfig,
		},
		{
			name: "hmac key too short",
			set: func(cfg *Config) {
				cfg.HMACKey = "short"
			},
			want: ErrInvalidConfig,
		},
		{
			name: "limit too low",
			set: func(cfg *Config) {
				cfg.Limit = -1
				cfg.HMACKey = strings.Repeat("h", 32)
			},
			want: ErrInvalidConfig,
		},
		{
			name: "limit too high",
			set: func(cfg *Config) {
				cfg.Limit = 100000
				cfg.HMACKey = strings.Repeat("h", 32)
			},
			want: ErrInvalidConfig,
		},
		{
			name: "window too short",
			set: func(cfg *Config) {
				cfg.Window = 500 * time.Millisecond
				cfg.HMACKey = strings.Repeat("h", 32)
			},
			want: ErrInvalidConfig,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			if tc.set != nil {
				tc.set(&cfg)
			}

			var limiter *Limiter
			var err error
			if tc.name == "missing redis client" {
				limiter, err = New(nil, cfg)
			} else {
				limiter, err = New(rdb, cfg)
			}

			if tc.want != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.want)
				assert.Nil(t, limiter)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, limiter)
			}
		})
	}
}

func TestAllow_SuccessWithinLimit(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, func(cfg *Config) {
		cfg.Limit = 5
		cfg.Window = 10 * time.Second
	})

	ctx := context.Background()
	key := "192.168.1.1"

	// 前 5 次请求应该全部通过
	for i := 0; i < 5; i++ {
		allowed, remaining, retryAfter, err := limiter.Allow(ctx, key)
		require.NoError(t, err)
		assert.True(t, allowed, "request %d should be allowed", i+1)
		assert.Equal(t, 4-i, remaining, "remaining should decrease")
		assert.Zero(t, retryAfter)
	}

	// 第 6 次请求应该被拒绝
	allowed, remaining, retryAfter, err := limiter.Allow(ctx, key)
	require.NoError(t, err)
	assert.False(t, allowed, "request 6 should be denied")
	assert.Equal(t, 0, remaining)
	assert.Greater(t, retryAfter, time.Duration(0))
	assert.LessOrEqual(t, retryAfter, 10*time.Second)
}

func TestAllow_MultipleKeysIndependent(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, func(cfg *Config) {
		cfg.Limit = 2
		cfg.Window = 10 * time.Second
	})

	ctx := context.Background()
	key1 := "192.168.1.1"
	key2 := "192.168.1.2"

	// key1 用完配额
	for i := 0; i < 2; i++ {
		allowed, _, _, err := limiter.Allow(ctx, key1)
		require.NoError(t, err)
		assert.True(t, allowed)
	}
	allowed, _, _, err := limiter.Allow(ctx, key1)
	require.NoError(t, err)
	assert.False(t, allowed, "key1 should be denied")

	// key2 仍然可以请求
	allowed, remaining, _, err := limiter.Allow(ctx, key2)
	require.NoError(t, err)
	assert.True(t, allowed, "key2 should be allowed")
	assert.Equal(t, 1, remaining)
}

func TestAllow_SlidingWindowExpiry(t *testing.T) {
	limiter, mr, _ := newTestLimiter(t, func(cfg *Config) {
		cfg.Limit = 3
		cfg.Window = 5 * time.Second
	})

	ctx := context.Background()
	key := "192.168.1.1"

	// 用完配额
	for i := 0; i < 3; i++ {
		allowed, _, _, err := limiter.Allow(ctx, key)
		require.NoError(t, err)
		assert.True(t, allowed)
	}

	// 第 4 次请求被拒绝
	allowed, _, retryAfter, err := limiter.Allow(ctx, key)
	require.NoError(t, err)
	assert.False(t, allowed)
	assert.Greater(t, retryAfter, time.Duration(0))

	// 快进时间，窗口过期
	mr.FastForward(6 * time.Second)

	// 应该恢复配额
	allowed, remaining, _, err := limiter.Allow(ctx, key)
	require.NoError(t, err)
	assert.True(t, allowed, "should allow after window expiry")
	assert.Equal(t, 2, remaining)
}

func TestAllow_ConcurrentRequests(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, func(cfg *Config) {
		cfg.Limit = 50
		cfg.Window = 10 * time.Second
	})

	ctx := context.Background()
	key := "192.168.1.1"

	var wg sync.WaitGroup
	var mu sync.Mutex
	allowedCount := 0
	deniedCount := 0

	// 100 个并发请求，但只有 50 个应该通过
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed, _, _, err := limiter.Allow(ctx, key)
			require.NoError(t, err)
			mu.Lock()
			if allowed {
				allowedCount++
			} else {
				deniedCount++
			}
			mu.Unlock()
		}()
	}

	wg.Wait()

	assert.Equal(t, 50, allowedCount, "exactly 50 requests should be allowed")
	assert.Equal(t, 50, deniedCount, "exactly 50 requests should be denied")
}

func TestAllow_EmptyKey(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, nil)

	ctx := context.Background()

	allowed, _, _, err := limiter.Allow(ctx, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrKeyRequired)
	assert.False(t, allowed)

	allowed, _, _, err = limiter.Allow(ctx, "   ")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrKeyRequired)
	assert.False(t, allowed)
}

func TestReset(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, func(cfg *Config) {
		cfg.Limit = 2
		cfg.Window = 10 * time.Second
	})

	ctx := context.Background()
	key := "192.168.1.1"

	// 用完配额
	for i := 0; i < 2; i++ {
		allowed, _, _, err := limiter.Allow(ctx, key)
		require.NoError(t, err)
		assert.True(t, allowed)
	}

	// 第 3 次请求被拒绝
	allowed, _, _, err := limiter.Allow(ctx, key)
	require.NoError(t, err)
	assert.False(t, allowed)

	// 重置限流状态
	err = limiter.Reset(ctx, key)
	require.NoError(t, err)

	// 应该立即恢复配额
	allowed, remaining, _, err := limiter.Allow(ctx, key)
	require.NoError(t, err)
	assert.True(t, allowed, "should allow after reset")
	assert.Equal(t, 1, remaining)
}

func TestReset_EmptyKey(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, nil)

	ctx := context.Background()

	err := limiter.Reset(ctx, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrKeyRequired)
}

func TestRedisFailure(t *testing.T) {
	limiter, mr, _ := newTestLimiter(t, nil)

	ctx := context.Background()
	key := "192.168.1.1"

	// 停止 miniredis
	mr.Close()

	// 请求应该返回 ErrStoreUnavailable
	allowed, _, _, err := limiter.Allow(ctx, key)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStoreUnavailable)
	assert.False(t, allowed)

	// Reset 也应该返回错误
	err = limiter.Reset(ctx, key)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrStoreUnavailable)
}

func TestHMACKeyPrivacy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HMACKey = strings.Repeat("h", 32)
	cfg.Limit = 5
	cfg.Window = 10 * time.Second

	mr := miniredis.RunT(t)
	defer mr.Close()

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer rdb.Close()

	limiter, err := New(rdb, cfg)
	require.NoError(t, err)

	ctx := context.Background()
	key := "sensitive@example.com"

	// 发起请求
	allowed, _, _, err := limiter.Allow(ctx, key)
	require.NoError(t, err)
	assert.True(t, allowed)

	// 检查 Redis 中是否存储了明文 key
	keys, err := rdb.Keys(ctx, "*").Result()
	require.NoError(t, err)

	for _, redisKey := range keys {
		// Redis key 中不应该包含明文的敏感信息
		assert.NotContains(t, redisKey, "sensitive@example.com",
			"HMAC key should prevent leaking sensitive information in Redis keys")
	}
}

// TestAllow_ContextCancellation 验证 context 取消的处理
func TestAllow_ContextCancellation(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, _, err := limiter.Allow(ctx, "test")
	assert.ErrorIs(t, err, context.Canceled)
}

// TestLimiter_GettersReturnCorrectValues 验证 Limiter 辅助方法
func TestLimiter_GettersReturnCorrectValues(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, func(cfg *Config) {
		cfg.Limit = 100
		cfg.Window = 5 * time.Minute
	})

	assert.Equal(t, 100, limiter.Limit())
	assert.Equal(t, 5*time.Minute, limiter.Window())
}

// TestAllow_SpecialCharactersInKey 验证特殊字符的处理
func TestAllow_SpecialCharactersInKey(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, nil)
	ctx := context.Background()

	testCases := []struct {
		name string
		key  string
	}{
		{"unicode", "用户-123"},
		{"email", "user@example.com"},
		{"spaces", "user with spaces"},
		{"newlines", "user\nwith\nnewlines"},
		{"tabs", "user\twith\ttabs"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			allowed, _, _, err := limiter.Allow(ctx, tc.key)
			assert.NoError(t, err)
			assert.True(t, allowed)
		})
	}
}

// TestReset_NonExistentKey 验证重置不存在的 key 是幂等的
func TestReset_NonExistentKey(t *testing.T) {
	limiter, _, _ := newTestLimiter(t, nil)
	ctx := context.Background()

	// Reset 不存在的 key 应该是幂等的
	err := limiter.Reset(ctx, "nonexistent-key")
	assert.NoError(t, err)

	// 再次 Reset 应该仍然成功
	err = limiter.Reset(ctx, "nonexistent-key")
	assert.NoError(t, err)
}
