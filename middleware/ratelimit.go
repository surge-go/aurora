package middleware

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/surge-go/aurora"
	"github.com/surge-go/aurora/core/logger"
	"github.com/surge-go/aurora/core/ratelimit"
)

// IPRateLimiter 创建基于 IP 的限流中间件。
//
// 该中间件会：
//  1. 从请求中提取客户端 IP（支持 X-Forwarded-For、X-Real-IP）
//  2. 调用限流器检查是否允许请求通过
//  3. 设置标准的限流响应头（X-RateLimit-*）
//  4. 超限时返回 429 Too Many Requests
//
// 使用示例:
//
//	limiter, _ := ratelimit.New(redisClient, ratelimit.Config{
//	    KeyPrefix: "aurora:login:ip:{limiter}",
//	    HMACKey:   os.Getenv("HMAC_KEY"),
//	    Limit:     10,
//	    Window:    time.Minute,
//	})
//
//	router.POST("/login",
//	    middleware.IPRateLimiter(limiter),
//	    handlers.Login,
//	)
func IPRateLimiter(limiter *ratelimit.Limiter, opts ...IPRateLimitOption) aurora.HandlerFunc {
	cfg := defaultIPRateLimitConfig(limiter)
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(c *aurora.Context) {
		ip := cfg.extractIP(c)
		if ip == "" {
			c.JSON(http.StatusForbidden, map[string]any{"error": "unable to identify client"})
			c.Abort()
			return
		}

		allowed, remaining, retryAfter, err := cfg.limiter.Allow(c.Context(), ip)

		// 限流器故障处理
		if err != nil {
			if cfg.logger != nil {
				cfg.logger.Error("rate limiter failed",
					logger.String("ip", ip),
					logger.String("error", err.Error()),
					logger.Bool("fail_open", cfg.failOpen),
				)
			}

			if cfg.failOpen {
				// 降级策略：放行请求
				c.Next()
				return
			}
			// 默认策略：拒绝请求
			c.JSON(http.StatusServiceUnavailable, map[string]any{"error": "rate limiter unavailable"})
			c.Abort()
			return
		}

		// 设置响应头
		c.SetHeader("X-RateLimit-Limit", strconv.Itoa(cfg.limit))
		c.SetHeader("X-RateLimit-Remaining", strconv.Itoa(remaining))

		if !allowed {
			if cfg.logger != nil {
				cfg.logger.Warn("rate limit exceeded",
					logger.String("ip", ip),
					logger.Int("remaining", remaining),
					logger.Float64("retry_after_sec", retryAfter.Seconds()),
				)
			}

			c.SetHeader("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			c.JSON(http.StatusTooManyRequests, map[string]any{
				"error":       "too many requests",
				"retry_after": retryAfter.Seconds(),
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// ipRateLimitConfig 中间件配置。
type ipRateLimitConfig struct {
	limiter   *ratelimit.Limiter
	logger    *logger.Logger
	extractIP func(c *aurora.Context) string
	failOpen  bool
	limit     int
}

// IPRateLimitOption 配置选项。
type IPRateLimitOption func(*ipRateLimitConfig)

// defaultIPRateLimitConfig 返回默认配置。
func defaultIPRateLimitConfig(limiter *ratelimit.Limiter) ipRateLimitConfig {
	return ipRateLimitConfig{
		limiter:   limiter,
		extractIP: defaultIPExtractor,
		failOpen:  false,
		limit:     limiter.Limit(), // 从 limiter 获取实际限流值
	}
}

// WithIPExtractor 自定义 IP 提取逻辑。
//
// 默认行为：
//  1. 检查 X-Forwarded-For（取第一个 IP）
//  2. 检查 X-Real-IP
//  3. 使用 RemoteAddr
func WithIPExtractor(fn func(c *aurora.Context) string) IPRateLimitOption {
	return func(cfg *ipRateLimitConfig) {
		cfg.extractIP = fn
	}
}

// WithFailOpen 限流器故障时放行请求（默认拒绝）。
//
// 适用场景：
//  - 全局限流中间件（避免 Redis 故障导致服务不可用）
//  - 非关键路由的限流保护
//
// 不适用场景：
//  - 登录接口限流（安全优先）
//  - 敏感操作限流
func WithFailOpen() IPRateLimitOption {
	return func(cfg *ipRateLimitConfig) {
		cfg.failOpen = true
	}
}

// WithRateLimitValue 设置响应头中显示的限流阈值。
//
// 注意：这只影响 X-RateLimit-Limit 响应头的显示值，
// 不影响实际的限流逻辑（由 limiter 控制）。
func WithRateLimitValue(limit int) IPRateLimitOption {
	return func(cfg *ipRateLimitConfig) {
		cfg.limit = limit
	}
}

// WithLogger 设置日志实例。
//
// 日志会记录：
//  - 限流器故障（error 级别）
//  - 达到限流阈值（warn 级别）
//
// 使用示例:
//
//	logger, _ := logger.New(&logger.Config{Level: logger.LevelInfo})
//	router.POST("/api",
//	    middleware.IPRateLimiter(limiter, middleware.WithLogger(logger)),
//	    handlers.API,
//	)
func WithLogger(log *logger.Logger) IPRateLimitOption {
	return func(cfg *ipRateLimitConfig) {
		cfg.logger = log
	}
}

// defaultIPExtractor 默认 IP 提取器。
//
// 优先级：
//  1. X-Forwarded-For（取第一个 IP，通常是真实客户端 IP）
//  2. X-Real-IP
//  3. RemoteAddr
func defaultIPExtractor(c *aurora.Context) string {
	// 1. 检查 X-Forwarded-For
	if xff := c.GetHeader("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For 格式: client, proxy1, proxy2
		// 取第一个 IP（真实客户端 IP）
		if clientIP, _, found := strings.Cut(xff, ","); found {
			return strings.TrimSpace(clientIP)
		}
		return strings.TrimSpace(xff)
	}

	// 2. 检查 X-Real-IP
	if xri := c.GetHeader("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}

	// 3. 使用 RemoteAddr
	if ip, _, err := net.SplitHostPort(c.Request().RemoteAddr); err == nil {
		return ip
	}
	return c.Request().RemoteAddr
}
