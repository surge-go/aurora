package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/surge-go/aurora"
	"github.com/surge-go/aurora/core/ratelimit"
)

func newTestRateLimiter(t *testing.T, limit int, window time.Duration) (*ratelimit.Limiter, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	cfg := ratelimit.Config{
		KeyPrefix: "aurora:test:{limiter}",
		HMACKey:   strings.Repeat("h", 32),
		Limit:     limit,
		Window:    window,
	}

	limiter, err := ratelimit.New(rdb, cfg)
	require.NoError(t, err)

	t.Cleanup(func() { _ = rdb.Close() })
	return limiter, mr
}

func TestIPRateLimiter_AllowedRequest(t *testing.T) {
	limiter, _ := newTestRateLimiter(t, 5, 10*time.Second)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(IPRateLimiter(limiter))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]any{"status": "ok"})
	})

	// 第 1 次请求应该通过
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "5", w.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "4", w.Header().Get("X-RateLimit-Remaining"))
	assert.Contains(t, w.Body.String(), `"status":"ok"`)
}

func TestIPRateLimiter_ExceedsLimit(t *testing.T) {
	limiter, _ := newTestRateLimiter(t, 2, 10*time.Second)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(IPRateLimiter(limiter))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]any{"status": "ok"})
	})

	// 用完配额
	for range 2 {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// 第 3 次请求应该被拒绝
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "2", w.Header().Get("X-RateLimit-Limit"))
	assert.Equal(t, "0", w.Header().Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, w.Header().Get("Retry-After"))
	assert.Contains(t, w.Body.String(), "too many requests")
}

func TestIPRateLimiter_DifferentIPs(t *testing.T) {
	limiter, _ := newTestRateLimiter(t, 2, 10*time.Second)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(IPRateLimiter(limiter))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]any{"status": "ok"})
	})

	// IP1 用完配额
	for range 2 {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	}

	// IP1 第 3 次请求被拒绝
	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "192.168.1.1:12345"
	w1 := httptest.NewRecorder()
	engine.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusTooManyRequests, w1.Code)

	// IP2 仍然可以请求
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "192.168.1.2:12345"
	w2 := httptest.NewRecorder()
	engine.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "1", w2.Header().Get("X-RateLimit-Remaining"))
}

func TestIPRateLimiter_XForwardedFor(t *testing.T) {
	limiter, _ := newTestRateLimiter(t, 5, 10*time.Second)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(IPRateLimiter(limiter))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]any{"status": "ok"})
	})

	// 使用 X-Forwarded-For
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.1, 198.51.100.1")
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	// 再次请求相同的 X-Forwarded-For（应该共享限流状态）
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.Header.Set("X-Forwarded-For", "203.0.113.1, 198.51.100.1")
	req2.RemoteAddr = "192.168.1.2:12345" // 不同的 RemoteAddr
	w2 := httptest.NewRecorder()
	engine.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "3", w2.Header().Get("X-RateLimit-Remaining"), "should share rate limit state")
}

func TestIPRateLimiter_XRealIP(t *testing.T) {
	limiter, _ := newTestRateLimiter(t, 5, 10*time.Second)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(IPRateLimiter(limiter))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]any{"status": "ok"})
	})

	// 使用 X-Real-IP
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Real-IP", "203.0.113.1")
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "4", w.Header().Get("X-RateLimit-Remaining"))
}

func TestIPRateLimiter_CustomExtractor(t *testing.T) {
	limiter, _ := newTestRateLimiter(t, 5, 10*time.Second)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	// 自定义提取器：从查询参数提取用户 ID
	engine.Use(IPRateLimiter(limiter, WithIPExtractor(func(c *aurora.Context) string {
		return c.Query("user_id")
	})))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]any{"status": "ok"})
	})

	// user_id=123 的请求
	req := httptest.NewRequest("GET", "/test?user_id=123", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "4", w.Header().Get("X-RateLimit-Remaining"))

	// user_id=456 的请求（独立限流）
	req2 := httptest.NewRequest("GET", "/test?user_id=456", nil)
	w2 := httptest.NewRecorder()
	engine.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)
	assert.Equal(t, "4", w2.Header().Get("X-RateLimit-Remaining"))
}

func TestIPRateLimiter_EmptyIP(t *testing.T) {
	limiter, _ := newTestRateLimiter(t, 5, 10*time.Second)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	// 自定义提取器：返回空字符串
	engine.Use(IPRateLimiter(limiter, WithIPExtractor(func(c *aurora.Context) string {
		return ""
	})))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]any{"status": "ok"})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Contains(t, w.Body.String(), "unable to identify client")
}

func TestIPRateLimiter_FailOpen(t *testing.T) {
	limiter, mr := newTestRateLimiter(t, 5, 10*time.Second)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(IPRateLimiter(limiter, WithFailOpen()))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]any{"status": "ok"})
	})

	// 停止 miniredis 模拟 Redis 故障
	mr.Close()

	// 配置了 FailOpen，应该放行请求
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"ok"`)
}

func TestIPRateLimiter_FailClosed(t *testing.T) {
	limiter, mr := newTestRateLimiter(t, 5, 10*time.Second)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	engine.Use(IPRateLimiter(limiter)) // 默认 FailClosed
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]any{"status": "ok"})
	})

	// 停止 miniredis 模拟 Redis 故障
	mr.Close()

	// 默认 FailClosed，应该拒绝请求
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), "rate limiter unavailable")
}

func TestIPRateLimiter_WithRateLimitValue(t *testing.T) {
	limiter, _ := newTestRateLimiter(t, 100, 10*time.Second)

	engine, err := aurora.New(aurora.DefaultConfig())
	require.NoError(t, err)
	// 自定义显示值
	engine.Use(IPRateLimiter(limiter, WithRateLimitValue(100)))
	engine.GET("/test", func(c *aurora.Context) {
		c.JSON(http.StatusOK, map[string]any{"status": "ok"})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "100", w.Header().Get("X-RateLimit-Limit"))
}
