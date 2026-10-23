package middleware

import (
	"fmt"
	"io"
	"net/http"

	"github.com/surge-go/aurora"
)

// BodyLimitConfig 定义请求体大小限制中间件的配置
type BodyLimitConfig struct {
	// Limit 请求体最大字节数
	// 超过此限制的请求将被拒绝，返回 413 Payload Too Large
	// 默认: 4MB (4 * 1024 * 1024)
	Limit int64

	// SkipPaths 跳过限制检查的路径前缀
	// 可用于某些特定端点（如文件上传）设置不同的限制
	SkipPaths []string

	// Response 自定义超限时的响应
	// 如果为 nil，将使用默认响应
	Response *BodyLimitResponse
}

// BodyLimitResponse 定义超限时的响应
type BodyLimitResponse struct {
	// StatusCode HTTP 状态码，默认 413
	StatusCode int

	// Message 错误消息
	Message string
}

// DefaultBodyLimitConfig 返回默认的 Body Limit 配置 (4MB)
func DefaultBodyLimitConfig() BodyLimitConfig {
	return BodyLimitConfig{
		Limit:     4 * 1024 * 1024, // 4MB
		SkipPaths: []string{},
		Response:  nil,
	}
}

// limitedReader 包装 io.ReadCloser，限制读取字节数
type limitedReader struct {
	reader    io.ReadCloser
	remaining int64
	limit     int64
}

func (lr *limitedReader) Read(p []byte) (n int, err error) {
	if lr.remaining <= 0 {
		return 0, fmt.Errorf("request body too large, limit is %d bytes", lr.limit)
	}

	if int64(len(p)) > lr.remaining {
		p = p[:lr.remaining]
	}

	n, err = lr.reader.Read(p)
	lr.remaining -= int64(n)

	return n, err
}

func (lr *limitedReader) Close() error {
	return lr.reader.Close()
}

// BodyLimit 返回一个请求体大小限制中间件
//
// 使用示例:
//
//	// 使用默认配置 (4MB)
//	engine.Use(middleware.BodyLimit(middleware.DefaultBodyLimitConfig()))
//
//	// 自定义限制 (10MB)
//	config := middleware.BodyLimitConfig{
//	    Limit: 10 * 1024 * 1024,
//	}
//	engine.Use(middleware.BodyLimit(config))
//
//	// 针对不同路由设置不同限制
//	api := engine.Group("/api")
//	api.Use(middleware.BodyLimit(middleware.BodyLimitConfig{
//	    Limit: 1 * 1024 * 1024, // 1MB
//	}))
//
//	upload := engine.Group("/upload")
//	upload.Use(middleware.BodyLimit(middleware.BodyLimitConfig{
//	    Limit: 100 * 1024 * 1024, // 100MB
//	}))
func BodyLimit(config BodyLimitConfig) aurora.HandlerFunc {
	if config.Limit <= 0 {
		panic("body limit: limit must be greater than 0")
	}

	// 默认响应
	if config.Response == nil {
		config.Response = &BodyLimitResponse{
			StatusCode: http.StatusRequestEntityTooLarge,
			Message:    fmt.Sprintf("Request body too large, maximum allowed is %d bytes", config.Limit),
		}
	}

	return func(c *aurora.Context) {
		// 检查路径是否跳过
		path := c.Request().URL.Path
		for _, skip := range config.SkipPaths {
			if len(path) >= len(skip) && path[:len(skip)] == skip {
				c.Next()
				return
			}
		}

		// 只对有 Body 的方法进行限制
		method := c.Request().Method
		if method != http.MethodPost && method != http.MethodPut &&
			method != http.MethodPatch && method != http.MethodDelete {
			c.Next()
			return
		}

		// 检查 Content-Length 头（快速路径）
		if contentLength := c.Request().ContentLength; contentLength > config.Limit {
			c.JSON(config.Response.StatusCode, map[string]string{
				"error": config.Response.Message,
			})
			c.Abort()
			return
		}

		// 包装 Request.Body 以限制读取
		if c.Request().Body != nil {
			c.Request().Body = &limitedReader{
				reader:    c.Request().Body,
				remaining: config.Limit,
				limit:     config.Limit,
			}
		}

		c.Next()
	}
}
