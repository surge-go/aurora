package middleware

import (
	"fmt"

	"github.com/surge-go/aurora"
)

// SecurityConfig 定义安全响应头中间件的配置
type SecurityConfig struct {
	// XFrameOptions 设置 X-Frame-Options 头，防止点击劫持攻击
	// 可选值: "DENY", "SAMEORIGIN", "ALLOW-FROM uri"
	// 默认: "DENY"
	XFrameOptions string

	// XContentTypeOptions 设置 X-Content-Type-Options 头，防止 MIME 类型嗅探
	// 默认: "nosniff"
	XContentTypeOptions string

	// XSSProtection 设置 X-XSS-Protection 头（已废弃但仍有用）
	// 可选值: "0" (禁用), "1" (启用), "1; mode=block" (启用并阻止)
	// 默认: "1; mode=block"
	XSSProtection string

	// HSTSMaxAge 设置 Strict-Transport-Security 头的 max-age（秒）
	// 强制浏览器使用 HTTPS，0 表示不设置 HSTS
	// 默认: 31536000 (1 年)
	HSTSMaxAge int

	// HSTSIncludeSubdomains 是否在 HSTS 中包含子域名
	// 默认: false
	HSTSIncludeSubdomains bool

	// HSTSPreload 是否启用 HSTS preload
	// 需要满足条件: max-age >= 1 年, includeSubDomains=true
	// 默认: false
	HSTSPreload bool

	// ContentSecurityPolicy 设置 Content-Security-Policy 头
	// 定义允许加载的资源来源，防止 XSS 和数据注入攻击
	// 示例: "default-src 'self'; script-src 'self' 'unsafe-inline'"
	// 默认: "" (不设置)
	ContentSecurityPolicy string

	// ReferrerPolicy 设置 Referrer-Policy 头
	// 控制 Referer 头的发送策略
	// 可选值: "no-referrer", "no-referrer-when-downgrade",
	//         "same-origin", "origin", "strict-origin",
	//         "origin-when-cross-origin", "strict-origin-when-cross-origin",
	//         "unsafe-url"
	// 默认: "strict-origin-when-cross-origin"
	ReferrerPolicy string

	// PermissionsPolicy 设置 Permissions-Policy 头（原 Feature-Policy）
	// 控制浏览器功能和 API 的使用
	// 示例: "geolocation=(), microphone=()"
	// 默认: "" (不设置)
	PermissionsPolicy string

	// CustomHeaders 自定义安全响应头
	// 可用于添加其他安全相关的头
	CustomHeaders map[string]string
}

// DefaultSecurityConfig 返回默认的安全配置
func DefaultSecurityConfig() SecurityConfig {
	return SecurityConfig{
		XFrameOptions:         "DENY",
		XContentTypeOptions:   "nosniff",
		XSSProtection:         "1; mode=block",
		HSTSMaxAge:            31536000, // 1 年
		HSTSIncludeSubdomains: false,
		HSTSPreload:           false,
		ContentSecurityPolicy: "",
		ReferrerPolicy:        "strict-origin-when-cross-origin",
		PermissionsPolicy:     "",
		CustomHeaders:         nil,
	}
}

// StrictSecurityConfig 返回严格的安全配置（适用于生产环境）
func StrictSecurityConfig() SecurityConfig {
	return SecurityConfig{
		XFrameOptions:         "DENY",
		XContentTypeOptions:   "nosniff",
		XSSProtection:         "1; mode=block",
		HSTSMaxAge:            63072000, // 2 年
		HSTSIncludeSubdomains: true,
		HSTSPreload:           true,
		ContentSecurityPolicy: "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'",
		ReferrerPolicy:        "strict-origin-when-cross-origin",
		PermissionsPolicy:     "geolocation=(), microphone=(), camera=()",
		CustomHeaders:         nil,
	}
}

// Security 返回一个安全响应头中间件
//
// 使用示例:
//
//	// 使用默认配置
//	engine.Use(middleware.Security(middleware.DefaultSecurityConfig()))
//
//	// 使用严格配置
//	engine.Use(middleware.Security(middleware.StrictSecurityConfig()))
//
//	// 自定义配置
//	config := middleware.SecurityConfig{
//	    XFrameOptions: "SAMEORIGIN",
//	    HSTSMaxAge: 31536000,
//	    ContentSecurityPolicy: "default-src 'self'",
//	}
//	engine.Use(middleware.Security(config))
func Security(config SecurityConfig) aurora.HandlerFunc {
	return func(c *aurora.Context) {
		headers := c.Writer().Header()

		// X-Frame-Options
		if config.XFrameOptions != "" {
			headers.Set("X-Frame-Options", config.XFrameOptions)
		}

		// X-Content-Type-Options
		if config.XContentTypeOptions != "" {
			headers.Set("X-Content-Type-Options", config.XContentTypeOptions)
		}

		// X-XSS-Protection
		if config.XSSProtection != "" {
			headers.Set("X-XSS-Protection", config.XSSProtection)
		}

		// Strict-Transport-Security (HSTS)
		if config.HSTSMaxAge > 0 {
			hsts := fmt.Sprintf("max-age=%d", config.HSTSMaxAge)
			if config.HSTSIncludeSubdomains {
				hsts += "; includeSubDomains"
			}
			if config.HSTSPreload {
				hsts += "; preload"
			}
			headers.Set("Strict-Transport-Security", hsts)
		}

		// Content-Security-Policy
		if config.ContentSecurityPolicy != "" {
			headers.Set("Content-Security-Policy", config.ContentSecurityPolicy)
		}

		// Referrer-Policy
		if config.ReferrerPolicy != "" {
			headers.Set("Referrer-Policy", config.ReferrerPolicy)
		}

		// Permissions-Policy
		if config.PermissionsPolicy != "" {
			headers.Set("Permissions-Policy", config.PermissionsPolicy)
		}

		// Custom Headers
		for key, value := range config.CustomHeaders {
			headers.Set(key, value)
		}

		c.Next()
	}
}
