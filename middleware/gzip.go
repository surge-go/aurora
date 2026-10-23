package middleware

import (
	"bufio"
	"compress/gzip"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/surge-go/aurora"
)

// GzipConfig 定义 Gzip 压缩中间件的配置
type GzipConfig struct {
	// Level 压缩级别 (gzip.HuffmanOnly=-2 到 gzip.BestCompression=9)
	// 默认: gzip.DefaultCompression (-1)
	Level int

	// MinLength 最小压缩长度（字节）
	// 小于此长度的响应不会被压缩，避免负优化
	// 默认: 1024 (1KB)
	MinLength int

	// ExcludedExtensions 排除的文件扩展名
	// 已经压缩的格式（如图片、视频）不应再次压缩
	ExcludedExtensions []string

	// ExcludedPaths 排除的路径前缀
	// 可用于排除某些 API 端点或静态资源路径
	ExcludedPaths []string

	// ExcludedContentTypes 排除的 Content-Type 前缀
	// 某些内容类型不适合压缩
	ExcludedContentTypes []string
}

// DefaultGzipConfig 返回默认的 Gzip 配置
func DefaultGzipConfig() GzipConfig {
	return GzipConfig{
		Level:              gzip.DefaultCompression,
		MinLength:          1024, // 1KB
		ExcludedExtensions: []string{".png", ".jpg", ".jpeg", ".gif", ".ico", ".mp4", ".avi", ".mp3", ".zip", ".gz", ".pdf"},
		ExcludedPaths:      []string{},
		ExcludedContentTypes: []string{
			"image/",
			"video/",
			"audio/",
			"application/zip",
			"application/x-gzip",
			"application/gzip",
			"application/x-compress",
			"application/x-compressed",
		},
	}
}

// gzipWriter 包装 ResponseWriter 以支持 gzip 压缩
type gzipWriter struct {
	http.ResponseWriter
	gzipWriter     *gzip.Writer
	level          int
	minLength      int
	buffer         []byte
	wroteHeader    bool
	shouldCompress bool
	excludedTypes  []string
	statusCode     int
	bytesWritten   int
}

var gzipWriterPool = sync.Pool{
	New: func() any {
		gz, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression)
		return gz
	},
}

func newGzipWriter(w http.ResponseWriter, level int, minLength int, excludedTypes []string) *gzipWriter {
	gw := &gzipWriter{
		ResponseWriter: w,
		minLength:      minLength,
		shouldCompress: true,
		excludedTypes:  excludedTypes,
		statusCode:     http.StatusOK,
		level:          level,
	}

	// 从池中获取 gzip.Writer 并重置
	gz := gzipWriterPool.Get().(*gzip.Writer)
	// 如果需要不同的压缩级别，重新创建
	if level != gzip.DefaultCompression {
		gz, _ = gzip.NewWriterLevel(w, level)
	} else {
		gz.Reset(w)
	}
	gw.gzipWriter = gz

	return gw
}

// WriteHeader 实现 http.ResponseWriter
func (gw *gzipWriter) WriteHeader(code int) {
	if gw.wroteHeader {
		return
	}
	gw.wroteHeader = true
	gw.statusCode = code

	// 检查 Content-Type 是否应该排除
	contentType := gw.Header().Get("Content-Type")
	if contentType != "" {
		for _, excluded := range gw.excludedTypes {
			if strings.HasPrefix(contentType, excluded) {
				gw.shouldCompress = false
				break
			}
		}
	}

	// 如果决定压缩，设置压缩响应头
	if gw.shouldCompress {
		gw.Header().Set("Content-Encoding", "gzip")
		gw.Header().Set("Vary", "Accept-Encoding")
		gw.Header().Del("Content-Length") // 压缩后长度会变化
	}

	gw.ResponseWriter.WriteHeader(code)
}

// Write 实现 http.ResponseWriter
func (gw *gzipWriter) Write(data []byte) (int, error) {
	if !gw.wroteHeader {
		gw.WriteHeader(http.StatusOK)
	}

	// 如果不应该压缩，直接写入原始数据
	if !gw.shouldCompress {
		n, err := gw.ResponseWriter.Write(data)
		gw.bytesWritten += n
		return n, err
	}

	// 缓冲数据，直到达到最小长度
	if len(gw.buffer)+len(data) < gw.minLength {
		gw.buffer = append(gw.buffer, data...)
		return len(data), nil
	}

	// 如果有缓冲数据，先写入缓冲数据
	if len(gw.buffer) > 0 {
		_, err := gw.gzipWriter.Write(gw.buffer)
		if err != nil {
			return 0, err
		}
		gw.buffer = nil
	}

	// 写入当前数据
	n, err := gw.gzipWriter.Write(data)
	gw.bytesWritten += len(data) // 统计原始字节数
	return n, err
}

// Flush 实现 http.Flusher
func (gw *gzipWriter) Flush() {
	if gw.gzipWriter != nil {
		gw.gzipWriter.Flush()
	}
	if flusher, ok := gw.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Close 关闭 gzip writer
func (gw *gzipWriter) Close() error {
	// 如果还有缓冲数据但小于最小长度，不压缩，直接写入
	if len(gw.buffer) > 0 && len(gw.buffer) < gw.minLength {
		gw.shouldCompress = false
		gw.Header().Del("Content-Encoding")
		gw.Header().Del("Vary")
		_, err := gw.ResponseWriter.Write(gw.buffer)
		gw.buffer = nil
		return err
	}

	// 写入剩余缓冲数据
	if len(gw.buffer) > 0 {
		_, err := gw.gzipWriter.Write(gw.buffer)
		gw.buffer = nil
		if err != nil {
			return err
		}
	}

	// 关闭 gzip writer 并放回池中
	var err error
	if gw.gzipWriter != nil {
		err = gw.gzipWriter.Close()
		gzipWriterPool.Put(gw.gzipWriter)
		gw.gzipWriter = nil
	}

	return err
}

// Status 返回 HTTP 状态码（兼容 gin.ResponseWriter）
func (gw *gzipWriter) Status() int {
	return gw.statusCode
}

// Size 返回已写入的字节数（兼容 gin.ResponseWriter）
func (gw *gzipWriter) Size() int {
	return gw.bytesWritten
}

// Written 返回是否已写入响应（兼容 gin.ResponseWriter）
func (gw *gzipWriter) Written() bool {
	return gw.bytesWritten > 0
}

// WriteString 实现 io.StringWriter（兼容 gin.ResponseWriter）
func (gw *gzipWriter) WriteString(s string) (int, error) {
	return gw.Write([]byte(s))
}

// CloseNotify 实现 http.CloseNotifier（兼容 gin.ResponseWriter）
func (gw *gzipWriter) CloseNotify() <-chan bool {
	if notifier, ok := gw.ResponseWriter.(http.CloseNotifier); ok {
		return notifier.CloseNotify()
	}
	// 如果底层 ResponseWriter 不支持 CloseNotify，返回一个永不关闭的 channel
	return make(chan bool)
}

// Hijack 实现 http.Hijacker（兼容 gin.ResponseWriter）
func (gw *gzipWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := gw.ResponseWriter.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Pusher 实现 http.Pusher（兼容 gin.ResponseWriter，HTTP/2 Server Push）
func (gw *gzipWriter) Pusher() http.Pusher {
	if pusher, ok := gw.ResponseWriter.(http.Pusher); ok {
		return pusher
	}
	return nil
}

// WriteHeaderNow 强制写入 HTTP 头（兼容 gin.ResponseWriter）
func (gw *gzipWriter) WriteHeaderNow() {
	if !gw.wroteHeader {
		gw.WriteHeader(gw.statusCode)
	}
}

// Gzip 返回一个 Gzip 压缩中间件
//
// 使用示例:
//
//	// 使用默认配置
//	engine.Use(middleware.Gzip(middleware.DefaultGzipConfig()))
//
//	// 自定义配置
//	config := middleware.GzipConfig{
//	    Level: gzip.BestSpeed,
//	    MinLength: 512,
//	    ExcludedPaths: []string{"/api/v1/upload"},
//	}
//	engine.Use(middleware.Gzip(config))
func Gzip(config GzipConfig) aurora.HandlerFunc {
	// 验证压缩级别
	if config.Level < gzip.HuffmanOnly || config.Level > gzip.BestCompression {
		panic("gzip: invalid compression level")
	}

	if config.MinLength < 0 {
		config.MinLength = 0
	}

	return func(c *aurora.Context) {
		// 检查客户端是否支持 gzip
		if !strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
			c.Next()
			return
		}

		// 检查路径是否排除
		path := c.Request().URL.Path
		for _, excluded := range config.ExcludedPaths {
			if strings.HasPrefix(path, excluded) {
				c.Next()
				return
			}
		}

		// 检查文件扩展名是否排除
		for _, ext := range config.ExcludedExtensions {
			if strings.HasSuffix(path, ext) {
				c.Next()
				return
			}
		}

		// 包装 ResponseWriter
		originalWriter := c.Writer()
		gw := newGzipWriter(originalWriter, config.Level, config.MinLength, config.ExcludedContentTypes)
		c.SetWriter(gw)

		// 在请求处理完成后关闭 gzip writer
		defer func() {
			gw.Close()
			c.SetWriter(originalWriter) // 恢复原始 writer
		}()

		c.Next()
	}
}
