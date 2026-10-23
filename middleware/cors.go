package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/surge-go/aurora"
)

// CORSConfig 定义 CORS 中间件的配置。
type CORSConfig struct {
	// AllowOrigins 允许的源列表。支持精确匹配和通配符 "*"。
	// 示例: []string{"https://example.com", "https://*.example.com"}
	// 默认: []string{"*"}
	AllowOrigins []string

	// AllowMethods 允许的 HTTP 方法列表。
	// 默认: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	AllowMethods []string

	// AllowHeaders 允许的请求头列表。
	// 默认: []string{"Origin", "Content-Type", "Accept", "Authorization"}
	AllowHeaders []string

	// ExposeHeaders 暴露给客户端的响应头列表。
	// 默认: []string{}
	ExposeHeaders []string

	// AllowCredentials 是否允许携带凭证（Cookie、Authorization 等）。
	// 注意：当设置为 true 时，AllowOrigins 不能包含通配符 "*"。
	// 默认: false
	AllowCredentials bool

	// MaxAge 预检请求（OPTIONS）的缓存时间（秒）。
	// 默认: 12 小时
	MaxAge time.Duration

	// AllowPrivateNetwork 是否允许私有网络请求。
	// 对应 Access-Control-Allow-Private-Network 响应头。
	// 默认: false
	AllowPrivateNetwork bool
}

// DefaultCORSConfig 返回默认的 CORS 配置（安全且宽松）。
func DefaultCORSConfig() CORSConfig {
	return CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
			http.MethodHead,
			http.MethodOptions,
		},
		AllowHeaders: []string{
			"Origin",
			"Content-Type",
			"Accept",
			"Authorization",
			"X-Request-ID",
		},
		ExposeHeaders:        []string{},
		AllowCredentials:     false,
		MaxAge:               12 * time.Hour,
		AllowPrivateNetwork:  false,
	}
}

// CORS 创建一个 CORS 中间件。
//
// 示例:
//
//	// 使用自定义配置
//	engine.Use(middleware.CORS(middleware.CORSConfig{
//	    AllowOrigins:     []string{"https://example.com", "https://app.example.com"},
//	    AllowMethods:     []string{"GET", "POST"},
//	    AllowCredentials: true,
//	}))
//
//	// 使用默认配置
//	engine.Use(middleware.CORS(middleware.DefaultCORSConfig()))
func CORS(config CORSConfig) aurora.Middleware {
	// 填充默认值
	if len(config.AllowOrigins) == 0 {
		config.AllowOrigins = []string{"*"}
	}
	if len(config.AllowMethods) == 0 {
		config.AllowMethods = DefaultCORSConfig().AllowMethods
	}
	if len(config.AllowHeaders) == 0 {
		config.AllowHeaders = DefaultCORSConfig().AllowHeaders
	}
	if config.MaxAge == 0 {
		config.MaxAge = 12 * time.Hour
	}

	// 安全检查：AllowCredentials=true 时不能使用通配符
	if config.AllowCredentials {
		for _, origin := range config.AllowOrigins {
			if origin == "*" {
				panic("CORS: AllowCredentials=true 时，AllowOrigins 不能包含通配符 '*'")
			}
		}
	}

	// 预计算静态响应头
	allowMethods := strings.Join(config.AllowMethods, ", ")
	allowHeaders := strings.Join(config.AllowHeaders, ", ")
	exposeHeaders := ""
	if len(config.ExposeHeaders) > 0 {
		exposeHeaders = strings.Join(config.ExposeHeaders, ", ")
	}
	maxAge := strconv.Itoa(int(config.MaxAge.Seconds()))

	// 判断是否使用通配符（允许所有源）
	allowAll := len(config.AllowOrigins) == 1 && config.AllowOrigins[0] == "*"

	return func(c *aurora.Context) {
		origin := c.GetHeader("Origin")

		// 如果没有 Origin 头，不是跨域请求，跳过 CORS 处理
		if origin == "" {
			c.Next()
			return
		}

		// 检查源是否允许
		allowed := allowAll || isOriginAllowed(origin, config.AllowOrigins)
		if !allowed {
			// 源不允许，不设置 CORS 头，继续处理请求
			c.Next()
			return
		}

		// 设置 Access-Control-Allow-Origin
		if allowAll {
			c.SetHeader("Access-Control-Allow-Origin", "*")
		} else {
			c.SetHeader("Access-Control-Allow-Origin", origin)
			// Vary: Origin 告诉缓存服务器，响应会根据 Origin 头变化
			c.SetHeader("Vary", "Origin")
		}

		// 设置 Access-Control-Allow-Credentials
		if config.AllowCredentials {
			c.SetHeader("Access-Control-Allow-Credentials", "true")
		}

		// 设置 Access-Control-Expose-Headers
		if exposeHeaders != "" {
			c.SetHeader("Access-Control-Expose-Headers", exposeHeaders)
		}

		// 处理预检请求（OPTIONS）
		if c.Request().Method == http.MethodOptions {
			// 设置预检请求的响应头
			c.SetHeader("Access-Control-Allow-Methods", allowMethods)
			c.SetHeader("Access-Control-Allow-Headers", allowHeaders)
			c.SetHeader("Access-Control-Max-Age", maxAge)

			// 处理私有网络请求
			if config.AllowPrivateNetwork {
				if c.GetHeader("Access-Control-Request-Private-Network") == "true" {
					c.SetHeader("Access-Control-Allow-Private-Network", "true")
				}
			}

			// 预检请求直接返回 204 No Content
			c.Status(http.StatusNoContent)
			c.Abort()
			return
		}

		// 实际请求继续处理
		c.Next()
	}
}

// isOriginAllowed 检查源是否在允许列表中。
// 支持精确匹配和简单的通配符匹配（*.example.com）。
func isOriginAllowed(origin string, allowedOrigins []string) bool {
	for _, allowed := range allowedOrigins {
		if allowed == origin {
			return true
		}

		// 支持通配符子域名匹配：https://*.example.com
		if strings.Contains(allowed, "*") {
			if matchWildcard(origin, allowed) {
				return true
			}
		}
	}
	return false
}

// matchWildcard 实现简单的通配符匹配。
// 仅支持 https://*.example.com 格式的子域名通配符。
func matchWildcard(origin, pattern string) bool {
	// 只支持一个 * 通配符
	if strings.Count(pattern, "*") != 1 {
		return false
	}

	// 分割模式为前缀和后缀
	parts := strings.SplitN(pattern, "*", 2)
	prefix, suffix := parts[0], parts[1]

	// 检查前缀和后缀是否匹配
	return strings.HasPrefix(origin, prefix) && strings.HasSuffix(origin, suffix) &&
		len(origin) >= len(prefix)+len(suffix)
}
